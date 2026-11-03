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

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
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
