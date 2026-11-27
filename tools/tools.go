// Package tools reads the `toolchain` and `mcp` sections of
// workplace-protocols.yaml and turns them into MCP client configuration.
//
// `toolchain` lists the tools you build and install, with the version of
// each. `mcp` lists the other MCP servers agents should get: servers someone
// else deploys, public remote servers, and packages run with npx or uvx. A
// tool with an MCP server declares it inline under `server`, so its version is
// written once. Every server is a standard MCP Registry server.json.
//
// Resolve works out which servers this machine gets and how it reaches each,
// and Config and Codex render the result for Claude Code and Codex.
//
// Secrets never go in the file. A `{name}` in a URL, header, argument or
// environment value is filled from the server.json variable of that name,
// from {host}, or from the environment variable the entry's `env:` maps it
// to. The renderers emit a reference to that environment variable, and the
// MCP client reads it when it starts the server.
//
// Declaring a server grants no authority. It says where a server is and how
// to connect, not what a caller may do with it.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	apiv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"gopkg.in/yaml.v3"
)

// DefaultChannel is the release channel ParseCatalog sets when the manifest
// names none. This package only records it; an installer that reads the
// catalog can use it to pick builds.
const DefaultChannel = "stable"

// RegistryTypeFleet is the server.json package registryType for a command you
// install on your machines yourself, by any means. The package identifier is
// the command, and it must be on PATH. No standard registryType covers that
// case, so public MCP registries will not accept these entries.
const RegistryTypeFleet = "fleet"

// HostVariable is the remote URL variable Resolve fills with the name of the
// online node carrying one of the entry's `where` tags, as in
// "http://{host}:8788/mcp".
const HostVariable = "host"

var (
	serverNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)
	urlVariablePattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
	entryNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	variablePattern    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	envNamePattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	headerNamePattern  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

var knownOS = []string{"darwin", "freebsd", "linux", "netbsd", "openbsd", "windows"}

// Catalog is the parsed `toolchain` section plus the top-level `mcp` list.
type Catalog struct {
	Channel string  `yaml:"channel,omitempty" json:"channel,omitempty"`
	Tools   []Tool  `yaml:"tools" json:"tools"`
	MCP     []Entry `yaml:"-" json:"mcp,omitempty"`
}

// Tool is one of your tools: its pinned version, the tags of the nodes it runs
// on, and optionally the MCP server it exposes.
type Tool struct {
	Name string `yaml:"name" json:"name"`
	// Version is the version you expect installed. An installer can compare it
	// with what a machine has.
	Version string `yaml:"version" json:"version"`
	// Repo is where the source lives, such as owner/name on GitHub.
	Repo string `yaml:"repo,omitempty" json:"repo,omitempty"`

	// Where limits the tool to nodes carrying one of these ACL tags. Empty
	// means every machine.
	Where []string `yaml:"where,omitempty" json:"where,omitempty"`
	// OS limits the tool to these operating systems, as Go names them
	// (darwin, linux, windows). Empty means every one.
	OS []string `yaml:"os,omitempty" json:"os,omitempty"`

	// Server is the tool's MCP server as an MCP Registry server.json. A tool
	// without one is a CLI or a background job that agents do not call
	// directly; it stays in the catalog so its version is still recorded.
	Server *apiv0.ServerJSON `yaml:"-" json:"server,omitempty"`
	// UseWhen, Roles, Projects, Env and HeadersHelper apply to Server, as on
	// an Entry.
	UseWhen       string            `yaml:"use_when,omitempty" json:"use_when,omitempty"`
	Roles         []string          `yaml:"roles,omitempty" json:"roles,omitempty"`
	Projects      []string          `yaml:"projects,omitempty" json:"projects,omitempty"`
	Env           map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	HeadersHelper string            `yaml:"headers_helper,omitempty" json:"headers_helper,omitempty"`

	// Role, Build, InstallCmd and SHA256 are hints for whatever installs your
	// tools. This package stores them and acts on none of them.

	// Role is a free-form label, such as agent or daemon.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// Build is the main package to compile, such as ./cmd/importer.
	Build string `yaml:"build,omitempty" json:"build,omitempty"`
	// InstallCmd is a command the tool provides to install itself, such as
	// "tracker daemon --install".
	InstallCmd string `yaml:"install_cmd,omitempty" json:"install_cmd,omitempty"`
	// SHA256 maps a platform, such as darwin-arm64, to the expected checksum
	// of that build.
	SHA256 map[string]string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// Entry is an MCP server agents should get that is not one of your tools.
type Entry struct {
	Name string `yaml:"name" json:"name"`
	// UseWhen tells agents when to reach for this server. server.json's own
	// description is capped at 100 characters; this is not.
	UseWhen string `yaml:"use_when,omitempty" json:"use_when,omitempty"`
	// Where, on a remote, is the tags of the nodes serving it, and {host} in
	// its URL becomes the first online one. On a package it limits which
	// machines run it. Empty means a fixed URL, or every machine.
	Where []string `yaml:"where,omitempty" json:"where,omitempty"`
	// OS limits the server to these operating systems.
	OS []string `yaml:"os,omitempty" json:"os,omitempty"`
	// Roles limits the server to machines whose role, as the caller states it
	// in Machine, is one of these. Empty means every machine.
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	// Projects makes the server available only inside these directories,
	// given relative to the caller's code root. Empty means everywhere.
	Projects []string `yaml:"projects,omitempty" json:"projects,omitempty"`
	// Enabled false keeps the entry in the file but gives it to no machine.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Env maps a {variable} used in the server block to the environment
	// variable that holds its value.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// HeadersHelper is a command that prints extra request headers for a
	// remote. Only clients that support one get it.
	HeadersHelper string            `yaml:"headers_helper,omitempty" json:"headers_helper,omitempty"`
	Server        *apiv0.ServerJSON `yaml:"-" json:"server"`
}

// UnmarshalYAML decodes a tool, rejecting fields it does not define.
func (t *Tool) UnmarshalYAML(node *yaml.Node) error {
	type plain Tool
	var p plain
	raw, err := decodeStrict(node, "tool", toolFields, &p)
	if err != nil {
		return err
	}
	*t = Tool(p)
	t.Server, err = parseServer(raw)
	if err != nil {
		return fmt.Errorf("tool %s: server: %w", t.Name, err)
	}
	return nil
}

// UnmarshalYAML decodes an mcp entry, rejecting fields it does not define.
func (e *Entry) UnmarshalYAML(node *yaml.Node) error {
	type plain Entry
	var p plain
	raw, err := decodeStrict(node, "mcp", entryFields, &p)
	if err != nil {
		return err
	}
	*e = Entry(p)
	e.Server, err = parseServer(raw)
	if err != nil {
		return fmt.Errorf("mcp %s: server: %w", e.Name, err)
	}
	return nil
}

var (
	toolFields  = yamlFields[Tool]()
	entryFields = yamlFields[Entry]()
)

func yamlFields[T any]() map[string]bool {
	fields := map[string]bool{"server": true}
	typ := reflect.TypeFor[T]()
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

// decodeStrict decodes node into dst after checking every key is in fields,
// and returns the raw `server` mapping for parseServer.
func decodeStrict(node *yaml.Node, kind string, fields map[string]bool, dst any) (map[string]any, error) {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if !fields[key.Value] {
				return nil, fmt.Errorf("line %d: %s entry has unrecognized field %q", key.Line, kind, key.Value)
			}
		}
	}
	if err := node.Decode(dst); err != nil {
		return nil, err
	}
	var raw struct {
		Server map[string]any `yaml:"server"`
	}
	if err := node.Decode(&raw); err != nil {
		return nil, err
	}
	return raw.Server, nil
}

// parseServer reads a server block with server.json's own JSON field names,
// rejecting fields server.json does not define. A server without a `$schema`
// gets the current registry schema URL.
func parseServer(raw map[string]any) (*apiv0.ServerJSON, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var server apiv0.ServerJSON
	if err := dec.Decode(&server); err != nil {
		return nil, err
	}
	if server.Schema == "" {
		server.Schema = model.CurrentSchemaURL
	}
	return &server, nil
}

// ParseCatalog decodes the `toolchain` and `mcp` sections out of
// workplace-protocols.yaml bytes and ignores the rest, so it can take
// discovery.Manifest.Raw.
func ParseCatalog(data []byte) (*Catalog, error) {
	var wrapper struct {
		Toolchain Catalog `yaml:"toolchain"`
		MCP       []Entry `yaml:"mcp"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parse toolchain: %w", err)
	}
	c := wrapper.Toolchain
	c.MCP = wrapper.MCP
	if c.Channel == "" {
		c.Channel = DefaultChannel
	}
	return &c, nil
}

func (t Tool) entry() Entry {
	return Entry{
		Name:          t.Name,
		UseWhen:       t.UseWhen,
		Where:         t.Where,
		OS:            t.OS,
		Roles:         t.Roles,
		Projects:      t.Projects,
		Env:           t.Env,
		HeadersHelper: t.HeadersHelper,
		Server:        t.Server,
	}
}

// Entries returns every MCP server in the catalog: the tools that declare a
// server, then the `mcp` list.
// Disabled reports whether the entry is parked with enabled: false.
func (e Entry) Disabled() bool { return e.Enabled != nil && !*e.Enabled }

func (c *Catalog) Entries() []Entry {
	var out []Entry
	for _, t := range c.Tools {
		if t.Server != nil {
			out = append(out, t.entry())
		}
	}
	return append(out, c.MCP...)
}

// Validate reports every problem at once rather than stopping at the first,
// since the file is edited by hand.
func (c *Catalog) Validate() error {
	var problems []string
	seen := make(map[string]bool, len(c.Tools))
	servers := make(map[string]bool, len(c.Tools)+len(c.MCP))

	for i, t := range c.Tools {
		at := fmt.Sprintf("tools[%d] (%s)", i, t.Name)
		switch {
		case t.Name == "":
			problems = append(problems, fmt.Sprintf("tools[%d]: name required", i))
		case seen[t.Name]:
			problems = append(problems, fmt.Sprintf("%s: listed twice", at))
		default:
			seen[t.Name] = true
		}
		if t.Version == "" {
			problems = append(problems, at+": version required")
		}
		problems = append(problems, prefix(at, placementProblems(t.Where, t.OS))...)
		if t.Server == nil {
			if t.UseWhen != "" || len(t.Roles) > 0 || len(t.Projects) > 0 || len(t.Env) > 0 || t.HeadersHelper != "" {
				problems = append(problems, at+": use_when, roles, projects, env and headers_helper need a server")
			}
			continue
		}
		servers[t.Name] = true
		problems = append(problems, prefix(at, serverProblems(t.entry(), t.Version))...)
	}

	for i, e := range c.MCP {
		at := fmt.Sprintf("mcp[%d] (%s)", i, e.Name)
		switch {
		case e.Name == "":
			problems = append(problems, fmt.Sprintf("mcp[%d]: name required", i))
		case servers[e.Name]:
			problems = append(problems, fmt.Sprintf("%s: another server already uses this name", at))
		default:
			servers[e.Name] = true
		}
		problems = append(problems, prefix(at, placementProblems(e.Where, e.OS))...)
		if e.Server == nil {
			problems = append(problems, at+": server required")
			continue
		}
		problems = append(problems, prefix(at, serverProblems(e, ""))...)
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid toolchain:\n    - %s", strings.Join(problems, "\n    - "))
	}
	return nil
}

func prefix(at string, problems []string) []string {
	for i, p := range problems {
		problems[i] = at + ": " + p
	}
	return problems
}

func scopeProblems(roles, projects []string) []string {
	var problems []string
	for _, r := range roles {
		if !entryNamePattern.MatchString(r) {
			problems = append(problems, fmt.Sprintf("role %q may use only letters, digits, - and _", r))
		}
	}
	for _, p := range projects {
		if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			problems = append(problems, fmt.Sprintf("project %q must be a clean relative path inside the code root", p))
		}
	}
	return problems
}

func placementProblems(where, goos []string) []string {
	var problems []string
	for _, w := range where {
		if !strings.HasPrefix(w, "tag:") {
			problems = append(problems, fmt.Sprintf("where %q must be an ACL tag (tag:...)", w))
		}
	}
	for _, o := range goos {
		if !slices.Contains(knownOS, o) {
			problems = append(problems, fmt.Sprintf("os %q must be one of %s", o, strings.Join(knownOS, ", ")))
		}
	}
	return problems
}

// serverProblems checks the server.json fields the registry schema requires,
// the entry's own fields, and that build can turn the server into an
// endpoint. Anything build would otherwise drop is rejected, so a declared
// setting is never silently ignored. toolVersion is empty for mcp entries,
// whose versions are not pinned by a tool.
func serverProblems(e Entry, toolVersion string) []string {
	s := e.Server
	problems := scopeProblems(e.Roles, e.Projects)
	if !entryNamePattern.MatchString(e.Name) {
		problems = append(problems, fmt.Sprintf("name %q may only use letters, digits, - and _ when it has a server", e.Name))
	}
	if len(s.Name) < 3 || len(s.Name) > 200 || !serverNamePattern.MatchString(s.Name) {
		problems = append(problems, fmt.Sprintf("server: name %q must be reverse-DNS with one slash, like io.github.owner/server", s.Name))
	}
	if s.Description == "" || len(s.Description) > 100 {
		problems = append(problems, "server: description must be 1 to 100 characters")
	}
	if s.Version == "" {
		problems = append(problems, "server: version required")
	}
	if toolVersion != "" && strings.TrimPrefix(s.Version, "v") != strings.TrimPrefix(toolVersion, "v") {
		problems = append(problems, fmt.Sprintf("server: version %q does not match the tool version %q", s.Version, toolVersion))
	}
	for _, variable := range slices.Sorted(maps.Keys(e.Env)) {
		if !variablePattern.MatchString(variable) {
			problems = append(problems, fmt.Sprintf("env: %q is not a valid variable name", variable))
		}
		if name := e.Env[variable]; !envNamePattern.MatchString(name) {
			problems = append(problems, fmt.Sprintf("env: %s maps to %q, which is not an UPPER_SNAKE environment variable name", variable, name))
		}
	}
	_, buildProblems := build(e, "{"+HostVariable+"}", len(e.Where) > 0)
	return append(problems, prefix("server", buildProblems)...)
}

// Lookup finds a tool by name.
func (c *Catalog) Lookup(name string) (Tool, bool) {
	for _, t := range c.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Names lists every tool in the catalog, sorted, whether or not it speaks MCP.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.Tools))
	for _, t := range c.Tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}
