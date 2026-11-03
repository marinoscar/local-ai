package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// assertUntouched proves that nothing but reads happened: the filesystem is byte for byte what it was,
// no unit was started, stopped, enabled, masked or reloaded, nothing was built, and the agent still runs.
func assertUntouched(t *testing.T, w *world, before string, where string) {
	t.Helper()
	if after := w.fs.snapshot(); after != before {
		t.Fatalf("%s: the filesystem changed", where)
	}
	if len(w.sd.calls) != 0 {
		t.Fatalf("%s: systemd was asked to change something: %v", where, w.sd.calls)
	}
	if len(w.builder.built) != 0 || w.agent.stops != 0 || !w.agent.active {
		t.Fatalf("%s: built=%d stops=%d agent=%v", where, len(w.builder.built), w.agent.stops, w.agent.active)
	}
	if active, _ := w.sd.State(context.Background(), LegacyUnitName); active.ActiveState != "active" {
		t.Fatalf("%s: the old service is not running", where)
	}
	if len(w.cfgNode.changes) != 0 || w.st.has || w.st.def != "" {
		t.Fatalf("%s: node.yaml or state.json were written", where)
	}
}

func prepare(t *testing.T, w *world, mod ...func(*Options)) (*Engine, *Plan) {
	t.Helper()
	e := w.engine(mod...)
	p, err := e.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return e, p
}

func hasProblem(p *Plan, code, cause string) bool {
	for _, pr := range p.Problems {
		if pr.Code == code && pr.Cause == cause {
			return true
		}
	}
	return false
}

func TestDryRunChangesNothingAndPrintsEveryStep(t *testing.T) {
	for name, o := range map[string]worldOpts{
		"same fs":  {hasCurrent: true},
		"cross fs": {hasCurrent: true, crossFS: true, withDraft: true, dropIn: true},
		"build":    {},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, o)
			w.cfgNode.cfg.Node.PublicHost = ptr("node.example.test")
			w.st.conn = "full"
			before := w.fs.snapshot()
			e, p := prepare(t, w)
			if !p.OK() {
				t.Fatalf("problems: %v", p.Problems)
			}
			var out bytes.Buffer
			e.RenderPlan(&out, p)
			for _, step := range forwardSteps {
				if !strings.Contains(out.String(), "\n"+step+" ") && !strings.HasPrefix(out.String(), step+" ") {
					t.Errorf("the plan does not show %s:\n%s", step, out.String())
				}
			}
			assertUntouched(t, w, before, "dry run")
			// plan.json and serve-before.json are written by a real run only.
			if w.fs.has("/var/lib/localai/migration") {
				t.Fatal("a dry run created the migration directory")
			}
			if (name == "build") != p.BuildNeeded {
				t.Fatalf("BuildNeeded = %v", p.BuildNeeded)
			}
		})
	}
}

func TestDryRunStopsAndPrintsEveryProblem(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true, ctxMismatch: true})
	w.fs.put("/etc/llama-qwen/api-key", []byte("not a key"), 0o600)
	before := w.fs.snapshot()
	e, p := prepare(t, w)
	if p.OK() || !hasProblem(p, doctor.SEC010, "key_shape") || !hasProblem(p, doctor.LLM017, "flags_differ") {
		t.Fatalf("problems: %+v", p.Problems)
	}
	var out bytes.Buffer
	e.RenderPlan(&out, p)
	text := out.String()
	for _, want := range []string{"The migration cannot start", "LAI-SEC-010", "LAI-LLM-017", "What to do:", "--accept-catalog-flags"} {
		if !strings.Contains(text, want) {
			t.Errorf("the output lacks %q:\n%s", want, text)
		}
	}
	// Run refuses a plan with problems, before touching anything.
	if _, err := e.Run(context.Background(), p); err == nil || CodeOf(err) == "" {
		t.Fatalf("Run: %v", err)
	}
	assertUntouched(t, w, before, "refused run")
}

func TestUnmatchedShaStopsBeforeAnyChange(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.fs.put("/home/labuser/models/m1/"+w.fileName, []byte("some other weights"), 0o644)
	before := w.fs.snapshot()
	e, p := prepare(t, w)
	if !hasProblem(p, doctor.LLM017, "unmatched_file") {
		t.Fatalf("problems: %+v", p.Problems)
	}
	if got := p.Problems[0].Details["sha256"]; got != sha256Hex([]byte("some other weights")) {
		t.Fatalf("the sha256 a person needs for the catalog is not in the problem: %v", got)
	}
	_, err := e.Run(context.Background(), p)
	if CodeOf(err) != doctor.LLM017 {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, w, before, "unmatched sha")
}

func TestFlagDifferenceStopsAndAcceptCatalogFlagsContinuesWithTheCatalogValues(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true, ctxMismatch: true})
	before := w.fs.snapshot()
	e, p := prepare(t, w)
	if !hasProblem(p, doctor.LLM017, "flags_differ") || len(p.Diffs) != 1 || p.Diffs[0].Flag != "-c" {
		t.Fatalf("problems %+v diffs %+v", p.Problems, p.Diffs)
	}
	if !strings.Contains(p.Problems[0].Message, "the unit has 8192, the catalog has 4096") {
		t.Fatalf("the diff is not shown: %s", p.Problems[0].Message)
	}
	if _, err := e.Run(context.Background(), p); CodeOf(err) != doctor.LLM017 {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, w, before, "flag difference")

	// With --accept-catalog-flags the migration goes on, and n_ctx must be the catalog's.
	w = newWorld(t, worldOpts{hasCurrent: true, ctxMismatch: true})
	var out bytes.Buffer
	e = w.engine(func(o *Options) { o.AcceptCatalogFlags = true })
	p, err := e.Prepare(context.Background())
	if err != nil || !p.OK() || !p.AcceptedFlags || p.ExpectedCtx != 4096 {
		t.Fatalf("%v %+v", err, p)
	}
	e.RenderPlan(&out, p)
	if !strings.Contains(out.String(), "The catalog's values replace these settings") {
		t.Fatalf("the accepted difference is not shown:\n%s", out.String())
	}
	if _, err := e.Run(context.Background(), p); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	assertMigrated(t, w, "accepted flags")
	var plan Plan
	mustJSON(t, mustRead(t, w, "/var/lib/localai/migration/llama-qwen/plan.json"), &plan)
	if !plan.AcceptedFlags || plan.ExpectedCtx != 4096 {
		t.Fatalf("plan.json %+v", plan)
	}
	// The same node but the new server reports the OLD context: M8f fails and rolls back.
	w = newWorld(t, worldOpts{hasCurrent: true, ctxMismatch: true})
	w.nctx = 8192
	e = w.engine(func(o *Options) { o.AcceptCatalogFlags = true })
	p, _ = e.Prepare(context.Background())
	_, err = e.Run(context.Background(), p)
	assertRolledBackError(t, err, "f)")
	assertRolledBack(t, w, "ctx reported wrong")
}

func TestPreflightProblems(t *testing.T) {
	type tc struct {
		name  string
		opts  worldOpts
		setup func(*world)
		code  string
		cause string
	}
	cases := []tc{
		{name: "port is not 8080", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "host_port",
			setup: func(w *world) {
				w.setUnit(func(s string) string { return strings.Replace(s, "--port 8080", "--port 9090", 1) })
			}},
		{name: "host is not loopback", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "host_port",
			setup: func(w *world) {
				w.setUnit(func(s string) string { return strings.Replace(s, "--host 127.0.0.1", "--host 0.0.0.0", 1) })
			}},
		{name: "node.yaml names another port", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "port",
			setup: func(w *world) { w.cfgNode.cfg.Ports.Llama = 8090 }},
		{name: "key is not hex", opts: worldOpts{hasCurrent: true}, code: doctor.SEC010, cause: "key_shape",
			setup: func(w *world) { w.fs.put("/etc/llama-qwen/api-key", []byte(strings.Repeat("A", 64)), 0o600) }},
		{name: "key is short", opts: worldOpts{hasCurrent: true}, code: doctor.SEC010, cause: "key_shape",
			setup: func(w *world) { w.fs.put("/etc/llama-qwen/api-key", []byte("abcdef"), 0o600) }},
		{name: "key file is missing", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "key_unreadable",
			setup: func(w *world) { _ = w.fs.Remove("/etc/llama-qwen/api-key") }},
		{name: "another key is already at the new path", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "key_differs",
			setup: func(w *world) { w.fs.put("/etc/localai/api-key", []byte(sha256Hex([]byte("other"))), 0o600) }},
		{name: "the unit passes the key inline", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "key_inline",
			setup: func(w *world) {
				w.setUnit(func(s string) string {
					return strings.Replace(s, "--api-key-file %d/llama-api-key", "--api-key "+sha256Hex([]byte("inline")), 1)
				})
			}},
		{name: "the unit has no key", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "no_key",
			setup: func(w *world) {
				w.setUnit(func(s string) string {
					return strings.Replace(s, "  --api-key-file %d/llama-api-key", "  --metrics", 1)
				})
			}},
		{name: "the key comes from another file", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "key_path",
			setup: func(w *world) {
				w.setUnit(func(s string) string {
					return strings.Replace(s, "LoadCredential=llama-api-key:/etc/llama-qwen/api-key", "LoadCredential=llama-api-key:/root/elsewhere", 1)
				})
			}},
		{name: "the unit has no model", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "no_model",
			setup: func(w *world) {
				w.setUnit(func(s string) string {
					return strings.Replace(s, "  -m /home/labuser/models/m1/Model-One-Q4.gguf \\\n", "", 1)
				})
			}},
		{name: "the unit is a shell wrapper", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "unit_unreadable",
			setup: func(w *world) {
				w.setUnit(func(s string) string {
					return strings.Replace(s, "ExecStart=/home/labuser/llama.cpp/build-cuda/bin/llama-server", "ExecStart=/bin/sh -c x", 1)
				})
			}},
		{name: "the legacy unit is not installed", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "unit_unreadable",
			setup: func(w *world) { _ = w.fs.Remove("/etc/systemd/system/llama-qwen.service") }},
		{name: "systemctl cat fails", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "unit_unreadable",
			setup: func(w *world) { w.catErr = "No files found for llama-qwen.service." }},
		{name: "a legacy alias that cannot be kept", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "alias_invalid",
			setup: func(w *world) {
				w.setUnit(func(s string) string { return strings.Replace(s, "--alias legacy-name", "--alias Legacy_Name", 1) })
			}},
		{name: "model missing from the catalog", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "no_catalog",
			setup: func(w *world) { w.cat = catalog.Empty() }},
		{name: "the agent is not installed", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "agent_not_installed",
			setup: func(w *world) {
				_ = w.fs.Remove("/srv/localai/models/.keep")
				_ = w.fs.Remove("/srv/localai/models")
				_ = w.fs.Remove("/srv/localai")
			}},
		{name: "the new service already runs", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "new_unit_running",
			setup: func(w *world) { w.sd.unit(NewUnitName).active = "active" }},
		{name: "a catalog file is missing next to the model", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "file_missing",
			setup: func(w *world) {
				m := w.cat.Models[0]
				m.Files = append(append([]catalog.ModelFile{}, m.Files...), catalog.ModelFile{Kind: "weights", Name: "Model-One-Q4-00002-of-00002.gguf", SHA256: sha256Hex([]byte("shard 2"))})
				w.cat = catalog.New("2026.11.02-1", []catalog.Model{m}, nil, nil)
			}},
		{name: "the sibling shard does not match", opts: worldOpts{hasCurrent: true}, code: doctor.LLM017, cause: "file_mismatch",
			setup: func(w *world) {
				m := w.cat.Models[0]
				m.Files = append(append([]catalog.ModelFile{}, m.Files...), catalog.ModelFile{Kind: "weights", Name: "shard2.gguf", SHA256: sha256Hex([]byte("shard 2"))})
				w.cat = catalog.New("2026.11.02-1", []catalog.Model{m}, nil, nil)
				w.fs.put("/home/labuser/models/m1/shard2.gguf", []byte("damaged shard"), 0o644)
			}},
		{name: "not enough room to copy", opts: worldOpts{hasCurrent: true, crossFS: true}, code: doctor.LLM017, cause: "disk_full",
			setup: func(w *world) { w.fs.free[1] = 100 }},
		{name: "no build and the tools are missing", opts: worldOpts{}, code: doctor.LLM018, cause: "cannot_build",
			setup: func(w *world) { w.builder.canErr = errors.New("nvcc is not installed") }},
		{name: "the commit cannot be told and there is no build", opts: worldOpts{}, code: doctor.LLM017, cause: "commit_unknown",
			setup: func(w *world) { w.legacyVersionOut = "version: unknown\n" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, c.opts)
			c.setup(w)
			before := w.fs.snapshot()
			e, p := prepare(t, w)
			if !hasProblem(p, c.code, c.cause) {
				t.Fatalf("problems = %+v", p.Problems)
			}
			if _, err := e.Run(context.Background(), p); CodeOf(err) == "" {
				t.Fatalf("Run: %v", err)
			}
			assertUntouched(t, w, before, c.name)
			for _, pr := range p.Problems {
				if _, _, ok := doctor.Text(pr.Code, "en"); !ok {
					t.Errorf("code %s is not in the doctor table", pr.Code)
				}
				if strings.Contains(pr.Message, w.key) {
					t.Error("a problem contains the key")
				}
			}
		})
	}
}

func TestPreflightWarningsAreNotProblems(t *testing.T) {
	// The legacy commit is unknown but a build is in current: carried on with a warning.
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.legacyVersionOut = "version: unknown\n"
	w.setUnit(func(s string) string {
		return strings.Replace(s, "[Service]\n", "[Service]\nEnvironment=HOME=/home/labuser GGML_X=1\nEnvironmentFile=/etc/default/x\n", 1)
	})
	_, p := prepare(t, w)
	if !p.OK() {
		t.Fatalf("problems %+v", p.Problems)
	}
	joined := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"commit is unknown", "not carried over (only GGML_* is): Environment=HOME", "EnvironmentFile=/etc/default/x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning with %q: %v", want, p.Warnings)
		}
	}
	if p.ExtraEnv["GGML_X"] != "1" || len(p.ExtraEnv) != 1 {
		t.Errorf("ExtraEnv = %v", p.ExtraEnv)
	}
}

func TestAliasEqualToTheModelIdNeedsNoLegacyAlias(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.alias = "model-one"
	w.setUnit(func(s string) string {
		return strings.Replace(s, "--alias legacy-name", "--alias model-one,Model-Two-ok", 1)
	})
	_, p := prepare(t, w)
	// "Model-Two-ok" has capitals: it cannot be kept, which stops the migration; the id is fine.
	if !hasProblem(p, doctor.LLM017, "alias_invalid") {
		t.Fatalf("problems %+v", p.Problems)
	}
	w = newWorld(t, worldOpts{hasCurrent: true})
	w.alias = "model-one"
	w.setUnit(func(s string) string { return strings.Replace(s, "--alias legacy-name", "--alias model-one", 1) })
	e, p := prepare(t, w)
	if !p.OK() || len(p.Aliases) != 1 {
		t.Fatalf("%+v", p)
	}
	// The alias list keeps the name (the router knows the id as an alias of itself): M8 asks for the id.
	if _, err := e.Run(context.Background(), p); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
}

func TestBuildStepBuildsTheLegacyCommitFromTheLegacyCheckout(t *testing.T) {
	w := newWorld(t, worldOpts{})
	e, p := prepare(t, w)
	if !p.OK() || !p.BuildNeeded || p.LegacyCommit != "f3586e66b" || p.Backend != "cuda" {
		t.Fatalf("%+v", p)
	}
	if _, err := e.Run(context.Background(), p); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if len(w.builder.built) != 1 {
		t.Fatalf("builds: %v", w.builder.built)
	}
	b := w.builder.built[0]
	if b.Commit != "f3586e66b" || b.Backend != "cuda" || b.SourceRepo != "/home/labuser/llama.cpp" {
		t.Fatalf("build request %+v", b)
	}
	if tgt, err := w.fs.Readlink("/opt/localai/llama.cpp/current"); err != nil || tgt != "builds/f3586e66b-cuda" {
		t.Fatalf("current -> %q %v", tgt, err)
	}
	if w.st.build != "f3586e66b-cuda" {
		t.Fatalf("state.llama.build = %q", w.st.build)
	}
	if got := w.cfgNode.cfg.Llama.Commit; got != "f3586e66b" {
		t.Fatalf("node.yaml llama.commit = %q", got)
	}
	// The switch never restarts anything: only the migration's own steps start units.
	if w.sd.called("start", NewUnitName) != 1 || w.sd.called("restart", NewUnitName) != 0 {
		t.Fatalf("calls: %v", w.sd.calls)
	}
	assertMigrated(t, w, "built")
}

func TestBuildAtAnotherCommitIsReplacedAndAFailedBuildChangesNothing(t *testing.T) {
	// current exists at another commit: a build is needed.
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.legacyVersionOut = "version: 6001 (commit 0123456abcdef)\n"
	_, p := prepare(t, w)
	if !p.BuildNeeded {
		t.Fatalf("a build at another commit is needed: %+v", p)
	}
	// A build that fails stops the run at M1 with the build's own code; the old service is untouched.
	w = newWorld(t, worldOpts{})
	w.builder.buildErr = &Error{Code: doctor.LLM018, Message: "compile error"}
	e, p := prepare(t, w)
	_, err := e.Run(context.Background(), p)
	if CodeOf(err) != doctor.LLM018 {
		t.Fatalf("err = %v", err)
	}
	assertLegacyServing(t, w, "failed build")
	var me *Error
	if !errors.As(err, &me) || me.Step != M1 || me.RolledBack {
		t.Fatalf("%+v", me)
	}
	// A build that has no GPU is refused at the check of M1.
	w = newWorld(t, worldOpts{})
	w.noGPU = true
	w.cfgNode.cfg.Llama.Backend = "vulkan"
	e, p = prepare(t, w)
	_, err = e.Run(context.Background(), p)
	if CodeOf(err) != doctor.LLM018 || !strings.Contains(err.Error(), "no vulkan GPU") {
		t.Fatalf("err = %v", err)
	}
	assertLegacyServing(t, w, "no gpu")
}

func assertLegacyServing(t *testing.T, w *world, where string) {
	t.Helper()
	st, _ := w.sd.State(context.Background(), LegacyUnitName)
	if st.ActiveState != "active" || !st.Enabled() {
		t.Fatalf("%s: the old service is %+v", where, st)
	}
	if w.portOwner() != "legacy" {
		t.Fatalf("%s: port owner %q", where, w.portOwner())
	}
	if ns, _ := w.sd.State(context.Background(), NewUnitName); ns.Active() {
		t.Fatalf("%s: the new service runs", where)
	}
}

func mustJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatal(err)
	}
}

func TestOneProcessOnTwoAddressesIsStillTheOldService(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.ssDual = true
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	assertMigrated(t, w, "ipv4 and ipv6 listeners")
}

func TestThePlanShowsTheSettingsOfTheOldUnit(t *testing.T) {
	// Even when the model is not in the catalog (so there is nothing to compare with), the dry run prints
	// what the old unit says and the sha256 of the file: what a person needs to write the catalog entry.
	w := newWorld(t, worldOpts{hasCurrent: true, withDraft: true})
	w.fs.put("/home/labuser/models/m1/"+w.fileName, []byte("not in the catalog"), 0o644)
	e, p := prepare(t, w)
	var out bytes.Buffer
	e.RenderPlan(&out, p)
	text := out.String()
	for _, want := range []string{"Settings of the old unit:", "model Model-One-Q4.gguf", "draft draft.gguf", "ctx 4096", "n_gpu_layers 99", "flash_attn on", "parallel 1", "alias legacy-name", sha256Hex([]byte("not in the catalog"))} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan lacks %q:\n%s", want, text)
		}
	}
	// A unit with the speculative flags and extra flags lists them in a fixed order.
	f, err := ParseExec(strings.Fields("/b/llama-server -m /m/a.gguf --spec-type draft-mtp --spec-draft-n-max 3 --spec-draft-ngl all --jinja -c 8"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.Summary(), "|")
	if got != "model a.gguf|ctx 8|spec_type draft-mtp|spec_n_max 3|spec_draft_ngl all|extra --jinja" {
		t.Fatalf("Summary = %s", got)
	}
}
