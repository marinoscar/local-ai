package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

// Multi-part products through the whole engine: the real machine, journal, index and retention over a
// temp node, with fake producers that write the shapes the real ones do (resolve.GitMirror,
// resolve.RepoDocs). No sleeps, no network: the fake clock and channels order everything.

const (
	mirrorDir  = "kit/git/llama.cpp.git"
	mirrorName = "kit-llama-cpp"
)

func docsBundle() plan.CatalogItem {
	return plan.ContentItem(&catalog.ContentItem{ID: "docs-bundle", Kind: "docs", Source: catalog.ContentSource{Type: "repo_docs"}})
}

func kitCatalog() plan.CatalogItem {
	return plan.ContentItem(&catalog.ContentItem{ID: "kit-catalog", Kind: "kit", Source: catalog.ContentSource{Type: "repo_docs"}})
}

func llamaMirror() plan.CatalogItem {
	return plan.ContentItem(&catalog.ContentItem{ID: mirrorName, Kind: "kit", Source: catalog.ContentSource{
		Type: "git_mirror", URL: "https://git.example.test/org-alpha/llama.cpp.git", AlsoTarballOf: "pinned"}})
}

func commit(label string) string { return sha([]byte("commit " + label))[:40] }

func writeTree(t testing.TB, dir string, files map[string][]byte) {
	t.Helper()
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func treePart(t testing.TB, name, stagingDir, from, to string, files map[string][]byte) resolve.Part {
	t.Helper()
	writeTree(t, filepath.Join(stagingDir, from), files)
	var sums []manifest.Sum
	for n, b := range files {
		sums = append(sums, manifest.Sum{SHA256: sha(b), Path: n})
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		t.Fatal(err)
	}
	return resolve.Part{Name: name, Kind: resolve.PartTree, From: from, To: to, Sums: sums, TreeSHA256: tree}
}

// fakeDocs is the repo_docs producer: the docs tree and the kit catalog, each produced for the item
// that wants it.
type fakeDocs struct {
	t       testing.TB
	version string // the version of the catalog item; the docs item gets "+inv"
	docs    map[string][]byte
	cat     map[string][]byte

	mu       sync.Mutex
	wants    []string
	failWant map[string]error
	corrupt  map[string]bool
	notices  []resolve.Notice
}

func (f *fakeDocs) Produce(ctx context.Context, req resolve.Request) (resolve.Product, error) {
	f.mu.Lock()
	f.wants = append(f.wants, req.Want)
	fail, corrupt := f.failWant[req.Want], f.corrupt[req.Want]
	f.mu.Unlock()
	if fail != nil {
		return resolve.Product{}, fail
	}
	staging := req.StagingDir()
	prod := resolve.Product{SortKey: f.version, Trust: manifest.TrustComputed, Source: map[string]any{"tag": "catalog-" + f.version}}
	switch req.Want {
	case resolve.PartNameCatalog:
		prod.Version = "catalog-" + f.version
		prod.Parts = []resolve.Part{treePart(f.t, "catalog", staging, "catalog", "kit/catalog", f.cat)}
	case resolve.PartNameDocs:
		prod.Version = "catalog-" + f.version + "+inv"
		prod.Parts = []resolve.Part{treePart(f.t, "docs", staging, "tree", "docs", f.docs)}
		prod.Notices = f.notices
	default:
		f.t.Errorf("unexpected Want %q", req.Want)
	}
	if corrupt {
		p := filepath.Join(staging, prod.Parts[0].From, "index.html")
		b, err := os.ReadFile(p)
		if err != nil {
			f.t.Fatal(err)
		}
		b[0] ^= 0xff
		if err := os.WriteFile(p, b, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return prod, nil
}

func (f *fakeDocs) wantsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.wants...)
	sort.Strings(out)
	return out
}

// fakeMirror is the git_mirror producer: the first time a clone staged for the engine to swap in,
// afterwards an in-place update, with a tarball per pinned commit staged for kit/src.
type fakeMirror struct {
	t        testing.TB
	version  string // what Product.Version says (may differ from the planned one)
	head     []byte
	objects  map[string][]byte
	tarballs map[string][]byte
	commit   string
	// block, when set, is called inside Produce before anything is written (a cancel test).
	block func(ctx context.Context) error

	mu    sync.Mutex
	calls []string // "<root>:first" or "<root>:update"
}

func (f *fakeMirror) Produce(ctx context.Context, req resolve.Request) (resolve.Product, error) {
	if f.block != nil {
		if err := f.block(ctx); err != nil {
			return resolve.Product{}, err
		}
	}
	present := filepath.Join(req.Root, filepath.FromSlash(mirrorDir))
	part := resolve.Part{Name: "mirror", Record: []string{"HEAD"}, To: mirrorDir}
	staging := req.StagingDir()
	files := map[string][]byte{"HEAD": f.head}
	for n, b := range f.objects {
		files[n] = b
	}
	kind := "first"
	if fi, err := os.Stat(present); err == nil && fi.IsDir() {
		writeTree(f.t, present, files)
		part.Kind = resolve.PartInPlace
		kind = "update"
	} else {
		writeTree(f.t, filepath.Join(staging, "llama.cpp.git"), files)
		part.Kind, part.From = resolve.PartTree, "llama.cpp.git"
	}
	f.mu.Lock()
	f.calls = append(f.calls, req.RootID+":"+kind)
	f.mu.Unlock()
	prod := resolve.Product{Version: f.version, SortKey: f.version, Trust: manifest.TrustComputed,
		Source: map[string]any{"url": req.Item.Content.Source.URL, "commit": f.commit}, Parts: []resolve.Part{part}}
	if len(f.tarballs) > 0 {
		writeTree(f.t, filepath.Join(staging, "src"), f.tarballs)
		var list []manifest.File
		for n, b := range f.tarballs {
			list = append(list, manifest.File{Kind: "source_tarball", Path: n, SizeBytes: int64(len(b)), SHA256: sha(b), SHA256Source: "computed_after_verify"})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
		prod.Parts = append(prod.Parts, resolve.Part{Name: "tarballs", Kind: resolve.PartFile, From: "src", To: "kit/src", Files: list})
	}
	return prod, nil
}

func (f *fakeMirror) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// pstack is the real stack over an internal root and, optionally, an ark drive.
type pstack struct {
	t         *testing.T
	base      string
	internal  ark.Root
	drive     ark.Root
	clk       *fakeClock
	evs       *recorder
	planner   *fakePlanner
	producers map[string]resolve.Producer
	itemRes   map[string]ItemResolver
	jr        *journal.Journal
	idx       *ark.Index
	eng       *Engine
}

func newPStack(t *testing.T, withDrive bool, producers map[string]resolve.Producer, items ...plan.CatalogItem) *pstack {
	t.Helper()
	return newPStackRes(t, withDrive, producers, nil, items...)
}

// newPStackRes is newPStack with plain item resolvers (huggingface, url, ...) registered as well.
func newPStackRes(t *testing.T, withDrive bool, producers map[string]resolve.Producer, itemRes map[string]ItemResolver, items ...plan.CatalogItem) *pstack {
	t.Helper()
	base := t.TempDir()
	s := &pstack{t: t, base: base, clk: newFakeClock(), evs: &recorder{}, producers: producers, itemRes: itemRes}
	s.internal = ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}
	roots := []ark.Root{s.internal}
	if withDrive {
		s.drive = ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), Mount: filepath.Join(base, "drive"), State: ark.StateReady, MountID: 7}
		roots = append(roots, s.drive)
	}
	for _, r := range roots {
		if err := os.MkdirAll(r.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.planner = &fakePlanner{roots: roots, items: map[string]plan.CatalogItem{}}
	for _, it := range items {
		s.planner.items[it.ID] = it
	}
	s.open()
	return s
}

func (s *pstack) roots() []ark.Root {
	if s.drive.ID != "" {
		return []ark.Root{s.internal, s.drive}
	}
	return []ark.Root{s.internal}
}

// open (re)builds the stack over the disk as it is: an agent start.
func (s *pstack) open() {
	s.t.Helper()
	roots := s.roots
	jr, rec, err := journal.Open(bg, journal.Options{Dir: filepath.Join(s.base, "var"), Now: s.clk.Now, Logger: quiet})
	if err != nil {
		s.t.Fatal(err)
	}
	s.jr = jr
	s.idx = ark.NewIndex(item.OSFS{})
	if _, err := s.idx.Rebuild(bg, roots()); err != nil {
		s.t.Fatal(err)
	}
	ret, err := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: roots, Copies: s.idx, Journal: jr.Items, Index: s.idx, Items: jr.Items, Now: s.clk.Now, Logger: quiet})
	if err != nil {
		s.t.Fatal(err)
	}
	dl := download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{}), Space: ret, Logger: quiet})
	machine, err := item.New(item.Deps{FS: item.OSFS{}, Journal: jr, Fetcher: dl, Index: s.idx, Remover: ret, Roots: roots, Now: s.clk.Now, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})
	if err != nil {
		s.t.Fatal(err)
	}
	resolvers := NewItemResolvers()
	for typ, p := range s.producers {
		if err := RegisterProducer(resolvers, typ, p); err != nil {
			s.t.Fatal(err)
		}
	}
	for typ, r := range s.itemRes {
		if err := resolvers.Register(typ, r); err != nil {
			s.t.Fatal(err)
		}
	}
	eng, err := New(Deps{
		Journal: jr, Machine: machine, Planner: s.planner, Copies: s.idx, FS: config.OSFS{}, JournalDir: filepath.Join(s.base, "var"),
		Settings: DefaultSettings, Events: s.evs, Clock: s.clk, Logger: quiet, Priority: func(bool) error { return nil },
		Lock: FileLock{Path: filepath.Join(s.base, "lock")}, Retention: ret, Resolvers: resolvers,
		Unknown: func(r ark.Root) ([]string, error) { return jr.Items.UnknownStaging(config.OSFS{}, r) },
	})
	if err != nil {
		s.t.Fatal(err)
	}
	s.eng = eng
	eng.Resume(bg, rec)
}

func (s *pstack) setRows(perRoot map[string][]plan.ItemPlan) {
	s.planner.mu.Lock()
	defer s.planner.mu.Unlock()
	s.planner.plans = nil
	for _, r := range s.roots() {
		if rows, ok := perRoot[r.ID]; ok {
			s.planner.plans = append(s.planner.plans, plan.RootPlan{Root: r.ID, Items: rows, MinFreeBytes: 1})
		}
	}
}

func (s *pstack) sync() RunStatus {
	s.t.Helper()
	got, err := s.eng.Start(bg, StartRequest{Trigger: TriggerManual})
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.eng.Wait(bg, got.RunID); err != nil {
		s.t.Fatal(err)
	}
	st, _ := s.eng.Status()
	return st
}

func (s *pstack) result(st RunStatus, rootID, id string) ItemResult {
	s.t.Helper()
	for _, r := range st.Items {
		if r.Root == rootID && r.Item == id {
			return r
		}
	}
	s.t.Fatalf("no result for %s/%s in %+v", rootID, id, st.Items)
	return ItemResult{}
}

func (s *pstack) load(root ark.Root, rel string) (manifest.Manifest, error) {
	it, err := ark.LoadItem(item.OSFS{}, root, rel)
	return it.Manifest, err
}

func (s *pstack) assertStagingClean(root ark.Root) {
	s.t.Helper()
	if left, _ := os.ReadDir(filepath.Join(root.Path, ".staging")); len(left) != 0 {
		var names []string
		for _, l := range left {
			names = append(names, l.Name())
		}
		s.t.Fatalf("%s: staging is not clean: %v", root.ID, names)
	}
}

func dlRow(id, kind, version string) plan.ItemPlan {
	return plan.ItemPlan{ID: id, Kind: kind, Action: plan.ActionDownload, ToVersion: version, NeedBytes: 100, FinalBytes: 100, Source: plan.SourceNetwork}
}

func copyRow(id, kind, version string) plan.ItemPlan {
	r := dlRow(id, kind, version)
	r.Source, r.FromRoot, r.After = plan.SourceLocalCopy, ark.InternalID, ark.InternalID
	return r
}

func docsFiles() map[string][]byte {
	return map[string][]byte{"index.html": data(2000, 11), "runbooks/a.html": data(900, 12), "runbooks/deep/b.html": data(40, 13)}
}

func catalogFiles() map[string][]byte {
	return map[string][]byte{"models.yaml": data(300, 21), "ark.yaml": data(310, 22), "CATALOG_VERSION": []byte("one\n"), "pins.json": data(80, 23)}
}

// TestRepoDocsProductIsTwoItems: the docs tree and the kit catalog of one repo_docs product are two items
// with their own journal records, verification, swap and manifest; the producer is asked for the part
// each wants; a drive is filled by local copies of the first root's verified directories (no second
// producer call), and the notices of the docs travel with the copy.
func TestRepoDocsProductIsTwoItems(t *testing.T) {
	fd := &fakeDocs{t: t, version: "one", docs: docsFiles(), cat: catalogFiles(), notices: []resolve.Notice{{Code: doctor.SYN062, Message: "no inventory"}}}
	s := newPStack(t, true, map[string]resolve.Producer{"repo_docs": fd}, docsBundle(), kitCatalog())
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("docs-bundle", "docs", "catalog-one+inv"), dlRow("kit-catalog", "kit", "catalog-one")},
		driveID:        {copyRow("docs-bundle", "docs", "catalog-one+inv"), copyRow("kit-catalog", "kit", "catalog-one")},
	})
	st := s.sync()
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Items != 4 || st.Totals.Done != 4 || st.Totals.Failed != 0 {
		t.Fatalf("status = %+v", st)
	}
	if got := fd.wantsSeen(); len(got) != 2 || got[0] != "catalog" || got[1] != "docs" {
		t.Fatalf("the producer was asked for %v: once per item, the part each wants", got)
	}
	for _, r := range []ark.Root{s.internal, s.drive} {
		dm, err := s.load(r, "docs")
		if err != nil {
			t.Fatalf("%s docs: %v", r.ID, err)
		}
		cm, err := s.load(r, "kit/catalog")
		if err != nil {
			t.Fatalf("%s catalog: %v", r.ID, err)
		}
		if dm.ItemID != "docs-bundle" || dm.ItemKind != "docs" || dm.Version != "catalog-one+inv" || dm.FileCount != len(docsFiles()) {
			t.Fatalf("%s docs manifest = %+v", r.ID, dm)
		}
		if cm.ItemID != "kit-catalog" || cm.ItemKind != "kit" || cm.Version != "catalog-one" || cm.FileCount != len(catalogFiles()) {
			t.Fatalf("%s catalog manifest = %+v", r.ID, cm)
		}
		for name, b := range docsFiles() {
			if got, err := os.ReadFile(filepath.Join(r.Path, "docs", filepath.FromSlash(name))); err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s docs/%s: %v", r.ID, name, err)
			}
		}
		for name, b := range catalogFiles() {
			if got, err := os.ReadFile(filepath.Join(r.Path, "kit", "catalog", filepath.FromSlash(name))); err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s kit/catalog/%s: %v", r.ID, name, err)
			}
		}
		var notices []string
		if err := json.Unmarshal(dm.Source.Fields[item.NoticesKey], &notices); err != nil || len(notices) != 1 || notices[0] != doctor.SYN062 {
			t.Fatalf("%s docs source.notices = %s, %v", r.ID, dm.Source.Fields[item.NoticesKey], err)
		}
		if _, ok := cm.Source.Fields[item.NoticesKey]; ok {
			t.Fatalf("%s: the catalog carries the notices of the docs", r.ID)
		}
		for _, id := range []string{"docs-bundle", "kit-catalog"} {
			if rec, _ := s.jr.Items.Get(r.ID, id); rec.State != journal.StateVerified {
				t.Fatalf("%s/%s record = %+v", r.ID, id, rec)
			}
		}
		s.assertStagingClean(r)
	}
	if dm, _ := s.load(s.internal, "docs"); dm.SyncedBy.RunID != st.RunID {
		t.Fatalf("synced_by = %+v", dm.SyncedBy)
	}
	// Two manifests, two directories: the catalog's manifest lists only the catalog.
	if _, err := os.Stat(filepath.Join(s.internal.Path, "docs", "kit")); err == nil {
		t.Fatal("the catalog was installed inside the docs")
	}
}

// TestOnePartFailsWhileTheOtherVerifies: the producer cannot make the docs; the catalog is still
// installed and verified, the docs item is reported with its own doctor code, and the run is partial.
func TestOnePartFailsWhileTheOtherVerifies(t *testing.T) {
	fd := &fakeDocs{t: t, version: "one", docs: docsFiles(), cat: catalogFiles(),
		failWant: map[string]error{resolve.PartNameDocs: &resolve.Error{Code: resolve.CodeVerifyDocs, Msg: "the rendered manuals lack: a.html"}}}
	s := newPStack(t, false, map[string]resolve.Producer{"repo_docs": fd}, docsBundle(), kitCatalog())
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("docs-bundle", "docs", "catalog-one+inv"), dlRow("kit-catalog", "kit", "catalog-one")},
	})
	st := s.sync()
	if st.State != StateDone || st.Result != ResultPartial || st.Totals.Done != 1 || st.Totals.Failed != 1 {
		t.Fatalf("status = %+v", st)
	}
	d, c := s.result(st, ark.InternalID, "docs-bundle"), s.result(st, ark.InternalID, "kit-catalog")
	if d.Result != "failed" || d.Code != resolve.CodeVerifyDocs || c.Result != "verified" {
		t.Fatalf("docs %+v, catalog %+v", d, c)
	}
	if _, err := s.load(s.internal, "docs"); err == nil {
		t.Fatal("a docs item that failed is present")
	}
	if cm, err := s.load(s.internal, "kit/catalog"); err != nil || cm.Version != "catalog-one" {
		t.Fatalf("the verified catalog: %+v, %v", cm, err)
	}
	// The failed part is retried by the next run and the verified one is left alone.
	fd.mu.Lock()
	fd.failWant = nil
	fd.mu.Unlock()
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("docs-bundle", "docs", "catalog-one+inv"), {ID: "kit-catalog", Kind: "kit", Action: plan.ActionKeep, ToVersion: "catalog-one"}},
	})
	st2 := s.sync()
	if st2.Result != ResultOK || st2.Totals.Done != 1 {
		t.Fatalf("second run = %+v", st2)
	}
	if dm, err := s.load(s.internal, "docs"); err != nil || dm.Version != "catalog-one+inv" {
		t.Fatalf("docs after the retry: %+v, %v", dm, err)
	}
	s.assertStagingClean(s.internal)
}

// TestQuarantineOfOnePartKeepsTheOther: the docs tree is damaged after the producer hashed it; the
// machine's read back from disk notices, quarantines that item with reason.json, and the catalog is not
// affected. Two quarantines in a row block the docs version.
func TestQuarantineOfOnePartKeepsTheOther(t *testing.T) {
	fd := &fakeDocs{t: t, version: "one", docs: docsFiles(), cat: catalogFiles(), corrupt: map[string]bool{resolve.PartNameDocs: true}}
	s := newPStack(t, false, map[string]resolve.Producer{"repo_docs": fd}, docsBundle(), kitCatalog())
	rows := map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow("docs-bundle", "docs", "catalog-one+inv"), dlRow("kit-catalog", "kit", "catalog-one")},
	}
	s.setRows(rows)
	st := s.sync()
	d, c := s.result(st, ark.InternalID, "docs-bundle"), s.result(st, ark.InternalID, "kit-catalog")
	if d.Result != "quarantined" || d.Code != download.CodeChecksum || c.Result != "verified" || st.Result != ResultPartial {
		t.Fatalf("docs %+v, catalog %+v, status %+v", d, c, st)
	}
	q, err := os.ReadDir(filepath.Join(s.internal.Path, ".staging", item.QuarantineDir, "docs-bundle"))
	if err != nil || len(q) != 1 {
		t.Fatalf("quarantine = %v, %v", q, err)
	}
	if _, err := os.Stat(filepath.Join(s.internal.Path, ".staging", item.QuarantineDir, "docs-bundle", q[0].Name(), item.ReasonFile)); err != nil {
		t.Fatalf("no reason.json: %v", err)
	}
	if _, err := s.load(s.internal, "docs"); err == nil {
		t.Fatal("a quarantined docs item is present")
	}
	st2 := s.sync()
	if d2 := s.result(st2, ark.InternalID, "docs-bundle"); d2.Result != "quarantined" {
		t.Fatalf("second run docs = %+v", d2)
	}
	st3 := s.sync()
	d3 := s.result(st3, ark.InternalID, "docs-bundle")
	if d3.Result != "skipped" || d3.Reason != item.ReasonBlocked || d3.Code != item.CodeQuarantined {
		t.Fatalf("third run docs = %+v", d3)
	}
}

// mirrorFiles is a bare repository as the fake producer writes it.
func mirrorFiles(label string) (head []byte, objects map[string][]byte) {
	head = []byte("ref: refs/heads/main\n")
	objects = map[string][]byte{"objects/pack/pack-" + label + ".pack": data(1500, int64(len(label))), "refs/heads/main": []byte(commit(label) + "\n")}
	return head, objects
}

// TestGitMirrorProductEndToEnd: the mirror and its tarballs sync as one item. The first time the clone is
// swapped in with the tarballs beside it; the next run updates it in place; each manifest records the
// tarballs under source.tarballs, kit.SourceFinder reads them from there, and a drive is filled by its own
// producer call (a mirror is not copied file by file between roots). A producer that finds a newer
// version than the plan expected installs under the planned one and records what it holds.
func TestGitMirrorProductEndToEnd(t *testing.T) {
	head, objects := mirrorFiles("one")
	fm := &fakeMirror{t: t, version: "aaaaaaaaaaaa", head: head, objects: objects, commit: commit("one"),
		tarballs: map[string][]byte{"llama.cpp-" + commit("pinned one") + ".tar.gz": data(700, 31)}}
	s := newPStack(t, true, map[string]resolve.Producer{"git_mirror": fm}, llamaMirror())
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {dlRow(mirrorName, "kit", "aaaaaaaaaaaa")},
		driveID:        {copyRow(mirrorName, "kit", "aaaaaaaaaaaa")},
	})
	st := s.sync()
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Done != 2 {
		t.Fatalf("status = %+v", st)
	}
	if got := fm.seen(); len(got) != 2 || got[0] != ark.InternalID+":first" || got[1] != driveID+":first" {
		t.Fatalf("producer calls = %v: the drive makes its own mirror", got)
	}
	for _, r := range []ark.Root{s.internal, s.drive} {
		m, err := s.load(r, mirrorDir)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		if m.ItemID != mirrorName || m.ItemKind != "kit" || m.Version != "aaaaaaaaaaaa" || m.Source.Type != "git_mirror" || m.Trust != manifest.TrustComputed || m.FileCount != 1 {
			t.Fatalf("%s manifest = %+v", r.ID, m)
		}
		for name, b := range fm.tarballs {
			if got, err := os.ReadFile(filepath.Join(r.Path, "kit", "src", name)); err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s kit/src/%s: %v", r.ID, name, err)
			}
			// The rebuild reads the digest from where the engine recorded it.
			if sum, ok := (kit.SourceFinder{}).RecordedSHA256(r.Path, "llama.cpp", name); !ok || sum != sha(b) {
				t.Fatalf("%s: RecordedSHA256 = %q, %v", r.ID, sum, ok)
			}
		}
		s.assertStagingClean(r)
	}

	// Update: the mirror is updated in place and gains a tarball; the old tarball stays.
	head2, objects2 := mirrorFiles("two")
	for n, b := range objects {
		objects2[n] = b
	}
	fm.mu.Lock()
	fm.head, fm.objects, fm.commit = head2, objects2, commit("two")
	fm.tarballs["llama.cpp-"+commit("pinned two")+".tar.gz"] = data(800, 32)
	fm.version = "bbbbbbbbbbbb"
	fm.mu.Unlock()
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {{ID: mirrorName, Kind: "kit", Action: plan.ActionUpdate, ToVersion: "bbbbbbbbbbbb", Source: plan.SourceNetwork, NeedBytes: 100, FinalBytes: 100}},
	})
	st2 := s.sync()
	if st2.Result != ResultOK || st2.Totals.Done != 1 {
		t.Fatalf("update run = %+v", st2)
	}
	m, err := s.load(s.internal, mirrorDir)
	if err != nil || m.Version != "bbbbbbbbbbbb" || len(m.History) != 1 || m.History[0].Version != "aaaaaaaaaaaa" || m.Source.String("commit") != commit("two") {
		t.Fatalf("updated manifest = %+v, %v", m, err)
	}
	if got := fm.seen(); got[len(got)-1] != ark.InternalID+":update" {
		t.Fatalf("producer calls = %v", got)
	}
	for name, b := range fm.tarballs {
		if got, err := os.ReadFile(filepath.Join(s.internal.Path, "kit", "src", name)); err != nil || !bytes.Equal(got, b) {
			t.Fatalf("kit/src/%s after the update: %v", name, err)
		}
	}
	if tm, err := manifest.Read(item.OSFS{}, filepath.Join(s.internal.Path, ".trash", mirrorName, "aaaaaaaaaaaa", manifest.FileName)); err != nil || tm.Version != "aaaaaaaaaaaa" {
		t.Fatalf("the old manifest is not in the trash: %v", err)
	}
	// The drive still has the old, complete version.
	if dm, err := s.load(s.drive, mirrorDir); err != nil || dm.Version != "aaaaaaaaaaaa" {
		t.Fatalf("drive = %+v, %v", dm, err)
	}

	// A source that moved between the plan and the fetch: installed under the planned version, and the
	// manifest says what it really holds.
	fm.mu.Lock()
	fm.version = "cccccccccccc"
	fm.commit = commit("three")
	fm.mu.Unlock()
	s.setRows(map[string][]plan.ItemPlan{
		ark.InternalID: {{ID: mirrorName, Kind: "kit", Action: plan.ActionUpdate, ToVersion: "dddddddddddd", Source: plan.SourceNetwork, NeedBytes: 100, FinalBytes: 100}},
	})
	if st3 := s.sync(); st3.Result != ResultOK {
		t.Fatalf("moved run = %+v", st3)
	}
	m, _ = s.load(s.internal, mirrorDir)
	if m.Version != "dddddddddddd" || m.Source.String("commit") != commit("three") || m.Source.String("produced_version") != "cccccccccccc" {
		t.Fatalf("manifest after the source moved = %+v", m)
	}
}

// TestCancelDuringAProductThenResume: a cancel while the producer is working leaves the item downloading
// and nothing present; the next run (after a restart) produces again and finishes.
func TestCancelDuringAProductThenResume(t *testing.T) {
	head, objects := mirrorFiles("one")
	entered := make(chan struct{}, 4)
	first := true
	var mu sync.Mutex
	fm := &fakeMirror{t: t, version: "aaaaaaaaaaaa", head: head, objects: objects, commit: commit("one"),
		tarballs: map[string][]byte{"llama.cpp-" + commit("pinned one") + ".tar.gz": data(700, 31)}}
	fm.block = func(ctx context.Context) error {
		mu.Lock()
		blocking := first
		first = false
		mu.Unlock()
		entered <- struct{}{}
		if blocking {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	s := newPStack(t, false, map[string]resolve.Producer{"git_mirror": fm}, llamaMirror())
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {dlRow(mirrorName, "kit", "aaaaaaaaaaaa")}})
	got, err := s.eng.Start(bg, StartRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the producer was never called")
	}
	s.eng.Cancel()
	if err := s.eng.Wait(bg, got.RunID); err != nil {
		t.Fatal(err)
	}
	st, _ := s.eng.Status()
	if st.State != StateCancelled || len(s.evs.itemDone()) != 0 {
		t.Fatalf("status = %+v, item_done = %+v", st, s.evs.itemDone())
	}
	if _, err := s.load(s.internal, mirrorDir); err == nil {
		t.Fatal("a cancelled item is present")
	}
	rec, _ := s.jr.Items.Get(ark.InternalID, mirrorName)
	if rec.State != journal.StateDownloading || rec.Version != "aaaaaaaaaaaa" {
		t.Fatalf("record = %+v", rec)
	}
	s.open() // the agent restarts
	st2 := s.sync()
	if st2.State != StateDone || st2.Result != ResultOK || st2.Totals.Done != 1 {
		t.Fatalf("second run = %+v", st2)
	}
	if m, err := s.load(s.internal, mirrorDir); err != nil || m.Version != "aaaaaaaaaaaa" || len(m.Source.Fields["tarballs"]) == 0 {
		t.Fatalf("after the resume: %+v, %v", m, err)
	}
	s.assertStagingClean(s.internal)
}

// TestThePlannerSeesTheParts runs the real planner over a catalog whose repo_docs product has two items
// and a mirror with tarballs: every item is its own row with its own directory and version (the catalog
// item has no "+inv"), and the engine runs each row as its own item.
func TestThePlannerSeesTheParts(t *testing.T) {
	docs := catalog.ContentItem{ID: "docs-bundle", Kind: "docs", ExpectedSizeBytes: 10, Source: catalog.ContentSource{Type: "repo_docs"}}
	cat := catalog.ContentItem{ID: "kit-catalog", Kind: "kit", ExpectedSizeBytes: 5, Source: catalog.ContentSource{Type: "repo_docs"}}
	mir := catalog.ContentItem{ID: mirrorName, Kind: "kit", ExpectedSizeBytes: 20, Source: catalog.ContentSource{
		Type: "git_mirror", URL: "https://git.example.test/org-alpha/llama.cpp.git", AlsoTarballOf: "pinned"}}
	prof := catalog.Profile{ID: "test-profile", Storage: catalog.Storage{Target: "internal", BudgetBytes: 1000, MinFreeBytes: 5},
		Include: catalog.Include{Content: []string{"docs-bundle", "kit-catalog", mirrorName}}}
	cg := catalog.New("one", nil, []catalog.ContentItem{docs, cat, mir}, []catalog.Profile{prof})
	rd := &resolve.RepoDocs{CatalogVersion: func() string { return "one" }, Inventory: func() string { return "/etc/localai/inventory.local.yaml" }}
	resolved := map[string]plan.Resolved{}
	for _, it := range []catalog.ContentItem{docs, cat} {
		it := it
		r, err := rd.Resolve(bg, plan.ContentItem(&it))
		if err != nil {
			t.Fatal(err)
		}
		resolved[it.ID] = r
	}
	resolved[mirrorName] = plan.Resolved{Version: "aaaaaaaaaaaa", SizeBytes: 20}
	rp := plan.Plan(plan.PlanInput{
		Root:    plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/srv/localai", Profile: "test-profile", TotalBytes: 100000, FreeBytes: 100000},
		Catalog: cg, Profile: &prof, Present: map[string]manifest.Manifest{}, Partial: map[string]plan.PartState{}, Resolved: resolved,
		Now: epoch,
	})
	byID := map[string]plan.ItemPlan{}
	for _, r := range rp.Items {
		byID[r.ID] = r
	}
	for id, want := range map[string][2]string{
		"docs-bundle": {"docs", "catalog-one+inv"}, "kit-catalog": {"kit/catalog", "catalog-one"}, mirrorName: {mirrorDir, "aaaaaaaaaaaa"},
	} {
		r, ok := byID[id]
		if !ok || r.Dir != want[0] || r.ToVersion != want[1] || r.Action != plan.ActionDownload {
			t.Fatalf("row of %s = %+v, want dir %s version %s", id, r, want[0], want[1])
		}
	}

	head, objects := mirrorFiles("one")
	fd := &fakeDocs{t: t, version: "one", docs: docsFiles(), cat: catalogFiles()}
	fm := &fakeMirror{t: t, version: "aaaaaaaaaaaa", head: head, objects: objects, commit: commit("one"),
		tarballs: map[string][]byte{"llama.cpp-" + commit("pinned one") + ".tar.gz": data(700, 31)}}
	s := newPStack(t, false, map[string]resolve.Producer{"repo_docs": fd, "git_mirror": fm}, plan.ContentItem(&docs), plan.ContentItem(&cat), plan.ContentItem(&mir))
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: rp.Items})
	st := s.sync()
	if st.Result != ResultOK || st.Totals.Items != 3 || st.Totals.Done != 3 {
		t.Fatalf("status = %+v", st)
	}
	for rel, id := range map[string]string{"docs": "docs-bundle", "kit/catalog": "kit-catalog", mirrorDir: mirrorName} {
		if m, err := s.load(s.internal, rel); err != nil || m.ItemID != id {
			t.Fatalf("%s: %+v, %v", rel, m, err)
		}
	}
}

// TestAptShapedProductAndKeep: a producer of the apt kind (one tree, signed-index trust, the closure in
// the source fields) installs through the engine, and when it says the present version is already the
// target (Keep) the item ends verified with no swap and the manifest untouched.
func TestAptShapedProductAndKeep(t *testing.T) {
	files := map[string][]byte{"pool/a.deb": data(3000, 51), "Packages": data(400, 52), "PACKAGES.lock": data(60, 53)}
	var mu sync.Mutex
	var presentSeen []string
	apt := producerFunc(func(_ context.Context, req resolve.Request) (resolve.Product, error) {
		mu.Lock()
		presentSeen = append(presentSeen, req.PresentVersion)
		mu.Unlock()
		prod := resolve.Product{Version: req.Version, SortKey: "2026-11-02T00:00:00Z", Trust: manifest.TrustSignedIndex,
			Source: map[string]any{"codename": "resolute", "arch": "amd64", "packages": []string{"build-essential"}}}
		if req.PresentDir != "" && req.PresentVersion == req.Version {
			prod.Keep = true
			return prod, nil
		}
		prod.Parts = []resolve.Part{treePart(t, "debs", req.StagingDir(), "tree", req.ItemDir, files)}
		return prod, nil
	})
	debs := plan.ContentItem(&catalog.ContentItem{ID: "kit-debs-resolute-amd64", Kind: "kit", Source: catalog.ContentSource{Type: "apt_closure", Codename: "resolute", Arch: "amd64"}})
	s := newPStack(t, false, map[string]resolve.Producer{"apt_closure": apt}, debs)
	row := dlRow("kit-debs-resolute-amd64", "kit", "closure1")
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {row}})
	if st := s.sync(); st.Result != ResultOK || st.Totals.Done != 1 {
		t.Fatalf("first run = %+v (%+v)", st, st.Items)
	}
	m, err := s.load(s.internal, "kit/debs/resolute-amd64")
	if err != nil || m.Trust != manifest.TrustSignedIndex || m.Version != "closure1" || m.SortKey != "2026-11-02T00:00:00Z" || m.Source.String("codename") != "resolute" || m.FileCount != len(files) {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	before, _ := os.ReadFile(filepath.Join(s.internal.Path, "kit", "debs", "resolute-amd64", manifest.FileName))

	// A refresh that finds the same closure: the producer says Keep, the item is verified, nothing moves.
	row.Action = plan.ActionUpdate
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {row}})
	if st := s.sync(); st.Result != ResultOK || st.Totals.Done != 1 {
		t.Fatalf("keep run = %+v (%+v)", st, st.Items)
	}
	after, _ := os.ReadFile(filepath.Join(s.internal.Path, "kit", "debs", "resolute-amd64", manifest.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("the manifest of a kept item was rewritten")
	}
	if _, err := os.Stat(filepath.Join(s.internal.Path, ".trash", "kit-debs-resolute-amd64")); err == nil {
		t.Fatal("a kept item has a trash entry")
	}
	if rec, _ := s.jr.Items.Get(ark.InternalID, "kit-debs-resolute-amd64"); rec.State != journal.StateVerified || rec.Version != "closure1" {
		t.Fatalf("record = %+v", rec)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(presentSeen) != 2 || presentSeen[0] != "" || presentSeen[1] != "closure1" {
		t.Fatalf("the producer saw the present versions %v", presentSeen)
	}
	s.assertStagingClean(s.internal)
}

// TestProductResolverMapsParts is the table of how a product's parts become what the machine installs.
func TestProductResolverMapsParts(t *testing.T) {
	tarballs := resolve.Part{Name: "tarballs", Kind: resolve.PartFile, From: "src", To: "kit/src",
		Files: []manifest.File{{Kind: "source_tarball", Path: "llama.cpp-x.tar.gz", SizeBytes: 3, SHA256: sha([]byte("x"))}}}
	mirrorTree := resolve.Part{Name: "mirror", Kind: resolve.PartTree, From: "llama.cpp.git", To: mirrorDir, Record: []string{"HEAD"}}
	mirrorInPlace := resolve.Part{Name: "mirror", Kind: resolve.PartInPlace, To: mirrorDir, Record: []string{"HEAD"}}
	docsPart := resolve.Part{Name: "docs", Kind: resolve.PartTree, From: "tree", To: "docs", TreeSHA256: sha([]byte("docs"))}
	catPart := resolve.Part{Name: "catalog", Kind: resolve.PartTree, From: "catalog", To: "kit/catalog", TreeSHA256: sha([]byte("cat"))}

	tk := func(it plan.CatalogItem, rel string) Task {
		return Task{Item: it, Row: plan.ItemPlan{ID: it.ID, ToVersion: "v1"}, Root: ark.Root{ID: ark.InternalID, Path: t.TempDir()}, RelDir: rel}
	}
	cases := []struct {
		name    string
		task    Task
		newerOK bool
		prod    resolve.Product
		check   func(t *testing.T, p item.Produced)
		code    string
	}{
		{"the docs item takes the docs part and ignores the catalog", tk(docsBundle(), "docs"), false,
			resolve.Product{Version: "v1", Parts: []resolve.Part{docsPart, catPart}},
			func(t *testing.T, p item.Produced) {
				if p.TreeFrom != "tree" || p.TreeSHA256 != docsPart.TreeSHA256 || p.InPlace || len(p.Companions) != 0 {
					t.Fatalf("%+v", p)
				}
			}, ""},
		{"the catalog item takes the catalog part and ignores the docs", tk(kitCatalog(), "kit/catalog"), false,
			resolve.Product{Version: "v1", Parts: []resolve.Part{docsPart, catPart}},
			func(t *testing.T, p item.Produced) {
				if p.TreeFrom != "catalog" || p.TreeSHA256 != catPart.TreeSHA256 {
					t.Fatalf("%+v", p)
				}
			}, ""},
		{"a first clone: the tree with its recorded file, and the tarballs as a companion", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{mirrorTree, tarballs}},
			func(t *testing.T, p item.Produced) {
				if p.TreeFrom != "llama.cpp.git" || p.TreeSHA256 != "" || len(p.Record) != 1 || len(p.Companions) != 1 ||
					p.Companions[0].Key != "tarballs" || p.Companions[0].To != "kit/src" || p.Companions[0].From != "src" || p.Companions[0].Files[0].Path != "llama.cpp-x.tar.gz" {
					t.Fatalf("%+v", p)
				}
			}, ""},
		{"an update in place", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{mirrorInPlace, tarballs}},
			func(t *testing.T, p item.Produced) {
				if !p.InPlace || p.TreeFrom != "" || len(p.Record) != 1 {
					t.Fatalf("%+v", p)
				}
			}, ""},
		{"a file set into a part the item may not write is not installed", tk(plan.ContentItem(&catalog.ContentItem{ID: mirrorName, Kind: "kit", Source: catalog.ContentSource{
			Type: "git_mirror", URL: "https://git.example.test/org-alpha/llama.cpp.git"}}), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{mirrorTree, tarballs}},
			func(t *testing.T, p item.Produced) {
				if len(p.Companions) != 0 {
					t.Fatalf("a companion the layout does not allow: %+v", p.Companions)
				}
			}, ""},
		{"a file set outside kit/ is not installed", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{mirrorTree, {Name: "x", Kind: resolve.PartFile, From: "x", To: "models/x", Files: tarballs.Files}}},
			func(t *testing.T, p item.Produced) {
				if len(p.Companions) != 0 {
					t.Fatalf("%+v", p.Companions)
				}
			}, ""},
		{"an in-place part that records nothing is refused", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{{Name: "mirror", Kind: resolve.PartInPlace, To: mirrorDir}}}, nil, item.CodeInternal},
		{"a file set as the item itself is refused", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v1", Parts: []resolve.Part{{Name: "mirror", Kind: resolve.PartFile, To: mirrorDir}}}, nil, item.CodeInternal},
		{"no part for the item", tk(kitCatalog(), "kit/catalog"), false,
			resolve.Product{Version: "v1", Parts: []resolve.Part{docsPart}}, nil, item.CodeInternal},
		{"two parts for the item", tk(kitCatalog(), "kit/catalog"), false,
			resolve.Product{Version: "v1", Parts: []resolve.Part{catPart, catPart}}, nil, item.CodeInternal},
		{"a newer version than planned is refused unless the type allows it", tk(docsBundle(), "docs"), false,
			resolve.Product{Version: "v2", Parts: []resolve.Part{docsPart}}, nil, doctor.SYN033},
		{"a newer version is recorded when the type allows it", tk(llamaMirror(), mirrorDir), true,
			resolve.Product{Version: "v2", Parts: []resolve.Part{mirrorTree}},
			func(t *testing.T, p item.Produced) {
				if p.Source["produced_version"] != "v2" {
					t.Fatalf("%+v", p.Source)
				}
			}, ""},
		{"keep", tk(docsBundle(), "docs"), false, resolve.Product{Version: "v1", Keep: true},
			func(t *testing.T, p item.Produced) {
				if !p.Keep {
					t.Fatalf("%+v", p)
				}
			}, ""},
		{"notices and extras are passed on", tk(docsBundle(), "docs"), false,
			resolve.Product{Version: "v1", SortKey: "one", Trust: manifest.TrustComputed, Parts: []resolve.Part{docsPart},
				Notices: []resolve.Notice{{Code: doctor.CLI014, Message: "no pdf"}}, Source: map[string]any{"tag": "catalog-one"}},
			func(t *testing.T, p item.Produced) {
				if len(p.Notices) != 1 || p.Notices[0] != doctor.CLI014 || p.SortKey != "one" || p.Trust != manifest.TrustComputed || p.Source["tag"] != "catalog-one" {
					t.Fatalf("%+v", p)
				}
			}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr := ProductResolver{Producer: stubProducer{c.prod}, NewerOK: c.newerOK}
			rsl, err := pr.ResolveTask(bg, c.task)
			if err != nil {
				t.Fatal(err)
			}
			if !rsl.Dir || rsl.Produce == nil || rsl.Version != "v1" || rsl.Source.Type != c.task.Item.SourceType() {
				t.Fatalf("resolution = %+v", rsl)
			}
			p, err := rsl.Produce(bg, item.ProduceEnv{StagingDir: filepath.Join(c.task.Root.Path, ".staging", c.task.Item.ID, "v1")})
			if c.code != "" {
				var re *item.ResolveError
				if !errors.As(err, &re) || re.Code != c.code {
					t.Fatalf("err = %v, want code %s", err, c.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, p)
		})
	}
}

type stubProducer struct{ prod resolve.Product }

func (s stubProducer) Produce(context.Context, resolve.Request) (resolve.Product, error) {
	return s.prod, nil
}

func TestProductResolverBuildsTheRequestFromTheTask(t *testing.T) {
	var got resolve.Request
	capture := producerFunc(func(_ context.Context, req resolve.Request) (resolve.Product, error) {
		got = req
		return resolve.Product{Version: "v1", Keep: true}, nil
	})
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(t.TempDir(), "localai")}
	other := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(t.TempDir(), "srv")}
	present := ark.Copy{RootID: driveID, Dir: filepath.Join(drive.Path, "docs"), Manifest: manifest.Manifest{Version: "v0"}}
	pr := ProductResolver{Producer: capture, Want: func(plan.CatalogItem) string { return "docs" }}
	rsl, err := pr.ResolveTask(bg, Task{Item: docsBundle(), Row: plan.ItemPlan{ID: "docs-bundle", ToVersion: "v1"}, Root: drive, RelDir: "docs",
		CatalogVersion: "2026.11.02-1", MinFree: 77, OtherRoots: []ark.Root{other}, Present: &present})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rsl.Produce(bg, item.ProduceEnv{StagingDir: filepath.Join(drive.Path, ".staging", "docs-bundle", "v1")}); err != nil {
		t.Fatal(err)
	}
	if got.RootID != driveID || got.Root != drive.Path || !got.Drive || got.Version != "v1" || got.CatalogVersion != "2026.11.02-1" || got.ItemDir != "docs" ||
		got.MinFreeBytes != 77 || got.Want != "docs" || len(got.OtherRoots) != 1 || got.OtherRoots[0] != other.Path ||
		got.PresentDir != present.Dir || got.PresentVersion != "v0" {
		t.Fatalf("request = %+v", got)
	}
	// A staging directory that is not where the producer writes is refused: nothing is produced elsewhere.
	if _, err := rsl.Produce(bg, item.ProduceEnv{StagingDir: filepath.Join(drive.Path, ".staging", "other", "v1")}); err == nil {
		t.Fatal("a producer was run for another staging directory")
	}
	// No version in the plan: unresolved, permanent.
	_, err = pr.ResolveTask(bg, Task{Item: docsBundle(), Row: plan.ItemPlan{ID: "docs-bundle"}, Root: drive, RelDir: "docs"})
	var re *item.ResolveError
	if !errors.As(err, &re) || re.Code != doctor.SYN033 || !re.Permanent {
		t.Fatalf("err = %v", err)
	}
}

type producerFunc func(context.Context, resolve.Request) (resolve.Product, error)

func (f producerFunc) Produce(ctx context.Context, r resolve.Request) (resolve.Product, error) {
	return f(ctx, r)
}

func TestProducerErrorsKeepTheirDoctorCode(t *testing.T) {
	for _, tc := range []struct {
		err       error
		code      string
		permanent bool
	}{
		{&resolve.Error{Code: resolve.CodeMirror, Msg: "damaged"}, resolve.CodeMirror, false},
		{&resolve.Error{Code: resolve.CodeCatalogLint, Msg: "lint"}, resolve.CodeCatalogLint, true},
		{&resolve.Error{Code: resolve.CodeTool, Msg: "no git"}, resolve.CodeTool, true},
		{&resolve.Error{Code: resolve.CodeRefused, Msg: "refused"}, resolve.CodeRefused, true},
		{&resolve.Error{Code: resolve.CodeOffline, Msg: "offline"}, resolve.CodeOffline, false},
	} {
		got := mapProducerError(tc.err)
		var re *item.ResolveError
		if !errors.As(got, &re) || re.Code != tc.code || re.Permanent != tc.permanent {
			t.Errorf("%v -> %v", tc.err, got)
		}
	}
	dl := &download.Error{Kind: download.KindDisk, Code: doctor.SYN040, Err: errors.New("disk")}
	if got := mapProducerError(dl); got != error(dl) {
		t.Errorf("a downloader error was wrapped: %v", got)
	}
	if got := mapProducerError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("a cancel was changed: %v", got)
	}
}

func TestRegisterProducer(t *testing.T) {
	r := NewItemResolvers()
	if err := RegisterProducer(r, "git_mirror", stubProducer{}); err != nil {
		t.Fatal(err)
	}
	tr, ok := r.TaskFor("git_mirror")
	if !ok || tr.CopiesLocally() {
		t.Fatal("a mirror must not be copied file by file between roots")
	}
	if err := RegisterProducer(r, "repo_docs", stubProducer{}); err != nil {
		t.Fatal(err)
	}
	if tr, _ := r.TaskFor("repo_docs"); !tr.CopiesLocally() {
		t.Fatal("a docs tree is copied between roots")
	}
	if err := RegisterProducer(r, "apt_closure", stubProducer{}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterProducer(r, "git_mirror", stubProducer{}); err == nil {
		t.Fatal("a second resolver for one source type was accepted")
	}
	if err := r.Register("repo_docs", ItemResolverFunc(nil)); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("Register over a task resolver: %v", err)
	}
	if err := RegisterProducer(r, "huggingface", stubProducer{}); err == nil {
		t.Fatal("a source type without a producer was accepted")
	}
	if _, ok := r.TaskFor("url"); ok {
		t.Fatal("TaskFor found an unregistered type")
	}
	var nilReg *ItemResolvers
	if _, ok := nilReg.TaskFor("git_mirror"); ok {
		t.Fatal("a nil registry found a resolver")
	}
}
