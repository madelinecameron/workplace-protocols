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

// Config renders the endpoints that are not limited to projects as an
// `mcpServers` map. Marshal it as JSON under that key to get a .mcp.json file.
//
// A remote with a headers helper gets only its literal headers: the helper
// supplies the rest, so the environment variables those headers read need not
// be set when Claude Code starts.
func Config(endpoints []Endpoint) map[string]Server {
	servers := map[string]Server{}
	for _, e := range endpoints {
		if len(e.Projects) == 0 {
			servers[e.Name] = claudeServer(e)
		}
	}
	return servers
}

// ProjectConfigs renders the endpoints limited to projects as one
// `mcpServers` map per project directory.
func ProjectConfigs(endpoints []Endpoint) map[string]map[string]Server {
	out := map[string]map[string]Server{}
	for _, e := range endpoints {
		for _, p := range e.Projects {
			if out[p] == nil {
				out[p] = map[string]Server{}
			}
			out[p][e.Name] = claudeServer(e)
		}
	}
	return out
}

// ForClaude returns the endpoint as Claude Code gets it: a remote with a
// headers helper keeps only the headers that read no environment variable.
func (e Endpoint) ForClaude() Endpoint {
	if e.Remote() && e.HeadersHelper != "" {
		e.Headers = slices.DeleteFunc(slices.Clone(e.Headers), func(h Field) bool {
			_, ok := h.Value.Literal()
			return !ok
		})
	}
	return e
}

func claudeServer(e Endpoint) Server {
	e = e.ForClaude()
	if e.Remote() {
		return Server{
			Type:          "http",
			URL:           e.URL.String(),
			Headers:       fieldMap(e.Headers),
			HeadersHelper: e.HeadersHelper,
		}
	}
	var args []string
	for _, a := range e.Args {
		args = append(args, a.String())
	}
	return Server{Command: e.Command, Args: args, Env: fieldMap(e.Env)}
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
// headers_helper, and has no per-project servers, so endpoints limited to
// projects are skipped.
func Codex(endpoints []Endpoint) ([]byte, error) {
	var (
		out      bytes.Buffer
		problems []string
	)
	for _, e := range endpoints {
		if len(e.Projects) > 0 {
			continue
		}
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
