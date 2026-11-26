// Package discovery finds and reads the manifest: one YAML file, kept on one
// node of your tailnet, that every tool and agent reads to learn the same facts.
//
// You mark the node that holds the file with a Tailscale ACL tag (DefaultTag
// unless you choose another). Resolve finds a node carrying that tag in
// `tailscale status` and reads the file from it over SSH. Nothing else has to
// be running, so a tool can configure itself on a fresh machine.
//
// This package reads only the fields every consumer needs. Other sections of
// the same file belong to whoever reads them: tools.ParseCatalog reads
// `toolchain`, and your own tools can add and parse sections of their own from
// Manifest.Raw. Unknown top-level keys are ignored here, so adding a section
// never breaks another reader.
package discovery

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/madelinecameron/workplace-protocols/tailnet"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultTag is the ACL tag Resolve looks for when the caller passes none.
	DefaultTag = "tag:workplace"
	// DefaultRemotePath is where SSHReader reads the manifest on that node.
	DefaultRemotePath = "~/.workplace-protocols/workplace-protocols.yaml"
)

// Manifest is the part of workplace-protocols.yaml this package defines.
type Manifest struct {
	// Tailnet is the tailnet's MagicDNS suffix, such as example.ts.net.
	Tailnet  string    `yaml:"tailnet" json:"tailnet"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`

	// Host is the node Resolve read the manifest from. Parse leaves it empty.
	Host string `yaml:"-" json:"host,omitempty"`
	// Raw is the whole file, for parsing sections this package does not define.
	Raw []byte `yaml:"-" json:"-"`
}

// Service is a shared service on the tailnet that tools look up by name, such
// as a database or a dashboard.
type Service struct {
	Name string `yaml:"name" json:"name"`
	// Host is the node the service runs on.
	Host string `yaml:"host,omitempty" json:"host,omitempty"`
	// Kind is a free-form label, such as postgres or dashboard.
	Kind string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Parse decodes workplace-protocols.yaml bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse workplace protocols manifest: %w", err)
	}
	m.Raw = data
	return &m, nil
}

// Validate checks the fields this package defines and reports every problem at
// once.
func (m *Manifest) Validate() error {
	var problems []string
	if m.Tailnet == "" {
		problems = append(problems, "tailnet: required")
	}
	seen := make(map[string]bool, len(m.Services))
	for i, s := range m.Services {
		switch {
		case s.Name == "":
			problems = append(problems, fmt.Sprintf("services[%d]: name required", i))
		case seen[s.Name]:
			problems = append(problems, fmt.Sprintf("services[%d]: %q listed twice", i, s.Name))
		default:
			seen[s.Name] = true
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid workplace protocols manifest:\n    - %s", strings.Join(problems, "\n    - "))
	}
	return nil
}

// Reader returns the manifest bytes held on host. Resolve takes one so that the
// transport can be swapped, and so it can be tested without SSH.
type Reader func(host string) ([]byte, error)

// Resolve finds an online node carrying tag (DefaultTag when empty), reads the
// manifest from it with read, and parses and validates it.
func Resolve(st *tailnet.Status, tag string, read Reader) (*Manifest, error) {
	if st == nil {
		return nil, fmt.Errorf("no tailnet status (is tailscale up?)")
	}
	if tag == "" {
		tag = DefaultTag
	}
	nodes := st.ByTag(tag)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no tailnet node carries %q: tag the node that holds the manifest, or pass the tag you use", tag)
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
	m.Host = host
	return m, nil
}

// SSHReader returns a Reader that runs `ssh <host> cat <path>`, with path
// defaulting to DefaultRemotePath. The calling user must be able to SSH to the
// node without a prompt, for example with Tailscale SSH or a key.
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

// pickReachable prefers an online node, so Resolve does not try a node that is
// down while another tagged node could answer.
func pickReachable(nodes []*tailnet.Node) *tailnet.Node {
	for _, n := range nodes {
		if n.Online {
			return n
		}
	}
	return nodes[0]
}
