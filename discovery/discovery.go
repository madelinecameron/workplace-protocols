// Package discovery is the bootstrap layer: a small manifest
// (workplace-protocols.yaml) describing tailnet-level facts — where the git hub
// and snapshot mirror live, where the service registry is, which box is the
// always-on "closet" — plus the resolver that finds and reads it.
//
// The design keeps the discovery root in Tailscale, not in a service. Exactly
// one fact is out-of-band: which node is the closet box, encoded as an ACL tag
// (default tag:closet). Everything else is derived by reading the manifest from
// that node. A fresh daemon can therefore self-configure with no coordinator
// running: find the closet by tag, read workplace-protocols.yaml, done.
package discovery

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/madelinecameron/workplace-protocols/tailnet"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultClosetTag is the ACL tag that marks the always-on box holding the
	// protocol manifest. It is the single out-of-band bootstrap fact.
	DefaultClosetTag = "tag:closet"
	// DefaultRemotePath is where the manifest is read from on the closet box.
	DefaultRemotePath = "~/.workplace-protocols/workplace-protocols.yaml"
)

// Manifest is the parsed workplace-protocols.yaml: tailnet-level facts both tools read.
type Manifest struct {
	Tailnet  string    `yaml:"tailnet" json:"tailnet"`
	Closet   Closet    `yaml:"closet" json:"closet"`
	Hub      Hub       `yaml:"hub" json:"hub"`
	Mirror   Mirror    `yaml:"mirror" json:"mirror"`
	Registry Registry  `yaml:"registry" json:"registry"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
}

// Closet identifies the always-on box that holds the manifest.
type Closet struct {
	Host string `yaml:"host" json:"host"`
	Tag  string `yaml:"tag,omitempty" json:"tag,omitempty"` // defaults to DefaultClosetTag
}

// Hub is the shared git hub that workstations sync through.
type Hub struct {
	Remote string `yaml:"remote,omitempty" json:"remote,omitempty"`
}

// Mirror is where workstation snapshots are mirrored.
type Mirror struct {
	Host string `yaml:"host,omitempty" json:"host,omitempty"`
	Dir  string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

// Registry is the service registry location.
type Registry struct {
	Remote string `yaml:"remote,omitempty" json:"remote,omitempty"`
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
}

// Service is an optional shared service on the tailnet.
type Service struct {
	Name string   `yaml:"name" json:"name"`
	Host string   `yaml:"host,omitempty" json:"host,omitempty"`
	Kind string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// ClosetTag returns the tag that marks the closet box, defaulting when unset.
func (m *Manifest) ClosetTag() string {
	if m.Closet.Tag != "" {
		return m.Closet.Tag
	}
	return DefaultClosetTag
}

// Parse decodes workplace-protocols.yaml bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse workplace protocols manifest: %w", err)
	}
	return &m, nil
}

// Validate checks the manifest carries enough to be useful. It is intentionally
// lenient — a manifest may describe only the hub, or only the registry — but a
// tailnet and a closet host are always required, and it must point at something.
func (m *Manifest) Validate() error {
	var problems []string
	if m.Tailnet == "" {
		problems = append(problems, "tailnet: required")
	}
	if m.Closet.Host == "" {
		problems = append(problems, "closet.host: required")
	}
	if m.Hub.Remote == "" && m.Registry.Remote == "" && m.Registry.Path == "" && len(m.Services) == 0 {
		problems = append(problems, "manifest points at nothing (set hub, registry, or services)")
	}
	for i, s := range m.Services {
		if s.Name == "" {
			problems = append(problems, fmt.Sprintf("services[%d]: name required", i))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid workplace protocols manifest:\n    - %s", strings.Join(problems, "\n    - "))
	}
	return nil
}

// Reader reads the raw workplace protocols manifest bytes from a closet host. It is injected
// so Resolve is testable without SSH.
type Reader func(host string) ([]byte, error)

// Resolve discovers the manifest: find the tailnet node carrying the closet tag,
// read the manifest from it, then parse and validate. tag defaults to
// DefaultClosetTag when empty. This is the whole bootstrap — no service, one
// round trip, keyed off a Tailscale tag.
func Resolve(st *tailnet.Status, tag string, read Reader) (*Manifest, error) {
	if st == nil {
		return nil, fmt.Errorf("no tailnet status (is tailscale up?)")
	}
	if tag == "" {
		tag = DefaultClosetTag
	}
	nodes := st.ByTag(tag)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no tailnet node carries %q — tag your closet box to bootstrap workplace protocols", tag)
	}
	node := pickReachable(nodes)
	host := node.DNSName
	if host == "" {
		host = node.Host
	}
	data, err := read(host)
	if err != nil {
		return nil, fmt.Errorf("read workplace protocols manifest from %s: %w", host, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// SSHReader returns a Reader that runs `ssh <host> cat <path>` (path defaults to
// DefaultRemotePath). This is the default bootstrap transport: no extra service,
// just the SSH already gating the tailnet.
func SSHReader(path string) Reader {
	if path == "" {
		path = DefaultRemotePath
	}
	return func(host string) ([]byte, error) {
		out, err := exec.Command("ssh", host, "cat", path).Output()
		if err != nil {
			return nil, fmt.Errorf("ssh %s cat %s: %w", host, path, err)
		}
		return out, nil
	}
}

// pickReachable prefers an online node so bootstrap doesn't try a box that is
// down when another tagged node could answer.
func pickReachable(nodes []*tailnet.Node) *tailnet.Node {
	for _, n := range nodes {
		if n.Online {
			return n
		}
	}
	return nodes[0]
}
