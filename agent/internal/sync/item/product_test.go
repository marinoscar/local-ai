package item

import (
	"context"
	"encoding/json"
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
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The jobs of multi-part products. The producers here are fakes that write the same shapes the real
// ones do (resolve.GitMirror, resolve.RepoDocs): a mirror first cloned into staging and afterwards
// updated in place, with the source tarballs staged beside it; a docs tree and a kit catalog that are
// two items of one product.

const (
	mirrorItem = "kit-llama-cpp"
	mirrorRel  = "kit/git/llama.cpp.git"
	mirrorURL  = "https://git.example.test/org-alpha/llama.cpp.git"
)

// commitOf builds a 40-hex commit id at run time: no digest literal is ever written in the repo.
func commitOf(label string) string { return sha([]byte("commit " + label))[:40] }

// mirrorData is what one version of the mirror consists of.
type mirrorData struct {
	head     []byte
	objects  content // files below the mirror directory
	tarballs content // file name -> bytes, installed into kit/src
	commit   string
}

func mirrorV1() mirrorData {
	return mirrorData{
		head:     []byte("ref: refs/heads/main\n"),
		objects:  content{"objects/pack/pack-one.pack": genData(900, 31), "refs/heads/main": genData(41, 32)},
		tarballs: content{"llama.cpp-" + commitOf("one") + ".tar.gz": genData(700, 33)},
		commit:   commitOf("head one"),
	}
}

func mirrorV2() mirrorData {
	return mirrorData{
		head: []byte("ref: refs/heads/trunk\n"),
		objects: content{
			"objects/pack/pack-one.pack": genData(900, 31), "objects/pack/pack-two.pack": genData(1100, 34), "refs/heads/main": genData(41, 35),
		},
		tarballs: content{
			"llama.cpp-" + commitOf("one") + ".tar.gz": genData(700, 33), // unchanged: stays where it is
			"llama.cpp-" + commitOf("two") + ".tar.gz": genData(800, 36),
		},
		commit: commitOf("head two"),
	}
}

func writeAll(t testing.TB, base string, c content) {
	t.Helper()
	for _, name := range sortedKeys(c) {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, c[name], 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// mirrorJob is the git mirror item at version ver. calls counts the Produce calls.
func (e *env) mirrorJob(ver string, d mirrorData, calls *int) Job {
	return Job{
		Root: e.root, Ref: ark.ItemRef{Kind: manifest.KindKit, ID: mirrorItem, Subdir: "git/llama.cpp.git"}, RelDir: mirrorRel,
		CatalogVersion: "2026.11.02-1", MinFree: 1,
		Resolution: &Resolution{
			Version: ver, SortKey: ver, Source: manifest.Source{Type: "git_mirror"}, Dir: true, Resolver: "git_mirror",
			Produce: func(ctx context.Context, env ProduceEnv) (Produced, error) {
				if calls != nil {
					*calls++
				}
				p := Produced{Trust: manifest.TrustComputed, Record: []string{"HEAD"}, Source: map[string]any{"url": mirrorURL, "commit": d.commit}}
				present := filepath.Join(e.root.Path, filepath.FromSlash(mirrorRel))
				if fi, err := os.Stat(present); err == nil && fi.IsDir() {
					writeAll(e.t, present, d.objects)
					writeAll(e.t, present, content{"HEAD": d.head})
					p.InPlace = true
				} else {
					clone := filepath.Join(env.StagingDir, "llama.cpp.git")
					writeAll(e.t, clone, d.objects)
					writeAll(e.t, clone, content{"HEAD": d.head})
					p.TreeFrom = "llama.cpp.git"
				}
				if len(d.tarballs) > 0 {
					writeAll(e.t, filepath.Join(env.StagingDir, "src"), d.tarballs)
					c := Companion{Key: "tarballs", From: "src", To: "kit/src"}
					for _, name := range sortedKeys(d.tarballs) {
						c.Files = append(c.Files, CompanionFile{Path: name, Kind: "source_tarball", Size: int64(len(d.tarballs[name])), SHA256: sha(d.tarballs[name]), SHA256Source: "computed_after_verify"})
					}
					p.Companions = []Companion{c}
				}
				return p, nil
			},
		},
	}
}

// treeHashOf is the tree hash of a content, as a producer computes it.
func treeHashOf(t testing.TB, c content) string {
	t.Helper()
	var sums []manifest.Sum
	for _, name := range sortedKeys(c) {
		sums = append(sums, manifest.Sum{SHA256: sha(c[name]), Path: name})
	}
	h, err := manifest.TreeSHA256(sums)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// docsProductJobs are the two items of a repo_docs product: the docs tree (to docs/) and the kit
// catalog (to kit/catalog/). Each is its own job: its own journal record, verification, swap and
// manifest.
func (e *env) docsProductJobs(ver string, docsC, catC content, calls *int) (docs, cat Job) {
	produce := func(c content, from string) func(context.Context, ProduceEnv) (Produced, error) {
		return func(ctx context.Context, env ProduceEnv) (Produced, error) {
			if calls != nil {
				*calls++
			}
			writeAll(e.t, filepath.Join(env.StagingDir, from), c)
			return Produced{Trust: manifest.TrustComputed, TreeFrom: from, TreeSHA256: treeHashOf(e.t, c), Source: map[string]any{"tag": "catalog-" + ver}}, nil
		}
	}
	docs = Job{
		Root: e.root, Ref: ark.ItemRef{Kind: manifest.KindDocs, ID: "docs"}, RelDir: "docs", CatalogVersion: "2026.11.02-1", MinFree: 1,
		Resolution: &Resolution{Version: ver, SortKey: ver, Source: manifest.Source{Type: "repo_docs"}, Dir: true, Resolver: "repo_docs", Produce: produce(docsC, "tree")},
	}
	cat = Job{
		Root: e.root, Ref: ark.ItemRef{Kind: manifest.KindKit, ID: "kit-catalog", Subdir: "catalog"}, RelDir: "kit/catalog", CatalogVersion: "2026.11.02-1", MinFree: 1,
		Resolution: &Resolution{Version: ver, SortKey: ver, Source: manifest.Source{Type: "repo_docs"}, Dir: true, Resolver: "repo_docs", Produce: produce(catC, "catalog")},
	}
	return docs, cat
}

func catalog1() content {
	return content{"models.yaml": genData(200, 41), "ark.yaml": genData(210, 42), "CATALOG_VERSION": []byte("one\n"), "pins.json": genData(90, 43)}
}
func catalog2() content {
	return content{"models.yaml": genData(220, 44), "ark.yaml": genData(215, 45), "CATALOG_VERSION": []byte("two\n"), "pins.json": genData(95, 46)}
}

// pscenario is one product put through the kill-at-every-step test: the jobs of rev1 are installed
// first (when old is set), then the jobs of rev2 run in one process that dies at the k-th step of all
// of them together.
type pscenario struct {
	name string
	old  bool
	jobs func(e *env, ver string) []Job
	// complete returns nil when item i is installed completely at ver: its manifest, its bytes and its
	// companions.
	complete func(e *env, i int, ver string) error
	// oldBytes returns nil when the bytes of the rev1 version of item i are still somewhere.
	oldBytes func(e *env, i int) error
	items    []string
	rels     []string
	baseline *env
}

func (s *pscenario) manifestOf(e *env, i int) (manifest.Manifest, error) {
	it, err := ark.LoadItem(OSFS{}, e.root, s.rels[i])
	return it.Manifest, err
}

func mirrorComplete(e *env, _ int, ver string) error {
	d := mirrorV1()
	if ver == "rev2" {
		d = mirrorV2()
	}
	dir := filepath.Join(e.root.Path, filepath.FromSlash(mirrorRel))
	m, err := manifest.Load(OSFS{}, dir)
	if err != nil {
		return err
	}
	if m.Version != ver || !m.IsDir() || m.FileCount != 1 {
		return fmt.Errorf("manifest = %+v", m)
	}
	sums, err := manifest.ReadSums(OSFS{}, dir)
	if err != nil || len(sums) != 1 || sums[0].Path != "HEAD" {
		return fmt.Errorf("SHA256SUMS = %v, %v", sums, err)
	}
	if tree, _ := manifest.TreeSHA256(sums); tree != m.TreeSHA256 {
		return errors.New("the tree hash does not follow SHA256SUMS")
	}
	if got := m.Source.String("commit"); got != d.commit {
		return fmt.Errorf("source.commit = %q", got)
	}
	if ver == "rev1" {
		// While a mirror is being updated in place, the producer rewrites HEAD and the refs before the
		// manifest of the new version is committed: the old manifest still describes a valid repository
		// (its packs are all there), and its HEAD sum may lag until the commit. Only the append-only
		// content is exact.
		packs := content{}
		for n, b := range d.objects {
			if strings.HasPrefix(n, "objects/") {
				packs[n] = b
			}
		}
		if err := sameContent(dir, packs); err != nil {
			return err
		}
	} else {
		if sums[0].SHA256 != sha(d.head) {
			return errors.New("SHA256SUMS does not record the HEAD of the new version")
		}
		if err := sameContent(dir, d.objects); err != nil {
			return err
		}
		if err := sameContent(dir, content{"HEAD": d.head}); err != nil {
			return err
		}
	}
	var rec []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size_bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(m.Source.Fields["tarballs"], &rec); err != nil || len(rec) != len(d.tarballs) {
		return fmt.Errorf("source.tarballs = %s, %v", m.Source.Fields["tarballs"], err)
	}
	for _, r := range rec {
		want, ok := d.tarballs[r.Path]
		if !ok || r.SHA256 != sha(want) || r.Size != int64(len(want)) {
			return fmt.Errorf("source.tarballs names %s wrongly", r.Path)
		}
	}
	return sameContent(filepath.Join(e.root.Path, "kit", "src"), d.tarballs)
}

func sameContent(dir string, c content) error {
	for name, data := range c {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if string(got) != string(data) {
			return fmt.Errorf("%s has other bytes than expected", name)
		}
	}
	return nil
}

func mirrorOldBytes(e *env, _ int) error {
	d := mirrorV1()
	dir := filepath.Join(e.root.Path, filepath.FromSlash(mirrorRel))
	// A mirror is append-mostly: every pack of the old version stays, and so does every old tarball. (A
	// ref file is rewritten by git itself; it is not "old bytes" in the sense of the promise.)
	packs := content{}
	for n, b := range d.objects {
		if strings.HasPrefix(n, "objects/") {
			packs[n] = b
		}
	}
	trash := filepath.Join(e.root.Path, ".trash", mirrorItem)
	for n, b := range packs {
		if !bytesAt(filepath.Join(dir, filepath.FromSlash(n)), b) && !trashHas(trash, n, b) && !stagedTreeHas(e.root.Path, mirrorItem, n, b) {
			return fmt.Errorf("the bytes of %s are gone", n)
		}
	}
	return sameContent(filepath.Join(e.root.Path, "kit", "src"), d.tarballs)
}

func docsComplete(e *env, i int, ver string) error {
	docsC, catC := docsAt(ver)
	rel, c := "docs", docsC
	if i == 1 {
		rel, c = "kit/catalog", catC
	}
	it, err := ark.LoadItem(OSFS{}, e.root, rel)
	if err != nil {
		return err
	}
	if it.Manifest.Version != ver || it.Manifest.TreeSHA256 != treeHashOf(e.t, c) {
		return fmt.Errorf("manifest = %+v", it.Manifest)
	}
	if it.Manifest.Source.String("tag") != "catalog-"+ver {
		return fmt.Errorf("source = %+v", it.Manifest.Source)
	}
	return sameContent(it.Dir, c)
}

func docsAt(ver string) (docs, cat content) {
	if ver == "rev2" {
		return docs2(), catalog2()
	}
	return docs1(), catalog1()
}

func docsOldBytes(e *env, i int) error {
	item, rel, c := "docs", "docs", docs1()
	if i == 1 {
		item, rel, c = "kit-catalog", "kit/catalog", catalog1()
	}
	trash := filepath.Join(e.root.Path, ".trash", item)
	for n, b := range c {
		if !bytesAt(filepath.Join(e.root.Path, filepath.FromSlash(rel), filepath.FromSlash(n)), b) && !trashHas(trash, n, b) && !stagedTreeHas(e.root.Path, item, n, b) {
			return fmt.Errorf("%s: the bytes of old file %s are gone", item, n)
		}
	}
	return nil
}

var pscenarios = []pscenario{
	{
		name:     "git mirror: first clone and its tarballs",
		jobs:     func(e *env, ver string) []Job { return []Job{e.mirrorJob(ver, mirrorV2(), nil)} },
		complete: mirrorComplete, oldBytes: mirrorOldBytes, items: []string{mirrorItem}, rels: []string{mirrorRel},
	},
	{
		name: "git mirror: update in place and its tarballs",
		old:  true,
		jobs: func(e *env, ver string) []Job {
			if ver == "rev1" {
				return []Job{e.mirrorJob(ver, mirrorV1(), nil)}
			}
			return []Job{e.mirrorJob(ver, mirrorV2(), nil)}
		},
		complete: mirrorComplete, oldBytes: mirrorOldBytes, items: []string{mirrorItem}, rels: []string{mirrorRel},
	},
	{
		name: "repo docs: the docs tree and the catalog, two items, fresh",
		jobs: func(e *env, ver string) []Job {
			d, c := docsAt(ver)
			a, b := e.docsProductJobs(ver, d, c, nil)
			return []Job{a, b}
		},
		complete: docsComplete, oldBytes: docsOldBytes, items: []string{"docs", "kit-catalog"}, rels: []string{"docs", "kit/catalog"},
	},
	{
		name: "repo docs: the docs tree and the catalog, two items, update through RENAME_EXCHANGE",
		old:  true,
		jobs: func(e *env, ver string) []Job {
			d, c := docsAt(ver)
			a, b := e.docsProductJobs(ver, d, c, nil)
			return []Job{a, b}
		},
		complete: docsComplete, oldBytes: docsOldBytes, items: []string{"docs", "kit-catalog"}, rels: []string{"docs", "kit/catalog"},
	},
}

// neverHalf is the invariant of every instant after a kill, for every item of the product: the old
// version complete, the new version complete, or nothing; and the bytes of the old version are never
// lost.
func (s *pscenario) neverHalf(t *testing.T, e *env, where string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for i := range s.items {
		m, err := s.manifestOf(e, i)
		switch {
		case err == nil && m.Version == "rev1":
			if cerr := s.complete(e, i, "rev1"); cerr != nil {
				t.Fatalf("%s: %s looks like rev1 and is not complete: %v", where, s.items[i], cerr)
			}
			out["old"]++
		case err == nil && m.Version == "rev2":
			if cerr := s.complete(e, i, "rev2"); cerr != nil {
				t.Fatalf("%s: %s looks like rev2 and is not complete: %v", where, s.items[i], cerr)
			}
			out["new"]++
		case err == nil:
			t.Fatalf("%s: %s has the unknown version %s", where, s.items[i], m.Version)
		case errors.Is(err, fs.ErrNotExist):
			out["none"]++
		default:
			t.Fatalf("%s: a half-swapped %s looks present: %v", where, s.items[i], err)
		}
		if s.old {
			if oerr := s.oldBytes(e, i); oerr != nil {
				t.Fatalf("%s: %v", where, oerr)
			}
		}
	}
	return out
}

func (s *pscenario) trial(t *testing.T, k int, after bool) (*env, bool) {
	t.Helper()
	var e *env
	if s.old {
		if s.baseline == nil {
			b := baselineEnv(t)
			for _, j := range s.jobs(b, "rev1") {
				b.install(j)
			}
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
	for _, j := range s.jobs(e, "rev2") {
		e.m.Run(bg, run, j)
		if crash.isDead() {
			break
		}
	}
	return e, crash.isDead()
}

// recover restarts the agent on the disk the kill left: the replay, Recover for the swaps in flight, and
// a Run of every item that is not at its target (the planner does that in real life).
func (s *pscenario) recover(t *testing.T, e *env, where string) {
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
	for i, j := range s.jobs(e, "rev2") {
		rec, ok := e.jr.Items.Get(e.root.ID, s.items[i])
		if ok && rec.State == journal.StateVerified && rec.Version == "rev2" {
			continue
		}
		if res := e.m.Run(bg, run, j); res.State != journal.StateVerified {
			t.Fatalf("%s: the resumed run of %s ended %+v", where, s.items[i], res)
		}
	}
}

func (s *pscenario) assertEnd(t *testing.T, e *env, where string) {
	t.Helper()
	for i, id := range s.items {
		if err := s.complete(e, i, "rev2"); err != nil {
			t.Fatalf("%s: %s is not complete at rev2: %v", where, id, err)
		}
		rec, _ := e.jr.Items.Get(e.root.ID, id)
		if rec.State != journal.StateVerified || rec.Version != "rev2" {
			t.Fatalf("%s: record of %s = %+v", where, id, rec)
		}
		if c, err := e.idx.BestCopy(id); err != nil || c.Manifest.Version != "rev2" {
			t.Fatalf("%s: best copy of %s = %+v, %v", where, id, c, err)
		}
		if s.old {
			tm, err := manifest.Read(OSFS{}, filepath.Join(e.root.Path, ".trash", id, "rev1", "MANIFEST.json"))
			if err != nil || tm.Version != "rev1" {
				t.Fatalf("%s: the old manifest of %s is not in the trash: %+v, %v", where, id, tm, err)
			}
		}
	}
	if left, _ := os.ReadDir(filepath.Join(e.root.Path, ".staging")); len(left) > 0 {
		var names []string
		for _, l := range left {
			names = append(names, l.Name())
		}
		t.Fatalf("%s: staging is not clean: %v", where, names)
	}
}

// TestProductSwapSurvivesAKillAtEveryStep is TestSwapSurvivesAKillAtEveryStep for multi-part products:
// the process dies before and after every journal event, rename, exchange, fsync, chmod and write of the
// whole install or update (a mirror with its tarballs is one item; the docs tree and the catalog are two
// items run one after the other in the same process), then restarts. Nothing half done is ever visible,
// the bytes of the old version are never lost, a restart always ends on the new version, and a second
// restart changes nothing.
func TestProductSwapSurvivesAKillAtEveryStep(t *testing.T) {
	for i := range pscenarios {
		sc := &pscenarios[i]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			clean, died := sc.trial(t, 0, false)
			if died {
				t.Fatal("the clean run died")
			}
			total := clean.fsys.steps()
			sc.assertEnd(t, clean, "clean run")
			if total < 40 {
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
					for kind, n := range sc.neverHalf(t, e, where) {
						outcomes[kind] += n
					}
					sc.recover(t, e, where)
					sc.assertEnd(t, e, where)
					// Recovery is idempotent: a second restart changes nothing.
					e.open(newCrashFS(e.base), e.fetch)
					sc.assertEnd(t, e, where+", restarted again")
				}
			}
			t.Logf("what a reader saw right after a kill: %v", outcomes)
			if sc.old && (outcomes["old"] == 0 || outcomes["new"] == 0 || outcomes["none"] == 0) {
				t.Fatalf("the kills never landed in every phase: %v", outcomes)
			}
		})
	}
}

// ---- unit tests ------------------------------------------------------------------------------------

func TestMirrorProductInstallsTreeAndCompanions(t *testing.T) {
	e := newEnv(t)
	calls := 0
	res := e.install(e.mirrorJob("rev1", mirrorV1(), &calls))
	if res.State != journal.StateVerified || calls != 1 {
		t.Fatalf("res = %+v, calls = %d", res, calls)
	}
	if err := mirrorComplete(e, 0, "rev1"); err != nil {
		t.Fatal(err)
	}
	m, _ := e.loadItem(mirrorRel)
	if m.Trust != manifest.TrustComputed || m.Source.Type != "git_mirror" || m.Source.String("url") != mirrorURL {
		t.Fatalf("manifest = %+v", m)
	}
	// total_bytes counts the mirror and the tarballs, not just HEAD.
	want := int64(len(mirrorV1().head))
	for _, b := range mirrorV1().objects {
		want += int64(len(b))
	}
	for _, b := range mirrorV1().tarballs {
		want += int64(len(b))
	}
	if m.TotalBytes != want {
		t.Fatalf("total_bytes = %d, want %d", m.TotalBytes, want)
	}
	// The mirror was cloned in staging and moved in as a tree; HEAD alone is what SHA256SUMS records.
	if !pathExists(filepath.Join(e.root.Path, mirrorRel, "objects", "pack", "pack-one.pack")) {
		t.Fatal("the clone is not in the item directory")
	}
}

func TestMirrorUpdateIsInPlaceAndKeepsTheOldVersionInTheTrash(t *testing.T) {
	e := newEnv(t)
	e.install(e.mirrorJob("rev1", mirrorV1(), nil))
	oldPack := readFile(t, filepath.Join(e.root.Path, mirrorRel, "objects", "pack", "pack-one.pack"))
	res := e.install(e.mirrorJob("rev2", mirrorV2(), nil))
	if res.State != journal.StateVerified {
		t.Fatalf("res = %+v", res)
	}
	if err := mirrorComplete(e, 0, "rev2"); err != nil {
		t.Fatal(err)
	}
	// No tree moved and nothing was exchanged: the directory kept its inode and its objects.
	if string(readFile(t, filepath.Join(e.root.Path, mirrorRel, "objects", "pack", "pack-one.pack"))) != string(oldPack) {
		t.Fatal("the old pack changed")
	}
	for _, op := range e.fsys.opLog() {
		if strings.HasPrefix(op, "exchange ") {
			t.Fatalf("an in-place update exchanged a directory: %s", op)
		}
	}
	// The old manifest and the old SHA256SUMS are in the trash, the old tarball is still there.
	trash := filepath.Join(e.root.Path, ".trash", mirrorItem, "rev1")
	tm, err := manifest.Read(OSFS{}, filepath.Join(trash, "MANIFEST.json"))
	if err != nil || tm.Version != "rev1" {
		t.Fatalf("old manifest: %+v, %v", tm, err)
	}
	if sums, err := manifest.ReadSums(OSFS{}, trash); err != nil || len(sums) != 1 || sums[0].SHA256 != sha(mirrorV1().head) {
		t.Fatalf("old SHA256SUMS: %v, %v", sums, err)
	}
	// The history of the manifest names the old version.
	m, _ := e.loadItem(mirrorRel)
	if len(m.History) != 1 || m.History[0].Version != "rev1" {
		t.Fatalf("history = %+v", m.History)
	}
	// The unchanged tarball was neither moved nor trashed; the new one arrived.
	if pathExists(filepath.Join(trash, "companions")) {
		t.Fatalf("an unchanged tarball was trashed")
	}
}

func TestADamagedCompanionIsTrashedNotOverwritten(t *testing.T) {
	e := newEnv(t)
	e.install(e.mirrorJob("rev1", mirrorV1(), nil))
	name := "llama.cpp-" + commitOf("one") + ".tar.gz"
	bad := filepath.Join(e.root.Path, "kit", "src", name)
	damaged := genData(700, 99)
	if err := os.WriteFile(bad, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	e.install(e.mirrorJob("rev2", mirrorV2(), nil))
	if err := mirrorComplete(e, 0, "rev2"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(e.root.Path, ".trash", mirrorItem, "rev1", "companions", "tarballs", name))
	if string(got) != string(damaged) {
		t.Fatal("the damaged tarball is not in the trash")
	}
}

func TestProducedContentIsKeptAcrossARestartAtStaged(t *testing.T) {
	e := newEnv(t)
	calls := 0
	job := e.mirrorJob("rev1", mirrorV1(), &calls)
	// Die right after the item is staged: the swap's first step.
	clean, _ := (&pscenario{jobs: func(e *env, ver string) []Job { return []Job{e.mirrorJob(ver, mirrorV1(), nil)} }}).trial(t, 0, false)
	target := 0
	for i, op := range clean.fsys.opLog() {
		// (the reference run is the scenario's rev2, which has the same steps as this rev1 run)
		if strings.HasPrefix(op, "open ") && strings.Contains(op, "/.staging/kit-llama-cpp/rev2/MANIFEST.json.tmp-") {
			target = i + 1 // the step that writes the pending manifest: the item is staged, no plan yet
			break
		}
	}
	if target == 0 {
		t.Fatal("no swap rename in the log")
	}
	e.open(newCrashFS(e.base), nil)
	crash := newCrashFS(e.base)
	e.open(crash, nil)
	crash.arm(target, false)
	run := e.startRun()
	e.m.Run(bg, run, job)
	if !crash.isDead() {
		t.Fatal("did not die")
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if !pathExists(filepath.Join(e.root.Path, ".staging", mirrorItem, "rev1", ProducedName)) {
		t.Fatal("PRODUCED.json is not in staging")
	}
	// Restart: the item is staged, so the swap runs without Produce.
	e.open(newCrashFS(e.base), nil)
	run = e.startRun()
	for _, in := range e.rec.Intents {
		if in.Swap != nil {
			t.Fatalf("a swap plan before the swap started: %+v", in)
		}
	}
	res := e.m.Run(bg, run, e.mirrorJob("rev1", mirrorV1(), &calls))
	if res.State != journal.StateVerified {
		t.Fatalf("res = %+v", res)
	}
	if calls != 1 {
		t.Fatalf("Produce ran again after a restart at staged: calls = %d", calls)
	}
	if err := mirrorComplete(e, 0, "rev1"); err != nil {
		t.Fatal(err)
	}
}

func TestACompanionThatDoesNotMatchIsQuarantinedWithTheItem(t *testing.T) {
	e := newEnv(t)
	d := mirrorV1()
	job := e.mirrorJob("rev1", d, nil)
	inner := job.Resolution.Produce
	job.Resolution.Produce = func(ctx context.Context, env ProduceEnv) (Produced, error) {
		p, err := inner(ctx, env)
		for name := range d.tarballs {
			// the file on disk is not what the producer hashed
			f := filepath.Join(env.StagingDir, "src", name)
			b := readFile(t, f)
			b[3] ^= 0xff
			if werr := os.WriteFile(f, b, 0o644); werr != nil {
				t.Fatal(werr)
			}
		}
		return p, err
	}
	run := e.startRun()
	res := e.m.Run(bg, run, job)
	if res.State != journal.StateQuarantined || res.Code != download.CodeChecksum || res.Quarantined == "" {
		t.Fatalf("res = %+v", res)
	}
	if _, err := e.loadItem(mirrorRel); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an item with a damaged companion is present: %v", err)
	}
	if pathExists(filepath.Join(e.root.Path, "kit", "src")) {
		t.Fatal("a damaged tarball reached kit/src")
	}
	rsn, err := os.ReadFile(filepath.Join(res.Quarantined, ReasonFile))
	if err != nil || !strings.Contains(string(rsn), "tarballs/") {
		t.Fatalf("reason.json = %s, %v", rsn, err)
	}
	// The next run produces again (the quarantined files are gone from staging) and succeeds.
	calls := 0
	res = e.m.Run(bg, run, e.mirrorJob("rev1", d, &calls))
	if res.State != journal.StateVerified || calls != 1 {
		t.Fatalf("second run: %+v, calls %d", res, calls)
	}
}

func TestATreeThatDoesNotMatchTheProducersHashIsQuarantinedAndTheOtherItemIsKept(t *testing.T) {
	e := newEnv(t)
	docs, cat := e.docsProductJobs("rev1", docs1(), catalog1(), nil)
	// the docs tree is damaged between the producer's hash and the verification
	inner := docs.Resolution.Produce
	docs.Resolution.Produce = func(ctx context.Context, env ProduceEnv) (Produced, error) {
		p, err := inner(ctx, env)
		f := filepath.Join(env.StagingDir, "tree", "index.html")
		b := readFile(t, f)
		b[0] ^= 0xff
		if werr := os.WriteFile(f, b, 0o644); werr != nil {
			t.Fatal(werr)
		}
		return p, err
	}
	run := e.startRun()
	dres := e.m.Run(bg, run, docs)
	cres := e.m.Run(bg, run, cat)
	if dres.State != journal.StateQuarantined || cres.State != journal.StateVerified {
		t.Fatalf("docs %+v, catalog %+v", dres, cres)
	}
	if _, err := e.loadItem("docs"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a damaged docs tree is present: %v", err)
	}
	if err := docsComplete(e, 1, "rev1"); err != nil {
		t.Fatal(err)
	}
	// Two quarantines in a row block the version, as for any item; the catalog item is not affected.
	docs2nd, _ := e.docsProductJobs("rev1", docs1(), catalog1(), nil)
	inner2 := docs2nd.Resolution.Produce
	docs2nd.Resolution.Produce = func(ctx context.Context, env ProduceEnv) (Produced, error) {
		p, err := inner2(ctx, env)
		f := filepath.Join(env.StagingDir, "tree", "index.html")
		b := readFile(t, f)
		b[0] ^= 0xff
		_ = os.WriteFile(f, b, 0o644)
		return p, err
	}
	if res := e.m.Run(bg, run, docs2nd); res.State != journal.StateQuarantined {
		t.Fatalf("second: %+v", res)
	}
	d3, _ := e.docsProductJobs("rev1", docs1(), catalog1(), nil)
	if res := e.m.Run(bg, run, d3); res.State != journal.StateSkipped || res.Reason != ReasonBlocked {
		t.Fatalf("third: %+v", res)
	}
}

func TestProducerErrorsAreMappedLikeBuildErrors(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		state     journal.ItemState
		code      string
		retryable bool
	}{
		{"resolve error, permanent", &ResolveError{Code: doctor.SYN024, Permanent: true, Err: errors.New("lacks the commit")}, journal.StateFailed, doctor.SYN024, false},
		{"resolve error, transient", &ResolveError{Code: doctor.SYN031, Err: errors.New("network")}, journal.StateFailed, doctor.SYN031, true},
		{"plain error", errors.New("boom"), journal.StateFailed, CodeUnresolved, true},
		{"disk full", &download.Error{Kind: download.KindDisk, Code: doctor.SYN040, Err: errors.New("disk")}, journal.StateSkipped, doctor.SYN040, true},
		{"permanent http", &download.Error{Kind: download.KindPermanent, Code: doctor.SYN030, Err: errors.New("404")}, journal.StateFailed, doctor.SYN030, false},
		{"exhausted", &download.Error{Kind: download.KindExhausted, Code: doctor.SYN031, Err: errors.New("retries")}, journal.StateFailed, doctor.SYN031, true},
		{"mismatch", &download.Error{Kind: download.KindMismatch, Code: download.CodeChecksum, Err: errors.New("sha")}, journal.StateFailed, download.CodeChecksum, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			job := e.mirrorJob("rev1", mirrorV1(), nil)
			job.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) { return Produced{}, c.err }
			res := e.m.Run(bg, e.startRun(), job)
			if res.State != c.state || res.Code != c.code || res.Retryable != c.retryable {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestProduceCancelledLeavesTheItemDownloading(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(bg)
	job := e.mirrorJob("rev1", mirrorV1(), nil)
	job.Resolution.Produce = func(ctx context.Context, _ ProduceEnv) (Produced, error) {
		cancel()
		return Produced{}, ctx.Err()
	}
	res := e.m.Run(ctx, e.startRun(), job)
	if !res.Cancelled || res.State != journal.StateDownloading {
		t.Fatalf("res = %+v", res)
	}
	rec, _ := e.jr.Items.Get(e.root.ID, mirrorItem)
	if rec.State != journal.StateDownloading {
		t.Fatalf("record = %+v", rec)
	}
}

func TestKeepVerifiesThePresentVersionWithoutASwap(t *testing.T) {
	e := newEnv(t)
	e.install(e.mirrorJob("rev1", mirrorV1(), nil))
	job := e.mirrorJob("rev1", mirrorV1(), nil)
	job.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) { return Produced{Keep: true}, nil }
	// a record at another state, so the machine runs the item
	if _, err := e.jr.Items.Update(e.root.ID, mirrorItem, func(i *journal.Item) { i.State = journal.StateFailed }); err != nil {
		t.Fatal(err)
	}
	before := e.fsys.steps()
	res := e.m.Run(bg, e.startRun(), job)
	if res.State != journal.StateVerified {
		t.Fatalf("res = %+v", res)
	}
	for _, op := range e.fsys.opLog()[before:] {
		if strings.Contains(op, "MANIFEST.json") && strings.HasPrefix(op, "rename ") {
			t.Fatalf("a manifest was renamed for a kept item: %s", op)
		}
	}
	// Keep of a version that is not on disk is a failure, never a verified record.
	bad := e.mirrorJob("rev9", mirrorV1(), nil)
	bad.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) { return Produced{Keep: true}, nil }
	if res := e.m.Run(bg, e.startRun(), bad); res.State != journal.StateFailed {
		t.Fatalf("keep of an absent version: %+v", res)
	}
}

// A kept item (the apt closure did not change) still reports its notices, so a kit built from
// unverified apt repositories (LAI-SYN-063) keeps raising its warning on every run.
func TestKeepCarriesTheNoticesOfTheProducer(t *testing.T) {
	e := newEnv(t)
	e.install(e.mirrorJob("rev1", mirrorV1(), nil))
	job := e.mirrorJob("rev1", mirrorV1(), nil)
	job.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) {
		return Produced{Keep: true, Notices: []string{doctor.SYN063}}, nil
	}
	if _, err := e.jr.Items.Update(e.root.ID, mirrorItem, func(i *journal.Item) { i.State = journal.StateFailed }); err != nil {
		t.Fatal(err)
	}
	res := e.m.Run(bg, e.startRun(), job)
	if res.State != journal.StateVerified || len(res.Notices) != 1 || res.Notices[0] != doctor.SYN063 {
		t.Fatalf("res = %+v", res)
	}
}

func TestNoticesAreRecordedInTheManifestAndTheResult(t *testing.T) {
	e := newEnv(t)
	docs, _ := e.docsProductJobs("rev1", docs1(), catalog1(), nil)
	inner := docs.Resolution.Produce
	docs.Resolution.Produce = func(ctx context.Context, env ProduceEnv) (Produced, error) {
		p, err := inner(ctx, env)
		p.Notices = []string{doctor.CLI014, doctor.SYN062}
		return p, err
	}
	res := e.install(docs)
	if len(res.Notices) != 2 {
		t.Fatalf("notices = %v", res.Notices)
	}
	m, _ := e.loadItem("docs")
	var got []string
	if err := json.Unmarshal(m.Source.Fields[NoticesKey], &got); err != nil || len(got) != 2 || got[0] != doctor.CLI014 || got[1] != doctor.SYN062 {
		t.Fatalf("source.notices = %s, %v", m.Source.Fields[NoticesKey], err)
	}
}

func TestInPlaceNeedsTheDirectoryAndARecordedFile(t *testing.T) {
	e := newEnv(t)
	job := e.mirrorJob("rev1", mirrorV1(), nil)
	job.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) {
		return Produced{InPlace: true, Record: []string{"HEAD"}}, nil
	}
	if res := e.m.Run(bg, e.startRun(), job); res.State != journal.StateFailed {
		t.Fatalf("an in-place item whose directory is missing: %+v", res)
	}
	job.Resolution.Produce = func(context.Context, ProduceEnv) (Produced, error) {
		_ = os.MkdirAll(filepath.Join(e.root.Path, mirrorRel), 0o755)
		return Produced{InPlace: true}, nil
	}
	if res := e.m.Run(bg, e.startRun(), job); res.State != journal.StateFailed {
		t.Fatalf("an in-place item that records no file: %+v", res)
	}
	if _, err := e.loadItem(mirrorRel); err == nil {
		t.Fatal("a failed in-place item is present")
	}
}

func TestValidateProducedRefusesWhatWouldLeaveTheItem(t *testing.T) {
	good := Companion{Key: "tarballs", From: "src", To: "kit/src", Files: []CompanionFile{{Path: "a.tar.gz", Kind: "source_tarball", SHA256: sha([]byte("a"))}}}
	with := func(f func(*Companion)) Produced {
		c := good
		c.Files = append([]CompanionFile(nil), good.Files...)
		f(&c)
		return Produced{Companions: []Companion{c}}
	}
	cases := []struct {
		name string
		p    Produced
		ok   bool
	}{
		{"a good companion", Produced{Companions: []Companion{good}}, true},
		{"a tree from a subdirectory", Produced{TreeFrom: "catalog"}, true},
		{"tree path escapes", Produced{TreeFrom: "../x"}, false},
		{"absolute tree path", Produced{TreeFrom: "/etc"}, false},
		{"in place with a tree", Produced{InPlace: true, TreeFrom: "x"}, false},
		{"in place with keep", Produced{InPlace: true, Keep: true}, false},
		{"record escapes", Produced{Record: []string{"../HEAD"}}, false},
		{"bad tree hash", Produced{TreeSHA256: "abc"}, false},
		{"bad trust", Produced{Trust: "maybe"}, false},
		{"reserved source key", Produced{Source: map[string]any{"type": "x"}}, false},
		{"notices key reserved", Produced{Source: map[string]any{NoticesKey: "x"}}, false},
		{"companion into the item", with(func(c *Companion) { c.To = mirrorRel + "/sub" }), false},
		{"companion over the item", with(func(c *Companion) { c.To = "kit/git" }), false},
		{"companion into staging", with(func(c *Companion) { c.To = ".staging/x" }), false},
		{"companion into the trash", with(func(c *Companion) { c.To = ".trash" }), false},
		{"companion to a parent", with(func(c *Companion) { c.To = "../kit" }), false},
		{"companion staged in the tree", with(func(c *Companion) { c.From = "tree/x" }), false},
		{"companion file escapes", with(func(c *Companion) { c.Files[0].Path = "../a" }), false},
		{"companion file without digest", with(func(c *Companion) { c.Files[0].SHA256 = "" }), false},
		{"companion key is reserved", with(func(c *Companion) { c.Key = "type" }), false},
		{"companion without files", with(func(c *Companion) { c.Files = nil }), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateProduced(c.p, mirrorRel)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestAProducedTreeThatNeedsMovingToTreeSurvivesALeftoverTree(t *testing.T) {
	// An earlier attempt left a tree/ in staging (moved aside, never deleted) and a producer that
	// stages elsewhere is not blocked by it.
	e := newEnv(t)
	stage := filepath.Join(e.root.Path, ".staging", "kit-catalog", "rev1")
	writeAll(t, filepath.Join(stage, "tree"), content{"stale": []byte("stale")})
	_, cat := e.docsProductJobs("rev1", docs1(), catalog1(), nil)
	e.install(cat)
	if err := docsComplete(e, 1, "rev1"); err != nil {
		t.Fatal(err)
	}
}
