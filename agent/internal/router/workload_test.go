package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
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

// The music engine is a process of its own, so the hardware sampler counts its graphics memory in
// G_others. A workload replaces whatever is loaded: with music loaded, the other workloads must still
// fit, and switching back must work (#566).
func TestWorkloadsFitWhileTheMusicEngineHoldsTheCard(t *testing.T) {
	r, e := workloadRig(t, func(c *orCfg) { c.env.GOthersMiB += 6000 })
	e.mu.Lock()
	e.resident["music-l"], e.vram = true, 6000
	e.mu.Unlock()
	for _, w := range r.o.Workloads(bg).Workloads {
		if (w.Name == "too-big") == w.Fits {
			t.Errorf("%s fits = %v while music is loaded", w.Name, w.Fits)
		}
	}
	if _, err := activate(r, "coding", false); err != nil {
		t.Fatalf("switching back from music: %v", err)
	}
	eqStrings(t, "engine calls", e.log(), []string{"stop music-l"})
	eqStrings(t, "llama loaded", r.loadedNow(), []string{"m-b"})
}

// unifiedSwitchRig is ai-n1 after #604: a unified-memory node (GTT 100 GiB, 124 GB RAM) whose
// samples report RAM available net of what the loaded models hold. "fast" is loaded and holds about
// 29 GB, so the sample says 95,000 MiB are free.
func unifiedSwitchRig(t *testing.T) *orRig {
	t.Helper()
	r := newOrRig(t, func(c *orCfg) {
		c.env.Unified = true
		c.env.GTotalMiB = 102400
		c.env.GOthersMiB = 0
		c.env.RTotalMiB = 124123
		c.env.RAvailMiB = 95000
		c.env.ModelsMax = 3
		c.profile.Router.ModelsMax = 3
		c.extra = []catalog.Model{
			llamaModel("fast", nil, 27973, 32768, nil, nil),
			llamaModel("smart", nil, 78746, 112640, nil, nil),
		}
		c.profile.Include.Models = append(c.profile.Include.Models, "fast", "smart")
		c.workloads = []Workload{
			{Name: "browser", Label: lbl("Browser agent", "Agente de navegador"), Models: []string{"fast"}},
			{Name: "browser-smart", Label: lbl("Browser agent (smart)", "Agente de navegador (inteligente)"), Models: []string{"smart"}},
		}
	})
	r.f.Add(fake.Model{ID: "fast"})
	r.f.Add(fake.Model{ID: "smart"})
	r.load("fast")
	return r
}

// #610: the RAM a loaded model holds is room for the workload that replaces it.
func TestWorkloadFitCreditsTheRAMOfTheModelsItUnloads(t *testing.T) {
	r := unifiedSwitchRig(t)
	for _, w := range r.o.Workloads(bg).Workloads {
		if !w.Fits {
			t.Errorf("%s fits = false with fast loaded; the switch unloads fast, so its RAM is room", w.Name)
		}
	}
	res, err := activate(r, "browser-smart", false)
	if err != nil {
		t.Fatalf("switching fast -> smart: %v", err)
	}
	eqStrings(t, "unloaded", res.Unloaded, []string{"fast"})
	if r.f.Status("smart") != fake.Loaded || r.f.Status("fast") == fake.Loaded {
		t.Errorf("router: smart=%v fast=%v, want smart loaded and fast not", r.f.Status("smart"), r.f.Status("fast"))
	}
}

// #610: with the big model loaded, its own workload must not read "does not fit" because the RAM
// it holds is missing from the sample (ai-n1: smart loaded, about 12 GB left).
func TestLoadedBigWorkloadStillFits(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) {
		c.env.Unified, c.env.GTotalMiB, c.env.RTotalMiB, c.env.RAvailMiB = true, 102400, 124123, 12000
		c.extra = []catalog.Model{llamaModel("smart", nil, 78746, 112640, nil, nil)}
		c.profile.Include.Models = append(c.profile.Include.Models, "smart")
		c.workloads = []Workload{{Name: "browser-smart", Label: lbl("Smart", "Inteligente"), Models: []string{"smart"}}}
	})
	r.f.Add(fake.Model{ID: "smart"})
	r.load("smart")
	for _, w := range r.o.Workloads(bg).Workloads {
		if w.Name == "browser-smart" && !w.Fits {
			t.Error("browser-smart fits = false while its own model is the one holding the RAM")
		}
	}
}

// The credit is only what is loaded: with nothing loaded and too little RAM, a big set still does not fit.
func TestWorkloadFitStillRefusesWhenTheRAMIsReallyShort(t *testing.T) {
	r := newOrRig(t, func(c *orCfg) {
		c.env.Unified, c.env.GTotalMiB, c.env.RTotalMiB, c.env.RAvailMiB = true, 102400, 124123, 60000
		c.extra = []catalog.Model{llamaModel("smart", nil, 78746, 112640, nil, nil)}
		c.profile.Include.Models = append(c.profile.Include.Models, "smart")
		c.workloads = []Workload{{Name: "browser-smart", Label: lbl("Smart", "Inteligente"), Models: []string{"smart"}}}
	})
	r.f.Add(fake.Model{ID: "smart"})
	if _, err := activate(r, "browser-smart", false); err == nil || !strings.Contains(err.Error(), string(CodeWorkloadNoFit)) {
		t.Fatalf("err = %v, want %s", err, CodeWorkloadNoFit)
	}
}
