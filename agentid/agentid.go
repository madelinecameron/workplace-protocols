// Package agentid derives the stable addresses used to name agents and humans
// across machines and tools. An address is a canonical string — "agent:scout",
// "human:alice", "role:owner/acme" — and an id is its hash, so two tools that
// were told the same name in different letter case, or with a stray slash, key
// the same thing.
//
// Every tool that names agents should use this package rather than its own
// copy, because addressing only works if they all compute byte-identical ids.
// A sender and a message store that disagree about whether "Scout" and "scout"
// are one agent deliver a message twice, or not at all.
//
// Identity here is persistent and coarse: an address names something you can
// send to and expect an answer from, not one run of it. A single run is a
// SessionID, derived from the address, so attribution can be finer-grained than
// addressing without needing a second naming scheme.
package agentid

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/madelinecameron/workplace-protocols/internal/idhash"
)

// IDLen is the hex length of a derived id. It matches the other id packages
// so their ids are interchangeable in logs and columns.
const IDLen = idhash.Len

// Kind distinguishes the three things an address can name. The set is closed:
// an address whose kind is not one of these does not parse, because a recipient
// nobody can deliver to is worse than a rejected send.
type Kind string

const (
	// KindAgent names a piece of software that acts: scout, reviewer, a session.
	KindAgent Kind = "agent"
	// KindHuman names a person. Humans are addressed exactly like agents.
	KindHuman Kind = "human"
	// KindRole names a position rather than an occupant: owner/acme,
	// reviewer. The system that delivers to it decides who holds the role.
	KindRole Kind = "role"
)

// ErrMalformed is returned by Parse for anything that is not a valid address.
var ErrMalformed = errors.New("agentid: malformed address")

// Address is a parsed, canonical address. The zero value is not a valid
// address; obtain one from Parse, or from the New* constructors.
type Address struct {
	Kind Kind
	// Name is the canonical name within the kind, lowercased, with segments
	// separated by "/". For agents and humans it is a single segment; for
	// roles it may be qualified ("owner/acme").
	Name string
}

// NewAgent returns the address of a named agent.
func NewAgent(name string) (Address, error) { return newAddr(KindAgent, name) }

// NewHuman returns the address of a named person.
func NewHuman(name string) (Address, error) { return newAddr(KindHuman, name) }

// NewRole returns the address of a role. Segments are joined with "/", so
// NewRole("owner", "acme") and NewRole("owner/acme") are the same address.
func NewRole(segments ...string) (Address, error) {
	return newAddr(KindRole, strings.Join(segments, "/"))
}

// Parse reads an address in "kind:name" form. It is tolerant of the ways the
// same address gets typed by hand — surrounding space, upper case, a leading or
// trailing slash on the name — and canonicalizes them away, because two spellings
// of one recipient is the failure this package exists to prevent.
func Parse(s string) (Address, error) {
	kind, name, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return Address{}, fmt.Errorf("%w: %q has no kind prefix", ErrMalformed, s)
	}
	return newAddr(Kind(strings.ToLower(strings.TrimSpace(kind))), name)
}

// MustParse is Parse for addresses fixed at compile time. It panics on failure.
func MustParse(s string) Address {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// String returns the canonical "kind:name" form. This is the value that goes on
// the wire and into storage; nothing should store the parts separately and
// rejoin them, because that is how the two spellings come back.
func (a Address) String() string { return string(a.Kind) + ":" + a.Name }

// ID is the derived, byte-identical key for this address. Use it where a fixed
// width matters — a column, a channel name, a metrics label — and String()
// where a human will read it.
func (a Address) ID() string { return idhash.Sum("addr:" + a.String()) }

// IsRole reports whether the address names a position rather than an occupant.
func (a Address) IsRole() bool { return a.Kind == KindRole }

// Valid reports whether the address is usable. It is false for the zero value.
func (a Address) Valid() bool { return a.Kind != "" && a.Name != "" }

// MarshalText implements encoding.TextMarshaler, so addresses round-trip through
// JSON, SQL drivers, and flag parsing as their canonical string.
func (a Address) MarshalText() ([]byte, error) {
	if !a.Valid() {
		return nil, fmt.Errorf("%w: zero address", ErrMalformed)
	}
	return []byte(a.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *Address) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// SessionID identifies one run of one agent: one interactive session, one pass
// of a routine, one task. It is derived from the persistent address plus
// whatever distinguishes the run, so attribution can answer "which agent did
// this" (the address) and "which run of it" (the session) without a registry.
//
// The nonce is supplied by the caller rather than generated here, because a
// derived id that changes on every call is not derived. Callers with a natural
// run identifier — a PID and start time, a job id, a git sha — should pass it.
func SessionID(a Address, host, nonce string) string {
	return idhash.Sum("sess:" + a.String() + ":" + strings.ToLower(strings.TrimSpace(host)) + ":" + nonce)
}

// EnvVar is the environment variable every process should read to learn who it
// is. Naming it once here is the point: an agent that guesses its own address
// from the hostname on one box and from a config file on another is two agents.
const EnvVar = "WORKPLACE_PROTOCOLS_AGENT"

// FromEnv resolves the current process's address from EnvVar. It returns an
// error rather than a fallback: an agent that does not know its own name should
// fail at startup, not act under a guessed name nobody can reply to.
func FromEnv() (Address, error) {
	raw := strings.TrimSpace(os.Getenv(EnvVar))
	if raw == "" {
		return Address{}, fmt.Errorf("agentid: %s is unset; every process must be told its own address", EnvVar)
	}
	return Parse(raw)
}

func newAddr(kind Kind, name string) (Address, error) {
	switch kind {
	case KindAgent, KindHuman, KindRole:
	default:
		return Address{}, fmt.Errorf("%w: unknown kind %q", ErrMalformed, kind)
	}

	canon, err := canonicalName(name)
	if err != nil {
		return Address{}, err
	}
	if kind != KindRole && strings.Contains(canon, "/") {
		return Address{}, fmt.Errorf("%w: %s name %q is qualified; only roles have segments", ErrMalformed, kind, canon)
	}
	return Address{Kind: kind, Name: canon}, nil
}

// canonicalName lowercases, collapses separator noise, and rejects anything that
// would make an address ambiguous or unsafe to interpolate into a channel name.
func canonicalName(name string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.Trim(s, "/")
	if s == "" {
		return "", fmt.Errorf("%w: empty name", ErrMalformed)
	}
	if len(s) > 128 {
		return "", fmt.Errorf("%w: name longer than 128 bytes", ErrMalformed)
	}

	segments := strings.Split(s, "/")
	for _, seg := range segments {
		if seg == "" {
			return "", fmt.Errorf("%w: %q has an empty segment", ErrMalformed, name)
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			default:
				return "", fmt.Errorf("%w: %q contains %q", ErrMalformed, name, r)
			}
		}
	}
	return strings.Join(segments, "/"), nil
}
