package repoid

import "testing"

// The same project cloned over ssh on one machine and https on another must
// resolve to one id, or a fleet view splits a single project in two.
func TestCanonicalRemoteUnifiesURLForms(t *testing.T) {
	forms := []string{
		"git@github.com:example/project.git",
		"https://github.com/example/project.git",
		"https://github.com/example/project",
		"ssh://git@github.com/example/project.git",
		"git@GitHub.com:example/project.git",
		"https://alice@github.com/example/project.git",
		"git@github.com:example/project.git/",
	}
	want := CanonicalRemote(forms[0])
	if want != "github.com/example/project" {
		t.Fatalf("canonical = %q, want github.com/example/project", want)
	}
	for _, f := range forms {
		if got := CanonicalRemote(f); got != want {
			t.Errorf("CanonicalRemote(%q) = %q, want %q", f, got, want)
		}
	}
}

func TestCanonicalRemoteDistinguishesDifferentRepos(t *testing.T) {
	a := CanonicalRemote("git@github.com:example/project.git")
	b := CanonicalRemote("git@github.com:example/other.git")
	c := CanonicalRemote("git@gitlab.com:example/project.git")
	if a == b || a == c || b == c {
		t.Errorf("distinct repos collided: %q %q %q", a, b, c)
	}
}

func TestCanonicalRemoteDropsPort(t *testing.T) {
	a := CanonicalRemote("ssh://git@example.com:2222/example/project.git")
	b := CanonicalRemote("ssh://git@example.com/example/project.git")
	if a != b {
		t.Errorf("port changed identity: %q vs %q", a, b)
	}
}

func TestRepoIDStableAcrossPathsWithSameRemote(t *testing.T) {
	remote := "git@github.com:example/project.git"
	a := RepoID(remote, "/home/alice/code/project")
	b := RepoID(remote, "/Users/alice/src/project")
	if a != b {
		t.Errorf("RepoID differs by path: %q vs %q", a, b)
	}
}

func TestRepoIDFallsBackToPathWithoutRemote(t *testing.T) {
	a := RepoID("", "/home/alice/code/scratch")
	b := RepoID("", "/home/alice/code/other")
	if a == b {
		t.Error("remoteless repos at different paths collided")
	}
	if a == "" || len(a) != IDLen {
		t.Errorf("id = %q, want %d hex chars", a, IDLen)
	}
}

// Worktrees and second clones of one project share a RepoID, so state documents
// must be keyed by path to avoid overwriting each other.
func TestPathIDDistinguishesWorktreesOfSameRepo(t *testing.T) {
	a := PathID("laptop", "/code/tracker")
	b := PathID("laptop", "/code/tracker-wt-fix")
	if a == b {
		t.Error("worktree and main checkout collided")
	}
	if PathID("laptop", "/code/tracker") == PathID("devbox", "/code/tracker") {
		t.Error("same path on different machines collided")
	}
}

// RepoID values are stored and joined on across tools, so the canonical form
// and the derived id must never change.
func TestRepoIDGoldenValueIsStable(t *testing.T) {
	const remote = "git@github.com:example/project.git"
	if got := CanonicalRemote(remote); got != "github.com/example/project" {
		t.Fatalf("canonical drifted: %q", got)
	}
	if got := RepoID(remote, "/anywhere"); got != "fe7277060101" {
		t.Fatalf("id drifted: %q, want fe7277060101", got)
	}
}
