package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

const mcpSample = `
toolchain:
  tools:
    - name: tracker
      version: v0.2.1
      server:
        name: io.github.example/tracker
        description: Workstation state
        version: 0.2.1
        packages:
          - {registryType: fleet, identifier: tracker, transport: {type: stdio}}
mcp:
  - name: mailbox
    use_when: Messages between agents.
    env: {token: MAILBOX_TOKEN, session: MAILBOX_SESSION}
    headers_helper: mailbox-headers
    server:
      name: io.github.example/mailbox
      description: Threaded mail
      version: 0.9.0
      remotes:
        - type: streamable-http
          url: https://mailbox.example.ts.net/mcp
          headers:
            - {name: Authorization, value: "Bearer {token}", isSecret: true}
            - {name: X-Session, value: "{session}"}
            - {name: X-Client, value: agent}
  - name: docs
    env: {key: DOCS_API_KEY}
    server:
      name: com.example/docs
      description: Library documentation
      version: 1.0.0
      remotes:
        - type: streamable-http
          url: https://docs.example.com/mcp
          headers: [{name: X-Api-Key, value: "{key}", isSecret: true}]
  - name: browser
    server:
      name: com.example/browser
      description: Browser automation
      version: 0.0.41
      packages:
        - registryType: npm
          identifier: "@example/browser-mcp"
          version: 0.0.41
          transport: {type: stdio}
          packageArguments: [{type: named, name: --headless}]
  - name: xcode
    os: [darwin]
    env: {key: XCODE_KEY}
    server:
      name: com.example/xcode
      description: Xcode builds
      version: 2.0.0
      packages:
        - registryType: pypi
          identifier: xcode-mcp
          transport: {type: stdio}
          environmentVariables:
            - {name: XCODE_KEY, value: "{key}", isSecret: true}
            - {name: XCODE_MODE, value: build}
`

func mcpEndpoints(t *testing.T, goos string) map[string]Endpoint {
	t.Helper()
	c := parse(t, mcpSample)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	eps, err := resolve(c, &tailnet.Status{Self: &tailnet.Node{Host: "laptop"}}, goos)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	byName := map[string]Endpoint{}
	for _, e := range eps {
		byName[e.Name] = e
	}
	return byName
}

func TestEntriesCombineToolServersAndTheMCPList(t *testing.T) {
	var names []string
	for _, e := range parse(t, mcpSample).Entries() {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "tracker,mailbox,docs,browser,xcode" {
		t.Fatalf("entries = %s", got)
	}
}

func TestMCPNameMayNotRepeatAToolName(t *testing.T) {
	doc := "toolchain:\n  tools:\n    - name: docs\n      version: v1\n" + mcpSample[strings.Index(mcpSample, "mcp:"):]
	err := parse(t, doc).Validate()
	if err == nil || !strings.Contains(err.Error(), "name already used") {
		t.Fatalf("error = %v, want a name collision", err)
	}
}

func TestMCPEntryRejectsUnknownFields(t *testing.T) {
	_, err := ParseCatalog([]byte("mcp:\n  - name: a\n    command: a\n"))
	if err == nil || !strings.Contains(err.Error(), `mcp entry has unrecognized field "command"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestOSLimitsWhichMachinesGetAServer(t *testing.T) {
	if _, ok := mcpEndpoints(t, "linux")["xcode"]; ok {
		t.Fatal("a darwin-only server resolved on linux")
	}
	if _, ok := mcpEndpoints(t, "darwin")["xcode"]; !ok {
		t.Fatal("a darwin-only server did not resolve on darwin")
	}
}

func TestPackagesRunThroughTheirRuntime(t *testing.T) {
	eps := mcpEndpoints(t, "darwin")
	if got := eps["browser"]; got.Command != "npx" || joinArgs(got.Args) != "-y @example/browser-mcp@0.0.41 --headless" {
		t.Fatalf("browser = %s %s", got.Command, joinArgs(got.Args))
	}
	if got := eps["xcode"]; got.Command != "uvx" || joinArgs(got.Args) != "xcode-mcp" {
		t.Fatalf("xcode = %s %s", got.Command, joinArgs(got.Args))
	}
}

func TestClaudeConfigReferencesEnvironmentVariables(t *testing.T) {
	cfg := Config([]Endpoint{mcpEndpoints(t, "darwin")["mailbox"], mcpEndpoints(t, "darwin")["xcode"]})
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mailbox":{"type":"http","url":"https://mailbox.example.ts.net/mcp",` +
		`"headers":{"Authorization":"Bearer ${MAILBOX_TOKEN}","X-Client":"agent","X-Session":"${MAILBOX_SESSION}"},` +
		`"headersHelper":"mailbox-headers"},` +
		`"xcode":{"command":"uvx","args":["xcode-mcp"],"env":{"XCODE_KEY":"${XCODE_KEY}","XCODE_MODE":"build"}}}`
	if string(data) != want {
		t.Fatalf("config =\n%s\nwant\n%s", data, want)
	}
}

func TestCodexConfig(t *testing.T) {
	eps := mcpEndpoints(t, "darwin")
	out, err := Codex([]Endpoint{eps["browser"], eps["docs"], eps["mailbox"], eps["xcode"]})
	if err != nil {
		t.Fatalf("Codex: %v", err)
	}
	want := `[mcp_servers.browser]
command = "npx"
args = ["-y", "@example/browser-mcp@0.0.41", "--headless"]

[mcp_servers.docs]
url = "https://docs.example.com/mcp"
env_http_headers = { "X-Api-Key" = "DOCS_API_KEY" }

[mcp_servers.mailbox]
url = "https://mailbox.example.ts.net/mcp"
bearer_token_env_var = "MAILBOX_TOKEN"
http_headers = { "X-Client" = "agent" }
env_http_headers = { "X-Session" = "MAILBOX_SESSION" }

[mcp_servers.xcode]
command = "uvx"
args = ["xcode-mcp"]
env = { "XCODE_MODE" = "build" }
env_vars = ["XCODE_KEY"]
`
	if string(out) != want {
		t.Fatalf("codex =\n%s\nwant\n%s", out, want)
	}
}

func TestCodexLeavesOutWhatItCannotExpress(t *testing.T) {
	ok := Endpoint{Name: "ok", Command: "ok"}
	renamed := Endpoint{Name: "renamed", Command: "x", Env: []Field{{Name: "API_KEY", Value: Template{{Env: "X_KEY"}}}}}
	composite := Endpoint{Name: "composite", URL: literal("https://x/mcp"), Headers: []Field{{Name: "Authorization", Value: Template{{Text: "Token "}, {Env: "X"}}}}}
	out, err := Codex([]Endpoint{ok, renamed, composite})
	if err == nil || !strings.Contains(err.Error(), "renamed") || !strings.Contains(err.Error(), "composite") {
		t.Fatalf("error = %v, want both unexpressible servers named", err)
	}
	if string(out) != "[mcp_servers.ok]\ncommand = \"ok\"\n" {
		t.Fatalf("out = %q, want only the expressible server", out)
	}
}
