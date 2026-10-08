package discovery

import (
	"fmt"
	"testing"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

const sample = `
tailnet: example.ts.net
closet:
  host: devbox
  tag: tag:closet
hub:
  remote: ssh://devbox.example.ts.net/~/fleet-hub.git
mirror:
  host: devbox.example.ts.net
  dir: /home/alice/fleet-mirrors
registry:
  remote: git@github.com:example/registry.git
  path: ~/registry
services:
  - name: operations
    host: devbox
    kind: board
    tags: [tag:closet]
`

func TestParseAndFields(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if m.Tailnet != "example.ts.net" || m.Closet.Host != "devbox" {
		t.Fatalf("top-level fields wrong: %+v", m)
	}
	if m.Hub.Remote == "" || m.Registry.Path != "~/registry" {
		t.Fatalf("hub/registry wrong: %+v", m)
	}
	if len(m.Services) != 1 || m.Services[0].Name != "operations" {
		t.Fatalf("services wrong: %+v", m.Services)
	}
	if m.ClosetTag() != "tag:closet" {
		t.Fatalf("closet tag = %q", m.ClosetTag())
	}
}

func TestClosetTagDefaults(t *testing.T) {
	m := &Manifest{}
	if m.ClosetTag() != DefaultClosetTag {
		t.Fatalf("default closet tag = %q, want %q", m.ClosetTag(), DefaultClosetTag)
	}
}

func TestValidate(t *testing.T) {
	valid, _ := Parse([]byte(sample))
	if err := valid.Validate(); err != nil {
		t.Fatalf("sample should be valid: %v", err)
	}

	cases := map[string]*Manifest{
		"no tailnet":     {Closet: Closet{Host: "x"}, Hub: Hub{Remote: "r"}},
		"no closet host": {Tailnet: "t", Hub: Hub{Remote: "r"}},
		"points nowhere": {Tailnet: "t", Closet: Closet{Host: "x"}},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestValidateAllowsHubOnlyOrRegistryOnly(t *testing.T) {
	hubOnly := &Manifest{Tailnet: "t", Closet: Closet{Host: "x"}, Hub: Hub{Remote: "r"}}
	regOnly := &Manifest{Tailnet: "t", Closet: Closet{Host: "x"}, Registry: Registry{Path: "~/registry"}}
	if err := hubOnly.Validate(); err != nil {
		t.Errorf("hub-only manifest should be valid: %v", err)
	}
	if err := regOnly.Validate(); err != nil {
		t.Errorf("registry-only manifest should be valid: %v", err)
	}
}

func statusWithCloset() *tailnet.Status {
	return &tailnet.Status{
		Nodes: []*tailnet.Node{
			{Host: "laptop", Online: true},
			{Host: "devbox", DNSName: "devbox.example.ts.net", Online: true, Tags: []string{"tag:closet"}},
		},
	}
}

func TestResolveFindsClosetByTagAndReads(t *testing.T) {
	var readHost string
	read := func(host string) ([]byte, error) {
		readHost = host
		return []byte(sample), nil
	}
	m, err := Resolve(statusWithCloset(), "", read)
	if err != nil {
		t.Fatal(err)
	}
	// Prefer the stable DNSName of the tagged node.
	if readHost != "devbox.example.ts.net" {
		t.Fatalf("read from %q, want devbox.example.ts.net", readHost)
	}
	if m.Tailnet != "example.ts.net" {
		t.Fatalf("resolved manifest wrong: %+v", m)
	}
}

func TestResolveNoClosetTag(t *testing.T) {
	st := &tailnet.Status{Nodes: []*tailnet.Node{{Host: "laptop", Online: true}}}
	_, err := Resolve(st, "", func(string) ([]byte, error) { return nil, nil })
	if err == nil {
		t.Fatal("expected error when no node carries the closet tag")
	}
}

func TestResolvePropagatesReadError(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, fmt.Errorf("ssh boom") }
	if _, err := Resolve(statusWithCloset(), "", read); err == nil {
		t.Fatal("expected read error to propagate")
	}
}

func TestResolveRejectsInvalidManifest(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("tailnet: t\n"), nil } // no closet host, points nowhere
	if _, err := Resolve(statusWithCloset(), "", read); err == nil {
		t.Fatal("expected validation to reject an incomplete manifest")
	}
}
