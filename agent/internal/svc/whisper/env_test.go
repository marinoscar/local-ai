package whisper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

type fakeCopies map[string]ark.Copy

func (f fakeCopies) BestCopy(id string) (ark.Copy, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return ark.Copy{}, ark.ErrNotPresent
}

func wmodel(id, file string, minVRAM int64) catalog.Model {
	return catalog.Model{ID: id, Engine: "whisper", Files: []catalog.ModelFile{{Kind: "whisper", Name: file}},
		Requirements: catalog.Requirements{MinVRAMMiB: minVRAM}}
}

func fixture() (*catalog.Catalog, *catalog.Profile) {
	llm := catalog.Model{ID: "llm", Engine: "llama", Requirements: catalog.Requirements{MinVRAMMiB: 6400}}
	cat := catalog.New("t", []catalog.Model{llm,
		wmodel("whisper-small", "ggml-small.bin", 1100), wmodel("whisper-medium", "ggml-medium.bin", 2600)}, nil, nil)
	prof := &catalog.Profile{
		Include:  catalog.Include{Models: []string{"llm", "whisper-small", "whisper-medium"}},
		Survival: catalog.Survival{Models: []string{"llm"}},
	}
	return cat, prof
}

func copyOf(id, file string) ark.Copy {
	return ark.Copy{Files: []string{"/srv/localai/speech/whisper/" + id + "/" + file}}
}

func TestResolve(t *testing.T) {
	cat, prof := fixture()
	small := fakeCopies{"whisper-small": copyOf("whisper-small", "ggml-small.bin")}
	both := fakeCopies{"whisper-small": copyOf("whisper-small", "ggml-small.bin"), "whisper-medium": copyOf("whisper-medium", "ggml-medium.bin")}
	hw := func(total, free int64) Hardware {
		return Hardware{PhysicalCores: 8, TotalVRAMMiB: total, FreeVRAMMiB: free}
	}

	tests := []struct {
		name      string
		mut       func(*Inputs)
		wantID    string
		wantDev   string
		wantCodes []string
		wantThr   int
		wantRes   int64
	}{
		{"8GB survival 6.4GB -> cpu with 013", func(in *Inputs) { in.Hardware = hw(8192, -1) }, "whisper-small", "cpu", []string{doctor.SVC013}, 4, 0},
		{"12GB -> gpu", func(in *Inputs) { in.Hardware = hw(12288, -1) }, "whisper-small", "gpu", nil, 4, 1100},
		{"128GB unified -> gpu", func(in *Inputs) { in.Hardware = hw(131072, -1) }, "whisper-small", "gpu", nil, 4, 1100},
		{"backend cpu -> cpu, no code", func(in *Inputs) { in.LlamaBackend = "cpu"; in.Hardware = hw(131072, -1) }, "whisper-small", "cpu", nil, 4, 0},
		{"whisper.backend cpu beats llama cuda", func(in *Inputs) { in.Config.Backend = "cpu"; in.Hardware = hw(131072, -1) }, "whisper-small", "cpu", nil, 4, 0},
		{"GPU full (free below need) -> cpu", func(in *Inputs) { in.Hardware = hw(24576, 500) }, "whisper-small", "cpu", []string{doctor.SVC013}, 4, 0},
		{"build is cpu -> cpu", func(in *Inputs) { in.BuildBackend = "cpu"; in.Hardware = hw(24576, -1) }, "whisper-small", "cpu", []string{doctor.SVC013}, 4, 0},
		{"no build, rule by hardware", func(in *Inputs) { in.BuildBackend = ""; in.Hardware = hw(24576, -1) }, "whisper-small", "gpu", nil, 4, 1100},
		{"explicit gpu honoured on 8GB", func(in *Inputs) { in.Config.Device = "gpu"; in.Hardware = hw(8192, -1) }, "whisper-small", "gpu", nil, 4, 1100},
		{"explicit cpu, no code", func(in *Inputs) { in.Config.Device = "cpu"; in.Hardware = hw(24576, -1) }, "whisper-small", "cpu", nil, 4, 0},
		{"fallback forces cpu even on explicit gpu", func(in *Inputs) { in.ForceCPU = true; in.Config.Device = "gpu"; in.Hardware = hw(24576, -1) }, "whisper-small", "cpu", []string{doctor.SVC013}, 4, 0},
		{"explicit model", func(in *Inputs) { in.Copies = both; in.Config.Model = "whisper-medium"; in.Hardware = hw(24576, -1) }, "whisper-medium", "gpu", nil, 4, 2600},
		{"auto takes the first present", func(in *Inputs) { in.Copies = both; in.Hardware = hw(24576, -1) }, "whisper-small", "gpu", nil, 4, 1100},
		{"auto skips an absent first model", func(in *Inputs) {
			in.Copies = fakeCopies{"whisper-medium": copyOf("whisper-medium", "ggml-medium.bin")}
			in.Hardware = hw(24576, -1)
		}, "whisper-medium", "gpu", nil, 4, 2600},
		{"explicit model not installed", func(in *Inputs) { in.Config.Model = "whisper-medium" }, "", "cpu", []string{doctor.SVC010}, 4, 0},
		{"explicit unknown model", func(in *Inputs) { in.Config.Model = "nope" }, "", "cpu", []string{doctor.SVC010}, 4, 0},
		{"explicit non-whisper model", func(in *Inputs) { in.Config.Model = "llm" }, "", "cpu", []string{doctor.SVC010}, 4, 0},
		{"no model installed", func(in *Inputs) { in.Copies = fakeCopies{} }, "", "cpu", []string{doctor.SVC010}, 4, 0},
		{"copy lacks the catalog file", func(in *Inputs) { in.Copies = fakeCopies{"whisper-small": copyOf("whisper-small", "other.bin")} }, "", "cpu", []string{doctor.SVC010}, 4, 0},
		{"threads explicit", func(in *Inputs) { in.Config.Threads = "3"; in.Hardware = hw(8192, -1) }, "whisper-small", "cpu", []string{doctor.SVC013}, 3, 0},
		{"threads capped at 8", func(in *Inputs) { in.Hardware = Hardware{PhysicalCores: 64, TotalVRAMMiB: 8192, FreeVRAMMiB: -1} }, "whisper-small", "cpu", []string{doctor.SVC013}, 8, 0},
		{"threads at least 1", func(in *Inputs) { in.Hardware = Hardware{PhysicalCores: 1, TotalVRAMMiB: 8192, FreeVRAMMiB: -1} }, "whisper-small", "cpu", []string{doctor.SVC013}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Inputs{Catalog: cat, Profile: prof, Copies: small, LlamaBackend: "cuda", Hardware: hw(8192, -1)}
			in.Config.Threads = "auto"
			tc.mut(&in)
			sel, probs := Resolve(in)
			if sel.ModelID != tc.wantID || sel.Device != tc.wantDev || sel.Threads != tc.wantThr || sel.ReserveMiB != tc.wantRes {
				t.Fatalf("got %+v, want id=%q dev=%q threads=%d reserve=%d", sel, tc.wantID, tc.wantDev, tc.wantThr, tc.wantRes)
			}
			var codes []string
			for _, p := range probs {
				codes = append(codes, p.Code)
			}
			if len(codes) != len(tc.wantCodes) || (len(codes) == 1 && codes[0] != tc.wantCodes[0]) {
				t.Fatalf("codes = %v, want %v", codes, tc.wantCodes)
			}
		})
	}
}

func TestRenderEnv(t *testing.T) {
	got := string(RenderEnv(Selection{ModelID: "whisper-small", ModelPath: "/srv/localai/speech/whisper/whisper-small/ggml-small.bin", Threads: 4, Device: "cpu"}))
	want := "# Generated by aiagent. Do not edit. Regenerated on model/drive changes.\n" +
		"WHISPER_MODEL=/srv/localai/speech/whisper/whisper-small/ggml-small.bin\nWHISPER_THREADS=4\nWHISPER_DEVICE_ARGS=--no-gpu\nWHISPER_MODEL_ID=whisper-small\n"
	if got != want {
		t.Fatalf("env =\n%s\nwant\n%s", got, want)
	}
	if g := string(RenderEnv(Selection{ModelID: "a", ModelPath: "/p", Threads: 2, Device: "gpu"})); !contains(g, "WHISPER_DEVICE_ARGS=\n") {
		t.Fatalf("gpu env must have empty device args:\n%s", g)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type fakeUnits struct {
	restarts, stops int
	restartErr      error
}

func (f *fakeUnits) Restart(context.Context, string) error { f.restarts++; return f.restartErr }
func (f *fakeUnits) Stop(context.Context, string) error    { f.stops++; return nil }

func TestEnvWriterApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whisper.env")
	u := &fakeUnits{}
	w := &EnvWriter{Path: path, Units: u, UID: -1, GID: -1}
	sel := Selection{ModelID: "whisper-small", ModelPath: "/m/ggml-small.bin", Threads: 4, Device: "cpu"}
	ctx := context.Background()

	res, err := w.Apply(ctx, sel)
	if err != nil || !res.Written || !res.Restarted || u.restarts != 1 {
		t.Fatalf("first apply: %+v err=%v restarts=%d", res, err, u.restarts)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v err=%v, want 0640", info.Mode(), err)
	}
	// Unchanged: no write, no restart.
	res, err = w.Apply(ctx, sel)
	if err != nil || res.Written || res.Restarted || u.restarts != 1 {
		t.Fatalf("unchanged apply: %+v err=%v restarts=%d", res, err, u.restarts)
	}
	// Changed: exactly one more restart.
	sel.Device = "gpu"
	res, err = w.Apply(ctx, sel)
	if err != nil || !res.Written || u.restarts != 2 {
		t.Fatalf("changed apply: %+v err=%v restarts=%d", res, err, u.restarts)
	}
	// No model: the unit is stopped and the file is not touched.
	before, _ := os.ReadFile(path)
	res, err = w.Apply(ctx, Selection{})
	if err != nil || !res.Stopped || u.stops != 1 || u.restarts != 2 {
		t.Fatalf("no model: %+v err=%v stops=%d", res, err, u.stops)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("no-model apply must leave the file alone")
	}
	// A failed restart is reported, the file stays written.
	u.restartErr = errors.New("boom")
	sel.Threads = 2
	res, err = w.Apply(ctx, sel)
	if err == nil || !res.Written || res.Restarted {
		t.Fatalf("failed restart: %+v err=%v", res, err)
	}
}

func TestEnvWriterNeedsUnits(t *testing.T) {
	if _, err := (&EnvWriter{Path: filepath.Join(t.TempDir(), "x")}).Apply(context.Background(), Selection{}); err == nil {
		t.Fatal("want an error without Units")
	}
}

func TestEnvWriterWriteFailure(t *testing.T) {
	u := &fakeUnits{}
	w := &EnvWriter{Path: filepath.Join(t.TempDir(), "missing-dir", "whisper.env"), Units: u, UID: -1, GID: -1}
	_, err := w.Apply(context.Background(), Selection{ModelID: "a", ModelPath: "/p", Threads: 1})
	if err == nil || u.restarts != 0 {
		t.Fatalf("err=%v restarts=%d, want a write error and no restart", err, u.restarts)
	}
}

func TestFallback(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	failed := services.UnitState{ActiveState: services.ActiveFailed}
	restarting := services.UnitState{ActiveState: services.ActiveActivating, SubState: "auto-restart"}
	up := func(since time.Time) services.UnitState {
		return services.UnitState{ActiveState: services.ActiveActive, SubState: "running", ActiveEnterTimestamp: since}
	}

	t.Run("two failed gpu starts switch once", func(t *testing.T) {
		var f Fallback
		if f.Observe(restarting, DeviceGPU, now) {
			t.Fatal("one failure must not switch")
		}
		if f.Observe(restarting, DeviceGPU, now) {
			t.Fatal("the same failing state is one failure, not two")
		}
		f.Observe(up(now), DeviceGPU, now)
		if !f.Observe(failed, DeviceGPU, now) {
			t.Fatal("second failure must switch")
		}
		if !f.ForceCPU() {
			t.Fatal("ForceCPU must be set")
		}
		if f.Observe(failed, DeviceGPU, now) {
			t.Fatal("must switch only once")
		}
	})
	t.Run("cpu failures never count", func(t *testing.T) {
		var f Fallback
		for i := 0; i < 5; i++ {
			f.Observe(failed, DeviceCPU, now)
			f.Observe(up(now), DeviceCPU, now)
		}
		if f.ForceCPU() || f.Failures() != 0 {
			t.Fatalf("forced=%v failures=%d", f.ForceCPU(), f.Failures())
		}
	})
	t.Run("a stable run clears the count", func(t *testing.T) {
		var f Fallback
		f.Observe(failed, DeviceGPU, now)
		f.Observe(up(now.Add(-time.Minute)), DeviceGPU, now)
		if f.Failures() != 0 {
			t.Fatalf("failures = %d, want 0", f.Failures())
		}
		if f.Observe(failed, DeviceGPU, now) {
			t.Fatal("a failure after a stable run is the first again")
		}
	})
}
