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
	if len(c.Tools) == 0 {
		t.Fatal("the shipped example declares no tools")
	}
	for _, tool := range c.Tools {
		if tool.Name == "" || tool.Version == "" {
			t.Errorf("tool %+v is missing a name or version", tool)
		}
	}
}

// The example exists to demonstrate the `server:` block. If every entry lost it
// the file would still parse and still validate, and the example would
// silently stop showing the thing it is for.
func TestShippedExampleDemonstratesEveryKind(t *testing.T) {
	c := exampleCatalog(t)

	var local, remote, catalogOnly int
	for _, tool := range c.Tools {
		switch {
		case tool.Server == nil:
			catalogOnly++
		case len(tool.Server.Remotes) > 0:
			remote++
		default:
			local++
		}
	}
	if local == 0 {
		t.Error("no local server in the example")
	}
	if remote == 0 {
		t.Error("no remote server in the example")
	}
	if catalogOnly == 0 {
		t.Error("no catalog-only tool in the example; the file should show that a tool may have no server")
	}
}

// Resolve against a synthetic tailnet: a server carrying tag:server is
// online, and we are a workstation. Every declared endpoint should come back
// reachable, and Config should render each one in the shape an MCP client
// consumes.
func TestShippedExampleResolvesOnAHealthyTailnet(t *testing.T) {
	c := exampleCatalog(t)
	self := &tailnet.Node{Host: "laptop", Tags: []string{"tag:workstation"}, Online: true}
	server := &tailnet.Node{
		Host:    "server",
		DNSName: "server.example.ts.net",
		Tags:    []string{"tag:server"},
		Online:  true,
	}

	eps, err := Resolve(c, &tailnet.Status{Self: self, Nodes: []*tailnet.Node{self, server}})
	if err != nil {
		t.Fatalf("Resolve on a healthy tailnet: %v", err)
	}

	declared := 0
	for _, tool := range c.Tools {
		if tool.Server != nil {
			declared++
		}
	}
	if len(eps) != declared {
		t.Fatalf("resolved %d endpoints, want %d declared", len(eps), declared)
	}

	servers := Config(eps)
	for name, srv := range servers {
		if srv.Type == "http" {
			if srv.URL == "" {
				t.Errorf("%s: http server rendered with no URL", name)
			}
			continue
		}
		if srv.Command == "" {
			t.Errorf("%s: stdio server rendered with no command", name)
		}
	}
	if len(servers) != declared {
		t.Fatalf("Config rendered %d servers, want %d", len(servers), declared)
	}
}
