package modelsapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

var hexKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

func fp(key string) string { return key[:4] + "…" + key[len(key)-4:] }

// ---- GET /key ----------------------------------------------------------------------------------

func TestShowKeyReturnsTheKeyAuditedAndNotCached(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	rec := r.get("/key", "op")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	body := decode[modelsapi.KeyBody](t, rec.Body.Bytes())
	if body.Key != r.oldKey || !hexKey.MatchString(body.Key) {
		t.Errorf("the key in the body is not the key file's")
	}
	if body.Fingerprint != fp(r.oldKey) {
		t.Errorf("fingerprint = %q", body.Fingerprint)
	}

	// Audit key.show with the principal, and with the fingerprint only.
	ev := r.audit.only(t, audit.ActionKeyShow)
	if ev.Result != audit.ResultOK || ev.Actor.Kind != audit.KindOperator || ev.Actor.Via != audit.ViaBearer || ev.Target["fingerprint"] != fp(r.oldKey) {
		t.Errorf("audit = %+v", ev)
	}
	if strings.Contains(r.everything(), r.oldKey) {
		t.Error("the key reached the log, the audit or the state")
	}
}

func TestShowKeyBaseURLsModelsAndSnippets(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.has(rootMain, "model-gamma-vl-q4")
	rec := r.get("/key", "op")
	body := decode[modelsapi.KeyBody](t, rec.Body.Bytes())

	if body.BaseURLs.Local != "http://127.0.0.1:8080/v1" {
		t.Errorf("local = %q", body.BaseURLs.Local)
	}
	if body.BaseURLs.Tailnet == nil || *body.BaseURLs.Tailnet != "https://hp-linux.example.test/v1" {
		t.Errorf("tailnet = %v", body.BaseURLs.Tailnet)
	}
	if body.BaseURLs.Public == nil || *body.BaseURLs.Public != "https://ai-n3.example.test/v1" {
		t.Errorf("public = %v", body.BaseURLs.Public)
	}
	if len(body.Models) != 2 || body.Models[0] != idAlpha || body.Models[1] != "model-gamma-vl-q4" {
		t.Errorf("models = %v", body.Models)
	}

	sn := body.Snippets
	wantCurl := `curl -s http://127.0.0.1:8080/v1/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"model":"` +
		idAlpha + `","messages":[{"role":"user","content":"Hello"}]}'`
	if sn.Curl != wantCurl {
		t.Errorf("curl = %s", sn.Curl)
	}
	if sn.Env != "OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>" {
		t.Errorf("env = %q", sn.Env)
	}
	if sn.OpenAIPython != "from openai import OpenAI\nclient = OpenAI(base_url=\"http://127.0.0.1:8080/v1\", api_key=KEY)" {
		t.Errorf("python = %q", sn.OpenAIPython)
	}
	prov := sn.OpenCode.Provider["local-llama"]
	if prov.NPM != "@ai-sdk/openai-compatible" || prov.Options.BaseURL != "http://127.0.0.1:8080/v1" || prov.Options.APIKey != "{file:~/.config/opencode/llama-api-key}" {
		t.Errorf("opencode provider = %+v", prov)
	}
	// limit.context is llama.ctx, limit.output client_hints.max_output_tokens (4096 without hints).
	if l := prov.Models[idAlpha].Limit; l.Context != 32768 || l.Output != 8192 {
		t.Errorf("alpha limit = %+v", l)
	}
	if l := prov.Models["model-gamma-vl-q4"].Limit; l.Context != 8192 || l.Output != 4096 {
		t.Errorf("gamma limit = %+v", l)
	}

	// Snippets carry placeholders only: not one of them holds the key, in any language.
	raw, _ := json.Marshal(body.Snippets)
	if strings.Contains(string(raw), r.oldKey) {
		t.Error("a snippet contains the key")
	}
}

func TestShowKeyOnANodeWithNoTailnetOrPublicName(t *testing.T) {
	r := newRig(t, func(rg *rig, d *modelsapi.Deps) {
		d.Tailnet = fakeTailnet{} // pattern none, or tailscale unavailable
		rg.cfg.cfg.Node.PublicHost = nil
		rg.cfg.cfg.Ports.Llama = 9090
	})
	rec := r.get("/key", "op")
	var raw struct {
		BaseURLs map[string]any `json:"base_urls"`
		Models   []string       `json:"models"`
		Snippets struct {
			Curl string `json:"curl"`
		} `json:"snippets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if v, ok := raw.BaseURLs["tailnet"]; !ok || v != nil {
		t.Errorf("tailnet = %v (must be present and null)", v)
	}
	if v, ok := raw.BaseURLs["public"]; !ok || v != nil {
		t.Errorf("public = %v (must be present and null)", v)
	}
	if raw.BaseURLs["local"] != "http://127.0.0.1:9090/v1" {
		t.Errorf("local = %v", raw.BaseURLs["local"])
	}
	// No model would load: the models list is empty and the snippet names a placeholder.
	if raw.Models == nil || len(raw.Models) != 0 || !strings.Contains(raw.Snippets.Curl, `"model":"<model>"`) {
		t.Errorf("models %v, curl %s", raw.Models, raw.Snippets.Curl)
	}
}

func TestShowKeyWithoutATailnetViewAtAll(t *testing.T) {
	r := newRig(t, func(_ *rig, d *modelsapi.Deps) { d.Tailnet = nil })
	body := decode[modelsapi.KeyBody](t, r.get("/key", "op").Body.Bytes())
	if body.BaseURLs.Tailnet != nil {
		t.Errorf("tailnet = %v", *body.BaseURLs.Tailnet)
	}
}

func TestShowKeyUsesTheDefaultModelInTheSnippet(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, "model-gamma-vl-q4")
	r.has(rootMain, idAlpha)
	// The default (alpha) is second in the catalog order of loadable models only when gamma comes
	// first; here alpha is both first and the default, so move the default to gamma.
	def := "model-gamma-vl-q4"
	if err := r.st.MutateSync(func(s *state.State) error { s.Models.Default = &def; return nil }); err != nil {
		t.Fatal(err)
	}
	body := decode[modelsapi.KeyBody](t, r.get("/key", "op").Body.Bytes())
	if !strings.Contains(body.Snippets.Curl, `"model":"model-gamma-vl-q4"`) {
		t.Errorf("curl = %s", body.Snippets.Curl)
	}
}

func TestShowKeyAuth(t *testing.T) {
	for _, c := range []struct {
		principal string
		status    int
	}{{"", 401}, {"portal", 401}, {"op", 200}, {"admin", 200}} {
		r := newRig(t, nil)
		rec := r.get("/key", c.principal)
		if rec.Code != c.status {
			t.Errorf("%q: status %d", c.principal, rec.Code)
		}
		if c.status != 200 {
			if strings.Contains(rec.Body.String(), r.oldKey) || len(r.audit.events()) != 0 {
				t.Errorf("%q: a refused request left a trace or leaked", c.principal)
			}
		}
	}
}

func TestShowKeyWhenTheKeyFileIsUnusable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, path string)
		reason string
	}{
		{"missing", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}, "unreadable"},
		{"too short", func(t *testing.T, p string) { writeKeyFile(t, p, "abcdef") }, "malformed"},
		{"not hex", func(t *testing.T, p string) { writeKeyFile(t, p, strings.Repeat("z", 64)) }, "malformed"},
		{"upper case", func(t *testing.T, p string) { writeKeyFile(t, p, strings.ToUpper(fakeKey(t, 0xAB))) }, "malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			c.mutate(t, r.keyPath)
			rec := r.get("/key", "op")
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != http.StatusInternalServerError || e.Error.Code != doctor.SEC010 || e.Error.Details["reason"] != c.reason {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if ev := r.audit.only(t, audit.ActionKeyShow); ev.Result != audit.ResultFail || ev.Code != doctor.SEC010 {
				t.Errorf("audit = %+v", ev)
			}
		})
	}
}

func writeKeyFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestShowKeyServiceMethodForTheCloudExecutor(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.svc.ShowKey(t.Context(), audit.Cloud("job-3")); err != nil {
		t.Fatal(err)
	}
	if ev := r.audit.only(t, audit.ActionKeyShow); ev.Actor.Kind != audit.KindCloud || ev.Actor.JobID != "job-3" {
		t.Errorf("audit = %+v", ev)
	}
}

// ---- POST /key/rotate --------------------------------------------------------------------------

func TestRotateKeyRestartsLlamaAndReportsEverything(t *testing.T) {
	r := newRig(t, nil)
	newKey := expectedKey(t, 0)
	rec := r.send("POST", "/key/rotate", "admin", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[modelsapi.KeyRotated](t, rec.Body.Bytes())

	// The new key: 64 hex digits, different, in the file, mode 0600 and owned by root.
	if disk := r.readKey(); disk != newKey || !hexKey.MatchString(disk) || disk == r.oldKey {
		t.Errorf("key file holds the wrong key")
	}
	fi, err := os.Stat(r.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if owners := r.fs.ownersSeen(); len(owners) != 1 || owners[0] != [2]int{0, 0} {
		t.Errorf("owner asked for = %v", owners)
	}
	if entries, _ := os.ReadDir(filepath.Dir(r.keyPath)); len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}

	if got.Fingerprint != fp(newKey) || got.Fingerprint == fp(r.oldKey) {
		t.Errorf("fingerprint = %q", got.Fingerprint)
	}
	if got.RotatedAt != "2026-11-02T15:14:05Z" || !got.CloudSynced || got.OpID == "" {
		t.Errorf("body = %+v", got)
	}
	if len(got.RestoredModels) != 1 || got.RestoredModels[0] != idAlpha {
		t.Errorf("restored = %v", got.RestoredModels)
	}
	if len(got.ClientsToUpdate) != 2 || !strings.Contains(got.ClientsToUpdate[0], "~/.config/opencode/llama-api-key") {
		t.Errorf("clients = %v", got.ClientsToUpdate)
	}
	if rec.Header().Get("X-LocalAI-Op-Id") != got.OpID {
		t.Errorf("op id header %q, body %q", rec.Header().Get("X-LocalAI-Op-Id"), got.OpID)
	}

	// state.key
	k := r.st.Snapshot().Key
	if k.Fingerprint != got.Fingerprint || k.RotatedAt == nil || !k.RotatedAt.Equal(t0.Add(10*time.Minute)) || !k.CloudSynced {
		t.Errorf("state.key = %+v", k)
	}
	// key.rotated {fingerprint}
	evs := r.pub.list()
	if len(evs) != 1 || evs[0].typ != events.TypeKeyRotated || evs[0].opID != got.OpID {
		t.Fatalf("events = %+v", evs)
	}
	if kr, ok := evs[0].data.(events.KeyRotated); !ok || kr.Fingerprint != got.Fingerprint {
		t.Errorf("event data = %+v", evs[0].data)
	}
	// audit key.rotate
	ev := r.audit.only(t, audit.ActionKeyRotate)
	if ev.Result != audit.ResultOK || ev.OpID != got.OpID || ev.Actor.Kind != audit.KindAdmin || ev.Target["fingerprint"] != got.Fingerprint || ev.Target["restart"] != "true" {
		t.Errorf("audit = %+v", ev)
	}
	// The scrubber learns the new key, and the AGT-016 notice of an earlier rotation is withdrawn.
	if len(r.liveKeys) != 1 || r.liveKeys[0] != newKey {
		t.Errorf("live keys = %d", len(r.liveKeys))
	}
	if al := r.alerts.list(); len(al) != 1 || al[0].op != "clear" || al[0].code != doctor.AGT016 {
		t.Errorf("alerts = %+v", al)
	}
	if r.pusher.count() != 1 {
		t.Errorf("pushes = %d", r.pusher.count())
	}
	if _, held := r.lock.Holder(); held {
		t.Error("the lock is still held")
	}
	for _, secret := range []string{newKey, r.oldKey} {
		if strings.Contains(r.everything(), secret) || strings.Contains(rec.Body.String(), secret) {
			t.Error("a key reached the log, the audit, the events, the state or the response")
		}
	}
}

func TestRotateKeyWithAnEmptyBodyRestarts(t *testing.T) {
	r := newRig(t, nil)
	rec := r.send("POST", "/key/rotate", "admin", ``)
	if rec.Code != http.StatusOK || r.restart.count() != 1 {
		t.Errorf("status %d, restarts %d", rec.Code, r.restart.count())
	}
}

func TestRotateKeyOrderOfSteps(t *testing.T) {
	r := newRig(t, nil)
	newKey := expectedKey(t, 0)
	var keyAtRestart string
	var eventsAtRestart, pushesAtRestart int
	r.restart.onRestart = func() {
		keyAtRestart, eventsAtRestart, pushesAtRestart = r.readKey(), len(r.pub.list()), r.pusher.count()
	}
	var lockFreeAtPush bool
	var eventsAtPush int
	r.pusher.onPush = func() {
		hold, err := r.lock.TryAcquire(router.HolderOther)
		lockFreeAtPush = err == nil
		if err == nil {
			hold.Release()
		}
		eventsAtPush = len(r.pub.list())
	}
	if rec := r.send("POST", "/key/rotate", "admin", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if steps := r.restart.steps(); len(steps) != 2 || steps[0] != "prepare" || steps[1] != "restart" {
		t.Errorf("steps = %v", steps)
	}
	if r.restart.heldKind != router.HolderRestart {
		t.Errorf("holder = %q", r.restart.heldKind)
	}
	if keyAtRestart != newKey {
		t.Error("the new key was not on disk when llama restarted")
	}
	if eventsAtRestart != 0 || pushesAtRestart != 0 {
		t.Errorf("key.rotated (%d) or the push (%d) came before the restart finished", eventsAtRestart, pushesAtRestart)
	}
	if !lockFreeAtPush {
		t.Error("the cloud push ran while modelOpsMu was held")
	}
	if eventsAtPush != 0 {
		t.Error("key.rotated was published before the push")
	}
}

func TestRotateKeyCloudSynced(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*rig, *modelsapi.Deps)
		synced bool
		pushes int
		warn   bool
	}{
		{"pushed", nil, true, 1, false},
		{"not linked", func(r *rig, _ *modelsapi.Deps) { r.pusher.err = modelsapi.ErrCloudSkipped }, false, 1, false},
		{"not linked, wrapped", func(r *rig, _ *modelsapi.Deps) {
			r.pusher.err = errors.Join(errors.New("detail"), modelsapi.ErrCloudSkipped)
		}, false, 1, false},
		{"push failed", func(r *rig, _ *modelsapi.Deps) { r.pusher.err = errors.New("503 from the cloud") }, false, 1, true},
		{"no pusher", func(_ *rig, d *modelsapi.Deps) { d.Pusher = nil }, false, 0, false},
		{"offline mode", func(r *rig, _ *modelsapi.Deps) { r.mode.set(state.ModeOffline) }, false, 0, false},
		{"survival mode", func(r *rig, _ *modelsapi.Deps) { r.mode.set(state.ModeSurvival) }, false, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.mod)
			rec := r.send("POST", "/key/rotate", "admin", `{}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			got := decode[modelsapi.KeyRotated](t, rec.Body.Bytes())
			if got.CloudSynced != c.synced || r.st.Snapshot().Key.CloudSynced != c.synced {
				t.Errorf("cloud_synced response %v, state %v, want %v", got.CloudSynced, r.st.Snapshot().Key.CloudSynced, c.synced)
			}
			if r.pusher.count() != c.pushes {
				t.Errorf("pushes = %d", r.pusher.count())
			}
			if warned := strings.Contains(r.logs.String(), "could not be sent to the cloud"); warned != c.warn {
				t.Errorf("warning logged = %v", warned)
			}
			// The rotation itself is never undone by the cloud.
			if r.readKey() != expectedKey(t, 0) || len(r.pub.list()) != 1 {
				t.Error("the key or its event is missing")
			}
		})
	}
}

func TestRotateKeyWithoutRestartLeavesLlamaAloneAndRaisesAGT016(t *testing.T) {
	r := newRig(t, nil)
	var heldKind string
	r.pusher.onPush = func() {
		if h, ok := r.lock.Holder(); ok {
			heldKind = h.Kind
		}
	}
	rec := r.send("POST", "/key/rotate", "admin", `{"restart": false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[modelsapi.KeyRotated](t, rec.Body.Bytes())
	if r.restart.count() != 0 {
		t.Error("llama was restarted")
	}
	if r.readKey() != expectedKey(t, 0) || got.Fingerprint != fp(expectedKey(t, 0)) {
		t.Error("the new key was not written")
	}
	if got.RestoredModels == nil || len(got.RestoredModels) != 0 || !strings.Contains(rec.Body.String(), `"restored_models":[]`) {
		t.Errorf("restored = %v", got.RestoredModels)
	}
	if !got.CloudSynced || got.OpID == "" || len(got.ClientsToUpdate) != 2 {
		t.Errorf("body = %+v", got)
	}
	al := r.alerts.list()
	if len(al) != 1 || al[0].op != "raise" || al[0].code != doctor.AGT016 || al[0].details["service"] != "llama" {
		t.Errorf("alerts = %+v", al)
	}
	if heldKind != "" {
		t.Errorf("the cloud push ran under the lock (%s)", heldKind)
	}
	if evs := r.pub.list(); len(evs) != 1 || evs[0].typ != events.TypeKeyRotated {
		t.Errorf("events = %+v", evs)
	}
	if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Result != audit.ResultOK || ev.Target["restart"] != "false" || ev.OpID != got.OpID {
		t.Errorf("audit = %+v", ev)
	}
}

func TestRotateKeyWhileAnotherModelOperationRuns(t *testing.T) {
	for _, body := range []string{`{}`, `{"restart": false}`} {
		t.Run(body, func(t *testing.T) {
			r := newRig(t, nil)
			hold, err := r.lock.TryAcquire(router.HolderLoad)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Release()
			rec := r.send("POST", "/key/rotate", "admin", body)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != http.StatusConflict || e.Error.Code != doctor.AGT022 || e.Error.Details["op_id"] != hold.OpID {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			// Nothing happened: the old key is in place and nobody was told of a rotation.
			if r.readKey() != r.oldKey {
				t.Error("the key file changed although the lock was busy")
			}
			if len(r.pub.list()) != 0 || r.pusher.count() != 0 || len(r.liveKeys) != 0 || r.st.Snapshot().Key.Fingerprint != "old0…old1" {
				t.Error("a busy rotation left traces")
			}
			if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Result != audit.ResultFail || ev.Code != doctor.AGT022 {
				t.Errorf("audit = %+v", ev)
			}
			if rec.Header().Get("X-LocalAI-Op-Id") != hold.OpID {
				t.Errorf("op id header = %q", rec.Header().Get("X-LocalAI-Op-Id"))
			}
		})
	}
}

func TestRotateKeyWhenLlamaDoesNotRestartTheNewKeyStays(t *testing.T) {
	r := newRig(t, nil)
	r.restart.restartErr = &router.OpError{
		Code: router.CodeLlamaDown, Status: http.StatusInternalServerError, Message: "The AI model service could not be restarted.",
		Details: map[string]any{"op_id": "op_restartfailed", "cause": "restart_failed"},
	}
	newKey := expectedKey(t, 0)
	rec := r.send("POST", "/key/rotate", "admin", `{}`)
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusInternalServerError || e.Error.Code != doctor.LLM001 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if e.Error.Details["key_rotated"] != true || e.Error.Details["fingerprint"] != fp(newKey) || e.Error.Details["cause"] != "restart_failed" {
		t.Errorf("details = %v", e.Error.Details)
	}
	// The new key stays: the old one is assumed leaked.
	if r.readKey() != newKey {
		t.Error("the key file was rolled back")
	}
	if k := r.st.Snapshot().Key; k.Fingerprint != fp(newKey) || k.RotatedAt == nil {
		t.Errorf("state.key = %+v", k)
	}
	// The event stream and the cloud still hear about it, best effort.
	if len(r.pub.list()) != 1 || r.pusher.count() != 1 {
		t.Errorf("events %d, pushes %d", len(r.pub.list()), r.pusher.count())
	}
	if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Result != audit.ResultFail || ev.Code != doctor.LLM001 || ev.Target["fingerprint"] != fp(newKey) {
		t.Errorf("audit = %+v", ev)
	}
	if rec.Header().Get("X-LocalAI-Op-Id") != "op_restartfailed" {
		t.Errorf("op id header = %q", rec.Header().Get("X-LocalAI-Op-Id"))
	}
	if strings.Contains(r.everything(), newKey) || strings.Contains(rec.Body.String(), newKey) {
		t.Error("the key leaked")
	}
	if len(r.alerts.list()) != 0 {
		t.Errorf("alerts = %+v", r.alerts.list())
	}
}

func TestRotateKeyOtherFailuresAfterTheWriteAlsoSayTheKeyRotated(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		marked bool
	}{
		{"the router stopped answering during the restore",
			&router.OpError{Code: router.CodeRouterDown, Status: http.StatusBadGateway, Message: "down", Details: map[string]any{}},
			502, doctor.LLM004, true},
		{"the agent is shutting down", router.ErrInterrupted, 503, doctor.AGT024, true},
		{"the caller gave up", context.DeadlineExceeded, 504, doctor.AGT099, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.restart.restartErr = c.err
			rec := r.send("POST", "/key/rotate", "admin", `{}`)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if (e.Error.Details["key_rotated"] == true) != c.marked {
				t.Errorf("details = %v", e.Error.Details)
			}
			if r.readKey() != expectedKey(t, 0) {
				t.Error("the key file was rolled back")
			}
			if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Result != audit.ResultFail || ev.Code != c.code {
				t.Errorf("audit = %+v", ev)
			}
		})
	}
}

func TestRotateKeyWriteFailureKeepsTheOldKey(t *testing.T) {
	r := newRig(t, func(rg *rig, d *modelsapi.Deps) { d.KeyFS = failRenameFS{rg.fs} })
	rec := r.send("POST", "/key/rotate", "admin", `{}`)
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusInternalServerError || e.Error.Code != doctor.AGT099 || e.Error.Details["reason"] != "key_write_failed" || e.Error.Details["key_rotated"] != false {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r.readKey() != r.oldKey {
		t.Error("the old key was lost")
	}
	if entries, _ := os.ReadDir(filepath.Dir(r.keyPath)); len(entries) != 1 {
		t.Errorf("a temp file was left behind: %v", entries)
	}
	// Prepare failed under the lock, so llama was never restarted and nobody heard of a rotation.
	if steps := r.restart.steps(); len(steps) != 1 || steps[0] != "prepare" {
		t.Errorf("steps = %v", steps)
	}
	if len(r.pub.list()) != 0 || r.pusher.count() != 0 || len(r.liveKeys) != 0 || r.st.Snapshot().Key.Fingerprint != "old0…old1" {
		t.Error("a failed write left traces")
	}
	if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Result != audit.ResultFail || ev.Code != doctor.AGT099 {
		t.Errorf("audit = %+v", ev)
	}
	if _, held := r.lock.Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRotateKeyWriteFailureWithoutRestart(t *testing.T) {
	r := newRig(t, func(rg *rig, d *modelsapi.Deps) { d.KeyFS = failRenameFS{rg.fs} })
	rec := r.send("POST", "/key/rotate", "admin", `{"restart": false}`)
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusInternalServerError || e.Error.Details["key_rotated"] != false || r.readKey() != r.oldKey {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.alerts.list()) != 0 {
		t.Errorf("alerts = %+v", r.alerts.list())
	}
	if _, held := r.lock.Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestRotateKeyWithoutEntropy(t *testing.T) {
	r := newRig(t, func(_ *rig, d *modelsapi.Deps) { d.Rand = failingRand{} })
	rec := r.send("POST", "/key/rotate", "admin", `{}`)
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusInternalServerError || e.Error.Code != doctor.AGT099 || e.Error.Details["reason"] != "key_generate_failed" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r.readKey() != r.oldKey || r.restart.count() != 0 {
		t.Error("something changed without a new key")
	}
}

func TestRotateKeyStateFileFailureDoesNotUndoTheRotation(t *testing.T) {
	r := newRig(t, func(_ *rig, d *modelsapi.Deps) { d.State = brokenState{} })
	rec := r.send("POST", "/key/rotate", "admin", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if r.readKey() != expectedKey(t, 0) || !strings.Contains(r.logs.String(), "could not be recorded in the state file") {
		t.Error("the key was not written, or the failure was not logged")
	}
}

type brokenState struct{}

func (brokenState) MutateSync(func(*state.State) error) error {
	return errors.New("state file is read-only")
}

func TestRotateKeyThenShowKeyReturnsTheNewKey(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	if rec := r.get("/key", "op"); decode[modelsapi.KeyBody](t, rec.Body.Bytes()).Key != r.oldKey {
		t.Fatal("setup")
	}
	if rec := r.send("POST", "/key/rotate", "admin", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	// The key source is told to re-read, so the very next GET /key is the new key even if the file's
	// modification time did not move.
	body := decode[modelsapi.KeyBody](t, r.get("/key", "op").Body.Bytes())
	if body.Key != expectedKey(t, 0) {
		t.Error("GET /key still returns the old key")
	}
}

func TestRotateKeyTwiceChangesTheFingerprintEachTime(t *testing.T) {
	r := newRig(t, nil)
	var seen []string
	for i := 0; i < 2; i++ {
		rec := r.send("POST", "/key/rotate", "admin", `{}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		seen = append(seen, decode[modelsapi.KeyRotated](t, rec.Body.Bytes()).Fingerprint)
	}
	if seen[0] == seen[1] || seen[1] != fp(expectedKey(t, 1)) {
		t.Errorf("fingerprints = %v", seen)
	}
}

func TestRotateKeyAuthAndBody(t *testing.T) {
	cases := []struct {
		name, principal, body string
		status                int
		code                  string
	}{
		{"anonymous", "", `{}`, 401, doctor.SEC001},
		{"operator", "op", `{}`, 403, doctor.SEC002},
		{"portal", "portal", `{}`, 401, doctor.SVC110},
		{"unknown field", "admin", `{"restart": true, "force": true}`, 400, doctor.AGT020},
		{"wrong type", "admin", `{"restart": "yes"}`, 400, doctor.AGT020},
		{"not json", "admin", `{`, 400, doctor.AGT020},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			rec := r.send("POST", "/key/rotate", c.principal, c.body)
			if rec.Code != c.status || envelope(t, rec.Body.Bytes()).Error.Code != c.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if r.readKey() != r.oldKey || r.restart.count() != 0 || len(r.audit.events()) != 0 {
				t.Error("a refused request changed something")
			}
		})
	}
}

func TestRotateKeyServiceMethodForTheCloudExecutor(t *testing.T) {
	r := newRig(t, nil)
	res, err := r.svc.RotateKey(t.Context(), modelsapi.KeyRotateRequest{Restart: true, Actor: audit.Cloud("job-9")})
	if err != nil || res.Fingerprint != fp(expectedKey(t, 0)) {
		t.Fatalf("%+v %v", res, err)
	}
	if ev := r.audit.only(t, audit.ActionKeyRotate); ev.Actor.Kind != audit.KindCloud || ev.Actor.JobID != "job-9" {
		t.Errorf("audit = %+v", ev)
	}
}
