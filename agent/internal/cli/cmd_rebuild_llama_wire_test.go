package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	routerfake "github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Tests of the seams of `aictl rebuild-llama` to the machine (AICTL-09): the ready-made build path, the
// agent, the post-switch probe, state.json, the smoke model. The engine is the fake of
// cmd_rebuild_llama_test.go.

// ---- --prebuilt ---------------------------------------------------------------------------------

func llamaSpec() exportSpec { return exportSpec{program: kit.ProgramLlama, commit: lCommit} }

func TestRebuildLlamaPrebuiltRestoresTheFile(t *testing.T) {
	f := newLlamaFix(t)
	file, rec := exportInto(t, f.kitRoot(), llamaSpec())
	f.eng.res.Restored = true
	r := f.run(t, "rebuild-llama", "--prebuilt", file, "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-llama.prebuilt.en", r)
	req := f.lastReq(t)
	if req.Prebuilt != file || req.PrebuiltSHA256 != rec.SHA256 || req.Backend != "cuda" || req.FromKit || req.SourceDir != "" {
		t.Errorf("request = %+v", req)
	}
	if len(f.eng.resolved) != 1 || f.eng.resolved[0].Commit != lCommit || f.eng.resolved[0].Prebuilt != "" {
		t.Errorf("the inputs were resolved with %+v", f.eng.resolved)
	}
	es := f.run(t, "--lang", "es", "rebuild-llama", "--prebuilt", file, "--yes")
	if es.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", es.code, es.out, es.errOut)
	}
	checkGolden(t, "rebuild-llama.prebuilt.es", es)
}

func TestRebuildLlamaPrebuiltDryRunOnlyVerifies(t *testing.T) {
	f := newLlamaFix(t)
	file, _ := exportInto(t, f.kitRoot(), llamaSpec())
	f.eng.res = build.Result{BuildID: lShort + "-cuda", DryRun: true, Restored: true}
	term := fake.Typed(true, "y")
	r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama", "--prebuilt", file, "--dry-run")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "is intact and fits this computer. Nothing was changed.") || term.Reads() != 0 {
		t.Fatalf("exit %d reads %d\n%s\n%s", r.code, term.Reads(), r.out, r.errOut)
	}
	if req := f.lastReq(t); !req.DryRun || req.Prebuilt != file {
		t.Errorf("request = %+v", req)
	}
	if f.eng.opt.Exporter != nil {
		t.Error("a dry run must not set up an export")
	}
}

func TestRebuildLlamaPrebuiltIsRefusedWhenItDoesNotFit(t *testing.T) {
	tests := []struct {
		name   string
		spec   exportSpec
		args   []string
		want   []string
		golden string
	}{
		{"another Ubuntu release", exportSpec{program: kit.ProgramLlama, commit: lCommit, codename: "resolute"}, nil,
			[]string{"Ubuntu version: the build is for resolute, this computer has noble"}, "rebuild-llama.prebuilt.release"},
		{"another backend", exportSpec{program: kit.ProgramLlama, commit: lCommit, backend: "vulkan"}, nil,
			[]string{"backend: the build is for vulkan, this computer has cuda"}, ""},
		{"another graphics chip", exportSpec{program: kit.ProgramLlama, commit: lCommit, cudaArch: "86"}, nil,
			[]string{"graphics chip (CUDA architecture): the build is for 86, this computer has 75"}, ""},
		{"another commit than asked for", llamaSpec(), []string{"--commit", "abcdef1"},
			[]string{"version of the source: the build is for " + lShort + ", this computer has abcdef1"}, ""},
		{"a whisper build for the llama command", exportSpec{program: kit.ProgramWhisper, commit: lCommit}, nil,
			[]string{"program: the build is for whisper.cpp, this computer has llama.cpp"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLlamaFix(t)
			file, _ := exportInto(t, f.kitRoot(), tc.spec)
			exportInto(t, f.kitRoot(), exportSpec{program: kit.ProgramLlama, backend: "cpu", commit: "0a1b2c3d4" + strings.Repeat("0", 31)})
			r := f.run(t, append([]string{"rebuild-llama", "--prebuilt", file, "--yes"}, tc.args...)...)
			if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT048+"]") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			for _, w := range tc.want {
				if !strings.Contains(unwrap(r.errOut), w) {
					t.Errorf("stderr lacks %q:\n%s", w, r.errOut)
				}
			}
			if !strings.Contains(unwrap(r.errOut), "Nothing was changed.") || !strings.Contains(unwrap(r.errOut), "llama.cpp-0a1b2c3d4-cpu-noble-amd64.tar.gz") {
				t.Errorf("the message must say nothing changed and what the kit has:\n%s", r.errOut)
			}
			if len(f.eng.reqs) != 0 {
				t.Error("a build that does not fit reached the engine")
			}
			if lines := f.auditLines(); len(lines) != 0 {
				t.Errorf("a refused restore must not be audited as a switch: %v", lines)
			}
			if tc.golden != "" {
				checkGolden(t, tc.golden+".en", r)
				es := f.run(t, append([]string{"--lang", "es", "rebuild-llama", "--prebuilt", file, "--yes"}, tc.args...)...)
				checkGolden(t, tc.golden+".es", es)
			}
		})
	}
}

func TestRebuildLlamaPrebuiltTheKitDoesNotListIsDistrusted(t *testing.T) {
	f := newLlamaFix(t)
	file, _ := exportInto(t, f.kitRoot(), llamaSpec())
	_ = os.Remove(filepath.Join(filepath.Dir(file), "MANIFEST.json"))
	r := f.run(t, "rebuild-llama", "--prebuilt", file, "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT001+"]") || len(f.eng.reqs) != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildLlamaPrebuiltConfirmationAndEngineFailure(t *testing.T) {
	f := newLlamaFix(t)
	file, _ := exportInto(t, f.kitRoot(), llamaSpec())
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-llama", "--prebuilt", file)
	if r.code != ExitRefused || len(f.eng.reqs) != 0 || !strings.Contains(unwrap(r.errOut), "Restore the ready-made llama.cpp build "+lShort+" and switch to it?") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	f.eng.err = llamaBuildErr(doctor.LLM019, build.StageSmoke, "The new llama.cpp build did not work; the previous build is still in use.", "rolled_back", true)
	if r := f.run(t, "rebuild-llama", "--prebuilt", file, "--yes"); r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-LLM-019]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildLlamaOffersTheReadyMadeBuildBeforeCompiling(t *testing.T) {
	t.Run("a yes at the terminal restores it", func(t *testing.T) {
		f := newLlamaFix(t)
		file, rec := exportInto(t, f.kitRoot(), llamaSpec())
		f.eng.res.Restored = true
		// the offer, then the confirmation of the restore
		term := fake.Typed(true, "y", "y")
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama")
		if r.code != 0 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		if req := f.lastReq(t); req.Prebuilt != file || req.PrebuiltSHA256 != rec.SHA256 {
			t.Errorf("request = %+v", req)
		}
		if !strings.Contains(unwrap(r.errOut), "The kit has a ready-made build for exactly this computer") {
			t.Errorf("the offer was not made:\n%s", r.errOut)
		}
	})
	t.Run("a no compiles after all", func(t *testing.T) {
		f := newLlamaFix(t)
		exportInto(t, f.kitRoot(), llamaSpec())
		term := fake.Typed(true, "n", "y")
		r := runCLI(t, DefaultRegistry, localOpts(term), "rebuild-llama")
		if r.code != 0 || f.lastReq(t).Prebuilt != "" {
			t.Fatalf("exit %d request %+v\n%s", r.code, f.lastReq(t), r.errOut)
		}
	})
	t.Run("--yes never chooses it for the operator: the hint names the file", func(t *testing.T) {
		f := newLlamaFix(t)
		file, _ := exportInto(t, f.kitRoot(), llamaSpec())
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-llama", "--yes")
		if r.code != 0 || f.lastReq(t).Prebuilt != "" || !strings.Contains(unwrap(r.out), "sudo aictl rebuild-llama --prebuilt "+file) {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("--json and --source make no offer", func(t *testing.T) {
		f := newLlamaFix(t)
		exportInto(t, f.kitRoot(), llamaSpec())
		for _, args := range [][]string{{"--json", "rebuild-llama", "--yes"}, {"rebuild-llama", "--yes", "--source", "/tmp/s"}} {
			r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), args...)
			if r.code != 0 || f.lastReq(t).Prebuilt != "" || strings.Contains(r.out+r.errOut, "ready-made") {
				t.Fatalf("%v: exit %d\n%s\n%s", args, r.code, r.out, r.errOut)
			}
		}
	})
	t.Run("a build for another backend is not offered", func(t *testing.T) {
		f := newLlamaFix(t)
		exportInto(t, f.kitRoot(), exportSpec{program: kit.ProgramLlama, commit: lCommit, backend: "vulkan"})
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-llama", "--yes")
		if r.code != 0 || strings.Contains(r.out+r.errOut, "ready-made") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
}

// ---- the agent: restart through the API ---------------------------------------------------------

func TestLlamaAgentRestartsThroughTheAgent(t *testing.T) {
	newRig := func(t *testing.T) (*mgRig, *llamaFix) {
		r := newMgRig(t, fake.Options{})
		return r, newLlamaFix(t)
	}
	run := func(r *mgRig, f *llamaFix, hook func(ctx context.Context, a build.Agent)) followOut {
		f.eng.onBuild = func(ctx context.Context, opt build.Options) { hook(ctx, opt.Agent) }
		return r.run(mgArgs("rebuild-llama", "--yes"))
	}
	t.Run("up and restarted: POST /services/llama/restart with an empty body", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().Respond(http.MethodPost, "/services/llama/restart", 200, `{"name":"llama","active":"active","restored_models":[],"op_id":"op_l1"}`)
		var up bool
		var rerr error
		got := run(r, f, func(ctx context.Context, a build.Agent) {
			up = a.Up(ctx)
			rerr = a.RestartLlama(ctx)
		})
		if got.code != 0 || !up || rerr != nil {
			t.Fatalf("exit %d up %v err %v\n%s", got.code, up, rerr, got.errOut)
		}
		if b := r.lastBody(http.MethodPost, "/services/llama/restart"); b != `{}` {
			t.Errorf("body = %s", b)
		}
	})
	t.Run("a busy agent is a BusyError", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().RespondError(http.MethodPost, "/services/llama/restart", 409, doctor.AGT022, "busy", "wait", map[string]any{"op_id": "op_busy"})
		var rerr error
		run(r, f, func(ctx context.Context, a build.Agent) { rerr = a.RestartLlama(ctx) })
		var busy *build.BusyError
		if !errors.As(rerr, &busy) {
			t.Fatalf("err = %v, want a *build.BusyError", rerr)
		}
	})
	t.Run("another failure is returned as it is", func(t *testing.T) {
		r, f := newRig(t)
		r.agent().RespondError(http.MethodPost, "/services/llama/restart", 500, doctor.SVC051, "failed", "see logs", nil)
		var rerr error
		run(r, f, func(ctx context.Context, a build.Agent) { rerr = a.RestartLlama(ctx) })
		var busy *build.BusyError
		if rerr == nil || errors.As(rerr, &busy) || errors.Is(rerr, build.ErrAgentUnavailable) {
			t.Fatalf("err = %v", rerr)
		}
	})
}

func TestLlamaAgentDownIsNotUpSoTheEngineUsesSystemctl(t *testing.T) {
	f := newLlamaFix(t)
	var up bool
	var rerr error
	f.eng.onBuild = func(ctx context.Context, opt build.Options) {
		up = opt.Agent.Up(ctx)
		rerr = opt.Agent.RestartLlama(ctx)
	}
	// localOpts points at a socket and a port nobody listens on.
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if up || !errors.Is(rerr, build.ErrAgentUnavailable) {
		t.Errorf("up = %v, err = %v: a down agent makes the switch use `systemctl restart localai-llama`", up, rerr)
	}
	if got := f.eng.opt.Recipe.Unit; got != "localai-llama.service" {
		t.Errorf("the unit systemctl restarts is %q", got)
	}
}

// ---- the post-switch probe ----------------------------------------------------------------------

// fakeRouter is the router the probe asks.
type fakeRouter struct {
	mu       sync.Mutex
	healthy  error
	models   []router.ModelStatus
	modelErr error
	chatErr  error
	chats    []string
}

func (r *fakeRouter) Health(context.Context) error { return r.healthy }
func (r *fakeRouter) Models(context.Context) ([]router.ModelStatus, error) {
	return r.models, r.modelErr
}
func (r *fakeRouter) Chat(_ context.Context, model string, n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chats = append(r.chats, model+":"+strconv.Itoa(n))
	return r.chatErr
}

type probeFix struct {
	*llamaFix
	rt   *fakeRouter
	port int
	made int
}

func newProbeFix(t *testing.T) *probeFix {
	t.Helper()
	p := &probeFix{llamaFix: newLlamaFix(t), rt: &fakeRouter{}}
	old := llamaRouterFn
	llamaRouterFn = func(_ local.Env, port int) (llamaRouter, error) {
		p.made++
		p.port = port
		return p.rt, nil
	}
	t.Cleanup(func() { llamaRouterFn = old })
	return p
}

// probe runs the command with a hook that hands the probe to the test, through a run on the agent (rig)
// or without one.
func (p *probeFix) probe(t *testing.T, rig *mgRig, hook func(ctx context.Context, pr build.Probe)) {
	t.Helper()
	p.eng.onBuild = func(ctx context.Context, opt build.Options) { hook(ctx, opt.Probe) }
	var code int
	var errOut string
	if rig != nil {
		got := rig.run(mgArgs("rebuild-llama", "--yes"))
		code, errOut = got.code, got.errOut
	} else {
		r := p.run(t, "rebuild-llama", "--yes")
		code, errOut = r.code, r.errOut
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}

func TestLlamaProbeDefaultModel(t *testing.T) {
	ctx := context.Background()
	t.Run("the agent's effective default", func(t *testing.T) {
		p := newProbeFix(t)
		rig := newMgRig(t, fake.Options{})
		rig.agent().Respond(http.MethodGet, "/models", 200, `{"default":"qwen3.5-9b-q4km","models":[]}`)
		var got string
		var err error
		p.probe(t, rig, func(ctx context.Context, pr build.Probe) { got, err = pr.DefaultModel(ctx) })
		if err != nil || got != "qwen3.5-9b-q4km" {
			t.Errorf("default = %q, %v", got, err)
		}
	})
	t.Run("the agent says there is none", func(t *testing.T) {
		p := newProbeFix(t)
		rig := newMgRig(t, fake.Options{})
		rig.agent().Respond(http.MethodGet, "/models", 200, `{"default":null,"models":[]}`)
		p.state(t, `{"models":{"default":"from-state"}}`) // the agent's answer wins, even an empty one
		var got string
		p.probe(t, rig, func(ctx context.Context, pr build.Probe) { got, _ = pr.DefaultModel(ctx) })
		if got != "" {
			t.Errorf("default = %q", got)
		}
	})
	t.Run("agent down: the one recorded in state.json", func(t *testing.T) {
		p := newProbeFix(t)
		p.state(t, `{"mode":"online","models":{"default":"from-state"}}`)
		var got string
		p.probe(t, nil, func(ctx context.Context, pr build.Probe) { got, _ = pr.DefaultModel(ctx) })
		if got != "from-state" {
			t.Errorf("default = %q", got)
		}
	})
	t.Run("agent down and nothing recorded: none, so only /health is checked", func(t *testing.T) {
		p := newProbeFix(t)
		var got string
		var err error
		p.probe(t, nil, func(ctx context.Context, pr build.Probe) { got, err = pr.DefaultModel(ctx) })
		if got != "" || err != nil {
			t.Errorf("default = %q, %v", got, err)
		}
	})
	_ = ctx
}

func TestLlamaProbeAsksTheRouterDirectly(t *testing.T) {
	p := newProbeFix(t)
	p.nodeYAML(t, "schema_version: 1\nports:\n  llama: 8123\n")
	p.rt.models = []router.ModelStatus{{ID: "m1", Status: "loaded"}, {ID: "m2", Status: "loading"}}
	var health, st1, st2, st3, chat error
	var s1, s2, s3 string
	p.probe(t, nil, func(ctx context.Context, pr build.Probe) {
		health = pr.Health(ctx)
		s1, st1 = pr.ModelStatus(ctx, "m1")
		s2, st2 = pr.ModelStatus(ctx, "m2")
		s3, st3 = pr.ModelStatus(ctx, "m3")
		chat = pr.Complete(ctx, "m1", build.SmokeTokens)
	})
	if health != nil || st1 != nil || st2 != nil || st3 != nil || chat != nil {
		t.Fatalf("errors: %v %v %v %v %v", health, st1, st2, st3, chat)
	}
	if s1 != build.StatusLoaded || s2 != build.StatusLoading || s3 != "" {
		t.Errorf("statuses = %q %q %q", s1, s2, s3)
	}
	if len(p.rt.chats) != 1 || p.rt.chats[0] != "m1:16" {
		t.Errorf("completions = %v: the check asks for 16 tokens", p.rt.chats)
	}
	if p.port != 8123 || p.made != 1 {
		t.Errorf("router port %d made %d: the port is ports.llama of node.yaml and the client is made once", p.port, p.made)
	}
}

func TestLlamaProbeReportsWhatTheRouterSays(t *testing.T) {
	p := newProbeFix(t)
	p.rt.healthy = errors.New("router: the router is not answering")
	p.rt.modelErr = errors.New("models unavailable")
	p.rt.chatErr = errors.New("completion failed")
	var errs [3]error
	p.probe(t, nil, func(ctx context.Context, pr build.Probe) {
		errs[0] = pr.Health(ctx)
		_, errs[1] = pr.ModelStatus(ctx, "m1")
		errs[2] = pr.Complete(ctx, "m1", 16)
	})
	for i, e := range errs {
		if e == nil {
			t.Errorf("error %d was swallowed", i)
		}
	}
}

func TestLlamaProbeLoadsTheModelThroughTheAgent(t *testing.T) {
	t.Run("POST /models/{id}/load without evicting the others and without waiting", func(t *testing.T) {
		p := newProbeFix(t)
		rig := newMgRig(t, fake.Options{})
		rig.agent().Respond(http.MethodPost, "/models/m1/load", 202, `{"op_id":"op_x"}`)
		var err error
		p.probe(t, rig, func(ctx context.Context, pr build.Probe) { err = pr.LoadModel(ctx, "m1") })
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if json.Unmarshal([]byte(rig.lastBody(http.MethodPost, "/models/m1/load")), &body) != nil || body["wait"] != false || body["unload_others"] != false {
			t.Errorf("body = %v", body)
		}
	})
	t.Run("a busy agent is already working on the models: not a failure", func(t *testing.T) {
		p := newProbeFix(t)
		rig := newMgRig(t, fake.Options{})
		rig.agent().RespondError(http.MethodPost, "/models/m1/load", 409, doctor.AGT022, "busy", "wait", nil)
		var err error
		p.probe(t, rig, func(ctx context.Context, pr build.Probe) { err = pr.LoadModel(ctx, "m1") })
		if err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a refusal of the model itself is a failure", func(t *testing.T) {
		p := newProbeFix(t)
		rig := newMgRig(t, fake.Options{})
		rig.agent().RespondError(http.MethodPost, "/models/m1/load", 422, doctor.LLM008, "does not fit", "pick another", nil)
		var err error
		p.probe(t, rig, func(ctx context.Context, pr build.Probe) { err = pr.LoadModel(ctx, "m1") })
		if err == nil || errors.Is(err, build.ErrNoAgent) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("agent down: ErrNoAgent, and the check keeps waiting for the router's own load", func(t *testing.T) {
		p := newProbeFix(t)
		var err error
		p.probe(t, nil, func(ctx context.Context, pr build.Probe) { err = pr.LoadModel(ctx, "m1") })
		if !errors.Is(err, build.ErrNoAgent) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestLlamaProbeRouterRefusingToStartIsReported(t *testing.T) {
	p := newProbeFix(t)
	llamaRouterFn = func(local.Env, int) (llamaRouter, error) { return nil, errors.New("bad port") }
	var errs [3]error
	p.probe(t, nil, func(ctx context.Context, pr build.Probe) {
		errs[0] = pr.Health(ctx)
		_, errs[1] = pr.ModelStatus(ctx, "m1")
		errs[2] = pr.Complete(ctx, "m1", 16)
	})
	for i, e := range errs {
		if e == nil {
			t.Errorf("error %d was swallowed", i)
		}
	}
}

// TestLlamaProbeOverTheRealRouterClient runs the default router constructor against the fake
// llama-server: the key file of /etc/localai, the port of node.yaml, /health, GET /models and a 16-token
// chat completion.
func TestLlamaProbeOverTheRealRouterClient(t *testing.T) {
	f := newLlamaFix(t)
	key := mgKey()
	rt := routerfake.New(key)
	t.Cleanup(rt.Close)
	rt.Add(routerfake.Model{ID: "m1"})
	rt.Set("m1", routerfake.Loaded)
	u, err := url.Parse(rt.URL())
	if err != nil {
		t.Fatal(err)
	}
	f.nodeYAML(t, "schema_version: 1\nports:\n  llama: "+u.Port()+"\n")
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "api-key"), []byte(key+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var health, load, chat error
	var status string
	f.eng.onBuild = func(ctx context.Context, opt build.Options) {
		health = opt.Probe.Health(ctx)
		status, load = opt.Probe.ModelStatus(ctx, "m1")
		chat = opt.Probe.Complete(ctx, "m1", build.SmokeTokens)
	}
	if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if health != nil || load != nil || chat != nil || status != build.StatusLoaded {
		t.Fatalf("health %v status %q (%v) chat %v", health, status, load, chat)
	}
	var sawChat bool
	for _, q := range rt.Requests() {
		if q.Path == "/v1/chat/completions" {
			sawChat = true
			var body map[string]any
			_ = json.Unmarshal([]byte(q.Body), &body)
			if body["max_tokens"] != float64(16) || !q.KeyOK {
				t.Errorf("chat request = %+v", q)
			}
		}
	}
	if !sawChat {
		t.Error("no chat completion reached the router")
	}
}

// ---- state.json ---------------------------------------------------------------------------------

func TestLlamaStateRecorder(t *testing.T) {
	seed := func(t *testing.T, f *llamaFix, build string) {
		t.Helper()
		st, err := state.Open(f.env.Paths.StateFile(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MutateSync(func(s *state.State) error { s.Llama.Build = build; return nil }); err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
	}
	t.Run("the new build is recorded and the old one becomes the previous build", func(t *testing.T) {
		f := newLlamaFix(t)
		seed(t, f, "0a1b2c3d4-cuda")
		if err := (llamaStateRecorder{f.env}).RecordBuild(lShort + "-cuda"); err != nil {
			t.Fatal(err)
		}
		v, ok := readStateView(f.env)
		if !ok || v.Llama.PreviousBuild == nil || *v.Llama.PreviousBuild != "0a1b2c3d4-cuda" {
			t.Fatalf("state = %+v %v", v, ok)
		}
		st, _ := state.Open(f.env.Paths.StateFile(), state.Options{})
		defer st.Close()
		if got := st.Snapshot().Llama.Build; got != lShort+"-cuda" {
			t.Errorf("build = %q", got)
		}
	})
	t.Run("the same build again changes nothing", func(t *testing.T) {
		f := newLlamaFix(t)
		seed(t, f, lShort+"-cuda")
		if err := (llamaStateRecorder{f.env}).RecordBuild(lShort + "-cuda"); err != nil {
			t.Fatal(err)
		}
		if v, _ := readStateView(f.env); v.Llama.PreviousBuild != nil {
			t.Errorf("previous build = %v", *v.Llama.PreviousBuild)
		}
	})
	t.Run("no state.json: nothing is created", func(t *testing.T) {
		f := newLlamaFix(t)
		if err := (llamaStateRecorder{f.env}).RecordBuild("x-cuda"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(f.env.Paths.StateFile()); !os.IsNotExist(err) {
			t.Errorf("state.json was created: %v", err)
		}
	})
	t.Run("a damaged state.json is left alone and reported", func(t *testing.T) {
		f := newLlamaFix(t)
		f.state(t, `{"mode": `)
		if err := (llamaStateRecorder{f.env}).RecordBuild("x-cuda"); err == nil {
			t.Error("a damaged file was written")
		}
		if got := readFile(t, f.env.Paths.StateFile()); got != `{"mode": ` {
			t.Errorf("state.json = %q", got)
		}
		entries, _ := os.ReadDir(f.env.Paths.StateDir)
		if len(entries) != 1 {
			t.Errorf("the folder gained files: %v", entries)
		}
	})
	t.Run("the engine is given it, and the previous build too", func(t *testing.T) {
		f := newLlamaFix(t)
		f.state(t, `{"llama":{"previous_build":"0a1b2c3d4-cuda"}}`)
		if r := f.run(t, "rebuild-llama", "--yes"); r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		if got := f.eng.opt.PreviousBuild(); got != "0a1b2c3d4-cuda" {
			t.Errorf("previous build = %q", got)
		}
		if _, ok := f.eng.opt.Recorder.(llamaStateRecorder); !ok {
			t.Errorf("recorder = %T", f.eng.opt.Recorder)
		}
	})
}

// ---- the smoke model ----------------------------------------------------------------------------

func TestLlamaSmokeModelIsTheSmallestVerifiedChatModel(t *testing.T) {
	f := newLlamaFix(t)
	if _, ok := llamaSmokeModel(f.env); ok {
		t.Fatal("a node with no catalog has no smoke model")
	}
	writeLocalCatalog(t, f.env.Paths.CatalogDir(), "2026.11.02-1")
	models := filepath.Join(f.env.Paths.InternalRoot, "models")
	put := func(id, engine string, size int, state string) string {
		dir := filepath.Join(models, id)
		file := id + ".gguf"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.Repeat("x", size)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mmproj-"+id+".gguf"), []byte("m"), 0o644); err != nil {
			t.Fatal(err)
		}
		m := manifest.Manifest{
			SchemaVersion: 1, ItemID: id, ItemKind: manifest.KindModel, Engine: engine, CatalogVersion: "2026.11.02-1",
			Version: "r1", SortKey: "r1", State: state, Source: manifest.Source{Type: "url"},
			Files: []manifest.File{
				{Kind: "weights", Path: file, SizeBytes: int64(size), SHA256: strings.Repeat("a", 64), SHA256Source: "catalog"},
				{Kind: "mmproj", Path: "mmproj-" + id + ".gguf", SizeBytes: 1, SHA256: strings.Repeat("b", 64), SHA256Source: "catalog"},
			},
			TotalBytes: int64(size) + 1, Trust: manifest.TrustPinned,
			DownloadedAt: kitEpoch, VerifiedAt: kitEpoch, LastReverifiedAt: kitEpoch,
			SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-1"},
		}
		if state == manifest.StateVerified {
			if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
				t.Fatal(err)
			}
		} else {
			raw, _ := json.Marshal(map[string]any{"schema_version": 1, "item_id": id, "state": state})
			if err := os.WriteFile(manifest.Path(dir), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, file)
	}
	put("model-delta-embed", "llama", 10, manifest.StateVerified)  // the smallest, but it cannot write text
	put("whisper-small", "whisper", 5, manifest.StateVerified)     // not a llama model
	put("model-gamma-vl-q4", "llama", 50, manifest.StateVerified)  // chat and vision
	put("model-alpha-9b-q4", "llama", 100, manifest.StateVerified) // chat
	put("model-beta-27b-q4", "llama", 20, "downloading")           // not verified: never used
	if got, ok := llamaSmokeModel(f.env); !ok || got != filepath.Join(models, "model-gamma-vl-q4", "model-gamma-vl-q4.gguf") {
		t.Errorf("smoke model = %q %v", got, ok)
	}
	// Remove the vision model's manifest: the next smallest chat model is used.
	_ = os.Remove(manifest.Path(filepath.Join(models, "model-gamma-vl-q4")))
	if got, ok := llamaSmokeModel(f.env); !ok || got != filepath.Join(models, "model-alpha-9b-q4", "model-alpha-9b-q4.gguf") {
		t.Errorf("smoke model = %q %v", got, ok)
	}
	// Only an embedding model is left: no completion step rather than a failing one.
	_ = os.Remove(manifest.Path(filepath.Join(models, "model-alpha-9b-q4")))
	if got, ok := llamaSmokeModel(f.env); ok {
		t.Errorf("smoke model = %q, want none", got)
	}
}

func TestLlamaSmokeModelWithAnInvalidCatalogIsNone(t *testing.T) {
	f := newLlamaFix(t)
	if err := os.MkdirAll(f.env.Paths.CatalogDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env.Paths.CatalogDir(), "models.yaml"), []byte("models: [{id: x}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := llamaSmokeModel(f.env); ok {
		t.Errorf("smoke model = %q", got)
	}
}

func TestLlamaDevicesDropTheMemoryFigures(t *testing.T) {
	tests := map[string]string{
		"": "",
		"CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design (7981 MiB, 7012 MiB free)": "CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design",
		"Vulkan0: AMD Radeon Graphics (RADV RENOIR)":                                 "Vulkan0: AMD Radeon Graphics (RADV RENOIR)",
	}
	for in, want := range tests {
		if got := llamaDevices([]string{in}); got != want {
			t.Errorf("llamaDevices(%q) = %q, want %q", in, got, want)
		}
	}
	if got := llamaDevices([]string{"CUDA0: A (1 MiB)", "CUDA1: B (2 MiB)"}); got != "CUDA0: A; CUDA1: B" {
		t.Errorf("two devices = %q", got)
	}
}

func TestLlamaProgressInSpanish(t *testing.T) {
	for in, want := range map[string]string{
		"Compiling (this takes a while)":                     "Compilando (tarda un rato)",
		"Unpacking the CUDA redistributable 12.4 of the kit": "Descomprimiendo el redistribuible de CUDA 12.4 del kit",
		"Looking for the llama.cpp source abc1234":           "Buscando el código fuente de llama.cpp abc1234",
		"Something the table does not know":                  "Something the table does not know",
	} {
		if got := llamaProgressText(LangES, in); got != want {
			t.Errorf("llamaProgressText(es, %q) = %q, want %q", in, got, want)
		}
		if got := llamaProgressText(LangEN, in); got != in {
			t.Errorf("English changed %q to %q", in, got)
		}
	}
}
