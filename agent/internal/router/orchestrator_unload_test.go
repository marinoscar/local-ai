package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func TestUnloadSuccess(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.load("m-a", "m-b") // desired_loaded: m-b, m-a
	r.seed(func(s *state.State) { d := "m-a"; s.Models.Default = &d })
	res, err := r.o.Unload(bg, "m-a", false, audit.Admin(audit.ViaPIN))
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "m-a" || res.AlreadyUnloaded || res.Restarted || res.OpID == "" {
		t.Errorf("result: %+v", res)
	}
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a"})
	eqStrings(t, "events", r.evs(), []string{"unloaded m-a request"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	s := r.snap().Models
	eqStrings(t, "desired_loaded", s.DesiredLoaded, []string{"m-b"})
	if s.Default == nil || *s.Default != "m-a" {
		t.Errorf("the default must not change when it is unloaded: %v", s.Default)
	}
	a := r.aud.events()
	if len(a) != 1 || a[0].Action != audit.ActionModelUnload || a[0].Result != audit.ResultOK || a[0].OpID != res.OpID || a[0].Actor.Kind != audit.KindAdmin {
		t.Errorf("audit: %+v", a)
	}
	if got := r.sup.started; len(got) != 1 || got[0] != res.OpID || r.sup.anyActive() {
		t.Errorf("suppression: %v active=%v", got, r.sup.anyActive())
	}
	if _, held := r.o.Lock().Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestUnloadOfAModelThatIsNotRunning(t *testing.T) {
	r := newOrRig(t)
	res, err := r.o.Unload(bg, "m-b", false, sysActor)
	if err != nil || !res.AlreadyUnloaded {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.calls()) != 0 {
		t.Errorf("router calls: %v", r.calls())
	}
	// A catalog model the router does not list is not running either.
	r.f.Remove("m-c")
	if res, err := r.o.Unload(bg, "m-c", false, sysActor); err != nil || !res.AlreadyUnloaded {
		t.Errorf("not listed: %+v %v", res, err)
	}
}

func TestUnloadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		router func(*orRig)
		code   Code
		status int
	}{
		{"unknown", "nope", nil, doctor.AGT021, 404},
		{"not a llama model", "whisper-x", nil, doctor.CAT005, 422},
		{"router down", "m-a", func(r *orRig) { r.f.Down(true) }, doctor.LLM004, 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOrRig(t)
			r.load("m-a")
			if tc.router != nil {
				tc.router(r)
			}
			_, err := r.o.Unload(bg, tc.id, false, sysActor)
			oe := opErrOf(t, err)
			if oe.Code != tc.code || oe.HTTPStatus() != tc.status {
				t.Errorf("got %s %d", oe.Code, oe.HTTPStatus())
			}
			if r.countCalls("unload") != 0 {
				t.Errorf("router calls: %v", r.calls())
			}
			if _, held := r.o.Lock().Holder(); held || r.sup.anyActive() {
				t.Error("lock or suppression leaked")
			}
		})
	}
}

func TestUnloadTheLastModelInSurvivalMode(t *testing.T) {
	t.Run("refused without force", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		r.load("m-c")
		_, err := r.o.Unload(bg, "m-c", false, sysActor)
		oe := opErrOf(t, err)
		if oe.Code != CodeRefused || oe.HTTPStatus() != http.StatusConflict {
			t.Fatalf("got %s %d", oe.Code, oe.HTTPStatus())
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-c"})
		if len(r.calls()) != 0 {
			t.Errorf("router calls: %v", r.calls())
		}
	})
	t.Run("allowed with force", func(t *testing.T) {
		r := newOrRig(t, orSurvival())
		r.load("m-c")
		if _, err := r.o.Unload(bg, "m-c", true, sysActor); err != nil {
			t.Fatal(err)
		}
		if len(r.loadedNow()) != 0 {
			t.Errorf("loaded: %v", r.loadedNow())
		}
	})
	t.Run("allowed when another model stays", func(t *testing.T) {
		r := newOrRig(t, orSurvival(), orMax(2))
		r.load("m-c", "m-d")
		if _, err := r.o.Unload(bg, "m-c", false, sysActor); err != nil {
			t.Fatal(err)
		}
		eqStrings(t, "loaded", r.loadedNow(), []string{"m-d"})
	})
	t.Run("allowed outside survival mode", func(t *testing.T) {
		r := newOrRig(t)
		r.load("m-a")
		if _, err := r.o.Unload(bg, "m-a", false, sysActor); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnloadThatHangsRestartsLlamaAndRestoresTheRest(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.load("m-a", "m-b")
	r.f.Stick("m-a", true)
	r.rs.hook = r.f.Restart // the restart ends every child, and clears the stuck one
	start := r.clk.Now()
	res, err := r.o.Unload(bg, "m-a", false, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restarted || len(res.Restored) != 1 || res.Restored[0] != "m-b" {
		t.Fatalf("result: %+v", res)
	}
	if waited := r.clk.Now().Sub(start); waited < 60*time.Second || waited > 75*time.Second {
		t.Errorf("waited %v before restarting (want about 60 s)", waited)
	}
	if r.rs.calls() != 1 {
		t.Errorf("llama was restarted %d times", r.rs.calls())
	}
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b"})
	if !r.alerts.has("raise", CodeUnloadHung, "*") {
		t.Error("LAI-LLM-031 was not raised")
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-b"})
	eqStrings(t, "events", r.evs(), []string{"unloaded m-a request"})
	if r.sup.anyActive() {
		t.Error("the watcher is still suppressed")
	}
}

func TestUnloadThatHangsWithoutARestarter(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) { c.noRestarter = true })
	r.load("m-a")
	r.f.Stick("m-a", true)
	_, err := r.o.Unload(bg, "m-a", false, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeUnloadHung || oe.HTTPStatus() != http.StatusGatewayTimeout {
		t.Fatalf("got %s %d", oe.Code, oe.HTTPStatus())
	}
	eqStrings(t, "desired_loaded", r.snap().Models.DesiredLoaded, []string{"m-a"})
}

func TestUnloadThatHangsAndTheRestartFails(t *testing.T) {
	r := newOrRig(t)
	r.load("m-a")
	r.f.Stick("m-a", true)
	r.rs.err = errTest
	_, err := r.o.Unload(bg, "m-a", false, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeRouterDown || oe.HTTPStatus() != 502 || oe.Details["cause"] != "restart_failed" {
		t.Fatalf("got %s %d %v", oe.Code, oe.HTTPStatus(), oe.Details)
	}
}

func TestUnloadThatHangsAndNothingComesBack(t *testing.T) {
	r := newOrRig(t, orMax(2))
	r.load("m-a", "m-b")
	r.f.Stick("m-a", true)
	r.f.RejectLoad("m-b", 500)
	r.rs.hook = r.f.Restart
	_, err := r.o.Unload(bg, "m-a", false, sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodeNothingLoaded {
		t.Fatalf("got %s", oe.Code)
	}
	if !r.alerts.has("raise", CodeNothingLoaded, "*") {
		t.Error("LAI-LLM-012 was not raised")
	}
}

func TestUnloadThatHangsKeepsToModelsMax(t *testing.T) {
	// Three models were desired but the node holds two: the restore stops at models_max.
	r := newOrRig(t, orMax(2))
	r.load("m-a", "m-b")
	r.seed(func(s *state.State) { s.Models.DesiredLoaded = []string{"m-b", "m-c", "m-d", "m-a"} })
	r.f.Stick("m-a", true)
	r.rs.hook = r.f.Restart
	res, err := r.o.Unload(bg, "m-a", false, sysActor)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "restored", res.Restored, []string{"m-b", "m-c"})
	eqStrings(t, "loaded", r.loadedNow(), []string{"m-b", "m-c"})
}

var errTest = errString("scripted failure")

type errString string

func (e errString) Error() string { return string(e) }
