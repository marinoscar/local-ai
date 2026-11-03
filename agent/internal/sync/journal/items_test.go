package journal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

var bg = context.Background()

func openItems(t testing.TB, o journal.Options) (*journal.Items, journal.Report) {
	t.Helper()
	it, rep, err := journal.OpenItems(bg, o)
	if err != nil {
		t.Fatal(err)
	}
	return it, rep
}

func itemsPath(o journal.Options) string { return filepath.Join(o.Dir, journal.ItemsName) }
func bakPath(o journal.Options) string   { return filepath.Join(o.Dir, journal.ItemsBackupName) }

func setState(s journal.ItemState, version string) func(*journal.Item) {
	return func(i *journal.Item) { i.State, i.Version = s, version }
}

func hasCode(ps []ark.Problem, code string) bool {
	for _, p := range ps {
		if p.Code == code {
			return true
		}
	}
	return false
}

func TestKeys(t *testing.T) {
	if k := journal.Key("ark-aaaa0001", "qwen-x"); k != "ark-aaaa0001/qwen-x" {
		t.Fatal(k)
	}
	r, i, ok := journal.SplitKey("internal/qwen-x")
	if !ok || r != "internal" || i != "qwen-x" {
		t.Fatal(r, i, ok)
	}
	for _, bad := range []string{"qwen-x", "/qwen-x", "internal/", "nope/qwen-x", "internal/a/b", "internal/.."} {
		if _, _, ok := journal.SplitKey(bad); ok {
			t.Errorf("SplitKey(%q) accepted", bad)
		}
	}
}

func TestFreshNodeIsEmptyAndQuiet(t *testing.T) {
	o := opts(t, newFS())
	it, rep := openItems(t, o)
	if rep.Source != journal.SourceEmpty || len(rep.Problems) != 0 || len(it.List()) != 0 {
		t.Fatalf("%+v", rep)
	}
	if exists(o.Dir) {
		t.Fatal("opening a fresh journal must not write anything")
	}
}

func TestUpdateGetListDelete(t *testing.T) {
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	rec, err := it.Update("internal", "qwen-x", func(i *journal.Item) {
		i.State, i.Version, i.Attempts = journal.StateDownloading, "v1", 2
		i.PartialBytes = 1234
		i.Checkpoints = map[string]journal.Checkpoint{"qwen-x.gguf": {Offset: 64 << 20, ShaState: "c3RhdGU=", ETag: `"e1"`, LastModified: "Mon, 02 Nov 2026 03:00:00 GMT", URL: "https://example.test/a?x=1&y=2"}}
		i.Extras = map[string]json.RawMessage{"inventory": json.RawMessage(`"` + sha("inv") + `"`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.RootID != "internal" || rec.ItemID != "qwen-x" || rec.UpdatedAt.IsZero() {
		t.Fatalf("%+v", rec)
	}
	if _, err := it.Update("ark-aaaa0001", "wiki-x", setState(journal.StateVerified, "2026-10")); err != nil {
		t.Fatal(err)
	}

	// A fresh process sees exactly this.
	again, rep := openItems(t, o)
	if rep.Source != journal.SourceFile {
		t.Fatalf("%+v", rep)
	}
	got, ok := again.Get("internal", "qwen-x")
	if !ok || got.State != journal.StateDownloading || got.Attempts != 2 || got.PartialBytes != 1234 || got.Checkpoints["qwen-x.gguf"].Offset != 64<<20 ||
		got.Checkpoints["qwen-x.gguf"].URL != "https://example.test/a?x=1&y=2" || string(got.Extras["inventory"]) != `"`+sha("inv")+`"` {
		t.Fatalf("%+v", got)
	}
	list := again.List()
	if len(list) != 2 || list[0].Key() != "ark-aaaa0001/wiki-x" || list[1].Key() != "internal/qwen-x" {
		t.Fatalf("list %+v", list)
	}
	if _, ok := again.Get("internal", "nope"); ok {
		t.Fatal("unknown pair")
	}

	// Copies are copies.
	got.Checkpoints["qwen-x.gguf"] = journal.Checkpoint{Offset: 1}
	if again2, _ := again.Get("internal", "qwen-x"); again2.Checkpoints["qwen-x.gguf"].Offset != 64<<20 {
		t.Fatal("Get must return a copy")
	}

	if err := again.Delete("internal", "qwen-x"); err != nil {
		t.Fatal(err)
	}
	if err := again.Delete("internal", "qwen-x"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
	if _, ok := again.Get("internal", "qwen-x"); ok {
		t.Fatal("deleted")
	}
	last, _ := openItems(t, o)
	if len(last.List()) != 1 {
		t.Fatal("Delete must be persisted")
	}
}

func TestUpdateValidation(t *testing.T) {
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	tests := []struct {
		name, root, item string
		fn               func(*journal.Item)
	}{
		{"no state set", "internal", "a-b", func(*journal.Item) {}},
		{"bad state", "internal", "a-b", setState("running", "v1")},
		{"bad root", "disk-1", "a-b", setState(journal.StatePlanned, "v1")},
		{"bad item", "internal", "a/b", setState(journal.StatePlanned, "v1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := it.Update(tc.root, tc.item, tc.fn); err == nil {
				t.Fatal("want an error")
			}
			if len(it.List()) != 0 || exists(itemsPath(o)) {
				t.Fatal("a refused update must change nothing")
			}
		})
	}
}

func TestItemsFileIsCanonicalAndKeepsUnknownKeys(t *testing.T) {
	o := opts(t, newFS())
	future := `{
  "schema_version": 1,
  "updated_at": "2026-11-02T18:00:00Z",
  "items": {
    "internal/qwen-x": {
      "state": "failed",
      "version": "v1",
      "attempts": 3,
      "last_error": "boom & <bust>",
      "code": "LAI-SYN-031",
      "partial_bytes": 5,
      "consecutive_failed_runs": 2,
      "updated_at": "2026-11-02T17:00:00Z",
      "extras": {"inventory": "x", "other": [1]},
      "future_field": {"a": 1}
    }
  },
  "future_top": true
}
`
	writeFile(t, itemsPath(o), future)
	it, rep := openItems(t, o)
	if rep.Source != journal.SourceFile {
		t.Fatalf("%+v", rep)
	}
	rec, _ := it.Get("internal", "qwen-x")
	if rec.State != journal.StateFailed || rec.Code != "LAI-SYN-031" || rec.LastError != "boom & <bust>" || rec.ConsecutiveFailedRuns != 2 {
		t.Fatalf("%+v", rec)
	}
	// Any write keeps what this code does not know, at both levels.
	if _, err := it.Update("internal", "qwen-x", func(i *journal.Item) { i.Attempts = 4 }); err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, itemsPath(o))), &file); err != nil {
		t.Fatal(err)
	}
	if string(file["future_top"]) != "true" {
		t.Fatalf("top-level unknown key lost: %v", file)
	}
	var items map[string]map[string]json.RawMessage
	if err := json.Unmarshal(file["items"], &items); err != nil {
		t.Fatal(err)
	}
	r := items["internal/qwen-x"]
	if string(r["future_field"]) != `{"a":1}` && !strings.Contains(string(r["future_field"]), `"a"`) {
		t.Fatalf("record unknown key lost: %s", r["future_field"])
	}
	if !strings.Contains(string(r["extras"]), `"other"`) || string(r["attempts"]) != "4" || !strings.Contains(string(r["last_error"]), "boom & <bust>") {
		t.Fatalf("record: %v", r)
	}
	// Bytes are stable: a second no-op rewrite changes only the timestamp it stamps.
	first := readFile(t, itemsPath(o))
	if _, err := it.Update("internal", "qwen-x", func(i *journal.Item) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(first, "\n") != strings.Count(readFile(t, itemsPath(o)), "\n") {
		t.Fatal("unstable encoding")
	}
}

func TestBackupIsThePreviousGoodFile(t *testing.T) {
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	if _, err := it.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	if exists(bakPath(o)) {
		t.Fatal("the first write has nothing to back up")
	}
	first := readFile(t, itemsPath(o))
	if _, err := it.Update("internal", "b-b", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, bakPath(o)) != first {
		t.Fatal("the backup must be the file as it was before the write")
	}

	// A damaged items.json never replaces the good backup.
	good := readFile(t, bakPath(o))
	writeFile(t, itemsPath(o), "{garbage")
	if _, err := it.Update("internal", "c-c", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, bakPath(o)) != good {
		t.Fatal("a damaged file must not become the backup")
	}
	if !strings.Contains(readFile(t, itemsPath(o)), `internal/c-c`) {
		t.Fatal("the new state must be written")
	}
}

// TestUpdateFaults fails every step of the atomic write, cleanly and as a killed process. The file on
// disk is always the state before or after the update, memory stays consistent with the file, and a
// restart reads a valid file.
func TestUpdateFaults(t *testing.T) {
	steps := []struct {
		op       string
		replaced bool
	}{{"open", false}, {"write", false}, {"chmod", false}, {"sync", false}, {"close", false}, {"rename", false}, {"syncdir", true}}
	for _, step := range steps {
		for _, crash := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/crash=%v", step.op, crash), func(t *testing.T) {
				fsys := newFS()
				o := opts(t, fsys)
				it, _ := openItems(t, o)
				if _, err := it.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
					t.Fatal(err)
				}
				if _, err := it.Update("internal", "a-a", setState(journal.StateResolving, "v1")); err != nil {
					t.Fatal(err)
				}

				// The backup copy goes first and is best effort; the 2nd call of each step is the real write.
				fsys.Crash = crash
				fsys.FailNth(step.op, 2)
				_, err := it.Update("internal", "a-a", setState(journal.StateDownloading, "v1"))
				if err == nil {
					t.Fatal("the fault must surface")
				}
				if step.replaced != errors.Is(err, config.ErrNotDurable) {
					t.Fatalf("ErrNotDurable=%v for %v", errors.Is(err, config.ErrNotDurable), err)
				}
				want := journal.StateResolving
				if step.replaced {
					want = journal.StateDownloading
				}
				if cur, _ := it.Get("internal", "a-a"); cur.State != want {
					t.Fatalf("memory says %s, want %s", cur.State, want)
				}

				// The "restarted process": fresh FS, same directory.
				restarted := o
				restarted.FS = newFS()
				re, rep := openItems(t, restarted)
				if rep.Source != journal.SourceFile {
					t.Fatalf("the file on disk must be valid, got %+v", rep)
				}
				if cur, _ := re.Get("internal", "a-a"); cur.State != want {
					t.Fatalf("disk says %s, want %s", cur.State, want)
				}
			})
		}
	}
}

// TestBackupFailureIsNotFatal: the backup copy is best effort. When it fails, the update still lands.
func TestBackupFailureIsNotFatal(t *testing.T) {
	for _, op := range []string{"open", "write", "chmod", "sync", "close", "rename", "syncdir"} {
		t.Run(op, func(t *testing.T) {
			fsys := newFS()
			o := opts(t, fsys)
			it, _ := openItems(t, o)
			if _, err := it.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
				t.Fatal(err)
			}
			fsys.FailNth(op, 1)
			if _, err := it.Update("internal", "a-a", setState(journal.StateResolving, "v1")); err != nil {
				t.Fatalf("the update must land: %v", err)
			}
			re, rep := openItems(t, o)
			if cur, _ := re.Get("internal", "a-a"); rep.Source != journal.SourceFile || cur.State != journal.StateResolving {
				t.Fatalf("%+v %+v", rep, cur)
			}
		})
	}
}

type fullFS struct{ *tfs }

type fullFile struct{ config.File }

func (fullFile) Write([]byte) (int, error) { return 0, syscall.ENOSPC }

func (f fullFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	file, err := f.tfs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return fullFile{file}, nil
}

type roFS struct{ *tfs }

func (f roFS) MkdirAll(path string, perm os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return syscall.EROFS
}

func (f roFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EROFS}
	}
	return f.tfs.OpenFile(name, flag, perm)
}

func TestUpdateDiskFullAndReadOnly(t *testing.T) {
	base := opts(t, newFS())
	seed, _ := openItems(t, base)
	if _, err := seed.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		fsys journal.FS
		want error
	}{
		{"disk full", fullFS{newFS()}, syscall.ENOSPC},
		{"read-only", roFS{newFS()}, syscall.EROFS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			o.FS = tc.fsys
			it, rep := openItems(t, o)
			if rep.Source != journal.SourceFile {
				t.Fatalf("reading still works: %+v", rep)
			}
			_, err := it.Update("internal", "a-a", setState(journal.StateResolving, "v1"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if cur, _ := it.Get("internal", "a-a"); cur.State != journal.StatePlanned {
				t.Fatalf("memory must keep the old state, got %s", cur.State)
			}
			if re, _ := openItems(t, base); func() journal.ItemState { c, _ := re.Get("internal", "a-a"); return c.State }() != journal.StatePlanned {
				t.Fatal("disk must keep the old state")
			}
		})
	}
}

// --- Open: damaged, missing, rebuilt ------------------------------------------------------------

func TestOpenFromBackup(t *testing.T) {
	fsys := newFS()
	o := opts(t, fsys)
	it, _ := openItems(t, o)
	if _, err := it.Update("internal", "a-a", func(i *journal.Item) {
		i.State, i.Version, i.Attempts = journal.StateFailed, "v1", 2
		i.Code = doctor.SYN031
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Update("internal", "b-b", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	// Truncate items.json in the middle (a torn write of some other kind).
	full := readFile(t, itemsPath(o))
	writeFile(t, itemsPath(o), full[:len(full)/2])

	re, rep := openItems(t, o)
	if rep.Source != journal.SourceBackup {
		t.Fatalf("source %s", rep.Source)
	}
	if !hasCode(rep.Problems, doctor.SYN050) || rep.Problems[len(rep.Problems)-1].Severity != doctor.Info {
		t.Fatalf("problems %+v", rep.Problems)
	}
	if !strings.Contains(rep.MovedAside, "items.json.damaged-") || !exists(rep.MovedAside) {
		t.Fatalf("the damaged file must be kept: %q", rep.MovedAside)
	}
	a, ok := re.Get("internal", "a-a")
	if !ok || a.Attempts != 2 || a.State != journal.StateFailed {
		t.Fatalf("the backup's records (with their counters) are used: %+v", a)
	}
	// And the repaired file is written out at once.
	again, rep2 := openItems(t, o)
	if rep2.Source != journal.SourceFile || len(again.List()) != len(re.List()) {
		t.Fatalf("%+v", rep2)
	}
}

func TestOpenRebuildsFromManifests(t *testing.T) {
	fsys := newFS()
	internal, drive := internalRoot(t), driveRoot(t, "ark-aaaa0001", 10)
	putModel(t, internal, "qwen-x", "v1")
	putModel(t, drive, "qwen-x", "v2")
	putModel(t, drive, "drive-only", "v1")

	tests := []struct {
		name  string
		setup func(o journal.Options)
		want  journal.Source
	}{
		{"items.json and backup deleted", func(o journal.Options) {}, journal.SourceRebuilt},
		{"items.json corrupt and no backup", func(o journal.Options) { writeFile(t, itemsPath(o), "not json") }, journal.SourceRebuilt},
		{"both corrupt", func(o journal.Options) {
			writeFile(t, itemsPath(o), "{")
			writeFile(t, bakPath(o), "also {")
		}, journal.SourceRebuilt},
		{"newer schema in both", func(o journal.Options) {
			writeFile(t, itemsPath(o), `{"schema_version":9,"items":"different"}`)
			writeFile(t, bakPath(o), `{"schema_version":9,"items":"different"}`)
		}, journal.SourceRebuilt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := opts(t, fsys)
			o.Scan = scanOf(fsys, internal, drive)
			tc.setup(o)
			it, rep := openItems(t, o)
			if rep.Source != tc.want || !hasCode(rep.Problems, doctor.SYN050) {
				t.Fatalf("%+v", rep)
			}
			var got []string
			for _, r := range it.List() {
				got = append(got, fmt.Sprintf("%s=%s@%s", r.Key(), r.State, r.Version))
			}
			want := []string{"ark-aaaa0001/drive-only=verified@v1", "ark-aaaa0001/qwen-x=verified@v2", "internal/qwen-x=verified@v1"}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("got  %v\nwant %v", got, want)
			}
			if _, rep2 := openItems(t, o); rep2.Source != journal.SourceFile {
				t.Fatalf("the rebuilt file must be written out: %+v", rep2)
			}
		})
	}
}

func TestOpenDeletedItemsFileUsesBackupThenManifests(t *testing.T) {
	fsys := newFS()
	internal := internalRoot(t)
	scanned := putModel(t, internal, "qwen-x", "v1")
	o := opts(t, fsys)
	o.Scan = scanOf(fsys, internal)

	it, _ := openItems(t, o)
	// The journal is a step behind: the swap committed (the manifest is on disk) but the record
	// still says swapping.
	if _, err := it.Update("internal", "other-x", func(i *journal.Item) { i.State, i.Version, i.Attempts = journal.StateFailed, "v9", 3 }); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Update("internal", "qwen-x", setState(journal.StatePlanned, scanned.Manifest.Version)); err != nil {
		t.Fatal(err)
	}
	// The backup is the file as it was before the last write, so the last write is the one that is lost.
	if _, err := it.Update("internal", "qwen-x", setState(journal.StateSwapping, scanned.Manifest.Version)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(itemsPath(o)); err != nil {
		t.Fatal(err)
	}
	re, rep := openItems(t, o)
	if rep.Source != journal.SourceBackup || !hasCode(rep.Problems, doctor.SYN050) || rep.MovedAside != "" {
		t.Fatalf("%+v", rep)
	}
	if r, _ := re.Get("internal", "qwen-x"); r.State != journal.StateVerified {
		t.Fatalf("the manifest is the commit point: %+v", r)
	}
	if r, _ := re.Get("internal", "other-x"); r.Attempts != 3 {
		t.Fatalf("the backup's other records survive: %+v", r)
	}
}

func TestOpenSurvivesWhatItCannotRead(t *testing.T) {
	fsys := newFS()
	o := opts(t, fsys)
	it, _ := openItems(t, o)
	if _, err := it.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	t.Run("an I/O error is not damage: the file is left alone and Open fails", func(t *testing.T) {
		bad := o
		f := newFS()
		f.FailOn("readfile", 1)
		bad.FS = f
		if _, _, err := journal.OpenItems(bg, bad); !errors.Is(err, fake.ErrInjected) {
			t.Fatalf("got %v", err)
		}
		if _, rep := openItems(t, o); rep.Source != journal.SourceFile {
			t.Fatal("the file must be untouched")
		}
	})
	t.Run("leftover temp files of a killed write are swept", func(t *testing.T) {
		writeFile(t, itemsPath(o)+".tmp-aabbccddeeff", "half")
		writeFile(t, bakPath(o)+".tmp-112233445566", "half")
		openItems(t, o)
		if exists(itemsPath(o)+".tmp-aabbccddeeff") || exists(bakPath(o)+".tmp-112233445566") {
			t.Fatal("temp files must be swept")
		}
	})
	t.Run("a failing scan fails Open", func(t *testing.T) {
		bad := o
		bad.Dir = filepath.Join(t.TempDir(), "other")
		writeFile(t, filepath.Join(bad.Dir, journal.ItemsName), "{")
		bad.Scan = func(context.Context) (journal.Scanned, error) { return journal.Scanned{}, errors.New("scan failed") }
		if _, _, err := journal.OpenItems(bg, bad); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("the repaired file cannot be written", func(t *testing.T) {
		bad := o
		bad.Dir = filepath.Join(t.TempDir(), "other")
		writeFile(t, filepath.Join(bad.Dir, journal.ItemsName), "{")
		f := newFS()
		f.FailOn("rename", -1)
		bad.FS = f
		if _, _, err := journal.OpenItems(bg, bad); err == nil {
			t.Fatal("want an error")
		}
	})
}

// --- Reconcile ----------------------------------------------------------------------------------

func TestReconcile(t *testing.T) {
	internal, drive, away := internalRoot(t), driveRoot(t, "ark-aaaa0001", 10), driveRoot(t, "ark-bbbb0002", 20)
	m1 := putModel(t, internal, "new-item", "v1")
	m2 := putModel(t, internal, "behind", "v2")
	m3 := putModel(t, internal, "upgraded", "v3")
	m4 := putModel(t, internal, "redownloading", "v2")

	o := opts(t, newFS())
	it, _ := openItems(t, o)
	put := func(root, item string, st journal.ItemState, version string) {
		if _, err := it.Update(root, item, func(i *journal.Item) {
			i.State, i.Version, i.Attempts, i.LastError, i.Code, i.PartialBytes = st, version, 2, "boom", "LAI-SYN-031", 10
			i.Checkpoints = map[string]journal.Checkpoint{"f": {Offset: 1}}
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("internal", "behind", journal.StateSwapping, "v2")           // swap committed, record behind
	put("internal", "upgraded", journal.StateVerified, "v1")         // record says v1, manifest says v3
	put("internal", "redownloading", journal.StateDownloading, "v9") // another version in flight: untouched
	put("internal", "vanished", journal.StateVerified, "v1")         // scanned root, no manifest: dropped
	put("internal", "swapping-now", journal.StateSwapping, "v1")     // manifest not written yet: kept
	put(away.ID, "on-unplugged-drive", journal.StateVerified, "v1")  // root not scanned: kept
	put(drive.ID, "on-drive", journal.StateFailed, "v1")             // scanned drive, not verified: kept

	sc := journal.Scanned{Items: []ark.ScannedItem{m1, m2, m3, m4}, Roots: []string{"internal", drive.ID}}
	changed, err := it.Reconcile(sc)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 4 { // new-item added, behind fixed, upgraded fixed, vanished dropped
		t.Fatalf("changed %d", changed)
	}
	state := func(root, item string) (journal.Item, bool) { return it.Get(root, item) }
	if r, ok := state("internal", "new-item"); !ok || r.State != journal.StateVerified || r.Version != "v1" {
		t.Fatalf("new-item %+v", r)
	}
	if r, _ := state("internal", "behind"); r.State != journal.StateVerified || r.Attempts != 0 || r.LastError != "" || r.Code != "" || r.PartialBytes != 0 || r.Checkpoints != nil {
		t.Fatalf("behind %+v", r)
	}
	if r, _ := state("internal", "upgraded"); r.Version != "v3" || r.State != journal.StateVerified {
		t.Fatalf("upgraded %+v", r)
	}
	if r, _ := state("internal", "redownloading"); r.State != journal.StateDownloading || r.Version != "v9" || r.Attempts != 2 {
		t.Fatalf("redownloading %+v", r)
	}
	if _, ok := state("internal", "vanished"); ok {
		t.Fatal("vanished must be dropped")
	}
	for _, k := range [][2]string{{"internal", "swapping-now"}, {away.ID, "on-unplugged-drive"}, {drive.ID, "on-drive"}} {
		if _, ok := state(k[0], k[1]); !ok {
			t.Fatalf("%v must be kept", k)
		}
	}
	// Nothing left to do: no write.
	before := readFile(t, itemsPath(o))
	if n, err := it.Reconcile(sc); err != nil || n != 0 || readFile(t, itemsPath(o)) != before {
		t.Fatalf("second reconcile: %d %v", n, err)
	}
}

// --- UnknownStaging -----------------------------------------------------------------------------

func TestUnknownStaging(t *testing.T) {
	root := internalRoot(t)
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	set := func(item string, st journal.ItemState, version string) {
		if _, err := it.Update(root.ID, item, setState(st, version)); err != nil {
			t.Fatal(err)
		}
	}
	set("downloading-now", journal.StateDownloading, "v2")
	set("failed-resumable", journal.StateFailed, "v2")
	set("done", journal.StateVerified, "v2")
	stage := func(parts ...string) {
		writeFile(t, filepath.Join(append([]string{root.Path, ".staging"}, append(parts, "x.part")...)...), "x")
	}
	stage("downloading-now", "v2")       // known
	stage("failed-resumable", "v2")      // known (a retryable .part is kept)
	stage("downloading-now", "v1")       // another version than the record's: unknown
	stage("done", "v2")                  // verified: nothing should be staged any more
	stage("no-record", "v1")             // no record
	stage("quarantine", "x", "20261102") // the quarantine tree is not staging

	got, err := it.UnknownStaging(config.OSFS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, p := range got {
		r, _ := filepath.Rel(filepath.Join(root.Path, ".staging"), p)
		rel = append(rel, filepath.ToSlash(r))
	}
	want := []string{"done/v2", "downloading-now/v1", "no-record/v1"}
	if strings.Join(rel, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", rel, want)
	}
	for _, p := range got {
		if !exists(p) {
			t.Fatal("UnknownStaging must delete nothing")
		}
	}
	if none, err := it.UnknownStaging(config.OSFS{}, ark.NewInternalRoot(filepath.Join(t.TempDir(), "none"), "")); err != nil || len(none) != 0 {
		t.Fatalf("no .staging: %v %v", none, err)
	}
}

func TestItemsConcurrentUpdates(t *testing.T) {
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := it.Update("internal", fmt.Sprintf("item-%d-%d", w, i), setState(journal.StatePlanned, "v1")); err != nil {
					t.Error(err)
				}
				it.List()
			}
		}(w)
	}
	wg.Wait()
	re, _ := openItems(t, o)
	if len(re.List()) != 40 {
		t.Fatalf("%d records on disk", len(re.List()))
	}
}
