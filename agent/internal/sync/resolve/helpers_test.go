package resolve

import (
	"context"
	"crypto/sha1" //nolint:gosec // a git object id, not a security hash
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fixedNow is the one clock reading of a test file; fixtures served and expected are both built from it.
var fixedNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// needTool skips the test with a clear message when a real tool is not installed.
func needTool(t *testing.T, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names {
		p, err := execx.Lookup(n)
		if err != nil {
			t.Skipf("%s is not installed: this test needs the real tool", n)
		}
		out[n] = p
	}
	return out
}

// toolEnv is the environment of the helper git calls of the tests.
func toolEnv(home string) []string {
	return []string{
		"PATH=" + execx.FixedPath,
		"HOME=" + home,
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Person A",
		"GIT_AUTHOR_EMAIL=person.a@example.test",
		"GIT_COMMITTER_NAME=Person A",
		"GIT_COMMITTER_EMAIL=person.a@example.test",
		"GIT_AUTHOR_DATE=" + fixedNow.Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + fixedNow.Format(time.RFC3339),
	}
}

// runTool runs a real tool for fixture building and fails the test on a non-zero exit.
func runTool(t *testing.T, env []string, bin string, args ...string) string {
	t.Helper()
	res, err := execx.Exec{}.Run(context.Background(), execx.Cmd{Path: bin, Args: args, Env: env, Timeout: time.Minute, MaxOut: 8 << 20})
	if err != nil {
		t.Fatalf("%s %v: %v", bin, args, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("%s %v exited %d: %s", bin, args, res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// upstream is a local git repository that stands in for the origin.
type upstream struct {
	t    *testing.T
	git  string
	env  []string
	work string
	Bare string
}

// newUpstream makes a work repository and a bare clone of it (the "origin" the mirror clones).
func newUpstream(t *testing.T) *upstream {
	t.Helper()
	git := needTool(t, "git")["git"]
	dir := t.TempDir()
	u := &upstream{t: t, git: git, env: toolEnv(dir), work: filepath.Join(dir, "work"), Bare: filepath.Join(dir, "origin.git")}
	runTool(t, u.env, git, "init", "-q", "-b", "main", u.work)
	u.commit("README", "first\n")
	runTool(t, u.env, git, "clone", "-q", "--bare", u.work, u.Bare)
	return u
}

// commit adds or changes a file, commits it and pushes it to the bare origin; it returns the commit id.
func (u *upstream) commit(name, content string) string {
	u.t.Helper()
	if err := os.WriteFile(filepath.Join(u.work, name), []byte(content), 0o644); err != nil {
		u.t.Fatal(err)
	}
	runTool(u.t, u.env, u.git, "-C", u.work, "add", "-A")
	runTool(u.t, u.env, u.git, "-C", u.work, "commit", "-q", "-m", "change "+name)
	if _, err := os.Stat(u.Bare); err == nil {
		runTool(u.t, u.env, u.git, "-C", u.work, "push", "-q", u.Bare, "HEAD:refs/heads/main")
	}
	return u.head()
}

func (u *upstream) head() string {
	return runTool(u.t, u.env, u.git, "-C", u.work, "rev-parse", "HEAD")
}

// absentCommit is a well-formed commit id no repository has.
func absentCommit(seed string) string {
	h := sha1.Sum([]byte("absent-commit-" + seed)) //nolint:gosec
	return hex.EncodeToString(h[:])
}

// testEnv is an Env for the real-tool tests: online, a private sync home, the fixed clock.
func testEnv(t *testing.T) *Env {
	t.Helper()
	return &Env{
		Online:   func() bool { return true },
		Now:      func() time.Time { return fixedNow },
		SyncHome: t.TempDir(),
	}
}

// contentItem builds a catalog item around a source.
func contentItem(id, kind string, src catalog.ContentSource) plan.CatalogItem {
	return plan.ContentItem(&catalog.ContentItem{ID: id, Kind: kind, Source: src})
}
