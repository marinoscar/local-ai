package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestPreflightOfExplicitAndUncheckedSources(t *testing.T) {
	t.Run("--source with a plain tree", func(t *testing.T) {
		k := newKitEnv(t)
		dir := t.TempDir()
		k.write(filepath.Join(dir, "CMakeLists.txt"), "project(x)\n")
		k.req.SourceDir, k.req.Commit = dir, testSHA
		rep := k.manager().PreflightReport(context.Background(), k.req)
		if !rep.OK() || rep.Source != "dir" {
			t.Fatalf("failed %v source %q", failedIDs(rep), rep.Source)
		}
	})
	t.Run("--source with a short commit and no history", func(t *testing.T) {
		k := newKitEnv(t)
		dir := t.TempDir()
		k.write(filepath.Join(dir, "CMakeLists.txt"), "project(x)\n")
		k.req.SourceDir, k.req.Commit = dir, testSHA[:9]
		rep := k.manager().PreflightReport(context.Background(), k.req)
		if got := failedIDs(rep); len(got) != 1 || got[0] != "source:"+doctor.LLM018 {
			t.Fatalf("failed %v", got)
		}
	})
	t.Run("--source that is not a tree", func(t *testing.T) {
		k := newKitEnv(t)
		k.req.SourceDir, k.req.Commit = t.TempDir(), testSHA
		if rep := k.manager().PreflightReport(context.Background(), k.req); rep.OK() {
			t.Fatal("an empty directory is not a source")
		}
		k.req.SourceDir = filepath.Join(t.TempDir(), "missing")
		if rep := k.manager().PreflightReport(context.Background(), k.req); rep.OK() {
			t.Fatal("a missing directory is not a source")
		}
	})
	t.Run("--source that is a git checkout", func(t *testing.T) {
		k := newKitEnv(t)
		dir := t.TempDir()
		k.write(filepath.Join(dir, ".git", "HEAD"), "ref")
		g := &kitGit{have: true, sha: testSHA}
		k.exec.on(g.handler)
		k.req.SourceDir, k.req.Commit = dir, testSHA
		if rep := k.manager().PreflightReport(context.Background(), k.req); !rep.OK() {
			t.Fatalf("failed %v", failedIDs(rep))
		}
		g.have = false
		if rep := k.manager().PreflightReport(context.Background(), k.req); rep.OK() {
			t.Fatal("a checkout without the commit is not a source")
		}
	})
	t.Run("a source that cannot be checked is assumed to work, a network one only online", func(t *testing.T) {
		k := newKitEnv(t)
		k.req.FromKit = false
		k.req.Sources = []Source{stubSource{name: "legacy", sha: testSHA}}
		rep := k.manager().PreflightReport(context.Background(), k.req)
		if !rep.OK() || rep.Source != "legacy" {
			t.Fatalf("failed %v source %q", failedIDs(rep), rep.Source)
		}
		k.req.Sources = []Source{stubSource{name: "web", network: true, sha: testSHA}}
		k.req.Offline = true
		if rep := k.manager().PreflightReport(context.Background(), k.req); rep.OK() {
			t.Fatal("offline, a network source is not a source")
		}
		k.req.Offline = false
		if rep := k.manager().PreflightReport(context.Background(), k.req); !rep.OK() || rep.Source != "web" {
			t.Fatalf("online: failed %v source %q", failedIDs(rep), rep.Source)
		}
	})
	t.Run("a source that fails for another reason", func(t *testing.T) {
		k := newKitEnv(t)
		k.req.FromKit = false
		k.req.Sources = []Source{checkerSource{err: errors.New("disk on fire")}}
		rep := k.manager().PreflightReport(context.Background(), k.req)
		if got := failedIDs(rep); len(got) != 1 || got[0] != "source:"+doctor.LLM018 {
			t.Fatalf("failed %v", got)
		}
	})
}

// checkerSource is a Source whose preflight answer is scripted.
type checkerSource struct{ err error }

func (checkerSource) Name() string  { return "odd" }
func (checkerSource) Network() bool { return false }
func (checkerSource) Fetch(context.Context, SourceEnv, string, string) (string, error) {
	return "", errors.New("not used")
}
func (c checkerSource) Check(context.Context, SourceEnv, string) (SourceProbe, error) {
	return SourceProbe{}, c.err
}

// swapFS calls on(path) at every Open.
type swapFS struct {
	OSFS
	on func(path string)
}

func (f swapFS) Open(path string) (File, error) {
	f.on(path)
	return f.OSFS.Open(path)
}

func TestAKitTarballThatChangesWhileItIsReadIsRefused(t *testing.T) {
	k := newKitEnv(t)
	good := kitTree(t, testSHA)
	path := k.kitTarball(testSHA, good, "src", "")
	// The file is replaced after the check (which hashed the original) and before the extraction
	// reads it: the digest over the bytes that are extracted catches it.
	opens := 0
	k.opts.FS = swapFS{OSFS: OSFS{}, on: func(p string) {
		if p != path {
			return
		}
		opens++
		if opens == 2 { // the first Open is the check, the second the extraction
			other := append(append([]byte{}, good...), 0)
			_ = os.WriteFile(path, other, 0o644)
		}
	}}
	_, err := k.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT001 {
		t.Fatalf("got %v (opens %d)", e, opens)
	}
	if k.exists("src/" + testSHA + "/CMakeLists.txt") {
		t.Error("a tree from a file that changed was kept")
	}
}
