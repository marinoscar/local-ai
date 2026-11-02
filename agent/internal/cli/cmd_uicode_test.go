package cli

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

func TestUICode(t *testing.T) {
	old := uiCodeNow
	t.Cleanup(func() { uiCodeNow = old })
	uiCodeNow = func() time.Time { return time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC) }
	const body = `{"code":"482913","expires_at":"2026-11-02T15:09:05Z"}`
	newRig := func(t *testing.T, body string) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/ui-code", 200, body)
		return r
	}
	t.Run("en", func(t *testing.T) {
		r := newRig(t, body)
		got := r.run(mgArgs("ui-code"))
		checkRun(t, got, 0, "Login code  482 913   (valid for 5 minutes, one use)\nOpen "+r.agent().URL+" in a browser and type this code.\n", "")
		if b := r.lastBody(http.MethodPost, "/ui-code"); b != "{}" {
			t.Errorf("body %q", b)
		}
	})
	t.Run("es", func(t *testing.T) {
		r := newRig(t, body)
		got := r.run(mgArgs("--lang", "es", "ui-code"))
		checkRun(t, got, 0, "Código de acceso  482 913   (válido por 5 minutos, un solo uso)\nAbra "+r.agent().URL+" en un navegador y escriba este código.\n", "")
	})
	t.Run("the code is the essential result: -q keeps it", func(t *testing.T) {
		got := newRig(t, body).run(mgArgs("-q", "ui-code"))
		if got.code != 0 || !strings.HasPrefix(got.out, "Login code  482 913") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("one minute", func(t *testing.T) {
		got := newRig(t, `{"code":"482913","expires_at":"2026-11-02T15:05:05Z"}`).run(mgArgs("ui-code"))
		if !strings.Contains(got.out, "(valid for 1 minute, one use)") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json", func(t *testing.T) {
		checkRun(t, newRig(t, body).run(mgArgs("--json", "ui-code")), 0, body+"\n", "")
	})
	t.Run("refusals and failures", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			exit   int
		}{{401, ExitDenied}, {403, ExitDenied}, {500, ExitFailed}} {
			r := newMgRig(t, fake.Options{})
			r.agent().RespondError(http.MethodPost, "/ui-code", tc.status, api.CodeUnauthenticated, "No.", "Join the group.", nil)
			if got := r.run(mgArgs("ui-code")); got.code != tc.exit || got.out != "" {
				t.Errorf("%d: %+v", tc.status, got)
			}
		}
		down := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		if got := down.run(mgArgs("ui-code")); got.code != ExitUnreachable {
			t.Errorf("down: %+v", got)
		}
		bad := newRig(t, `{"expires_at":"2026-11-02T15:09:05Z"}`)
		if got := bad.run(mgArgs("ui-code")); got.code != ExitFailed || !strings.Contains(got.errOut, "[LAI-CLI-009]") {
			t.Errorf("no code: %+v", got)
		}
	})
}
