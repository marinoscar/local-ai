package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl delete` (AICTL-05, aictl.md section 4.7): the size-aware question, the typed second
// confirmation of --allow-last-copy, the admin PIN, the refusals and the --json rules, against the
// contract of DELETE /models/{id} (agent.md section 3.8.12, AGENT-15).

const (
	delPath = "/models/" + bigID
	delBody = `{"id":"` + bigID + `","deleted":[{"root":"/srv/localai","bytes":19000000000}],"op_id":"op_del1"}`
)

func newDeleteRig(t *testing.T) *mgRig {
	t.Helper()
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, delPath, 200,
		`{"id":"`+bigID+`","display_name":"Qwen 3.6 35B","status":"stored","size_bytes":19000000000}`)
	r.agent().Respond(http.MethodDelete, delPath, 200, delBody)
	return r
}

// deleteRequest is the DELETE the fake saw last, with its query.
func deleteRequest(t *testing.T, r *mgRig) string {
	t.Helper()
	reqs := r.agent().Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == http.MethodDelete {
			return reqs[i].Path
		}
	}
	t.Fatalf("no DELETE in %v", r.requestLines())
	return ""
}

func countDeletes(r *mgRig) int {
	n := 0
	for _, q := range r.agent().Requests() {
		if q.Method == http.MethodDelete {
			n++
		}
	}
	return n
}

func TestDelete(t *testing.T) {
	const question = "Delete Qwen 3.6 35B from this computer (19.0 GB)? [y/N] "
	const done = "✓ Deleted Qwen 3.6 35B from /srv/localai (19.0 GB freed).\n"
	t.Run("the question names the model and its size; yes deletes and keeps the last copy", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID).withTerm("y"))
		checkRun(t, got, 0, done, question)
		if p := deleteRequest(t, r); p != delPath+"?keep_last=true" {
			t.Errorf("request %s", p)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		got := newDeleteRig(t).run(mgArgs("--lang", "es", "delete", bigID).withTerm("s"))
		checkRun(t, got, 0, "✓ Se borró Qwen 3.6 35B de /srv/localai (19.0 GB liberados).\n",
			"¿Borrar Qwen 3.6 35B de este equipo (19.0 GB)? [s/N] ")
	})
	t.Run("--root names the place and is in the query", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID, "--root", "/srv/localai").withTerm("y"))
		checkRun(t, got, 0, done, "Delete Qwen 3.6 35B from /srv/localai (19.0 GB)? [y/N] ")
		if p := deleteRequest(t, r); p != delPath+"?keep_last=true&root=%2Fsrv%2Flocalai" {
			t.Errorf("request %s", p)
		}
		r = newDeleteRig(t)
		if got := r.run(mgArgs("delete", bigID, "--root", "ark-0123abcd").withTerm("y")); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if p := deleteRequest(t, r); p != delPath+"?keep_last=true&root=ark-0123abcd" {
			t.Errorf("request %s", p)
		}
	})
	t.Run("the default answer is No: nothing is deleted", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID).withTerm(""))
		checkRun(t, got, 0, "i Nothing was deleted.\n", question)
		if countDeletes(r) != 0 {
			t.Errorf("a DELETE was sent")
		}
		r = newDeleteRig(t)
		if got := r.run(mgArgs("delete", bigID).withTerm("no")); got.code != 0 || countDeletes(r) != 0 {
			t.Errorf("no: %+v", got)
		}
	})
	t.Run("--yes asks nothing and needs no read", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		checkRun(t, got, 0, done, "")
		if got := strings.Join(r.requestLines(), ","); got != "GET "+delPath+",DELETE "+delPath+"?keep_last=true" {
			// the name for the result line is a best-effort read; the question needed none
			t.Errorf("requests %s", got)
		}
	})
	t.Run("-y before the command name is the same", func(t *testing.T) {
		if got := newDeleteRig(t).run(mgArgs("-y", "delete", bigID)); got.code != 0 || got.out != done {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no terminal and no --yes: LAI-CLI-007 before any request", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v, requests %v", got, r.requestLines())
		}
	})
	t.Run("--json needs --yes, then prints the body", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("--json", "delete", bigID))
		if got.code != ExitUsage || !strings.Contains(got.out, `"code":"LAI-CLI-007"`) || len(r.requestLines()) != 0 {
			t.Fatalf("without --yes: %+v", got)
		}
		r = newDeleteRig(t)
		checkRun(t, r.run(mgArgs("--json", "delete", bigID, "--yes")), 0, delBody+"\n", "")
		if got := strings.Join(r.requestLines(), ","); got != "DELETE "+delPath+"?keep_last=true" {
			t.Errorf("--json --yes must not read anything first: %s", got)
		}
	})
	t.Run("a model that does not exist ends at the read, before the question", func(t *testing.T) {
		r := newDeleteRig(t)
		r.agent().RespondError(http.MethodGet, delPath, 404, "LAI-AGT-021", "No such model.", "Check the name.", nil)
		got := r.run(mgArgs("delete", bigID).withTerm("y"))
		if got.code != ExitNotFound || !strings.Contains(got.errOut, "[LAI-AGT-021]") || strings.Contains(got.errOut, "[y/N]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("nothing stored there: said, not a failure", func(t *testing.T) {
		r := newDeleteRig(t)
		r.agent().Respond(http.MethodDelete, delPath, 200, `{"id":"`+bigID+`","deleted":[],"op_id":"op_del1"}`)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		checkRun(t, got, 0, "i Qwen 3.6 35B was not stored there. Nothing was deleted.\n", "")
	})
	t.Run("a copy kept as the last verified one is said, with the way to delete it", func(t *testing.T) {
		r := newDeleteRig(t)
		r.agent().Respond(http.MethodDelete, delPath, 200, `{"id":"`+bigID+`","deleted":[{"root":"/srv/localai","bytes":19000000000}],`+
			`"kept":[{"root":"/media/user/AI-ARK/localai","reason":"last_verified_copy"},{"root":"ark-0123abcd","reason":"not_present"}],"op_id":"op_del1"}`)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		want := done + "i Kept the copy in /media/user/AI-ARK/localai: it is the last verified copy. Add\n  --allow-last-copy to delete it too.\n"
		checkRun(t, got, 0, want, "")
	})
	t.Run("the router reload had a problem: a warning, the files are gone, exit 0", func(t *testing.T) {
		r := newDeleteRig(t)
		r.agent().Respond(http.MethodDelete, delPath, 200, `{"id":"`+bigID+`","deleted":[{"root":"/srv/localai","bytes":19000000000}],`+
			`"warnings":[{"code":"LAI-LLM-007","message":"The model list could not be applied."}],"op_id":"op_del1"}`)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		if got.code != 0 || got.out != done || !strings.Contains(got.errOut, "[LAI-LLM-007]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("two content folders: one line each", func(t *testing.T) {
		r := newDeleteRig(t)
		r.agent().Respond(http.MethodDelete, delPath, 200, `{"id":"`+bigID+`","deleted":[{"root":"/srv/localai","bytes":19000000000},{"root":"/media/user/AI-ARK/localai","bytes":19000000000}],"op_id":"op_del1"}`)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		if got.code != 0 || strings.Count(got.out, "✓ Deleted Qwen 3.6 35B from ") != 2 {
			t.Fatalf("%+v", got)
		}
	})

	refusals := []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"a loaded model", 409, "LAI-AGT-022", ExitRefused},
		{"the last verified copy", 409, "LAI-SYN-044", ExitRefused},
		{"a model that is not there", 404, "LAI-AGT-021", ExitNotFound},
		{"survival mode", 409, "LAI-AGT-023", ExitRefused},
		{"not allowed", 403, "LAI-SEC-002", ExitDenied},
		{"a bug", 500, "LAI-AGT-099", ExitFailed},
	}
	for _, tc := range refusals {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			r := newDeleteRig(t)
			r.agent().RespondError(http.MethodDelete, delPath, tc.status, tc.code, "Refused.", "Unload it first.", nil)
			got := r.run(mgArgs("delete", bigID).withTerm("y"))
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("a bad id or root is exit 2 before any request", func(t *testing.T) {
		r := newDeleteRig(t)
		for _, a := range [][]string{{"delete", "Qwen"}, {"delete"}, {"delete", bigID, "--root", "somewhere"}, {"delete", bigID, smallID}} {
			if got := r.run(mgArgs(a...).withTerm("y")); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
	t.Run("Ctrl-C at the question is exit 130 and deletes nothing", func(t *testing.T) {
		r := newDeleteRig(t)
		term := &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}
		o := mgArgs("delete", bigID)
		o.term = term
		got := r.run(o)
		if got.code != ExitInterrupted || countDeletes(r) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("delete", bigID, "--yes"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

func TestDeleteAllowLastCopy(t *testing.T) {
	const typed = "This is the last verified copy anywhere. Type the model id to confirm: "
	t.Run("asks the id after the first yes, then keep_last=false", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID, "--allow-last-copy").withTerm("y", bigID))
		checkRun(t, got, 0, "✓ Deleted Qwen 3.6 35B from /srv/localai (19.0 GB freed).\n",
			"Delete Qwen 3.6 35B from this computer (19.0 GB)? [y/N] "+typed)
		if p := deleteRequest(t, r); p != delPath+"?keep_last=false" {
			t.Errorf("request %s", p)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		got := newDeleteRig(t).run(mgArgs("--lang", "es", "delete", bigID, "--allow-last-copy").withTerm("s", bigID))
		if got.code != 0 || !strings.Contains(got.errOut, "Esta es la última copia verificada que existe. Escriba el identificador del modelo para confirmar: ") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a different id, an empty one or the end of the input deletes nothing", func(t *testing.T) {
		for _, answer := range []string{"yes", "", " qwen "} {
			r := newDeleteRig(t)
			got := r.run(mgArgs("delete", bigID, "--allow-last-copy").withTerm("y", answer))
			checkRun(t, got, 0, "i Nothing was deleted.\n", "Delete Qwen 3.6 35B from this computer (19.0 GB)? [y/N] "+typed)
			if countDeletes(r) != 0 {
				t.Errorf("answer %q: a DELETE was sent", answer)
			}
		}
	})
	t.Run("--yes answers the first question but never the typed one", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID, "--allow-last-copy", "--yes").withTerm(bigID))
		if got.code != 0 || strings.Contains(got.errOut, "[y/N]") || !strings.Contains(got.errOut, typed) {
			t.Fatalf("%+v", got)
		}
		if p := deleteRequest(t, r); p != delPath+"?keep_last=false" {
			t.Errorf("request %s", p)
		}
	})
	t.Run("without a terminal --yes is not enough: exit 2, nothing sent", func(t *testing.T) {
		r := newDeleteRig(t)
		got := r.run(mgArgs("delete", bigID, "--allow-last-copy", "--yes"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || !strings.Contains(got.errOut, "`--yes` does not answer this question") ||
			len(r.requestLines()) != 0 {
			t.Fatalf("%+v, requests %v", got, r.requestLines())
		}
		r = newDeleteRig(t)
		got = r.run(mgArgs("--json", "--yes", "delete", bigID, "--allow-last-copy"))
		if got.code != ExitUsage || !strings.Contains(got.out, `"code":"LAI-CLI-007"`) || len(r.requestLines()) != 0 {
			t.Fatalf("--json: %+v", got)
		}
	})
}

// An operator (not root) is asked for the PIN only after the confirmation, and only when the agent
// asks for it.
func TestDeleteAsOperator(t *testing.T) {
	setup := func(t *testing.T) (*adminAgent, *mgRig) {
		a := newAdminAgent(t)
		a.h.agent.Respond(http.MethodGet, delPath, 200,
			`{"id":"`+bigID+`","display_name":"Qwen 3.6 35B","status":"stored","size_bytes":19000000000}`)
		a.h.agent.Handle(http.MethodDelete, delPath, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, delBody)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		return a, &mgRig{t: t, h: a.h}
	}
	t.Run("question, then the PIN prompt, then the deletion", func(t *testing.T) {
		a, r := setup(t)
		got := r.run(mgArgs("delete", bigID).withTerm("y", testPIN))
		if got.code != 0 || got.out != "✓ Deleted Qwen 3.6 35B from /srv/localai (19.0 GB freed).\n" || a.verifies() != 1 {
			t.Fatalf("%+v verifies %d", got, a.verifies())
		}
		if !strings.Contains(got.errOut, "[y/N]") || !strings.Contains(got.errOut, "Admin PIN: ") {
			t.Errorf("stderr %q", got.errOut)
		}
		secretFree(t, testPIN, got.out, got.errOut)
	})
	t.Run("a No never asks for the PIN", func(t *testing.T) {
		a, r := setup(t)
		got := r.run(mgArgs("delete", bigID).withTerm("n"))
		if got.code != 0 || a.verifies() != 0 || countDeletes(r) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the PIN from stdin", func(t *testing.T) {
		a, r := setup(t)
		got := r.run(mgArgs("--pin-stdin", "delete", bigID, "--yes").setStdin(testPIN + "\n"))
		if got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no terminal and no --pin-stdin: LAI-CLI-008, exit 4", func(t *testing.T) {
		_, r := setup(t)
		got := r.run(mgArgs("delete", bigID, "--yes"))
		if got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-CLI-008]") {
			t.Fatalf("%+v", got)
		}
	})
}
