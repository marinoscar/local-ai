package cloud

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// Integration scenario 4 of agent.md section 9.2, at the level this package can prove: a cloud
// model.load job is claimed from the fake cloud, its executor loads the model on a fake router (which
// is what changes the node's status view), the result is posted with the claim token and conforms to
// the cloud's schema (the fake refuses a result that does not), and the state push that follows the
// command shows the model loaded. The same scenario over the daemon's real router, /status and
// listeners is AGENT-20's (internal/app).
func TestScenarioCloudModelLoadIsReflectedInTheStatePush(t *testing.T) {
	const id = "qwen3.5-9b-q4km"
	loaded := make(chan struct{})
	var cr *claimRig
	h := newTestHandler(TypeModelLoad, func(ctx context.Context, job Job, params any) (Outcome, error) {
		// The fake router loads the model: the status and the model view now show it.
		cr.models.mu.Lock()
		cr.models.view = statusapi.ModelsView{Default: id, ModelsMax: 1, Rows: []statusapi.ModelRow{
			{ID: id, Present: true, Verified: true, Loadable: true, Status: "loaded", Loaded: true, Default: true, Roles: []string{"chat"}, InProfile: true},
		}}
		cr.models.mu.Unlock()
		cr.status.edit(func(s *statusapiStatus) { s.Models.Loaded = []string{id}; s.Models.Default = str(id) })
		close(loaded)
		return Outcome{Result: map[string]any{
			"model_id": id, "status": "loaded", "load_ms": 4200, "unloaded": []string{}, "vram_used_mib": 6791, "is_default": true,
		}}, nil
	})
	// The strict decoder of the type, as internal/cloud/jobs has it.
	h.decode = func(raw json.RawMessage) (any, error) {
		var p struct {
			ModelID      string `json:"model_id"`
			Wait         bool   `json:"wait"`
			UnloadOthers bool   `json:"unload_others"`
			SetDefault   bool   `json:"set_default"`
		}
		return p, DecodeParams(raw, &p)
	}
	cr = newClaimRig(t, []*testHandler{h})
	cr.queue("job-load", "localai.model.load", map[string]any{"model_id": id, "wait": true, "unload_others": true, "set_default": false})
	cr.start()

	if err := cr.srv.WaitJob(cr.ctx, "job-load", fake.JobSucceeded); err != nil {
		rec, _ := cr.srv.Job("job-load")
		t.Fatalf("%v: job = %+v", err, rec)
	}
	<-loaded
	cr.waitDone(cr.claimer.jobDone("job-load"))

	rec, _ := cr.srv.Job("job-load")
	var res map[string]any
	if err := json.Unmarshal(rec.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res["model_id"] != id || res["status"] != "loaded" || res["is_default"] != true {
		t.Errorf("result = %s", rec.Result)
	}
	body := cr.srv.RequestsFor(fake.RouteResult)[0].Body
	var posted struct {
		Type       string `json:"type"`
		ClaimToken string `json:"claimToken"`
	}
	if err := json.Unmarshal(body, &posted); err != nil || posted.Type != "localai.model.load" || posted.ClaimToken != "tok-job-load" {
		t.Errorf("posted = %s (%v)", body, err)
	}

	// The push after the command (step 7) carries the loaded model; waitState-style polling, not a sleep:
	// the fake wakes WaitCount on every request.
	var snap StateSnapshot
	for n := 1; ; n++ {
		cr.waitCount(fake.RouteState, n)
		states := cr.srv.States()
		if len(states) < n {
			continue
		}
		last := states[len(states)-1]
		if err := json.Unmarshal(last, &snap); err != nil {
			t.Fatal(err)
		}
		validate(t, last)
		if scenarioModelLoaded(snap, id) {
			break
		}
		if n > 20 {
			t.Fatalf("no snapshot shows %s loaded: %s", id, last)
		}
	}
}

func scenarioModelLoaded(s StateSnapshot, id string) bool {
	for _, m := range s.Models {
		if m.ID == id && m.Status == "loaded" && m.Default {
			return true
		}
	}
	return false
}
