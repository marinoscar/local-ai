package modelsapi_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

func TestDownloadQueuesASyncRunForOneModel(t *testing.T) {
	r := newRig(t, nil)
	rec := r.send("POST", "/models/"+idBeta+"/download", "op", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[modelsapi.DownloadAccepted](t, rec.Body.Bytes())
	if got.ID != idBeta || got.SyncRunID != "run-20261102T150405Z-a1f3" || !got.Queued {
		t.Errorf("body = %+v", got)
	}
	calls := r.sync.downloadCalls()
	if len(calls) != 1 || calls[0].Model.ID != idBeta || len(calls[0].Roots) != 0 || calls[0].AllowOfflineMode {
		t.Fatalf("engine calls = %+v", calls)
	}
	if calls[0].Actor.Kind != audit.KindOperator || calls[0].Actor.Via != audit.ViaBearer {
		t.Errorf("actor = %+v", calls[0].Actor)
	}
}

func TestDownloadPassesTheRootsAndTheOfflineFlagOn(t *testing.T) {
	r := newRig(t, nil)
	r.mode.set(state.ModeOffline)
	rec := r.send("POST", "/models/"+idAlpha+"/download", "op", `{"roots": ["`+rootDrive+`", "ark-3f9c0a12"], "allow_offline_mode": true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	calls := r.sync.downloadCalls()
	if len(calls) != 1 || len(calls[0].Roots) != 2 || calls[0].Roots[0] != rootDrive || calls[0].Roots[1] != "ark-3f9c0a12" || !calls[0].AllowOfflineMode {
		t.Errorf("engine calls = %+v", calls)
	}
}

func TestDownloadModeRules(t *testing.T) {
	cases := []struct {
		name   string
		mode   state.Mode
		body   string
		status int
		code   string
		calls  int
	}{
		{"online", state.ModeOnline, `{}`, 202, "", 1},
		{"offline refused", state.ModeOffline, `{}`, 409, doctor.AGT023, 0},
		{"offline allowed", state.ModeOffline, `{"allow_offline_mode": true}`, 202, "", 1},
		{"survival refused", state.ModeSurvival, `{}`, 409, doctor.SYN010, 0},
		{"survival refused even when allowed", state.ModeSurvival, `{"allow_offline_mode": true}`, 409, doctor.SYN010, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.mode.set(c.mode)
			rec := r.send("POST", "/models/"+idAlpha+"/download", "op", c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if c.code != "" {
				e := envelope(t, rec.Body.Bytes())
				if e.Error.Code != c.code || e.Error.Details["mode"] != string(c.mode) {
					t.Errorf("error = %+v", e.Error)
				}
				if c.code == doctor.AGT023 && e.Error.Details["requires"] != "allow_offline_mode" {
					t.Errorf("details = %v", e.Error.Details)
				}
			}
			if n := len(r.sync.downloadCalls()); n != c.calls {
				t.Errorf("%d engine calls, want %d", n, c.calls)
			}
		})
	}
}

func TestDownloadWithoutAModeEngineIsOnline(t *testing.T) {
	r := newRig(t, func(_ *rig, d *modelsapi.Deps) { d.Mode = nil })
	if rec := r.send("POST", "/models/"+idAlpha+"/download", "op", ``); rec.Code != http.StatusAccepted {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestDownloadRefusals(t *testing.T) {
	cases := []struct {
		name, target, principal, body string
		status                        int
		code                          string
		field                         string
	}{
		{"unknown model", "/models/no-such-model/download", "op", `{}`, 404, doctor.AGT021, ""},
		{"anonymous", "/models/" + idAlpha + "/download", "", `{}`, 401, doctor.SEC001, ""},
		{"portal session", "/models/" + idAlpha + "/download", "portal", `{}`, 401, doctor.SVC110, ""},
		{"unknown field", "/models/" + idAlpha + "/download", "op", `{"root": "x"}`, 400, doctor.AGT020, "root"},
		{"roots of the wrong type", "/models/" + idAlpha + "/download", "op", `{"roots": "x"}`, 400, doctor.AGT020, "roots"},
		{"empty root", "/models/" + idAlpha + "/download", "op", `{"roots": [""]}`, 400, doctor.AGT020, "roots"},
		{"too many roots", "/models/" + idAlpha + "/download", "op", `{"roots": ["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]}`, 400, doctor.AGT020, "roots"},
		{"not json", "/models/" + idAlpha + "/download", "op", `{`, 400, doctor.AGT020, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			rec := r.send("POST", c.target, c.principal, c.body)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if c.field != "" && e.Error.Details["field"] != c.field {
				t.Errorf("details = %v", e.Error.Details)
			}
			if len(r.sync.downloadCalls()) != 0 {
				t.Error("the engine was called for a refused request")
			}
		})
	}
}

func TestDownloadWithoutAContentTypeIs415(t *testing.T) {
	r := newRig(t, nil)
	rec := do(t, r.h, call{method: "POST", target: "/models/" + idAlpha + "/download", principal: "op", header: map[string]string{"Content-Type": "text/plain"}})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status %d", rec.Code)
	}
}

func TestDownloadEngineErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
		want   any
	}{
		{"a run is active", &modelsapi.RunActiveError{RunID: "run-20261102T031500Z-a1f3"}, 409, doctor.SYN005, "run_id", "run-20261102T031500Z-a1f3"},
		{"unknown root", &modelsapi.UnknownRootError{Root: "/mnt/nowhere"}, 400, doctor.AGT020, "value", "/mnt/nowhere"},
		{"no writable root", modelsapi.ErrNoWritableRoot, 422, doctor.ARK001, "", nil},
		{"a bug", errors.New("engine exploded"), 500, doctor.AGT099, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.sync.dlErr = c.err
			rec := r.send("POST", "/models/"+idAlpha+"/download", "op", `{}`)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if c.detail != "" && e.Error.Details[c.detail] != c.want {
				t.Errorf("details = %v", e.Error.Details)
			}
			if c.status == 500 && strings.Contains(rec.Body.String(), "exploded") {
				t.Errorf("the text of a bug reached the client: %s", rec.Body)
			}
		})
	}
}

func TestDownloadServiceMethodForTheCloudExecutor(t *testing.T) {
	r := newRig(t, nil)
	res, err := r.svc.Download(t.Context(), modelsapi.DownloadRequest{ID: idAlpha, Actor: audit.Cloud("job-1")})
	if err != nil || res.SyncRunID == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if calls := r.sync.downloadCalls(); len(calls) != 1 || calls[0].Actor.Kind != audit.KindCloud || calls[0].Actor.JobID != "job-1" {
		t.Errorf("engine calls = %+v", calls)
	}
}
