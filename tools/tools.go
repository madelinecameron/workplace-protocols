// Package tools reads the `toolchain` section of workplace-protocols.yaml: a
// list of your tools, the version of each, and how an agent reaches its MCP
// server.
//
// Without a shared list, each MCP server is registered by hand in each agent
// on each machine, and those registrations drift apart. Here every tool's MCP
// server is declared once, as a standard MCP Registry server.json under
// `server`. Resolve combines the list with `tailscale status` to find where
// each server is reachable from this machine, and Config renders the result as
// the `mcpServers` JSON that MCP clients such as Claude Code accept.
//
// It does not control who may call what. Declaring a server says where it is,
// not what a caller may do with it; put a gateway or the server's own auth in
// front of anything that needs that.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	apiv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"gopkg.in/yaml.v3"

	"github.com/madelinecameron/workplace-protocols/tailnet"
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
// online node carrying one of the tool's `where` tags, as in
// "http://{host}:8788/mcp".
const HostVariable = "host"

var (
	serverNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)
	urlVariablePattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
)

// Catalog is the parsed `toolchain` section: which tools exist, and at which
// version.
type Catalog struct {
	Channel string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Tools   []Tool `yaml:"tools" json:"tools"`
}

// Tool is one of your tools: its pinned version, the tags of the nodes it runs
// on, and optionally the MCP server it exposes.
type Tool struct {
	Name string `yaml:"name" json:"name"`
	// Version is the version you expect installed. An installer can compare it
	// with what a machine has.
	Version string `yaml:"version" json:"version"`

	// Where limits the tool to nodes carrying one of these ACL tags. Empty
	// means every machine.
	Where []string `yaml:"where,omitempty" json:"where,omitempty"`

	// Server is the tool's MCP server as an MCP Registry server.json. A tool
	// without one is a CLI or a background job that agents do not call
	// directly; it stays in the catalog so its version is still recorded.
	Server *apiv0.ServerJSON `yaml:"-" json:"server,omitempty"`

	// Role, Build and InstallCmd are hints for whatever installs your tools.
	// This package stores them and acts on none of them.

	// Role is a free-form label, such as agent or daemon.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// Build is the main package to compile, such as ./cmd/importer.
	Build string `yaml:"build,omitempty" json:"build,omitempty"`
	// InstallCmd is a command the tool provides to install itself, such as
	// "tracker daemon --install".
	InstallCmd string `yaml:"install_cmd,omitempty" json:"install_cmd,omitempty"`
}

// UnmarshalYAML decodes a tool, rejecting fields it does not define, and reads
// `server` with server.json's own JSON field names, rejecting fields
// server.json does not define. A server without a `$schema` gets the current
// registry schema URL.
func (t *Tool) UnmarshalYAML(node *yaml.Node) error {
	type plain Tool
	var raw struct {
		Tool   plain          `yaml:",inline"`
		Server map[string]any `yaml:"server"`
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if !toolFields[key.Value] {
				return fmt.Errorf("line %d: tool entry has unrecognized field %q", key.Line, key.Value)
			}
		}
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*t = Tool(raw.Tool)
	if raw.Server == nil {
		return nil
	}
	data, err := json.Marshal(raw.Server)
	if err != nil {
		return fmt.Errorf("tool %s: server: %w", t.Name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var server apiv0.ServerJSON
	if err := dec.Decode(&server); err != nil {
		return fmt.Errorf("tool %s: server: %w", t.Name, err)
	}
	if server.Schema == "" {
		server.Schema = model.CurrentSchemaURL
	}
	t.Server = &server
	return nil
}

// toolFields is every key a tool entry may carry: Tool's yaml names plus
// "server".
var toolFields = func() map[string]bool {
	fields := map[string]bool{"server": true}
	typ := reflect.TypeFor[Tool]()
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}()

// Endpoint is a resolved way to reach one tool: the catalog entry joined with
// the concrete node serving it.
type Endpoint struct {
	Tool Tool
	// Node is the tailnet node serving an HTTP endpoint. Nil for stdio, which
	// runs on the caller's own machine.
	Node *tailnet.Node
	// URL is set for an HTTP endpoint.
	URL string
	// Command and Args are set for a stdio endpoint.
	Command string
	Args    []string
}

// ParseCatalog decodes the `toolchain` section out of workplace-protocols.yaml
// bytes and ignores the rest, so it can take discovery.Manifest.Raw.
func ParseCatalog(data []byte) (*Catalog, error) {
	var wrapper struct {
		Toolchain Catalog `yaml:"toolchain"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parse toolchain: %w", err)
	}
	c := wrapper.Toolchain
	if c.Channel == "" {
		c.Channel = DefaultChannel
	}
	return &c, nil
}

// Validate reports every problem at once rather than stopping at the first,
// since the file is edited by hand.
func (c *Catalog) Validate() error {
	var problems []string
	seen := make(map[string]bool, len(c.Tools))

	for i, t := range c.Tools {
		switch {
		case t.Name == "":
			problems = append(problems, fmt.Sprintf("tools[%d]: name required", i))
		case seen[t.Name]:
			problems = append(problems, fmt.Sprintf("tools[%d]: %q listed twice", i, t.Name))
		default:
			seen[t.Name] = true
		}
		if t.Version == "" {
			problems = append(problems, fmt.Sprintf("tools[%d] (%s): version required", i, t.Name))
		}
		for _, w := range t.Where {
			if !strings.HasPrefix(w, "tag:") {
				problems = append(problems, fmt.Sprintf("tools[%d] (%s): where %q must be an ACL tag (tag:...)", i, t.Name, w))
			}
		}
		if t.Server != nil {
			for _, p := range serverProblems(t) {
				problems = append(problems, fmt.Sprintf("tools[%d] (%s): server: %s", i, t.Name, p))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid toolchain:\n    - %s", strings.Join(problems, "\n    - "))
	}
	return nil
}

// serverProblems checks the server.json fields the registry schema requires
// and the subset of server.json that Resolve can turn into an endpoint.
// Anything Resolve would otherwise drop is rejected, so a declared setting is
// never silently ignored.
func serverProblems(t Tool) []string {
	s := t.Server
	var problems []string
	if len(s.Name) < 3 || len(s.Name) > 200 || !serverNamePattern.MatchString(s.Name) {
		problems = append(problems, fmt.Sprintf("name %q must be reverse-DNS with one slash, like io.github.owner/server", s.Name))
	}
	if s.Description == "" || len(s.Description) > 100 {
		problems = append(problems, "description must be 1 to 100 characters")
	}
	if strings.TrimPrefix(s.Version, "v") != strings.TrimPrefix(t.Version, "v") {
		problems = append(problems, fmt.Sprintf("version %q does not match the tool version %q", s.Version, t.Version))
	}

	switch {
	case len(s.Packages) > 0 && len(s.Remotes) > 0:
		problems = append(problems, "declare packages or remotes, not both")
	case len(s.Packages) > 1 || len(s.Remotes) > 1:
		problems = append(problems, "declare exactly one package or remote")
	case len(s.Packages) == 1:
		problems = append(problems, packageProblems(s.Packages[0])...)
	case len(s.Remotes) == 1:
		problems = append(problems, remoteProblems(s.Remotes[0], t.Where)...)
	default:
		problems = append(problems, "declares no package or remote")
	}
	return problems
}

func packageProblems(p model.Package) []string {
	var problems []string
	if p.RegistryType != RegistryTypeFleet {
		problems = append(problems, fmt.Sprintf("package registryType %q is not supported; use %q", p.RegistryType, RegistryTypeFleet))
	}
	if p.Identifier == "" {
		problems = append(problems, "package identifier (the command to run) is required")
	}
	if p.Transport.Type != model.TransportTypeStdio {
		problems = append(problems, fmt.Sprintf("package transport %q is not supported; use %q", p.Transport.Type, model.TransportTypeStdio))
	}
	if len(p.RuntimeArguments) > 0 || len(p.EnvironmentVariables) > 0 || p.RunTimeHint != "" {
		problems = append(problems, "runtimeHint, runtimeArguments and environmentVariables are not supported")
	}
	for _, arg := range p.PackageArguments {
		switch arg.Type {
		case model.ArgumentTypePositional:
			if arg.Value == "" {
				problems = append(problems, "positional package arguments need a value")
			}
		case model.ArgumentTypeNamed:
			if arg.Name == "" {
				problems = append(problems, "named package arguments need a name")
			}
		default:
			problems = append(problems, fmt.Sprintf("package argument type %q is unknown", arg.Type))
		}
	}
	return problems
}

func remoteProblems(r model.Transport, where []string) []string {
	var problems []string
	if r.Type != model.TransportTypeStreamableHTTP {
		problems = append(problems, fmt.Sprintf("remote type %q is not supported; use %q", r.Type, model.TransportTypeStreamableHTTP))
	}
	if len(r.Headers) > 0 {
		problems = append(problems, "remote headers are not supported")
	}
	if !strings.Contains(r.URL, "{"+HostVariable+"}") {
		problems = append(problems, fmt.Sprintf("remote url %q must contain {%s}, filled from the where tag", r.URL, HostVariable))
	}
	if len(where) == 0 {
		problems = append(problems, "a remote needs a where tag naming the node that serves it")
	}
	for _, m := range urlVariablePattern.FindAllStringSubmatch(r.URL, -1) {
		name := m[1]
		if name == HostVariable {
			continue
		}
		if v, ok := r.Variables[name]; !ok || v.Value == "" && v.Default == "" {
			problems = append(problems, fmt.Sprintf("remote url variable {%s} needs a value or default", name))
		}
	}
	return problems
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

// Resolve turns the catalog into the endpoints a caller can actually reach.
// Call Validate first; Resolve reports tools it cannot use as unreachable
// rather than repeating every validation rule.
//
// Tools with no server are skipped. A remote is served by the first online
// node carrying one of its `where` tags, and that node's name fills {host}. A
// package runs on this machine, and is skipped when `where` excludes this
// machine, because the command will not be installed here.
//
// st may be nil: packages still resolve and remotes are reported as
// unreachable, so local tools keep working while Tailscale is down.
func Resolve(c *Catalog, st *tailnet.Status) ([]Endpoint, error) {
	if c == nil {
		return nil, fmt.Errorf("no catalog")
	}
	var (
		out      []Endpoint
		unreach  []string
		selfTags []string
	)
	if st != nil && st.Self != nil {
		selfTags = st.Self.Tags
	}

	for _, t := range c.Tools {
		s := t.Server
		switch {
		case s == nil:
			continue
		case len(s.Remotes) > 0:
			if len(t.Where) == 0 {
				unreach = append(unreach, fmt.Sprintf("%s (remote declares no where tag)", t.Name))
				continue
			}
			node := serveNode(st, t.Where)
			if node == nil {
				unreach = append(unreach, fmt.Sprintf("%s (no online node carries %s)", t.Name, strings.Join(t.Where, ", ")))
				continue
			}
			host := node.DNSName
			if host == "" {
				host = node.Host
			}
			out = append(out, Endpoint{Tool: t, Node: node, URL: expandURL(s.Remotes[0], host)})
		case len(s.Packages) > 0:
			if !runsHere(t.Where, selfTags) {
				continue
			}
			p := s.Packages[0]
			out = append(out, Endpoint{Tool: t, Command: p.Identifier, Args: packageArgs(p.PackageArguments)})
		default:
			unreach = append(unreach, fmt.Sprintf("%s (server declares no package or remote)", t.Name))
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	if len(unreach) > 0 {
		// Partial results plus an error: a caller that needs every tool can
		// fail, and one that takes what it can get still has the rest.
		return out, fmt.Errorf("unreachable: %s", strings.Join(unreach, "; "))
	}
	return out, nil
}

// expandURL fills {host} and every other variable from its value or default.
func expandURL(r model.Transport, host string) string {
	return urlVariablePattern.ReplaceAllStringFunc(r.URL, func(match string) string {
		name := match[1 : len(match)-1]
		if name == HostVariable {
			return host
		}
		if v := r.Variables[name]; v.Value != "" {
			return v.Value
		}
		return r.Variables[name].Default
	})
}

func packageArgs(args []model.Argument) []string {
	var out []string
	for _, arg := range args {
		if arg.Type == model.ArgumentTypeNamed {
			out = append(out, arg.Name)
		}
		if arg.Value != "" {
			out = append(out, arg.Value)
		}
	}
	return out
}

// serveNode picks the first online node carrying one of the `where` tags, in
// tag order and then in tailnet order.
func serveNode(st *tailnet.Status, where []string) *tailnet.Node {
	if st == nil {
		return nil
	}
	var candidates []*tailnet.Node
	for _, tag := range where {
		candidates = append(candidates, st.ByTag(tag)...)
	}
	for _, n := range candidates {
		if n.Online {
			return n
		}
	}
	return nil
}

// runsHere reports whether a tool constrained to `where` is installed on a
// machine carrying selfTags. An unconstrained tool runs everywhere.
func runsHere(where, selfTags []string) bool {
	if len(where) == 0 {
		return true
	}
	for _, w := range where {
		for _, t := range selfTags {
			if w == t {
				return true
			}
		}
	}
	return false
}

// Server is one entry in an MCP client configuration.
type Server struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Type    string   `json:"type,omitempty"`
	URL     string   `json:"url,omitempty"`
}

// Config renders endpoints as the `mcpServers` map MCP clients accept, so an
// agent's server list is generated from the catalog instead of typed by hand.
// Marshal it as JSON under whatever key the client wants.
func Config(endpoints []Endpoint) map[string]Server {
	servers := make(map[string]Server, len(endpoints))
	for _, e := range endpoints {
		if e.URL != "" {
			servers[e.Tool.Name] = Server{Type: "http", URL: e.URL}
			continue
		}
		servers[e.Tool.Name] = Server{Command: e.Command, Args: e.Args}
	}
	return servers
}
