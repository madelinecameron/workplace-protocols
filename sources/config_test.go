package sources

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShippedExampleLoadsAndValidates(t *testing.T) {
	config, err := Load(filepath.Join("..", "examples", "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("the shipped example does not validate: %v", err)
	}
	kinds := map[Transport]bool{}
	for _, server := range config.Servers {
		kinds[server.Kind()] = true
	}
	if !kinds[TransportStdio] || !kinds[TransportHTTP] {
		t.Fatal("the shipped example should show both a stdio and an http server")
	}
}

func TestLoadIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "accepts a full config",
			body: `{"servers":[{"id":"operations","command":"operations","args":["mcp"],
				"env":["TRACKER_CONFIG"],"allow":["operations_overview"]}]}`,
		},
		{
			name:    "rejects an unknown field",
			body:    `{"servers":[{"id":"a","command":"a","allow":["t"],"sudo":true}]}`,
			wantErr: "unknown field",
		},
		{
			name:    "rejects an unknown top level field",
			body:    `{"servers":[],"defaults":{}}`,
			wantErr: "unknown field",
		},
		{
			name:    "rejects a second JSON value",
			body:    `{"servers":[]}{"servers":[]}`,
			wantErr: "more than one JSON value",
		},
		{
			name:    "rejects trailing garbage",
			body:    `{"servers":[]} nope`,
			wantErr: "trailing config data",
		},
		{
			name:    "rejects malformed JSON",
			body:    `{"servers":`,
			wantErr: "parsing config",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.body))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadReportsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil || !strings.Contains(err.Error(), "opening config") {
		t.Fatalf("Load error = %v, want it to mention opening config", err)
	}
}

func TestValidate(t *testing.T) {
	stdio := Server{ID: "operations", Command: "operations", Args: []string{"mcp"}, Allow: []string{"operations_overview"}}
	http := Server{
		ID:        "gateway",
		Transport: TransportHTTP,
		URL:       "https://gateway.example/mcp",
		Headers:   map[string]string{"Authorization": "GATEWAY_TOKEN"},
		Allow:     []string{"github_list_repos"},
		Trust:     TrustGrants,
	}

	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{name: "empty config", config: Config{}},
		{name: "stdio and http side by side", config: Config{Servers: []Server{stdio, http}}},
		{
			name:    "stdio server may not set a url",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.URL = "https://x.example/mcp" })}},
			wantErr: "must not set url or headers",
		},
		{
			name: "stdio server may not set headers",
			config: Config{Servers: []Server{withServer(stdio, func(s *Server) {
				s.Headers = map[string]string{"Authorization": "TOKEN"}
			})}},
			wantErr: "must not set url or headers",
		},
		{
			name:    "http server may not set a command",
			config:  Config{Servers: []Server{withServer(http, func(s *Server) { s.Command = "gateway" })}},
			wantErr: "must not set command, args or env",
		},
		{
			name:    "http server may not set args",
			config:  Config{Servers: []Server{withServer(http, func(s *Server) { s.Args = []string{"mcp"} })}},
			wantErr: "must not set command, args or env",
		},
		{
			name:    "http server may not forward environment",
			config:  Config{Servers: []Server{withServer(http, func(s *Server) { s.Env = []string{"TOKEN"} })}},
			wantErr: "must not set command, args or env",
		},
		{
			name:    "stdio server needs a command",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Command = "  " })}},
			wantErr: "has no command",
		},
		{
			name:    "http server needs an absolute url",
			config:  Config{Servers: []Server{withServer(http, func(s *Server) { s.URL = "/mcp" })}},
			wantErr: "absolute http or https url",
		},
		{
			name:    "http server rejects a non http scheme",
			config:  Config{Servers: []Server{withServer(http, func(s *Server) { s.URL = "file:///etc/passwd" })}},
			wantErr: "absolute http or https url",
		},
		{
			name:    "unknown transport",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Transport = "grpc" })}},
			wantErr: "unknown transport",
		},
		{
			name:    "unknown trust",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Trust = "vibes" })}},
			wantErr: "unknown trust",
		},
		{
			name:    "uppercase id",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.ID = "Operations" })}},
			wantErr: "invalid server id",
		},
		{
			name:    "id starting with a dash",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.ID = "-ops" })}},
			wantErr: "invalid server id",
		},
		{
			name:    "id longer than 64 characters",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.ID = strings.Repeat("a", 65) })}},
			wantErr: "invalid server id",
		},
		{
			name:    "reserved id",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.ID = ReservedID })}},
			wantErr: "is reserved",
		},
		{
			name:    "empty allowlist",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Allow = nil })}},
			wantErr: "no explicit tool allowlist",
		},
		{
			name:    "blank allowlist entry",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Allow = []string{" "} })}},
			wantErr: "empty allowed tool",
		},
		{
			name:    "repeated allowlist entry",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Allow = []string{"a", "a"} })}},
			wantErr: `allows "a" twice`,
		},
		{
			name:    "lowercase environment name",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Env = []string{"tracker_config"} })}},
			wantErr: "invalid environment name",
		},
		{
			name:    "environment name starting with a digit",
			config:  Config{Servers: []Server{withServer(stdio, func(s *Server) { s.Env = []string{"1TOKEN"} })}},
			wantErr: "invalid environment name",
		},
		{
			name: "header name with a space",
			config: Config{Servers: []Server{withServer(http, func(s *Server) {
				s.Headers = map[string]string{"Bad Header": "TOKEN"}
			})}},
			wantErr: "invalid header name",
		},
		{
			name: "header carrying a literal secret",
			config: Config{Servers: []Server{withServer(http, func(s *Server) {
				s.Headers = map[string]string{"Authorization": "Bearer hunter2"}
			})}},
			wantErr: "must name an environment variable",
		},
		{
			name:    "duplicate server ids",
			config:  Config{Servers: []Server{stdio, stdio}},
			wantErr: "duplicate server id",
		},
		{
			name: "colliding exposed tool names",
			config: Config{Servers: []Server{
				{ID: "a", Command: "a", Allow: []string{"b__c"}},
				{ID: "a__b", Command: "b", Allow: []string{"c"}},
			}},
			wantErr: "both expose the tool name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func withServer(base Server, mutate func(*Server)) Server {
	copied := base
	copied.Args = slices.Clone(base.Args)
	copied.Env = slices.Clone(base.Env)
	copied.Allow = slices.Clone(base.Allow)
	if base.Headers != nil {
		copied.Headers = map[string]string{}
		for name, value := range base.Headers {
			copied.Headers[name] = value
		}
	}
	mutate(&copied)
	return copied
}

func TestDefaults(t *testing.T) {
	bare := Server{ID: "a", Command: "a", Allow: []string{"t"}}
	if got := bare.Kind(); got != TransportStdio {
		t.Errorf("Kind() = %q, want %q", got, TransportStdio)
	}
	if got := bare.Basis(); got != TrustAnnotations {
		t.Errorf("Basis() = %q, want %q", got, TrustAnnotations)
	}
	explicit := Server{ID: "a", Transport: TransportHTTP, Trust: TrustGrants}
	if got := explicit.Kind(); got != TransportHTTP {
		t.Errorf("Kind() = %q, want %q", got, TransportHTTP)
	}
	if got := explicit.Basis(); got != TrustGrants {
		t.Errorf("Basis() = %q, want %q", got, TrustGrants)
	}
}

func TestExposedName(t *testing.T) {
	tests := []struct {
		server, tool, want string
	}{
		{"operations", "operations_overview", "sources__operations__operations_overview"},
		{"gateway", "github/list.repos", "sources__gateway__github_list_repos"},
	}
	for _, test := range tests {
		if got := ExposedName(test.server, test.tool); got != test.want {
			t.Errorf("ExposedName(%q, %q) = %q, want %q", test.server, test.tool, got, test.want)
		}
	}
}

func TestSelectedEnvironment(t *testing.T) {
	t.Setenv("HOME", "/home/agent")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("TMPDIR", "/tmp/agent")
	t.Setenv("TRACKER_CONFIG", "/etc/tracker.toml")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")

	got := selectedEnvironment([]string{"TRACKER_CONFIG", "TRACKER_CONFIG", "ABSENT_VARIABLE"})
	want := []string{"HOME=/home/agent", "PATH=/usr/bin", "TMPDIR=/tmp/agent", "TRACKER_CONFIG=/etc/tracker.toml"}
	if !slices.Equal(got, want) {
		t.Fatalf("selectedEnvironment() = %v, want %v", got, want)
	}
}
