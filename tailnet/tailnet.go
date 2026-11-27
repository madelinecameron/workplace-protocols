// Package tailnet talks to the local Tailscale daemon and distills
// `tailscale status --json` into a small, dependency-free inventory of the
// reachable nodes, their tags, and MagicDNS names.
//
// The other packages use it to find nodes by ACL tag, so a node's job is set in
// Tailscale rather than in a config file.
package tailnet

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Node is a single machine in the tailnet, distilled from `tailscale status`.
type Node struct {
	Host     string   // DNSName's leading label, e.g. "web-prod"
	DNSName  string   // full MagicDNS name
	Addrs    []string // tailnet IPs
	Tags     []string // ACL tags, e.g. ["tag:acme","tag:prod"]
	Online   bool
	OS       string
	LastSeen time.Time
}

// Status is the parsed tailnet state.
type Status struct {
	MagicDNSSuffix string
	Self           *Node
	Nodes          []*Node
}

// runner is overridable in tests.
var runner = func(args ...string) ([]byte, error) {
	return exec.Command("tailscale", args...).Output()
}

// Available reports whether the tailscale CLI is installed.
func Available() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// Fetch reads and parses `tailscale status --json`.
func Fetch() (*Status, error) {
	out, err := runner("status", "--json")
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w (is tailscale installed and up?)", err)
	}
	return parseStatus(out)
}

// rawStatus mirrors the subset of `tailscale status --json` we consume.
type rawStatus struct {
	MagicDNSSuffix string              `json:"MagicDNSSuffix"`
	Self           *rawPeer            `json:"Self"`
	Peer           map[string]*rawPeer `json:"Peer"`
}

type rawPeer struct {
	HostName     string    `json:"HostName"`
	DNSName      string    `json:"DNSName"`
	OS           string    `json:"OS"`
	TailscaleIPs []string  `json:"TailscaleIPs"`
	Tags         []string  `json:"Tags"`
	Online       bool      `json:"Online"`
	LastSeen     time.Time `json:"LastSeen"`
}

func parseStatus(data []byte) (*Status, error) {
	var raw rawStatus
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	st := &Status{MagicDNSSuffix: raw.MagicDNSSuffix}
	conv := func(p *rawPeer) *Node {
		if p == nil {
			return nil
		}
		host := p.HostName
		if host == "" && p.DNSName != "" {
			host = strings.SplitN(p.DNSName, ".", 2)[0]
		}
		return &Node{
			Host:     host,
			DNSName:  strings.TrimSuffix(p.DNSName, "."),
			Addrs:    p.TailscaleIPs,
			Tags:     p.Tags,
			Online:   p.Online,
			OS:       p.OS,
			LastSeen: p.LastSeen,
		}
	}
	st.Self = conv(raw.Self)
	if st.Self != nil {
		st.Self.Online = true // self is always up
	}
	for _, p := range raw.Peer {
		st.Nodes = append(st.Nodes, conv(p))
	}
	// Peers arrive as a map. Sorting makes every "first match" choice made over
	// Nodes the same on every run.
	sort.Slice(st.Nodes, func(i, j int) bool {
		if st.Nodes[i].Host != st.Nodes[j].Host {
			return st.Nodes[i].Host < st.Nodes[j].Host
		}
		return st.Nodes[i].DNSName < st.Nodes[j].DNSName
	})
	return st, nil
}

// Lookup finds a node by its short hostname or full DNS name. Self is included.
func (s *Status) Lookup(name string) *Node {
	name = strings.TrimSuffix(name, ".")
	short := strings.SplitN(name, ".", 2)[0]
	for _, n := range s.all() {
		if n.Host == short || n.DNSName == name || n.Host == name {
			return n
		}
	}
	return nil
}

// ByTag returns every node carrying the given ACL tag, Self included.
func (s *Status) ByTag(tag string) []*Node {
	var out []*Node
	for _, n := range s.all() {
		for _, t := range n.Tags {
			if t == tag {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// all returns Self (if any) followed by the peer nodes.
func (s *Status) all() []*Node {
	if s.Self == nil {
		return s.Nodes
	}
	return append([]*Node{s.Self}, s.Nodes...)
}
