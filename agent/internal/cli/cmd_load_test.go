package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Tests of `aictl load`, `unload` and `default <id>` (AICTL-05, aictl.md sections 4.3 to 4.5). The
// fake agent publishes the events of an operation from inside the POST handler, so they reach aictl
// before it knows the op id and must be held and matched afterwards, as with the real agent.

const (
	qwenID   = "qwen3.5-9b-q4km"
	smallID  = "small-vlm-q4km"
	bigID    = "qwen3.6-35b-a3b-q4km"
	loadOpID = "op_7k2m9q4r8t1c"
)

// modelsBody lists the models the tests talk about, with display names.
const modelsBody = `{"models_max":1,"default":"` + qwenID + `","models":[` +
	`{"id":"` + qwenID + `","display_name":"Qwen 3.5 9B","engine":"llama","status":"unloaded","size_bytes":5868826976},` +
	`{"id":"` + smallID + `","display_name":"Small VLM","engine":"llama","status":"loaded","size_bytes":3200000000},` +
	`{"id":"` + bigID + `","display_name":"Qwen 3.6 35B","engine":"llama","status":"absent","size_bytes":19000000000}]}`

// loadRig is a fake agent whose load route starts an operation: publish runs inside the handler and
// the handler then answers with status and body.
type loadRig struct {
	*mgRig
	publish func(ev *fake.Events)
	status  int
	body    string
}

func newLoadRig(t *testing.T) *loadRig {
	t.Helper()
	r := &loadRig{mgRig: newMgRig(t, fake.Options{}), status: 202,
		body: `{"id":"` + qwenID + `","status":"loading","op_id":"` + loadOpID + `"}`}
	ev := r.events()
	r.agent().Respond(http.MethodGet, "/models", 200, modelsBody)
	r.agent().Handle(http.MethodPost, "/models/"+qwenID+"/load", func(w http.ResponseWriter, _ *http.Request) {
		if r.publish != nil {
			r.publish(ev)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LocalAI-Op-Id", loadOpID)
		w.WriteHeader(r.status)
		io.WriteString(w, r.body)
	})
	return r
}

// loadsWell publishes a load that evicts the small model, shows progress and succeeds. A restored
// model's own model.loaded (same op id) must not end the wait.
func loadsWell(ev *fake.Events) {
	ev.Publish(events.TypeModelLoading, loadOpID, events.ModelLoading{Model: qwenID, Unloading: []string{smallID}, OpID: loadOpID})
	ev.Publish(events.TypeModelUnloaded, loadOpID, events.ModelUnloaded{Model: smallID, Reason: events.UnloadEvicted})
	half, full := 0.5, 1.0
	ev.Publish(events.TypeModelLoading, loadOpID, events.ModelLoading{Model: qwenID, Unloading: []string{smallID}, OpID: loadOpID, Progress: &half})
	ev.Publish(events.TypeModelLoading, loadOpID, events.ModelLoading{Model: qwenID, Unloading: []string{smallID}, OpID: loadOpID, Progress: &full})
	ev.Publish(events.TypeModelLoading, "op_someone_else", events.ModelLoading{Model: bigID, OpID: "op_someone_else"})
	ev.Publish(events.TypeModelLoaded, loadOpID, events.ModelLoaded{Model: qwenID, LoadMS: 7100})
}

// loadsBadly publishes a load that fails, rolls back (the evicted model is restored under the same
// op id) and reports model.failed with code.
func loadsBadly(code string, rolledBack bool) func(*fake.Events) {
	return func(ev *fake.Events) {
		ev.Publish(events.TypeModelLoading, loadOpID, events.ModelLoading{Model: qwenID, Unloading: []string{smallID}, OpID: loadOpID})
		ev.Publish(events.TypeModelUnloaded, loadOpID, events.ModelUnloaded{Model: smallID, Reason: events.UnloadEvicted})
		ev.Publish(events.TypeModelLoaded, loadOpID, events.ModelLoaded{Model: smallID, LoadMS: 5000}) // restored by the rollback
		restored := []string{}
		if rolledBack {
			restored = []string{smallID}
		}
		ev.Publish(events.TypeModelFailed, loadOpID, events.ModelFailed{Model: qwenID, Code: code, Message: "failed", RolledBack: rolledBack, Restored: restored})
	}
}

func TestLoad(t *testing.T) {
	const ready = "✓ Qwen 3.5 9B is ready. (7.1 s)\n  Use it at http://127.0.0.1:8080/v1 with model \"" + qwenID + "\".\n"
	t.Run("human, off a terminal: the unloading line, the loading line, the result", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		got := r.run(mgArgs("load", qwenID))
		checkRun(t, got, 0, ready, "… Unloading Small VLM to make room\n… Loading Qwen 3.5 9B\n")
		if b := r.lastBody(http.MethodPost, "/models/"+qwenID+"/load"); b != `{"unload_others":true,"wait":false}` {
			t.Errorf("body %s", b)
		}
		if got := strings.Join(r.requestLines(), ","); got != "GET /models,POST /models/"+qwenID+"/load" {
			t.Errorf("requests %s", got)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		got := r.run(mgArgs("--lang", "es", "load", qwenID))
		checkRun(t, got, 0,
			"✓ Qwen 3.5 9B está listo. (7.1 s)\n  Úselo en http://127.0.0.1:8080/v1 con el modelo \""+qwenID+"\".\n",
			"… Descargando Small VLM para hacer lugar\n… Cargando Qwen 3.5 9B\n")
	})
	t.Run("--keep-others is in the body", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		if got := r.run(mgArgs("load", qwenID, "--keep-others")); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, "/models/"+qwenID+"/load"); b != `{"unload_others":false,"wait":false}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("--json is the final answer, stderr empty", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		got := r.run(mgArgs("--json", "load", qwenID))
		want := `{"id":"` + qwenID + `","status":"loaded","already_loaded":false,"load_ms":7100,"unloaded":["` + smallID + `"],"op_id":"` + loadOpID + `"}` + "\n"
		checkRun(t, got, 0, want, "")
	})
	t.Run("--quiet prints no progress and no result line", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		checkRun(t, r.run(mgArgs("-q", "load", qwenID)), 0, "", "")
	})
	// The 202 of ModelLoading carries `warning` since #232 (the planner knows it before the answer), and
	// aictl shows it with its fix, with or without --no-wait.
	t.Run("a memory warning of the agent is shown when it comes", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		r.body = `{"id":"` + qwenID + `","status":"loading","op_id":"` + loadOpID + `","warning":{"code":"LAI-HW-005","message":"Other programs use much of the graphics memory."}}`
		got := r.run(mgArgs("load", qwenID))
		if got.code != 0 || !strings.Contains(got.errOut, "[LAI-HW-005]") || !strings.HasPrefix(got.out, "✓ Qwen 3.5 9B is ready.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("already loaded: the 200 answer ends it at once", func(t *testing.T) {
		r := newLoadRig(t)
		r.status = 200
		r.body = `{"id":"` + qwenID + `","status":"loaded","already_loaded":true,"load_ms":0,"unloaded":[],"op_id":"` + loadOpID + `"}`
		got := r.run(mgArgs("load", qwenID))
		checkRun(t, got, 0, "✓ Qwen 3.5 9B is already loaded.\n  Use it at http://127.0.0.1:8080/v1 with model \""+qwenID+"\".\n", "")
		checkRun(t, r.run(mgArgs("--json", "load", qwenID)), 0, r.body+"\n", "")
	})
	t.Run("--no-wait: the 202, no event stream", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		got := r.run(mgArgs("load", qwenID, "--no-wait"))
		checkRun(t, got, 0, "i Loading Qwen 3.5 9B has started. Check it with `aictl models`.\n", "")
		if n := len(r.events().Connections()); n != 0 {
			t.Errorf("events connections = %d, want none", n)
		}
		checkRun(t, r.run(mgArgs("--json", "load", qwenID, "--no-wait")), 0, r.body+"\n", "")
	})
	t.Run("--no-wait still shows the memory warning of the 202, with its fix", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsWell
		r.body = `{"id":"` + qwenID + `","status":"loading","op_id":"` + loadOpID + `","warning":{"code":"LAI-HW-005","message":"Other programs use much of the graphics memory."}}`
		got := r.run(mgArgs("load", qwenID, "--no-wait"))
		if got.code != 0 || !strings.Contains(got.errOut, "[LAI-HW-005]") || !strings.Contains(got.out, "Loading Qwen 3.5 9B has started") {
			t.Fatalf("%+v", got)
		}
		checkRun(t, r.run(mgArgs("--json", "load", qwenID, "--no-wait")), 0, r.body+"\n", "")
	})
	t.Run("a model the agent does not know by name falls back to the id", func(t *testing.T) {
		r := newLoadRig(t)
		r.agent().RespondError(http.MethodGet, "/models", 500, "LAI-AGT-099", "Bug.", "Report it.", nil)
		r.publish = loadsWell
		got := r.run(mgArgs("load", qwenID))
		if got.code != 0 || !strings.HasPrefix(got.out, "✓ "+qwenID+" is ready. (7.1 s)\n") {
			t.Fatalf("%+v", got)
		}
	})
}

func TestLoadFailures(t *testing.T) {
	t.Run("rollback: the model failed, the previous one is back (exit 1)", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsBadly("LAI-LLM-009", true)
		got := r.run(mgArgs("load", qwenID))
		wantErr := "… Unloading Small VLM to make room\n… Loading Qwen 3.5 9B\n" +
			"✗ The model Qwen 3.5 9B failed to start. The previous model was started again.\n" +
			"  [LAI-LLM-009]\n" +
			"  → Fix: Choose a smaller model (`aictl models --loadable`) or check\n" +
			"         `aictl logs --unit llama`.\n"
		checkRun(t, got, ExitFailed, "", wantErr)
	})
	t.Run("rollback in Spanish", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsBadly("LAI-LLM-009", true)
		got := r.run(mgArgs("--lang", "es", "load", qwenID))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "✗ El modelo Qwen 3.5 9B no pudo iniciar. Se volvió a iniciar el modelo anterior.") ||
			!strings.Contains(got.errOut, "[LAI-LLM-009]") || !strings.Contains(got.errOut, "→ Solución:") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json: the envelope with the rollback in its details", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsBadly("LAI-LLM-009", true)
		got := r.run(mgArgs("--json", "load", qwenID))
		if got.code != ExitFailed || got.errOut != "" || !strings.Contains(got.out, `"code":"LAI-LLM-009"`) ||
			!strings.Contains(got.out, `"rolled_back":true`) || !strings.Contains(got.out, `"restored":["`+smallID+`"]`) ||
			!strings.Contains(got.out, `"op_id":"`+loadOpID+`"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("took too long: exit 7", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsBadly("LAI-LLM-010", true)
		got := r.run(mgArgs("load", qwenID))
		if got.code != ExitTimeout || !strings.Contains(flat(got.errOut), "✗ The model Qwen 3.5 9B took too long to start. The previous model was started again.") ||
			!strings.Contains(got.errOut, "[LAI-LLM-010]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("nothing could be started: exit 1, critical", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = loadsBadly("LAI-LLM-012", false)
		got := r.run(mgArgs("load", qwenID))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "✗ No model could be started, not even the previous one.") ||
			!strings.Contains(got.errOut, "[LAI-LLM-012]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a failed load of another model is not ours", func(t *testing.T) {
		r := newLoadRig(t)
		r.publish = func(ev *fake.Events) {
			ev.Publish(events.TypeModelFailed, "op_someone_else", events.ModelFailed{Model: bigID, Code: "LAI-LLM-009"})
			loadsWell(ev)
		}
		if got := r.run(mgArgs("load", qwenID)); got.code != 0 {
			t.Fatalf("%+v", got)
		}
	})

	// What the agent refuses before it starts (the answer to the POST itself).
	refusals := []struct {
		name    string
		status  int
		code    string
		details map[string]any
		exit    int
	}{
		{"unknown model", 404, "LAI-AGT-021", map[string]any{"kind": "model"}, ExitNotFound},
		{"another operation runs", 409, "LAI-AGT-022", map[string]any{"op_id": "op_busy"}, ExitRefused},
		{"does not fit", 422, "LAI-CAT-005", nil, ExitRefused},
		{"files missing", 422, "LAI-LLM-008", nil, ExitRefused},
		{"no room and --keep-others", 409, "LAI-LLM-011", nil, ExitRefused},
		{"router not answering", 502, "LAI-LLM-004", nil, ExitFailed},
		{"denied by the PIN rules", 403, "LAI-SEC-013", nil, ExitDenied},
	}
	for _, tc := range refusals {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			r := newLoadRig(t)
			r.agent().RespondError(http.MethodPost, "/models/"+qwenID+"/load", tc.status, tc.code, "Refused.", "Fix it.", tc.details)
			got := r.run(mgArgs("load", qwenID))
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("the agent is down: exit 3", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("load", qwenID))
		if got.code != ExitUnreachable || !strings.Contains(got.errOut, "[LAI-CLI-001]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a bad id never reaches the agent (exit 2)", func(t *testing.T) {
		for _, id := range []string{"Qwen", "ab", "-qwen", "qwen_9b", "has space", "../status", strings.Repeat("a", 65)} {
			r := newLoadRig(t)
			got := r.run(mgArgs("load", id))
			if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
				t.Errorf("%q: %+v, requests %v", id, got, r.requestLines())
			}
		}
		r := newLoadRig(t)
		for _, a := range [][]string{{"load"}, {"load", qwenID, smallID}} {
			if got := r.run(mgArgs(a...)); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
}

// A lost stream: the model's real state decides (aictl.md section 3.3.1).
func TestLoadAfterALostStream(t *testing.T) {
	t.Run("loaded while the stream was down", func(t *testing.T) {
		r := newLoadRig(t)
		r.agent().Respond(http.MethodGet, "/models/"+qwenID, 200, `{"id":"`+qwenID+`","status":"loaded","last_load_ms":6400}`)
		r.publish = func(ev *fake.Events) {
			// The load finished but its events were lost: the stream drops and nothing else comes.
			go ev.Drop()
		}
		got := r.run(mgArgs("load", qwenID))
		if got.code != 0 || !strings.HasPrefix(got.out, "✓ Qwen 3.5 9B is ready. (6.4 s)\n") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("failed while the stream was down", func(t *testing.T) {
		r := newLoadRig(t)
		r.agent().Respond(http.MethodGet, "/models/"+qwenID, 200, `{"id":"`+qwenID+`","status":"failed"}`)
		r.publish = func(ev *fake.Events) { go ev.Drop() }
		got := r.run(mgArgs("load", qwenID))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "[LAI-LLM-009]") {
			t.Fatalf("%+v", got)
		}
	})
}

// Ctrl-C stops the waiting, not the load: exit 130 and no request but the one that started it.
func TestLoadCtrlC(t *testing.T) {
	r := newLoadRig(t)
	// The load starts but never ends. Its first event is held until aictl knows the op id, so the
	// "Loading" line proves that the request was answered and the wait began.
	r.publish = func(ev *fake.Events) {
		ev.Publish(events.TypeModelLoading, loadOpID, events.ModelLoading{Model: qwenID, OpID: loadOpID})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := mgArgs("load", qwenID)
	o.ctx = ctx
	ch, stderr := r.runAsync(o)
	untilTrue(t, "the loading line", func() bool { return strings.Contains(stderr.String(), "… Loading Qwen 3.5 9B") })
	cancel()
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.errOut, "Still running in the background") {
		t.Fatalf("%+v", got)
	}
	for _, q := range r.requestLines() {
		if q != "GET /models" && q != "POST /models/"+qwenID+"/load" {
			t.Errorf("unexpected request %q after Ctrl-C (an unload must never be sent)", q)
		}
	}
}

// --- unload -------------------------------------------------------------------------------------------

func TestUnload(t *testing.T) {
	newRig := func(t *testing.T, body string) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/models/"+smallID, 200, `{"id":"`+smallID+`","display_name":"Small VLM","status":"unloaded"}`)
		r.agent().Respond(http.MethodGet, "/models", 200, modelsBody)
		r.agent().Respond(http.MethodPost, "/models/"+smallID+"/unload", 200, body)
		return r
	}
	const done = `{"id":"` + smallID + `","status":"unloaded","already_unloaded":false,"op_id":"op_1"}`
	t.Run("human en and es", func(t *testing.T) {
		r := newRig(t, done)
		checkRun(t, r.run(mgArgs("unload", smallID)), 0, "✓ Small VLM was unloaded.\n", "")
		if b := r.lastBody(http.MethodPost, "/models/"+smallID+"/unload"); b != `{}` {
			t.Errorf("body %s", b)
		}
		checkRun(t, newRig(t, done).run(mgArgs("--lang", "es", "unload", smallID)), 0, "✓ Small VLM se descargó.\n", "")
	})
	t.Run("--force is in the body", func(t *testing.T) {
		r := newRig(t, done)
		r.run(mgArgs("unload", smallID, "--force"))
		if b := r.lastBody(http.MethodPost, "/models/"+smallID+"/unload"); b != `{"force":true}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("--json is the body", func(t *testing.T) {
		checkRun(t, newRig(t, done).run(mgArgs("--json", "unload", smallID)), 0, done+"\n", "")
	})
	t.Run("already unloaded", func(t *testing.T) {
		r := newRig(t, `{"id":"`+smallID+`","status":"unloaded","already_unloaded":true,"op_id":"op_1"}`)
		checkRun(t, r.run(mgArgs("unload", smallID)), 0, "i Small VLM was already unloaded.\n", "")
	})
	t.Run("the model would not stop: llama was restarted (a warning, exit 0)", func(t *testing.T) {
		r := newRig(t, `{"id":"`+smallID+`","status":"unloaded","already_unloaded":false,"op_id":"op_1","restarted":true,"restored":["`+qwenID+`"]}`)
		got := r.run(mgArgs("unload", smallID))
		if got.code != 0 || got.out != "✓ Small VLM was unloaded.\n" || !strings.Contains(got.errOut, "[LAI-LLM-031]") ||
			!strings.Contains(flat(got.errOut), "Loaded again: Qwen 3.5 9B.") {
			t.Fatalf("%+v", got)
		}
	})
	failures := []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"unknown model", 404, "LAI-AGT-021", ExitNotFound},
		{"survival and the last model, without --force", 409, "LAI-AGT-023", ExitRefused},
		{"another operation runs", 409, "LAI-AGT-022", ExitRefused},
		{"not a llama model", 422, "LAI-CAT-005", ExitRefused},
		{"nothing could be loaded after the restart", 500, "LAI-LLM-012", ExitFailed},
		{"router not answering", 502, "LAI-LLM-004", ExitFailed},
		{"the model would not stop in time", 504, "LAI-LLM-031", ExitTimeout},
	}
	for _, tc := range failures {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			r := newRig(t, done)
			r.agent().RespondError(http.MethodPost, "/models/"+smallID+"/unload", tc.status, tc.code, "No.", "Fix it.", nil)
			got := r.run(mgArgs("unload", smallID))
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("a bad id is exit 2 before any request", func(t *testing.T) {
		r := newRig(t, done)
		got := r.run(mgArgs("unload", "Small VLM"))
		if got.code != ExitUsage || len(r.requestLines()) != 0 {
			t.Fatalf("%+v %v", got, r.requestLines())
		}
	})
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("unload", smallID))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

// --- default <id> ---------------------------------------------------------------------------------------

func TestDefaultSet(t *testing.T) {
	const changed = `{"default":"` + qwenID + `","previous":"` + smallID + `","ini_regenerated":true}`
	newRig := func(t *testing.T, status string) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPut, "/models/default", 200, changed)
		r.agent().Respond(http.MethodGet, "/models/"+qwenID, 200, `{"id":"`+qwenID+`","display_name":"Qwen 3.5 9B","status":"`+status+`"}`)
		return r
	}
	t.Run("not loaded", func(t *testing.T) {
		r := newRig(t, "unloaded")
		got := r.run(mgArgs("default", qwenID))
		checkRun(t, got, 0, "✓ The default model is now Qwen 3.5 9B. It starts automatically when the\n"+
			"  computer starts.\n"+
			"  It is not loaded right now. Load it with `aictl load "+qwenID+"`.\n", "")
		if b := r.lastBody(http.MethodPut, "/models/default"); b != `{"id":"`+qwenID+`"}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("loaded", func(t *testing.T) {
		got := newRig(t, "loaded").run(mgArgs("default", qwenID))
		if got.code != 0 || !strings.HasSuffix(got.out, "\n  It is loaded right now.\n") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		got := newRig(t, "unloaded").run(mgArgs("--lang", "es", "default", qwenID))
		checkRun(t, got, 0, "✓ El modelo predeterminado ahora es Qwen 3.5 9B. Se inicia solo cuando arranca\n"+
			"  el equipo.\n"+
			"  No está cargado ahora. Cárguelo con `aictl load "+qwenID+"`.\n", "")
	})
	t.Run("--json is the body", func(t *testing.T) {
		checkRun(t, newRig(t, "unloaded").run(mgArgs("--json", "default", qwenID)), 0, changed+"\n", "")
	})
	t.Run("showing still works without an argument", func(t *testing.T) {
		r := newRig(t, "loaded")
		r.agent().Respond(http.MethodGet, "/models", 200, modelsBody)
		checkRun(t, r.run(mgArgs("--json", "default")), 0, `{"default":"`+qwenID+`"}`+"\n", "")
	})
	for _, tc := range []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"not present or not loadable", 422, "LAI-CAT-005", ExitRefused},
		{"files missing", 422, "LAI-LLM-008", ExitRefused},
		{"unknown model", 404, "LAI-AGT-021", ExitNotFound},
		{"the list could not be written", 500, "LAI-LLM-007", ExitFailed},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			r := newRig(t, "unloaded")
			r.agent().RespondError(http.MethodPut, "/models/default", tc.status, tc.code, "No.", "Fix it.", nil)
			got := r.run(mgArgs("default", qwenID))
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("a bad id, or two ids, is exit 2", func(t *testing.T) {
		r := newRig(t, "unloaded")
		for _, a := range [][]string{{"default", "Qwen"}, {"default", qwenID, smallID}} {
			if got := r.run(mgArgs(a...)); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
}
