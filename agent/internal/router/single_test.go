package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func TestSingleLoadSuccess(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	before := r.unitText()

	res, err := r.load(idVLM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.AlreadyLoaded || res.Status != "loaded" || res.ID != idVLM || res.OpID == "" {
		t.Fatalf("result = %+v", res)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{idQwen})

	// The unit now serves the new model: the golden file is the §3.4 shape for it (note --mmproj).
	checkGolden(t, "localai-llama.single.vlm.service", []byte(r.unitText()))
	// The previous unit is kept next to it, byte for byte, for a crash in the middle of a switch.
	if got := r.prevText(); got != before {
		t.Fatalf(".prev is not the previous unit:\n%s", got)
	}
	if def := r.stateDefault(); def != idVLM {
		t.Fatalf("state.models.default = %q, want %q", def, idVLM)
	}
	snap := r.st.Snapshot()
	eqStrings(t, "desired_loaded", snap.Models.DesiredLoaded, []string{idVLM})
	if ll := r.lastLoad(); ll.ID != idVLM || ll.Result != state.LoadOK || ll.OpID != res.OpID {
		t.Fatalf("last_load = %+v", ll)
	}
	// One daemon-reload and one restart; the router's own endpoints and models.ini are never touched.
	if r.reload.calls() != 1 {
		t.Errorf("daemon-reload ran %d times, want 1", r.reload.calls())
	}
	if n, _ := r.sys.counts(); n != 1 {
		t.Errorf("llama was restarted %d times, want 1", n)
	}
	for _, p := range []string{"POST /models/load", "POST /models/unload", "GET /models"} {
		if r.srv.count(p) != 0 {
			t.Errorf("single mode called %q", p)
		}
	}
	if _, ok := r.fs.get("/etc/localai/models.ini"); ok {
		t.Error("models.ini was written in single mode")
	}
	if len(r.ini.defaults()) != 0 {
		t.Error("the preset generator ran in single mode")
	}
	// Events: the load says it is a restart, the previous model stops, the default moves.
	evs := r.evs()
	if len(evs) != 4 {
		t.Fatalf("events = %q", evs)
	}
	for i, want := range []string{
		`model.loading {"model":"` + idVLM + `","unloading":["` + idQwen + `"],"op_id":"` + res.OpID + `","restart":true}`,
		`model.unloaded {"model":"` + idQwen + `","reason":"evicted"}`,
		`model.loaded {"model":"` + idVLM + `","load_ms":`,
		`model.default_changed {"model":"` + idVLM + `","previous":"` + idQwen + `"}`,
	} {
		if !strings.HasPrefix(evs[i], want) {
			t.Errorf("event %d = %s\nwant prefix %s", i, evs[i], want)
		}
	}
	if !r.alerts.has("clear", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was not cleared")
	}
	a := r.aud.events()
	if len(a) != 1 || a[0].Action != "model.load" || a[0].Result != "ok" {
		t.Fatalf("audit = %+v", a)
	}
	if c := r.o.Counters(); c.LoadsOK != 1 || c.Rollbacks != 0 {
		t.Fatalf("counters = %+v", c)
	}
}

func TestSingleLoadAlreadyLoadedAndAliases(t *testing.T) {
	cases := []struct {
		name    string
		healthy bool
		models  []srvModel
		load    string
		already bool
	}{
		{"current model", true, []srvModel{{ID: idQwen, Aliases: []string{idQwen, "qwen3.5-9b"}}}, idQwen, true},
		{"served under its alias", true, []srvModel{{ID: "qwen3.5-9b", Aliases: []string{"qwen3.5-9b", idQwen}}}, idQwen, true},
		{"another model is served", true, []srvModel{{ID: idVLM, Aliases: []string{idVLM}}}, idQwen, false},
		{"listed but /health is not 200", false, []srvModel{{ID: idQwen, Aliases: []string{idQwen}}}, idQwen, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSingleRig(t)
			r.boot(idQwen)
			r.srv.set(c.healthy, LlamaModeSingle, c.models...)
			res, err := r.load(c.load)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			n, _ := r.sys.counts()
			if res.AlreadyLoaded != c.already {
				t.Fatalf("AlreadyLoaded = %v, want %v", res.AlreadyLoaded, c.already)
			}
			if c.already && (n != 0 || r.reload.calls() != 0) {
				t.Fatalf("an already loaded model restarted llama (%d) or reloaded systemd (%d)", n, r.reload.calls())
			}
			if !c.already && n != 1 {
				t.Fatalf("restarts = %d, want 1", n)
			}
		})
	}
}

// TestSingleStatus: a model is loaded when /health is 200 and the id is listed, aliases included.
func TestSingleStatus(t *testing.T) {
	r := newSingleRig(t)
	both := []srvModel{{ID: idQwen, Aliases: []string{idQwen, "qwen3.5-9b"}}}
	cases := []struct {
		name    string
		healthy bool
		models  []srvModel
		ask     string
		want    Status
	}{
		{"id listed", true, both, idQwen, StatusLoaded},
		{"alias listed", true, both, "qwen3.5-9b", StatusLoaded},
		{"other model", true, both, idVLM, StatusUnloaded},
		{"health not 200", false, both, idQwen, StatusUnloaded},
		{"nothing listed", true, nil, idQwen, StatusUnloaded},
	}
	for _, c := range cases {
		r.srv.set(c.healthy, LlamaModeSingle, c.models...)
		if got := r.o.singleStatus(bg, c.ask); got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSingleLoadChecks(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		mut    func(*singleCfg)
		code   Code
		status int
	}{
		{"unknown model", "no-such-model", nil, CodeNotFound, 404},
		{"not a llama model", "whisper-x", nil, CodeNotFit, 422},
		{"units not wired", idVLM, func(c *singleCfg) { c.noUnits = true }, CodePresetFailed, 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mut []func(*singleCfg)
			if c.mut != nil {
				mut = append(mut, c.mut)
			}
			r := newSingleRig(t, mut...)
			r.boot(idQwen)
			if c.id == "whisper-x" {
				// the catalog of the rig has only llama models: add a speech one
				orig := r.o.opt.Inputs
				r.o.opt.Inputs = func() Inputs {
					in := orig()
					models := append([]catalog.Model{}, in.Catalog.Models...)
					models = append(models, catalog.Model{ID: "whisper-x", Engine: "whisper", Requirements: req(0, 2000)})
					in.Catalog = &catalog.Catalog{Version: "test", Models: models}
					return in
				}
			}
			_, err := r.load(c.id)
			oe := opErrOf(t, err)
			if oe.Code != c.code || oe.Status != c.status {
				t.Fatalf("error = %s %d (%s), want %s %d", oe.Code, oe.Status, oe.Message, c.code, c.status)
			}
			if n, _ := r.sys.counts(); n != 0 {
				t.Fatalf("a refused load restarted llama %d times", n)
			}
		})
	}
}

// TestSingleLoadFailureRollsBack: every failure of the new unit puts the previous unit back, restarts,
// waits, and answers with the code of the failure, rolled_back true. Nothing is recorded as default.
func TestSingleLoadFailureRollsBack(t *testing.T) {
	cases := []struct {
		name         string
		prepare      func(r *singleRig)
		code         Code
		status       int
		cause        string
		restarts     int // llama restarts: the load, then the rollback
		reloads      int // daemon-reloads: the new unit, then the old one
		lastLoad     string
		minWaitPolls int
	}{
		{
			name:    "the new model crashes at start",
			prepare: func(r *singleRig) { r.sys.crash[idVLM] = true },
			code:    CodeLoadTimeout, status: http.StatusGatewayTimeout, cause: kindTimeout,
			restarts: 2, reloads: 2, lastLoad: state.LoadTimeout, minWaitPolls: 300,
		},
		{
			name:    "the server comes up with another model",
			prepare: func(r *singleRig) { r.sys.wrong[0] = srvModel{ID: "some-other-model", Aliases: []string{"some-other-model"}} },
			code:    CodeModelFailed, status: http.StatusInternalServerError, cause: kindNotListed,
			restarts: 2, reloads: 2, lastLoad: state.LoadFailed,
		},
		{
			name:    "systemctl restart fails",
			prepare: func(r *singleRig) { r.sys.fail[0] = errors.New("restart failed") },
			code:    CodeModelFailed, status: http.StatusInternalServerError, cause: kindRestartFailed,
			restarts: 2, reloads: 2, lastLoad: state.LoadFailed,
		},
		{
			name:    "daemon-reload fails: llama is not restarted",
			prepare: func(r *singleRig) { r.reload.fail[0] = true },
			code:    CodeModelFailed, status: http.StatusInternalServerError, cause: kindUnit,
			restarts: 0, reloads: 2, lastLoad: state.LoadFailed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSingleRig(t)
			r.boot(idQwen)
			before := r.unitText()
			c.prepare(r)

			_, err := r.load(idVLM)
			oe := opErrOf(t, err)
			if oe.Code != c.code || oe.Status != c.status {
				t.Fatalf("error = %s %d (%s), want %s %d", oe.Code, oe.Status, oe.Message, c.code, c.status)
			}
			if oe.Details["rolled_back"] != true || oe.Details["cause"] != c.cause {
				t.Fatalf("details = %v, want rolled_back true and cause %s", oe.Details, c.cause)
			}
			eqStrings(t, "restored", oe.Details["restored"].([]string), []string{idQwen})
			if oe.Details["op_id"] == "" {
				t.Error("no op_id in the details")
			}
			// The previous unit is back byte for byte, the previous model serves again, the default
			// did not move.
			if got := r.unitText(); got != before {
				t.Fatalf("the unit was not restored:\n%s", got)
			}
			if got := r.o.singleStatus(bg, idQwen); got != StatusLoaded {
				t.Fatalf("the previous model is %s after the rollback", got)
			}
			if def := r.stateDefault(); def != "" {
				t.Fatalf("state.models.default = %q after a failed load", def)
			}
			if n, _ := r.sys.counts(); n != c.restarts {
				t.Errorf("restarts = %d, want %d", n, c.restarts)
			}
			if r.reload.calls() != c.reloads {
				t.Errorf("daemon-reloads = %d, want %d", r.reload.calls(), c.reloads)
			}
			if ll := r.lastLoad(); ll.Result != c.lastLoad || ll.ID != idVLM {
				t.Errorf("last_load = %+v", ll)
			}
			if r.alerts.has("raise", CodeNothingLoaded, "*") {
				t.Error("LAI-LLM-012 was raised although the previous model came back")
			}
			evs := r.evs()
			if !hasSub(evs, `model.failed {"model":"`+idVLM+`","code":"`+string(c.code)+`","message":"`) || !hasSub(evs, `"rolled_back":true,"restored":["`+idQwen+`"]`) {
				t.Errorf("events = %q", evs)
			}
			if cn := r.o.Counters(); cn.LoadsFailed != 1 || cn.Rollbacks != 1 {
				t.Errorf("counters = %+v", cn)
			}
			if a := r.aud.events(); len(a) != 1 || a[0].Result != "fail" || a[0].Code != string(c.code) {
				t.Errorf("audit = %+v", a)
			}
			if c.minWaitPolls > 0 && r.sleeps < c.minWaitPolls {
				t.Errorf("the load gave up after %d polls, before the load timeout", r.sleeps)
			}
		})
	}
}

// TestSingleLoadBothFail: the new model fails and the previous one cannot be started again either:
// 500 LAI-LLM-012, rolled_back false, the alert is raised, and the default is untouched.
func TestSingleLoadBothFail(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(r *singleRig)
	}{
		{"neither model starts", func(r *singleRig) { r.sys.crash[idVLM] = true; r.sys.crash[idQwen] = true }},
		{"the previous unit cannot be written back", func(r *singleRig) {
			r.sys.crash[idVLM] = true
			r.sys.onRestart = func(n int) {
				if n == 0 {
					r.fs.memFS.mu.Lock()
					r.fs.memFS.failOp["rename"] = errors.New("read-only file system")
					r.fs.memFS.mu.Unlock()
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSingleRig(t)
			r.boot(idQwen)
			c.prepare(r)
			_, err := r.load(idVLM)
			oe := opErrOf(t, err)
			if oe.Code != CodeNothingLoaded || oe.Status != http.StatusInternalServerError {
				t.Fatalf("error = %s %d, want LAI-LLM-012 500", oe.Code, oe.Status)
			}
			if oe.Details["rolled_back"] != false {
				t.Fatalf("details = %v, want rolled_back false", oe.Details)
			}
			if !r.alerts.has("raise", CodeNothingLoaded, "*") {
				t.Error("LAI-LLM-012 was not raised")
			}
			if def := r.stateDefault(); def != "" {
				t.Fatalf("state.models.default = %q", def)
			}
			if !hasSub(r.evs(), `"code":"`+string(CodeNothingLoaded)+`"`) {
				t.Error("model.failed did not carry LAI-LLM-012")
			}
		})
	}
}

func TestSingleUnload(t *testing.T) {
	t.Run("without force it is refused", func(t *testing.T) {
		r := newSingleRig(t)
		r.boot(idQwen)
		_, err := r.o.Unload(bg, idQwen, false, sysActor)
		oe := opErrOf(t, err)
		if string(oe.Code) != doctor.AGT023 || oe.Status != http.StatusConflict {
			t.Fatalf("error = %s %d, want LAI-AGT-023 409", oe.Code, oe.Status)
		}
		if oe.Details["mode"] != LlamaModeSingle || oe.Details["model"] != idQwen {
			t.Fatalf("details = %v", oe.Details)
		}
		if _, stops := r.sys.counts(); stops != 0 {
			t.Fatal("the unit was stopped without force")
		}
		if r.o.singleStatus(bg, idQwen) != StatusLoaded {
			t.Fatal("the model is not loaded any more")
		}
		a := r.aud.events()
		if len(a) != 1 || a[0].Action != "model.unload" || a[0].Result != "fail" || a[0].Code != string(CodeRefused) {
			t.Fatalf("audit = %+v", a)
		}
	})
	t.Run("with force the unit is stopped", func(t *testing.T) {
		r := newSingleRig(t)
		r.boot(idQwen)
		r.seed(func(s *state.State) { s.Models.DesiredLoaded = []string{idQwen}; d := idQwen; s.Models.Default = &d })
		res, err := r.o.Unload(bg, idQwen, true, sysActor)
		if err != nil {
			t.Fatalf("Unload: %v", err)
		}
		if res.AlreadyUnloaded || res.Restarted || res.OpID == "" {
			t.Fatalf("result = %+v", res)
		}
		if _, stops := r.sys.counts(); stops != 1 {
			t.Fatalf("stops = %d, want 1", stops)
		}
		if r.o.singleStatus(bg, idQwen) != StatusUnloaded {
			t.Fatal("the model is still loaded")
		}
		snap := r.st.Snapshot()
		if len(snap.Models.DesiredLoaded) != 0 {
			t.Fatalf("desired_loaded = %v", snap.Models.DesiredLoaded)
		}
		if r.stateDefault() != idQwen {
			t.Fatal("an unload changed the default")
		}
		if evs := r.evs(); len(evs) != 1 || !strings.HasPrefix(evs[0], `model.unloaded {"model":"`+idQwen+`","reason":"request"}`) {
			t.Fatalf("events = %q", evs)
		}
	})
	t.Run("a model that is not loaded is already unloaded", func(t *testing.T) {
		r := newSingleRig(t)
		r.boot(idQwen)
		res, err := r.o.Unload(bg, idVLM, false, sysActor)
		if err != nil || !res.AlreadyUnloaded {
			t.Fatalf("Unload = %+v, %v", res, err)
		}
		if _, stops := r.sys.counts(); stops != 0 {
			t.Fatal("a stop was issued")
		}
	})
	t.Run("an unknown model is 404", func(t *testing.T) {
		r := newSingleRig(t)
		r.boot(idQwen)
		_, err := r.o.Unload(bg, "no-such-model", true, sysActor)
		if oe := opErrOf(t, err); oe.Code != CodeNotFound {
			t.Fatalf("error = %v", oe)
		}
	})
}

func (r *singleRig) seed(fn func(*state.State)) {
	r.t.Helper()
	if err := r.st.MutateSync(func(s *state.State) error { fn(s); return nil }); err != nil {
		r.t.Fatal(err)
	}
}

// TestSingleLoadWaitsForSlowStart: a model that takes a while to answer is waited for, without a
// real sleep: the virtual clock moves with every poll.
func TestSingleLoadWaitsForSlowStart(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	r.sys.slow[idVLM] = 40
	start := r.clk.Now()
	if _, err := r.load(idVLM); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if took := r.clk.Now().Sub(start); took < 40*time.Second || took > 60*time.Second {
		t.Fatalf("the load took %s of virtual time, want about 40 s", took)
	}
	if ll := r.lastLoad(); ll.Result != state.LoadOK || ll.LoadMS < 40000 {
		t.Fatalf("last_load = %+v", ll)
	}
}

// TestSingleLoadNoWait: with wait false the answer is the 202 and the outcome arrives by OnDone.
func TestSingleLoadNoWait(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	done := make(chan error, 1)
	res, err := r.o.LoadWith(bg, LoadRequest{ID: idVLM, Wait: false, UnloadOthers: true, Actor: sysActor,
		OnDone: func(_ LoadResult, err error) { done <- err }})
	if err != nil || !res.Accepted || res.Status != "loading" {
		t.Fatalf("LoadWith = %+v, %v", res, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("background load: %v", err)
	}
	if r.stateDefault() != idVLM {
		t.Fatal("the default did not move")
	}
}

// TestSingleSetDefault: in single mode the default is recorded and the unit follows without a restart;
// models.ini is not written.
func TestSingleSetDefault(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	res, err := r.o.SetDefault(bg, idVLM, sysActor)
	if err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if res.Default != idVLM || res.Previous != idQwen {
		t.Fatalf("result = %+v", res)
	}
	if n, _ := r.sys.counts(); n != 0 {
		t.Fatal("SetDefault restarted llama")
	}
	if !strings.Contains(r.unitText(), "small-vlm-q4km/model-Q4_K_M.gguf") {
		t.Fatalf("the unit does not serve the new default:\n%s", r.unitText())
	}
	if len(r.ini.defaults()) != 0 {
		t.Fatal("models.ini was regenerated in single mode")
	}
	if !r.alerts.has("raise", CodeUnitRestart, "llama-unit") {
		t.Error("LAI-AGT-016 was not raised for the changed command line")
	}
}

// TestSingleSwitchRouterSingleRouter: flipping llama.mode regenerates the unit, reloads systemd,
// restarts llama and brings the default back, and LAI-LLM-023 is raised while single mode is active.
func TestSingleSwitchRouterSingleRouter(t *testing.T) {
	r := newSingleRig(t, func(c *singleCfg) { c.mode = LlamaModeRouter })
	// The node starts as a router: unit and server of router mode.
	in := r.inputs()
	if _, err := r.gen.Generate(bg, r.o.routerUnitInputs(in)); err != nil {
		t.Fatal(err)
	}
	r.sys.apply()
	routerUnit := r.unitText()
	if !strings.Contains(routerUnit, "--models-max 1") {
		t.Fatalf("not a router unit:\n%s", routerUnit)
	}
	r.clearRecorders()

	// Already in the mode node.yaml asks for: nothing happens.
	res, err := r.o.ApplyMode(bg, ModeRequest{})
	if err != nil || res.Changed {
		t.Fatalf("ApplyMode in the running mode = %+v, %v", res, err)
	}
	if n, _ := r.sys.counts(); n != 0 || r.reload.calls() != 0 {
		t.Fatal("a switch to the mode that runs restarted llama")
	}

	// router -> single
	r.setMode(LlamaModeSingle)
	res, err = r.o.ApplyMode(bg, ModeRequest{})
	if err != nil {
		t.Fatalf("ApplyMode to single: %v", err)
	}
	if !res.Changed || res.Mode != LlamaModeSingle || res.Model != idQwen {
		t.Fatalf("result = %+v", res)
	}
	singleUnit := r.unitText()
	if strings.Contains(singleUnit, "--models-max") || !strings.Contains(singleUnit, "-m /srv/localai/models/"+idQwen+"/") {
		t.Fatalf("not the single unit of the default:\n%s", singleUnit)
	}
	if r.prevText() != routerUnit {
		t.Fatal(".prev is not the router unit")
	}
	if n, _ := r.sys.counts(); n != 1 || r.reload.calls() != 1 {
		t.Fatalf("restarts = %d, reloads = %d, want 1 and 1", n, r.reload.calls())
	}
	if !r.alerts.has("raise", CodeSingleMode, "*") {
		t.Fatal("LAI-LLM-023 was not raised in single mode")
	}
	if r.o.singleStatus(bg, idQwen) != StatusLoaded {
		t.Fatal("the default is not served in single mode")
	}
	if len(r.ini.defaults()) != 0 {
		t.Fatal("models.ini was written while switching to single mode")
	}
	evs := r.evs()
	if !hasSub(evs, `"restart":true`) || !hasSub(evs, `model.loaded {"model":"`+idQwen+`"`) {
		t.Fatalf("events = %q", evs)
	}

	// In single mode a load moves the default.
	if _, err := r.load(idVLM); err != nil {
		t.Fatalf("Load in single mode: %v", err)
	}
	if r.stateDefault() != idVLM {
		t.Fatal("the default did not follow the load")
	}
	r.clearRecorders()

	// single -> router: the unit is router again, models.ini is brought to the new default before
	// the restart, and the models come back.
	r.setMode(LlamaModeRouter)
	res, err = r.o.ApplyMode(bg, ModeRequest{})
	if err != nil {
		t.Fatalf("ApplyMode to router: %v", err)
	}
	if !res.Changed || res.Mode != LlamaModeRouter {
		t.Fatalf("result = %+v", res)
	}
	eqStrings(t, "restored", res.Restored, []string{idVLM})
	if got := r.unitText(); got != routerUnit {
		t.Fatalf("the router unit is not back:\n%s", got)
	}
	eqStrings(t, "models.ini generated for the default", r.ini.defaults(), []string{idVLM})
	if n, _ := r.sys.counts(); n != 1 || r.reload.calls() != 1 {
		t.Fatalf("restarts = %d, reloads = %d, want 1 and 1", n, r.reload.calls())
	}
	if !r.alerts.has("clear", CodeSingleMode, "*") {
		t.Fatal("LAI-LLM-023 was not cleared when router mode came back")
	}
	if r.stateDefault() != idVLM {
		t.Fatal("the default was lost in the switch back")
	}
	if got, err := r.client.Models(bg); err != nil || len(got) != 2 || !got[1].Loaded() {
		t.Fatalf("router Models = %+v, %v", got, err)
	}
}

// TestSingleSwitchFailures: a switch that cannot start the model reports it and raises LAI-LLM-013;
// it is never rolled back (node.yaml says what the operator wants).
func TestSingleSwitchFailures(t *testing.T) {
	t.Run("the default does not start in single mode", func(t *testing.T) {
		r := newSingleRig(t, func(c *singleCfg) { c.mode = LlamaModeRouter })
		if _, err := r.gen.Generate(bg, r.o.routerUnitInputs(r.inputs())); err != nil {
			t.Fatal(err)
		}
		r.sys.apply()
		r.sys.crash[idQwen] = true
		r.setMode(LlamaModeSingle)
		_, err := r.o.ApplyMode(bg, ModeRequest{})
		oe := opErrOf(t, err)
		if oe.Code != CodeLoadTimeout {
			t.Fatalf("error = %v", oe)
		}
		if !r.alerts.has("raise", CodeDefaultNotStarted, "*") {
			t.Error("LAI-LLM-013 was not raised")
		}
	})
	t.Run("no default model", func(t *testing.T) {
		r := newSingleRig(t, func(c *singleCfg) { c.mode = LlamaModeRouter })
		r.sys.apply() // no unit yet: the server is down
		r.setMode(LlamaModeSingle)
		orig := r.o.opt.Inputs
		r.o.opt.Inputs = func() Inputs {
			in := orig()
			p := *in.Profile
			p.DefaultModel = ""
			in.Profile = &p
			return in
		}
		_, err := r.o.ApplyMode(bg, ModeRequest{})
		if oe := opErrOf(t, err); oe.Code != CodeNoDefault {
			t.Fatalf("error = %v", oe)
		}
		if n, _ := r.sys.counts(); n != 0 {
			t.Fatal("llama was restarted without a model to serve")
		}
	})
	t.Run("without units it does nothing", func(t *testing.T) {
		r := newSingleRig(t, func(c *singleCfg) { c.noUnits = true; c.mode = LlamaModeSingle })
		res, err := r.o.ApplyMode(bg, ModeRequest{})
		if err != nil || res.Changed {
			t.Fatalf("ApplyMode = %+v, %v", res, err)
		}
	})
}

// TestSingleCrashMidSwitchIsRecoveredByReconcile: the agent dies after the unit was rewritten for the
// new model but before the load finished. The unit on disk is the new one, .prev holds the previous
// one, and state.models.default still names the previous model, so the boot reconcile (#32) renders
// the previous unit again.
func TestSingleCrashMidSwitchIsRecoveredByReconcile(t *testing.T) {
	r := newSingleRig(t)
	r.boot(idQwen)
	before := r.unitText()

	// The shutdown (or a crash) arrives while llama restarts: the load is left as interrupted and,
	// as in router mode, no rollback runs.
	r.sys.slow[idVLM] = 5
	r.sys.onRestart = func(int) { r.o.closeOnce.Do(func() { close(r.o.closing) }) }
	_, err := r.load(idVLM)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Load = %v, want ErrInterrupted", err)
	}
	if got := r.unitText(); got == before || !strings.Contains(got, idVLM) {
		t.Fatalf("the unit was not rewritten for the new model:\n%s", got)
	}
	if got := r.prevText(); got != before {
		t.Fatalf(".prev is not the previous unit:\n%s", got)
	}
	if def := r.stateDefault(); def != "" {
		t.Fatalf("state.models.default = %q before the load finished", def)
	}
	if ll := r.lastLoad(); ll.Result != state.LoadInterrupted {
		t.Fatalf("last_load = %+v", ll)
	}

	// The agent starts again: reconcile renders the unit from the recorded default.
	rec, err := NewReconciler(ReconcileOptions{
		Units: r.gen, Router: r.client,
		Inputs: func() (Inputs, UnitInputs) {
			in := r.inputs()
			id := singleDefault(in, r.st.Snapshot())
			m, _ := findModel(in.Catalog, id)
			return in, r.o.singleUnitInputs(in, m, rootInternal)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reload.mu.Lock()
	r.reload.n = 0
	r.reload.mu.Unlock()
	res, err := rec.Reconcile(bg)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.Unit.Written || !res.Unit.Reloaded || res.ModelsRan {
		t.Fatalf("reconcile result = %+v", res)
	}
	if got := r.unitText(); got != before {
		t.Fatalf("reconcile did not bring the previous unit back:\n%s", got)
	}
	if r.reload.calls() != 1 {
		t.Fatalf("daemon-reloads = %d, want 1", r.reload.calls())
	}
}

func TestSingleKeyNeverLogged(t *testing.T) {
	// newSingleRig fails the test when the bearer is in the logs; run one failing and one passing load.
	r := newSingleRig(t)
	r.boot(idQwen)
	r.sys.crash[idVLM] = true
	_, _ = r.load(idVLM)
	r.sys.crash[idVLM] = false
	if _, err := r.load(idVLM); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
