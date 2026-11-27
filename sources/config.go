package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Transport names the wire protocol used to reach a source server.
type Transport string

const (
	// TransportStdio launches the server as a child process and speaks to it
	// over stdin and stdout. It is the default when a server omits the field.
	TransportStdio Transport = "stdio"
	// TransportHTTP speaks to an already-running server over streamable HTTP.
	TransportHTTP Transport = "http"
)

// TrustBasis records why this package is willing to believe a tool only reads.
type TrustBasis string

const (
	// TrustAnnotations requires the server to declare annotations.readOnlyHint
	// on every allowlisted tool. It is the default.
	TrustAnnotations TrustBasis = "annotations"
	// TrustGrants accepts an allowlisted tool without that annotation, because
	// the server derives its tool list from an operator-reviewed grant set. The
	// basis is recorded on every resulting tool so a caller can show it.
	TrustGrants TrustBasis = "grants"
)

// ReservedID is the one server id a config may not use. Exposed tool names are
// namespaced under it, so a server claiming it could pose as this package.
const ReservedID = "sources"

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	envPattern    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	headerPattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

// Config is the whole operator-reviewable description of what this package may
// reach. It is meant to be committed, so it names environment variables rather
// than holding any secret itself.
type Config struct {
	Servers []Server `json:"servers"`
}

// Server describes one read-only MCP server and the exact tools it may expose.
type Server struct {
	ID        string    `json:"id"`
	Transport Transport `json:"transport,omitempty"`

	// Command and Args launch a stdio server. Env lists the names of
	// environment variables to forward to it, on top of HOME, PATH and TMPDIR.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`

	// URL addresses an http server. Headers maps a header name to the NAME of
	// an environment variable holding its value, never to a literal value.
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Allow is an exact tool-name allowlist and must name at least one tool.
	// Discovery may only confirm these names; it may never add to them.
	Allow []string `json:"allow"`

	Trust TrustBasis `json:"trust,omitempty"`
}

// Kind reports the transport the server will actually use, resolving the
// stdio default for a server that omits the field.
func (s Server) Kind() Transport {
	if s.Transport == "" {
		return TransportStdio
	}
	return s.Transport
}

// Basis reports the trust basis the server will actually use, resolving the
// annotations default for a server that omits the field.
func (s Server) Basis() TrustBasis {
	if s.Trust == "" {
		return TrustAnnotations
	}
	return s.Trust
}

// Load reads a config file strictly: unknown fields are rejected and the file
// must hold exactly one JSON value. It does not validate the contents; call
// [Config.Validate] or hand the result to [OpenConfig].
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("sources: opening config: %w", err)
	}
	defer func() { _ = f.Close() }()

	var config Config
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("sources: parsing config: %w", err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("sources: config contains more than one JSON value")
		}
		return Config{}, fmt.Errorf("sources: trailing config data: %w", err)
	}
	return config, nil
}

// Validate checks everything that can be decided without contacting a server:
// shape, identifiers, transport coherence, and the uniqueness of both server
// ids and the tool names they would expose.
func (c Config) Validate() error {
	servers := map[string]struct{}{}
	exposed := map[string]string{}
	for _, server := range c.Servers {
		if err := server.validate(); err != nil {
			return err
		}
		if _, exists := servers[server.ID]; exists {
			return fmt.Errorf("sources: duplicate server id %q", server.ID)
		}
		servers[server.ID] = struct{}{}
		for _, tool := range server.Allow {
			name := ExposedName(server.ID, tool)
			if owner, exists := exposed[name]; exists {
				return fmt.Errorf(
					"sources: servers %s and %s both expose the tool name %q",
					owner, server.ID, name,
				)
			}
			exposed[name] = server.ID
		}
	}
	return nil
}

func (s Server) validate() error {
	if !idPattern.MatchString(s.ID) {
		return fmt.Errorf("sources: invalid server id %q", s.ID)
	}
	if s.ID == ReservedID {
		return fmt.Errorf("sources: server id %q is reserved", ReservedID)
	}
	switch s.Transport {
	case "", TransportStdio, TransportHTTP:
	default:
		return fmt.Errorf("sources: server %s has unknown transport %q", s.ID, s.Transport)
	}
	switch s.Trust {
	case "", TrustAnnotations, TrustGrants:
	default:
		return fmt.Errorf("sources: server %s has unknown trust %q", s.ID, s.Trust)
	}
	if err := s.validateTransport(); err != nil {
		return err
	}
	return s.validateAllow()
}

func (s Server) validateTransport() error {
	if s.Kind() == TransportHTTP {
		if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
			return fmt.Errorf("sources: http server %s must not set command, args or env", s.ID)
		}
		return s.validateURLAndHeaders()
	}
	if s.URL != "" || len(s.Headers) > 0 {
		return fmt.Errorf("sources: stdio server %s must not set url or headers", s.ID)
	}
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("sources: stdio server %s has no command", s.ID)
	}
	for _, name := range s.Env {
		if !envPattern.MatchString(name) {
			return fmt.Errorf("sources: server %s has invalid environment name %q", s.ID, name)
		}
	}
	return nil
}

func (s Server) validateURLAndHeaders() error {
	parsed, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("sources: http server %s has an unparseable url", s.ID)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("sources: http server %s needs an absolute http or https url", s.ID)
	}
	for name, env := range s.Headers {
		if !headerPattern.MatchString(name) {
			return fmt.Errorf("sources: server %s has invalid header name %q", s.ID, name)
		}
		if !envPattern.MatchString(env) {
			return fmt.Errorf(
				"sources: server %s header %s must name an environment variable, got %q",
				s.ID, name, env,
			)
		}
	}
	return nil
}

func (s Server) validateAllow() error {
	if len(s.Allow) == 0 {
		return fmt.Errorf("sources: server %s has no explicit tool allowlist", s.ID)
	}
	seen := map[string]struct{}{}
	for _, name := range s.Allow {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("sources: server %s has an empty allowed tool", s.ID)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("sources: server %s allows %q twice", s.ID, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ExposedName is the name a tool is offered under, namespaced by server so two
// sources can each publish a tool of the same underlying name.
func ExposedName(server, tool string) string {
	return ReservedID + "__" + safePart(server) + "__" + safePart(tool)
}

func safePart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
