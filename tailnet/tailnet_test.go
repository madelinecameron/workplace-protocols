package tailnet

import (
	"strings"
	"testing"
)

const sample = `{
  "MagicDNSSuffix": "tail1234.ts.net",
  "Self": {"HostName":"my-laptop","DNSName":"my-laptop.tail1234.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.9"],"Online":true},
  "Peer": {
    "key1": {"HostName":"web-prod","DNSName":"web-prod.tail1234.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.1"],"Tags":["tag:acme","tag:prod"],"Online":true},
    "key2": {"HostName":"acme-dev","DNSName":"acme-dev.tail1234.ts.net.","TailscaleIPs":["100.64.0.2"],"Online":false}
  }
}`

func TestParseStatus(t *testing.T) {
	st, err := parseStatus([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if st.MagicDNSSuffix != "tail1234.ts.net" {
		t.Fatalf("suffix = %q", st.MagicDNSSuffix)
	}
	if st.Self == nil || st.Self.Host != "my-laptop" {
		t.Fatalf("self = %+v", st.Self)
	}
	if len(st.Nodes) != 2 {
		t.Fatalf("want 2 peers, got %d", len(st.Nodes))
	}
}

func TestLookupByShortAndFQDN(t *testing.T) {
	st, _ := parseStatus([]byte(sample))
	n := st.Lookup("web-prod")
	if n == nil || !n.Online {
		t.Fatalf("lookup short failed: %+v", n)
	}
	if got := st.Lookup("web-prod.tail1234.ts.net"); got == nil {
		t.Fatal("lookup by fqdn failed")
	}
	// DNSName should be trimmed of the trailing dot.
	if n.DNSName != "web-prod.tail1234.ts.net" {
		t.Fatalf("dnsname = %q", n.DNSName)
	}
	if st.Lookup("nonexistent") != nil {
		t.Fatal("expected nil for unknown host")
	}
}

func TestByTag(t *testing.T) {
	st, _ := parseStatus([]byte(sample))
	prod := st.ByTag("tag:prod")
	if len(prod) != 1 || prod[0].Host != "web-prod" {
		t.Fatalf("ByTag(tag:prod) = %+v", prod)
	}
	if got := st.ByTag("tag:absent"); got != nil {
		t.Fatalf("ByTag for a missing tag should be empty, got %+v", got)
	}
}

func TestNodesAreSortedByHost(t *testing.T) {
	const manyPeers = `{
	  "Peer": {
	    "k1": {"HostName":"delta"}, "k2": {"HostName":"alpha"}, "k3": {"HostName":"charlie"},
	    "k4": {"HostName":"bravo"}, "k5": {"HostName":"echo"}
	  }
	}`
	for range 20 {
		st, err := parseStatus([]byte(manyPeers))
		if err != nil {
			t.Fatal(err)
		}
		var hosts []string
		for _, n := range st.Nodes {
			hosts = append(hosts, n.Host)
		}
		if got := strings.Join(hosts, ","); got != "alpha,bravo,charlie,delta,echo" {
			t.Fatalf("nodes = %s, want sorted by host", got)
		}
	}
}

func TestByTagIncludesSelf(t *testing.T) {
	const withSelfTag = `{
	  "Self": {"HostName":"closet","DNSName":"closet.ts.net.","Tags":["tag:closet"],"Online":true},
	  "Peer": {}
	}`
	st, _ := parseStatus([]byte(withSelfTag))
	if got := st.ByTag("tag:closet"); len(got) != 1 || got[0].Host != "closet" {
		t.Fatalf("ByTag should include Self, got %+v", got)
	}
}
