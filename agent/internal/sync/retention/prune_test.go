package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// orphans is an OrphanLister over a fixed table (the planner's job in production).
func orphans(table map[string][]string) OrphanLister {
	return func(_ context.Context, root ark.Root) ([]string, error) { return table[root.ID], nil }
}

func TestPruneOrphans(t *testing.T) {
	fresh := now.Add(-time.Hour)
	t.Run("an orphan with another fresh verified copy goes", func(t *testing.T) {
		r := newRig(t)
		gone := r.install("internal", "osm-north-america", "20260501", fresh)
		keep := r.install(driveID, "osm-north-america", "20260501", fresh)
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"osm-north-america"}})})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Deleted) != 1 || len(res.Refused) != 0 {
			t.Fatalf("%+v", res)
		}
		d := res.Deleted[0]
		if d.Root != "internal" || d.Item != "osm-north-america" || d.Version != "20260501" || d.Kind != PruneKindOrphan || d.Bytes == 0 || res.FreedBytes != d.Bytes {
			t.Fatalf("deleted = %+v, freed %d", d, res.FreedBytes)
		}
		if pathExists(filepath.Join(gone, "w.gguf")) || !pathExists(filepath.Join(keep, "w.gguf")) {
			t.Fatal("the wrong copy went")
		}
	})

	t.Run("the only verified copy is always refused: last_verified_copy, nothing deleted", func(t *testing.T) {
		r := newRig(t)
		dir := r.install("internal", "gutenberg-en", "20260501", fresh)
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"gutenberg-en"}})})
		if err != nil {
			t.Fatal(err)
		}
		want := []PruneRefusal{{Root: "internal", Item: "gutenberg-en", Version: "20260501", Reason: ReasonLastVerifiedCopy, Kind: PruneKindOrphan}}
		if len(res.Deleted) != 0 || !reflect.DeepEqual(res.Refused, want) || res.FreedBytes != 0 {
			t.Fatalf("%+v", res)
		}
		if !pathExists(filepath.Join(dir, "w.gguf")) || !pathExists(filepath.Join(dir, "MANIFEST.json")) {
			t.Fatal("a refused prune touched the copy")
		}
	})

	t.Run("a copy that is only on a drive that is not mounted does not save the orphan", func(t *testing.T) {
		r := newRig(t)
		r.install(driveID, "gutenberg-en", "20260501", fresh)
		dir := r.install("internal", "gutenberg-en", "20260501", fresh)
		r.roots[1].State = ark.StateNotMounted
		r.rebuild()
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"gutenberg-en"}})})
		if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 1 || res.Refused[0].Reason != ReasonLastVerifiedCopy {
			t.Fatalf("%+v, %v", res, err)
		}
		if !pathExists(filepath.Join(dir, "w.gguf")) {
			t.Fatal("deleted")
		}
	})

	t.Run("a copy not re-verified within 24 h does not save the orphan", func(t *testing.T) {
		r := newRig(t)
		r.install(driveID, "gutenberg-en", "20260501", now.Add(-48*time.Hour))
		r.install("internal", "gutenberg-en", "20260501", fresh)
		res, _ := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"gutenberg-en"}})})
		if len(res.Deleted) != 0 || len(res.Refused) != 1 {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("an orphan on two roots: one copy goes, the last one is kept (live state, one at a time)", func(t *testing.T) {
		r := newRig(t)
		internal := r.install("internal", "gutenberg-en", "20260501", fresh)
		drive := r.install(driveID, "gutenberg-en", "20260501", fresh)
		list := orphans(map[string][]string{"internal": {"gutenberg-en"}, driveID: {"gutenberg-en"}})
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: list})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Deleted) != 1 || res.Deleted[0].Root != "internal" || len(res.Refused) != 1 || res.Refused[0].Root != driveID || res.Refused[0].Reason != ReasonLastVerifiedCopy {
			t.Fatalf("%+v", res)
		}
		if pathExists(filepath.Join(internal, "w.gguf")) || !pathExists(filepath.Join(drive, "w.gguf")) {
			t.Fatal("not exactly one copy is left")
		}
	})

	t.Run("dry_run deletes nothing and says what a real run would do, last copy included", func(t *testing.T) {
		r := newRig(t)
		internal := r.install("internal", "gutenberg-en", "20260501", fresh)
		drive := r.install(driveID, "gutenberg-en", "20260501", fresh)
		list := orphans(map[string][]string{"internal": {"gutenberg-en"}, driveID: {"gutenberg-en"}})
		dry, err := r.ret.Prune(bg, PruneRequest{Orphans: true, DryRun: true, Orphan: list})
		if err != nil {
			t.Fatal(err)
		}
		if !pathExists(filepath.Join(internal, "w.gguf")) || !pathExists(filepath.Join(drive, "w.gguf")) || len(r.items.deleted) != 0 {
			t.Fatal("a dry run deleted")
		}
		real, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: list})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dry, real) {
			t.Fatalf("dry run %+v\nreal run %+v", dry, real)
		}
	})

	t.Run("a read-only root is refused per orphan, read_only", func(t *testing.T) {
		r := newRig(t)
		r.install(driveID, "gutenberg-en", "20260501", fresh)
		ro := r.install("internal", "gutenberg-en", "20260501", fresh)
		r.roots[0].ReadOnly = true
		res, err := r.ret.Prune(bg, PruneRequest{Roots: []string{"internal"}, Orphans: true, Orphan: orphans(map[string][]string{"internal": {"gutenberg-en"}})})
		if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 1 || res.Refused[0].Reason != ReasonReadOnly {
			t.Fatalf("%+v, %v", res, err)
		}
		if !pathExists(filepath.Join(ro, "w.gguf")) {
			t.Fatal("a read-only root was modified")
		}
		// Without naming a root, a read-only root is simply not pruned.
		res, err = r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"gutenberg-en"}})})
		if err != nil || len(res.Deleted) != 0 || len(res.Refused) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})

	t.Run("an id the planner lists but the root does not hold is skipped", func(t *testing.T) {
		r := newRig(t)
		r.install("internal", "other", "1", fresh)
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"ghost"}})})
		if err != nil || len(res.Deleted)+len(res.Refused) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})

	t.Run("kit/config is never listed, never deleted (R9)", func(t *testing.T) {
		r := newRig(t)
		cfg := filepath.Join(r.root("internal").Path, "kit", "config")
		_ = os.MkdirAll(cfg, 0o755)
		_ = os.WriteFile(filepath.Join(cfg, "node.yaml"), []byte("x"), 0o644)
		res, err := r.ret.Prune(bg, PruneRequest{Orphans: true, Trash: true, Orphan: orphans(nil)})
		if err != nil || len(res.Deleted) != 0 || !pathExists(filepath.Join(cfg, "node.yaml")) {
			t.Fatalf("%+v, %v", res, err)
		}
	})
}

func TestPruneTrash(t *testing.T) {
	fresh := now.Add(-time.Hour)
	r := newRig(t)
	r.install("internal", "qwen-x", "rev3", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	young := r.trash("internal", "qwen-x", "rev2", fresh, time.Hour) // prune ignores age
	old := r.trash("internal", "qwen-x", "rev1", fresh, 30*24*time.Hour)
	r.install("internal", "gemma-y", "rev2", fresh)
	r.setState("internal", "gemma-y", journal.StateSwapped)
	held := r.trash("internal", "gemma-y", "rev1", fresh, 30*24*time.Hour)

	dry, err := r.ret.Prune(bg, PruneRequest{Trash: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !pathExists(young) || !pathExists(old) || !pathExists(held) {
		t.Fatal("a dry run deleted")
	}
	res, err := r.ret.Prune(bg, PruneRequest{Trash: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dry, res) {
		t.Fatalf("dry %+v\nreal %+v", dry, res)
	}
	if len(res.Deleted) != 2 || res.Deleted[0].Version != "rev1" || res.Deleted[1].Version != "rev2" || res.Deleted[0].Kind != PruneKindTrash {
		t.Fatalf("deleted = %+v", res.Deleted)
	}
	want := []PruneRefusal{{Root: "internal", Item: "gemma-y", Version: "rev1", Reason: ReasonReplacementUnverified, Kind: PruneKindTrash}}
	if !reflect.DeepEqual(res.Refused, want) {
		t.Fatalf("refused = %+v", res.Refused)
	}
	if pathExists(young) || pathExists(old) || !pathExists(held) {
		t.Fatalf("young %v old %v held %v", pathExists(young), pathExists(old), pathExists(held))
	}
	if res.FreedBytes != res.Deleted[0].Bytes+res.Deleted[1].Bytes || res.FreedBytes == 0 {
		t.Fatalf("freed %d", res.FreedBytes)
	}
}

// Orphans first, then trash: an orphan that has only an old version in .trash as its other copy goes,
// and that trash entry then stays, because nothing live would be left.
func TestPruneOrphanThenItsTrash(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	live := r.install("internal", "qwen-x", "rev2", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	tr := r.trash("internal", "qwen-x", "rev1", fresh, 20*24*time.Hour)
	req := PruneRequest{Orphans: true, Trash: true, Orphan: orphans(map[string][]string{"internal": {"qwen-x"}})}
	dry := req
	dry.DryRun = true
	want, err := r.ret.Prune(bg, dry)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.ret.Prune(bg, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, res) {
		t.Fatalf("dry %+v\nreal %+v", want, res)
	}
	if len(res.Deleted) != 1 || res.Deleted[0].Kind != PruneKindOrphan || len(res.Refused) != 1 || res.Refused[0].Kind != PruneKindTrash {
		t.Fatalf("%+v", res)
	}
	if pathExists(filepath.Join(live, "w.gguf")) || !pathExists(tr) {
		t.Fatal("the surviving copy is not the old version in the trash")
	}
}

func TestPruneRequestRules(t *testing.T) {
	r := newRig(t)
	if _, err := r.ret.Prune(bg, PruneRequest{}); !errors.Is(err, ErrNothingSelected) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.ret.Prune(bg, PruneRequest{Orphans: true}); err == nil {
		t.Fatal("orphans without a planner list were accepted")
	}
	if _, err := r.ret.Prune(bg, PruneRequest{Trash: true, Roots: []string{"ark-ffffffff"}}); err == nil {
		t.Fatal("an unmounted root was accepted")
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	r.install("internal", "qwen-x", "rev1", now)
	if _, err := r.ret.Prune(ctx, PruneRequest{Orphans: true, Orphan: orphans(map[string][]string{"internal": {"qwen-x"}})}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// The lists in a result are never nil, so the JSON is [] and not null.
	res, _ := r.ret.Prune(bg, PruneRequest{Trash: true})
	if res.Deleted == nil || res.Refused == nil {
		t.Fatal("nil lists")
	}
}

func TestCanDeleteItem(t *testing.T) {
	fresh := now.Add(-time.Hour)
	r := newRig(t)
	r.install("internal", "qwen-x", "rev1", fresh)
	if ok, reason := r.ret.CanDeleteItem("internal", "qwen-x"); ok || reason != ReasonLastVerifiedCopy {
		t.Fatalf("%v %q", ok, reason)
	}
	if ok, reason := r.ret.CanDeleteItem(driveID, "qwen-x"); ok || reason != ReasonNotPresent {
		t.Fatalf("%v %q", ok, reason)
	}
	if ok, reason := r.ret.CanDeleteItem("ark-ffffffff", "qwen-x"); ok || reason != ReasonNotPresent {
		t.Fatalf("%v %q", ok, reason)
	}
	r.install(driveID, "qwen-x", "rev1", fresh)
	if ok, reason := r.ret.CanDeleteItem("internal", "qwen-x"); !ok || reason != ReasonOK {
		t.Fatalf("%v %q", ok, reason)
	}
	r.roots[0].ReadOnly = true
	if ok, reason := r.ret.CanDeleteItem("internal", "qwen-x"); ok || reason != ReasonReadOnly {
		t.Fatalf("%v %q", ok, reason)
	}
	// R8 holds for DeleteCopy even without keep_last.
	res, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: "internal", ItemID: "qwen-x"})
	if err != nil || res.Deleted || res.Reason != ReasonReadOnly {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestPurgeTakesQuarantineBeforeTrash(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	r.install("internal", "qwen-x", "rev3", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	tr := r.trash("internal", "qwen-x", "rev1", fresh, 24*time.Hour)
	// two quarantines: the second has the "-1" suffix of a quarantine in the same second
	base := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x")
	oldQ := filepath.Join(base, now.Add(-3*24*time.Hour).Format("20060102T150405Z"))
	newQ := filepath.Join(base, now.Add(-1*24*time.Hour).Format("20060102T150405Z")+"-1")
	for _, q := range []string{oldQ, newQ} {
		_ = os.MkdirAll(q, 0o700)
		_ = os.WriteFile(filepath.Join(q, "reason.json"), []byte("{}"), 0o600)
	}
	// Needing one byte frees the oldest quarantine only; trash is untouched.
	freed, err := r.ret.Purge(bg, "internal", 1)
	if err != nil || freed == 0 {
		t.Fatalf("freed %d, %v", freed, err)
	}
	if pathExists(oldQ) || !pathExists(newQ) || !pathExists(tr) {
		t.Fatalf("oldQ %v newQ %v trash %v", pathExists(oldQ), pathExists(newQ), pathExists(tr))
	}
	// Needing a lot takes the rest of the quarantine, then the trash.
	if _, err := r.ret.Purge(bg, "internal", 1<<40); err != nil {
		t.Fatal(err)
	}
	if pathExists(newQ) || pathExists(tr) {
		t.Fatalf("newQ %v trash %v", pathExists(newQ), pathExists(tr))
	}
}

func TestQuarantineWithASuffixExpires(t *testing.T) {
	r := newRig(t)
	q := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", now.Add(-9*24*time.Hour).Format("20060102T150405Z")+"-2")
	_ = os.MkdirAll(q, 0o700)
	_ = os.WriteFile(filepath.Join(q, "reason.json"), []byte("{}"), 0o600)
	got, err := r.ret.PurgeExpired(bg, "internal", DefaultTrashDays, DefaultQuarantineDays)
	if err != nil || got.Quarantine != 1 || pathExists(q) {
		t.Fatalf("%+v, %v, exists %v", got, err, pathExists(q))
	}
}

func TestReadOnlyRootsAreNeverPurged(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	r.install("internal", "qwen-x", "rev3", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	tr := r.trash("internal", "qwen-x", "rev1", fresh, 30*24*time.Hour)
	q := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", now.Add(-30*24*time.Hour).Format("20060102T150405Z"))
	_ = os.MkdirAll(q, 0o700)
	r.roots[0].State = ark.StateReadOnly
	if freed, err := r.ret.Purge(bg, "internal", 1<<40); err != nil || freed != 0 {
		t.Fatalf("freed %d, %v", freed, err)
	}
	if got, err := r.ret.PurgeExpired(bg, "internal", 14, 7); err != nil || got.Trash != 0 || got.Quarantine != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
	if !pathExists(tr) || !pathExists(q) {
		t.Fatal("a read-only root was modified")
	}
}
