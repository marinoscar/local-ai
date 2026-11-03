package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// --- mode (show) ------------------------------------------------------------------------------------

func fixedModeClock(t *testing.T) {
	t.Helper()
	oldNow, oldLocal := modeNow, modeLocal
	t.Cleanup(func() { modeNow, modeLocal = oldNow, oldLocal })
	modeNow = func() time.Time { return time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC) }
	modeLocal = time.UTC
}

const modeOnline = `{"mode":"online","since":"2026-11-01T22:10:00Z","transition":null,"allowed_targets":["offline","survival"],` +
	`"leave_requires_admin":false,"connectivity":"full","suggest_survival":false}`

func TestModeShow(t *testing.T) {
	fixedModeClock(t)
	show := func(body string, a ...string) followOut {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/mode", 200, body)
		return r.run(mgArgs(append(a, "mode")...))
	}
	t.Run("en", func(t *testing.T) {
		checkRun(t, show(modeOnline), 0,
			"Mode  online (since yesterday 22:10)\n      Switch with: aictl mode offline | aictl mode survival\n", "")
	})
	t.Run("es", func(t *testing.T) {
		checkRun(t, show(modeOnline, "--lang", "es"), 0,
			"Modo  en línea (desde ayer 22:10)\n      Cambie con: aictl mode offline | aictl mode survival\n", "")
	})
	t.Run("-v adds the absolute time", func(t *testing.T) {
		got := show(modeOnline, "-v")
		if !strings.HasPrefix(got.out, "Mode  online (since yesterday 22:10 (2026-11-01T22:10:00Z))\n") {
			t.Fatalf("%q", got.out)
		}
	})
	t.Run("--json is the body", func(t *testing.T) {
		checkRun(t, show(modeOnline, "--json"), 0, modeOnline+"\n", "")
	})
	t.Run("a change under way, survival that needs the PIN, a suggestion", func(t *testing.T) {
		body := `{"mode":"survival","since":"2026-11-02T14:00:00Z","transition":{"target":"online","step":"tailscale_on","started_at":"2026-11-02T14:59:50Z"},` +
			`"allowed_targets":["online","offline"],"leave_requires_admin":true,"connectivity":"not_checked","suggest_survival":true}`
		want := "Mode  survival (since today 14:00)\n" +
			"      Changing to online: Publishing this node on the tailnet\n" +
			"      Switch with: aictl mode online | aictl mode offline\n" +
			"      Leaving this mode needs the admin PIN (not with sudo).\n" +
			"i No internet for 30 minutes. Everything here still works. To save power and\n" +
			"  keep the essentials, run `aictl mode survival`.\n"
		checkRun(t, show(body), 0, want, "")
	})
	t.Run("an old date is shown as a date", func(t *testing.T) {
		got := show(strings.Replace(modeOnline, "2026-11-01T22:10:00Z", "2026-10-20T08:05:00Z", 1))
		if !strings.HasPrefix(got.out, "Mode  online (since 2026-10-20 08:05)\n") {
			t.Fatalf("%q", got.out)
		}
	})
	t.Run("failures", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status int
			exit   int
		}{{"unauthenticated", 401, ExitDenied}, {"forbidden", 403, ExitDenied}, {"bug", 500, ExitFailed}} {
			r := newMgRig(t, fake.Options{})
			r.agent().RespondError(http.MethodGet, "/mode", tc.status, "LAI-SEC-001", "No.", "Fix it.", nil)
			if got := r.run(mgArgs("mode")); got.code != tc.exit || got.out != "" {
				t.Errorf("%s: %+v", tc.name, got)
			}
		}
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		if got := r.run(mgArgs("mode")); got.code != ExitUnreachable || !strings.Contains(got.errOut, "[LAI-CLI-001]") {
			t.Errorf("down: %+v", got)
		}
	})
	t.Run("the show deadline is 10 s but --timeout wins", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Handle(http.MethodGet, "/mode", func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-req.Context().Done():
			case <-time.After(3 * time.Second):
			}
		})
		got := r.run(mgArgs("--timeout", "100ms", "mode"))
		if got.code != ExitTimeout || !strings.Contains(got.errOut, "[LAI-CLI-004]") {
			t.Fatalf("%+v", got)
		}
	})
}

func TestModeUsage(t *testing.T) {
	for _, a := range [][]string{{"mode", "sleep"}, {"mode", "online", "offline"}} {
		r := newMgRig(t, fake.Options{})
		got := r.run(mgArgs(a...))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
			t.Errorf("%v: %+v", a, got)
		}
	}
}

func TestModeStepLabels(t *testing.T) {
	cases := []struct{ step, en, es string }{
		{"link_stop", "Stopping cloud connection", "Deteniendo la conexión con la nube"},
		{"sync_cancel", "Stopping the running sync", "Deteniendo la sincronización en curso"},
		{"tailscale_off", "Unpublishing this node from the tailnet", "Quitando este nodo de la tailnet"},
		{"survival_models", "Starting the models", "Iniciando los modelos"},
		{"survival_services", "Starting the library and speech", "Iniciando la biblioteca y la voz"},
		{"tailscale_on", "Publishing this node on the tailnet", "Publicando este nodo en la tailnet"},
		{"link_start", "Connecting to the cloud", "Conectando con la nube"},
		{"prober_stop", "", ""}, {"persist", "", ""}, {"emit", "", ""}, {"sync_catchup", "", ""}, {"nope", "", ""},
	}
	for _, tc := range cases {
		if got := modeStepLabel(LangEN, tc.step); got != tc.en {
			t.Errorf("%s en = %q", tc.step, got)
		}
		if got := modeStepLabel(LangES, tc.step); got != tc.es {
			t.Errorf("%s es = %q", tc.step, got)
		}
	}
}

// --- mode survival ------------------------------------------------------------------------------------

const modelNames = `{"models":[{"id":"qwen3.5-9b-q4km","display_name":"Qwen 3.5 9B"},{"id":"small-vlm-q4km","display_name":"Tiny fallback model"}]}`

// survivalRig is a fake agent whose POST /mode/survival publishes the events of a survival
// transition from inside the handler (they reach aictl before it knows the op id and must be held).
type survivalRig struct {
	*mgRig
	body      string // what the POST answered with (set per test)
	status    int
	published func()
}

func newSurvivalRig(t *testing.T) *survivalRig {
	t.Helper()
	r := &survivalRig{mgRig: newMgRig(t, fake.Options{}), status: 202, body: `{"op_id":"op_1","target":"survival"}`}
	ev := r.events()
	r.agent().Respond(http.MethodGet, "/models", 200, modelNames)
	r.agent().Handle(http.MethodPost, "/mode/survival", func(w http.ResponseWriter, _ *http.Request) {
		if r.published != nil {
			r.published()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LocalAI-Op-Id", "op_1")
		w.WriteHeader(r.status)
		io.WriteString(w, r.body)
	})
	r.published = func() {
		step := func(name, state string) {
			ev.Publish(events.TypeModeStep, "op_1", events.ModeStep{Target: "survival", Step: name, State: state})
		}
		for _, s := range []string{"link_stop", "sync_cancel", "prober_stop", "tailscale_off"} {
			step(s, "started")
			step(s, "done")
		}
		step("survival_models", "started")
		p := 0.4
		ev.Publish(events.TypeModelLoading, "op_m1", events.ModelLoading{Model: "qwen3.5-9b-q4km", OpID: "op_m1", Progress: &p})
		ev.Publish(events.TypeModelLoading, "op_m2", events.ModelLoading{Model: "small-vlm-q4km", OpID: "op_m2"})
		step("survival_models", "done")
		step("survival_services", "started")
		step("survival_services", "done")
		step("persist", "done")
		ev.Publish(events.TypeModeChanged, "op_1", events.ModeChanged{From: "online", To: "survival", OpID: "op_1",
			Warnings: []events.Warning{{Code: "LAI-LLM-020", Message: "The preferred model failed to start; using the next one."}}})
	}
	return r
}

func TestModeSurvival(t *testing.T) {
	t.Run("human, off a terminal: one line per step, then the portal and the warning", func(t *testing.T) {
		r := newSurvivalRig(t)
		got := r.run(mgArgs("mode", "survival"))
		wantErr := "… Stopping cloud connection\n" +
			"… Stopping the running sync\n" +
			"… Unpublishing this node from the tailnet\n" +
			"… Starting Qwen 3.5 9B\n" +
			"… Starting Tiny fallback model\n" +
			"… Starting the library and speech\n" +
			// The style guide wraps at 80 columns; the spec's example line is 91 wide.
			"! In survival mode the preferred model did not start; the next one is used.\n" +
			"  [LAI-LLM-020]\n" +
			"  → Fix: Nothing to do now. Later, check `aictl logs --unit llama`.\n"
		wantOut := "✓ Survival mode is on. Open " + r.agent().URL + " for the Survival Portal.\n"
		checkRun(t, got, 0, wantOut, wantErr)
		if body := r.lastBody(http.MethodPost, "/mode/survival"); body != "{}" {
			t.Errorf("body %q", body)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newSurvivalRig(t)
		got := r.run(mgArgs("--lang", "es", "mode", "survival"))
		for _, want := range []string{"… Deteniendo la conexión con la nube\n", "… Iniciando Qwen 3.5 9B\n", "… Iniciando la biblioteca y la voz\n",
			"! En modo supervivencia no arrancó el modelo preferido; se usa el siguiente.\n  [LAI-LLM-020]\n  → Solución: No hay que hacer nada ahora."} {
			if !strings.Contains(got.errOut, want) {
				t.Errorf("stderr misses %q:\n%s", want, got.errOut)
			}
		}
		if got.code != 0 || !strings.HasPrefix(got.out, "✓ El modo supervivencia está activo. Abra ") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json is the final document and stderr stays empty", func(t *testing.T) {
		r := newSurvivalRig(t)
		got := r.run(mgArgs("--json", "mode", "survival"))
		// The document is a map: its keys come out sorted.
		want := `{"from":"online","mode":"survival","op_id":"op_1","warnings":[{"code":"LAI-LLM-020","message":"The preferred model failed to start; using the next one."}]}` + "\n"
		checkRun(t, got, 0, want, "")
	})
	t.Run("--quiet prints no progress and no result line", func(t *testing.T) {
		r := newSurvivalRig(t)
		got := r.run(mgArgs("-q", "mode", "survival"))
		if got.code != 0 || got.out != "" || strings.Contains(got.errOut, "…") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--reapply is in the body", func(t *testing.T) {
		r := newSurvivalRig(t)
		if got := r.run(mgArgs("mode", "survival", "--reapply")); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if body := r.lastBody(http.MethodPost, "/mode/survival"); body != `{"reapply":true}` {
			t.Errorf("body %q", body)
		}
	})
	t.Run("already in survival", func(t *testing.T) {
		r := newSurvivalRig(t)
		r.published, r.status, r.body = nil, 200, `{"mode":"survival","already":true}`
		checkRun(t, r.run(mgArgs("mode", "survival")), 0, "i Already in survival mode.\n", "")
		got := r.run(mgArgs("--json", "mode", "survival"))
		checkRun(t, got, 0, `{"mode":"survival","already":true}`+"\n", "")
	})
	t.Run("queued while the agent starts: say so and keep following", func(t *testing.T) {
		r := newSurvivalRig(t)
		r.body = `{"op_id":"op_1","target":"survival","queued":true}`
		got := r.run(mgArgs("mode", "survival"))
		if got.code != 0 || !strings.HasPrefix(got.errOut, "… Survival mode will start as soon as the agent is ready.\n") ||
			!strings.Contains(got.errOut, "… Stopping cloud connection") || !strings.HasPrefix(got.out, "✓ Survival mode is on.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--no-wait returns when it has started", func(t *testing.T) {
		r := newSurvivalRig(t)
		got := r.run(mgArgs("mode", "survival", "--no-wait"))
		checkRun(t, got, 0, "i The change to survival mode has started. Check it with `aictl mode`.\n", "")
		if n := len(r.events().Connections()); n != 0 {
			t.Errorf("events connections = %d, want none", n)
		}
	})
	t.Run("--no-wait while queued", func(t *testing.T) {
		r := newSurvivalRig(t)
		r.body = `{"op_id":"op_1","target":"survival","queued":true}`
		got := r.run(mgArgs("mode", "survival", "--no-wait"))
		if got.code != 0 || !strings.Contains(got.out, "starts as soon as the agent is ready") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("another change is running: the agent preempts, but a refusal is exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().RespondError(http.MethodPost, "/mode/survival", 409, "LAI-AGT-022", "Another change is running.", "Wait.", nil)
		if got := r.run(mgArgs("mode", "survival")); got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-022]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent is down: LAI-CLI-001 and nothing else can help", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("mode", "survival"))
		if got.code != ExitUnreachable || !strings.Contains(got.errOut, "[LAI-CLI-001]") ||
			!strings.Contains(got.errOut, "sudo systemctl start localai-agent") {
			t.Fatalf("%+v", got)
		}
	})
}

// Ctrl-C stops the waiting, not the change: exit 130 and no request but the one that started it.
func TestModeSurvivalCtrlC(t *testing.T) {
	r := newSurvivalRig(t)
	// The transition starts and never ends. Its first event is held until aictl knows the op id, so the
	// line it draws proves that the request was answered and the wait began: pressing Ctrl-C before
	// that would end the request itself (still exit 130, but not the line this test is about).
	r.published = func() {
		r.ev.Publish(events.TypeModeStep, "op_1", events.ModeStep{Target: "survival", Step: "link_stop", State: "started"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := mgArgs("mode", "survival")
	o.ctx = ctx
	ch, stderr := r.runAsync(o)
	untilTrue(t, "the first step", func() bool { return strings.Contains(stderr.String(), "… Stopping cloud connection") })
	cancel()
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.errOut, "Still running in the background") {
		t.Fatalf("%+v", got)
	}
	for _, q := range r.requestLines() {
		if q != "POST /mode/survival" && q != "GET /models" {
			t.Errorf("unexpected request %q after Ctrl-C", q)
		}
	}
}

// A person who is not an operator can neither open the socket nor read the token, and the survival
// route needs neither: it is sent over TCP without an Authorization header.
func TestModeSurvivalWithoutSocketAndToken(t *testing.T) {
	r := newSurvivalRig(t)
	r.mgRig.h.agent.CloseSocket()
	r.h.unreadable()
	// The event stream needs a credential, which this person has not.
	r.agent().RespondError(http.MethodGet, "/events", 401, api.CodeUnauthenticated, "Sign in.", "Use a token.", nil)
	r.published = nil
	got := r.run(mgArgs("mode", "survival"))
	want := "i Survival mode is starting. In a minute, open " + r.agent().URL + " for the\n  Survival Portal.\n"
	checkRun(t, got, 0, want, "")
	var post *fake.Request
	for _, q := range r.agent().Requests() {
		if q.Method == http.MethodPost && q.Path == "/mode/survival" {
			q := q
			post = &q
		}
		if q.Via != "tcp" {
			t.Errorf("%s %s went over %s", q.Method, q.Path, q.Via)
		}
		if q.Header.Get("Authorization") != "" {
			t.Errorf("%s %s carried Authorization", q.Method, q.Path)
		}
	}
	if post == nil {
		t.Fatal("the survival request was not sent")
	}

	// Any other route still needs the credential: no anonymous fallback for them.
	r2 := newMgRig(t, fake.Options{})
	r2.agent().CloseSocket()
	r2.h.unreadable()
	if got := r2.run(mgArgs("mode", "offline")); got.code == 0 || len(r2.requestLines()) != 0 {
		t.Fatalf("mode offline without a credential: %+v, requests %v", got, r2.requestLines())
	}
}

// While the agent starts, its event stream may answer "starting"; survival is still requested, and
// the agent queues it.
func TestModeSurvivalWhileTheAgentStarts(t *testing.T) {
	r := newSurvivalRig(t)
	r.published = nil
	r.body = `{"op_id":"op_1","target":"survival","queued":true}`
	r.agent().RespondError(http.MethodGet, "/events", 503, api.CodeStarting, "Starting.", "Wait.", nil)
	got := r.run(mgArgs("mode", "survival"))
	if got.code != 0 || !strings.Contains(got.out, "Survival mode was requested. It starts as soon as the agent is ready.") {
		t.Fatalf("%+v", got)
	}
	if r.agent().Count("/mode/survival") != 1 {
		t.Errorf("survival requests = %d", r.agent().Count("/mode/survival"))
	}
}

// --- mode online / offline ----------------------------------------------------------------------------

func TestModeOnlineOffline(t *testing.T) {
	done := `{"op_id":"op_9","from":"survival","mode":"online","warnings":[]}`
	t.Run("wait: the 200 body is the result", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().Respond(http.MethodPost, "/mode", 200, done)
		checkRun(t, r.run(mgArgs("mode", "online")), 0, "✓ Online mode is on.\n", "")
		if body := r.lastBody(http.MethodPost, "/mode"); body != `{"target":"online","wait":true}` {
			t.Errorf("body %s", body)
		}
		checkRun(t, r.run(mgArgs("--json", "mode", "online")), 0, done+"\n", "")
	})
	t.Run("offline in Spanish", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().Respond(http.MethodPost, "/mode", 200, `{"op_id":"op_9","from":"online","mode":"offline","warnings":[]}`)
		checkRun(t, r.run(mgArgs("--lang", "es", "mode", "offline")), 0, "✓ El modo sin conexión está activo.\n", "")
	})
	t.Run("--reapply and --no-wait are in the body", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().Respond(http.MethodPost, "/mode", 202, `{"op_id":"op_9","from":"online","target":"offline","transition":{"step":"link_stop","started_at":"2026-11-02T15:04:05Z"}}`)
		got := r.run(mgArgs("mode", "offline", "--no-wait", "--reapply"))
		checkRun(t, got, 0, "i The change to offline mode has started. Check it with `aictl mode`.\n", "")
		if body := r.lastBody(http.MethodPost, "/mode"); body != `{"reapply":true,"target":"offline","wait":false}` {
			t.Errorf("body %s", body)
		}
	})
	t.Run("already there", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().Respond(http.MethodPost, "/mode", 200, `{"mode":"offline","already":true}`)
		checkRun(t, r.run(mgArgs("mode", "offline")), 0, "i Already in offline mode.\n", "")
	})
	t.Run("a warning that came with the change", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().Respond(http.MethodPost, "/mode", 200, `{"op_id":"op_9","from":"survival","mode":"online","warnings":[{"code":"LAI-NET-003","message":"Tailscale is not signed in."}]}`)
		got := r.run(mgArgs("mode", "online"))
		if got.code != 0 || !strings.Contains(got.errOut, "[LAI-NET-003]") || !strings.HasPrefix(got.out, "✓ Online mode is on.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a 202 is followed through the events", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		ev := r.events()
		r.agent().Handle(http.MethodPost, "/mode", func(w http.ResponseWriter, _ *http.Request) {
			ev.Publish(events.TypeModeStep, "op_9", events.ModeStep{Target: "online", Step: "tailscale_on", State: "started"})
			ev.Publish(events.TypeModeStep, "op_9", events.ModeStep{Target: "online", Step: "link_start", State: "started"})
			ev.Publish(events.TypeModeChanged, "op_9", events.ModeChanged{From: "offline", To: "online", OpID: "op_9"})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(202)
			io.WriteString(w, `{"op_id":"op_9","from":"offline","target":"online","transition":{"step":"tailscale_on","started_at":"2026-11-02T15:04:05Z"}}`)
		})
		got := r.run(mgArgs("mode", "online"))
		checkRun(t, got, 0, "✓ Online mode is on.\n", "… Publishing this node on the tailnet\n… Connecting to the cloud\n")
	})
	t.Run("another change is running: exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().RespondError(http.MethodPost, "/mode", 409, "LAI-AGT-022", "Another change is running.", "Wait.", map[string]any{"op_id": "op_3"})
		if got := r.run(mgArgs("mode", "offline")); got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-022]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("leaving survival without permission: exit 4", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().RespondError(http.MethodPost, "/mode", 403, "LAI-SEC-002", "Leaving survival mode needs permission.", "Ask an administrator.", nil)
		if got := r.run(mgArgs("mode", "online")); got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-SEC-002]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json failure is the envelope", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.events()
		r.agent().RespondError(http.MethodPost, "/mode", 409, "LAI-AGT-022", "Another change is running.", "Wait.", nil)
		got := r.run(mgArgs("--json", "mode", "offline"))
		if got.code != ExitRefused || got.errOut != "" || !strings.Contains(got.out, `"code":"LAI-AGT-022"`) {
			t.Fatalf("%+v", got)
		}
	})
}

// An operator over a TCP token is asked for the PIN when leaving survival; the PIN never appears.
func TestModeLeaveSurvivalAsksForThePIN(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Handle(http.MethodPost, "/mode", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"op_id":"op_9","from":"survival","mode":"online","warnings":[]}`)
			return
		}
		fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
	})
	r := &mgRig{t: t, h: a.h}
	r.events()
	got := r.run(mgArgs("mode", "online").withTerm(testPIN))
	checkRun(t, got, 0, "✓ Online mode is on.\n", "Admin PIN: \n")
	secretFree(t, testPIN, got.out, got.errOut)

	t.Run("no terminal: LAI-CLI-008", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.RespondError(http.MethodPost, "/mode", 403, api.CodeAdminRequired, "Needs the PIN.", "Enter it.", map[string]any{"pin_required": true})
		r := &mgRig{t: t, h: a.h}
		r.events()
		if got := r.run(mgArgs("mode", "online")); got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-CLI-008]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("survival never asks", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Respond(http.MethodPost, "/mode/survival", 200, `{"mode":"survival","already":true}`)
		r := &mgRig{t: t, h: a.h}
		r.events()
		got := r.run(mgArgs("mode", "survival"))
		if got.code != 0 || a.verifies() != 0 {
			t.Fatalf("%+v, verifies %d", got, a.verifies())
		}
	})
}
