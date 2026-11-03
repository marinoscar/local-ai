package run

import (
	"bytes"
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

func data(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// TestFullSyncIntoTwoRoots runs the real machine, the real downloader, the real journal, index and
// retention against a local HTTP server: the first root downloads, the second copies locally after the
// first verified (no second download), both end verified with identical bytes and manifests, the
// events say so, and READY is refreshed after every swap and at the end.
func TestFullSyncIntoTwoRoots(t *testing.T) {
	files := map[string][]byte{"a.gguf": data(300_000, 1), "b.json": data(500, 2)}
	var hits sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:]
		b, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		n, _ := hits.LoadOrStore(name, new(int32))
		atomic.AddInt32(n.(*int32), 1)
		w.Header().Set("Content-Length", itoa(len(b)))
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	base := t.TempDir()
	internal := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), Mount: filepath.Join(base, "drive"), State: ark.StateReady, MountID: 7}
	for _, r := range []ark.Root{internal, drive} {
		if err := os.MkdirAll(r.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := func() []ark.Root { return []ark.Root{internal, drive} }
	clk := newFakeClock()
	fsys := item.OSFS{}

	jr, _, err := journal.Open(bg, journal.Options{Dir: filepath.Join(base, "var"), Now: clk.Now, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	idx := ark.NewIndex(fsys)
	if _, err := idx.Rebuild(bg, roots()); err != nil {
		t.Fatal(err)
	}
	ret, err := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: roots, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Now: clk.Now, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	dl := download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{}), Space: ret, Logger: quiet})
	machine, err := item.New(item.Deps{FS: fsys, Journal: jr, Fetcher: dl, Index: idx, Remover: ret, Roots: roots, Now: clk.Now, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}

	model := &catalog.Model{
		ID: "m1", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/m1", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files: []catalog.ModelFile{
			{Kind: "weights", Name: "a.gguf", URL: srv.URL + "/a.gguf", SizeBytes: int64(len(files["a.gguf"])), SHA256: sha(files["a.gguf"])},
			{Kind: "config", Name: "b.json", URL: srv.URL + "/b.json", SizeBytes: int64(len(files["b.json"])), SHA256: sha(files["b.json"])},
		},
	}
	local := row("m1", 300_500)
	local.Source, local.FromRoot, local.After = plan.SourceLocalCopy, ark.InternalID, ark.InternalID
	first := row("m1", 300_500)
	first.ToVersion, local.ToVersion = "0123456789ab", "0123456789ab"
	planner := &fakePlanner{roots: roots(), items: map[string]plan.CatalogItem{"m1": plan.ModelItem(model)}, plans: []plan.RootPlan{
		{Root: ark.InternalID, Items: []plan.ItemPlan{first}, MinFreeBytes: 1},
		{Root: driveID, Items: []plan.ItemPlan{local}, MinFreeBytes: 1},
	}}
	evs := &recorder{}
	var mu sync.Mutex
	var readyCalls [][]string
	ready := readyFunc(func(ids []string) map[string]string {
		mu.Lock()
		readyCalls = append(readyCalls, append([]string(nil), ids...))
		mu.Unlock()
		out := map[string]string{}
		for _, id := range ids {
			out[id] = "green"
		}
		return out
	})
	eng, err := New(Deps{
		Journal: jr, Machine: machine, Planner: planner, Copies: idx, FS: config.OSFS{}, JournalDir: filepath.Join(base, "var"),
		Settings: DefaultSettings, Events: evs, Clock: clk, Logger: quiet, Priority: func(bool) error { return nil },
		Lock: FileLock{Path: filepath.Join(base, "lock")}, Retention: ret,
		Unknown: func(root ark.Root) ([]string, error) { return jr.Items.UnknownStaging(config.OSFS{}, root) },
		Ready:   ready,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := eng.Start(bg, StartRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Wait(bg, got.RunID); err != nil {
		t.Fatal(err)
	}
	st, _ := eng.Status()
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Done != 2 || st.Totals.Failed != 0 || st.Totals.Items != 2 {
		t.Fatalf("status = %+v", st)
	}

	// Both roots hold the item, byte for byte, with manifests that agree.
	var manifests []manifest.Manifest
	for _, r := range []ark.Root{internal, drive} {
		it, err := ark.LoadItem(fsys, r, "models/m1")
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		manifests = append(manifests, it.Manifest)
		for name, b := range files {
			got, err := os.ReadFile(filepath.Join(r.Path, "models", "m1", name))
			if err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s/%s: %v", r.ID, name, err)
			}
		}
		if left, _ := os.ReadDir(filepath.Join(r.Path, ".staging")); len(left) != 0 {
			t.Fatalf("%s: staging is not clean", r.ID)
		}
	}
	if manifests[0].Version != "0123456789ab" || manifests[0].Version != manifests[1].Version || manifests[0].TotalBytes != 300_500 || manifests[0].SyncedBy.RunID != got.RunID {
		t.Fatalf("manifests = %+v", manifests)
	}
	for i := range manifests[0].Files {
		if manifests[0].Files[i].SHA256 != manifests[1].Files[i].SHA256 {
			t.Fatal("the copies disagree on a checksum")
		}
	}
	if c := idx.Copies("m1"); len(c) != 2 || c[0].RootID != ark.InternalID {
		t.Fatalf("copies = %+v", c)
	}

	// The drive's copy came from the internal root: the server saw each file once.
	hits.Range(func(k, v any) bool {
		if n := atomic.LoadInt32(v.(*int32)); n != 1 {
			t.Errorf("%v was downloaded %d times", k, n)
		}
		return true
	})
	// Events: both items done, the internal root first; READY refreshed after each swap and at the end.
	done := evs.itemDone()
	if len(done) != 2 || done[0].Root != ark.InternalID || done[1].Root != driveID || done[0].Result != "verified" || done[1].Result != "verified" {
		t.Fatalf("item_done = %+v", done)
	}
	if d := evs.done(); len(d) != 1 || d[0].State != StateDone || d[0].Ready[ark.InternalID] != "green" || d[0].Ready[driveID] != "green" {
		t.Fatalf("sync.done = %+v", d)
	}
	if want := [][]string{{ark.InternalID}, {driveID}, {ark.InternalID, driveID}}; !reflect.DeepEqual(readyCalls, want) {
		t.Fatalf("READY refreshed for %v, want %v", readyCalls, want)
	}
	for _, r := range []string{ark.InternalID, driveID} {
		if rec, _ := jr.Items.Get(r, "m1"); rec.State != journal.StateVerified || rec.Version != "0123456789ab" {
			t.Fatalf("%s record = %+v", r, rec)
		}
	}
	// A second sync of the same plan is idempotent: nothing is re-downloaded.
	planner.mu.Lock()
	planner.plans = []plan.RootPlan{{Root: ark.InternalID, Items: []plan.ItemPlan{{ID: "m1", Kind: "model", Action: plan.ActionKeep}}, MinFreeBytes: 1}}
	planner.mu.Unlock()
	got2, _ := eng.Start(bg, StartRequest{Trigger: TriggerManual})
	_ = eng.Wait(bg, got2.RunID)
	if st2, _ := eng.Status(); st2.State != StateDone || st2.Totals.Items != 0 {
		t.Fatalf("second run = %+v", st2)
	}
}

type readyFunc func(ids []string) map[string]string

func (f readyFunc) Refresh(_ context.Context, ids []string) (map[string]string, error) {
	return f(ids), nil
}

// realRig is the real stack (journal, index, retention, downloader, machine, engine) over one root and
// the given HTTP handler.
type realRig struct {
	t    *testing.T
	base string
	root ark.Root
	jr   *journal.Journal
	idx  *ark.Index
	evs  *recorder
	eng  *Engine
	pl   *fakePlanner
	clk  *fakeClock
}

func newRealRig(t *testing.T, handler http.Handler, model *catalog.Model, rows []plan.ItemPlan) *realRig {
	t.Helper()
	base := t.TempDir()
	root := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}
	if err := os.MkdirAll(root.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &realRig{t: t, base: base, root: root, evs: &recorder{}, clk: newFakeClock()}
	r.pl = &fakePlanner{roots: []ark.Root{root}, items: map[string]plan.CatalogItem{model.ID: plan.ModelItem(model)}, plans: []plan.RootPlan{{Root: ark.InternalID, Items: rows, MinFreeBytes: 1}}}
	r.open(handler)
	return r
}

// open (re)builds the whole stack over the disk as it is: an agent start.
func (r *realRig) open(_ http.Handler) {
	r.t.Helper()
	roots := func() []ark.Root { return []ark.Root{r.root} }
	jr, rec, err := journal.Open(bg, journal.Options{Dir: filepath.Join(r.base, "var"), Now: r.clk.Now, Logger: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	r.jr = jr
	r.idx = ark.NewIndex(item.OSFS{})
	if _, err := r.idx.Rebuild(bg, roots()); err != nil {
		r.t.Fatal(err)
	}
	ret, err := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: roots, Copies: r.idx, Journal: jr.Items, Index: r.idx, Items: jr.Items, Now: r.clk.Now, Logger: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	dl := download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{}), Space: ret, Logger: quiet, CheckpointEvery: 1 << 20})
	machine, err := item.New(item.Deps{FS: item.OSFS{}, Journal: jr, Fetcher: dl, Index: r.idx, Remover: ret, Roots: roots, Now: r.clk.Now, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	eng, err := New(Deps{
		Journal: jr, Machine: machine, Planner: r.pl, Copies: r.idx, FS: config.OSFS{}, JournalDir: filepath.Join(r.base, "var"),
		Settings: DefaultSettings, Events: r.evs, Clock: r.clk, Logger: quiet, Priority: func(bool) error { return nil },
		Lock: FileLock{Path: filepath.Join(r.base, "lock")}, Retention: ret,
		Unknown: func(root ark.Root) ([]string, error) { return jr.Items.UnknownStaging(config.OSFS{}, root) },
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.eng = eng
	eng.Resume(bg, rec)
}

// TestCancelMidDownloadThenResumeFromTheCheckpoint cancels a run while a big file is half downloaded,
// checks that the .part and its checkpoint are kept and the item is not present, restarts the agent,
// and runs again: the server sees a Range request that starts where the first run stopped, and the file
// ends byte-identical.
func TestCancelMidDownloadThenResumeFromTheCheckpoint(t *testing.T) {
	body := data(3<<20+12345, 7)
	var mu sync.Mutex
	var ranges []string
	first := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		isFirst := first
		first = false
		mu.Unlock()
		if !isFirst {
			http.ServeContent(w, r, "m1.gguf", time.Time{}, bytes.NewReader(body))
			return
		}
		// The first request: send a little over a block and then hang until the client goes away.
		w.Header().Set("Content-Length", itoa(len(body)))
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(body[:(1<<20)+(1<<19)])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	model := &catalog.Model{
		ID: "m1", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/m1", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files: []catalog.ModelFile{{Kind: "weights", Name: "m1.gguf", URL: srv.URL + "/m1.gguf", SizeBytes: int64(len(body)), SHA256: sha(body)}},
	}
	rw := row("m1", int64(len(body)))
	rw.ToVersion = "0123456789ab"
	r := newRealRig(t, nil, model, []plan.ItemPlan{rw})

	// Wait until the client has APPLIED the first block (progress comes after the write to the .part),
	// not merely until the server has sent it.
	applied := make(chan struct{})
	var once sync.Once
	r.evs.onProgress = func(p events.SyncProgress) {
		if p.BytesDone >= 1<<20 {
			once.Do(func() { close(applied) })
		}
	}
	got, err := r.eng.Start(bg, StartRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	<-applied
	r.eng.Cancel()
	if err := r.eng.Wait(bg, got.RunID); err != nil {
		t.Fatal(err)
	}
	st, _ := r.eng.Status()
	if st.State != StateCancelled || len(r.evs.itemDone()) != 0 {
		t.Fatalf("status = %+v, item_done = %+v", st, r.evs.itemDone())
	}
	if _, err := ark.LoadItem(item.OSFS{}, r.root, "models/m1"); err == nil {
		t.Fatal("a cancelled item is present")
	}
	part := filepath.Join(r.root.Path, ".staging", "m1", "0123456789ab", "m1.gguf.part")
	info, err := os.Stat(part)
	if err != nil || info.Size() < 1<<20 {
		t.Fatalf(".part = %v, %v", info, err)
	}
	rec, _ := r.jr.Items.Get("internal", "m1")
	cp, ok := rec.Checkpoints["m1.gguf"]
	if rec.State != journal.StateDownloading || !ok || cp.Offset < 1<<20 {
		t.Fatalf("record = %+v", rec)
	}

	// The agent restarts and syncs again: it resumes where it stopped.
	r.open(nil)
	got2, err := r.eng.Start(bg, StartRequest{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Wait(bg, got2.RunID); err != nil {
		t.Fatal(err)
	}
	st2, _ := r.eng.Status()
	if st2.State != StateDone || st2.Totals.Done != 1 {
		t.Fatalf("second run = %+v", st2)
	}
	mu.Lock()
	resumed := append([]string(nil), ranges...)
	mu.Unlock()
	if len(resumed) != 2 || resumed[0] != "" || resumed[1] != "bytes="+itoa(int(cp.Offset))+"-" {
		t.Fatalf("requests = %v (the second must resume at the checkpoint, offset %d)", resumed, cp.Offset)
	}
	it, err := ark.LoadItem(item.OSFS{}, r.root, "models/m1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.root.Path, "models", "m1", "m1.gguf")); !bytes.Equal(got, body) {
		t.Fatal("the resumed file differs")
	}
	if it.Manifest.Files[0].SHA256 != sha(body) {
		t.Fatal("the manifest does not carry the digest of the whole file")
	}
	if left, _ := os.ReadDir(filepath.Join(r.root.Path, ".staging")); len(left) != 0 {
		t.Fatalf("staging is not clean: %v", left)
	}
}
