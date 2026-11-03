package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

var bg = context.Background()

var now = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fakeJournal map[string]journal.Item

func (f fakeJournal) Get(root, item string) (journal.Item, bool) {
	i, ok := f[journal.Key(root, item)]
	return i, ok
}

type fakeItems struct{ deleted []string }

func (f *fakeItems) Delete(root, item string) error {
	f.deleted = append(f.deleted, root+"/"+item)
	return nil
}

// rig is a node with two roots, a real best-copy index over them and a retention.
type rig struct {
	t     testing.TB
	roots []ark.Root
	idx   *ark.Index
	jr    fakeJournal
	items *fakeItems
	ret   *Retention
	clock time.Time
}

const driveID = "ark-0a0b0c0d"

func newRig(t testing.TB) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, jr: fakeJournal{}, items: &fakeItems{}, clock: now}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady},
		{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), State: ark.StateReady, MountID: 3},
	}
	for _, ro := range r.roots {
		if err := os.MkdirAll(ro.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.idx = ark.NewIndex(config.OSFS{})
	ret, err := New(Deps{
		FS: OSFS{}, Roots: func() []ark.Root { return r.roots }, Copies: r.idx, Journal: r.jr, Index: r.idx, Items: r.items,
		Now: func() time.Time { return r.clock }, Logger: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ret = ret
	return r
}

func (r *rig) root(id string) ark.Root {
	for _, ro := range r.roots {
		if ro.ID == id {
			return ro
		}
	}
	r.t.Fatalf("no root %s", id)
	return ark.Root{}
}

// install puts a verified copy of a model on a root: a file and a manifest that says it was verified
// at verifiedAt (and re-verified then).
func (r *rig) install(rootID, itemID, version string, verifiedAt time.Time) string {
	r.t.Helper()
	dir := filepath.Join(r.root(rootID).Path, "models", itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	body := []byte("weights of " + itemID + " " + version)
	if err := os.WriteFile(filepath.Join(dir, "w.gguf"), body, 0o644); err != nil {
		r.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: itemID, ItemKind: "model", Engine: "llama", Version: version, SortKey: version, State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		Files:        []manifest.File{{Kind: "weights", Path: "w.gguf", SizeBytes: int64(len(body)), SHA256: sha(body)}},
		TotalBytes:   int64(len(body)),
		DownloadedAt: verifiedAt, VerifiedAt: verifiedAt, LastReverifiedAt: verifiedAt,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	r.rebuild()
	return dir
}

func (r *rig) rebuild() {
	if _, err := r.idx.Rebuild(bg, r.roots); err != nil {
		r.t.Fatal(err)
	}
}

// trash puts a superseded version in .trash/<item>/<version>/ with its manifest, aged ago.
func (r *rig) trash(rootID, itemID, version string, verifiedAt time.Time, age time.Duration) string {
	r.t.Helper()
	dir := filepath.Join(r.root(rootID).Path, ".trash", itemID, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	body := []byte("old weights of " + itemID + " " + version)
	if err := os.WriteFile(filepath.Join(dir, "w.gguf"), body, 0o644); err != nil {
		r.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: itemID, ItemKind: "model", Engine: "llama", Version: version, SortKey: version, State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		Files:        []manifest.File{{Kind: "weights", Path: "w.gguf", SizeBytes: int64(len(body)), SHA256: sha(body)}},
		TotalBytes:   int64(len(body)),
		DownloadedAt: verifiedAt, VerifiedAt: verifiedAt, LastReverifiedAt: verifiedAt,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	at := r.clock.Add(-age)
	if err := os.Chtimes(dir, at, at); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

func (r *rig) setState(root, item string, state journal.ItemState) {
	r.jr[journal.Key(root, item)] = journal.Item{RootID: root, ItemID: item, State: state}
}

func TestCanDeleteMatrix(t *testing.T) {
	fresh := now.Add(-time.Hour)
	stale := now.Add(-48 * time.Hour)
	type setup func(r *rig) (path string)
	cases := []struct {
		name   string
		setup  setup
		allow  bool
		reason string
	}{
		{"the only verified copy is never deleted (R1)", func(r *rig) string {
			return r.install("internal", "qwen-x", "rev1", fresh)
		}, false, ReasonLastVerifiedCopy},
		{"a file of the only copy is not deleted either", func(r *rig) string {
			return filepath.Join(r.install("internal", "qwen-x", "rev1", fresh), "w.gguf")
		}, false, ReasonLastVerifiedCopy},
		{"another verified copy on a mounted drive, verified within 24 h", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev1", fresh)
			return r.install("internal", "qwen-x", "rev1", fresh)
		}, true, ReasonOK},
		{"another copy that was not re-verified within 24 h does not count", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev1", stale)
			return r.install("internal", "qwen-x", "rev1", fresh)
		}, false, ReasonLastVerifiedCopy},
		{"a copy on a drive that is not mounted does not count", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev1", fresh)
			p := r.install("internal", "qwen-x", "rev1", fresh)
			r.roots[1].State = ark.StateNotMounted
			r.rebuild()
			return p
		}, false, ReasonLastVerifiedCopy},
		{"a copy on a drive that is being ejected does not count", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev1", fresh)
			p := r.install("internal", "qwen-x", "rev1", fresh)
			r.idx.SetRootState(driveID, ark.StateEjecting)
			return p
		}, false, ReasonLastVerifiedCopy},
		{"another version of the same item counts (any version)", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev0", fresh)
			return r.install("internal", "qwen-x", "rev1", fresh)
		}, true, ReasonOK},
		{"a copy of another item does not count", func(r *rig) string {
			r.install(driveID, "other-model", "rev1", fresh)
			return r.install("internal", "qwen-x", "rev1", fresh)
		}, false, ReasonLastVerifiedCopy},
		{"an intact, fresh, verified trash entry counts as a copy", func(r *rig) string {
			p := r.install("internal", "qwen-x", "rev2", fresh)
			r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
			return p
		}, true, ReasonOK},
		{"a trash entry that is not fresh does not", func(r *rig) string {
			p := r.install("internal", "qwen-x", "rev2", fresh)
			r.trash("internal", "qwen-x", "rev1", stale, time.Hour)
			return p
		}, false, ReasonLastVerifiedCopy},
		{"a trash entry whose manifest is damaged does not", func(r *rig) string {
			p := r.install("internal", "qwen-x", "rev2", fresh)
			tr := r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
			_ = os.WriteFile(filepath.Join(tr, "MANIFEST.json"), []byte("{not json"), 0o644)
			return p
		}, false, ReasonLastVerifiedCopy},
		{"trash is purged once the live copy is verified (R3)", func(r *rig) string {
			r.install("internal", "qwen-x", "rev2", fresh)
			r.setState("internal", "qwen-x", journal.StateVerified)
			return r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
		}, true, ReasonOK},
		{"trash stays while the replacement is only swapped, not verified", func(r *rig) string {
			r.install("internal", "qwen-x", "rev2", fresh)
			r.setState("internal", "qwen-x", journal.StateSwapped)
			return r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
		}, false, ReasonReplacementUnverified},
		{"trash stays while the swap is under way", func(r *rig) string {
			r.install("internal", "qwen-x", "rev2", fresh)
			r.setState("internal", "qwen-x", journal.StateSwapping)
			return r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
		}, false, ReasonReplacementUnverified},
		{"trash with no live copy anywhere stays", func(r *rig) string {
			return r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
		}, false, ReasonReplacementUnverified},
		{"a live copy on another root is enough for the trash", func(r *rig) string {
			r.install(driveID, "qwen-x", "rev2", fresh)
			return r.trash("internal", "qwen-x", "rev1", fresh, time.Hour)
		}, true, ReasonOK},
		{"staging was never verified: it may go", func(r *rig) string {
			p := filepath.Join(r.root("internal").Path, ".staging", "qwen-x", "rev9")
			_ = os.MkdirAll(p, 0o700)
			return p
		}, true, ReasonOK},
		{"quarantine is not a copy: it may go (R5)", func(r *rig) string {
			p := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", "20261101T000000Z")
			_ = os.MkdirAll(p, 0o700)
			return p
		}, true, ReasonOK},
		{"kit/config is not an item (R9)", func(r *rig) string {
			p := filepath.Join(r.root("internal").Path, "kit", "config")
			_ = os.MkdirAll(p, 0o755)
			return p
		}, false, ReasonNotAnItem},
		{"a directory without a manifest is not an item (R9)", func(r *rig) string {
			p := filepath.Join(r.root("internal").Path, "models", "half-swapped")
			_ = os.MkdirAll(p, 0o755)
			return p
		}, false, ReasonNotAnItem},
		{"the root itself, .trash and .staging are never deleted", func(r *rig) string {
			return filepath.Join(r.root("internal").Path, ".trash")
		}, false, ReasonNotAnItem},
		{"a path outside every root", func(r *rig) string { return "/etc/passwd" }, false, ReasonNotAnItem},
		{"a sibling whose name starts like the root", func(r *rig) string {
			return r.root("internal").Path + "-evil/models/x"
		}, false, ReasonNotAnItem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			path := tc.setup(r)
			ok, reason := r.ret.CanDelete(path)
			if ok != tc.allow || reason != tc.reason {
				t.Fatalf("CanDelete = %v, %q; want %v, %q", ok, reason, tc.allow, tc.reason)
			}
		})
	}
}

// R1 is checked against the live index at the moment of the deletion, not against what was true when
// the deletion was planned.
func TestRemoveChecksTheLiveStateAtDeletionTime(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	internal := r.install("internal", "qwen-x", "rev1", fresh)
	r.install(driveID, "qwen-x", "rev1", fresh)
	if ok, _ := r.ret.CanDelete(internal); !ok {
		t.Fatal("with two copies the internal one may go")
	}
	// The drive is pulled between the plan and the deletion.
	r.roots[1].State = ark.StateNotMounted
	r.rebuild()
	err := r.ret.Remove(internal)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonLastVerifiedCopy {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := os.Stat(filepath.Join(internal, "w.gguf")); err != nil {
		t.Fatalf("a refused removal touched the copy: %v", err)
	}
	// The drive comes back: now it is allowed, and it really deletes.
	r.roots[1].State = ark.StateReady
	r.rebuild()
	if err := r.ret.Remove(internal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(internal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("not deleted: %v", err)
	}
	// And a path that is already gone is no error.
	if err := r.ret.Remove(internal); err == nil {
		// a vanished copy is "not an item" now: refused, never a delete
		t.Log("a second removal of a gone path is accepted")
	}
}

func TestRemoveStagingRefusesEverythingElse(t *testing.T) {
	r := newRig(t)
	copyDir := r.install("internal", "qwen-x", "rev1", now)
	stage := filepath.Join(r.root("internal").Path, ".staging", "qwen-x", "rev2")
	_ = os.MkdirAll(stage, 0o700)
	_ = os.WriteFile(filepath.Join(stage, "w.gguf.part"), []byte("partial"), 0o600)

	for _, p := range []string{copyDir, filepath.Join(r.root("internal").Path, ".trash", "qwen-x"), "/tmp"} {
		var refused *RefusedError
		if err := r.ret.RemoveStaging(p); !errors.As(err, &refused) || refused.Reason != ReasonNotAnItem {
			t.Errorf("RemoveStaging(%s) = %v", p, err)
		}
	}
	if err := r.ret.RemoveStaging(stage); err != nil {
		t.Fatal(err)
	}
	// The item's staging directory goes with its last version.
	if _, err := os.Stat(filepath.Join(r.root("internal").Path, ".staging", "qwen-x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the empty staging directory of the item is left: %v", err)
	}
}

func TestPurgeFreesTrashOldestFirstAndKeepsWhatIsRefused(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	r.install("internal", "qwen-x", "rev3", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	oldest := r.trash("internal", "qwen-x", "rev1", fresh, 10*24*time.Hour)
	newer := r.trash("internal", "qwen-x", "rev2", fresh, 3*24*time.Hour)
	// An item whose replacement is not verified yet keeps its trash.
	r.install("internal", "gemma-y", "rev2", fresh)
	r.setState("internal", "gemma-y", journal.StateSwapped)
	kept := r.trash("internal", "gemma-y", "rev1", fresh, 20*24*time.Hour)

	entries, err := r.ret.ListTrash("internal")
	if err != nil || len(entries) != 3 || entries[0].Path != kept || entries[1].Path != oldest || entries[2].Path != newer {
		t.Fatalf("trash = %+v, %v", entries, err)
	}
	// Needing a little frees just the oldest eligible entry.
	freed, err := r.ret.Purge(bg, "internal", 1)
	if err != nil || freed != entries[1].Bytes {
		t.Fatalf("freed %d, %v (want %d)", freed, err, entries[1].Bytes)
	}
	if pathExists(oldest) || !pathExists(newer) || !pathExists(kept) {
		t.Fatalf("oldest %v newer %v kept %v", pathExists(oldest), pathExists(newer), pathExists(kept))
	}
	// Needing a lot frees every eligible entry and still keeps the refused one.
	freed, err = r.ret.Purge(bg, "internal", 1<<40)
	if err != nil || freed != entries[2].Bytes {
		t.Fatalf("freed %d, %v", freed, err)
	}
	if pathExists(newer) || !pathExists(kept) {
		t.Fatalf("newer %v kept %v", pathExists(newer), pathExists(kept))
	}
	// The emptied .trash/qwen-x is tidied away.
	if pathExists(filepath.Join(r.root("internal").Path, ".trash", "qwen-x")) {
		t.Fatal("an empty trash directory of an item is left")
	}
	// Nothing eligible is left: a purge frees nothing and is no error.
	if freed, err = r.ret.Purge(bg, "internal", 1<<40); err != nil || freed != 0 {
		t.Fatalf("freed %d, %v", freed, err)
	}
	if _, err := r.ret.Purge(bg, "ark-ffffffff", 1); err == nil {
		t.Fatal("an unknown root was accepted")
	}
}

func TestPurgeStopsWhenTheContextEnds(t *testing.T) {
	r := newRig(t)
	r.install("internal", "qwen-x", "rev3", now)
	r.setState("internal", "qwen-x", journal.StateVerified)
	trash := r.trash("internal", "qwen-x", "rev1", now, 24*time.Hour)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := r.ret.Purge(ctx, "internal", 1<<40); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !pathExists(trash) {
		t.Fatal("a cancelled purge deleted")
	}
}

func TestPurgeExpiredByAge(t *testing.T) {
	r := newRig(t)
	fresh := now.Add(-time.Hour)
	r.install("internal", "qwen-x", "rev3", fresh)
	r.setState("internal", "qwen-x", journal.StateVerified)
	old := r.trash("internal", "qwen-x", "rev1", fresh, 15*24*time.Hour)
	young := r.trash("internal", "qwen-x", "rev2", fresh, 13*24*time.Hour)

	oldQ := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", now.Add(-8*24*time.Hour).Format("20060102T150405Z"))
	youngQ := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x", now.Add(-6*24*time.Hour).Format("20060102T150405Z"))
	for _, q := range []string{oldQ, youngQ} {
		_ = os.MkdirAll(q, 0o700)
		_ = os.WriteFile(filepath.Join(q, "reason.json"), []byte("{}"), 0o600)
	}
	got, err := r.ret.PurgeExpired(bg, "internal", DefaultTrashDays, DefaultQuarantineDays)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trash != 1 || got.Quarantine != 1 || got.Bytes == 0 {
		t.Fatalf("expired = %+v", got)
	}
	if pathExists(old) || !pathExists(young) || pathExists(oldQ) || !pathExists(youngQ) {
		t.Fatalf("old %v young %v oldQ %v youngQ %v", pathExists(old), pathExists(young), pathExists(oldQ), pathExists(youngQ))
	}
	// Zero days means "never expires".
	if got, _ = r.ret.PurgeExpired(bg, "internal", 0, 0); got.Trash != 0 || got.Quarantine != 0 {
		t.Fatalf("expired with zero ages = %+v", got)
	}
}

func TestPurgeUnknownStaging(t *testing.T) {
	r := newRig(t)
	junk := filepath.Join(r.root("internal").Path, ".staging", "ghost", "rev1")
	_ = os.MkdirAll(junk, 0o700)
	n, err := r.ret.PurgeUnknownStaging(func(root ark.Root) ([]string, error) { return []string{junk}, nil }, "internal")
	if err != nil || n != 1 || pathExists(junk) {
		t.Fatalf("removed %d, %v, exists %v", n, err, pathExists(junk))
	}
	// A list that names something that is not staging is refused outright.
	copyDir := r.install("internal", "qwen-x", "rev1", now)
	if _, err := r.ret.PurgeUnknownStaging(func(root ark.Root) ([]string, error) { return []string{copyDir}, nil }, "internal"); err == nil {
		t.Fatal("a verified copy was offered for removal")
	}
	if !pathExists(copyDir) {
		t.Fatal("a verified copy was deleted")
	}
}

func TestDeleteCopy(t *testing.T) {
	fresh := now.Add(-time.Hour)
	t.Run("keep_last refuses the only copy and keeps every byte", func(t *testing.T) {
		r := newRig(t)
		dir := r.install("internal", "qwen-x", "rev1", fresh)
		res, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: "internal", ItemID: "qwen-x", KeepLast: true})
		if err != nil || res.Deleted || res.Reason != ReasonLastVerifiedCopy {
			t.Fatalf("%+v, %v", res, err)
		}
		if !pathExists(filepath.Join(dir, "w.gguf")) || !pathExists(filepath.Join(dir, "MANIFEST.json")) || len(r.items.deleted) != 0 {
			t.Fatal("a refused delete changed something")
		}
	})
	t.Run("a model on two roots loses one copy and keeps the other", func(t *testing.T) {
		r := newRig(t)
		internal := r.install("internal", "qwen-x", "rev1", fresh)
		drive := r.install(driveID, "qwen-x", "rev1", fresh)
		res, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: "internal", ItemID: "qwen-x", KeepLast: true})
		if err != nil || !res.Deleted || res.Bytes == 0 {
			t.Fatalf("%+v, %v", res, err)
		}
		if pathExists(filepath.Join(internal, "w.gguf")) || pathExists(filepath.Join(internal, "MANIFEST.json")) {
			t.Fatal("the internal copy is still there")
		}
		if !reflect.DeepEqual(r.items.deleted, []string{"internal/qwen-x"}) {
			t.Fatalf("journal deletes = %v", r.items.deleted)
		}
		// The best-copy index already forgot it: only the drive's copy is left, and it is the last.
		if c := r.idx.Copies("qwen-x"); len(c) != 1 || c[0].RootID != driveID {
			t.Fatalf("copies = %+v", c)
		}
		res, err = r.ret.DeleteCopy(bg, DeleteRequest{RootID: driveID, ItemID: "qwen-x", KeepLast: true})
		if err != nil || res.Deleted || res.Reason != ReasonLastVerifiedCopy || !pathExists(filepath.Join(drive, "w.gguf")) {
			t.Fatalf("the last copy: %+v, %v", res, err)
		}
	})
	t.Run("without keep_last the last copy goes (the explicit override)", func(t *testing.T) {
		r := newRig(t)
		dir := r.install("internal", "qwen-x", "rev1", fresh)
		res, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: "internal", ItemID: "qwen-x"})
		if err != nil || !res.Deleted || pathExists(dir) {
			t.Fatalf("%+v, %v, exists %v", res, err, pathExists(dir))
		}
	})
	t.Run("a root with no copy is not_present", func(t *testing.T) {
		r := newRig(t)
		r.install("internal", "qwen-x", "rev1", fresh)
		res, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: driveID, ItemID: "qwen-x", KeepLast: true})
		if err != nil || res.Deleted || res.Reason != ReasonNotPresent {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("an unknown root", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.ret.DeleteCopy(bg, DeleteRequest{RootID: "ark-ffffffff", ItemID: "x"}); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("the manifest goes first: a kill in the middle leaves 'not present', never a half copy", func(t *testing.T) {
		r := newRig(t)
		dir := r.install("internal", "qwen-x", "rev1", fresh)
		// Only the manifest removal happened (the process died before the files went).
		if err := os.Remove(filepath.Join(dir, "MANIFEST.json")); err != nil {
			t.Fatal(err)
		}
		r.rebuild()
		if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
			t.Fatalf("a copy without a manifest is present: %v", err)
		}
	})
}

func TestNewChecksItsDependencies(t *testing.T) {
	if _, err := New(Deps{}); err == nil || !strings.Contains(err.Error(), "FS") {
		t.Fatalf("err = %v", err)
	}
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// The spec's static check (ark-sync.md section 9.1): no file of internal/sync or internal/ark outside
// this package deletes. Deletion is the one thing retention exists to own.
func TestOnlyRetentionDeletes(t *testing.T) {
	banned := []string{"os.Remove", "os.RemoveAll", "unix.Unlink", "syscall.Unlink", "unix.Unlinkat", "unix.Rmdir", "syscall.Rmdir"}
	for _, root := range []string{"..", "../../ark"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if filepath.Base(path) == "retention" || filepath.Base(path) == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for n, line := range strings.Split(string(src), "\n") {
				code := line
				if i := strings.Index(code, "//"); i >= 0 {
					code = code[:i]
				}
				for _, b := range banned {
					if strings.Contains(code, b+"(") {
						t.Errorf("%s:%d calls %s: only internal/sync/retention may delete", path, n+1, b)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
