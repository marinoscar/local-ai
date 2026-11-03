package item

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func TestDirectoryItemVerification(t *testing.T) {
	t.Run("a built tree is hashed, listed in SHA256SUMS and installed", func(t *testing.T) {
		e := newEnv(t)
		res := e.m.Run(bg, e.startRun(), e.dirJob("rev1", docs1()))
		if res.State != journal.StateVerified {
			t.Fatalf("result = %+v", res)
		}
		assertContent(t, e.docsDir(), docs1())
		sums, err := manifest.ReadSums(OSFS{}, e.docsDir())
		if err != nil || len(sums) != 3 {
			t.Fatalf("SHA256SUMS = %v, %v", sums, err)
		}
		for _, s := range sums {
			if s.SHA256 != sha(docs1()[s.Path]) {
				t.Fatalf("sum of %s is wrong", s.Path)
			}
		}
	})
	t.Run("the tree hash the resolver expects must match", func(t *testing.T) {
		e := newEnv(t)
		job := e.dirJob("rev1", docs1())
		var sums []manifest.Sum
		for name, b := range docs1() {
			sums = append(sums, manifest.Sum{SHA256: sha(b), Path: name})
		}
		want, _ := manifest.TreeSHA256(sums)
		job.Resolution.ExpectedTree = want
		if res := e.m.Run(bg, e.startRun(), job); res.State != journal.StateVerified {
			t.Fatalf("a matching tree hash: %+v", res)
		}
		e2 := newEnv(t)
		job = e2.dirJob("rev1", docs1())
		job.Resolution.ExpectedTree = sha([]byte("another tree"))
		res := e2.m.Run(bg, e2.startRun(), job)
		if res.State != journal.StateQuarantined || res.Code != doctor.SYN020 {
			t.Fatalf("a wrong tree hash: %+v", res)
		}
		if !pathExists(filepath.Join(res.Quarantined, TreeDir, "index.html")) || pathExists(e2.docsDir()) {
			t.Fatal("the tree was not moved to quarantine, or something was installed")
		}
	})
	t.Run("a symlink in the tree is never staged", func(t *testing.T) {
		e := newEnv(t)
		job := e.dirJob("rev1", docs1())
		build := job.Resolution.Build
		job.Resolution.Build = func(ctx context.Context, tree string) error {
			if err := build(ctx, tree); err != nil {
				return err
			}
			return os.Symlink("/etc/hostname", filepath.Join(tree, "link"))
		}
		res := e.m.Run(bg, e.startRun(), job)
		if res.State != journal.StateQuarantined || res.Reason != "not_regular" {
			t.Fatalf("result = %+v", res)
		}
		if pathExists(e.docsDir()) {
			t.Fatal("a tree with a symlink was installed")
		}
	})
	t.Run("an empty tree is refused", func(t *testing.T) {
		e := newEnv(t)
		job := e.dirJob("rev1", docs1())
		job.Resolution.Build = func(ctx context.Context, tree string) error { return nil }
		if res := e.m.Run(bg, e.startRun(), job); res.State != journal.StateQuarantined || res.Reason != "empty" {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("a failing build is a failure with the resolver's code", func(t *testing.T) {
		e := newEnv(t)
		job := e.dirJob("rev1", docs1())
		job.Resolution.Build = func(ctx context.Context, tree string) error {
			return &ResolveError{Code: doctor.SYN061, Permanent: true, Err: errors.New("git is missing")}
		}
		res := e.m.Run(bg, e.startRun(), job)
		if res.State != journal.StateFailed || res.Code != doctor.SYN061 || res.Reason != "build" || res.Retryable {
			t.Fatalf("result = %+v", res)
		}
		job.Resolution.Build = func(ctx context.Context, tree string) error { return errors.New("network blip") }
		if res = e.m.Run(bg, e.startRun(), job); res.State != journal.StateFailed || res.Code != CodeUnresolved || !res.Retryable {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("a cancelled build leaves the item resumable", func(t *testing.T) {
		e := newEnv(t)
		ctx, cancel := context.WithCancel(bg)
		job := e.dirJob("rev1", docs1())
		job.Resolution.Build = func(ctx context.Context, tree string) error { cancel(); return ctx.Err() }
		res := e.m.Run(ctx, e.startRun(), job)
		if !res.Cancelled || res.State != journal.StateDownloading {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("a tree made of files (a local copy) lands under tree/ and is checked per file", func(t *testing.T) {
		e := newEnv(t)
		c := docs1()
		var files []File
		for _, name := range sortedKeys(c) {
			e.fetch.data[TreeDir+"/"+name] = c[name]
			files = append(files, File{Path: name, URL: "https://example.test/" + name, SHA256: sha(c[name])})
		}
		job := e.dirJob("rev1", c)
		job.Resolution.Build, job.Resolution.Files = nil, files
		res := e.m.Run(bg, e.startRun(), job)
		if res.State != journal.StateVerified {
			t.Fatalf("result = %+v", res)
		}
		assertContent(t, e.docsDir(), c)
		// One file that is not what the source said fails the whole tree.
		e2 := newEnv(t)
		for _, name := range sortedKeys(c) {
			e2.fetch.data[TreeDir+"/"+name] = c[name]
		}
		bad := append([]File(nil), files...)
		bad[0].SHA256 = sha([]byte("something else"))
		job = e2.dirJob("rev1", c)
		job.Resolution.Build, job.Resolution.Files = nil, bad
		if res := e2.m.Run(bg, e2.startRun(), job); res.State != journal.StateQuarantined {
			t.Fatalf("a wrong file: %+v", res)
		}
	})
}

// recGate records what the machine takes from the gate.
type recGate struct {
	mu       sync.Mutex
	networks int
	excl     []string
	released int
}

func (g *recGate) Network(context.Context) (func(), error) {
	g.mu.Lock()
	g.networks++
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.released++; g.mu.Unlock() }, nil
}

func (g *recGate) Exclusive(_ context.Context, key string) (func(), error) {
	g.mu.Lock()
	g.excl = append(g.excl, key)
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.released++; g.mu.Unlock() }, nil
}

func TestTheGateIsUsedForTheNetworkAndForExclusiveBuilds(t *testing.T) {
	e := newEnv(t)
	g := &recGate{}
	job := e.fileJob("rev1", v1(), true)
	job.Gate = g
	if res := e.m.Run(bg, e.startRun(), job); res.State != journal.StateVerified {
		t.Fatalf("result = %+v", res)
	}
	if g.networks != 1 || g.released != 1 {
		t.Fatalf("network slots taken %d, released %d: one slot for the whole download phase", g.networks, g.released)
	}
	// A local copy takes no network slot.
	e2 := newEnv(t)
	g2 := &recGate{}
	job = e2.fileJob("rev1", v1(), true)
	job.Gate = g2
	job.Resolution.Local = true
	e2.m.Run(bg, e2.startRun(), job)
	if g2.networks != 0 {
		t.Fatalf("a local copy took %d network slots", g2.networks)
	}
	// An exclusive build holds its resource for the build only.
	e3 := newEnv(t)
	g3 := &recGate{}
	job = e3.dirJob("rev1", docs1())
	job.Gate = g3
	job.Resolution.Exclusive = "pmtiles"
	if res := e3.m.Run(bg, e3.startRun(), job); res.State != journal.StateVerified {
		t.Fatalf("result = %+v", res)
	}
	if !reflect.DeepEqual(g3.excl, []string{"pmtiles"}) {
		t.Fatalf("exclusive = %v", g3.excl)
	}
}
