package verify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Root+"/"+c.Item)
	}
	return out
}

func resultsOf(rep Report) map[string]string {
	out := map[string]string{}
	for _, o := range rep.Outcomes {
		out[o.Root+"/"+o.Item] = o.Result + ":" + o.Reason
	}
	return out
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestOldestLastReverifiedFirstAndDueFilter(t *testing.T) {
	r := newRig(t)
	r.install("internal", "c-recent", "v1", epoch.Add(-days(2)))
	r.install("internal", "a-old", "v1", epoch.Add(-days(90)))
	r.install(driveID, "b-older", "v1", epoch.Add(-days(120)))
	r.install("internal", "d-mid", "v1", epoch.Add(-days(40)))

	t.Run("oldest first across roots, only what is due", func(t *testing.T) {
		got, err := r.rv.Candidates(bg, Options{DueDays: 30})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{driveID + "/b-older", "internal/a-old", "internal/d-mid"}
		if strings.Join(ids(got), ",") != strings.Join(want, ",") {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	})
	t.Run("full ignores reverify_days and takes everything", func(t *testing.T) {
		got, _ := r.rv.Candidates(bg, Options{DueDays: 30, Full: true})
		if len(got) != 4 || got[0].Item != "b-older" || got[3].Item != "c-recent" {
			t.Fatalf("full = %v", ids(got))
		}
	})
	t.Run("zero days means everything is due", func(t *testing.T) {
		got, _ := r.rv.Candidates(bg, Options{})
		if len(got) != 4 {
			t.Fatalf("got %v", ids(got))
		}
	})
	t.Run("a named item is checked whatever its age; a root filter narrows", func(t *testing.T) {
		got, _ := r.rv.Candidates(bg, Options{DueDays: 30, Items: []string{"c-recent"}})
		if len(got) != 1 || got[0].Item != "c-recent" {
			t.Fatalf("named = %v", ids(got))
		}
		got, _ = r.rv.Candidates(bg, Options{Full: true, Roots: []string{driveID}})
		if len(got) != 1 || got[0].Root != driveID {
			t.Fatalf("root filter = %v", ids(got))
		}
	})
	t.Run("equal dates fall back to the root order, then the item id", func(t *testing.T) {
		r.install("internal", "m-tie", "v1", epoch.Add(-days(200)))
		r.install(driveID, "a-tie", "v1", epoch.Add(-days(200)))
		r.install("internal", "b-tie", "v1", epoch.Add(-days(200)))
		got, _ := r.rv.Candidates(bg, Options{DueDays: 30})
		want := []string{"internal/b-tie", "internal/m-tie", driveID + "/a-tie"}
		if strings.Join(ids(got)[:3], ",") != strings.Join(want, ",") {
			t.Fatalf("order = %v, want it to start with %v", ids(got), want)
		}
	})
}

func TestARunStampsWhatMatchedAndNamesWhatIsNotThere(t *testing.T) {
	r := newRig(t)
	r.install("internal", "qwen-x", "v1", epoch.Add(-days(60)))
	r.clk.Advance(time.Hour)
	var begun []int64
	var seen []Outcome
	rep, err := r.rv.Run(bg, Options{
		Items:  []string{"qwen-x", "no-such-item"},
		Begin:  func(n int, b int64) { begun = append(begun, int64(n), b) },
		Record: func(o Outcome) { seen = append(seen, o) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified() != 1 || rep.Damaged() != 0 || rep.Stopped || len(rep.Left) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	got := resultsOf(rep)
	if got["internal/qwen-x"] != "verified:" || got["/no-such-item"] != "skipped:"+ReasonNotPresent {
		t.Fatalf("results = %v", got)
	}
	if len(seen) != 2 || len(begun) != 2 || begun[0] != 1 || begun[1] <= 0 {
		t.Fatalf("callbacks: seen %d, begun %v", len(seen), begun)
	}
	// last_reverified_at moved to now on the disk and in the index; nothing else in the manifest did.
	m := r.onDisk("internal", "qwen-x")
	if !m.LastReverifiedAt.Equal(epoch.Add(time.Hour)) || m.Version != "v1" || m.VerifiedAt.Equal(m.LastReverifiedAt) {
		t.Fatalf("manifest = %+v", m)
	}
	if !r.lastReverified("internal", "qwen-x").Equal(epoch.Add(time.Hour)) {
		t.Fatal("the index still has the old stamp")
	}
}

func TestBudgetStopsWithinBudgetPlusOneFile(t *testing.T) {
	r := newRig(t)
	r.fs.readCost = 10 * time.Minute // every file read costs 10 minutes of the fake clock
	for i, id := range []string{"item-1", "item-2", "item-3", "item-4", "item-5"} {
		r.install("internal", id, "v1", epoch.Add(-days(100-i))) // item-1 is the oldest
	}
	r.clk.Advance(time.Minute)
	stampAt := r.clk.Now()
	rep, err := r.rv.Run(bg, Options{Budget: 25 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Elapsed 0, 10 and 20 minutes are inside the budget, so three files are read; at 30 the pass stops.
	if !rep.Stopped || rep.Verified() != 3 || len(rep.Left) != 2 {
		t.Fatalf("stopped=%v verified=%d left=%d", rep.Stopped, rep.Verified(), len(rep.Left))
	}
	if rep.Left[0].Item != "item-4" || rep.Left[1].Item != "item-5" {
		t.Fatalf("left = %v", ids(rep.Left))
	}
	// The oldest three were stamped; the others keep their old date.
	for _, id := range []string{"item-1", "item-2", "item-3"} {
		if got := r.onDisk("internal", id).LastReverifiedAt; got.Before(stampAt) {
			t.Errorf("%s not stamped: %v", id, got)
		}
	}
	for _, id := range []string{"item-4", "item-5"} {
		if got := r.onDisk("internal", id).LastReverifiedAt; !got.Before(epoch) {
			t.Errorf("%s was stamped although the budget was spent: %v", id, got)
		}
	}
}

func TestBudgetIsCheckedBetweenFilesOfAnItemAndAnItemCutShortKeepsItsOldDate(t *testing.T) {
	r := newRig(t)
	r.fs.readCost = 10 * time.Minute
	old := epoch.Add(-days(100))
	r.install("internal", "two-files", "v1", old, "a.gguf", "b.gguf")
	rep, err := r.rv.Run(bg, Options{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// The first file is read (elapsed 0), then the budget is spent: the item is left, not stamped.
	if !rep.Stopped || len(rep.Left) != 1 || rep.Verified() != 0 {
		t.Fatalf("%+v", rep)
	}
	if got := r.onDisk("internal", "two-files").LastReverifiedAt; !got.Equal(old) {
		t.Fatalf("a half-checked item was stamped: %v", got)
	}
}

func TestACancelledReadKeepsTheOldDateAndLeavesTheCopyAlone(t *testing.T) {
	r := newRig(t)
	old := epoch.Add(-days(100))
	dir := r.install("internal", "qwen-x", "v1", old, "a.gguf", "b.gguf")
	ctx, cancel := context.WithCancel(bg)
	r.fs.cancel = cancel
	r.fs.midFile = filepath.Join(dir, "b.gguf") // the process is killed in the middle of the second file
	rep, err := r.rv.Run(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(rep.Outcomes) != 0 {
		t.Fatalf("a cut-short read was settled: %+v", rep.Outcomes)
	}
	if got := r.onDisk("internal", "qwen-x").LastReverifiedAt; !got.Equal(old) {
		t.Fatalf("stamped: %v", got)
	}
	if _, err := r.idx.BestCopy("qwen-x"); err != nil {
		t.Fatal("the copy must still be served")
	}
}

func TestAFlippedByteIsQuarantinedAndReportedAsSYN025(t *testing.T) {
	r := newRig(t)
	long := epoch.Add(-days(60))
	dir := r.install("internal", "qwen-x", "v1", long)
	r.install("internal", "fine", "v1", long)
	rot(t, filepath.Join(dir, "w.gguf"))

	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var bad Outcome
	for _, o := range rep.Outcomes {
		if o.Item == "qwen-x" {
			bad = o
		}
	}
	if bad.Result != ResultQuarantined || bad.Reason != ReasonBitRot || bad.Code != doctor.SYN025 || bad.Version != "v1" {
		t.Fatalf("outcome = %+v", bad)
	}
	if got := resultsOf(rep)["internal/fine"]; got != "verified:" {
		t.Fatalf("the neighbour = %s", got)
	}
	// The copy is no longer an item: the directory moved whole, nothing deleted, with the reason beside it.
	if pathExists(dir) {
		t.Fatal("the damaged copy is still in place")
	}
	qroot := filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x")
	stamps, _ := os.ReadDir(qroot)
	if len(stamps) != 1 {
		t.Fatalf("quarantine entries = %d", len(stamps))
	}
	q := filepath.Join(qroot, stamps[0].Name())
	for _, f := range []string{"reason.json", filepath.Join("new", "w.gguf"), filepath.Join("new", "MANIFEST.json")} {
		if !pathExists(filepath.Join(q, f)) {
			t.Errorf("quarantine lacks %s", f)
		}
	}
	var reason struct {
		Code, Item, Root, Version, Resolver string
		Files                               []struct {
			Path           string `json:"path"`
			ExpectedSHA256 string `json:"expected_sha256"`
			ActualSHA256   string `json:"actual_sha256"`
		}
	}
	raw, _ := os.ReadFile(filepath.Join(q, "reason.json"))
	if err := json.Unmarshal(raw, &reason); err != nil || reason.Code != doctor.SYN025 || reason.Item != "qwen-x" || reason.Version != "v1" || len(reason.Files) != 1 {
		t.Fatalf("reason.json = %s (%v)", raw, err)
	}
	// The index and the journal: gone from every answer, recorded as quarantined with the code.
	if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("a quarantined copy is still offered: %v", err)
	}
	rec, ok := r.jr.Get("internal", "qwen-x")
	if !ok || rec.State != journal.StateQuarantined || rec.Code != doctor.SYN025 || rec.Version != "v1" {
		t.Fatalf("journal = %+v", rec)
	}
}

func TestAnOlderVersionInTheTrashIsRestoredWhenTheLiveCopyRotted(t *testing.T) {
	r := newRig(t)
	long := epoch.Add(-days(60))
	live := r.install("internal", "qwen-x", "rev2", long)
	r.trash("internal", "qwen-x", "rev1", long)
	rot(t, filepath.Join(live, "w.gguf"))

	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := rep.Outcomes[0]
	if o.Result != ResultQuarantined || o.Reason != ReasonRestored || o.Restored != "rev1" || o.Code != doctor.SYN025 {
		t.Fatalf("outcome = %+v", o)
	}
	// R4: the older version is the live copy again, hashing to its own manifest; READY will call it stale.
	c, err := r.idx.BestCopy("qwen-x")
	if err != nil || c.Manifest.Version != "rev1" {
		t.Fatalf("best copy = %v, %v", c.Manifest.Version, err)
	}
	got, _ := os.ReadFile(filepath.Join(live, "w.gguf"))
	if string(got) != string(body("qwen-x", "rev1", "w.gguf")) {
		t.Fatal("the old bytes are not back")
	}
	if rec, _ := r.jr.Get("internal", "qwen-x"); rec.State != journal.StateVerified || rec.Version != "rev1" {
		t.Fatalf("journal = %+v", rec)
	}
	// The rotten files are in quarantine, not deleted.
	stamps, _ := os.ReadDir(filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x"))
	if len(stamps) != 1 {
		t.Fatalf("quarantine entries = %d", len(stamps))
	}
}

func TestARottenTrashEntryIsNotRestoredSoTheCopyIsQuarantined(t *testing.T) {
	r := newRig(t)
	long := epoch.Add(-days(60))
	live := r.install("internal", "qwen-x", "rev2", long)
	tr := r.trash("internal", "qwen-x", "rev1", long)
	rot(t, filepath.Join(live, "w.gguf"))
	rot(t, filepath.Join(tr, "w.gguf"))
	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := rep.Outcomes[0]; o.Reason != ReasonBitRot || o.Restored != "" {
		t.Fatalf("outcome = %+v", o)
	}
	if !pathExists(filepath.Join(tr, "w.gguf")) {
		t.Fatal("the trash entry must stay for a person to look at")
	}
}

func TestADirectoryItemIsCheckedAgainstItsSumsAndTreeHash(t *testing.T) {
	files := map[string]string{"index.html": "<h1>manuals</h1>", "pages/one.html": "page one", "pages/two.html": "page two"}
	long := epoch.Add(-days(60))

	t.Run("a whole tree matches", func(t *testing.T) {
		r := newRig(t)
		r.installDocs("internal", long, files)
		rep, err := r.rv.Run(bg, Options{})
		if err != nil || rep.Verified() != 1 {
			t.Fatalf("%+v, %v", rep, err)
		}
	})
	t.Run("a flipped byte in one page quarantines the whole tree", func(t *testing.T) {
		r := newRig(t)
		dir := r.installDocs("internal", long, files)
		rot(t, filepath.Join(dir, "pages", "two.html"))
		rep, err := r.rv.Run(bg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if o := rep.Outcomes[0]; o.Result != ResultQuarantined || o.Reason != ReasonBitRot || o.Code != doctor.SYN025 {
			t.Fatalf("outcome = %+v", o)
		}
		if pathExists(dir) {
			t.Fatal("the damaged tree is still in place")
		}
	})
	t.Run("a SHA256SUMS that no longer matches tree_sha256 is damage too", func(t *testing.T) {
		r := newRig(t)
		dir := r.installDocs("internal", long, files)
		sums, err := manifest.ReadSums(config.OSFS{}, dir)
		if err != nil {
			t.Fatal(err)
		}
		sums[0].SHA256 = strings.Repeat("a", 64) // still a well-formed file, but not the one tree_sha256 covers
		if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
			t.Fatal(err)
		}
		rep, err := r.rv.Run(bg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if o := rep.Outcomes[0]; o.Result != ResultQuarantined || o.Code != doctor.SYN025 {
			t.Fatalf("a tampered SHA256SUMS passed: %+v", o)
		}
	})
	t.Run("a page that vanished is damage", func(t *testing.T) {
		r := newRig(t)
		dir := r.installDocs("internal", long, files)
		if err := os.Rename(filepath.Join(dir, "pages", "one.html"), filepath.Join(t.TempDir(), "gone")); err != nil {
			t.Fatal(err)
		}
		rep, err := r.rv.Run(bg, Options{})
		if err != nil || rep.Damaged() != 1 {
			t.Fatalf("%+v, %v", rep, err)
		}
	})
}

func TestAnUnreadableFileIsDamageNotAVerifiedCopy(t *testing.T) {
	r := newRig(t)
	dir := r.install("internal", "qwen-x", "v1", epoch.Add(-days(60)))
	r.fs.readErr[filepath.Join(dir, "w.gguf")] = errEIO
	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := rep.Outcomes[0]; o.Result != ResultQuarantined || o.Code != doctor.SYN025 {
		t.Fatalf("outcome = %+v", o)
	}
}

// A flip that only the read path sees (a bad cable, a failing controller) is bit rot as far as the
// agent can tell: the bytes it was given are not the bytes the manifest describes.
func TestABitFlippedOnTheReadPathIsDamageToo(t *testing.T) {
	r := newRig(t)
	dir := r.install(driveID, "qwen-x", "v1", epoch.Add(-days(60)))
	r.setRoot(driveID, func(ro *ark.Root) { ro.State = ark.StateReadOnly; ro.ReadOnly = true })
	r.fs.flip[filepath.Join(dir, "w.gguf")] = true
	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := rep.Outcomes[0]; o.Result != ResultQuarantined || o.Reason != ReasonExcluded || o.Code != doctor.SYN025 {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestAReadOnlyRootIsReadNeverWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*ark.Root)
	}{
		{"read-only mount", func(ro *ark.Root) { ro.State, ro.ReadOnly = ark.StateReadOnly, true }},
		{"unclean", func(ro *ark.Root) { ro.State = ark.StateUnclean }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			long := epoch.Add(-days(60))
			bad := r.install(driveID, "rotten", "v1", long)
			r.install(driveID, "fine", "v1", long)
			r.install("internal", "rotten", "v1", long)
			r.setRoot(driveID, tc.mod)
			rot(t, filepath.Join(bad, "w.gguf"))
			before := snapshot(t, r.root(driveID).Path)

			rep, err := r.rv.Run(bg, Options{Roots: []string{driveID}})
			if err != nil {
				t.Fatal(err)
			}
			got := resultsOf(rep)
			if got[driveID+"/rotten"] != "quarantined:"+ReasonExcluded || got[driveID+"/fine"] != "verified:" {
				t.Fatalf("results = %v", got)
			}
			if o := rep.Outcomes[0]; o.Item == "rotten" && o.Code != doctor.SYN025 {
				t.Fatalf("code = %s", o.Code)
			}
			// No disk writes: the whole tree of the root is byte-identical.
			if after := snapshot(t, r.root(driveID).Path); !equal(before, after) {
				t.Fatalf("a read-only root was written to:\nbefore %v\nafter  %v", before, after)
			}
			if !r.idx.Excluded(driveID, "rotten", "v1") {
				t.Fatal("the damaged copy must be excluded in memory")
			}
			// Best copy now falls to the other root's copy of the same item.
			if c, err := r.idx.BestCopy("rotten"); err != nil || c.RootID != "internal" {
				t.Fatalf("best copy = %v, %v", c.RootID, err)
			}
			if rec, ok := r.jr.Get(driveID, "rotten"); !ok || rec.State != journal.StateQuarantined || rec.Code != doctor.SYN025 {
				t.Fatalf("journal = %+v", rec)
			}
		})
	}
}

func TestAFullDiskDuringQuarantineLeavesTheCopyAndExcludesItInMemory(t *testing.T) {
	r := newRig(t)
	dir := r.install("internal", "qwen-x", "v1", epoch.Add(-days(60)))
	rot(t, filepath.Join(dir, "w.gguf"))
	r.fs.mkdirErr = syscall.ENOSPC

	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := rep.Outcomes[0]; o.Result != ResultFailed || o.Reason != ReasonQuarantineFailed || o.Code != doctor.SYN025 {
		t.Fatalf("outcome = %+v", o)
	}
	if !pathExists(filepath.Join(dir, "MANIFEST.json")) || !pathExists(filepath.Join(dir, "w.gguf")) {
		t.Fatal("the copy must stay where it is when it cannot be moved")
	}
	if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("the damaged copy is still offered: %v", err)
	}

	// The disk has room again; the next pass (after the mount is rebuilt) finishes the job.
	r.fs.mkdirErr = nil
	r.rebuild()
	rep, err = r.rv.Run(bg, Options{})
	if err != nil || rep.Outcomes[0].Reason != ReasonBitRot {
		t.Fatalf("%+v, %v", rep.Outcomes, err)
	}
	if pathExists(dir) {
		t.Fatal("the second pass did not quarantine it")
	}
}

func TestAProcessKilledAtTheRenameLeavesNothingHalfMovedAndTheNextPassFinishes(t *testing.T) {
	r := newRig(t)
	dir := r.install("internal", "qwen-x", "v1", epoch.Add(-days(60)))
	rot(t, filepath.Join(dir, "w.gguf"))
	r.fs.renameOf[dir] = syscall.EIO // reason.json is written, then the move fails: the process is gone

	rep, err := r.rv.Run(bg, Options{})
	if err != nil || rep.Outcomes[0].Reason != ReasonQuarantineFailed {
		t.Fatalf("%+v, %v", rep.Outcomes, err)
	}
	if !pathExists(filepath.Join(dir, "MANIFEST.json")) {
		t.Fatal("the copy moved although the rename failed")
	}
	r.fs.renameOf = map[string]error{}
	r.rebuild()
	rep, err = r.rv.Run(bg, Options{})
	if err != nil || rep.Outcomes[0].Reason != ReasonBitRot {
		t.Fatalf("%+v, %v", rep.Outcomes, err)
	}
	if pathExists(dir) {
		t.Fatal("not quarantined by the second pass")
	}
	// Both passes left their reason.json; nothing was deleted.
	stamps, _ := os.ReadDir(filepath.Join(r.root("internal").Path, ".staging", "quarantine", "qwen-x"))
	if len(stamps) != 2 {
		t.Fatalf("quarantine entries = %d, want the abandoned one and the real one", len(stamps))
	}
}

func TestAnItemBeingSwappedIsLeftForNextTime(t *testing.T) {
	r := newRig(t)
	dir := r.install("internal", "qwen-x", "v1", epoch.Add(-days(60)))
	rot(t, filepath.Join(dir, "w.gguf"))
	r.jr.set("internal", "qwen-x", journal.StateSwapping)
	rep, err := r.rv.Run(bg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := rep.Outcomes[0]; o.Result != ResultSkipped || o.Reason != ReasonBusy {
		t.Fatalf("outcome = %+v", o)
	}
	if !pathExists(filepath.Join(dir, "w.gguf")) {
		t.Fatal("touched")
	}
}

func TestACopyReplacedWhileItWasReadIsLeftAlone(t *testing.T) {
	r := newRig(t)
	long := epoch.Add(-days(60))
	dir := r.install("internal", "qwen-x", "v1", long)
	// A newer manifest appears between the scan and the stamp (a swap the engine did not announce).
	m := r.onDisk("internal", "qwen-x")
	m.Version, m.SortKey = "v2", "v2"
	r.fs.readCost = time.Minute
	cands, _, err := r.rv.candidates(bg, Options{})
	if err != nil || len(cands) != 1 {
		t.Fatal(err)
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}
	out := Outcome{Result: ResultVerified}
	r.rv.markGood(bg, cands[0], &out, quiet)
	if out.Result != ResultSkipped || out.Reason != ReasonChanged {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.onDisk("internal", "qwen-x"); !got.LastReverifiedAt.Equal(long) {
		t.Fatalf("a replaced copy was stamped: %v", got.LastReverifiedAt)
	}
}

func TestOnAnUncleanRootAGoodCopyBecomesVisible(t *testing.T) {
	r := newRig(t)
	r.install(driveID, "qwen-x", "v1", epoch.Add(-days(60)))
	r.setRoot(driveID, func(ro *ark.Root) { ro.State = ark.StateUnclean })
	if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("an unverified copy on an unclean root must be invisible")
	}
	before := snapshot(t, r.root(driveID).Path)
	rep, err := r.rv.Run(bg, Options{Roots: []string{driveID}})
	if err != nil || rep.Verified() != 1 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if _, err := r.idx.BestCopy("qwen-x"); err != nil {
		t.Fatalf("after a passed check the copy must be served: %v", err)
	}
	if after := snapshot(t, r.root(driveID).Path); !equal(before, after) {
		t.Fatal("an unclean root was written to")
	}
}

func TestNewChecksItsDependencies(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("accepted nothing")
	}
	if _, err := NewServeVerifier(ServeDeps{}); err == nil {
		t.Fatal("accepted nothing")
	}
}

func TestUnmountedRootsAndMissingFilesAreNotCandidates(t *testing.T) {
	r := newRig(t)
	r.install(driveID, "qwen-x", "v1", epoch.Add(-days(60)))
	r.setRoot(driveID, func(ro *ark.Root) { ro.State = ark.StateEjecting })
	got, err := r.rv.Candidates(bg, Options{})
	if err != nil || len(got) != 0 {
		t.Fatalf("%v, %v", ids(got), err)
	}
}
