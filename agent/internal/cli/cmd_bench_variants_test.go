package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/router/bench"
)

// More tests of `aictl bench` (see cmd_bench_test.go for the machine they run on).

// wantBenchOut is the human result of the standard run.
func wantBenchOut(b *benchRig) string {
	return "✓ " + qwenID + " on " + benchGPU + ":\n" +
		"  graphics memory 6.4 GB at ctx 32768 → suggested min_vram_mib: 7240\n" +
		"  prompt 1450 tokens/s · generation 38.4 tokens/s (41.0 with MTP, 71% accepted)\n" +
		"  Saved " + b.resultPath() + "\n" +
		"  For the catalog PR:\n" +
		"    requirements:\n" +
		"      min_vram_mib: 7240   # measured on RTX 2070 Max-Q 8 GB, build f3586e66b-cuda, 2026-11-02\n"
}

func (o mgRun) setJSON() mgRun {
	o.args = append([]string{"--json"}, o.args...)
	return o
}

func TestBenchVariants(t *testing.T) {
	t.Run("nothing loaded: nothing to unload, so no question and no restore", func(t *testing.T) {
		b := newBenchRig(t)
		b.install(t)
		got := b.run(b.benchArgs(qwenID))
		checkRun(t, got, 0, wantBenchOut(b), "… Measuring graphics memory at full context (32768 tokens)\n… Measuring speed with llama-bench\n")
		if ops := strings.Join(b.w.Ops(), ","); ops != "load "+qwenID+",unload "+qwenID {
			t.Errorf("operations: %s", ops)
		}
	})
	t.Run("--yes skips the question", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(b.benchArgs(qwenID, "--yes"))
		if got.code != 0 || got.out != wantBenchOut(b) || strings.Contains(got.errOut, "[y/N]") {
			t.Fatalf("%+v", got)
		}
		b2 := newBenchRig(t, smallID)
		b2.install(t)
		if got := b2.run(mgArgs("-y", "bench", qwenID)); got.code != 0 {
			t.Fatalf("-y: %+v", got)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(mgArgs("--lang", "es", "bench", qwenID).withTerm("s"))
		wantErr := "La prueba necesita la memoria gráfica para ella sola: se descargará Small VLM y se volverá a cargar al terminar. ¿Continuar? [s/N] " +
			"… Descargando Small VLM para que la prueba tenga la memoria para ella sola\n" +
			"… Midiendo la memoria gráfica con el contexto completo (32768 tokens)\n" +
			"… Midiendo la velocidad con llama-bench\n" +
			"… Cargando Small VLM de nuevo\n"
		wantOut := "✓ " + qwenID + " en " + benchGPU + ":\n" +
			"  memoria gráfica 6.4 GB con ctx 32768 → sugerido min_vram_mib: 7240\n" +
			"  entrada 1450 tokens/s · generación 38.4 tokens/s (41.0 con MTP, 71% aceptados)\n" +
			"  Guardado en " + b.resultPath() + "\n" +
			"  Para el PR del catálogo:\n" +
			"    requirements:\n" +
			"      min_vram_mib: 7240   # measured on RTX 2070 Max-Q 8 GB, build f3586e66b-cuda, 2026-11-02\n"
		checkRun(t, got, 0, wantOut, wantErr)
	})
	t.Run("--json is the bench JSON of the result file and stderr stays empty", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(b.benchArgs(qwenID, "--yes").setJSON())
		data, err := os.ReadFile(b.resultPath())
		if err != nil {
			t.Fatal(err)
		}
		checkRun(t, got, 0, string(data), "")
		for _, field := range []string{`"model":"` + qwenID + `"`, `"node":"ai-n3"`, `"build":"f3586e66b-cuda"`, `"ctx":32768`, `"baseline_vram_mib":210`,
			`"peak_vram_mib":6791`, `"measured_vram_mib":6581`, `"suggested_min_vram_mib":7240`, `"pp512_tps":1450.2`, `"tg128_tps":38.4`,
			`"server_tg_tps":41`, `"mtp_acceptance":0.71`, `"measured_at":"2026-11-02T10:30:00Z"`} {
			if !strings.Contains(got.out, field) {
				t.Errorf("--json misses %s: %s", field, got.out)
			}
		}
	})
	t.Run("--quiet keeps the essential lines", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(mgArgs("-q", "bench", qwenID, "--yes"))
		if got.code != 0 || strings.Contains(got.errOut, "…") || !strings.Contains(got.out, "min_vram_mib: 7240") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the llama environment of the node reaches llama-bench", func(t *testing.T) {
		b := newBenchRig(t)
		b.node = "node:\n  id: ai-n3\nllama:\n  backend: cuda\n  extra_env:\n    GGML_CUDA_NO_PINNED: \"1\"\n"
		b.dropIn = "# GENERATED\n[Service]\nEnvironment=GGML_VK_X=2\nEnvironment=\"GGML_CUDA_NO_PINNED=0\"\n"
		b.install(t)
		if got := b.run(b.benchArgs(qwenID)); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		calls := b.exec.Calls()
		if len(calls) != 1 || strings.Join(calls[0].Env, " ") != "PATH="+execx.FixedPath+" GGML_CUDA_NO_PINNED=1 GGML_VK_X=2" {
			t.Errorf("llama-bench calls %+v", calls)
		}
	})
	t.Run("a different answer to the question changes nothing", func(t *testing.T) {
		for _, answer := range []string{"", "n", "no"} {
			b := newBenchRig(t, smallID)
			b.install(t)
			got := b.run(b.benchArgs(qwenID).withTerm(answer))
			checkRun(t, got, 0, "i Nothing was changed.\n", benchQuestion)
			if len(b.w.Ops()) != 0 {
				t.Errorf("answer %q: operations %v", answer, b.w.Ops())
			}
		}
	})
	t.Run("models are loaded and nobody can be asked: LAI-CLI-007, exit 2, nothing changed", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		got := b.run(b.benchArgs(qwenID))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || len(b.w.Ops()) != 0 {
			t.Fatalf("%+v ops %v", got, b.w.Ops())
		}
		got = b.run(b.benchArgs(qwenID).setJSON())
		if got.code != ExitUsage || !strings.Contains(got.out, `"code":"LAI-CLI-007"`) || len(b.w.Ops()) != 0 {
			t.Fatalf("--json: %+v", got)
		}
	})
	t.Run("not root: exit 4 before anything is asked or touched", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		o := b.benchArgs(qwenID, "--yes")
		o.euid = 1000
		got := b.run(o)
		if got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-CLI-006]") || len(b.requestLines()) != 0 {
			t.Fatalf("%+v requests %v", got, b.requestLines())
		}
	})
	t.Run("a bad id or no id is exit 2 before any request", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.install(t)
		for _, a := range [][]string{{"bench"}, {"bench", "Qwen"}, {"bench", qwenID, smallID}} {
			if got := b.run(mgArgs(a...)); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(b.requestLines()) != 0 {
			t.Errorf("requests %v", b.requestLines())
		}
	})
	t.Run("the agent is down: exit 3", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("bench", qwenID, "--yes"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

func TestBenchFailures(t *testing.T) {
	failed := func(t *testing.T, b *benchRig, args ...string) followOut {
		t.Helper()
		b.install(t)
		return b.run(mgArgs(append(append([]string{}, args...), "bench", qwenID, "--yes")...))
	}
	restored := func(t *testing.T, b *benchRig) {
		t.Helper()
		if loaded := strings.Join(b.w.Loaded(), ","); loaded != smallID {
			t.Errorf("loaded after the failure: %q (the models of before must be back)", loaded)
		}
		if _, err := os.Stat(b.resultPath()); err == nil {
			t.Errorf("a failed run wrote a result file")
		}
	}
	t.Run("another model operation is running: exit 6, nothing changed", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.w.loading[smallID] = true
		got := failed(t, b)
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-022]") || len(b.w.Ops()) != 0 {
			t.Fatalf("%+v ops %v", got, b.w.Ops())
		}
	})
	t.Run("another operation starts before the first unload: exit 6", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.w.failBusy[smallID] = true
		got := failed(t, b)
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-022]") || got.out != "" {
			t.Fatalf("%+v", got)
		}
		if loaded := strings.Join(b.w.Loaded(), ","); loaded != smallID {
			t.Errorf("loaded %q", loaded)
		}
	})
	t.Run("unknown model: exit 5", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.agent().RespondError(http.MethodGet, "/models/"+qwenID, 404, "LAI-AGT-021", "No such model.", "Check the name.", nil)
		got := failed(t, b)
		if got.code != ExitNotFound || !strings.Contains(got.errOut, "[LAI-AGT-021]") || len(b.w.Ops()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the files are missing: exit 6, nothing changed", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.present = false
		got := failed(t, b)
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-LLM-008]") || len(b.w.Ops()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the chat request fails: LAI-LLM-025, exit 1, the models are back", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.chatErr = true
		got := failed(t, b)
		if got.code != ExitFailed || got.out != "" || !strings.Contains(got.errOut, "[LAI-LLM-025]") ||
			!strings.Contains(flat(got.errOut), "(measure: the chat request failed: the model service answered the chat request with HTTP 500)") {
			t.Fatalf("%+v", got)
		}
		restored(t, b)
		secretFree(t, b.key, got.out, got.errOut)
	})
	t.Run("--json: the envelope", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.chatErr = true
		got := failed(t, b, "--json")
		if got.code != ExitFailed || got.errOut != "" || !strings.Contains(got.out, `"code":"LAI-LLM-025"`) || !strings.Contains(got.out, `"step":"measure"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("llama-bench fails: LAI-LLM-025 at the throughput step, the models are back", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		ps, _ := bench.ParsePreset(b.preset)
		b.exec.Expect(execfake.Key(bench.DefaultBenchBinary, ps.BenchArgs()...), execx.Result{ExitCode: 1, Stderr: []byte("CUDA error\n")}, nil)
		got := failed(t, b)
		if got.code != ExitFailed || !strings.Contains(got.errOut, "[LAI-LLM-025]") || !strings.Contains(flat(got.errOut), "(throughput: llama-bench exited with code 1: CUDA error)") {
			t.Fatalf("%+v", got)
		}
		restored(t, b)
	})
	t.Run("the router address is not on this computer: the key is not sent, nothing changed", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.agent().Respond(http.MethodGet, "/key", 200, `{"key":"`+b.key+`","fingerprint":"x","base_urls":{"local":"http://192.0.2.1:8080/v1"},"models":[]}`)
		got := failed(t, b)
		if got.code != ExitFailed || !strings.Contains(got.errOut, "[LAI-LLM-025]") || len(b.w.Ops()) != 0 || len(b.chatAuth) != 0 {
			t.Fatalf("%+v", got)
		}
		secretFree(t, b.key, got.out, got.errOut)
	})
	t.Run("the models could not be loaded again: a warning with the fix, exit 0", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.w.failLoad[smallID] = true
		got := failed(t, b)
		wantErr := "… Unloading Small VLM so the test has the memory to itself\n" +
			"… Measuring graphics memory at full context (32768 tokens)\n" +
			"… Measuring speed with llama-bench\n" +
			"… Loading Small VLM again\n" +
			"! Small VLM could not be loaded again, so this computer is not as it was before\n" +
			"  the test.\n" +
			"  → Fix: Load it with `aictl load " + smallID + "`.\n"
		checkRun(t, got, 0, wantBenchOut(b), wantErr)
	})
	t.Run("another operation during the run: the numbers are kept, no value is suggested", func(t *testing.T) {
		b := newBenchRig(t, smallID)
		b.chatHook = func() {
			b.events().Publish(events.TypeModelLoading, "op_foreign", events.ModelLoading{Model: bigID, OpID: "op_foreign"})
		}
		// llama-bench runs after the event went in: wait until aictl has handed it to the engine.
		b.gate = func() {
			for {
				select {
				case ev := <-b.seen:
					if ev.OpID == "op_foreign" {
						return
					}
				case <-time.After(5 * time.Second): // the assertion below then fails, instead of a hang
					return
				}
			}
		}
		got := failed(t, b)
		wantOut := "✓ " + qwenID + " on " + benchGPU + ":\n" +
			"  graphics memory 6.4 GB at ctx 32768\n" +
			"  prompt 1450 tokens/s · generation 38.4 tokens/s (41.0 with MTP, 71% accepted)\n" +
			"  Saved " + b.resultPath() + "\n"
		if got.code != 0 || got.out != wantOut ||
			!strings.Contains(flat(got.errOut), "Another model operation ran during the test, so the numbers are not reliable and no catalog value is suggested.") ||
			!strings.Contains(flat(got.errOut), "Run `aictl bench "+qwenID+"` again when nothing else is loading models.") {
			t.Fatalf("%+v", got)
		}
		data, _ := os.ReadFile(b.resultPath())
		if !strings.Contains(string(data), `"suggested_min_vram_mib":null`) || !strings.Contains(string(data), `"disturbed":true`) {
			t.Errorf("result file %s", data)
		}
	})
}

// Ctrl-C ends the test at once and the engine puts the models back before aictl returns.
func TestBenchCtrlC(t *testing.T) {
	b := newBenchRig(t, smallID)
	b.chatBlocks = true
	b.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := b.benchArgs(qwenID, "--yes")
	o.ctx = ctx
	ch, _ := b.runAsync(o)
	select {
	case <-b.chatStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the chat request never started")
	}
	cancel()
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.out, "Stopped. The models that were loaded before were started again.") {
		t.Fatalf("%+v", got)
	}
	if loaded := strings.Join(b.w.Loaded(), ","); loaded != smallID {
		t.Errorf("loaded after Ctrl-C: %q (the engine must restore them)", loaded)
	}
	if ops := b.w.Ops(); len(ops) < 2 || ops[len(ops)-1] != "load "+smallID {
		t.Errorf("operations %v", ops)
	}
}

// --- report and helpers -------------------------------------------------------------------------------------

func TestBenchReportShapes(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	f64 := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	render := func(res *bench.Result, lang string, ascii bool) string {
		var o, e strings.Builder
		p := out.New(out.Options{Stdout: &o, Stderr: &e, Lang: Lang(lang), UTF8: !ascii})
		c := &Call{Out: p, Lang: Lang(lang)}
		c.benchReport(res, "m-9b-q4km", modelNameMap{})
		return o.String() + e.String()
	}
	t.Run("CPU only: memory and min_ram_mib, no MTP", func(t *testing.T) {
		res := &bench.Result{Model: "m-9b-q4km", Ctx: 8192, PeakRSSMiB: i64(5120), SuggestedMinRAMMiB: i64(5632), PP512TPS: 88.3, TG128TPS: 9.1,
			ServerTGTPS: f64(9.4), MeasuredAt: "2026-11-02T10:30:00Z", Build: "abc123-cpu", Path: "/var/lib/localai/bench/m.json"}
		want := "✓ m-9b-q4km on this computer (CPU only):\n" +
			"  memory 5.0 GB at ctx 8192 → suggested min_ram_mib: 5632\n" +
			"  prompt 88 tokens/s · generation 9.1 tokens/s (9.4 through the server)\n" +
			"  Saved /var/lib/localai/bench/m.json\n" +
			"  For the catalog PR:\n" +
			"    requirements:\n" +
			"      min_ram_mib: 5632   # measured on CPU only, build abc123-cpu, 2026-11-02\n"
		if got := render(res, "en", false); got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("ASCII symbols", func(t *testing.T) {
		res := &bench.Result{Model: "m", Ctx: 4096, GPU: str("GPU X"), MeasuredVRAMMiB: i64(2048), SuggestedMinVRAMMiB: i64(2253), PP512TPS: 1, TG128TPS: 2,
			MeasuredAt: "2026-11-02T10:30:00Z", Build: "b", Path: "/p.json"}
		got := render(res, "en", true)
		want := "[ok] m-9b-q4km on GPU X:\n  graphics memory 2.0 GB at ctx 4096 -> suggested min_vram_mib: 2253\n  prompt 1 tokens/s - generation 2.0 tokens/s\n"
		if !strings.HasPrefix(got, want) {
			t.Errorf("got:\n%s\nwant prefix:\n%s", got, want)
		}
	})
}

func TestBenchHelpers(t *testing.T) {
	t.Run("parseDropIn reads both forms and the escapes", func(t *testing.T) {
		data := "# GENERATED\n[Service]\nEnvironment=GGML_A=1\nEnvironment=\"GGML_B=two words\"\nEnvironment=GGML_C=100%%\n" +
			"Environment=\"GGML_D=say \\\"hi\\\" \\\\ ok\"\nExecStart=/bin/true\nEnvironment=NOEQUALS\n"
		got := parseDropIn([]byte(data))
		want := map[string]string{"GGML_A": "1", "GGML_B": "two words", "GGML_C": "100%", "GGML_D": `say "hi" \ ok`}
		if len(got) != len(want) {
			t.Fatalf("got %v", got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
	})
	t.Run("only an address on this computer gets the key", func(t *testing.T) {
		for u, want := range map[string]bool{
			"http://127.0.0.1:8080/v1": true, "http://localhost:8080/v1": true, "http://[::1]:8080/v1": true,
			"http://192.0.2.1:8080/v1": false, "https://example.test/v1": false, "ftp://127.0.0.1/": false, "not a url": false, "": false,
		} {
			if got := isLoopbackURL(u); got != want {
				t.Errorf("isLoopbackURL(%q) = %v, want %v", u, got, want)
			}
		}
	})
	t.Run("an unreadable node.yaml and drop-in only mean less detail", func(t *testing.T) {
		n := readBenchNode(benchHost{NodeYAML: filepath.Join(t.TempDir(), "nope"), DropIn: filepath.Join(t.TempDir(), "nope")})
		if n.ID != "" || n.Backend != "" || n.Env != nil {
			t.Errorf("%+v", n)
		}
	})
	t.Run("bench errors map to the documented exits", func(t *testing.T) {
		c := &Call{Lang: LangEN, Out: out.New(out.Options{Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})}
		busy := &bench.BusyError{OpID: "op_x"}
		cases := []struct {
			name string
			err  error
			exit int
			code string
		}{
			{"busy", busy, ExitRefused, "LAI-AGT-022"},
			{"busy in a step", &bench.Error{Code: bench.CodeBusy, Step: bench.StepUnload, Err: busy}, ExitRefused, "LAI-AGT-022"},
			{"unknown", &bench.Error{Code: bench.CodeUnknownModel, Step: bench.StepPrecheck, Err: bench.ErrUnknownModel}, ExitNotFound, "LAI-AGT-021"},
			{"files missing", &bench.Error{Code: bench.CodeFilesMissing, Err: errors.New("x")}, ExitRefused, "LAI-LLM-008"},
			{"failed", &bench.Error{Code: bench.CodeBenchFailed, Step: bench.StepLoad, Err: errors.New("it broke.")}, ExitFailed, "LAI-LLM-025"},
		}
		for _, tc := range cases {
			e := c.benchError(tc.err, "m-9b-q4km")
			var ce *CLIError
			if !errors.As(e, &ce) || ce.Code != tc.code || ExitFor(e) != tc.exit {
				t.Errorf("%s: %#v", tc.name, e)
			}
		}
		if e := c.benchError(&bench.Error{Err: context.DeadlineExceeded}, "m"); ExitFor(e) != ExitTimeout {
			t.Errorf("deadline: %v", e)
		}
	})
}
