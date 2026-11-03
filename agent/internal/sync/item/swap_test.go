package item

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func docs1() content {
	return content{"index.html": genData(300, 21), "guide/a.txt": genData(1200, 22), "guide/b.txt": genData(80, 23)}
}
func docs2() content {
	return content{"index.html": genData(310, 24), "guide/a.txt": genData(1210, 25), "new/c.txt": genData(90, 26)}
}

// scenario is one kind of swap that is put through the kill-at-every-step test.
type scenario struct {
	name string
	// old is installed first (nil: a fresh install); next is the version that is swapped in.
	old, next content
	dir       bool
	item      string
	rel       string
	// baseline is the node with the old version installed, copied for every trial.
	baseline *env
}

var scenarios = []scenario{
	{name: "file item, fresh install", next: v1(), item: "qwen-x", rel: "models/qwen-x"},
	{name: "file item, update", old: v1(), next: v2(), item: "qwen-x", rel: "models/qwen-x"},
	{name: "directory item, fresh install", next: docs1(), dir: true, item: "docs", rel: "docs"},
	{name: "directory item, update through RENAME_EXCHANGE", old: docs1(), next: docs2(), dir: true, item: "docs", rel: "docs"},
}

func (s *scenario) job(e *env, ver string, c content) Job {
	if s.dir {
		return e.dirJob(ver, c)
	}
	return e.fileJob(ver, c, true)
}

func (s *scenario) dirPath(e *env) string {
	return filepath.Join(e.root.Path, filepath.FromSlash(s.rel))
}

// neverHalf is the invariant of every instant after a kill: what a reader of the root sees is the old
// version complete, the new version complete, or nothing (a swap in progress has no manifest); and the
// bytes of the old version are never lost, they are in the item directory or in the trash.
func (s *scenario) neverHalf(t *testing.T, e *env, where string) string {
	t.Helper()
	m, err := ark.LoadItem(OSFS{}, e.root, s.rel)
	switch {
	case err == nil && m.Manifest.Version == "rev1":
		assertContent(t, s.dirPath(e), s.oldOr(s.next))
		return "old"
	case err == nil && m.Manifest.Version == "rev2":
		assertContent(t, s.dirPath(e), s.next)
		return "new"
	case err == nil:
		t.Fatalf("%s: unknown version %s", where, m.Manifest.Version)
	case errors.Is(err, fs.ErrNotExist):
	default:
		t.Fatalf("%s: a half-swapped item looks present: %v", where, err)
	}
	if s.old != nil {
		trash := filepath.Join(e.root.Path, ".trash", s.item)
		for name, data := range s.old {
			if !bytesAt(filepath.Join(s.dirPath(e), filepath.FromSlash(name)), data) && !trashHas(trash, name, data) && !stagedTreeHas(e.root.Path, s.item, name, data) {
				t.Fatalf("%s: the bytes of old file %s are gone", where, name)
			}
		}
	}
	return "none"
}

// oldOr returns the old content when there is one (a fresh install has no old state to check).
func (s *scenario) oldOr(fallback content) content {
	if s.old != nil {
		return s.old
	}
	return fallback
}

func bytesAt(p string, want []byte) bool {
	got, err := os.ReadFile(p)
	return err == nil && string(got) == string(want)
}

// trashHas looks for a file below any version of an item's trash (a file item keeps files at their
// paths, a directory item under tree/).
func trashHas(trashItem, name string, want []byte) bool {
	vers, _ := os.ReadDir(trashItem)
	for _, v := range vers {
		for _, p := range []string{filepath.Join(trashItem, v.Name(), filepath.FromSlash(name)), filepath.Join(trashItem, v.Name(), TreeDir, filepath.FromSlash(name))} {
			if bytesAt(p, want) {
				return true
			}
		}
	}
	return false
}

// stagedTreeHas looks for a file in the staged tree of any version: right after an exchange the old tree
// sits there until it moves to the trash.
func stagedTreeHas(rootPath, item, name string, want []byte) bool {
	vers, _ := os.ReadDir(filepath.Join(rootPath, ".staging", item))
	for _, v := range vers {
		if bytesAt(filepath.Join(rootPath, ".staging", item, v.Name(), TreeDir, filepath.FromSlash(name)), want) {
			return true
		}
	}
	return false
}

// trial installs the old version (when the scenario has one) on a quiet filesystem and then runs the
// swap of the new one with the process armed to die at the k-th step (k == 0: never).
func (s *scenario) trial(t *testing.T, k int, after bool) (e *env, died bool) {
	t.Helper()
	if s.old != nil {
		if s.baseline == nil {
			b := baselineEnv(t)
			b.install(s.job(b, "rev1", s.old))
			s.baseline = b
		}
		e = cloneEnv(t, s.baseline)
	} else {
		e = newEnv(t)
	}
	crash := newCrashFS(e.base)
	e.open(crash, e.fetch)
	if k > 0 {
		crash.arm(k, after)
	}
	run, err := e.jr.Runs.Start()
	if err != nil {
		if crash.isDead() {
			return e, true
		}
		t.Fatal(err)
	}
	e.run = run
	e.m.Run(bg, run, s.job(e, "rev2", s.next))
	return e, crash.isDead()
}

// recover restarts the agent on the disk the kill left and brings the item to its end state: the
// journal replay, Recover for the swaps that were in flight, and a Run for whatever else was in flight.
func (s *scenario) recover(t *testing.T, e *env, where string) Result {
	t.Helper()
	e.open(newCrashFS(e.base), e.fetch)
	run := e.startRun()
	keys := make([]string, 0, len(e.rec.Intents))
	for k := range e.rec.Intents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		in := e.rec.Intents[k]
		if in.Swap == nil {
			continue
		}
		if res, ok := e.m.Recover(bg, run, in); ok && res.State != journal.StateVerified {
			t.Fatalf("%s: Recover did not complete the swap: %+v", where, res)
		}
	}
	// A restarted engine runs what the planner says is not at its target: not the verified item.
	rec, ok := e.jr.Items.Get(e.root.ID, s.item)
	if ok && rec.State == journal.StateVerified && rec.Version == "rev2" {
		return Result{State: journal.StateVerified}
	}
	return e.m.Run(bg, run, s.job(e, "rev2", s.next))
}

func (s *scenario) assertEnd(t *testing.T, e *env, where string) {
	t.Helper()
	m, err := ark.LoadItem(OSFS{}, e.root, s.rel)
	if err != nil || m.Manifest.Version != "rev2" {
		t.Fatalf("%s: end state is not the new version: %+v, %v", where, m, err)
	}
	assertContent(t, s.dirPath(e), s.next)
	rec, _ := e.jr.Items.Get(e.root.ID, s.item)
	if rec.State != journal.StateVerified || rec.Version != "rev2" {
		t.Fatalf("%s: record = %+v", where, rec)
	}
	if c, err := e.idx.BestCopy(s.item); err != nil || c.Manifest.Version != "rev2" {
		t.Fatalf("%s: best copy = %+v, %v", where, c, err)
	}
	if s.old != nil {
		// The old version is in the trash with its manifest, byte for byte.
		trash := filepath.Join(e.root.Path, ".trash", s.item, "rev1")
		tm, err := manifest.Read(OSFS{}, filepath.Join(trash, "MANIFEST.json"))
		if err != nil || tm.Version != "rev1" {
			t.Fatalf("%s: the old manifest is not in the trash: %+v, %v", where, tm, err)
		}
		base := trash
		if s.dir {
			base = filepath.Join(trash, TreeDir)
		}
		assertContent(t, base, s.old)
	}
	if left, _ := os.ReadDir(filepath.Join(e.root.Path, ".staging")); len(left) > 0 {
		var names []string
		for _, l := range left {
			names = append(names, l.Name())
		}
		t.Fatalf("%s: staging is not clean: %v", where, names)
	}
}

// TestSwapSurvivesAKillAtEveryStep kills the process before and after every numbered step of the whole
// run (every journal event, every rename, exchange, fsync, chmod and write), restarts it, and demands
// that nothing half done is ever visible and that the item always ends as the new version.
func TestSwapSurvivesAKillAtEveryStep(t *testing.T) {
	for i := range scenarios {
		sc := &scenarios[i]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			// A clean run tells how many steps there are.
			clean, died := sc.trial(t, 0, false)
			if died {
				t.Fatal("the clean run died")
			}
			total := clean.fsys.steps()
			sc.assertEnd(t, clean, "clean run")
			if total < 20 {
				t.Fatalf("only %d steps: the counter is not counting", total)
			}
			t.Logf("%s: %d steps, %d kills", sc.name, total, 2*total)
			outcomes := map[string]int{}
			for k := 1; k <= total; k++ {
				for _, after := range []bool{false, true} {
					where := fmt.Sprintf("kill %s step %d/%d", map[bool]string{false: "before", true: "after"}[after], k, total)
					e, died := sc.trial(t, k, after)
					if !died {
						t.Fatalf("%s: the process did not die", where)
					}
					outcomes[sc.neverHalf(t, e, where)]++
					res := sc.recover(t, e, where)
					if res.State != journal.StateVerified {
						t.Fatalf("%s: the resumed run ended %+v", where, res)
					}
					sc.assertEnd(t, e, where)
					// Recovery is idempotent: a second restart changes nothing.
					e.open(newCrashFS(e.base), e.fetch)
					sc.assertEnd(t, e, where+", restarted again")
				}
			}
			t.Logf("what a reader saw right after a kill: %v", outcomes)
			if sc.old != nil && (outcomes["old"] == 0 || outcomes["new"] == 0 || outcomes["none"] == 0) {
				t.Fatalf("the kills never landed in every phase: %v", outcomes)
			}
		})
	}
}

// TestManifestIsTheLastWriteOfASwap uses the operation log: the rename of MANIFEST.json into the item
// directory is the last step that changes the item directory, and only fsyncs of directories follow it.
func TestManifestIsTheLastWriteOfASwap(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e, died := sc.trial(t, 0, false)
			if died {
				t.Fatal("died")
			}
			ops := e.fsys.opLog()
			commit := -1
			for i, op := range ops {
				if strings.HasPrefix(op, "rename ") && strings.HasSuffix(op, "-> /srv/"+sc.rel+"/MANIFEST.json") && strings.Contains(op, "/.staging/") {
					commit = i
				}
			}
			if commit < 0 {
				t.Fatalf("no manifest rename in %v", ops)
			}
			// Everything the swap did to the item directory or the trash comes before the commit.
			for _, op := range ops[commit+1:] {
				if strings.Contains(op, "/srv/"+sc.rel) && !strings.HasPrefix(op, "syncdir ") && !strings.HasPrefix(op, "removeall ") {
					t.Fatalf("a write to the item directory after the commit: %q", op)
				}
				if strings.Contains(op, "/.trash/") {
					t.Fatalf("a trash move after the commit: %q", op)
				}
			}
			// The commit is preceded by fsyncs of the directories the swap touched.
			sawSync := false
			for _, op := range ops[:commit] {
				if strings.HasPrefix(op, "syncdir ") && strings.Contains(op, "/.staging/") {
					sawSync = true
				}
			}
			if !sawSync {
				t.Fatalf("the staging directory was not fsynced before the commit: %v", ops[:commit])
			}
			// Exactly one rename lands a manifest in the item directory: nothing writes it twice.
			n := 0
			for _, op := range ops {
				if strings.HasPrefix(op, "rename ") && strings.HasSuffix(op, "-> /srv/"+sc.rel+"/MANIFEST.json") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d manifest renames", n)
			}
		})
	}
}

// TestDirectorySwapUsesRenameExchange: an existing directory is swapped as a whole with
// renameat2(RENAME_EXCHANGE), and the displaced tree ends in the trash.
func TestDirectorySwapUsesRenameExchange(t *testing.T) {
	sc := &scenarios[3]
	e, _ := sc.trial(t, 0, false)
	var exchanges []string
	for _, op := range e.fsys.opLog() {
		if strings.HasPrefix(op, "exchange ") {
			exchanges = append(exchanges, op)
		}
	}
	if len(exchanges) != 1 || !strings.Contains(exchanges[0], "/srv/.staging/docs/rev2/tree <-> /srv/docs") {
		t.Fatalf("exchanges = %v", exchanges)
	}
	// A fresh install has nothing to exchange with: a plain rename.
	e2, _ := scenarios[2].trial(t, 0, false)
	for _, op := range e2.fsys.opLog() {
		if strings.HasPrefix(op, "exchange ") {
			t.Fatalf("a fresh directory install exchanged: %s", op)
		}
	}
	// The tree files are readable by the services: directories 0755, files 0644.
	for _, p := range []string{"docs", "docs/guide"} {
		if info, err := os.Stat(filepath.Join(e.root.Path, p)); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v %v", p, info, err)
		}
	}
	if info, err := os.Stat(filepath.Join(e.root.Path, "docs", "guide", "a.txt")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	sums, err := manifest.ReadSums(OSFS{}, filepath.Join(e.root.Path, "docs"))
	if err != nil || len(sums) != 3 {
		t.Fatalf("SHA256SUMS = %v, %v", sums, err)
	}
	m, _ := ark.LoadItem(OSFS{}, e.root, "docs")
	tree, _ := manifest.TreeSHA256(sums)
	if m.Manifest.TreeSHA256 != tree || m.Manifest.FileCount != 3 {
		t.Fatalf("manifest = %+v", m.Manifest)
	}
}

// TestLostFileFailsTheSwapAndRestoresTheOldVersion kills a file swap after the old version moved to the
// trash, destroys a staged file that has not moved yet (its source and destination are both gone), and
// restarts: the swap is failed with LAI-SYN-026 and the old version comes back from the trash.
func TestLostFileFailsTheSwapAndRestoresTheOldVersion(t *testing.T) {
	sc := &scenarios[1]
	// Find the step right after the old manifest moved to the trash.
	clean, _ := sc.trial(t, 0, false)
	target := 0
	for i, op := range clean.fsys.opLog() {
		if strings.HasPrefix(op, "rename ") && strings.Contains(op, "-> /srv/.trash/qwen-x/rev1/MANIFEST.json") {
			target = i + 1
		}
	}
	if target == 0 {
		t.Fatal("no trash move of the manifest in the log")
	}
	e, died := sc.trial(t, target, true)
	if !died {
		t.Fatal("did not die")
	}
	if _, err := ark.LoadItem(OSFS{}, e.root, sc.rel); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after the old manifest left, the item must be not present, got %v", err)
	}
	// The staged new file is destroyed before the restart.
	if err := os.Remove(filepath.Join(e.root.Path, ".staging", "qwen-x", "rev2", "c.bin")); err != nil {
		t.Fatal(err)
	}
	e.open(newCrashFS(e.base), e.fetch)
	run := e.startRun()
	var res Result
	for _, in := range e.rec.Intents {
		if in.Swap != nil {
			res, _ = e.m.Recover(bg, run, in)
		}
	}
	if res.State != journal.StateFailed || res.Code != doctor.SYN026 || res.Reason != "swap_lost" {
		t.Fatalf("result = %+v", res)
	}
	m, err := ark.LoadItem(OSFS{}, e.root, sc.rel)
	if err != nil || m.Manifest.Version != "rev1" {
		t.Fatalf("the old version was not restored: %+v, %v", m.Manifest, err)
	}
	assertContent(t, sc.dirPath(e), v1())
	if c, err := e.idx.BestCopy("qwen-x"); err != nil || c.Manifest.Version != "rev1" {
		t.Fatalf("best copy = %+v, %v", c, err)
	}
	rec, _ := e.jr.Items.Get("internal", "qwen-x")
	if rec.State != journal.StateFailed || rec.Code != doctor.SYN026 {
		t.Fatalf("record = %+v", rec)
	}
	// What had moved in of the new version is in quarantine, not deleted and not left in the item.
	if pathExists(filepath.Join(sc.dirPath(e), "c.bin")) {
		t.Fatal("a file of the failed version is left in the item directory")
	}
}

// TestLostFileOfAFreshInstallLeavesNothingPresent: no previous version to restore.
func TestLostFileOfAFreshInstallLeavesNothingPresent(t *testing.T) {
	sc := &scenarios[0]
	clean, _ := sc.trial(t, 0, false)
	target := 0
	for i, op := range clean.fsys.opLog() {
		if strings.HasPrefix(op, "rename ") && strings.HasSuffix(op, "-> /srv/models/qwen-x/a.gguf") {
			target = i + 1
		}
	}
	e, died := sc.trial(t, target, true)
	if !died {
		t.Fatal("did not die")
	}
	_ = os.Remove(filepath.Join(e.root.Path, ".staging", "qwen-x", "rev2", "b.json"))
	e.open(newCrashFS(e.base), e.fetch)
	run := e.startRun()
	var res Result
	for _, in := range e.rec.Intents {
		if in.Swap != nil {
			res, _ = e.m.Recover(bg, run, in)
		}
	}
	if res.State != journal.StateFailed || res.Code != doctor.SYN026 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := ark.LoadItem(OSFS{}, e.root, sc.rel); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed fresh install is present: %v", err)
	}
	if pathExists(filepath.Join(sc.dirPath(e), "a.gguf")) {
		t.Fatal("the half-installed file was left in the item directory")
	}
}

// TestRecoverIsIdempotent: replaying a finished swap changes nothing and is recognised by its manifest.
func TestRecoverIsIdempotent(t *testing.T) {
	sc := &scenarios[1]
	clean, _ := sc.trial(t, 0, false)
	e, _ := sc.trial(t, 0, false)
	_ = clean
	e.open(newCrashFS(e.base), e.fetch)
	run := e.startRun()
	// Hand Recover the plan of the swap that finished: a restart after swap_done but before verified.
	log, err := e.jr.Runs.Read(e.rec.ReplayedRun)
	if err != nil {
		t.Fatal(err)
	}
	var plan *journal.SwapPlan
	for _, ev := range log.Events {
		if ev.Ev == journal.EvSwapPlan {
			plan = &journal.SwapPlan{Renames: ev.Renames, Trash: ev.Trash, Exchange: ev.Exchange}
		}
	}
	if plan == nil {
		t.Fatal("no swap plan in the log")
	}
	for i := 0; i < 2; i++ {
		res, ok := e.m.Recover(bg, run, journal.Intent{RootID: "internal", ItemID: "qwen-x", Version: "rev2", Swap: plan})
		if !ok || res.State != journal.StateVerified {
			t.Fatalf("recover %d: %+v ok=%v", i, res, ok)
		}
		sc.assertEnd(t, e, fmt.Sprintf("recover %d", i))
	}
	// An intent without a plan, or for a root that is not mounted, is not Recover's.
	if _, ok := e.m.Recover(bg, run, journal.Intent{RootID: "internal", ItemID: "qwen-x", Version: "rev2"}); ok {
		t.Fatal("recovered an intent without a plan")
	}
	if _, ok := e.m.Recover(bg, run, journal.Intent{RootID: "ark-0000aaaa", ItemID: "qwen-x", Version: "rev2", Swap: plan}); ok {
		t.Fatal("recovered an intent of an unmounted root")
	}
}

func TestSwapPlanIsTheSpecs(t *testing.T) {
	e := newEnv(t)
	e.install(e.fileJob("rev1", v1(), true))
	e.fetch.data["a.gguf"] = v2()["a.gguf"]
	// Plan the update by hand and compare with the shape of ark-sync.md section 4.5.1.
	newM := manifest.Manifest{SchemaVersion: 1, ItemID: "qwen-x", ItemKind: "model", Engine: "llama", Version: "rev2", SortKey: "rev2", State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned, DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch,
		Files: []manifest.File{{Kind: "weights", Path: "a.gguf", SizeBytes: 3500, SHA256: sha(v2()["a.gguf"])}, {Kind: "weights", Path: "c.bin", SizeBytes: 64, SHA256: sha(v2()["c.bin"])}}}
	sp, old, err := e.m.planSwap(swapInput{root: e.root, relDir: "models/qwen-x", itemID: "qwen-x", version: "rev2", newM: newM})
	if err != nil {
		t.Fatal(err)
	}
	if old == nil || old.Version != "rev1" {
		t.Fatalf("old = %+v", old)
	}
	d, s, tr := e.itemDir(), filepath.Join(e.root.Path, ".staging", "qwen-x", "rev2"), filepath.Join(e.root.Path, ".trash", "qwen-x", "rev1")
	wantTrash := [][2]string{
		{filepath.Join(d, "MANIFEST.json"), filepath.Join(tr, "MANIFEST.json")}, // the manifest first
		{filepath.Join(d, "a.gguf"), filepath.Join(tr, "a.gguf")},
		{filepath.Join(d, "b.json"), filepath.Join(tr, "b.json")},
	}
	wantRenames := [][2]string{
		{filepath.Join(s, "a.gguf"), filepath.Join(d, "a.gguf")},
		{filepath.Join(s, "c.bin"), filepath.Join(d, "c.bin")},
		{filepath.Join(s, "MANIFEST.json"), filepath.Join(d, "MANIFEST.json")}, // the commit last
	}
	if fmt.Sprint(sp.Plan.Trash) != fmt.Sprint(wantTrash) || fmt.Sprint(sp.Plan.Renames) != fmt.Sprint(wantRenames) || len(sp.Plan.Exchange) != 0 {
		t.Fatalf("plan:\n trash   %v\n renames %v\n exchange %v", sp.Plan.Trash, sp.Plan.Renames, sp.Plan.Exchange)
	}
	// The same version superseded twice gets a free trash directory, never an overwrite.
	if got := freeTrashDir(OSFS{}, e.root.Path, "qwen-x", "rev1"); got != tr {
		t.Fatalf("free trash dir = %s", got)
	}
	_ = os.MkdirAll(tr, 0o700)
	if got := freeTrashDir(OSFS{}, e.root.Path, "qwen-x", "rev1"); got != tr+".1" {
		t.Fatalf("second trash dir = %s", got)
	}
	// A file item cannot become a directory item.
	dirM := newM
	dirM.Files, dirM.TreeSHA256, dirM.FileCount = nil, sha([]byte("t")), 1
	if _, _, err := e.m.planSwap(swapInput{root: e.root, relDir: "models/qwen-x", itemID: "qwen-x", version: "rev2", newM: dirM}); !errors.Is(err, ErrKindChanged) {
		t.Fatalf("kind change: %v", err)
	}
}

func TestPathsFromPlanRejectsPlansThatLeaveTheRoot(t *testing.T) {
	e := newEnv(t)
	good := journalPlan(e)
	if _, err := pathsFromPlan(e.root, "qwen-x", "rev2", good); err != nil {
		t.Fatal(err)
	}
	bad := journalPlan(e)
	bad.Renames[len(bad.Renames)-1] = [2]string{"/elsewhere/.staging/x/MANIFEST.json", "/elsewhere/models/x/MANIFEST.json"}
	if _, err := pathsFromPlan(e.root, "qwen-x", "rev2", bad); err == nil {
		t.Fatal("a plan outside the root was accepted")
	}
	if _, err := pathsFromPlan(e.root, "qwen-x", "rev2", journal.SwapPlan{}); err == nil {
		t.Fatal("an empty plan was accepted")
	}
	noManifest := journalPlan(e)
	noManifest.Renames = noManifest.Renames[:1]
	if _, err := pathsFromPlan(e.root, "qwen-x", "rev2", noManifest); err == nil {
		t.Fatal("a plan that does not end with the manifest rename was accepted")
	}
}

func journalPlan(e *env) journal.SwapPlan {
	s := filepath.Join(e.root.Path, ".staging", "qwen-x", "rev2")
	return journal.SwapPlan{Renames: [][2]string{
		{filepath.Join(s, "a.gguf"), filepath.Join(e.itemDir(), "a.gguf")},
		{filepath.Join(s, "MANIFEST.json"), filepath.Join(e.itemDir(), "MANIFEST.json")},
	}}
}
