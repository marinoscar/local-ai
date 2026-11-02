package cli

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// --- catalog refresh --------------------------------------------------------------------------------

func TestCatalogRefresh(t *testing.T) {
	const changed = `{"version":"2026.11.02-1","previous":"2026.10.20-1","changed":true,"source":"cloud"}`
	refresh := func(t *testing.T, body string, a ...string) (*mgRig, followOut) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/catalog/refresh", 200, body)
		return r, r.run(mgArgs(append(a, "catalog", "refresh")...))
	}
	t.Run("updated", func(t *testing.T) {
		r, got := refresh(t, changed)
		checkRun(t, got, 0, "✓ Catalog updated: 2026.10.20-1 → 2026.11.02-1 (from the cloud).\n", "")
		if b := r.lastBody(http.MethodPost, "/catalog/refresh"); b != "{}" {
			t.Errorf("body %q", b)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		_, got := refresh(t, changed, "--lang", "es")
		checkRun(t, got, 0, "✓ Catálogo actualizado: 2026.10.20-1 → 2026.11.02-1 (desde la nube).\n", "")
	})
	t.Run("a terminal that is not UTF-8 gets ASCII symbols", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/catalog/refresh", 200, changed)
		got := r.run(mgArgs("catalog", "refresh").lang("C"))
		checkRun(t, got, 0, "[ok] Catalog updated: 2026.10.20-1 -> 2026.11.02-1 (from the cloud).\n", "")
	})
	t.Run("from GitHub, with no earlier version", func(t *testing.T) {
		_, got := refresh(t, `{"version":"2026.11.02-1","previous":null,"changed":true,"source":"github"}`)
		checkRun(t, got, 0, "✓ Catalog updated to 2026.11.02-1 (from GitHub).\n", "")
	})
	t.Run("nothing new", func(t *testing.T) {
		_, got := refresh(t, `{"version":"2026.11.02-1","previous":"2026.11.02-1","changed":false,"source":"cloud"}`)
		checkRun(t, got, 0, "i The catalog is already up to date (2026.11.02-1).\n", "")
	})
	t.Run("--json", func(t *testing.T) {
		_, got := refresh(t, changed, "--json")
		checkRun(t, got, 0, changed+"\n", "")
	})
	t.Run("offline and survival: exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPost, "/catalog/refresh", 409, "LAI-AGT-023", "This action is not allowed in the current mode.",
			"Switch mode first (`aictl mode online`).", nil)
		got := r.run(mgArgs("catalog", "refresh"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-023]") || !strings.Contains(got.errOut, "aictl mode online") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a rejected catalog: exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPost, "/catalog/refresh", 422, "LAI-CAT-002", "New catalog failed validation, previous kept.", "Report it.", nil)
		if got := r.run(mgArgs("catalog", "refresh")); got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-CAT-002]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent is down: exit 3", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		if got := r.run(mgArgs("catalog", "refresh")); got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the 120 s deadline is a timeout, not a hang", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Handle(http.MethodPost, "/catalog/refresh", func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-req.Context().Done():
			case <-time.After(3 * time.Second):
			}
		})
		got := r.run(mgArgs("--timeout", "100ms", "catalog", "refresh"))
		if got.code != ExitTimeout {
			t.Fatalf("%+v", got)
		}
	})
}
