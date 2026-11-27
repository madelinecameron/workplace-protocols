package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/registry/pkg/model"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

// A manifest with every kind of entry: local servers, a remote server, and a
// catalog-only tool with no server.
const sample = `
tailnet: tail1234.ts.net
toolchain:
  channel: stable
  tools:
    - name: tracker
      version: v0.2.1
      role: agent
      server:
        name: io.github.example/tracker
        description: Workstation state
        version: 0.2.1
        packages:
          - registryType: fleet
            identifier: tracker
            transport: {type: stdio}
            packageArguments:
              - {type: positional, value: mcp}
              - {type: named, name: --profile, value: agents}
    - name: deployer
      version: v0.1.1
      server:
        name: io.github.example/deployer
        description: Deployments
        version: 0.1.1
        packages:
          - registryType: fleet
            identifier: deployer-mcp
            transport: {type: stdio}
    - name: operations
      version: v0.2.0
      role: agent
      where: [tag:server]
      server:
        name: io.github.example/operations
        description: Operations overview
        version: 0.2.0
        remotes:
          - type: streamable-http
            url: http://{host}:{port}/mcp
            variables:
              host: {isRequired: true}
              port: {default: "8787"}
    - name: fleet-publish
      version: v0.1.0
`

func parse(t *testing.T, doc string) *Catalog {
	t.Helper()
	c, err := ParseCatalog([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	return c
}

func status(self *tailnet.Node, nodes ...*tailnet.Node) *tailnet.Status {
	return &tailnet.Status{Self: self, Nodes: nodes}
}

func serverNode() *tailnet.Node {
	return &tailnet.Node{Host: "server", DNSName: "server.ts.net", Tags: []string{"tag:server"}, Online: true}
}

func TestParseReadsFleetFieldsAndServerJSON(t *testing.T) {
	c := parse(t, sample)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(c.Names(), ","); got != "deployer,fleet-publish,operations,tracker" {
		t.Fatalf("names = %s", got)
	}
	tracker, _ := c.Lookup("tracker")
	if tracker.Version != "v0.2.1" || tracker.Role != "agent" {
		t.Fatalf("tracker = %+v", tracker)
	}
	if tracker.Server == nil || tracker.Server.Packages[0].RegistryType != RegistryTypeFleet {
		t.Fatalf("tracker server = %+v", tracker.Server)
	}
	if tracker.Server.Schema != model.CurrentSchemaURL {
		t.Fatalf("$schema = %q, want the current registry schema", tracker.Server.Schema)
	}
	if publish, _ := c.Lookup("fleet-publish"); publish.Server != nil {
		t.Fatal("a tool with no server block got a server")
	}
}

func TestChannelDefaultsToStable(t *testing.T) {
	c := parse(t, "toolchain:\n  tools:\n    - name: tracker\n      version: v1\n")
	if c.Channel != DefaultChannel {
		t.Fatalf("channel = %q, want %q", c.Channel, DefaultChannel)
	}
}

func TestParseRejectsFieldsServerJSONDoesNotDefine(t *testing.T) {
	doc := "toolchain:\n  tools:\n    - name: c\n      version: v1\n      server:\n        name: io.github.example/c\n        transport: stdio\n"
	if _, err := ParseCatalog([]byte(doc)); err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}

func TestParseRejectsUnknownToolFields(t *testing.T) {
	doc := "toolchain:\n  tools:\n    - name: c\n      version: v1\n      mcp:\n        args: [mcp]\n"
	if _, err := ParseCatalog([]byte(doc)); err == nil || !strings.Contains(err.Error(), `line 5: tool entry has unrecognized field "mcp"`) {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}

func TestToolMarshalsServerAsJSON(t *testing.T) {
	tracker, _ := parse(t, sample).Lookup("tracker")
	data, err := json.Marshal(tracker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"registryType":"fleet"`) {
		t.Fatalf("marshalled tool lost server.json field names: %s", data)
	}
}

func server(body string) string {
	return "toolchain:\n  tools:\n    - name: c\n      version: v1.0.0\n" + body
}

func TestValidateRejectsTheThingsThatMakeTheCatalogUseless(t *testing.T) {
	const fleetPackage = "        packages:\n          - {registryType: fleet, identifier: c, transport: {type: stdio}}\n"
	const header = "      server:\n        name: io.github.example/c\n        description: d\n        version: 1.0.0\n"
	cases := map[string]struct{ doc, want string }{
		"no version": {
			"toolchain:\n  tools:\n    - name: tracker\n",
			"version required",
		},
		"duplicate name": {
			"toolchain:\n  tools:\n    - name: tracker\n      version: v1\n    - name: tracker\n      version: v2\n",
			"listed twice",
		},
		"where is not a tag": {
			"toolchain:\n  tools:\n    - name: tracker\n      version: v1\n      where: [server]\n",
			"must be an ACL tag",
		},
		"name not reverse-DNS": {
			server("      server:\n        name: c\n        description: d\n        version: 1.0.0\n" + fleetPackage),
			"reverse-DNS",
		},
		"description too long": {
			server("      server:\n        name: io.github.example/c\n        description: " + strings.Repeat("x", 101) + "\n        version: 1.0.0\n" + fleetPackage),
			"1 to 100 characters",
		},
		"version drift": {
			server("      server:\n        name: io.github.example/c\n        description: d\n        version: 2.0.0\n" + fleetPackage),
			"does not match the tool version",
		},
		"no endpoint": {
			server(header),
			"no package or remote",
		},
		"unsupported registry type": {
			server(header + "        packages:\n          - {registryType: cargo, identifier: c, transport: {type: stdio}}\n"),
			`registryType "cargo" is not supported`,
		},
		"fleet package with a runtime hint": {
			server(header + "        packages:\n          - {registryType: fleet, identifier: c, runtimeHint: npx, transport: {type: stdio}}\n"),
			"runtimeHint, runtimeArguments and version do not apply",
		},
		"npm package with the wrong runtime": {
			server(header + "        packages:\n          - {registryType: npm, identifier: c, runtimeHint: docker, transport: {type: stdio}}\n"),
			`npm packages run with npx, not "docker"`,
		},
		"environment variable without a value": {
			server(header + "        packages:\n          - registryType: fleet\n            identifier: c\n            transport: {type: stdio}\n            environmentVariables: [{name: TOKEN}]\n"),
			"environment variable TOKEN needs a value or default",
		},
		"secret written into the file": {
			server(header + "        remotes:\n          - type: streamable-http\n            url: https://c.example/mcp\n            headers: [{name: Authorization, value: Bearer hunter2, isSecret: true}]\n"),
			"must come from a {variable} bound in env:",
		},
		"unbound variable": {
			server(header + "        remotes:\n          - type: streamable-http\n            url: https://c.example/mcp\n            headers: [{name: Authorization, value: \"Bearer {token}\"}]\n"),
			"{token} has no value",
		},
		"unused env binding": {
			server("      env: {token: C_TOKEN}\n" + header + "        remotes:\n          - {type: streamable-http, url: https://c.example/mcp}\n"),
			"env: token is not used",
		},
		"env binding to a lowercase name": {
			server("      env: {token: c_token}\n" + header + "        remotes:\n          - type: streamable-http\n            url: https://c.example/mcp\n            headers: [{name: Authorization, value: \"Bearer {token}\"}]\n"),
			"not an UPPER_SNAKE environment variable name",
		},
		"unknown os": {
			"toolchain:\n  tools:\n    - name: tracker\n      version: v1\n      os: [macos]\n",
			`os "macos" must be one of`,
		},
		"use_when without a server": {
			"toolchain:\n  tools:\n    - name: tracker\n      version: v1\n      use_when: always\n",
			"need a server",
		},
		"headers helper on a package": {
			server("      headers_helper: print-headers\n" + header + fleetPackage),
			"headers_helper only applies to a remote",
		},
		"remote without host": {
			server("      where: [tag:server]\n" + header + "        remotes:\n          - {type: streamable-http, url: http://server:80/mcp}\n"),
			"must contain {host}",
		},
		"remote without where": {
			server(header + "        remotes:\n          - {type: streamable-http, url: \"http://{host}:80/mcp\"}\n"),
			"{host} needs a where tag",
		},
		"remote variable without a value": {
			server("      where: [tag:server]\n" + header + "        remotes:\n          - {type: streamable-http, url: \"http://{host}:{port}/mcp\"}\n"),
			"{port} has no value",
		},
		"sse remote": {
			server("      where: [tag:server]\n" + header + "        remotes:\n          - {type: sse, url: \"http://{host}:80/sse\"}\n"),
			`remote type "sse" is not supported`,
		},
		"package and remote": {
			server("      where: [tag:server]\n" + header + fleetPackage + "        remotes:\n          - {type: streamable-http, url: \"http://{host}:80/mcp\"}\n"),
			"not both",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := parse(t, tc.doc).Validate()
			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	err := parse(t, "toolchain:\n  tools:\n    - name: a\n    - name: b\n      where: [nope]\n").Validate()
	if err == nil {
		t.Fatal("want an error")
	}
	if n := strings.Count(err.Error(), "\n    - "); n < 3 {
		t.Fatalf("reported %d problems, want at least 3:\n%s", n, err)
	}
}

func TestResolvePackagesRunLocallyWithTheirArguments(t *testing.T) {
	eps, _ := Resolve(parse(t, sample), status(&tailnet.Node{Host: "laptop"}))
	byName := map[string]Endpoint{}
	for _, e := range eps {
		byName[e.Name] = e
	}
	if got := byName["tracker"]; got.Command != "tracker" || joinArgs(got.Args) != "mcp --profile agents" {
		t.Fatalf("tracker endpoint = %+v", got)
	}
	if got := byName["deployer"]; got.Command != "deployer-mcp" || len(got.Args) != 0 {
		t.Fatalf("deployer endpoint = %+v", got)
	}
	if _, ok := byName["fleet-publish"]; ok {
		t.Fatal("a tool with no server was offered to an agent")
	}
}

func TestResolveRemoteFillsHostAndDefaults(t *testing.T) {
	offline := &tailnet.Node{Host: "old-server", DNSName: "old-server.ts.net", Tags: []string{"tag:server"}}
	eps, err := Resolve(parse(t, sample), status(&tailnet.Node{Host: "laptop"}, offline, serverNode()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, e := range eps {
		if e.Name == "operations" {
			if e.URL.String() != "http://server.ts.net:8787/mcp" {
				t.Fatalf("operations URL = %q", e.URL)
			}
			return
		}
	}
	t.Fatal("operations did not resolve")
}

func TestResolveReportsUnreachableWithoutDroppingTheRest(t *testing.T) {
	// No node carries tag:server. Returning only the error would take the
	// whole local plane down with one offline box.
	eps, err := Resolve(parse(t, sample), status(&tailnet.Node{Host: "laptop"}))
	if err == nil || !strings.Contains(err.Error(), "operations") {
		t.Fatalf("error = %v, want it to name operations", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints alongside the error, want the 2 local ones", len(eps))
	}
}

func TestResolveSkipsPackagesThisMachineDoesNotRun(t *testing.T) {
	doc := server("      where: [tag:server]\n      server:\n        name: io.github.example/c\n        description: d\n        version: 1.0.0\n        packages:\n          - {registryType: fleet, identifier: c, transport: {type: stdio}}\n")
	laptop := &tailnet.Node{Host: "laptop", Tags: []string{"tag:workstation"}}
	if eps, err := Resolve(parse(t, doc), status(laptop)); err != nil || len(eps) != 0 {
		t.Fatalf("offered a binary that is not installed here: %+v, %v", eps, err)
	}
	if eps, _ := Resolve(parse(t, doc), status(serverNode())); len(eps) != 1 {
		t.Fatalf("the tagged node should run its own tool, got %+v", eps)
	}
}

func TestResolveWithoutTailscaleStillServesTheLocalPlane(t *testing.T) {
	eps, err := Resolve(parse(t, sample), nil)
	if err == nil {
		t.Fatal("want an unreachable error for the remote")
	}
	if len(eps) != 2 {
		t.Fatalf("got %d local endpoints with no tailnet, want 2", len(eps))
	}
}

func TestConfigRendersBothTransports(t *testing.T) {
	eps, err := Resolve(parse(t, sample), status(&tailnet.Node{Host: "laptop"}, serverNode()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := Config(eps)
	if got := cfg["tracker"]; got.Command != "tracker" || got.URL != "" {
		t.Fatalf("tracker server = %+v", got)
	}
	if got := cfg["operations"]; got.Type != "http" || got.URL == "" || got.Command != "" {
		t.Fatalf("operations server = %+v", got)
	}
}

func joinArgs(args []Template) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}
