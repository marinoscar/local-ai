package bench

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// cpuProcs is a Procs over the fake world: the router (pid 100) is always there, the model's child
// (pid 200) exists while the model is loaded, and its resident set grows during a request.
type cpuProcs struct{ w *world }

func (p cpuProcs) PIDs(context.Context) ([]int, error) {
	p.w.mu.Lock()
	defer p.w.mu.Unlock()
	if p.w.isLoaded(testModel) {
		return []int{100, 200}, nil
	}
	return []int{100}, nil
}

func (p cpuProcs) VmRSSKiB(pid int) (int64, error) {
	p.w.mu.Lock()
	defer p.w.mu.Unlock()
	if pid == 100 {
		return 50_000, nil
	}
	if p.w.chatting {
		p.w.samples++
		p.w.cond.Broadcast()
		return 4_400_000, nil
	}
	return 4_000_000, nil
}

func cpuRig(t *testing.T) (*rig, *Bench) {
	t.Helper()
	r := newRig(t)
	r.gpu = &fakeSampler{w: r.w} // no GPUs: a CPU-only node
	b, err := New(Deps{Agent: r.agent, Chat: r.chat, GPU: r.gpu, Procs: cpuProcs{r.w}, Exec: r.exec}, r.opts)
	if err != nil {
		t.Fatal(err)
	}
	return r, b
}

func TestRunCPUOnlyNode(t *testing.T) {
	r, b := cpuRig(t)
	res, err := b.Run(context.Background(), testModel)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.GPU != nil || res.BaselineVRAMMiB != nil || res.PeakVRAMMiB != nil || res.MeasuredVRAMMiB != nil || res.SuggestedMinVRAMMiB != nil {
		t.Errorf("a CPU node has no VRAM fields: %+v", res)
	}
	// Peak VmRSS of the child only (the router's own 50 MB is not the model's): 4 400 000 KiB is
	// 4296.9 MiB, 4297 rounded up; ceil(4297 x 1.10) = 4727.
	if res.PeakRSSMiB == nil || *res.PeakRSSMiB != 4297 || res.SuggestedMinRAMMiB == nil || *res.SuggestedMinRAMMiB != 4727 {
		t.Errorf("rss = %v, suggested = %v", deref64(res.PeakRSSMiB), deref64(res.SuggestedMinRAMMiB))
	}
	if key, v, ok := res.SuggestedMiB(); !ok || key != "min_ram_mib" || v != 4727 {
		t.Errorf("SuggestedMiB = %q %d %v", key, v, ok)
	}
	want := "requirements:\n  min_ram_mib: 4727   # measured on CPU only, build f3586e66b-cuda, 2026-11-02\n"
	if got := res.Snippet(); got != want {
		t.Errorf("Snippet:\n%s\nwant:\n%s", got, want)
	}
	raw, _ := os.ReadFile(res.Path)
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gpu", "baseline_vram_mib", "peak_vram_mib", "measured_vram_mib", "suggested_min_vram_mib"} {
		v, ok := f[k]
		if !ok || v != nil {
			t.Errorf("%s = %v (present %v), want an explicit null on a CPU node: %s", k, v, ok, raw)
		}
	}
	if len(r.w.Loaded()) != 0 {
		t.Errorf("loaded = %v", r.w.Loaded())
	}
}

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestRunCPUOnlyNodeNeedsAProcessReader(t *testing.T) {
	r := newRig(t)
	r.gpu = &fakeSampler{w: r.w}
	_, err := r.run() // Deps.Procs is nil
	if e := asBench(t, err); e.Step != StepBaseline || !strings.Contains(e.Error(), "process reader") {
		t.Errorf("error = %v", err)
	}
}

func TestRunCPUOnlyDisturbedHasNoSuggestion(t *testing.T) {
	r, b := cpuRig(t)
	r.chat.before = func() { r.agent.emit("model.unloaded", "") }
	res, err := b.Run(context.Background(), testModel)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Disturbed || res.SuggestedMinRAMMiB != nil || res.Snippet() != "" {
		t.Errorf("disturbed = %v, suggested = %v", res.Disturbed, res.SuggestedMinRAMMiB)
	}
	if res.PeakRSSMiB == nil || *res.PeakRSSMiB != 4297 {
		t.Errorf("the measurement stays: %v", res.PeakRSSMiB)
	}
}

func TestParsePreset(t *testing.T) {
	good := "; GENERATED\n[m-9b]\nmodel = /srv/localai/models/m-9b/m-Q4_K_M.gguf\nc = 32768\nn-gpu-layers = all\nflash-attn = auto\nparallel = 1\n# a note\n"
	cases := []struct {
		name    string
		text    string
		want    Preset
		wantErr string
	}{
		{"complete section", good, Preset{Weights: "/srv/localai/models/m-9b/m-Q4_K_M.gguf", Ctx: 32768, NGL: "all", FlashAttn: "auto"}, ""},
		{"section header and spaces are optional", "model=/a/b.gguf\nc=4096", Preset{Weights: "/a/b.gguf", Ctx: 4096}, ""},
		{"ctx-size is the same key", "model = /a/b.gguf\nctx-size = 8192\n", Preset{Weights: "/a/b.gguf", Ctx: 8192}, ""},
		{"a later value wins", "model = /a/b.gguf\nc = 1\nc = 2\n", Preset{Weights: "/a/b.gguf", Ctx: 2}, ""},
		{"no model", "c = 4096\n", Preset{}, "no model file"},
		{"relative model", "model = b.gguf\nc = 4096\n", Preset{}, "not an absolute path"},
		{"no context", "model = /a/b.gguf\n", Preset{}, "no usable context"},
		{"zero context", "model = /a/b.gguf\nc = 0\n", Preset{}, "no usable context"},
		{"text context", "model = /a/b.gguf\nc = big\n", Preset{}, "no usable context"},
		{"empty", "", Preset{}, "no model file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePreset(c.text)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

func TestBenchArgs(t *testing.T) {
	tail := "-p 512 -n 128 -r 3 -o json"
	cases := []struct {
		name string
		p    Preset
		want string
	}{
		{"numeric layers and flash attention on", Preset{Weights: "/w.gguf", NGL: "99", FlashAttn: "on"}, "-m /w.gguf -ngl 99 -fa 1 " + tail},
		{"all layers become 99", Preset{Weights: "/w.gguf", NGL: "all", FlashAttn: "off"}, "-m /w.gguf -ngl 99 -fa 0 " + tail},
		{"auto leaves llama-bench's default", Preset{Weights: "/w.gguf", NGL: "20", FlashAttn: "auto"}, "-m /w.gguf -ngl 20 " + tail},
		{"nothing said", Preset{Weights: "/w.gguf"}, "-m /w.gguf " + tail},
		{"case is ignored", Preset{Weights: "/w.gguf", NGL: "ALL", FlashAttn: "ON"}, "-m /w.gguf -ngl 99 -fa 1 " + tail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(c.p.BenchArgs(), " "); got != c.want {
				t.Errorf("args = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseBenchJSON(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		pp, tg  float64
		wantErr string
	}{
		{"both tests", benchJSONOK, 1450.21, 38.41, ""},
		{"banner and log lines around the array", "ggml_cuda_init: found 1 CUDA devices\n" + benchJSONOK + "\n", 1450.21, 38.41, ""},
		{"extra fields and rows are ignored", `[{"n_prompt":512,"n_gen":0,"avg_ts":10.5,"model_type":"x"},{"n_prompt":0,"n_gen":128,"avg_ts":2.5},{"n_prompt":512,"n_gen":128,"avg_ts":7}]`, 10.5, 2.5, ""},
		{"no array", "error: failed to load model", 0, 0, "no JSON array"},
		{"invalid JSON", "[{oops}]", 0, 0, "not valid JSON"},
		{"generation test missing", `[{"n_prompt":512,"n_gen":0,"avg_ts":10}]`, 0, 0, "missing"},
		{"prompt test missing", `[{"n_prompt":0,"n_gen":128,"avg_ts":10}]`, 0, 0, "missing"},
		{"zero speed", `[{"n_prompt":512,"n_gen":0,"avg_ts":0},{"n_prompt":0,"n_gen":128,"avg_ts":3}]`, 0, 0, "missing"},
		{"empty array", `[]`, 0, 0, "missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pp, tg, err := ParseBenchJSON([]byte(c.out))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || pp != c.pp || tg != c.tg {
				t.Errorf("got %v %v %v; want %v %v", pp, tg, err, c.pp, c.tg)
			}
		})
	}
}

func TestShortGPUName(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		want  string
	}{
		{"NVIDIA GeForce RTX 2070 with Max-Q Design", 7982, "RTX 2070 Max-Q 8 GB"},
		{"NVIDIA GeForce Example GPU", 12288, "Example GPU 12 GB"},
		{"AMD Radeon Example APU", 62464, "AMD Radeon Example APU 61 GB"},
		{"  odd   spacing\nand a newline ", 1024, "odd spacing and a newline 1 GB"},
		{"Some GPU", 0, "Some GPU"},
	}
	for _, c := range cases {
		if got := ShortGPUName(c.name, c.total); got != c.want {
			t.Errorf("ShortGPUName(%q, %d) = %q, want %q", c.name, c.total, got, c.want)
		}
	}
}

func TestSnippetWithoutASuggestion(t *testing.T) {
	if got := (&Result{}).Snippet(); got != "" {
		t.Errorf("Snippet = %q", got)
	}
}

func TestProcFS(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/fs/cgroup/system.slice/localai-llama.service/cgroup.procs": {Data: []byte("100\n200\nnot-a-pid\n")},
		"proc/100/status": {Data: []byte("Name:\tllama-server\nVmPeak:\t  900 kB\nVmRSS:\t   51200 kB\nThreads:\t4\n")},
		"proc/200/status": {Data: []byte("Name:\tkthread\n")},
	}
	p := NewProcFS(fsys, "")
	pids, err := p.PIDs(context.Background())
	if err != nil || len(pids) != 2 || pids[0] != 100 || pids[1] != 200 {
		t.Fatalf("PIDs = %v, %v", pids, err)
	}
	if v, err := p.VmRSSKiB(100); err != nil || v != 51200 {
		t.Errorf("VmRSSKiB(100) = %d, %v", v, err)
	}
	if _, err := p.VmRSSKiB(200); err == nil {
		t.Error("a status without VmRSS must be an error")
	}
	if _, err := p.VmRSSKiB(300); err == nil {
		t.Error("an exited process must be an error")
	}
	if _, err := NewProcFS(fstest.MapFS{}, "").PIDs(context.Background()); err == nil {
		t.Error("a missing cgroup must be an error")
	}
}

func TestRSSTrackerCountsOnlyNewProcesses(t *testing.T) {
	w := newWorld()
	p := cpuProcs{w}
	tr, err := newRSSTracker(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.sample(context.Background()); err == nil {
		t.Error("with no new process there is nothing to measure")
	}
	w.loaded = []string{testModel}
	if v, err := tr.sample(context.Background()); err != nil || v != 3907 { // 4 000 000 KiB = 3906.25 MiB
		t.Errorf("sample = %d, %v; want 3907 (the router's own pid is excluded)", v, err)
	}
}

func TestDetectBackend(t *testing.T) {
	nv := hardware.GPU{Vendor: hardware.VendorNVIDIA}
	amd := hardware.GPU{Vendor: hardware.VendorAMD}
	for name, c := range map[string]struct {
		gpus []hardware.GPU
		want string
	}{
		"nvidia": {[]hardware.GPU{nv}, hardware.BackendCUDA},
		"amd":    {[]hardware.GPU{amd}, hardware.BackendVulkan},
		"hybrid": {[]hardware.GPU{amd, nv}, hardware.BackendCUDA},
		"none":   {nil, hardware.BackendCPU},
	} {
		if got := detectBackend(c.gpus); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}
