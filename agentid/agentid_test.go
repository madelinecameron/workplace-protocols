package agentid

import (
	"encoding/json"
	"testing"
)

// The same agent named by hand in a config, by a shell export, and by a Go
// constant must resolve to one address, or a mailbox holds two inboxes for one
// agent and half its mail is never read.
func TestParseUnifiesSpellings(t *testing.T) {
	forms := []string{
		"agent:scout",
		"Agent:Scout",
		"  agent:SCOUT  ",
		"AGENT: scout ",
		"agent:/scout/",
	}
	want := MustParse(forms[0])
	if want.String() != "agent:scout" {
		t.Fatalf("canonical = %q, want agent:scout", want.String())
	}
	for _, f := range forms {
		got, err := Parse(f)
		if err != nil {
			t.Errorf("Parse(%q) errored: %v", f, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", f, got, want)
		}
		if got.ID() != want.ID() {
			t.Errorf("Parse(%q).ID() = %q, want %q", f, got.ID(), want.ID())
		}
	}
}

func TestDistinctAddressesDoNotCollide(t *testing.T) {
	ids := map[string]string{}
	for _, s := range []string{
		"agent:scout", "agent:reviewer", "human:scout", "role:scout",
		"role:owner/acme", "role:owner/globex",
	} {
		a := MustParse(s)
		if prev, ok := ids[a.ID()]; ok {
			t.Errorf("%q and %q share id %q", prev, s, a.ID())
		}
		ids[a.ID()] = s
	}
	if len(ids) != 6 {
		t.Errorf("got %d distinct ids, want 6", len(ids))
	}
}

func TestIDLength(t *testing.T) {
	if got := MustParse("agent:scout").ID(); len(got) != IDLen {
		t.Errorf("ID() = %q, length %d, want %d", got, len(got), IDLen)
	}
}

func TestRoleSegmentsJoinAndCompareEqual(t *testing.T) {
	a, err := NewRole("Owner", "Acme")
	if err != nil {
		t.Fatalf("NewRole: %v", err)
	}
	b := MustParse("role:owner/acme")
	if a != b {
		t.Errorf("NewRole = %q, MustParse = %q", a, b)
	}
	if !a.IsRole() {
		t.Error("role address does not report IsRole")
	}
	if b.Kind != KindRole {
		t.Errorf("Kind = %q, want %q", b.Kind, KindRole)
	}
}

// Only roles are qualified. An agent named "scout/prod" would be a second naming
// scheme smuggled into the name field, and role expansion could not tell it
// apart from a position.
func TestOnlyRolesMayBeQualified(t *testing.T) {
	for _, s := range []string{"agent:scout/prod", "human:alice/personal"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
	if _, err := Parse("role:owner/acme"); err != nil {
		t.Errorf("Parse qualified role errored: %v", err)
	}
}

func TestMalformedAddresses(t *testing.T) {
	for _, s := range []string{
		"",
		"scout",             // no kind
		"agent:",            // no name
		"robot:scout",       // unknown kind
		"agent:scout agent", // space inside a name
		"agent:scout@host",  // punctuation that is not a separator
		"role:owner//dog",   // empty segment
		"agent:" + longName(129),
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
}

func TestZeroAddressIsInvalid(t *testing.T) {
	var a Address
	if a.Valid() {
		t.Error("zero Address reports Valid")
	}
	if _, err := a.MarshalText(); err == nil {
		t.Error("zero Address marshalled without error")
	}
}

func TestTextRoundTrip(t *testing.T) {
	type envelope struct {
		To Address `json:"to"`
	}
	in := envelope{To: MustParse("role:owner/acme")}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"to":"role:owner/acme"}` {
		t.Errorf("Marshal = %s", b)
	}

	var out envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.To != in.To {
		t.Errorf("round trip = %q, want %q", out.To, in.To)
	}
}

// A session distinguishes runs; the address it derives from does not change, so
// "which agent" and "which run" stay separately answerable.
func TestSessionIDVariesByRunAndNotByAddress(t *testing.T) {
	scout := MustParse("agent:scout")
	a := SessionID(scout, "closet", "1")
	b := SessionID(scout, "closet", "2")
	c := SessionID(scout, "workstation", "1")
	if a == b || a == c || b == c {
		t.Errorf("sessions collided: %q %q %q", a, b, c)
	}
	if SessionID(scout, "CLOSET", "1") != a {
		t.Error("host case changed the session id")
	}
	if len(a) != IDLen {
		t.Errorf("SessionID length %d, want %d", len(a), IDLen)
	}
}

// An agent that does not know its own name must fail at startup rather than
// send from a guess nobody can reply to.
func TestFromEnvRequiresTheVariable(t *testing.T) {
	t.Setenv(EnvVar, "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv succeeded with the variable unset")
	}

	t.Setenv(EnvVar, " Agent:Reviewer ")
	got, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if want := MustParse("agent:reviewer"); got != want {
		t.Errorf("FromEnv = %q, want %q", got, want)
	}
}

func longName(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// Address ids are stored and joined on across tools, so the derivation must
// never change. This pins one value.
func TestIDIsStable(t *testing.T) {
	if got := MustParse("agent:alice").ID(); got != "40e4d0439115" {
		t.Fatalf("ID = %q, want 40e4d0439115", got)
	}
}
