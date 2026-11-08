package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// `aictl ark budget` (issue #435): the fix of LAI-SYN-041 names it instead of node.yaml.

const profileFromProfile = `{"profile":"small-8gb","overrides":{"add_models":[],"add_content":["maps-alpha"],"remove_models":[],"remove_content":[],"budget_bytes":null},` +
	`"effective":{"models":[],"content":[],"planned_bytes":180000000000,"budget_bytes":220000000000,"skipped":[{"kind":"content","id":"maps-beta","reason":"budget","size_bytes":1}]}}`

const profileOverridden = `{"profile":"small-8gb","overrides":{"add_models":[],"add_content":[],"remove_models":[],"remove_content":[],"budget_bytes":500000000000},` +
	`"effective":{"models":[],"content":[],"planned_bytes":200000000000,"budget_bytes":500000000000,"skipped":[]}}`

func TestParseBudget(t *testing.T) {
	for in, want := range map[string]int64{
		"500G": 500_000_000_000, "500 GB": 500_000_000_000, "1.5T": 1_500_000_000_000, "2tb": 2_000_000_000_000, "1g": 1_000_000_000,
	} {
		if got, ok := parseBudget(in); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "500", "0G", "0.5G", "-1G", "500M", "1e3G", "G", "2000T", "500 G B"} {
		if got, ok := parseBudget(in); ok {
			t.Errorf("%q accepted as %d", in, got)
		}
	}
}

func TestArkBudgetShow(t *testing.T) {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/profile", 200, profileFromProfile)
	got := r.run(mgArgs("ark", "budget"))
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	out := unwrap(got.out + got.errOut)
	for _, want := range []string{
		"i The offline content may use up to 220 GB of this computer's disk (from the profile small-8gb).",
		"i The plan uses 180 GB.",
		"! 1 item(s) do not fit. To make room, raise the budget, for example `aictl ark budget 400G`.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	j := r.run(mgArgs("--json", "ark", "budget"))
	var doc map[string]any
	if j.code != 0 || json.Unmarshal([]byte(j.out), &doc) != nil || doc["budget_bytes"] != 220000000000.0 || doc["overridden"] != false || doc["skipped"] != 1.0 {
		t.Errorf("json %+v", j)
	}
	if n := r.agent().Count("/profile"); n != 2 {
		t.Errorf("requests %d", n)
	}
}

func TestArkBudgetSet(t *testing.T) {
	t.Run("sets the override and keeps the other keys", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/profile", 200, profileFromProfile)
		r.agent().Respond(http.MethodPut, "/profile", 200, profileOverridden)
		got := r.run(mgArgs("ark", "budget", "500G"))
		if got.code != 0 || !strings.Contains(unwrap(got.out), "✓ The offline content may now use up to 500 GB of this computer's disk.") ||
			!strings.Contains(unwrap(got.out), "Nothing was deleted. Run `aictl ark sync` to fetch what now fits.") {
			t.Fatalf("%+v", got)
		}
		const body = `{"profile_overrides":{"add_content":["maps-alpha"],"add_models":[],"budget_bytes":500000000000,"remove_content":[],"remove_models":[]}}`
		if b := r.lastBody(http.MethodPut, "/profile"); b != body {
			t.Errorf("body %s", b)
		}
	})
	t.Run("default removes the override", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/profile", 200, profileOverridden)
		r.agent().Respond(http.MethodPut, "/profile", 200, profileFromProfile)
		got := r.run(mgArgs("--lang", "es", "ark", "budget", "default"))
		if got.code != 0 || !strings.Contains(got.out, "El presupuesto volvió a ser el del perfil: 220 GB.") {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPut, "/profile"); !strings.Contains(b, `"budget_bytes":null`) {
			t.Errorf("body %s", b)
		}
	})
	t.Run("nothing to change: no write", func(t *testing.T) {
		for _, tc := range []struct{ profile, arg string }{{profileOverridden, "500G"}, {profileFromProfile, "default"}} {
			r := newMgRig(t, fake.Options{})
			r.agent().Respond(http.MethodGet, "/profile", 200, tc.profile)
			got := r.run(mgArgs("ark", "budget", tc.arg))
			if got.code != 0 || !strings.Contains(got.out, "already") {
				t.Errorf("%s: %+v", tc.arg, got)
			}
			for _, q := range r.agent().Requests() {
				if q.Method == http.MethodPut {
					t.Errorf("%s: wrote", tc.arg)
				}
			}
		}
	})
	t.Run("a bad size is exit 2 before any request", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		for _, args := range [][]string{{"ark", "budget", "500"}, {"ark", "budget", "lots"}, {"ark", "budget", "1G", "2G"}} {
			got := r.run(mgArgs(args...))
			if got.code != ExitUsage || !strings.Contains(got.errOut, "LAI-CLI-005") {
				t.Errorf("%v: %+v", args, got)
			}
		}
		if n := len(r.agent().Requests()); n != 0 {
			t.Errorf("%d requests", n)
		}
	})
	t.Run("an operator is asked for the PIN", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Respond(http.MethodGet, "/profile", 200, profileFromProfile)
		a.h.agent.Handle(http.MethodPut, "/profile", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, profileOverridden)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("ark", "budget", "500G").withTerm(testPIN))
		if got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v verifies %d", got, a.verifies())
		}
		secretFree(t, testPIN, got.out, got.errOut)
	})
}
