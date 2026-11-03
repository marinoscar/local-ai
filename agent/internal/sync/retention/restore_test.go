package retention

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// rot flips the content of a file without changing its size, which is what bit rot looks like to the
// cheap load check (it still loads) and to the re-hash (it does not match).
func rot(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreFromTrash(t *testing.T) {
	long := now.Add(-40 * 24 * time.Hour)
	setup := func(t *testing.T) (*rig, string, string) {
		r := newRig(t)
		live := r.install("internal", "qwen-x", "rev2", long)
		tr := r.trash("internal", "qwen-x", "rev1", long, 2*24*time.Hour)
		rot(t, filepath.Join(live, "w.gguf"))
		return r, live, tr
	}

	t.Run("a rotten live copy and a verified older version: restored, re-hashed, marked verified", func(t *testing.T) {
		r, live, tr := setup(t)
		oldBody, _ := os.ReadFile(filepath.Join(tr, "w.gguf"))
		res, err := r.ret.Restore(bg, "internal", "qwen-x")
		if err != nil {
			t.Fatal(err)
		}
		if res.Version != "rev1" || res.Dir != live || res.Quarantine == "" {
			t.Fatalf("%+v", res)
		}
		// The old bytes are back in place, the manifest names the old version, re-verified now.
		got, _ := os.ReadFile(filepath.Join(live, "w.gguf"))
		if string(got) != string(oldBody) {
			t.Fatal("the old file is not back")
		}
		m, err := manifest.Load(config.OSFS{}, live)
		if err != nil || m.Version != "rev1" || !m.LastReverifiedAt.Equal(now) {
			t.Fatalf("manifest %+v, %v", m, err)
		}
		// The index serves the restored version.
		c, err := r.idx.BestCopy("qwen-x")
		if err != nil || c.Manifest.Version != "rev1" {
			t.Fatalf("best copy %+v, %v", c.Manifest.Version, err)
		}
		// The bad files and the old manifest are in quarantine, with a reason; nothing was deleted.
		for _, f := range []string{"reason.json", "MANIFEST.json", filepath.Join("new", "w.gguf")} {
			if !pathExists(filepath.Join(res.Quarantine, f)) {
				t.Errorf("quarantine lacks %s", f)
			}
		}
		// The trash entry gave its files up and is tidied.
		if pathExists(tr) {
			t.Error("the emptied trash entry is left")
		}
		// The journal says verified at the restored version.
		if len(r.items.updated) != 1 || r.items.updated[0].State != journal.StateVerified || r.items.updated[0].Version != "rev1" || r.items.updated[0].Code != "" {
			t.Fatalf("journal = %+v", r.items.updated)
		}
	})

	t.Run("it works when the caller already moved the bad files to quarantine", func(t *testing.T) {
		r, live, _ := setup(t)
		q := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", "20261101T000000Z")
		_ = os.MkdirAll(q, 0o700)
		_ = os.Rename(filepath.Join(live, "w.gguf"), filepath.Join(q, "w.gguf"))
		_ = os.Rename(filepath.Join(live, "MANIFEST.json"), filepath.Join(q, "MANIFEST.json"))
		r.rebuild()
		if _, err := r.idx.BestCopy("qwen-x"); err == nil {
			t.Fatal("a copy with no manifest is present")
		}
		res, err := r.ret.Restore(bg, "internal", "qwen-x")
		if err != nil || res.Version != "rev1" {
			t.Fatalf("%+v, %v", res, err)
		}
		if res.Quarantine != "" {
			t.Errorf("a second quarantine was made for nothing: %s", res.Quarantine)
		}
		if c, err := r.idx.BestCopy("qwen-x"); err != nil || c.Manifest.Version != "rev1" {
			t.Fatalf("%v", err)
		}
	})

	t.Run("a corrupt older version is not restored: no candidate", func(t *testing.T) {
		r, live, tr := setup(t)
		rot(t, filepath.Join(tr, "w.gguf"))
		if _, err := r.ret.Restore(bg, "internal", "qwen-x"); !errors.Is(err, ErrNoRestorable) {
			t.Fatalf("err = %v", err)
		}
		// Nothing moved: the live files and the trash are as they were.
		if !pathExists(filepath.Join(live, "MANIFEST.json")) || !pathExists(filepath.Join(tr, "w.gguf")) {
			t.Fatal("a refused restore moved something")
		}
	})

	t.Run("the next best verified version is taken when the newest in the trash is bad", func(t *testing.T) {
		r, live, _ := setup(t)
		newer := r.trash("internal", "qwen-x", "rev1b", long, time.Hour)
		rot(t, filepath.Join(newer, "w.gguf"))
		res, err := r.ret.Restore(bg, "internal", "qwen-x")
		if err != nil || res.Version != "rev1" {
			t.Fatalf("%+v, %v", res, err)
		}
		if !pathExists(newer) || !pathExists(filepath.Join(live, "w.gguf")) {
			t.Fatal("the bad trash entry must stay for the person to look at")
		}
	})

	t.Run("no trash at all", func(t *testing.T) {
		r := newRig(t)
		r.install("internal", "qwen-x", "rev2", long)
		if _, err := r.ret.Restore(bg, "internal", "qwen-x"); !errors.Is(err, ErrNoRestorable) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a read-only root is refused", func(t *testing.T) {
		r, _, tr := setup(t)
		r.roots[0].ReadOnly = true
		var refused *RefusedError
		if _, err := r.ret.Restore(bg, "internal", "qwen-x"); !errors.As(err, &refused) || refused.Reason != ReasonReadOnly {
			t.Fatalf("err = %v", err)
		}
		if !pathExists(filepath.Join(tr, "w.gguf")) {
			t.Fatal("touched")
		}
	})

	t.Run("an item that is being swapped is left alone", func(t *testing.T) {
		r, _, _ := setup(t)
		r.setState("internal", "qwen-x", journal.StateSwapping)
		if _, err := r.ret.Restore(bg, "internal", "qwen-x"); !errors.Is(err, ErrItemBusy) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an unknown root", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.ret.Restore(bg, "ark-ffffffff", "qwen-x"); err == nil {
			t.Fatal("accepted")
		}
	})

	t.Run("a Restore cut short after the files moved is finished by running it again", func(t *testing.T) {
		r, live, tr := setup(t)
		// The state a crash leaves after step 2: the old files are in the item directory, the live
		// manifest is gone, and only the old manifest is left in .trash.
		_ = os.Remove(filepath.Join(live, "MANIFEST.json"))
		_ = os.Remove(filepath.Join(live, "w.gguf"))
		if err := os.Rename(filepath.Join(tr, "w.gguf"), filepath.Join(live, "w.gguf")); err != nil {
			t.Fatal(err)
		}
		r.rebuild()
		res, err := r.ret.Restore(bg, "internal", "qwen-x")
		if err != nil || res.Version != "rev1" {
			t.Fatalf("%+v, %v", res, err)
		}
		if c, err := r.idx.BestCopy("qwen-x"); err != nil || c.Manifest.Version != "rev1" {
			t.Fatalf("%v", err)
		}
	})
}

// A restored copy is a copy like any other: it is the one thing left, so R1 protects it.
func TestARestoredCopyIsProtectedLikeAnyOther(t *testing.T) {
	r := newRig(t)
	long := now.Add(-40 * 24 * time.Hour)
	live := r.install("internal", "qwen-x", "rev2", long)
	r.trash("internal", "qwen-x", "rev1", long, time.Hour)
	rot(t, filepath.Join(live, "w.gguf"))
	if _, err := r.ret.Restore(bg, "internal", "qwen-x"); err != nil {
		t.Fatal(err)
	}
	if ok, reason := r.ret.CanDeleteItem("internal", "qwen-x"); ok || reason != ReasonLastVerifiedCopy {
		t.Fatalf("%v %q", ok, reason)
	}
	// And the first prune that follows keeps it.
	res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"qwen-x"}})})
	if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
}
