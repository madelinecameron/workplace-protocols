// Command workplace-protocols checks a manifest and renders MCP client
// configuration from it.
//
//	workplace-protocols [-f file] validate
//	workplace-protocols [-f file] [-role r] [-values file] render claude|codex
//	workplace-protocols [-f file] [-role r] servers [-json]
//	workplace-protocols [-f file] list -json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/madelinecameron/workplace-protocols/discovery"
	"github.com/madelinecameron/workplace-protocols/tailnet"
	"github.com/madelinecameron/workplace-protocols/tools"
)

const usage = `usage: workplace-protocols [-f file] [-role r] [-values file] <command>

commands:
  validate          check the manifest and report every problem
  render claude     print .mcp.json for Claude Code; servers limited to
                    projects go under "projects", keyed by directory
  render codex      print [mcp_servers] tables for Codex's config.toml
  servers [-json]   list the MCP servers this machine gets, and how it reaches each
  list -json        print every MCP entry in the manifest, for whatever renders docs

flags:
  -f file       the manifest; else $%s, else %s
  -role r       this machine's role, matched against an entry's roles
  -values file  a JSON object of environment variable values to write into the
                config in place of references; render claude then leaves out
                any server that still needs a variable
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "workplace-protocols:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("workplace-protocols", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprintf(stderr, usage, discovery.EnvFile, discovery.DefaultPath) }
	file := flags.String("f", "", "manifest `file`")
	role := flags.String("role", "", "this machine's `role`")
	valuesFile := flags.String("values", "", "JSON `file` of environment variable values")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return errors.New("no command")
	}

	manifest, err := discovery.Load(*file)
	if err != nil {
		return err
	}
	catalog, err := tools.ParseCatalog(manifest.Raw)
	if err != nil {
		return err
	}
	if err := catalog.Validate(); err != nil {
		return err
	}

	values, err := readValues(*valuesFile)
	if err != nil {
		return err
	}
	machine := tools.Machine{Role: *role}

	switch cmd := flags.Arg(0); cmd {
	case "validate":
		_, err := fmt.Fprintf(stdout, "%s is valid: %d tools, %d MCP servers, %d services\n",
			manifest.Source, len(catalog.Tools), len(catalog.Entries()), len(manifest.Services))
		return err
	case "render":
		if flags.NArg() != 2 {
			return errors.New("render needs a client: claude or codex")
		}
		return render(flags.Arg(1), catalog, machine, values, stdout, stderr)
	case "servers":
		if flags.Arg(1) == "-json" {
			return serversJSON(catalog, machine, stdout, stderr)
		}
		return servers(catalog, machine, stdout, stderr)
	case "list":
		if flags.Arg(1) != "-json" {
			return errors.New("list prints JSON only: list -json")
		}
		return list(catalog, stdout)
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func readValues(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("%s: want a JSON object of strings: %w", path, err)
	}
	return values, nil
}

func resolve(catalog *tools.Catalog, machine tools.Machine, stderr io.Writer) []tools.Endpoint {
	st, err := tailnet.Fetch()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "warning:", err)
	}
	endpoints, err := tools.ResolveFor(catalog, st, machine)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "warning:", err)
	}
	return endpoints
}

func render(client string, catalog *tools.Catalog, machine tools.Machine, values map[string]string, stdout, stderr io.Writer) error {
	switch client {
	case "claude", "codex":
	default:
		return fmt.Errorf("unknown client %q: use claude or codex", client)
	}
	endpoints := resolve(catalog, machine, stderr)
	var missing []string
	if values != nil {
		endpoints = tools.Fill(endpoints, values)
		if client == "claude" {
			endpoints = slices.DeleteFunc(endpoints, func(e tools.Endpoint) bool {
				if refs := e.ForClaude().Refs(); len(refs) > 0 {
					missing = append(missing, fmt.Sprintf("%s (needs %s)", e.Name, strings.Join(refs, ", ")))
					return true
				}
				return false
			})
		}
	}

	if client == "codex" {
		out, err := tools.Codex(endpoints)
		if _, writeErr := stdout.Write(out); writeErr != nil {
			return writeErr
		}
		return err
	}
	doc := map[string]any{"mcpServers": tools.Config(endpoints)}
	if projects := tools.ProjectConfigs(endpoints); len(projects) > 0 {
		byDir := map[string]any{}
		for dir, servers := range projects {
			byDir[dir] = map[string]any{"mcpServers": servers}
		}
		doc["projects"] = byDir
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, string(out)); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("left out of the Claude config: %s", strings.Join(missing, "; "))
	}
	return nil
}

func target(e tools.Endpoint) (transport, target string) {
	if e.Remote() {
		return "http", e.URL.String()
	}
	t := e.Command
	for _, a := range e.Args {
		t += " " + a.String()
	}
	return "stdio", t
}

func servers(catalog *tools.Catalog, machine tools.Machine, stdout, stderr io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	rows := []string{"NAME\tTRANSPORT\tTARGET"}
	for _, e := range resolve(catalog, machine, stderr) {
		transport, t := target(e)
		rows = append(rows, e.Name+"\t"+transport+"\t"+t)
	}
	if _, err := io.WriteString(w, strings.Join(rows, "\n")+"\n"); err != nil {
		return err
	}
	return w.Flush()
}

type serverRow struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Target    string   `json:"target"`
	Needs     []string `json:"needs"`
	Projects  []string `json:"projects,omitempty"`
}

func serversJSON(catalog *tools.Catalog, machine tools.Machine, stdout, stderr io.Writer) error {
	rows := []serverRow{}
	for _, e := range resolve(catalog, machine, stderr) {
		transport, t := target(e)
		rows = append(rows, serverRow{Name: e.Name, Transport: transport, Target: t, Needs: e.Refs(), Projects: e.Projects})
	}
	return writeJSON(stdout, rows)
}

type entryRow struct {
	Name     string   `json:"name"`
	UseWhen  string   `json:"use_when,omitempty"`
	Enabled  bool     `json:"enabled"`
	OS       []string `json:"os,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Where    []string `json:"where,omitempty"`
	Projects []string `json:"projects,omitempty"`
	// Runs is what a package runs or a remote's URL, before variables are
	// filled in.
	Runs string `json:"runs"`
}

func list(catalog *tools.Catalog, stdout io.Writer) error {
	rows := []entryRow{}
	for _, e := range catalog.Entries() {
		row := entryRow{Name: e.Name, UseWhen: e.UseWhen, Enabled: !e.Disabled(), OS: e.OS, Roles: e.Roles, Where: e.Where, Projects: e.Projects}
		switch {
		case len(e.Server.Remotes) > 0:
			row.Runs = e.Server.Remotes[0].URL
		case len(e.Server.Packages) > 0:
			row.Runs = e.Server.Packages[0].Identifier
		}
		rows = append(rows, row)
	}
	return writeJSON(stdout, rows)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
