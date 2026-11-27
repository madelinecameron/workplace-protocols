// Command workplace-protocols checks a manifest and renders MCP client
// configuration from it.
//
//	workplace-protocols [-f file] validate
//	workplace-protocols [-f file] render claude|codex
//	workplace-protocols [-f file] servers
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/madelinecameron/workplace-protocols/discovery"
	"github.com/madelinecameron/workplace-protocols/tailnet"
	"github.com/madelinecameron/workplace-protocols/tools"
)

const usage = `usage: workplace-protocols [-f file] <command>

commands:
  validate          check the manifest and report every problem
  render claude     print .mcp.json for Claude Code
  render codex      print [mcp_servers] tables for Codex's config.toml
  servers           list the MCP servers this machine gets, and how it reaches each

The manifest is read from -f, else $%s, else %s.
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

	switch cmd := flags.Arg(0); cmd {
	case "validate":
		_, err := fmt.Fprintf(stdout, "%s is valid: %d tools, %d MCP servers, %d services\n",
			manifest.Source, len(catalog.Tools), len(catalog.Entries()), len(manifest.Services))
		return err
	case "render":
		if flags.NArg() != 2 {
			return errors.New("render needs a client: claude or codex")
		}
		return render(flags.Arg(1), catalog, stdout, stderr)
	case "servers":
		return servers(catalog, stdout, stderr)
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func resolve(catalog *tools.Catalog, stderr io.Writer) []tools.Endpoint {
	st, err := tailnet.Fetch()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "warning:", err)
	}
	endpoints, err := tools.Resolve(catalog, st)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "warning:", err)
	}
	return endpoints
}

func render(client string, catalog *tools.Catalog, stdout, stderr io.Writer) error {
	switch client {
	case "claude":
		endpoints := resolve(catalog, stderr)
		out, err := json.MarshalIndent(map[string]any{"mcpServers": tools.Config(endpoints)}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(out))
		return err
	case "codex":
		out, err := tools.Codex(resolve(catalog, stderr))
		if _, writeErr := stdout.Write(out); writeErr != nil {
			return writeErr
		}
		return err
	default:
		return fmt.Errorf("unknown client %q: use claude or codex", client)
	}
}

func servers(catalog *tools.Catalog, stdout, stderr io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	rows := []string{"NAME\tTRANSPORT\tTARGET"}
	for _, e := range resolve(catalog, stderr) {
		if e.Remote() {
			rows = append(rows, e.Name+"\thttp\t"+e.URL.String())
			continue
		}
		target := e.Command
		for _, a := range e.Args {
			target += " " + a.String()
		}
		rows = append(rows, e.Name+"\tstdio\t"+target)
	}
	if _, err := io.WriteString(w, strings.Join(rows, "\n")+"\n"); err != nil {
		return err
	}
	return w.Flush()
}
