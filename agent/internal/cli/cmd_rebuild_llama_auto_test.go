package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// Tests of the fallback chain of `aictl rebuild-llama` with no backend flags (issue #433, aictl.md
// section 4.23): the ready-made build, the node's backend, Vulkan, the processor; one retry with fewer
// jobs when the compiler runs out of memory; explicit flags keep the single build.

const oomTail = "FAILED: ggml/src/ggml-cuda/CMakeFiles/ggml-cuda.dir/fattn.cu.o\nnvcc error   : 'cicc' died due to signal 9 (Kill signal)\nninja: build stopped: subcommand failed."

// chainEngine scripts the fake engine per backend: resolve answers the inputs of the backend asked
// for ("" is the node's, cuda), and fails holds what each build of that backend returns, in order (a
// missing entry succeeds).
func chainEngine(f *llamaFix, resolveErr map[string]error, fails map[string][]error) {
	calls := map[string]int{}
	f.eng.resolveFn = func(req build.Request) (build.Inputs, error) {
		b := req.Backend
		if b == "" {
			b = "cuda"
		}
		if err := resolveErr[b]; err != nil {
			return build.Inputs{}, err
		}
		in := build.Inputs{Commit: lCommit, ShortCommit: lShort, Backend: b, Jobs: 5, Activate: true}
		if req.Jobs > 0 {
			in.Jobs = req.Jobs
		}
		if b == "cuda" {
			in.CudaArch = "75"
		}
		return in, nil
	}
	f.eng.buildFn = func(req build.Request) (build.Result, error) {
		b := req.Backend
		if b == "" {
			b = "cuda"
		}
		n := calls[b]
		calls[b]++
		if n < len(fails[b]) && fails[b][n] != nil {
			return build.Result{}, fails[b][n]
		}
		res := f.eng.res
		res.BuildID = lShort + "-" + b
		return res, nil
	}
}

func backendsAsked(f *llamaFix) []string {
	var out []string
	for _, r := range f.eng.reqs {
		b := r.Backend
		if b == "" {
			b = "(node)"
		}
		out = append(out, b)
	}
	return out
}

var (
	errCompile = llamaBuildErr(doctor.LLM018, build.StageBuild, "Building llama.cpp failed: cmake exited with status 1.", "cause", "exit", "exit_code", 1, "log_tail", "error: something")
	errOOM     = llamaBuildErr(doctor.LLM018, build.StageBuild, "Building llama.cpp failed: cmake exited with status 1.", "cause", "exit", "exit_code", 1, "log_tail", oomTail)
	errVulkan  = llamaBuildErr(doctor.KIT012, build.StagePreflight, "No real Vulkan graphics device was found.")
	errNoSrc   = llamaBuildErr(doctor.KIT002, build.StageSource, "The kit doesn't have llama.cpp "+lShort+".")
	errUnused  = llamaBuildErr(doctor.LLM019, build.StageCheck, "The new llama.cpp build did not work; the previous build is still in use.", "rolled_back", true)
	errStuck   = llamaBuildErr(doctor.LLM019, build.StageCheck, "The new build did not work and the previous one could not be put back.", "rolled_back", false)
	errNoArch  = llamaBuildErr(doctor.HW019, build.StageResolve, "The CUDA architecture of the GPU could not be detected.")
	errNvccOff = llamaBuildErr(doctor.LLM018, build.StagePreflight, "Building llama.cpp needs nvcc.", "cause", "missing_tools", "missing", []string{"nvcc"})
	errNoCMake = llamaBuildErr(doctor.LLM018, build.StagePreflight, "Building llama.cpp needs cmake.", "cause", "missing_tools", "missing", []string{"cmake"})
)

func TestRebuildLlamaFallbackChain(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		resolveErr map[string]error
		fails      map[string][]error
		wantExit   int
		wantAsked  []string
		wantJobs   []int // the Jobs of each request, when checked
		wantOut    []string
		wantNotOut []string
	}{
		{
			name: "cuda fails, Vulkan fails, the processor works", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errCompile}, "vulkan": {errVulkan}},
			wantExit:  0,
			wantAsked: []string{"(node)", "vulkan", "cpu"},
			wantOut: []string{
				"… That did not work (LAI-LLM-018). Trying building for Vulkan (works with most graphics chips) instead.",
				"… That did not work (LAI-KIT-012). Trying building for the processor only (cpu: slower answers, but it always works) instead.",
				"… Building llama.cpp " + lShort + " for vulkan, 5 parallel jobs",
				"… Building llama.cpp " + lShort + " for cpu, 5 parallel jobs",
				"! Building llama.cpp failed: cmake exited with status 1.",
				"✓ Build " + lShort + "-cpu passed its test",
			},
		},
		{
			name: "everything fails: the last error is the answer", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errCompile}, "vulkan": {errVulkan}, "cpu": {errCompile}},
			wantExit:  ExitFailed,
			wantAsked: []string{"(node)", "vulkan", "cpu"},
			wantOut:   []string{"[LAI-LLM-018]"},
		},
		{
			name: "a build that did not serve and was rolled back tries the next", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errUnused}},
			wantAsked: []string{"(node)", "vulkan"},
			wantOut:   []string{"That did not work (LAI-LLM-019)."},
		},
		{
			name: "out of memory: once more with half the jobs, and it says so", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errOOM}},
			wantAsked: []string{"(node)", "(node)"},
			wantJobs:  []int{0, 2},
			wantOut:   []string{"! The compiler ran out of memory with 5 parallel jobs. Trying again with 2: slower, but it needs less memory."},
		},
		{
			name: "out of memory twice: the retry is not repeated, the next backend is tried", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errOOM, errOOM}},
			wantAsked: []string{"(node)", "(node)", "vulkan"},
			wantJobs:  []int{0, 2, 0},
		},
		{
			name: "the graphics chip cannot be read: Vulkan", args: []string{"--yes"},
			resolveErr: map[string]error{"cuda": errNoArch},
			wantAsked:  []string{"vulkan"},
			wantOut:    []string{"That did not work (LAI-HW-019)."},
		},
		{
			name: "only the CUDA compiler is missing: the next backend", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errNvccOff}},
			wantAsked: []string{"(node)", "vulkan"},
		},
		{
			name: "a missing generic tool stops: no backend builds without it", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errNoCMake}},
			wantExit:  ExitFailed,
			wantAsked: []string{"(node)"},
		},
		{
			name: "a source the kit lacks stops: another backend needs the same source", args: []string{"--yes"},
			fails:      map[string][]error{"cuda": {errNoSrc}},
			wantExit:   ExitFailed,
			wantAsked:  []string{"(node)"},
			wantOut:    []string{"[LAI-KIT-002]"},
			wantNotOut: []string{"Trying"},
		},
		{
			name: "a rollback that failed stops", args: []string{"--yes"},
			fails:     map[string][]error{"cuda": {errStuck}},
			wantExit:  ExitFailed,
			wantAsked: []string{"(node)"},
		},
		{
			name: "explicit --backend keeps the single build", args: []string{"--yes", "--backend", "cuda"},
			fails:      map[string][]error{"cuda": {errOOM}},
			wantExit:   ExitFailed,
			wantAsked:  []string{"cuda"},
			wantNotOut: []string{"It tries, in order", "ran out of memory", "Trying"},
		},
		{
			name: "explicit --jobs keeps the single build", args: []string{"--yes", "--jobs", "4"},
			fails:     map[string][]error{"cuda": {errOOM}},
			wantExit:  ExitFailed,
			wantAsked: []string{"(node)"},
			wantJobs:  []int{4},
		},
		{
			name: "explicit --cuda-arch keeps the single build", args: []string{"--yes", "--cuda-arch", "86"},
			fails:     map[string][]error{"cuda": {errCompile}},
			wantExit:  ExitFailed,
			wantAsked: []string{"(node)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			chainEngine(f, tc.resolveErr, tc.fails)
			r := f.run(t, append([]string{"rebuild-llama"}, tc.args...)...)
			all := unwrap(r.out + r.errOut)
			if r.code != tc.wantExit {
				t.Fatalf("exit %d, want %d\n%s", r.code, tc.wantExit, all)
			}
			if got := backendsAsked(f); strings.Join(got, ",") != strings.Join(tc.wantAsked, ",") {
				t.Errorf("builds asked %v, want %v\n%s", got, tc.wantAsked, all)
			}
			for i, j := range tc.wantJobs {
				if i < len(f.eng.reqs) && f.eng.reqs[i].Jobs != j {
					t.Errorf("request %d asked %d jobs, want %d", i, f.eng.reqs[i].Jobs, j)
				}
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(all, w) {
					t.Errorf("output lacks %q:\n%s", w, all)
				}
			}
			for _, w := range tc.wantNotOut {
				if strings.Contains(all, w) {
					t.Errorf("output has %q:\n%s", w, all)
				}
			}
		})
	}
}

func TestRebuildLlamaFallbackAsksAtTheTerminal(t *testing.T) {
	t.Run("Enter is yes, n stops with the last error", func(t *testing.T) {
		f := newLlamaFix(t)
		chainEngine(f, nil, map[string][]error{"cuda": {errCompile}, "vulkan": {errVulkan}})
		// the build's confirmation, Enter for Vulkan, then no for the processor
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y", "", "n")), "rebuild-llama")
		if r.code != ExitFailed || strings.Join(backendsAsked(f), ",") != "(node),vulkan" {
			t.Fatalf("exit %d asked %v\n%s\n%s", r.code, backendsAsked(f), r.out, r.errOut)
		}
		errOut := unwrap(r.errOut)
		for _, w := range []string{
			"That did not work (LAI-LLM-018). Try building for Vulkan (works with most graphics chips) instead? [Y/n]",
			"That did not work (LAI-KIT-012). Try building for the processor only (cpu: slower answers, but it always works) instead? [Y/n]",
			"[LAI-KIT-012]",
		} {
			if !strings.Contains(errOut, w) {
				t.Errorf("stderr lacks %q:\n%s", w, errOut)
			}
		}
		if strings.Count(errOut, "[y/N]") != 1 {
			t.Errorf("the build's confirmation is asked once, the later builds were agreed by [Y/n]:\n%s", errOut)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		f := newLlamaFix(t)
		chainEngine(f, nil, map[string][]error{"cuda": {errCompile}})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "s", "s")), "--lang", "es", "rebuild-llama")
		if r.code != 0 || !strings.Contains(unwrap(r.errOut), "Eso no funcionó (LAI-LLM-018). ¿Probar compilar para Vulkan (funciona con la mayoría de los chips gráficos) en su lugar? [S/n]") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("no terminal and no --yes refuses before anything, as before", func(t *testing.T) {
		f := newLlamaFix(t)
		chainEngine(f, nil, map[string][]error{"cuda": {errCompile}})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "rebuild-llama")
		if r.code != ExitUsage || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d asked %v\n%s", r.code, backendsAsked(f), r.errOut)
		}
	})
	t.Run("no terminal with --yes goes on by itself", func(t *testing.T) {
		f := newLlamaFix(t)
		chainEngine(f, nil, map[string][]error{"cuda": {errCompile}})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "rebuild-llama", "--yes")
		if r.code != 0 || strings.Join(backendsAsked(f), ",") != "(node),vulkan" {
			t.Fatalf("exit %d asked %v\n%s", r.code, backendsAsked(f), r.errOut)
		}
	})
}

func TestRebuildLlamaFallbackJSONIsOnlyTheFinalDocument(t *testing.T) {
	f := newLlamaFix(t)
	chainEngine(f, nil, map[string][]error{"cuda": {errCompile}, "vulkan": {errVulkan}})
	r := f.run(t, "--json", "rebuild-llama", "--yes")
	var doc struct {
		BuildID string `json:"build_id"`
	}
	if r.code != 0 || r.errOut != "" || json.Unmarshal([]byte(r.out), &doc) != nil || doc.BuildID != lShort+"-cpu" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildLlamaReadyMadeBuildFailsThenCompiles(t *testing.T) {
	f := newLlamaFix(t)
	file, _ := exportInto(t, f.kitRoot(), llamaSpec())
	chainEngine(f, nil, nil)
	f.eng.buildFn = func(req build.Request) (build.Result, error) {
		if req.Prebuilt != "" {
			return build.Result{}, errUnused
		}
		return f.eng.res, nil
	}
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != 0 || len(f.eng.reqs) != 2 || f.eng.reqs[0].Prebuilt != file || f.eng.reqs[1].Prebuilt != "" {
		t.Fatalf("exit %d reqs %d\n%s\n%s", r.code, len(f.eng.reqs), r.out, r.errOut)
	}
	if !strings.Contains(unwrap(r.errOut), "… That did not work (LAI-LLM-019). Trying building for the NVIDIA graphics chip (cuda) instead.") {
		t.Errorf("the next option is not said:\n%s", r.errOut)
	}
}

func TestRebuildLlamaDecliningTheReadyMadeBuildAsksTheBuildConfirmation(t *testing.T) {
	f := newLlamaFix(t)
	exportInto(t, f.kitRoot(), llamaSpec())
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n", "n")), "rebuild-llama")
	if r.code != ExitRefused || len(f.eng.reqs) != 0 || strings.Contains(r.out+r.errOut, "That did not work") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestLlamaOutOfMemoryPatterns(t *testing.T) {
	for _, tc := range []struct {
		tail string
		want bool
	}{
		{"c++: fatal error: Killed signal terminated program cc1plus", true},
		{"nvcc error   : 'cicc' died due to signal 9 (Kill signal)", true},
		{"virtual memory exhausted: Cannot allocate memory", true},
		{"internal compiler error: Killed (program cc1plus)", true},
		{"error: 'foo' was not declared in this scope", false},
	} {
		err := llamaBuildErr(doctor.LLM018, build.StageBuild, "x", "cause", "exit", "log_tail", tc.tail)
		if got := llamaOutOfMemory(err); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.tail, got, tc.want)
		}
	}
	if llamaOutOfMemory(llamaBuildErr(doctor.LLM018, build.StageConfigure, "x", "log_tail", oomTail)) {
		t.Error("only the compile step runs out of memory this way")
	}
}
