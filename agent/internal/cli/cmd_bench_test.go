package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/router/bench"
)

// Tests of `aictl bench` (AICTL-05, aictl.md section 4.24). The real engine (internal/router/bench)
// runs against a fake machine: a fake agent that serves the models routes and the event stream, a
// local HTTP server standing in for the router's chat endpoint, a GPU whose used memory follows the
// loaded models, and a fake llama-bench. Nothing sleeps: the chat request ends when the engine has
// sampled the GPU a few times while it ran.

const (
	benchPreset = "[" + qwenID + "]\nmodel = /srv/localai/models/qwen/qwen.gguf\nc = 32768\nn-gpu-layers = 99\nflash-attn = on\n"
	benchJSONOK = `[{"n_prompt":512,"n_gen":0,"avg_ts":1450.21,"stddev_ts":3.1},{"n_prompt":0,"n_gen":128,"avg_ts":38.41,"stddev_ts":0.2}]`
	benchGPU    = "NVIDIA GeForce RTX 2070 with Max-Q Design"

	idleMiB    = 210  // an idle GPU
	weightsMiB = 5800 // one loaded model
	requestMiB = 781  // KV cache and compute buffers, only during a request
)

var benchNow = time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)

// benchWorld is the machine the fakes share.
type benchWorld struct {
	mu       sync.Mutex
	cond     *sync.Cond
	loaded   []string
	chatting bool
	samples  int
	opSeq    int
	ops      []string
	loading  map[string]bool // models the agent reports as loading
	failLoad map[string]bool // loads of these models answer 500
	failBusy map[string]bool // unloads of these models answer 409 LAI-AGT-022
}

func newBenchWorld(loaded ...string) *benchWorld {
	w := &benchWorld{loaded: append([]string(nil), loaded...), loading: map[string]bool{}, failLoad: map[string]bool{}, failBusy: map[string]bool{}}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *benchWorld) isLoaded(id string) bool {
	for _, l := range w.loaded {
		if l == id {
			return true
		}
	}
	return false
}

func (w *benchWorld) usedMiB() int64 {
	used := int64(idleMiB) + int64(len(w.loaded))*weightsMiB
	if w.chatting {
		used += requestMiB
	}
	return used
}

func (w *benchWorld) Loaded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.loaded...)
}

func (w *benchWorld) Ops() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.ops...)
}

// SampleGPUs implements bench.GPUSampler.
func (w *benchWorld) SampleGPUs(context.Context) ([]hardware.GPU, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.chatting {
		w.samples++
		w.cond.Broadcast()
	}
	return []hardware.GPU{{Vendor: hardware.VendorNVIDIA, Name: benchGPU, VRAMTotalMiB: 7982, VRAMUsedMiB: w.usedMiB()}}, nil
}

// benchRig is the machine plus the command's view of it.
type benchRig struct {
	*mgRig
	w        *benchWorld
	exec     *execfake.Runner
	dir      string
	router   *httptest.Server
	key      string
	preset   string
	present  bool
	node     string // node.yaml text
	dropIn   string
	chatErr  bool   // the router answers the chat request with 500
	chatHook func() // runs when a chat request starts
	// chatBlocks makes the router hold the chat request until the client goes away (Ctrl-C).
	chatBlocks  bool
	chatStarted chan struct{}
	gate        func() // runs when llama-bench is about to run
	chatAuth    []string
	timings     string
	seen        chan events.Event
}

func newBenchRig(t *testing.T, loaded ...string) *benchRig {
	t.Helper()
	b := &benchRig{
		mgRig: newMgRig(t, fake.Options{}), w: newBenchWorld(loaded...), exec: execfake.New(), dir: t.TempDir(),
		key: mgKey(), preset: benchPreset, present: true,
		node:    "node:\n  id: ai-n3\nllama:\n  backend: cuda\n",
		timings: `{"timings":{"predicted_per_second":41.04,"draft_n":100,"draft_n_accepted":71}}`,
		seen:    make(chan events.Event, 256), chatStarted: make(chan struct{}, 1),
	}
	ps, err := bench.ParsePreset(b.preset)
	if err != nil {
		t.Fatal(err)
	}
	b.exec.Expect(execfake.Key(bench.DefaultBenchBinary, ps.BenchArgs()...), execx.Result{Stdout: []byte(benchJSONOK)}, nil)

	// The router's chat endpoint: it answers once the engine has sampled the GPU while the request
	// ran, so the memory curve always holds the request's peak.
	b.router = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server notice a client that goes away (Ctrl-C).
		io.Copy(io.Discard, r.Body)
		b.w.mu.Lock()
		b.chatAuth = append(b.chatAuth, r.Header.Get("Authorization"))
		b.w.mu.Unlock()
		if b.chatHook != nil {
			b.chatHook()
		}
		if b.chatBlocks {
			select {
			case b.chatStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		if b.chatErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		b.w.mu.Lock()
		b.w.chatting = true
		b.w.samples = 0
		for b.w.samples < 3 {
			b.w.cond.Wait()
		}
		b.w.chatting = false
		b.w.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, b.timings)
	}))
	t.Cleanup(b.router.Close)

	ev := b.events()
	ag := b.agent()
	ag.Handle(http.MethodGet, "/models", func(w http.ResponseWriter, _ *http.Request) {
		b.w.mu.Lock()
		defer b.w.mu.Unlock()
		status := func(id string) string {
			switch {
			case b.w.loading[id]:
				return "loading"
			case b.w.isLoaded(id):
				return "loaded"
			}
			return "unloaded"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"models":[{"id":%q,"display_name":"Qwen 3.5 9B","engine":"llama","present":true,"verified":true,"status":%q},`+
			`{"id":%q,"display_name":"Small VLM","engine":"llama","present":true,"verified":true,"status":%q}]}`,
			qwenID, status(qwenID), smallID, status(smallID))
	})
	ag.Handle(http.MethodGet, "/models/"+qwenID, func(w http.ResponseWriter, _ *http.Request) {
		b.w.mu.Lock()
		defer b.w.mu.Unlock()
		doc := map[string]any{"id": qwenID, "display_name": "Qwen 3.5 9B", "engine": "llama", "present": b.present, "verified": b.present,
			"status": "unloaded", "preset": b.preset}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	})
	opID := func() string { b.w.opSeq++; return fmt.Sprintf("op_%012d", b.w.opSeq) }
	for _, id := range []string{qwenID, smallID} {
		id := id
		ag.Handle(http.MethodPost, "/models/"+id+"/load", func(w http.ResponseWriter, _ *http.Request) {
			b.w.mu.Lock()
			b.w.ops = append(b.w.ops, "load "+id)
			if b.w.failLoad[id] {
				b.w.mu.Unlock()
				fake.WriteEnvelope(w, 500, "LAI-LLM-009", "The model failed to start.", "Check the logs.", nil)
				return
			}
			op := opID()
			if !b.w.isLoaded(id) {
				b.w.loaded = append(b.w.loaded, id)
			}
			b.w.mu.Unlock()
			ev.Publish(events.TypeModelLoading, op, events.ModelLoading{Model: id, OpID: op})
			ev.Publish(events.TypeModelLoaded, op, events.ModelLoaded{Model: id, LoadMS: 100})
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"status":"loaded","already_loaded":false,"load_ms":100,"unloaded":[],"op_id":%q}`, id, op)
		})
		ag.Handle(http.MethodPost, "/models/"+id+"/unload", func(w http.ResponseWriter, _ *http.Request) {
			b.w.mu.Lock()
			b.w.ops = append(b.w.ops, "unload "+id)
			if b.w.failBusy[id] {
				b.w.mu.Unlock()
				fake.WriteEnvelope(w, 409, "LAI-AGT-022", "Another model operation is running.", "Wait.", map[string]any{"op_id": "op_busy"})
				return
			}
			op := opID()
			kept := b.w.loaded[:0:0]
			for _, l := range b.w.loaded {
				if l != id {
					kept = append(kept, l)
				}
			}
			b.w.loaded = kept
			b.w.mu.Unlock()
			ev.Publish(events.TypeModelUnloaded, op, events.ModelUnloaded{Model: id, Reason: events.UnloadRequest})
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"status":"unloaded","already_unloaded":false,"op_id":%q}`, id, op)
		})
	}
	ag.Handle(http.MethodGet, "/key", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"key":%q,"fingerprint":"c0de…c0de","base_urls":{"local":%q,"tailnet":null,"public":null},"models":[]}`,
			b.key, b.router.URL+"/v1")
	})
	return b
}

// install replaces the host of the command with this machine.
func (b *benchRig) install(t *testing.T) {
	t.Helper()
	nodeFile, dropFile := filepath.Join(b.dir, "node.yaml"), filepath.Join(b.dir, "50-env.conf")
	if err := os.WriteFile(nodeFile, []byte(b.node), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.dropIn != "" {
		if err := os.WriteFile(dropFile, []byte(b.dropIn), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(b.dir, "current")
	if err := os.Symlink("/opt/localai/llama.cpp/builds/f3586e66b-cuda", link); err != nil {
		t.Fatal(err)
	}
	var runner execx.Runner = b.exec
	if b.gate != nil {
		runner = gateExec{inner: b.exec, before: b.gate}
	}
	old := benchHostFn
	t.Cleanup(func() { benchHostFn = old })
	benchHostFn = func() benchHost {
		return benchHost{
			GPU: b.w, Exec: runner, Dir: filepath.Join(b.dir, "bench"), BuildLink: link, NodeYAML: nodeFile, DropIn: dropFile,
			Now:     func() time.Time { return benchNow },
			Sleep:   func(ctx context.Context, _ time.Duration) error { runtime.Gosched(); return ctx.Err() },
			OnEvent: func(ev events.Event) { b.seen <- ev },
		}
	}
}

// gateExec runs before first, then the fake llama-bench.
type gateExec struct {
	inner  execx.Runner
	before func()
}

func (g gateExec) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	g.before()
	return g.inner.Run(ctx, c)
}

func (b *benchRig) benchArgs(a ...string) mgRun { return mgArgs(append([]string{"bench"}, a...)...) }

func (b *benchRig) resultPath() string {
	return filepath.Join(b.dir, "bench", qwenID+"-20261102T103000Z.json")
}

const benchQuestion = "The test needs the graphics memory to itself: Small VLM will be unloaded and loaded again afterwards. Continue? [y/N] "

func TestBench(t *testing.T) {
	wantProgress := "… Unloading Small VLM so the test has the memory to itself\n" +
		"… Measuring graphics memory at full context (32768 tokens)\n" +
		"… Measuring speed with llama-bench\n" +
		"… Loading Small VLM again\n"

	t.Run("asks, measures, saves, prints the catalog value and loads the model that was loaded", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(b.benchArgs(qwenID).withTerm("y"))
		checkRun(t, got, 0, wantBenchOut(b), benchQuestion+wantProgress)
		if loaded := strings.Join(b.w.Loaded(), ","); loaded != smallID {
			t.Errorf("loaded after the run: %s (the models of before must be back)", loaded)
		}
		if ops := strings.Join(b.w.Ops(), ","); ops != "unload "+smallID+",load "+qwenID+",unload "+qwenID+",load "+smallID {
			t.Errorf("operations: %s", ops)
		}
		// The result file is the bench JSON.
		data, err := os.ReadFile(b.resultPath())
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil || doc["suggested_min_vram_mib"] != float64(7240) || doc["node"] != "ai-n3" ||
			doc["build"] != "f3586e66b-cuda" || doc["measured_vram_mib"] != float64(6581) {
			t.Errorf("result file %s (%v)", data, err)
		}
		// The key went to the router, in a header, and nowhere else.
		if len(b.chatAuth) != 1 || b.chatAuth[0] != "Bearer "+b.key {
			t.Errorf("chat Authorization headers %v", b.chatAuth)
		}
		secretFree(t, b.key, got.out, got.errOut)
	})
}
