package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Workloads (E11, #509; llama-router.md section 4.16) on the music rig: ai-n3, 8 GB, models_max 1.

func lbl(en, es string) catalog.WorkloadLabel { return catalog.WorkloadLabel{EN: en, ES: es} }

func testWorkloads() []Workload {
	return []Workload{
		{Name: "chat", Label: lbl("Chat", "Conversación"), Models: []string{"m-a"}},
		{Name: "coding", Label: lbl("Coding", "Programación"), Models: []string{"m-b"}},
		{Name: "music", Label: lbl("Music", "Música"), Models: []string{"music-l"}},
		{Name: "song-and-chat", Label: lbl("Song and chat", "Canción y charla"), Models: []string{"m-a", "music-s"}},
		{Name: "too-big", Label: lbl("Too big", "Demasiado"), Models: []string{"m-a", "music-l"}},
	}
}

func withWorkloads() func(*orCfg) { return func(c *orCfg) { c.workloads = testWorkloads() } }

func workloadRig(t *testing.T, mut ...func(*orCfg)) (*orRig, *fakeEngine) {
	return musicRig(t, append([]func(*orCfg){withWorkloads()}, mut...)...)
}

func activate(r *orRig, name string, force bool) (WorkloadResult, error) {
	return r.o.ActivateWorkload(bg, WorkloadRequest{Name: name, Force: force, Wait: true, Actor: sysActor})
}

func TestWorkloadFromEmpty(t *testing.T) {
	r, _ := workloadRig(t)
	res, err := activate(r, "chat", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Active != "chat" || strings.Join(res.Loaded, ",") != "m-a" || len(res.Unloaded) != 0 {
		t.Errorf("result: %+v", res)
	}
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a"})
	if w := r.snap().Workloads; w == nil || w.Active != "chat" {
		t.Errorf("state.workloads: %+v", w)
	}
	evs := r.evs()
	if !strings.HasPrefix(evs[0], "workload.progress") {
		t.Errorf("events: %q", evs)
	}
	if last := evs[len(evs)-1]; !strings.HasPrefix(last, "workload.done") {
		t.Errorf("last event: %q", last)
	}
	a := r.aud.events()
	if len(a) != 1 || a[0].Action != audit.ActionWorkloadActivate || a[0].Target["name"] != "chat" || a[0].Target["force"] != "false" || a[0].Result != audit.ResultOK {
		t.Errorf("audit: %+v", a)
	}
	v := r.o.Workloads(bg)
	if v.Active != "chat" {
		t.Errorf("view active = %q", v.Active)
	}
	for _, w := range v.Workloads {
		if (w.Name == "chat") != w.Active {
			t.Errorf("%s active = %v", w.Name, w.Active)
		}
		if (w.Name == "too-big") == w.Fits {
			t.Errorf("%s fits = %v", w.Name, w.Fits)
		}
	}
}

func TestWorkloadFromAnotherSwitchesEngines(t *testing.T) {
	r, e := workloadRig(t)
	r.load("m-a")
	res, err := activate(r, "music", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Unloaded, ",") != "m-a" || strings.Join(res.Loaded, ",") != "music-l" {
		t.Errorf("result: %+v", res)
	}
	eqStrings(t, "router calls", r.calls(), []string{"unload m-a"})
	eqStrings(t, "engine calls", e.log(), []string{"start music-l"})
	if v := r.o.Workloads(bg); v.Active != "music" {
		t.Errorf("view active = %q", v.Active)
	}
	// And back: music is stopped, m-b loads.
	if _, err := activate(r, "coding", false); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "engine calls", e.log(), []string{"start music-l", "stop music-l"})
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-b"})
}

func TestWorkloadAlreadyActiveDoesNothing(t *testing.T) {
	r, e := workloadRig(t)
	r.load("m-a")
	res, err := activate(r, "chat", false)
	if err != nil || len(res.Loaded) != 0 || len(res.Unloaded) != 0 || res.Active != "chat" {
		t.Fatalf("activate = %+v, %v", res, err)
	}
	eqStrings(t, "router calls", r.calls(), nil)
	eqStrings(t, "engine calls", e.log(), nil)
}

func TestWorkloadMemberFailsRollsBack(t *testing.T) {
	r, e := workloadRig(t)
	r.load("m-a")
	e.startErr = &EngineError{Code: CodeEngineStart, Cause: "unit"}
	_, err := activate(r, "music", false)
	oe := opErrOf(t, err)
	if oe.Code != CodeWorkloadFailed || oe.HTTPStatus() != http.StatusInternalServerError || oe.Details["cause"] != string(CodeEngineStart) {
		t.Fatalf("err = %v %+v", err, oe.Details)
	}
	eqStrings(t, "restored", oe.Details["restored"].([]string), []string{"m-a"})
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a"})
	eqStrings(t, "music", e.holding(), nil)
	if w := r.snap().Workloads; w != nil {
		t.Errorf("state.workloads changed: %+v", w)
	}
	evs := r.evs()
	if last := evs[len(evs)-1]; !strings.Contains(last, "rolled_back") || !strings.Contains(last, string(CodeWorkloadFailed)) {
		t.Errorf("last event: %q", last)
	}
}

func TestWorkloadLlamaMemberFailsRollsBack(t *testing.T) {
	r, e := workloadRig(t)
	e.set("music-l")
	r.f.FailLoad("m-a", 1)
	_, err := activate(r, "chat", false)
	oe := opErrOf(t, err)
	if oe.Code != CodeWorkloadFailed || oe.Details["cause"] != string(CodeModelFailed) {
		t.Fatalf("err = %v %+v", err, oe.Details)
	}
	eqStrings(t, "music back", e.holding(), []string{"music-l"})
}

func TestWorkloadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mut    []func(*orCfg)
		wl     string
		force  bool
		code   Code
		status int
	}{
		{"unknown", nil, "nope", false, CodeWorkloadUnknown, http.StatusNotFound},
		{"does not fit", nil, "too-big", false, CodeWorkloadNoFit, http.StatusUnprocessableEntity},
		{"survival without force", []func(*orCfg){orSurvival()}, "chat", false, CodeWorkloadSurvival, http.StatusConflict},
		{"music in survival even with force", []func(*orCfg){orSurvival()}, "music", true, CodeRefused, http.StatusConflict},
		{"files missing", []func(*orCfg){func(c *orCfg) { c.presence.missing = map[string]bool{"m-b": true} }}, "coding", false, CodeFilesMissing, http.StatusUnprocessableEntity},
		{"single mode", []func(*orCfg){func(c *orCfg) { c.llama.Mode = LlamaModeSingle }}, "chat", false, CodeRefused, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, e := workloadRig(t, c.mut...)
			_, err := activate(r, c.wl, c.force)
			oe := opErrOf(t, err)
			if oe.Code != c.code || oe.HTTPStatus() != c.status {
				t.Fatalf("err = %v (status %d)", err, oe.HTTPStatus())
			}
			eqStrings(t, "router calls", r.calls(), nil)
			eqStrings(t, "engine calls", e.log(), nil)
		})
	}
	t.Run("not installed", func(t *testing.T) {
		r, e := workloadRig(t)
		e.installed = errOf("no runtime")
		_, err := activate(r, "music", false)
		if oe := opErrOf(t, err); oe.Code != CodeEngineMissing {
			t.Fatalf("err = %v", err)
		}
	})
}

type errOf string

func (e errOf) Error() string { return string(e) }

func TestWorkloadSurvivalWithForceKeepsTheSurvivalModels(t *testing.T) {
	r, _ := workloadRig(t, orSurvival(), orMax(2))
	r.load("m-c")
	res, err := activate(r, "chat", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unloaded) != 0 {
		t.Errorf("a survival model was unloaded: %+v", res)
	}
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a", "m-c"})
	if v := r.o.Workloads(bg); v.Active != "chat" {
		t.Errorf("view active = %q (survival models beside it do not count)", v.Active)
	}
}

func TestWorkloadBusyAnswers409(t *testing.T) {
	r, _ := workloadRig(t)
	hold, err := r.o.Lock().TryAcquire(HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	_, err = activate(r, "chat", false)
	if oe := opErrOf(t, err); oe.Code != CodeBusy || oe.Details["op_id"] != hold.OpID {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkloadInTheBackground(t *testing.T) {
	r, _ := workloadRig(t)
	done := make(chan error, 1)
	res, err := r.o.ActivateWorkload(bg, WorkloadRequest{Name: "chat", Actor: sysActor, OnDone: func(_ WorkloadResult, err error) { done <- err }})
	if err != nil || !res.Accepted || res.OpID == "" {
		t.Fatalf("activate = %+v, %v", res, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-a"})
}

func TestWorkloadsBodyShape(t *testing.T) {
	b, err := json.Marshal(WorkloadsBody(WorkloadsView{Workloads: []WorkloadView{{Name: "chat", Label: lbl("Chat", "Charla"), Models: []string{"m-a"}, Active: true, Fits: true}}, Active: "chat"}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"workloads":[{"name":"chat","label":{"en":"Chat","es":"Charla"},"models":["m-a"],"active":true,"fits":true,"missing":[]}],"active":"chat"}`
	if string(b) != want {
		t.Errorf("body:\n%s\nwant\n%s", b, want)
	}
	b, _ = json.Marshal(WorkloadsBody(WorkloadsView{}))
	if string(b) != `{"workloads":[],"active":null}` {
		t.Errorf("empty body: %s", b)
	}
}

func TestResolveWorkloads(t *testing.T) {
	cat := &catalog.Catalog{Models: orModels()}
	prof := &catalog.Profile{Workloads: map[string]catalog.Workload{
		"chat":   {Label: lbl("Chat", "Charla"), Models: []string{"m-a"}},
		"coding": {Label: lbl("Coding", "Programación"), Models: []string{"m-b"}},
	}}
	node := map[string]config.Workload{
		"chat":  {Label: config.WorkloadLabel{EN: "Talk", ES: "Hablar"}, Models: []string{"m-c", "m-c"}},
		"music": {Label: config.WorkloadLabel{EN: "Music", ES: "Música"}, Models: []string{"no-such"}},
	}
	got, dropped := ResolveWorkloads(cat, prof, node)
	if len(got) != 2 || got[0].Name != "chat" || got[0].Label.EN != "Talk" || strings.Join(got[0].Models, ",") != "m-c" || got[1].Name != "coding" {
		t.Errorf("resolved: %+v", got)
	}
	if len(dropped) != 1 || dropped[0].Name != "music" || dropped[0].Unknown[0] != "no-such" {
		t.Errorf("dropped: %+v", dropped)
	}
}
