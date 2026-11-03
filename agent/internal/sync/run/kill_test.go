package run

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

const (
	envHelper = "LAI_RUN_HELPER"
	envBase   = "LAI_RUN_BASE"
	envURL    = "LAI_RUN_URL"
	envSeed   = "LAI_RUN_SEED"
	envSize   = "LAI_RUN_SIZE"

	killSize     = 3<<20 + 4097
	killBuf      = 64 << 10
	killCkpt     = 256 << 10
	killVersion  = "0123456789ab"
	killRevision = "0123456789abcdef0123456789abcdef01234567"
)

// stepClock advances one second per reading, so every progress report of the child is published (the
// engine throttles to one a second) and the parent can follow the child's real progress.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func (c *stepClock) AfterFunc(time.Duration, func()) func() bool { return func() bool { return false } }

// printer is the child's event bus: it prints what the engine published, one line each, to stdout.
type printer struct{ mu sync.Mutex }

func (p *printer) Publish(typ, _ string, data any) error {
	if ev, ok := data.(events.SyncProgress); ok {
		p.mu.Lock()
		fmt.Printf("P %s %d\n", ev.State, ev.BytesDone)
		p.mu.Unlock()
	}
	return nil
}

// TestHelperEngineProcess is not a test: the SIGKILL test re-executes the test binary into it to play
// an agent that starts, replays its journal, resumes interrupted swaps, syncs one model and exits 0
// when the run is done. The parent kills it with SIGKILL at a chosen byte of the download.
func TestHelperEngineProcess(t *testing.T) {
	if os.Getenv(envHelper) != "1" {
		t.Skip("helper process")
	}
	seed, _ := strconv.ParseInt(os.Getenv(envSeed), 10, 64)
	size, _ := strconv.Atoi(os.Getenv(envSize))
	body := data(size, seed)
	base := os.Getenv(envBase)
	root := ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}
	roots := func() []ark.Root { return []ark.Root{root} }
	jr, rec, err := journal.Open(context.Background(), journal.Options{Dir: filepath.Join(base, "var"), Logger: quiet,
		Scan: journal.ScanRoots(config.OSFS{}, roots)})
	if err != nil {
		os.Exit(2)
	}
	idx := ark.NewIndex(item.OSFS{})
	if _, err := idx.Rebuild(context.Background(), roots()); err != nil {
		os.Exit(2)
	}
	ret, _ := retention.New(retention.Deps{FS: retention.OSFS{}, Roots: roots, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Logger: quiet})
	dl := download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{}), Space: ret, Logger: quiet, CheckpointEvery: killCkpt, BufSize: killBuf})
	machine, _ := item.New(item.Deps{FS: item.OSFS{}, Journal: jr, Fetcher: dl, Index: idx, Remover: ret, Roots: roots, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})
	model := &catalog.Model{
		ID: "m1", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/m1", Revision: killRevision},
		Files: []catalog.ModelFile{{Kind: "weights", Name: "m1.gguf", URL: os.Getenv(envURL), SizeBytes: int64(len(body)), SHA256: sha(body)}},
	}
	rw := row("m1", int64(len(body)))
	rw.ToVersion = killVersion
	pl := &fakePlanner{roots: roots(), items: map[string]plan.CatalogItem{"m1": plan.ModelItem(model)}, plans: []plan.RootPlan{{Root: ark.InternalID, Items: []plan.ItemPlan{rw}, MinFreeBytes: 1}}}
	eng, err := New(Deps{
		Journal: jr, Machine: machine, Planner: pl, Copies: idx, FS: config.OSFS{}, JournalDir: filepath.Join(base, "var"),
		Settings: DefaultSettings, Priority: func(bool) error { return nil }, Lock: FileLock{Path: filepath.Join(base, "lock")}, Retention: ret,
		Clock: &stepClock{t: epoch}, Events: &printer{},
		Unknown: func(r ark.Root) ([]string, error) { return jr.Items.UnknownStaging(config.OSFS{}, r) },
	})
	if err != nil {
		os.Exit(2)
	}
	eng.Resume(context.Background(), rec)
	got, err := eng.Start(context.Background(), StartRequest{Trigger: TriggerManual})
	if err != nil {
		os.Exit(3)
	}
	_ = eng.Wait(context.Background(), got.RunID)
	if st, _ := eng.Status(); st.State != StateDone || st.Totals.Done != 1 {
		os.Exit(4)
	}
	os.Exit(0)
}

// rangeServer serves the model with Range support and records the Range header of every request.
type rangeServer struct {
	body []byte

	mu     sync.Mutex
	ranges []string
}

func (k *rangeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	k.ranges = append(k.ranges, r.Header.Get("Range"))
	k.mu.Unlock()
	http.ServeContent(w, r, "m1.gguf", time.Time{}, bytes.NewReader(k.body))
}

// TestSIGKILLAtRandomByteOffsetsThenFinish kills a real agent process (real fsyncs, a journal read back
// from its own files, the real engine, machine, downloader and retention) with SIGKILL at random
// offsets of the download, restarts it, and demands that the run finishes: identical bytes, a verified
// manifest, no leftover staging, and the resume never starts anywhere but a checkpoint (acceptance
// criterion of SYNC-07; the kill at every swap step is item/swap_test.go).
func TestSIGKILLAtRandomByteOffsetsThenFinish(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	const trials = killTrials
	rnd := rand.New(rand.NewPCG(21, 22))
	resumed, killedVerifying := 0, 0
	for i := 0; i < trials; i++ {
		// Where the process dies: once the child has REPORTED (after the bytes reached its .part, not
		// merely after the server sent them) a state and at least this many bytes.
		state, at := "downloading", int64(1+rnd.IntN(killSize/killBuf-1))*killBuf
		if i%5 == 4 {
			state, at = "verifying", int64(1+rnd.IntN(killSize-1))
		}
		t.Run(strconv.Itoa(i)+"_"+state+"_at_"+strconv.FormatInt(at, 10), func(t *testing.T) {
			body := data(killSize, 99)
			rs := &rangeServer{body: body}
			srv := httptest.NewServer(rs)
			defer srv.Close()
			base := t.TempDir()
			if err := os.MkdirAll(filepath.Join(base, "srv"), 0o755); err != nil {
				t.Fatal(err)
			}
			child := func() *exec.Cmd {
				c := exec.Command(self, "-test.run=^TestHelperEngineProcess$")
				c.Env = append(os.Environ(), envHelper+"=1", envBase+"="+base, envURL+"="+srv.URL+"/m1.gguf", envSeed+"=99", envSize+"="+strconv.Itoa(killSize))
				return c
			}
			c1 := child()
			out, err := c1.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := c1.Start(); err != nil {
				t.Fatal(err)
			}
			killed := false
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				var st string
				var done int64
				if n, _ := fmt.Sscanf(sc.Text(), "P %s %d", &st, &done); n == 2 && st == state && done >= at && !killed {
					killed = true
					if err := c1.Process.Signal(os.Kill); err != nil {
						t.Fatal(err)
					}
				}
			}
			_ = c1.Wait()
			if !killed {
				t.Fatalf("the agent finished before it reported %s at %d bytes", state, at)
			}
			if state == "verifying" {
				killedVerifying++
			}

			// Restart: the agent replays its journal, resumes, and finishes the run.
			c2 := child()
			if out, err := c2.CombinedOutput(); err != nil {
				t.Fatalf("the restarted agent failed: %v\n%s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(base, "srv", "models", "m1", "m1.gguf"))
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("the file after the kill: %v (%d bytes)", err, len(got))
			}
			it, err := ark.LoadItem(item.OSFS{}, ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady}, "models/m1")
			if err != nil || it.Manifest.Version != killVersion || it.Manifest.Files[0].SHA256 != sha(body) {
				t.Fatalf("manifest: %+v, %v", it.Manifest, err)
			}
			if left, _ := os.ReadDir(filepath.Join(base, "srv", ".staging")); len(left) != 0 {
				t.Fatalf("leftover staging: %v", left)
			}
			rs.mu.Lock()
			ranges := append([]string(nil), rs.ranges...)
			rs.mu.Unlock()
			if len(ranges) < 1 || len(ranges) > 2 || ranges[0] != "" {
				t.Fatalf("requests = %q", ranges)
			}
			// A resume starts at a checkpoint: a whole number of checkpoint steps. (Killed while verifying,
			// the file is complete: no request at all.)
			if len(ranges) == 2 && ranges[1] != "" {
				off, err := strconv.ParseInt(ranges[1][len("bytes="):len(ranges[1])-1], 10, 64)
				if err != nil || off%killCkpt != 0 || off < killCkpt {
					t.Fatalf("resumed with %q", ranges[1])
				}
				resumed++
			}
			if state == "verifying" && len(ranges) != 1 {
				t.Fatalf("a file that was completely downloaded was requested again: %q", ranges)
			}
		})
	}
	if resumed == 0 || killedVerifying == 0 {
		t.Errorf("resumed %d trials from a checkpoint, killed %d while verifying", resumed, killedVerifying)
	}
}
