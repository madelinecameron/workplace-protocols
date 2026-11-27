package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madelinecameron/workplace-protocols/tailnet"
)

const sample = `
tailnet: example.ts.net
services:
  - name: hub
    url: ssh://git@devbox.example.ts.net/srv/hub.git
    tags: [tag:workplace]
    parameters:
      checkout: ~/hub
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
	hub, ok := m.Service("hub")
	if !ok || hub.URL != "ssh://git@devbox.example.ts.net/srv/hub.git" || hub.Parameters["checkout"] != "~/hub" {
		t.Fatalf("hub = %+v, %v", hub, ok)
	}
	if _, ok := m.Service("absent"); ok {
		t.Fatal("found a service that is not declared")
	}
	if string(m.Raw) != sample {
		t.Fatal("Raw does not hold the parsed bytes")
	}
}

func TestShippedExampleValidates(t *testing.T) {
	m, err := Load(filepath.Join("..", "examples", "workplace-protocols.yaml"))
	if err != nil {
		t.Fatalf("the shipped example does not load: %v", err)
	}
	if len(m.Services) == 0 {
		t.Fatal("the shipped example declares no services")
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

	url := "postgres://db:5432/app"
	cases := map[string]*Manifest{
		"no tailnet":         {Services: []Service{{Name: "a", URL: url}}},
		"unnamed service":    {Tailnet: "t", Services: []Service{{URL: url}}},
		"duplicate service":  {Tailnet: "t", Services: []Service{{Name: "a", URL: url}, {Name: "a", URL: url}}},
		"no url":             {Tailnet: "t", Services: []Service{{Name: "a"}}},
		"url with no scheme": {Tailnet: "t", Services: []Service{{Name: "a", URL: "server/db"}}},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workplace-protocols.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPrefersThePathThenTheEnvironmentThenHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".workplace-protocols"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".workplace-protocols", "workplace-protocols.yaml"), []byte("tailnet: home.ts.net\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromEnv := writeManifest(t, "tailnet: env.ts.net\n")
	given := writeManifest(t, "tailnet: given.ts.net\n")

	t.Setenv(EnvFile, "")
	if m, err := Load(""); err != nil || m.Tailnet != "home.ts.net" {
		t.Fatalf("default path: %+v, %v", m, err)
	}
	t.Setenv(EnvFile, fromEnv)
	if m, err := Load(""); err != nil || m.Tailnet != "env.ts.net" || m.Source != fromEnv {
		t.Fatalf("environment: %+v, %v", m, err)
	}
	if m, err := Load(given); err != nil || m.Tailnet != "given.ts.net" {
		t.Fatalf("given path: %+v, %v", m, err)
	}
}

func TestLoadExplainsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil || !strings.Contains(err.Error(), EnvFile) {
		t.Fatalf("error = %v, want it to mention %s", err, EnvFile)
	}
}

func TestLoadValidates(t *testing.T) {
	if _, err := Load(writeManifest(t, "services: []\n")); err == nil {
		t.Fatal("expected a manifest with no tailnet to be rejected")
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
	if m.Source != readHost || m.Tailnet != "example.ts.net" {
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
