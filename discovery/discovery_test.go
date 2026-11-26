package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

const sample = `
tailnet: example.ts.net
services:
  - name: dashboard
    host: devbox
    kind: dashboard
    tags: [tag:workplace]
toolchain:
  tools: []
backups:
  bucket: s3://example
`

func TestParseAndFields(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if m.Tailnet != "example.ts.net" {
		t.Fatalf("tailnet = %q", m.Tailnet)
	}
	if len(m.Services) != 1 || m.Services[0].Name != "dashboard" {
		t.Fatalf("services wrong: %+v", m.Services)
	}
	if string(m.Raw) != sample {
		t.Fatal("Raw does not hold the parsed bytes")
	}
}

func TestShippedExampleValidates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "examples", "workplace-protocols.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("the shipped example does not validate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	valid, _ := Parse([]byte(sample))
	if err := valid.Validate(); err != nil {
		t.Fatalf("sample should be valid: %v", err)
	}
	if err := (&Manifest{Tailnet: "t"}).Validate(); err != nil {
		t.Fatalf("a manifest with only a tailnet should be valid: %v", err)
	}

	cases := map[string]*Manifest{
		"no tailnet":        {Services: []Service{{Name: "a"}}},
		"unnamed service":   {Tailnet: "t", Services: []Service{{Host: "x"}}},
		"duplicate service": {Tailnet: "t", Services: []Service{{Name: "a"}, {Name: "a"}}},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func statusWithTaggedNode() *tailnet.Status {
	return &tailnet.Status{
		Nodes: []*tailnet.Node{
			{Host: "laptop", Online: true},
			{Host: "devbox", DNSName: "devbox.example.ts.net", Online: true, Tags: []string{DefaultTag}},
		},
	}
}

func TestResolveFindsTaggedNodeAndReads(t *testing.T) {
	var readHost string
	read := func(host string) ([]byte, error) {
		readHost = host
		return []byte(sample), nil
	}
	m, err := Resolve(statusWithTaggedNode(), "", read)
	if err != nil {
		t.Fatal(err)
	}
	if readHost != "devbox.example.ts.net" {
		t.Fatalf("read from %q, want devbox.example.ts.net", readHost)
	}
	if m.Host != readHost || m.Tailnet != "example.ts.net" {
		t.Fatalf("resolved manifest wrong: %+v", m)
	}
}

func TestResolveUsesTheGivenTag(t *testing.T) {
	st := &tailnet.Status{Nodes: []*tailnet.Node{{Host: "box", Online: true, Tags: []string{"tag:closet"}}}}
	read := func(string) ([]byte, error) { return []byte(sample), nil }
	if _, err := Resolve(st, "", read); err == nil {
		t.Fatal("the default tag should not match tag:closet")
	}
	if _, err := Resolve(st, "tag:closet", read); err != nil {
		t.Fatalf("Resolve with an explicit tag: %v", err)
	}
}

func TestResolveNoTaggedNode(t *testing.T) {
	st := &tailnet.Status{Nodes: []*tailnet.Node{{Host: "laptop", Online: true}}}
	_, err := Resolve(st, "", func(string) ([]byte, error) { return nil, nil })
	if err == nil {
		t.Fatal("expected error when no node carries the tag")
	}
}

func TestResolvePropagatesReadError(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, fmt.Errorf("ssh boom") }
	if _, err := Resolve(statusWithTaggedNode(), "", read); err == nil {
		t.Fatal("expected read error to propagate")
	}
}

func TestResolveRejectsInvalidManifest(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("services: []\n"), nil }
	if _, err := Resolve(statusWithTaggedNode(), "", read); err == nil {
		t.Fatal("expected validation to reject a manifest with no tailnet")
	}
}
