package statusapi_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGetProfile(t *testing.T) {
	r := newRig(t, nil)
	rec := r.get("/profile", "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[statusapi.ProfileBody](t, rec)
	if body.Profile != "small-8gb" || body.DisplayName == "" {
		t.Errorf("profile = %q %q", body.Profile, body.DisplayName)
	}
	wantID, wantReasons := r.cat.Suggest(snapAiN3())
	if body.Suggested == nil || *body.Suggested != wantID || strings.Join(body.MatchReasons, "|") != strings.Join(wantReasons, "|") {
		t.Errorf("suggested = %v %v, want %s %v", body.Suggested, body.MatchReasons, wantID, wantReasons)
	}
	plan, err := r.cat.EffectivePlan("small-8gb", catalog.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Effective.Models, ",") != strings.Join(plan.Models, ",") || body.Effective.PlannedBytes != plan.PlannedBytes || body.Effective.BudgetBytes != plan.BudgetBytes {
		t.Errorf("effective = %+v, want %+v", body.Effective, plan)
	}
	if body.Overrides.AddModels == nil || body.Overrides.BudgetBytes != nil || body.Effective.Skipped == nil {
		t.Errorf("overrides/skipped must be empty lists, not null: %+v", body)
	}
	wantError(t, r.get("/profile", ""), 401, api.CodeUnauthenticated)
}

// TestGetProfileUnknown: a node.yaml naming a profile the catalog lacks answers LAI-CAT-003 and still
// says what would fit (agent.md section 3.8.30).
func TestGetProfileUnknown(t *testing.T) {
	r := newRig(t, nil)
	writeFile(t, r.nodePath, strings.Replace(nodeYAML, "profile: small-8gb", "profile: no-such-profile", 1))
	r.cfg.ReloadNow()
	e := wantError(t, r.get("/profile", "op"), 422, catalog.CodeUnknownProfile)
	if e.Error.Details["profile"] != "no-such-profile" || e.Error.Details["suggested"] == nil {
		t.Errorf("details = %v", e.Error.Details)
	}
}

func TestGetProfileUnknownReference(t *testing.T) {
	r := newRig(t, nil)
	writeFile(t, r.nodePath, strings.Replace(nodeYAML, "add_models: []", "add_models: [model-missing]", 1))
	r.cfg.ReloadNow()
	e := wantError(t, r.get("/profile", "op"), 422, catalog.CodeUnknownRef)
	unknown, _ := e.Error.Details["unknown"].([]any)
	if len(unknown) != 1 {
		t.Fatalf("details = %v", e.Error.Details)
	}
}

func TestPutProfile(t *testing.T) {
	r := newRig(t, nil)
	rec := r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb", "profile_overrides": null}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[statusapi.ProfileBody](t, rec)
	if body.Profile != "cuda-12gb" || len(body.Warnings) != 0 {
		t.Errorf("body = %+v", body)
	}

	// node.yaml: the profile changed, the comments stayed, the previous file is the .bak.
	got := readFile(t, r.nodePath)
	if !strings.Contains(got, "profile: cuda-12gb # keep this comment") || !strings.HasPrefix(got, "# The node file of this test.") {
		t.Errorf("node.yaml lost its comments or its profile:\n%s", got)
	}
	if bak := readFile(t, r.nodePath+".bak"); bak != nodeYAML {
		t.Errorf("node.yaml.bak is not the previous file:\n%s", bak)
	}
	if r.cfg.Get().Profile != "cuda-12gb" {
		t.Errorf("the store still has %q", r.cfg.Get().Profile)
	}
	if r.regen.count() != 1 {
		t.Errorf("regenerations = %d, want 1", r.regen.count())
	}
	// GET now shows the same body.
	again := decode[statusapi.ProfileBody](t, r.get("/profile", "op"))
	if again.Profile != "cuda-12gb" || again.DisplayName != body.DisplayName {
		t.Errorf("GET after PUT = %+v", again)
	}

	evs := r.audit.events()
	if len(evs) != 1 || evs[0].Action != audit.ActionProfileSet || evs[0].Result != audit.ResultOK {
		t.Fatalf("audit = %+v", evs)
	}
	if evs[0].Target["profile"] != "cuda-12gb" || evs[0].Target["previous"] != "small-8gb" || evs[0].Actor.Kind != audit.KindAdmin || evs[0].ReqID == "" {
		t.Errorf("audit event = %+v", evs[0])
	}
}

func TestPutProfileOverrides(t *testing.T) {
	r := newRig(t, nil)
	rec := r.send("PUT", "/profile", "admin", `{"profile_overrides": {"add_models": ["model-beta-27b-q4"], "remove_content": ["wiki-alpha-en"], "budget_bytes": 90000000000}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode[statusapi.ProfileBody](t, rec)
	if body.Profile != "small-8gb" || body.Overrides.BudgetBytes == nil || *body.Overrides.BudgetBytes != 90000000000 || body.Effective.BudgetBytes != 90000000000 {
		t.Errorf("body = %+v", body)
	}
	cfg := r.cfg.Get()
	if len(cfg.ProfileOverrides.AddModels) != 1 || cfg.ProfileOverrides.AddModels[0] != "model-beta-27b-q4" || cfg.ProfileOverrides.BudgetBytes == nil {
		t.Errorf("overrides in node.yaml = %+v", cfg.ProfileOverrides)
	}
	if !strings.Contains(readFile(t, r.nodePath), "# keep this comment") {
		t.Error("the comment is gone")
	}

	// A body without profile_overrides keeps them; one with an empty object clears them.
	r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb"}`)
	if got := r.cfg.Get().ProfileOverrides; len(got.AddModels) != 1 || got.BudgetBytes == nil {
		t.Errorf("overrides after a profile-only PUT = %+v", got)
	}
	r.send("PUT", "/profile", "admin", `{"profile_overrides": {}}`)
	if got := r.cfg.Get().ProfileOverrides; len(got.AddModels) != 0 || len(got.RemoveContent) != 0 || got.BudgetBytes != nil {
		t.Errorf("overrides after {} = %+v", got)
	}
}

func TestPutProfileNoChange(t *testing.T) {
	r := newRig(t, nil)
	for _, body := range []string{`{}`, `{"profile": null, "profile_overrides": null}`, `{"profile": "small-8gb"}`} {
		rec := r.send("PUT", "/profile", "admin", body)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", body, rec.Code, rec.Body.String())
		}
		if got := readFile(t, r.nodePath); got != nodeYAML {
			t.Errorf("%s rewrote node.yaml:\n%s", body, got)
		}
	}
}

// TestPutProfileRefused: a request the catalog or the rules refuse writes nothing and reloads nothing.
func TestPutProfileRefused(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		code       string
		field      string
	}{
		{"unknown profile", `{"profile": "no-such-profile"}`, 422, catalog.CodeUnknownProfile, ""},
		{"unknown add_models id", `{"profile_overrides": {"add_models": ["model-missing"]}}`, 422, catalog.CodeUnknownRef, ""},
		{"unknown content id", `{"profile": "cuda-12gb", "profile_overrides": {"remove_content": ["content-missing"]}}`, 422, catalog.CodeUnknownRef, ""},
		{"empty profile", `{"profile": ""}`, 400, api.CodeInvalidRequest, "profile"},
		{"zero budget", `{"profile_overrides": {"budget_bytes": 0}}`, 400, api.CodeInvalidRequest, "profile_overrides.budget_bytes"},
		{"negative budget", `{"profile_overrides": {"budget_bytes": -5}}`, 400, api.CodeInvalidRequest, "profile_overrides.budget_bytes"},
		{"unknown field", `{"profile": "cuda-12gb", "bogus": 1}`, 400, api.CodeInvalidRequest, "bogus"},
		{"unknown override field", `{"profile_overrides": {"add_model": []}}`, 400, api.CodeInvalidRequest, "add_model"},
		{"wrong type", `{"profile": 7}`, 400, api.CodeInvalidRequest, "profile"},
		{"malformed", `{`, 400, api.CodeInvalidRequest, ""},
		{"empty body", ``, 400, api.CodeInvalidRequest, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			rec := r.send("PUT", "/profile", "admin", c.body)
			e := wantError(t, rec, c.status, c.code)
			if c.field != "" && e.Error.Details["field"] != c.field {
				t.Errorf("details = %v", e.Error.Details)
			}
			if got := readFile(t, r.nodePath); got != nodeYAML {
				t.Errorf("node.yaml was rewritten:\n%s", got)
			}
			if _, err := os.Stat(r.nodePath + ".bak"); err == nil {
				t.Error("a .bak was written")
			}
			if r.regen.count() != 0 {
				t.Error("the router was regenerated")
			}
			evs := r.audit.events()
			if c.status == 422 {
				if len(evs) != 1 || evs[0].Action != audit.ActionProfileSet || evs[0].Result != audit.ResultFail || evs[0].Code != c.code {
					t.Errorf("audit = %+v", evs)
				}
			}
		})
	}
}

func TestPutProfileAuth(t *testing.T) {
	r := newRig(t, nil)
	body := `{"profile": "cuda-12gb"}`
	// An operator without the PIN grant is refused by the server (agent.md section 3.9.1 step 7).
	wantError(t, r.send("PUT", "/profile", "op", body), 403, doctor.SEC002)
	wantError(t, r.send("PUT", "/profile", "", body), 401, doctor.SEC001)
	wantError(t, r.send("PUT", "/profile", "portal", body), 401, doctor.SVC110)
	wantError(t, do(t, r.h, call{method: "PUT", target: "/profile", principal: "admin", body: body, header: map[string]string{"Content-Type": "text/plain"}}), 415, api.CodeInvalidRequest)
	if got := readFile(t, r.nodePath); got != nodeYAML {
		t.Errorf("node.yaml was rewritten:\n%s", got)
	}
	if r.regen.count() != 0 || len(r.audit.events()) != 0 {
		t.Error("a refused request reloaded or audited something")
	}
}

// TestPutProfileReloadProblems: the change is written whatever the router does; a problem there is a
// warning in the body, never a failure.
func TestPutProfileReloadProblems(t *testing.T) {
	t.Run("warnings of the generator", func(t *testing.T) {
		r := newRig(t, nil)
		r.regen.res = statusapi.RegenResult{Written: true, Warnings: []statusapi.WarningBlock{{Code: doctor.LLM008, Message: "A model file is missing (model-beta-27b-q4)."}}}
		rec := r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb"}`)
		body := decode[statusapi.ProfileBody](t, rec)
		if rec.Code != 200 || len(body.Warnings) != 1 || body.Warnings[0].Code != doctor.LLM008 {
			t.Fatalf("status %d body %+v", rec.Code, body)
		}
	})
	t.Run("generator error", func(t *testing.T) {
		r := newRig(t, nil)
		r.regen.err = errors.New("models.ini could not be written")
		rec := r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb"}`)
		body := decode[statusapi.ProfileBody](t, rec)
		if rec.Code != 200 || len(body.Warnings) != 1 || body.Warnings[0].Code != api.CodeInternal {
			t.Fatalf("status %d body %+v", rec.Code, body)
		}
		if strings.Contains(rec.Body.String(), "models.ini could not") {
			t.Error("the error text of the generator reached the client")
		}
		if r.cfg.Get().Profile != "cuda-12gb" {
			t.Error("node.yaml was not written")
		}
	})
	t.Run("no regenerator", func(t *testing.T) {
		r := newRig(t, func(_ *rig, d *statusapi.Deps) { d.Regenerator = nil })
		if rec := r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb"}`); rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestPutProfileInvalidNodeYAML: an edit node.yaml's own validation refuses is a 400 that writes nothing.
func TestPutProfileInvalidNodeYAML(t *testing.T) {
	r := newRig(t, func(r *rig, d *statusapi.Deps) { d.Config = failingConfig{r.cfg} })
	rec := r.send("PUT", "/profile", "admin", `{"profile": "cuda-12gb"}`)
	e := wantError(t, rec, 400, api.CodeInvalidRequest)
	if e.Error.Details["reason"] != "node_yaml_invalid" {
		t.Errorf("details = %v", e.Error.Details)
	}
}
