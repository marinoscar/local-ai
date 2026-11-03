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

// Tests of `aictl ark prune` (aictl.md section 4.10, ark-sync.md section 4.10): the list first, the
// typed DELETE, --yes, --dry-run, the admin PIN, the sentence for a last copy that was kept, and the
// rule that no flag can ask for it to be deleted.

const (
	pruneList = `{"deleted":[
		{"root":"internal","item":"osm-north-america","version":"20260501","bytes":11200000000,"kind":"orphan"},
		{"root":"internal","item":"qwen-x","version":"rev1","bytes":3100000000,"kind":"trash"}],
		"refused":[{"root":"internal","item":"gutenberg-en","version":"20260501","reason":"last_verified_copy","kind":"orphan"}],
		"freed_bytes":14300000000}`
	pruneNothing = `{"deleted":[],"refused":[{"root":"internal","item":"gutenberg-en","version":"20260501","reason":"last_verified_copy","kind":"orphan"}],"freed_bytes":0}`
)

// pruneRig answers POST /ark/prune with the same list for a dry run and for the real request, and
// remembers each request body.
func newPruneRig(t *testing.T, answer string) (*mgRig, *[]map[string]any) {
	t.Helper()
	r := newMgRig(t, fake.Options{})
	var bodies []map[string]any
	r.agent().Handle(http.MethodPost, "/ark/prune", func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answer)
	})
	return r, &bodies
}

func countPrunes(r *mgRig) int {
	n := 0
	for _, l := range r.requestLines() {
		if l == "POST /ark/prune" {
			n++
		}
	}
	return n
}

func TestArkPruneDryRun(t *testing.T) {
	r, bodies := newPruneRig(t, pruneList)
	got := r.run(mgArgs("ark", "prune", "--orphans", "--trash", "--dry-run"))
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	for _, want := range []string{
		"Would delete 2 item(s) (14.3 GB):", "osm-north-america", "20260501", "11.2 GB", "not in the profile", "qwen-x", "old version",
		"This is the only copy of gutenberg-en on this computer; it was kept.", "Nothing was changed (dry run).",
	} {
		if !strings.Contains(got.out+got.errOut, want) {
			t.Errorf("output lacks %q:\n%s\n%s", want, got.out, got.errOut)
		}
	}
	if len(*bodies) != 1 || (*bodies)[0]["dry_run"] != true || (*bodies)[0]["orphans"] != true || (*bodies)[0]["trash"] != true {
		t.Fatalf("requests = %v", *bodies)
	}
}

func TestArkPruneAsksToTypeDELETE(t *testing.T) {
	t.Run("DELETE confirms: the list, the question, then the real request", func(t *testing.T) {
		r, bodies := newPruneRig(t, pruneList)
		got := r.run(mgArgs("ark", "prune", "--orphans", "--root", "internal").withTerm("DELETE"))
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if !strings.Contains(got.errOut, "Type DELETE to confirm:") || !strings.Contains(got.errOut, "This cannot be undone") {
			t.Errorf("question missing:\n%s", got.errOut)
		}
		for _, want := range []string{"Deleted osm-north-america (20260501) from internal (11.2 GB freed).", "Freed 14.3 GB."} {
			if !strings.Contains(got.out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, got.out)
			}
		}
		if len(*bodies) != 2 || (*bodies)[0]["dry_run"] != true || (*bodies)[1]["dry_run"] != false {
			t.Fatalf("requests = %v", *bodies)
		}
		if roots, _ := (*bodies)[1]["roots"].([]any); len(roots) != 1 || roots[0] != "internal" {
			t.Errorf("roots = %v", (*bodies)[1]["roots"])
		}
	})
	t.Run("anything else is a No: nothing is sent after the list", func(t *testing.T) {
		for _, typed := range []string{"delete", "yes", "y", ""} {
			r, _ := newPruneRig(t, pruneList)
			got := r.run(mgArgs("ark", "prune", "--orphans").withTerm(typed))
			if got.code != 0 || !strings.Contains(got.out+got.errOut, "Nothing was deleted.") || countPrunes(r) != 1 {
				t.Errorf("%q: %+v, prunes %d", typed, got, countPrunes(r))
			}
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r, _ := newPruneRig(t, pruneList)
		got := r.run(mgArgs("--lang", "es", "ark", "prune", "--orphans").withTerm("DELETE"))
		if got.code != 0 || !strings.Contains(got.errOut, "Escriba DELETE para confirmar:") || !strings.Contains(got.out, "Se liberaron 14.3 GB.") {
			t.Fatalf("%+v", got)
		}
	})
}

func TestArkPruneYes(t *testing.T) {
	r, bodies := newPruneRig(t, pruneList)
	got := r.run(mgArgs("ark", "prune", "--trash", "--yes"))
	if got.code != 0 || strings.Contains(got.errOut, "DELETE") || len(*bodies) != 2 || (*bodies)[1]["dry_run"] != false {
		t.Fatalf("%+v, requests %v", got, *bodies)
	}
	if !strings.Contains(got.out, "Freed 14.3 GB.") {
		t.Errorf("stdout:\n%s", got.out)
	}
	// --json prints the body of the real request.
	r, _ = newPruneRig(t, pruneList)
	j := r.run(mgArgs("--json", "ark", "prune", "--orphans", "--yes"))
	var a pruneAnswer
	if j.code != 0 || json.Unmarshal([]byte(j.out), &a) != nil || a.FreedBytes != 14300000000 {
		t.Fatalf("%+v", j)
	}
}

func TestArkPruneNothingToDelete(t *testing.T) {
	r, _ := newPruneRig(t, pruneNothing)
	got := r.run(mgArgs("ark", "prune", "--orphans").withTerm("DELETE"))
	if got.code != 0 || !strings.Contains(got.out+got.errOut, "Nothing to delete.") || !strings.Contains(got.out+got.errOut, "only copy of gutenberg-en") || countPrunes(r) != 1 {
		t.Fatalf("%+v, prunes %d", got, countPrunes(r))
	}
	if strings.Contains(got.errOut, "DELETE") {
		t.Error("asked to confirm a deletion of nothing")
	}
}

func TestArkPruneUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"nothing chosen":    {"ark", "prune"},
		"a dry run alone":   {"ark", "prune", "--dry-run"},
		"a bad root":        {"ark", "prune", "--orphans", "--root", "nope"},
		"a stray argument":  {"ark", "prune", "--orphans", "extra"},
		"an unknown option": {"ark", "prune", "--orphans", "--allow-last-copy"},
		"another override":  {"ark", "prune", "--orphans", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newPruneRig(t, pruneList)
			got := r.run(mgArgs(args...))
			if got.code != ExitUsage || countPrunes(r) != 0 {
				t.Fatalf("%+v, prunes %d", got, countPrunes(r))
			}
		})
	}
	t.Run("no terminal and no --yes: nothing is sent", func(t *testing.T) {
		r, _ := newPruneRig(t, pruneList)
		got := r.run(mgArgs("ark", "prune", "--orphans"))
		if got.code != ExitUsage || countPrunes(r) != 0 || !strings.Contains(got.errOut, "--yes") {
			t.Fatalf("%+v, prunes %d", got, countPrunes(r))
		}
		j := newMgRig(t, fake.Options{})
		if got := j.run(mgArgs("--json", "ark", "prune", "--orphans")); got.code != ExitUsage {
			t.Fatalf("%+v", got)
		}
	})
}

func TestArkPruneAsksForThePINOnlyWhenTheAgentDoes(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Handle(http.MethodPost, "/ark/prune", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, pruneList)
			return
		}
		fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
	})
	r := &mgRig{t: t, h: a.h}
	got := r.run(mgArgs("ark", "prune", "--orphans", "--yes").withTerm(testPIN))
	if got.code != 0 || a.verifies() != 1 {
		t.Fatalf("%+v verifies %d", got, a.verifies())
	}
	secretFree(t, testPIN, got.out, got.errOut)
}

func TestArkPruneRefusals(t *testing.T) {
	r := newMgRig(t, fake.Options{})
	r.agent().RespondError(http.MethodPost, "/ark/prune", 409, "LAI-SYN-005", "Another sync is already running.", "Wait for it to finish, or cancel it with `aictl ark sync --cancel`.", nil)
	got := r.run(mgArgs("ark", "prune", "--orphans", "--yes"))
	if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-SYN-005]") || !strings.Contains(got.errOut, "Wait for it to finish") {
		t.Fatalf("%+v", got)
	}
}
