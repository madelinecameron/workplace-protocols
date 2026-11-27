package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Server is one entry in the `mcpServers` map that Claude Code reads from
// .mcp.json. Environment references are written as ${NAME}, which Claude Code
// expands when it starts the server.
type Server struct {
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Type          string            `json:"type,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	HeadersHelper string            `json:"headersHelper,omitempty"`
}

// Config renders endpoints as an `mcpServers` map. Marshal it as JSON under
// that key to get a .mcp.json file.
func Config(endpoints []Endpoint) map[string]Server {
	servers := make(map[string]Server, len(endpoints))
	for _, e := range endpoints {
		if e.Remote() {
			servers[e.Name] = Server{
				Type:          "http",
				URL:           e.URL.String(),
				Headers:       fieldMap(e.Headers),
				HeadersHelper: e.HeadersHelper,
			}
			continue
		}
		var args []string
		for _, a := range e.Args {
			args = append(args, a.String())
		}
		servers[e.Name] = Server{Command: e.Command, Args: args, Env: fieldMap(e.Env)}
	}
	return servers
}

func fieldMap(fields []Field) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		out[f.Name] = f.Value.String()
	}
	return out
}

// Codex renders endpoints as `[mcp_servers.<name>]` tables for Codex's
// config.toml.
//
// Codex reads an environment variable only as a whole value: a header can be
// `Bearer {token}` (bearer_token_env_var) or exactly `{token}`
// (env_http_headers), and a stdio server can inherit a variable of the same
// name (env_vars). An endpoint that needs anything else is left out and named
// in the returned error, alongside the TOML for the rest. Codex gets no
// headers_helper.
func Codex(endpoints []Endpoint) ([]byte, error) {
	var (
		out      bytes.Buffer
		problems []string
	)
	for _, e := range endpoints {
		table, err := codexTable(e)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", e.Name, err))
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "[mcp_servers.%s]\n", e.Name)
		out.WriteString(table)
	}
	if len(problems) > 0 {
		return out.Bytes(), fmt.Errorf("left out of the Codex config: %s", strings.Join(problems, "; "))
	}
	return out.Bytes(), nil
}

func codexTable(e Endpoint) (string, error) {
	var b strings.Builder
	line := func(key, value string) { fmt.Fprintf(&b, "%s = %s\n", key, value) }

	if e.Remote() {
		url, ok := e.URL.Literal()
		if !ok {
			return "", errors.New("Codex cannot read an environment variable inside a url")
		}
		line("url", tomlString(url))
		static := map[string]string{}
		fromEnv := map[string]string{}
		for _, h := range e.Headers {
			if v, ok := h.Value.Literal(); ok {
				static[h.Name] = v
				continue
			}
			if env, ok := bearer(h); ok {
				line("bearer_token_env_var", tomlString(env))
				continue
			}
			if len(h.Value) == 1 {
				fromEnv[h.Name] = h.Value[0].Env
				continue
			}
			return "", fmt.Errorf("Codex cannot build header %s from %s", h.Name, h.Value)
		}
		if len(static) > 0 {
			line("http_headers", tomlTable(static))
		}
		if len(fromEnv) > 0 {
			line("env_http_headers", tomlTable(fromEnv))
		}
		return b.String(), nil
	}

	line("command", tomlString(e.Command))
	var args []string
	for _, a := range e.Args {
		v, ok := a.Literal()
		if !ok {
			return "", fmt.Errorf("Codex cannot read an environment variable inside argument %s", a)
		}
		args = append(args, tomlString(v))
	}
	if len(args) > 0 {
		line("args", "["+strings.Join(args, ", ")+"]")
	}
	static := map[string]string{}
	var inherit []string
	for _, f := range e.Env {
		if v, ok := f.Value.Literal(); ok {
			static[f.Name] = v
			continue
		}
		if len(f.Value) == 1 && f.Value[0].Env == f.Name {
			inherit = append(inherit, tomlString(f.Name))
			continue
		}
		return "", fmt.Errorf("Codex can only pass %s through from a variable of the same name, not as %s", f.Name, f.Value)
	}
	if len(static) > 0 {
		line("env", tomlTable(static))
	}
	if len(inherit) > 0 {
		line("env_vars", "["+strings.Join(inherit, ", ")+"]")
	}
	return b.String(), nil
}

func bearer(h Field) (string, bool) {
	if h.Name != "Authorization" || len(h.Value) != 2 || h.Value[0].Text != "Bearer " || h.Value[1].Env == "" {
		return "", false
	}
	return h.Value[1].Env, true
}

// tomlString quotes s as a TOML basic string. JSON string escapes are a
// subset of TOML's.
func tomlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func tomlTable(m map[string]string) string {
	var pairs []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		pairs = append(pairs, tomlString(k)+" = "+tomlString(m[k]))
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}
