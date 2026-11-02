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

const enrollOK = `{"state":"linked","server_node_id":"3f1c2e9a-0000-4000-8000-00000000abcd","reattached":false}`

func newCloudRig(t *testing.T, enrolled string) *mgRig {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodPost, "/cloud/enroll", 200, enrolled)
	r.agent().Respond(http.MethodGet, "/cloud", 200, `{"enrolled":true,"name":"ai-n3","state":"linked"}`)
	return r
}

func TestCloudEnrollPrompt(t *testing.T) {
	t.Run("typed at the prompt, never echoed", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		term := fake.Typed(true, mgCred())
		o := mgArgs("cloud", "enroll")
		o.term = term
		got := r.run(o)
		checkRun(t, got, 0, "✓ Connected to https://ai.marin.cr as ai-n3. The node shows up on the Nodes\n  page.\n", "Node credential (nod_…): \n")
		if kinds := term.Kinds(); len(kinds) != 1 || kinds[0] != "secret" {
			t.Errorf("the credential was read as %v, want one secret read (no echo)", kinds)
		}
		const want = `{"credential":"` + "%s" + `","url":"https://ai.marin.cr"}`
		if b := r.lastBody(http.MethodPost, "/cloud/enroll"); b != strings.Replace(want, "%s", mgCred(), 1) {
			t.Errorf("body %s", b)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
	})
	t.Run("Spanish prompt and --url", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("--lang", "es", "cloud", "enroll", "--url", "https://nube.example.test/").withTerm(mgCred()))
		checkRun(t, got, 0, "✓ Conectado a https://nube.example.test como ai-n3. El nodo aparece en la página\n  Nodos.\n", "Credencial del nodo (nod_…): \n")
		if b := r.lastBody(http.MethodPost, "/cloud/enroll"); !strings.Contains(b, `"url":"https://nube.example.test"`) {
			t.Errorf("body %s", b)
		}
	})
	t.Run("a node that was linked before", func(t *testing.T) {
		r := newCloudRig(t, `{"state":"linked","server_node_id":"x","reattached":true}`)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		if got.code != 0 || !strings.Contains(got.out, "Its earlier record was reused.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the name cannot be read: the line is worded without it", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		r.agent().RespondError(http.MethodGet, "/cloud", 500, "LAI-AGT-099", "Bug.", "Report.", nil)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		checkRun(t, got, 0, "✓ Connected to https://ai.marin.cr. The node shows up on the Nodes page.\n", "Node credential (nod_…): \n")
	})
	t.Run("the cloud is not reachable: saved, exit 0, LAI-CLD-053", func(t *testing.T) {
		r := newCloudRig(t, `{"state":"backoff","last_error":"dial tcp: no route to host"}`)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		want := "Node credential (nod_…): \n" +
			"! Saved. The cloud is not reachable right now; the node will connect by itself.\n  [LAI-CLD-053]\n"
		checkRun(t, got, 0, "", want)
	})
	t.Run("offline mode: stored, the link starts when online", func(t *testing.T) {
		r := newCloudRig(t, `{"state":"stopped"}`)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		if got.code != 0 || got.out != "i Saved. The node connects to the cloud when it is in online mode.\n" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a rejected credential: exit 6 LAI-CLD-051, never shown", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		r.agent().RespondError(http.MethodPost, "/cloud/enroll", 422, "LAI-CLD-051", "The cloud rejected the credential.", "Make a new one.", nil)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-CLD-051]") {
			t.Fatalf("%+v", got)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
	})
	t.Run("--json is the body", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("--json", "cloud", "enroll", "--credential-stdin").setStdin(mgCred() + "\n"))
		checkRun(t, got, 0, enrollOK+"\n", "")
	})
}

// The credential is not an argument, and the error for one does not repeat it.
func TestCloudEnrollRefusesTheCredentialAsAnArgument(t *testing.T) {
	for _, a := range [][]string{
		{"cloud", "enroll", mgCred()},
		{"cloud", "enroll", "--credential", mgCred()},
		{"cloud", "enroll", "--credential=" + mgCred()},
	} {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs(a...).withTerm(mgCred()))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
			t.Errorf("%v: %+v", a[:2], got)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
	}
}

func TestCloudEnrollStdin(t *testing.T) {
	t.Run("one line of stdin, no terminal", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll", "--credential-stdin").setStdin("  " + mgCred() + "  \n"))
		checkRun(t, got, 0, "✓ Connected to https://ai.marin.cr as ai-n3. The node shows up on the Nodes\n  page.\n", "")
		if b := r.lastBody(http.MethodPost, "/cloud/enroll"); !strings.Contains(b, mgCred()) {
			t.Errorf("body %s", b)
		}
	})
	t.Run("the credential line comes first, the PIN line second", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Respond(http.MethodGet, "/cloud", 200, `{"enrolled":true,"name":"ai-n3"}`)
		a.h.agent.Handle(http.MethodPost, "/cloud/enroll", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, enrollOK)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("--pin-stdin", "cloud", "enroll", "--credential-stdin").setStdin(mgCred() + "\n" + testPIN + "\n"))
		if got.code != 0 || a.verifies() != 1 || a.pins[0] != testPIN {
			t.Fatalf("%+v verifies %d pins %v", got, a.verifies(), a.pins)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
		secretFree(t, testPIN, got.out, got.errOut)
	})
	t.Run("an empty stdin", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll", "--credential-stdin"))
		if got.code != ExitUsage || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
}

func TestCloudEnrollRefusals(t *testing.T) {
	t.Run("no terminal and no --credential-stdin", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "--credential-stdin") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json cannot prompt", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		o := mgArgs("--json", "cloud", "enroll").withTerm(mgCred())
		got := r.run(o)
		if got.code != ExitUsage || got.errOut != "" || !strings.Contains(got.out, `"code":"LAI-CLI-005"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("something that is not a node credential is not sent, and not shown", func(t *testing.T) {
		wrong := "pat" + "_" + strings.Repeat("Q4", 20)
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll").withTerm(wrong))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "starts with nod_") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
		secretFree(t, wrong, got.out, got.errOut)
	})
	t.Run("an empty answer at the prompt", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll").withTerm(""))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "No credential was entered.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("Ctrl-C at the prompt is exit 130", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		o := mgArgs("cloud", "enroll")
		o.term = &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}
		if got := r.run(o); got.code != ExitInterrupted || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("bad --url values", func(t *testing.T) {
		for _, u := range []string{"ftp://x.example.test", "https://user:pw@x.example.test", "not a url", "https://"} {
			r := newCloudRig(t, enrollOK)
			got := r.run(mgArgs("cloud", "enroll", "--url", u).withTerm(mgCred()))
			if got.code != ExitUsage || len(r.requestLines()) != 0 {
				t.Errorf("%q: %+v", u, got)
			}
			if strings.Contains(u, "pw@") && strings.Contains(got.errOut, "pw") && !strings.Contains(got.errOut, "--url") {
				t.Errorf("%q: %s", u, got.errOut)
			}
		}
	})
	t.Run("the agent is down: exit 3", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
	})
	t.Run("the debug log never holds the credential", func(t *testing.T) {
		r := newCloudRig(t, enrollOK)
		got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred()).setenv("LOCALAI_CLI_DEBUG", "1"))
		if got.code != 0 || !strings.Contains(got.errOut, `"path":"/api/v1/cloud/enroll"`) {
			t.Fatalf("%+v", got)
		}
		secretFree(t, mgCred(), got.out, got.errOut)
	})
}

func TestCloudEnrollAsAnOperatorNeedsThePIN(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Respond(http.MethodGet, "/cloud", 200, `{"enrolled":true,"name":"ai-n3"}`)
	a.h.agent.Handle(http.MethodPost, "/cloud/enroll", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, enrollOK)
			return
		}
		fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
	})
	r := &mgRig{t: t, h: a.h}
	got := r.run(mgArgs("cloud", "enroll").withTerm(mgCred(), testPIN))
	if got.code != 0 || a.verifies() != 1 {
		t.Fatalf("%+v", got)
	}
	// The credential was typed once; the retry after the PIN sends the same one.
	var bodies int
	for _, q := range a.h.agent.Requests() {
		if q.Path == "/cloud/enroll" {
			bodies++
			if !strings.Contains(q.Body, mgCred()) {
				t.Errorf("request %d lost the credential", bodies)
			}
		}
	}
	if bodies != 2 {
		t.Errorf("enroll requests = %d", bodies)
	}
	secretFree(t, mgCred(), got.out, got.errOut)
}

// --- unenroll ---------------------------------------------------------------------------------------------

func TestCloudUnenroll(t *testing.T) {
	newRig := func(t *testing.T) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodDelete, "/cloud", 200, `{"enrolled":false}`)
		return r
	}
	t.Run("confirmed", func(t *testing.T) {
		r := newRig(t)
		got := r.run(mgArgs("cloud", "unenroll").withTerm("y"))
		if got.code != 0 || got.out != "✓ This node is no longer connected to the cloud.\n" ||
			!strings.HasSuffix(got.errOut, "[y/N] ") || !strings.Contains(got.errOut, "Disconnect this node from the cloud?") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--yes, Spanish", func(t *testing.T) {
		got := newRig(t).run(mgArgs("-y", "--lang", "es", "cloud", "unenroll"))
		checkRun(t, got, 0, "✓ Este nodo ya no está conectado a la nube.\n", "")
	})
	t.Run("declined", func(t *testing.T) {
		r := newRig(t)
		got := r.run(mgArgs("cloud", "unenroll").withTerm("n"))
		if got.code != 0 || got.out != "" || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no terminal and no --yes", func(t *testing.T) {
		r := newRig(t)
		got := r.run(mgArgs("cloud", "unenroll"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-007]") || len(r.requestLines()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json", func(t *testing.T) {
		checkRun(t, newRig(t).run(mgArgs("--json", "-y", "cloud", "unenroll")), 0, `{"enrolled":false}`+"\n", "")
	})
	t.Run("an operator is asked for the PIN", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Handle(http.MethodDelete, "/cloud", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"enrolled":false}`)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("cloud", "unenroll").withTerm("y", testPIN))
		if got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a locked PIN is exit 4", func(t *testing.T) {
		a := newAdminAgent(t)
		a.lockAt, a.wrong = 1, 1
		a.h.agent.RespondError(http.MethodDelete, "/cloud", 403, api.CodeAdminRequired, "Needs the PIN.", "Enter it.", map[string]any{"pin_required": true})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("cloud", "unenroll").withTerm("y", "0000"))
		if got.code != ExitDenied || !strings.Contains(got.errOut, "[LAI-SEC-004]") {
			t.Fatalf("%+v", got)
		}
	})
}

// --- resend-key -------------------------------------------------------------------------------------------

func TestCloudResendKey(t *testing.T) {
	t.Run("sent", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/cloud/resend-key", 200, `{"key_synced":true,"fingerprint":"c0de…c0de"}`)
		checkRun(t, r.run(mgArgs("cloud", "resend-key")), 0, "✓ The API key (fingerprint c0de…c0de) was sent to the cloud again.\n", "")
		checkRun(t, r.run(mgArgs("--lang", "es", "cloud", "resend-key")), 0, "✓ La clave de la API (huella c0de…c0de) se envió de nuevo a la nube.\n", "")
		checkRun(t, r.run(mgArgs("--json", "cloud", "resend-key")), 0, `{"key_synced":true,"fingerprint":"c0de…c0de"}`+"\n", "")
	})
	t.Run("the cloud did not take it: exit 1", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/cloud/resend-key", 200, `{"key_synced":false,"fingerprint":"c0de…c0de"}`)
		got := r.run(mgArgs("cloud", "resend-key"))
		if got.code != ExitFailed || got.out != "" || !strings.Contains(got.errOut, "The cloud did not take the key.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("not linked: exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPost, "/cloud/resend-key", 409, "LAI-AGT-023", "This action is not allowed in the current mode.", "Switch mode first.", nil)
		if got := r.run(mgArgs("cloud", "resend-key")); got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-AGT-023]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an operator is asked for the PIN", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Handle(http.MethodPost, "/cloud/resend-key", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"key_synced":true,"fingerprint":"x"}`)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		if got := r.run(mgArgs("cloud", "resend-key").withTerm(testPIN)); got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v", got)
		}
	})
}
