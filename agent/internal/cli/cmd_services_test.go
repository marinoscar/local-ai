package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl services start|stop|restart` (AICTL-10, aictl.md section 4.13).

// newServicesRig answers POST /services/{name}/{action} the way the agent does: the unit ends active for
// a start or a restart and inactive for a stop; llama also reports the models it restored.
func newServicesRig(t *testing.T) *mgRig {
	t.Helper()
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/models", 200, modelsBody)
	for _, n := range []string{"llama", "kiwix", "whisper"} {
		restored := `[]`
		if n == "llama" {
			restored = `["` + qwenID + `"]`
		}
		r.agent().Respond(http.MethodPost, "/services/"+n+"/start", 200, `{"name":"`+n+`","active":"active","restored_models":`+restored+`,"op_id":"op_s1"}`)
		r.agent().Respond(http.MethodPost, "/services/"+n+"/restart", 200, `{"name":"`+n+`","active":"active","restored_models":`+restored+`,"op_id":"op_s2"}`)
		r.agent().Respond(http.MethodPost, "/services/"+n+"/stop", 200, `{"name":"`+n+`","active":"inactive","restored_models":[],"op_id":"op_s3"}`)
	}
	return r
}

func TestServicesActions(t *testing.T) {
	cases := []struct {
		args        []string
		stdout, err string
	}{
		{[]string{"services", "start", "llama"}, "✓ The AI model service is running. Qwen 3.5 9B is loaded.\n", "… Starting the AI model service\n"},
		{[]string{"services", "stop", "llama"}, "✓ The AI model service is stopped.\n", "… Stopping the AI model service\n"},
		{[]string{"services", "restart", "llama"}, "✓ The AI model service is running again. Qwen 3.5 9B is loaded.\n", "… Restarting the AI model service\n"},
		{[]string{"services", "start", "kiwix"}, "✓ The library service is running.\n", "… Starting the library service\n"},
		{[]string{"services", "stop", "kiwix"}, "✓ The library service is stopped.\n", "… Stopping the library service\n"},
		{[]string{"services", "restart", "whisper"}, "✓ The speech service is running again.\n", "… Restarting the speech service\n"},
		{[]string{"services", "stop", "whisper"}, "✓ The speech service is stopped.\n", "… Stopping the speech service\n"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			r := newServicesRig(t)
			checkRun(t, r.run(mgArgs(tc.args...)), 0, tc.stdout, tc.err)
			want := "POST /services/" + tc.args[2] + "/" + tc.args[1]
			if got := r.requestLines(); !contains(got, want) {
				t.Errorf("requests %v lack %s", got, want)
			}
			if b := r.lastBody(http.MethodPost, "/services/"+tc.args[2]+"/"+tc.args[1]); b != `{}` {
				t.Errorf("body %s", b)
			}
		})
	}
}

func TestServicesActionsSpanish(t *testing.T) {
	checkRun(t, newServicesRig(t).run(mgArgs("--lang", "es", "services", "stop", "llama")),
		0, "✓ El servicio de modelos de IA está detenido.\n", "… Deteniendo el servicio de modelos de IA\n")
	checkRun(t, newServicesRig(t).run(mgArgs("--lang", "es", "services", "start", "whisper")),
		0, "✓ El servicio de voz está en marcha.\n", "… Iniciando el servicio de voz\n")
	got := newServicesRig(t).run(mgArgs("--lang", "es", "services", "start", "llama"))
	if got.code != 0 || !strings.Contains(flat(got.out), "El servicio de modelos de IA está en marcha. Qwen 3.5 9B está cargado.") {
		t.Fatalf("%+v", got)
	}
}

func TestServicesRestartIsTheRestartCommand(t *testing.T) {
	a := newServicesRig(t).run(mgArgs("services", "restart", "llama"))
	b := newServicesRig(t).run(mgArgs("restart", "llama"))
	if a != b {
		t.Fatalf("services restart and restart differ:\n%+v\n%+v", a, b)
	}
}

func TestServicesForceAndJSONAndQuiet(t *testing.T) {
	r := newServicesRig(t)
	r.run(mgArgs("services", "stop", "llama", "--force"))
	if b := r.lastBody(http.MethodPost, "/services/llama/stop"); b != `{"force":true}` {
		t.Errorf("body %s", b)
	}
	r = newServicesRig(t)
	r.run(mgArgs("services", "restart", "llama", "--force"))
	if b := r.lastBody(http.MethodPost, "/services/llama/restart"); b != `{"force":true}` {
		t.Errorf("body %s", b)
	}
	checkRun(t, newServicesRig(t).run(mgArgs("--json", "services", "start", "llama")), 0,
		`{"name":"llama","active":"active","restored_models":["`+qwenID+`"],"op_id":"op_s1"}`+"\n", "")
	checkRun(t, newServicesRig(t).run(mgArgs("-q", "services", "stop", "kiwix")), 0, "", "")
}

func TestServicesStopLlamaInSurvivalNeedsForce(t *testing.T) {
	r := newServicesRig(t)
	r.agent().RespondError(http.MethodPost, "/services/llama/stop", 409, "LAI-AGT-023", "Not allowed in survival mode.", "Add --force to stop it anyway.",
		map[string]any{"mode": "survival", "requires": "force"})
	got := r.run(mgArgs("services", "stop", "llama"))
	if got.code != ExitRefused || got.out != "" || !strings.Contains(got.errOut, "[LAI-AGT-023]") {
		t.Fatalf("%+v", got)
	}
	if b := r.lastBody(http.MethodPost, "/services/llama/stop"); b != `{}` {
		t.Errorf("the first try carries no force: %s", b)
	}
	// With --force the agent accepts it.
	r.agent().Respond(http.MethodPost, "/services/llama/stop", 200, `{"name":"llama","active":"inactive","restored_models":[],"op_id":"op_s9"}`)
	checkRun(t, r.run(mgArgs("services", "stop", "llama", "--force")), 0, "✓ The AI model service is stopped.\n", "… Stopping the AI model service\n")
}

func TestServicesActionFailures(t *testing.T) {
	t.Run("an unknown service is exit 2 before any request", func(t *testing.T) {
		r := newServicesRig(t)
		for _, a := range [][]string{{"services", "start", "nginx"}, {"services", "stop"}, {"services", "restart", "llama", "kiwix"}, {"services", "start", "../llama"}} {
			if got := r.run(mgArgs(a...)); got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
	failures := []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"the agent does not know the service", 404, "LAI-AGT-021", ExitNotFound},
		{"another operation runs", 409, "LAI-AGT-022", ExitRefused},
		{"the service did not come back", 502, "LAI-SVC-002", ExitFailed},
		{"the route deadline", 504, "LAI-AGT-099", ExitTimeout},
		{"not allowed", 403, "LAI-SEC-013", ExitDenied},
	}
	for _, tc := range failures {
		for _, action := range []string{"start", "stop"} {
			t.Run(action+": "+tc.name, func(t *testing.T) {
				r := newServicesRig(t)
				r.agent().RespondError(http.MethodPost, "/services/whisper/"+action, tc.status, tc.code, "No.", "Fix it.", nil)
				got := r.run(mgArgs("services", action, "whisper"))
				if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
					t.Fatalf("%+v", got)
				}
			})
		}
	}
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("services", "start", "llama"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the list still works", func(t *testing.T) {
		r := newServicesRig(t)
		r.agent().Respond(http.MethodGet, "/services", 200, `{"services":[{"name":"llama","active":"active","sub":"running","healthy":true}]}`)
		got := r.run(mgArgs("services"))
		if got.code != 0 || !strings.Contains(got.out, "llama") {
			t.Fatalf("%+v", got)
		}
	})
}

func TestServicesGolden(t *testing.T) {
	checkGolden(t, "services.start.en", runResult(newServicesRig(t).run(mgArgs("services", "start", "llama"))))
	checkGolden(t, "services.stop.es", runResult(newServicesRig(t).run(mgArgs("--lang", "es", "services", "stop", "kiwix"))))
	checkGolden(t, "services.restart.en", runResult(newServicesRig(t).run(mgArgs("services", "restart", "whisper"))))
}
