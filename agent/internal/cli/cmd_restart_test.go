package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl restart` (AICTL-05, aictl.md section 4.14).

const llamaRestarted = `{"name":"llama","active":"active","restored_models":["` + qwenID + `"],"op_id":"op_r1"}`

func newRestartRig(t *testing.T, body string) *mgRig {
	t.Helper()
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/models", 200, modelsBody)
	for _, n := range []string{"llama", "kiwix", "whisper"} {
		r.agent().Respond(http.MethodPost, "/services/"+n+"/restart", 200, strings.Replace(body, `"llama"`, `"`+n+`"`, 1))
	}
	return r
}

func TestRestart(t *testing.T) {
	t.Run("llama: the models that came back are named", func(t *testing.T) {
		r := newRestartRig(t, llamaRestarted)
		got := r.run(mgArgs("restart", "llama"))
		checkRun(t, got, 0, "✓ The AI model service is running again. Qwen 3.5 9B is loaded.\n", "… Restarting the AI model service\n")
		if b := r.lastBody(http.MethodPost, "/services/llama/restart"); b != `{}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		got := newRestartRig(t, llamaRestarted).run(mgArgs("--lang", "es", "restart", "llama"))
		checkRun(t, got, 0, "✓ El servicio de modelos de IA está en marcha otra vez. Qwen 3.5 9B está\n  cargado.\n",
			"… Reiniciando el servicio de modelos de IA\n")
	})
	t.Run("two models came back", func(t *testing.T) {
		body := `{"name":"llama","active":"active","restored_models":["` + qwenID + `","` + smallID + `"],"op_id":"op_r1"}`
		got := newRestartRig(t, body).run(mgArgs("restart", "llama"))
		if got.code != 0 || !strings.Contains(flat(got.out), "Qwen 3.5 9B and Small VLM are loaded.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("llama with no model to restore says how to load one", func(t *testing.T) {
		body := `{"name":"llama","active":"active","restored_models":[],"op_id":"op_r1"}`
		got := newRestartRig(t, body).run(mgArgs("restart", "llama"))
		if got.code != 0 || !strings.Contains(flat(got.out), "No model is loaded yet: load one with `aictl load <model-id>`.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("kiwix and whisper", func(t *testing.T) {
		body := `{"name":"llama","active":"active","restored_models":[],"op_id":"op_r1"}`
		r := newRestartRig(t, body)
		checkRun(t, r.run(mgArgs("restart", "kiwix")), 0, "✓ The library service is running again.\n", "… Restarting the library service\n")
		checkRun(t, r.run(mgArgs("restart", "whisper")), 0, "✓ The speech service is running again.\n", "… Restarting the speech service\n")
	})
	t.Run("--force is in the body", func(t *testing.T) {
		r := newRestartRig(t, llamaRestarted)
		r.run(mgArgs("restart", "llama", "--force"))
		if b := r.lastBody(http.MethodPost, "/services/llama/restart"); b != `{"force":true}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("--json is the body, nothing else", func(t *testing.T) {
		checkRun(t, newRestartRig(t, llamaRestarted).run(mgArgs("--json", "restart", "llama")), 0, llamaRestarted+"\n", "")
	})
	t.Run("--quiet", func(t *testing.T) {
		checkRun(t, newRestartRig(t, llamaRestarted).run(mgArgs("-q", "restart", "llama")), 0, "", "")
	})
	t.Run("an unknown service or none is exit 2 before any request", func(t *testing.T) {
		r := newRestartRig(t, llamaRestarted)
		for _, a := range [][]string{{"restart", "nginx"}, {"restart"}, {"restart", "llama", "kiwix"}, {"restart", "../llama"}} {
			if got := r.run(mgArgs(a...)); got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
	failures := []struct {
		name    string
		status  int
		code    string
		details map[string]any
		exit    int
	}{
		{"another operation runs", 409, "LAI-AGT-022", map[string]any{"op_id": "op_busy"}, ExitRefused},
		{"survival mode", 409, "LAI-AGT-023", map[string]any{"mode": "survival", "requires": "force"}, ExitRefused},
		{"the service did not come back", 502, "LAI-LLM-001", map[string]any{"service": "llama", "reason": "timeout"}, ExitFailed},
		{"unknown service for the agent", 404, "LAI-AGT-021", map[string]any{"kind": "service"}, ExitNotFound},
		{"the route deadline", 504, "LAI-AGT-099", map[string]any{"reason": "deadline_exceeded"}, ExitTimeout},
		{"not allowed", 403, "LAI-SEC-013", nil, ExitDenied},
	}
	for _, tc := range failures {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			r := newRestartRig(t, llamaRestarted)
			r.agent().RespondError(http.MethodPost, "/services/llama/restart", tc.status, tc.code, "No.", "Fix it.", tc.details)
			got := r.run(mgArgs("restart", "llama"))
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("restart", "llama"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the deadline is 120 s: --timeout wins", func(t *testing.T) {
		r := newRestartRig(t, llamaRestarted)
		r.agent().Handle(http.MethodPost, "/services/llama/restart", func(w http.ResponseWriter, req *http.Request) {
			<-req.Context().Done()
		})
		got := r.run(mgArgs("--timeout", "100ms", "restart", "llama"))
		if got.code != ExitTimeout || !strings.Contains(got.errOut, "[LAI-CLI-004]") {
			t.Fatalf("%+v", got)
		}
	})
}
