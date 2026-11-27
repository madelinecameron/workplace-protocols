package tools

import (
	"cmp"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/registry/pkg/model"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

// Endpoint is how this machine reaches one MCP server.
type Endpoint struct {
	Name    string
	UseWhen string
	// Projects, when set, limits the endpoint to these directories under the
	// caller's code root.
	Projects []string
	// Node is the tailnet node serving a remote found by its where tag. Nil
	// for a fixed URL and for a package, which runs on this machine.
	Node *tailnet.Node

	// URL, Headers and HeadersHelper are set for a remote.
	URL           Template
	Headers       []Field
	HeadersHelper string

	// Command, Args and Env are set for a package.
	Command string
	Args    []Template
	Env     []Field
}

// Refs lists the environment variables the endpoint still reads, sorted.
func (e Endpoint) Refs() []string {
	seen := map[string]bool{}
	add := func(t Template) {
		for _, p := range t {
			if p.Env != "" {
				seen[p.Env] = true
			}
		}
	}
	add(e.URL)
	for _, f := range append(slices.Clone(e.Headers), e.Env...) {
		add(f.Value)
	}
	for _, a := range e.Args {
		add(a)
	}
	return slices.Sorted(maps.Keys(seen))
}

// Fill replaces each reference to an environment variable that values holds
// with its value. References to variables values lacks are left in place.
func Fill(endpoints []Endpoint, values map[string]string) []Endpoint {
	fill := func(t Template) Template {
		var out Template
		for _, p := range t {
			if v, ok := values[p.Env]; ok && p.Env != "" {
				out = out.appendText(v)
				continue
			}
			if p.Env != "" {
				out = append(out, p)
				continue
			}
			out = out.appendText(p.Text)
		}
		return out
	}
	fields := func(fs []Field) []Field {
		var out []Field
		for _, f := range fs {
			out = append(out, Field{Name: f.Name, Value: fill(f.Value)})
		}
		return out
	}
	out := make([]Endpoint, len(endpoints))
	for i, e := range endpoints {
		e.URL = fill(e.URL)
		e.Headers = fields(e.Headers)
		e.Env = fields(e.Env)
		var args []Template
		for _, a := range e.Args {
			args = append(args, fill(a))
		}
		e.Args = args
		out[i] = e
	}
	return out
}

// Remote reports whether the endpoint is reached over HTTP.
func (e Endpoint) Remote() bool { return e.Command == "" }

// Field is a named header or environment variable.
type Field struct {
	Name  string
	Value Template
}

// Template is a string made of literal text and references to environment
// variables, which the MCP client fills in when it starts the server.
type Template []Part

// Part is either literal Text or a reference to the environment variable Env.
type Part struct {
	Text string
	Env  string
}

// Literal returns the template as a plain string if it references no
// environment variable.
func (t Template) Literal() (string, bool) {
	var b strings.Builder
	for _, p := range t {
		if p.Env != "" {
			return "", false
		}
		b.WriteString(p.Text)
	}
	return b.String(), true
}

// Format renders the template, writing each environment reference with ref.
func (t Template) Format(ref func(env string) string) string {
	var b strings.Builder
	for _, p := range t {
		if p.Env != "" {
			b.WriteString(ref(p.Env))
			continue
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// String renders the template with ${NAME} references.
func (t Template) String() string {
	return t.Format(func(env string) string { return "${" + env + "}" })
}

func literal(s string) Template {
	if s == "" {
		return nil
	}
	return Template{{Text: s}}
}

func (t Template) appendText(s string) Template {
	if s == "" {
		return t
	}
	if n := len(t); n > 0 && t[n-1].Env == "" {
		t[n-1].Text += s
		return t
	}
	return append(t, Part{Text: s})
}

// binder fills {variables} in server.json values: from the value's own
// variables, then {host}, then the entry's env map.
type binder struct {
	env     map[string]string
	host    string
	hostOK  bool
	used    map[string]bool
	missing []string
}

func (b *binder) expand(s string, local map[string]model.Input) Template {
	var t Template
	last := 0
	for _, loc := range urlVariablePattern.FindAllStringSubmatchIndex(s, -1) {
		t = t.appendText(s[last:loc[0]])
		last = loc[1]
		name := s[loc[2]:loc[3]]
		if v, ok := local[name]; ok && cmp.Or(v.Value, v.Default) != "" {
			t = t.appendText(cmp.Or(v.Value, v.Default))
			continue
		}
		if name == HostVariable && b.hostOK {
			t = t.appendText(b.host)
			continue
		}
		if env, ok := b.env[name]; ok {
			b.used[name] = true
			t = append(t, Part{Env: env})
			continue
		}
		if !slices.Contains(b.missing, name) {
			b.missing = append(b.missing, name)
		}
	}
	return t.appendText(s[last:])
}

func (b *binder) field(kind string, kv model.KeyValueInput) (Field, []string) {
	value := cmp.Or(kv.Value, kv.Default)
	if value == "" {
		return Field{}, []string{fmt.Sprintf("%s %s needs a value or default", kind, kv.Name)}
	}
	t := b.expand(value, kv.Variables)
	if _, isLiteral := t.Literal(); kv.IsSecret && isLiteral {
		return Field{}, []string{fmt.Sprintf("%s %s is secret, so its value must come from a {variable} bound in env:, not from the file", kind, kv.Name)}
	}
	return Field{Name: kv.Name, Value: t}, nil
}

func (b *binder) args(args []model.Argument) ([]Template, []string) {
	var (
		out      []Template
		problems []string
	)
	for _, arg := range args {
		value := cmp.Or(arg.Value, arg.Default)
		switch arg.Type {
		case model.ArgumentTypePositional:
			if value == "" {
				problems = append(problems, "positional arguments need a value or default")
				continue
			}
		case model.ArgumentTypeNamed:
			if arg.Name == "" {
				problems = append(problems, "named arguments need a name")
				continue
			}
			out = append(out, literal(arg.Name))
		default:
			problems = append(problems, fmt.Sprintf("argument type %q is unknown", arg.Type))
			continue
		}
		if value != "" {
			out = append(out, b.expand(value, arg.Variables))
		}
	}
	return out, problems
}

// build turns an entry into an endpoint, filling {host} with host when hostOK.
// Validate calls it with a placeholder host to collect problems; Resolve calls
// it with the serving node.
func build(e Entry, host string, hostOK bool) (Endpoint, []string) {
	s := e.Server
	b := &binder{env: e.Env, host: host, hostOK: hostOK, used: map[string]bool{}}
	ep := Endpoint{Name: e.Name, UseWhen: e.UseWhen}
	var problems []string
	switch {
	case len(s.Packages) > 0 && len(s.Remotes) > 0:
		return ep, []string{"declare packages or remotes, not both"}
	case len(s.Packages) > 1 || len(s.Remotes) > 1:
		return ep, []string{"declare exactly one package or remote"}
	case len(s.Packages) == 1:
		problems = buildPackage(&ep, e, s.Packages[0], b)
	case len(s.Remotes) == 1:
		problems = buildRemote(&ep, e, s.Remotes[0], b)
	default:
		return ep, []string{"declares no package or remote"}
	}
	for _, name := range b.missing {
		if name == HostVariable {
			problems = append(problems, "{host} needs a where tag naming the nodes that serve it")
			continue
		}
		problems = append(problems, fmt.Sprintf("{%s} has no value: give it a value or default, or map it to an environment variable in env:", name))
	}
	for _, variable := range slices.Sorted(maps.Keys(e.Env)) {
		if !b.used[variable] {
			problems = append(problems, fmt.Sprintf("env: %s is not used by any {%s} in the server block", variable, variable))
		}
	}
	return ep, problems
}

func buildPackage(ep *Endpoint, e Entry, p model.Package, b *binder) []string {
	var problems []string
	if p.Transport.Type != model.TransportTypeStdio {
		problems = append(problems, fmt.Sprintf("package transport %q is not supported; use %q", p.Transport.Type, model.TransportTypeStdio))
	}
	if p.Identifier == "" {
		problems = append(problems, "package identifier is required")
	}
	if e.HeadersHelper != "" {
		problems = append(problems, "headers_helper only applies to a remote")
	}
	runtimeArgs, argProblems := b.args(p.RuntimeArguments)
	problems = append(problems, argProblems...)
	packageArgs, argProblems := b.args(p.PackageArguments)
	problems = append(problems, argProblems...)

	spec := p.Identifier
	if p.Version != "" {
		spec += "@" + p.Version
	}
	switch p.RegistryType {
	case RegistryTypeFleet:
		if p.RunTimeHint != "" || len(p.RuntimeArguments) > 0 || p.Version != "" {
			problems = append(problems, "a fleet package runs its identifier from PATH; runtimeHint, runtimeArguments and version do not apply")
		}
		ep.Command = p.Identifier
		ep.Args = packageArgs
	case model.RegistryTypeNPM, model.RegistryTypePyPI:
		hint := map[string]string{model.RegistryTypeNPM: "npx", model.RegistryTypePyPI: "uvx"}[p.RegistryType]
		if p.RunTimeHint != "" && p.RunTimeHint != hint {
			problems = append(problems, fmt.Sprintf("%s packages run with %s, not %q", p.RegistryType, hint, p.RunTimeHint))
		}
		if hint == "npx" && len(runtimeArgs) == 0 {
			runtimeArgs = []Template{literal("-y")}
		}
		ep.Command = hint
		ep.Args = append(append(runtimeArgs, literal(spec)), packageArgs...)
	default:
		problems = append(problems, fmt.Sprintf("package registryType %q is not supported; use %s, %s or %s",
			p.RegistryType, RegistryTypeFleet, model.RegistryTypeNPM, model.RegistryTypePyPI))
	}

	for _, kv := range p.EnvironmentVariables {
		if !envNamePattern.MatchString(kv.Name) {
			problems = append(problems, fmt.Sprintf("environment variable name %q must be UPPER_SNAKE", kv.Name))
			continue
		}
		f, fieldProblems := b.field("environment variable", kv)
		problems = append(problems, fieldProblems...)
		if fieldProblems == nil {
			ep.Env = append(ep.Env, f)
		}
	}
	return problems
}

func buildRemote(ep *Endpoint, e Entry, r model.Transport, b *binder) []string {
	var problems []string
	if r.Type != model.TransportTypeStreamableHTTP {
		problems = append(problems, fmt.Sprintf("remote type %q is not supported; use %q", r.Type, model.TransportTypeStreamableHTTP))
	}
	if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
		problems = append(problems, fmt.Sprintf("remote url %q must start with http:// or https://", r.URL))
	}
	if len(e.Where) > 0 && !strings.Contains(r.URL, "{"+HostVariable+"}") {
		problems = append(problems, fmt.Sprintf("remote url %q must contain {%s} when the entry has a where tag", r.URL, HostVariable))
	}
	ep.URL = b.expand(r.URL, r.Variables)
	ep.HeadersHelper = e.HeadersHelper
	for _, kv := range r.Headers {
		if !headerNamePattern.MatchString(kv.Name) {
			problems = append(problems, fmt.Sprintf("header name %q is not valid", kv.Name))
			continue
		}
		f, fieldProblems := b.field("header", kv)
		problems = append(problems, fieldProblems...)
		if fieldProblems == nil {
			ep.Headers = append(ep.Headers, f)
		}
	}
	return problems
}

// Resolve turns the catalog into the endpoints this machine gets. Call
// Validate first; Resolve reports entries it cannot use as unreachable rather
// than repeating every validation rule.
//
// An entry whose os excludes this machine is skipped. A remote with a where
// tag is served by the first online node carrying one, and that node's name
// fills {host}; a remote without one has a fixed URL. A package runs on this
// machine, and is skipped when where excludes this machine, because the
// command will not be installed here.
//
// st may be nil: packages and fixed URLs still resolve and tagged remotes are
// reported as unreachable, so the rest keeps working while Tailscale is down.
func Resolve(c *Catalog, st *tailnet.Status) ([]Endpoint, error) {
	return ResolveFor(c, st, Machine{OS: runtime.GOOS})
}

// Machine describes the machine endpoints are resolved for.
type Machine struct {
	// OS is the operating system as Go names it. Empty means runtime.GOOS.
	OS string
	// Role is the machine's role, matched against an entry's roles. An entry
	// that lists roles is skipped when Role is empty.
	Role string
}

// ResolveFor is Resolve for a stated machine. It also skips entries that are
// disabled or whose roles exclude m.Role.
func ResolveFor(c *Catalog, st *tailnet.Status, m Machine) ([]Endpoint, error) {
	return resolve(c, st, cmp.Or(m.OS, runtime.GOOS), m.Role)
}

func resolve(c *Catalog, st *tailnet.Status, goos, role string) ([]Endpoint, error) {
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

	for _, e := range c.Entries() {
		if e.Server == nil || e.Disabled() ||
			len(e.OS) > 0 && !slices.Contains(e.OS, goos) ||
			len(e.Roles) > 0 && !slices.Contains(e.Roles, role) {
			continue
		}
		remote := len(e.Server.Remotes) > 0
		var node *tailnet.Node
		host := ""
		switch {
		case remote && len(e.Where) > 0:
			node = serveNode(st, e.Where)
			if node == nil {
				unreach = append(unreach, fmt.Sprintf("%s (no online node carries %s)", e.Name, strings.Join(e.Where, ", ")))
				continue
			}
			host = cmp.Or(node.DNSName, node.Host)
		case !remote && !runsHere(e.Where, selfTags):
			continue
		}
		ep, problems := build(e, host, node != nil)
		if len(problems) > 0 {
			unreach = append(unreach, fmt.Sprintf("%s (%s)", e.Name, problems[0]))
			continue
		}
		ep.Node = node
		ep.Projects = e.Projects
		out = append(out, ep)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(unreach) > 0 {
		// Partial results plus an error: a caller that needs every server can
		// fail, and one that takes what it can get still has the rest.
		return out, fmt.Errorf("unreachable: %s", strings.Join(unreach, "; "))
	}
	return out, nil
}

// serveNode picks the first online node carrying one of the `where` tags, in
// tag order and then in tailnet order.
func serveNode(st *tailnet.Status, where []string) *tailnet.Node {
	if st == nil {
		return nil
	}
	for _, tag := range where {
		for _, n := range st.ByTag(tag) {
			if n.Online {
				return n
			}
		}
	}
	return nil
}

// runsHere reports whether an entry constrained to `where` is installed on a
// machine carrying selfTags. An unconstrained entry runs everywhere.
func runsHere(where, selfTags []string) bool {
	if len(where) == 0 {
		return true
	}
	for _, w := range where {
		if slices.Contains(selfTags, w) {
			return true
		}
	}
	return false
}
