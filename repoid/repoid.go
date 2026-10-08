// Package repoid derives the stable keys used to correlate git repositories
// across machines and tools. The identity is a hash of the canonicalized remote
// URL, so a project cloned over ssh on one machine and https on another — or at
// different paths — resolves to one id.
//
// It is shared, not copied, because correlation only works if every tool
// computes a byte-identical id. One tool may key its state by RepoID while
// another looks a service's repo up by the same id. A drift in this function
// silently breaks that join.
package repoid

import (
	"strings"

	"github.com/madelinecameron/workplace-protocols/internal/idhash"
)

// IDLen is the hex length of a derived id. It matches the other id packages
// so their ids are interchangeable in logs and columns.
const IDLen = idhash.Len

// RepoID identifies a project across machines. It is derived from the canonical
// remote URL so clones at different paths on different machines resolve to the
// same id. Repositories without a remote fall back to a path-derived id, which
// is necessarily machine-local.
func RepoID(remote, absPath string) string {
	if remote == "" {
		return idhash.Sum("path:" + absPath)
	}
	return idhash.Sum("remote:" + CanonicalRemote(remote))
}

// PathID identifies one working tree on one machine. It keys state documents so
// that several clones or worktrees of the same project never collide.
func PathID(machine, absPath string) string {
	return idhash.Sum("wt:" + machine + ":" + absPath)
}

// CanonicalRemote normalizes a remote URL so that ssh, https, and scp-style
// forms of the same repository compare equal.
func CanonicalRemote(remote string) string {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if strings.Contains(s, "@") && strings.Contains(s, ":") {
		// scp-style: git@github.com:owner/repo
		s = strings.Replace(s, ":", "/", 1)
	}

	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}

	host, path, ok := strings.Cut(s, "/")
	if !ok {
		return strings.ToLower(s)
	}
	host, _, _ = strings.Cut(host, ":") // drop any port
	return strings.ToLower(host) + "/" + strings.Trim(path, "/")
}
