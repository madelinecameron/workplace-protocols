// Package repoid derives the stable keys used to correlate git repositories
// across machines and tools. The identity is a hash of the canonicalized remote
// URL, so a project cloned over ssh on one machine and https on another — or at
// different paths — resolves to one id.
//
// Every tool that keys data by repository should use this package rather than
// its own copy: if one tool stores by RepoID and another looks up by it, any
// difference between their copies silently breaks the lookup.
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

// PathID identifies one working tree on one machine, so several clones or
// worktrees of the same repository get different ids.
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
