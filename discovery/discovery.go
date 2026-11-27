// Package discovery loads the manifest: one YAML file that every tool and
// agent reads to learn the same facts about your machines.
//
// Load reads the file from local disk, which suits a file you distribute with
// a dotfiles manager or configuration management. Resolve instead finds a
// tailnet node carrying an ACL tag and reads the file from it over SSH, so a
// fresh machine can configure itself with nothing installed but Tailscale.
//
// This package reads only `tailnet` and `services`. Other sections of the same
// file belong to whoever reads them: tools.ParseCatalog reads `toolchain` and
// `mcp`, and your own tools can parse sections of their own from Manifest.Raw.
// Unknown top-level keys are ignored here, so adding a section never breaks
// another reader.
package discovery

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/madelinecameron/workplace-protocols/tailnet"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultTag is the ACL tag Resolve looks for when the caller passes none.
	DefaultTag = "tag:workplace"
	// DefaultPath is where Load looks when given no path and EnvFile is unset,
	// and where SSHReader reads on the remote node.
	DefaultPath = "~/.workplace-protocols/workplace-protocols.yaml"
	// EnvFile names the environment variable Load checks for a manifest path.
	EnvFile = "WORKPLACE_PROTOCOLS_FILE"
)

// Manifest is the part of workplace-protocols.yaml this package defines.
type Manifest struct {
	// Tailnet is the tailnet's MagicDNS suffix, such as example.ts.net.
	Tailnet  string    `yaml:"tailnet" json:"tailnet"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`

	// Source is the file Load read, or the node Resolve read from.
	Source string `yaml:"-" json:"source,omitempty"`
	// Raw is the whole file, for parsing sections this package does not define.
	Raw []byte `yaml:"-" json:"-"`
}

// Service is something on your network that your tools find by name: a git
// remote, a database, an API. Agents do not connect to services; MCP servers
// go in the `mcp` section instead.
type Service struct {
	Name string `yaml:"name" json:"name"`
	// URL is where the service is, in whatever scheme it speaks, such as
	// ssh://git@host/srv/repo.git or postgres://db.example.ts.net:5432/app.
	URL  string   `yaml:"url" json:"url"`
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Parameters holds anything else a consumer needs that is not part of
	// the URL, such as a local checkout path.
	Parameters map[string]string `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

// Service finds a service by name.
func (m *Manifest) Service(name string) (Service, bool) {
	for _, s := range m.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
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
		if u, err := url.Parse(s.URL); err != nil || u.Scheme == "" {
			problems = append(problems, fmt.Sprintf("services[%d] (%s): url %q must be an absolute URL with a scheme", i, s.Name, s.URL))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid workplace protocols manifest:\n    - %s", strings.Join(problems, "\n    - "))
	}
	return nil
}

// Load reads, parses and validates the manifest from local disk. It reads
// path when given, otherwise the file named by $WORKPLACE_PROTOCOLS_FILE,
// otherwise DefaultPath. A leading ~/ means the user's home directory.
func Load(path string) (*Manifest, error) {
	if path == "" {
		path = os.Getenv(EnvFile)
	}
	if path == "" {
		path = DefaultPath
	}
	expanded, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(expanded)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no manifest at %s: create it, set $%s, or pass a path", expanded, EnvFile)
	}
	if err != nil {
		return nil, err
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	m.Source = expanded
	return m, nil
}

func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", path, err)
	}
	return filepath.Join(home, rest), nil
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
	m.Source = host
	return m, nil
}

// SSHReader returns a Reader that runs `ssh <host> cat <path>`, with path
// defaulting to DefaultPath. The calling user must be able to SSH to the node
// without a prompt, for example with Tailscale SSH or a key.
func SSHReader(path string) Reader {
	if path == "" {
		path = DefaultPath
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
