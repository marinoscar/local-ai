package router

import (
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// defaultRig is an orchestrator over the generator test world (the ai-n3 node, a scripted router
// with the 9B model loaded), so SetDefault can be seen to rewrite a real models.ini.
type defaultRig struct {
	*world
	o   *Orchestrator
	st  *state.Store
	pub *pubRec
	aud *auditRec
}

func newDefaultRig(t *testing.T) *defaultRig {
	t.Helper()
	w := newWorld(t)
	w.router = &scriptRouter{statuses: []ModelStatus{st("qwen3.5-9b-q4km", StatusLoaded), st("small-vlm-q4km", StatusUnloaded)}}
	w.build()
	w.mustGenerate()
	w.router.mu.Lock()
	w.router.calls = nil
	w.router.mu.Unlock()
	store, err := state.Open(orStatePath, state.Options{FS: statefake.New(), AgentVersion: "test", Now: w.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := &defaultRig{world: w, st: store, pub: &pubRec{}, aud: &auditRec{}}
	o, err := NewOrchestrator(OrchestratorOptions{
		Router: w.router, State: store, Inputs: w.inputs, Presence: ManifestPresence{FS: w.fs},
		Generator: w.gen, Publisher: d.pub, Audit: d.aud, Clock: w.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.o = o
	return d
}

func (d *defaultRig) startup(id string) bool {
	d.t.Helper()
	return ParseINI([]byte(d.iniText()))[id]["load-on-startup"] == "true"
}

func TestSetDefaultRegeneratesTheIniAndLoadsNothing(t *testing.T) {
	d := newDefaultRig(t)
	if !d.startup("qwen3.5-9b-q4km") || d.startup("small-vlm-q4km") {
		t.Fatal("the starting file does not make the 9B the default")
	}
	res, err := d.o.SetDefault(bg, "small-vlm-q4km", audit.Operator(audit.ViaBearer))
	if err != nil {
		t.Fatal(err)
	}
	if res.Default != "small-vlm-q4km" || res.Previous != "qwen3.5-9b-q4km" || !res.IniRegenerated {
		t.Errorf("result: %+v", res)
	}
	if d.startup("qwen3.5-9b-q4km") || !d.startup("small-vlm-q4km") {
		t.Error("load-on-startup did not move to the new default")
	}
	if got := d.st.Snapshot().Models.Default; got == nil || *got != "small-vlm-q4km" {
		t.Errorf("state.models.default = %v", got)
	}
	// Reload-safety: a default change alone leaves the running model alone, and loads nothing.
	for _, c := range d.router.callList() {
		t.Errorf("the router was called: %s", c)
	}
	var evs []string
	for _, e := range d.pub.take() {
		evs = append(evs, e.typ)
	}
	eqStrings(t, "events", evs, []string{"model.default_changed"})
	if a := d.aud.events(); len(a) != 1 || a[0].Action != audit.ActionModelSetDefault || a[0].Result != audit.ResultOK || a[0].Target["model"] != "small-vlm-q4km" {
		t.Errorf("audit: %+v", a)
	}

	// The same default again changes nothing and announces nothing.
	res, err = d.o.SetDefault(bg, "small-vlm-q4km", sysActor)
	if err != nil || res.IniRegenerated || res.Previous != "small-vlm-q4km" {
		t.Errorf("repeat: %+v %v", res, err)
	}
	if got := d.pub.take(); len(got) != 0 {
		t.Errorf("events for an unchanged default: %v", got)
	}
}

func TestSetDefaultRefusals(t *testing.T) {
	cases := []struct {
		name string
		id   string
		mut  func(*orCfg)
		code Code
		stat int
	}{
		{"unknown", "nope", nil, doctor.AGT021, 404},
		{"not a llama model", "whisper-x", nil, doctor.CAT005, 422},
		{"files missing", "m-b", func(c *orCfg) { c.presence.missing = map[string]bool{"m-b": true} }, doctor.LLM008, 422},
		{"does not fit the node", "m-b", func(c *orCfg) { c.env.GTotalMiB = 3000 }, doctor.CAT005, 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOrRig(t, tc.mut)
			_, err := r.o.SetDefault(bg, tc.id, sysActor)
			oe := opErrOf(t, err)
			if oe.Code != tc.code || oe.HTTPStatus() != tc.stat {
				t.Errorf("got %s %d", oe.Code, oe.HTTPStatus())
			}
			if r.snap().Models.Default != nil {
				t.Errorf("the default was set: %v", *r.snap().Models.Default)
			}
			if a := r.aud.events(); len(a) != 1 || a[0].Result != audit.ResultFail || a[0].Code != string(tc.code) {
				t.Errorf("audit: %+v", a)
			}
		})
	}
}

func TestSetDefaultWithoutAGeneratorOnlyRecordsIt(t *testing.T) {
	r := newOrRig(t)
	res, err := r.o.SetDefault(bg, "m-b", sysActor)
	if err != nil || res.Default != "m-b" || res.Previous != "m-a" || res.IniRegenerated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.snap().Models.Default; got == nil || *got != "m-b" {
		t.Errorf("state.models.default = %v", got)
	}
	if got := r.evs(); len(got) != 1 || !strings.HasPrefix(got[0], "default_changed m-b previous=m-a") {
		t.Errorf("events: %q", got)
	}
	if len(r.f.Requests()) != 0 {
		t.Error("the router was called")
	}
}

func TestSetDefaultUndoesTheStateWhenTheFileCannotBeWritten(t *testing.T) {
	d := newDefaultRig(t)
	d.fs.failOp["rename"] = syscall.EIO
	_, err := d.o.SetDefault(bg, "small-vlm-q4km", sysActor)
	oe := opErrOf(t, err)
	if oe.Code != CodePresetFailed || oe.HTTPStatus() != 500 {
		t.Fatalf("got %s %d", oe.Code, oe.HTTPStatus())
	}
	if got := d.st.Snapshot().Models.Default; got != nil {
		t.Errorf("the default stayed set to %q", *got)
	}
	if got := d.pub.take(); len(got) != 0 {
		t.Errorf("events: %v", got)
	}
	// Once the disk works again the same call succeeds.
	delete(d.fs.failOp, "rename")
	if _, err := d.o.SetDefault(bg, "small-vlm-q4km", sysActor); err != nil {
		t.Fatal(err)
	}
}
