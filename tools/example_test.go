package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

// examples/workplace-protocols.yaml is what people copy to start a manifest.
// These tests read the shipped file rather than a copy, so an example that
// stops parsing fails here instead of on someone's machine.
func exampleCatalog(t *testing.T) *Catalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "examples", "workplace-protocols.yaml"))
	if err != nil {
		t.Fatalf("reading the shipped example: %v", err)
	}
	c, err := ParseCatalog(data)
	if err != nil {
		t.Fatalf("parsing the shipped example: %v", err)
	}
	return c
}

func TestShippedExampleParsesAndValidates(t *testing.T) {
	c := exampleCatalog(t)
	if err := c.Validate(); err != nil {
		t.Fatalf("the shipped example does not validate: %v", err)
	}
	if len(c.Tools) == 0 || len(c.MCP) == 0 {
		t.Fatal("the shipped example should declare tools and mcp entries")
	}
}

// The example exists to demonstrate each kind of entry. If one disappeared
// the file would still validate, and silently stop showing it.
func TestShippedExampleDemonstratesEveryKind(t *testing.T) {
	c := exampleCatalog(t)
	var catalogOnly, tagged, fixed, fleet, npm, secret int
	for _, tool := range c.Tools {
		if tool.Server == nil {
			catalogOnly++
		}
	}
	for _, e := range c.Entries() {
		for _, r := range e.Server.Remotes {
			if len(e.Where) > 0 {
				tagged++
			} else {
				fixed++
			}
			for _, h := range r.Headers {
				if h.IsSecret {
					secret++
				}
			}
		}
		for _, p := range e.Server.Packages {
			switch p.RegistryType {
			case RegistryTypeFleet:
				fleet++
			case "npm":
				npm++
			}
		}
	}
	for kind, n := range map[string]int{
		"tool without a server": catalogOnly, "remote found by tag": tagged, "fixed-URL remote": fixed,
		"fleet package": fleet, "npm package": npm, "secret header": secret,
	} {
		if n == 0 {
			t.Errorf("the example has no %s", kind)
		}
	}
}

// On a healthy tailnet every entry for this OS resolves, and Config renders
// each one in the shape an MCP client consumes.
func TestShippedExampleResolvesOnAHealthyTailnet(t *testing.T) {
	c := exampleCatalog(t)
	self := &tailnet.Node{Host: "laptop", Tags: []string{"tag:workstation"}, Online: true}
	server := &tailnet.Node{Host: "server", DNSName: "server.example.ts.net", Tags: []string{"tag:server"}, Online: true}

	eps, err := resolve(c, &tailnet.Status{Self: self, Nodes: []*tailnet.Node{self, server}}, "darwin", "")
	if err != nil {
		t.Fatalf("Resolve on a healthy tailnet: %v", err)
	}
	if want := len(c.Entries()); len(eps) != want {
		t.Fatalf("resolved %d endpoints, want %d", len(eps), want)
	}
	for name, srv := range Config(eps) {
		if srv.URL == "" && srv.Command == "" {
			t.Errorf("%s rendered with neither a url nor a command", name)
		}
	}
	if _, err := Codex(eps); err != nil {
		t.Fatalf("the example should render for Codex: %v", err)
	}
}
