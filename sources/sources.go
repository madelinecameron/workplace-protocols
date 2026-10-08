// Package sources is a client-side plane for reading from explicitly
// allowlisted, read-only MCP servers.
//
// It exists because an agent that answers questions about the world needs to
// read from many systems, while the authority to reach each one is decided by
// an operator rather than by the agent or by the servers themselves. The config
// file is the whole of that decision: it names every server, every tool that
// server may expose, and the environment variables that carry any credential.
// It holds no secret of its own, so it is safe to commit and review.
//
// Discovery grants nothing. Connecting to a server and listing its tools can
// only confirm that the names an operator already wrote down exist and are
// declared read-only. A server that offers more tools, renames one, or drops
// the read-only declaration does not widen what the plane will call; it makes
// the plane refuse. A server that fails any part of that contract is closed and
// reported as a blind spot, and the remaining servers stay usable, so one
// broken source degrades the agent's sight rather than its whole session.
//
// Blind-spot reasons are written for a model to read. They never carry the
// underlying error text, which can contain a command line, a token or an
// endpoint; the real error stays on [BlindSpot.Err] for logs and operators.
//
// A gap inside a single read is reported the same way. Not all MCP content has
// a faithful text form, so a read can arrive partly unrenderable, and a
// [Result] carries what was left out beside what came through. Those fields are
// structured rather than written into the text because a source controls its
// own text: a notice in prose could be forged by the source it describes, while
// a field on [Result] can only have been set by the runtime.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reasons reported for a server the plane could not bring up. They are fixed
// per transport so that nothing derived from an error, a command line or an
// endpoint can reach a model through them.
const (
	ReasonStdioUnreachable = "could not start or connect to the configured MCP server"
	ReasonHTTPUnreachable  = "the configured HTTP MCP endpoint was unreachable"
)

// A Result is what one read returned, together with everything the runtime
// could not put in Text.
//
// The omissions are structured rather than written into Text on purpose. A
// source controls its own text, so a source could emit something that looks
// like a runtime notice and a reader could not tell the two apart. These fields
// are set by the runtime alone and a source cannot forge them.
type Result struct {
	// Text is the rendered content.
	Text string
	// DroppedKinds names the content kinds that could not be rendered, in the
	// order first seen and without repeats. MCP content is not all text; an
	// image or an embedded resource has no faithful text form, so it is
	// reported here rather than silently discarded.
	DroppedKinds []string
	// StructuredDisplacedText reports that the source returned structured
	// content and text blocks together, and only the structured content was
	// rendered.
	StructuredDisplacedText bool
	// Truncated reports that Text was cut to the configured byte cap. It is a
	// fact about the read, not about the content, which is why it lives here
	// and not in Text.
	Truncated bool
	// OriginalBytes is what Text measured before any cut, so a reader can see
	// how much was elided rather than guess at it. It equals len(Text) when
	// nothing was truncated, and is always set on a successful read.
	OriginalBytes int
}

// Omitted reports whether anything the source returned failed to reach Text.
func (r Result) Omitted() bool {
	return len(r.DroppedKinds) > 0 || r.StructuredDisplacedText || r.Truncated
}

// An Option adjusts how a Set behaves at run time. Options belong to the
// service embedding the plane rather than to the operator, so they are not part
// of the config file an operator reviews.
type Option func(*settings)

type settings struct {
	maxResultBytes   int
	maxResponseBytes int
}

// WithMaxResultBytes caps the bytes a single call may render. Zero, the
// default, means no cap and preserves the behaviour of a Set opened without
// options. Text is cut on a UTF-8 boundary at or below the cap and the Result
// is marked truncated.
//
// The cap bounds what reaches a caller, not what the process holds. The MCP
// client decodes a whole response into memory before this package sees it, so
// an enormous read is already resident by the time the cap applies.
func WithMaxResultBytes(n int) Option {
	return func(s *settings) { s.maxResultBytes = n }
}

// WithMaxHTTPResponseBytes stops an http source's response mid-read once it
// exceeds n bytes, so an enormous reply fails instead of being held whole.
// Zero, the default, means no limit. Unlike [WithMaxResultBytes] this bounds
// what the process holds, because it acts before the response is decoded.
//
// Exceeding it during [Open] closes that server and reports it as a blind spot,
// which is why the failure is worth having: an oversized source degrades to a
// visible gap rather than to a dead process. Exceeding it during a call fails
// that call alone and leaves the source healthy.
//
// The limit is deliberately http-only. A stdio source is a local binary the
// operator installs, invoked with arguments written here, so an
// oversized reply from one is a bug to fix in that binary rather than a
// hostile input to defend against. Reaching the same seam for stdio would mean
// managing the child process by hand instead of letting the SDK own it, and
// that is more surface area than the risk justifies. The asymmetry is a
// judgement, not an oversight.
func WithMaxHTTPResponseBytes(n int) Option {
	return func(s *settings) { s.maxResponseBytes = n }
}

func newSettings(opts []Option) settings {
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// A Tool is one allowlisted read, bound to the session that serves it.
type Tool struct {
	// Name is the namespaced name the tool is offered under.
	Name string
	// ServerID is the config id of the server that serves it.
	ServerID string
	// ToolName is the name the server itself uses.
	ToolName string
	// Description is the server's own description, unframed.
	Description string
	// Schema is the tool's input schema as JSON.
	Schema json.RawMessage
	// Trust is why the tool was accepted as read-only.
	Trust TrustBasis

	call func(context.Context, map[string]any) (Result, error)
}

// Call invokes the tool and renders what it returned.
//
// A tool that reports an error yields the zero Result and that error, with its
// text subject to the same byte cap as a successful read.
func (t Tool) Call(ctx context.Context, args map[string]any) (Result, error) {
	if t.call == nil {
		return Result{}, fmt.Errorf("sources: tool %q is not bound to a session", t.Name)
	}
	return t.call(ctx, args)
}

// A BlindSpot is a configured server the plane could not bring up. Reason is
// safe to show a model; Err holds the underlying failure for logs.
type BlindSpot struct {
	ServerID string
	Reason   string
	Err      error
}

// A Set is the healthy tools and the blind spots produced by one config.
type Set struct {
	tools      []Tool
	byName     map[string]Tool
	sessions   []session
	blindSpots []BlindSpot
}

// Open loads a config from path and brings up every server it names.
//
// It fails only on a config the operator must fix. A server that cannot be
// reached, or that does not satisfy its allowlist, becomes a blind spot and
// leaves the others usable.
func Open(ctx context.Context, path string, opts ...Option) (*Set, error) {
	config, err := Load(path)
	if err != nil {
		return nil, err
	}
	return OpenConfig(ctx, config, opts...)
}

// OpenConfig is [Open] for a config a caller already holds.
func OpenConfig(ctx context.Context, config Config, opts ...Option) (*Set, error) {
	return open(ctx, config, dial, newSettings(opts))
}

// A session is the part of an MCP client session this package uses. Naming it
// keeps the transports and the verification logic independently testable.
type session interface {
	ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

type dialer func(context.Context, Server, settings) (session, error)

func open(ctx context.Context, config Config, dial dialer, conf settings) (*Set, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	set := &Set{byName: map[string]Tool{}}
	for _, server := range config.Servers {
		sess, err := dial(ctx, server, conf)
		if err != nil {
			set.blind(server, unreachable(server), err)
			continue
		}
		tools, err := allowedTools(ctx, server, sess, conf)
		if err != nil {
			_ = sess.Close()
			set.blind(server, reasonFor(server, err), err)
			continue
		}
		set.sessions = append(set.sessions, sess)
		for _, tool := range tools {
			set.tools = append(set.tools, tool)
			set.byName[tool.Name] = tool
		}
	}
	return set, nil
}

func (s *Set) blind(server Server, reason string, err error) {
	s.blindSpots = append(s.blindSpots, BlindSpot{ServerID: server.ID, Reason: reason, Err: err})
}

// Tools returns the healthy tools in config order.
func (s *Set) Tools() []Tool {
	if s == nil {
		return nil
	}
	return append([]Tool(nil), s.tools...)
}

// Lookup finds a healthy tool by its exposed name.
func (s *Set) Lookup(name string) (Tool, bool) {
	if s == nil {
		return Tool{}, false
	}
	tool, ok := s.byName[name]
	return tool, ok
}

// Call invokes a healthy tool by its exposed name.
func (s *Set) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	tool, ok := s.Lookup(name)
	if !ok {
		return Result{}, fmt.Errorf("sources: no such tool %q", name)
	}
	return tool.Call(ctx, args)
}

// BlindSpots returns the servers that failed, in config order.
func (s *Set) BlindSpots() []BlindSpot {
	if s == nil {
		return nil
	}
	return append([]BlindSpot(nil), s.blindSpots...)
}

// Close closes every session in reverse order of opening and joins the errors.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	for i := len(s.sessions) - 1; i >= 0; i-- {
		if err := s.sessions[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.sessions = nil
	return errors.Join(errs...)
}

// contractFailure is a server that answered but did not satisfy its allowlist.
// Its reason names only things the operator wrote in the config.
type contractFailure struct {
	reason string
	err    error
}

func (c *contractFailure) Error() string { return c.err.Error() }
func (c *contractFailure) Unwrap() error { return c.err }

func reasonFor(server Server, err error) string {
	var failure *contractFailure
	if errors.As(err, &failure) {
		return failure.reason
	}
	return unreachable(server)
}

func unreachable(server Server) string {
	if server.Kind() == TransportHTTP {
		return ReasonHTTPUnreachable
	}
	return ReasonStdioUnreachable
}

func dial(ctx context.Context, server Server, conf settings) (session, error) {
	transport, err := clientTransport(server, conf)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: ReservedID, Version: moduleVersion()},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	return client.Connect(ctx, transport, nil)
}

// moduleVersion is the version of this module as built into the binary, so a
// server sees the client release that connected to it.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	if info.Main.Path == modulePath {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			return dep.Version
		}
	}
	return "(devel)"
}

const modulePath = "github.com/madelinecameron/workplace-protocols"

func clientTransport(server Server, conf settings) (mcp.Transport, error) {
	if server.Kind() == TransportHTTP {
		headers, err := resolveHeaders(server)
		if err != nil {
			return nil, err
		}
		return &mcp.StreamableClientTransport{
			Endpoint: server.URL,
			HTTPClient: &http.Client{Transport: &headerTransport{
				headers:  headers,
				maxBytes: conf.maxResponseBytes,
			}},
		}, nil
	}
	// No response limit is imposed here. See [WithMaxHTTPResponseBytes] for why
	// the stdio side is left unbounded on purpose.
	return &mcp.CommandTransport{Command: command(server)}, nil
}

// command builds the child process for a stdio server. The context that opened
// the plane is deliberately not attached: the child's lifetime is the Set's,
// and Close is what ends it.
func command(server Server) *exec.Cmd {
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = selectedEnvironment(server.Env)
	return cmd
}

func selectedEnvironment(extra []string) []string {
	names := append([]string{"HOME", "PATH", "TMPDIR"}, extra...)
	seen := map[string]struct{}{}
	selected := make([]string, 0, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		if value, exists := os.LookupEnv(name); exists {
			selected = append(selected, name+"="+value)
		}
	}
	return selected
}

func resolveHeaders(server Server) (http.Header, error) {
	headers := http.Header{}
	for name, variable := range server.Headers {
		value, exists := os.LookupEnv(variable)
		if !exists {
			return nil, fmt.Errorf(
				"sources: server %s header %s names unset environment variable %s",
				server.ID, name, variable,
			)
		}
		headers.Set(name, value)
	}
	return headers, nil
}

// headerTransport adds the resolved headers to every request and, when a limit
// is configured, bounds every response. Cloning keeps the caller's request
// untouched, which matters because the SDK reuses requests across reconnects.
//
// This is the only seam where an oversized reply can be stopped before it is
// held in full: everything above it receives an already-decoded message.
type headerTransport struct {
	base     http.RoundTripper
	headers  http.Header
	maxBytes int
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	for name, values := range t.headers {
		clone.Header[name] = append([]string(nil), values...)
	}
	resp, err := base.RoundTrip(clone)
	if err != nil || t.maxBytes <= 0 {
		return resp, err
	}
	// One byte of headroom so a response of exactly maxBytes is still legal.
	resp.Body = &boundedBody{
		ReadCloser: resp.Body,
		remaining:  int64(t.maxBytes) + 1,
		limit:      t.maxBytes,
	}
	return resp, nil
}

// boundedBody fails a response once it grows past the configured limit. It
// carries no endpoint or header detail, because the error can surface to a
// caller that renders it.
type boundedBody struct {
	io.ReadCloser
	remaining int64
	limit     int
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("sources: http response exceeded the configured %d byte limit", b.limit)
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func allowedTools(ctx context.Context, server Server, sess session, conf settings) ([]Tool, error) {
	discovered, err := discover(ctx, server, sess)
	if err != nil {
		return nil, err
	}
	basis := server.Basis()
	tools := make([]Tool, 0, len(server.Allow))
	for _, name := range server.Allow {
		definition, exists := discovered[name]
		if !exists {
			return nil, &contractFailure{
				reason: fmt.Sprintf("the configured MCP server does not expose the allowlisted tool %q", name),
				err:    fmt.Errorf("sources: %s allows missing tool %q", server.ID, name),
			}
		}
		if basis == TrustAnnotations && (definition.Annotations == nil || !definition.Annotations.ReadOnlyHint) {
			return nil, &contractFailure{
				reason: fmt.Sprintf("the allowlisted tool %q does not declare that it only reads", name),
				err: fmt.Errorf(
					"sources: %s tool %q is allowlisted but does not declare readOnlyHint=true",
					server.ID, name,
				),
			}
		}
		schema, err := json.Marshal(definition.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("sources: schema for %s/%s: %w", server.ID, name, err)
		}
		if string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		toolName := name
		tools = append(tools, Tool{
			Name:        ExposedName(server.ID, name),
			ServerID:    server.ID,
			ToolName:    toolName,
			Description: definition.Description,
			Schema:      schema,
			Trust:       basis,
			call: func(ctx context.Context, args map[string]any) (Result, error) {
				result, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
				if err != nil {
					return Result{}, fmt.Errorf("sources: calling %s/%s: %w", server.ID, toolName, err)
				}
				return render(result, conf.maxResultBytes)
			},
		})
	}
	return tools, nil
}

func discover(ctx context.Context, server Server, sess session) (map[string]*mcp.Tool, error) {
	discovered := map[string]*mcp.Tool{}
	cursor := ""
	for {
		result, err := sess.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("sources: listing %s tools: %w", server.ID, err)
		}
		for _, tool := range result.Tools {
			discovered[tool.Name] = tool
		}
		if result.NextCursor == "" {
			return discovered, nil
		}
		cursor = result.NextCursor
	}
}

func render(result *mcp.CallToolResult, maxBytes int) (Result, error) {
	var (
		rendered Result
		parts    []string
	)
	if result.StructuredContent != nil {
		body, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return Result{}, fmt.Errorf("sources: encoding structured result: %w", err)
		}
		parts = append(parts, string(body))
	}
	for _, content := range result.Content {
		text, isText := content.(*mcp.TextContent)
		switch {
		case isText && result.StructuredContent != nil:
			rendered.StructuredDisplacedText = true
		case isText:
			parts = append(parts, text.Text)
		default:
			rendered.dropped(content)
		}
	}

	joined := strings.Join(parts, "\n")
	if result.IsError {
		if joined == "" {
			joined = "source tool reported an error"
		}
		capped, _ := truncate(joined, maxBytes)
		return Result{}, errors.New(capped)
	}
	rendered.OriginalBytes = len(joined)
	rendered.Text, rendered.Truncated = truncate(joined, maxBytes)
	return rendered, nil
}

func (r *Result) dropped(content mcp.Content) {
	kind := kindOf(content)
	if slices.Contains(r.DroppedKinds, kind) {
		return
	}
	r.DroppedKinds = append(r.DroppedKinds, kind)
}

// kindOf names a content block the way the MCP wire format does, so the name
// means something to a reader. Kinds without a case here are named by their
// own wire "type" field. An unrecognised block still gets reported, which is
// the point: what matters is that a reader learns something was left out.
func kindOf(content mcp.Content) string {
	switch content.(type) {
	case *mcp.TextContent:
		return "text"
	case *mcp.ImageContent:
		return "image"
	case *mcp.AudioContent:
		return "audio"
	case *mcp.ResourceLink:
		return "resource_link"
	case *mcp.EmbeddedResource:
		return "resource"
	}
	body, err := content.MarshalJSON()
	if err != nil {
		return "unknown"
	}
	var wire struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Type == "" {
		return "unknown"
	}
	return wire.Type
}

// truncate cuts text to at most max bytes, backing up to a UTF-8 boundary so
// the result is never left holding a broken rune.
func truncate(text string, max int) (string, bool) {
	if max <= 0 || len(text) <= max {
		return text, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}
