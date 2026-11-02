package cli

import (
	"context"
	"encoding/json"
	"github.com/marinoscar/local-ai/agent/internal/leakcheck"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

const (
	testPIN   = "4821"
	testGrant = "pg_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCde"
)

// adminAgent is a fake agent for the PIN flow: DELETE /models/x needs the grant, POST /pin/verify
// accepts testPIN and counts wrong answers.
type adminAgent struct {
	h *harness

	mu       sync.Mutex
	wrong    int // wrong PINs seen
	lockAt   int // answer 429 once this many wrong PINs were seen (0: never)
	noPIN    bool
	badGrant bool // the grant is never accepted (an expired one)
	pins     []string
}

func newAdminAgent(t *testing.T) *adminAgent {
	t.Helper()
	a := &adminAgent{h: newHarness(t, fake.Options{})}
	a.h.agent.Handle(http.MethodDelete, "/models/x", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		bad := a.badGrant
		a.mu.Unlock()
		if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant && !bad {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"deleted":true}`)
			return
		}
		fake.WriteEnvelope(w, http.StatusForbidden, api.CodeAdminRequired,
			"This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
	})
	a.h.agent.Handle(http.MethodPost, "/pin/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pin string `json:"pin"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.pins = append(a.pins, body.Pin)
		switch {
		case a.noPIN:
			fake.WriteEnvelope(w, http.StatusConflict, "LAI-SEC-005", "No admin PIN has been set.", "Run `sudo aictl pin set`.", nil)
		case a.lockAt > 0 && a.wrong >= a.lockAt:
			w.Header().Set("Retry-After", "252")
			fake.WriteEnvelope(w, http.StatusTooManyRequests, api.CodeRateLimited,
				"Too many wrong PIN attempts.", "Wait, then try again.", nil)
		case body.Pin == testPIN:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"grant":"`+testGrant+`","expires_at":"2026-11-02T15:09:05Z"}`)
		default:
			a.wrong++
			fake.WriteEnvelope(w, http.StatusUnauthorized, "LAI-SEC-003", "That PIN is not right.", "Try again.",
				map[string]any{"remaining_attempts": 5 - a.wrong})
		}
	})
	return a
}

// verifies returns how many PINs were sent to /pin/verify.
func (a *adminAgent) verifies() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pins)
}

// adminRegistry has a command that deletes through Admin, and one that makes two admin calls.
func adminRegistry() *Registry {
	reg := NewRegistry()
	del := func(c *Call, cl *Client) error {
		return c.Admin(func() error {
			var raw json.RawMessage
			_, err := cl.Do(c.Ctx, http.MethodDelete, "/models/x", nil, &raw)
			if err != nil {
				return err
			}
			if c.Global.JSON {
				return c.Out.JSON().Raw(raw)
			}
			c.Out.OK("Deleted.")
			return nil
		})
	}
	reg.MustAdd(&Command{
		Name: "rm", Summary: "Admin delete.", Marks: MarkAdmin,
		Run: func(c *Call) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			return del(c, cl)
		},
	})
	reg.MustAdd(&Command{
		Name: "rm2", Summary: "Two admin calls.", Marks: MarkAdmin,
		Run: func(c *Call) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			if err := del(c, cl); err != nil {
				return err
			}
			return del(c, cl)
		},
	})
	return reg
}

func (a *adminAgent) opts(term Terminal) runOpts {
	return runOpts{
		env:  map[string]string{"LOCALAI_SOCKET": a.h.agent.SocketPath, "LOCALAI_URL": a.h.agent.URL, "LANG": "en_US.UTF-8"},
		h:    a.h,
		term: term,
	}
}

func deleteGrants(a *adminAgent) []string {
	var out []string
	for _, r := range a.h.agent.Requests() {
		if r.Method == http.MethodDelete {
			out = append(out, r.Header.Get("X-LocalAI-Pin-Grant"))
		}
	}
	return out
}

// Root over the socket: the agent already sees admin, so there is no 403, no prompt, no verify.
func TestAdminRootNeverPrompts(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Respond(http.MethodDelete, "/models/x", 200, `{"deleted":true}`)
	term := fake.Typed(true, testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != 0 || r.out != "✓ Deleted.\n" || r.errOut != "" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
	if term.Reads() != 0 || a.verifies() != 0 || a.h.agent.Count("/pin/verify") != 0 {
		t.Errorf("reads %d, verifies %d: root must never be asked", term.Reads(), a.verifies())
	}
}

// An operator: 403 pin_required, prompt without echo, verify, retry with the grant header.
func TestAdminPromptThenRetryWithGrant(t *testing.T) {
	a := newAdminAgent(t)
	term := fake.Typed(true, testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != 0 || r.out != "✓ Deleted.\n" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
	if r.errOut != "Admin PIN: \n" {
		t.Errorf("stderr = %q, want the prompt and the line end the terminal did not echo", r.errOut)
	}
	if got := term.Kinds(); len(got) != 1 || got[0] != "secret" {
		t.Errorf("the PIN must be read without echo, got %v", got)
	}
	// DELETE (403), POST /pin/verify, DELETE with the grant.
	var seq []string
	for _, rq := range a.h.agent.Requests() {
		if rq.Path != "/health" {
			seq = append(seq, rq.Method+" "+rq.Path)
		}
	}
	if strings.Join(seq, ",") != "DELETE /models/x,POST /pin/verify,DELETE /models/x" {
		t.Errorf("requests = %v", seq)
	}
	if g := deleteGrants(a); len(g) != 2 || g[0] != "" || g[1] != testGrant {
		t.Errorf("grant headers on the two DELETEs = %q", g)
	}
	for _, rq := range a.h.agent.Requests() {
		if rq.Path == "/pin/verify" {
			if rq.Header.Get("X-LocalAI-Pin-Grant") != "" {
				t.Error("the verify request must not carry a grant")
			}
			if rq.Body != `{"pin":"4821"}` {
				t.Errorf("verify body = %q", rq.Body)
			}
		}
	}
	if strings.Contains(r.out+r.errOut, testPIN) || strings.Contains(r.out+r.errOut, testGrant) {
		t.Error("the PIN or the grant was printed")
	}
}

func TestAdminSpanishPrompt(t *testing.T) {
	a := newAdminAgent(t)
	o := a.opts(fake.Typed(true, testPIN))
	o.env["LANG"] = "es_CR.UTF-8"
	r := runCLI(t, adminRegistry(), o, "rm")
	if r.code != 0 || !strings.HasPrefix(r.errOut, "PIN de administrador: ") {
		t.Fatalf("exit %d err %q", r.code, r.errOut)
	}
}

// The grant is kept for the rest of the command: a second admin call asks nothing.
func TestAdminGrantIsReusedWithinTheCommand(t *testing.T) {
	a := newAdminAgent(t)
	term := fake.Typed(true, testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm2")
	if r.code != 0 {
		t.Fatalf("exit %d err %q", r.code, r.errOut)
	}
	if a.verifies() != 1 || term.Reads() != 1 {
		t.Errorf("verifies %d reads %d, want 1 each", a.verifies(), term.Reads())
	}
	if g := deleteGrants(a); len(g) != 3 || g[0] != "" || g[1] != testGrant || g[2] != testGrant {
		t.Errorf("grant headers = %q", g)
	}
}

func TestAdminWrongPINThenRight(t *testing.T) {
	a := newAdminAgent(t)
	term := fake.Typed(true, "0000", testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != 0 || r.out != "✓ Deleted.\n" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
	for _, want := range []string{
		"Admin PIN: \n",
		"! That PIN is not right.  [LAI-SEC-003]",
		"→ Fix: Try again.",
		"! Tries left: 4.",
	} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	if strings.Count(r.errOut, "Admin PIN: ") != 2 {
		t.Errorf("two prompts expected:\n%s", r.errOut)
	}
	if strings.Contains(r.errOut, "0000") || strings.Contains(r.errOut, testPIN) {
		t.Errorf("a PIN was printed:\n%s", r.errOut)
	}
}

func TestAdminWrongPINThreeTimes(t *testing.T) {
	a := newAdminAgent(t)
	term := fake.Typed(true, "1111", "2222", "3333", testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitDenied {
		t.Fatalf("exit %d, want 4\n%s", r.code, r.errOut)
	}
	if a.verifies() != 3 || term.Reads() != 3 {
		t.Errorf("verifies %d reads %d, want 3 each (the fourth PIN must never be asked)", a.verifies(), term.Reads())
	}
	// Two intermediate warnings, then the final error from the framework.
	if strings.Count(r.errOut, "! That PIN is not right.") != 2 || !strings.Contains(r.errOut, "✗ That PIN is not right.  [LAI-SEC-003]") {
		t.Errorf("stderr:\n%s", r.errOut)
	}
	if !strings.Contains(r.errOut, "Tries left: 4.") || !strings.Contains(r.errOut, "Tries left: 3.") {
		t.Errorf("tries left missing:\n%s", r.errOut)
	}
	if g := deleteGrants(a); len(g) != 1 {
		t.Errorf("the request must not be retried without a grant: %q", g)
	}
}

func TestAdminRateLimitedShowsTheWait(t *testing.T) {
	a := newAdminAgent(t)
	a.lockAt = 1
	a.wrong = 1
	term := fake.Typed(true, "0000", testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitDenied {
		t.Fatalf("exit %d, want 4 (PIN locked)\n%s", r.code, r.errOut)
	}
	for _, want := range []string{"✗ Too many wrong PIN attempts.  [LAI-SEC-004]", "→ Fix: Wait, then try again.", "→ Wait: 4 min 12 s"} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	if a.verifies() != 1 {
		t.Errorf("a lockout ends the prompts at once, verifies = %d", a.verifies())
	}
	es := a.opts(fake.Typed(true, "0000"))
	es.env["LANG"] = "es_CR.UTF-8"
	re := runCLI(t, adminRegistry(), es, "rm")
	if !strings.Contains(re.errOut, "→ Espere: 4 min 12 s") {
		t.Errorf("Spanish wait line:\n%s", re.errOut)
	}
}

func TestAdminRateLimitedJSON(t *testing.T) {
	a := newAdminAgent(t)
	a.lockAt = 1
	a.wrong = 1
	o := a.opts(fake.Typed(false, "0000"))
	r := runCLI(t, adminRegistry(), o, "--json", "--pin-stdin", "rm")
	if r.code != ExitDenied || r.errOut != "" {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	var env struct{ Error struct{ Code string } }
	if err := json.Unmarshal([]byte(r.out), &env); err != nil || env.Error.Code != api.CodeRateLimited {
		t.Errorf("stdout %q: %v", r.out, err)
	}
}

func TestAdminNoPINSet(t *testing.T) {
	a := newAdminAgent(t)
	a.noPIN = true
	r := runCLI(t, adminRegistry(), a.opts(fake.Typed(true, testPIN)), "rm")
	if r.code != ExitRefused || !strings.Contains(r.errOut, "[LAI-SEC-005]") || !strings.Contains(r.errOut, "sudo aictl pin set") {
		t.Errorf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestAdminPinStdin(t *testing.T) {
	t.Run("one line from a pipe", func(t *testing.T) {
		a := newAdminAgent(t)
		term := fake.Typed(false, testPIN)
		r := runCLI(t, adminRegistry(), a.opts(term), "--pin-stdin", "rm")
		if r.code != 0 || r.out != "✓ Deleted.\n" {
			t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
		if r.errOut != "" {
			t.Errorf("no prompt text on a pipe, stderr = %q", r.errOut)
		}
		if got := term.Kinds(); len(got) != 1 || got[0] != "line" {
			t.Errorf("reads = %v", got)
		}
	})
	t.Run("through the default stdin reader", func(t *testing.T) {
		a := newAdminAgent(t)
		o := a.opts(nil)
		o.stdin = testPIN + "\r\nignored\n"
		r := runCLI(t, adminRegistry(), o, "--pin-stdin", "rm")
		if r.code != 0 || a.pins[0] != testPIN {
			t.Fatalf("exit %d pins %q err %q", r.code, a.pins, r.errOut)
		}
	})
	t.Run("wrong PIN is one try", func(t *testing.T) {
		a := newAdminAgent(t)
		term := fake.Typed(false, "0000", testPIN)
		r := runCLI(t, adminRegistry(), a.opts(term), "--pin-stdin", "rm")
		if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-SEC-003]") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		if a.verifies() != 1 {
			t.Errorf("verifies = %d, want 1", a.verifies())
		}
	})
	t.Run("empty stdin", func(t *testing.T) {
		a := newAdminAgent(t)
		r := runCLI(t, adminRegistry(), a.opts(fake.Typed(false)), "--pin-stdin", "rm")
		if r.code != ExitDenied || !strings.Contains(r.errOut, "No PIN was entered.  [LAI-CLI-008]") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		if a.verifies() != 0 {
			t.Error("an empty PIN must not be sent")
		}
	})
	t.Run("blank line", func(t *testing.T) {
		a := newAdminAgent(t)
		r := runCLI(t, adminRegistry(), a.opts(fake.Typed(false, "")), "--pin-stdin", "rm")
		if r.code != ExitDenied || a.verifies() != 0 {
			t.Fatalf("exit %d verifies %d", r.code, a.verifies())
		}
	})
	t.Run("with --json the stdout is the command's document only", func(t *testing.T) {
		a := newAdminAgent(t)
		r := runCLI(t, adminRegistry(), a.opts(fake.Typed(false, testPIN)), "--json", "--pin-stdin", "rm")
		if r.code != 0 || r.out != `{"deleted":true}`+"\n" || r.errOut != "" {
			t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("on a terminal it is read without echo", func(t *testing.T) {
		a := newAdminAgent(t)
		term := fake.Typed(true, testPIN)
		r := runCLI(t, adminRegistry(), a.opts(term), "--pin-stdin", "rm")
		if r.code != 0 || term.Kinds()[0] != "secret" {
			t.Fatalf("exit %d kinds %v", r.code, term.Kinds())
		}
	})
}

func TestAdminNoTerminal(t *testing.T) {
	tests := []struct {
		name string
		args []string
		term *fake.Terminal
		json bool
	}{
		{name: "no terminal", term: fake.Typed(false, testPIN)},
		{name: "--json on a terminal", args: []string{"--json"}, term: fake.Typed(true, testPIN), json: true},
		{name: "--json without a terminal", args: []string{"--json"}, term: fake.Typed(false, testPIN), json: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdminAgent(t)
			r := runCLI(t, adminRegistry(), a.opts(tc.term), append(append([]string{}, tc.args...), "rm")...)
			if r.code != ExitDenied {
				t.Fatalf("exit %d, want 4\nout %q err %q", r.code, r.out, r.errOut)
			}
			if tc.term.Reads() != 0 || a.verifies() != 0 {
				t.Errorf("nothing may be read or verified: reads %d verifies %d", tc.term.Reads(), a.verifies())
			}
			if tc.json {
				var env struct{ Error struct{ Code, Fix string } }
				if r.errOut != "" || json.Unmarshal([]byte(r.out), &env) != nil || env.Error.Code != "LAI-CLI-008" || env.Error.Fix == "" {
					t.Errorf("envelope: out %q err %q", r.out, r.errOut)
				}
				return
			}
			for _, want := range []string{"✗ The admin PIN is needed but there is no terminal to ask for it.  [LAI-CLI-008]",
				"→ Fix: Run it in a terminal, use `sudo`, or pass `--pin-stdin`."} {
				if !strings.Contains(r.errOut, want) {
					t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
				}
			}
		})
	}
}

func TestAdminClosedStdinWithoutTerminal(t *testing.T) {
	a := newAdminAgent(t)
	r := runCLI(t, adminRegistry(), a.opts(nil), "rm") // stdin is an empty reader: nothing to ask on
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-008]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestAdminCtrlCAtThePrompt(t *testing.T) {
	a := newAdminAgent(t)
	term := &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitInterrupted {
		t.Fatalf("exit %d, want 130", r.code)
	}
	if r.errOut != "Admin PIN: \n" {
		t.Errorf("stderr = %q: the prompt line is ended and nothing else is printed", r.errOut)
	}
	if a.verifies() != 0 {
		t.Error("nothing may be verified after Ctrl-C")
	}
}

func TestAdminCtrlDAtThePrompt(t *testing.T) {
	a := newAdminAgent(t)
	term := &fake.Terminal{TTY: true, Script: []fake.Input{{Err: io.EOF}}}
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-008]") || a.verifies() != 0 {
		t.Fatalf("exit %d verifies %d\n%s", r.code, a.verifies(), r.errOut)
	}
}

func TestAdminEmptyPINAtThePrompt(t *testing.T) {
	a := newAdminAgent(t)
	r := runCLI(t, adminRegistry(), a.opts(fake.Typed(true, "")), "rm")
	if r.code != ExitDenied || a.verifies() != 0 {
		t.Fatalf("exit %d verifies %d", r.code, a.verifies())
	}
}

// A 403 that is not a request for the PIN (not an operator at all) is returned untouched.
func TestAdminOtherForbiddenIsNotAPINRequest(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.RespondError(http.MethodDelete, "/models/x", 403, api.CodePeerNotOperator, "Not an operator.", "Ask an administrator.", nil)
	term := fake.Typed(true, testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-SEC-013]") || term.Reads() != 0 {
		t.Fatalf("exit %d reads %d\n%s", r.code, term.Reads(), r.errOut)
	}
}

// The request is retried once: if the agent still wants a PIN (the grant expired at once), that is
// the answer, and the user is not asked again.
func TestAdminRetriesOnlyOnce(t *testing.T) {
	a := newAdminAgent(t)
	a.badGrant = true
	term := fake.Typed(true, testPIN, testPIN)
	r := runCLI(t, adminRegistry(), a.opts(term), "rm")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-SEC-002]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if term.Reads() != 1 || a.verifies() != 1 || len(deleteGrants(a)) != 2 {
		t.Errorf("reads %d verifies %d deletes %d", term.Reads(), a.verifies(), len(deleteGrants(a)))
	}
}

// A grant the agent returns empty is an answer aictl cannot use.
func TestAdminEmptyGrant(t *testing.T) {
	a := newAdminAgent(t)
	a.h.agent.Respond(http.MethodPost, "/pin/verify", 200, `{"grant":""}`)
	r := runCLI(t, adminRegistry(), a.opts(fake.Typed(true, testPIN)), "rm")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-CLI-009]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

// The PIN and the grant stay out of LOCALAI_CLI_DEBUG's log of requests and responses.
func TestAdminSecretsStayOutOfTheDebugLog(t *testing.T) {
	a := newAdminAgent(t)
	o := a.opts(fake.Typed(true, testPIN))
	o.env["LOCALAI_CLI_DEBUG"] = "1"
	r := runCLI(t, adminRegistry(), o, "rm")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if !strings.Contains(r.errOut, `"path":"/api/v1/pin/verify"`) {
		t.Fatalf("the debug log should show the verify request:\n%s", r.errOut)
	}
	for _, secret := range []string{testPIN, testGrant, testGrant[:20]} {
		// leakcheck, not strings.Contains: the log's nanosecond timestamps can contain the PIN's digits.
		if leakcheck.Contains(r.errOut, secret) {
			t.Errorf("debug log contains %q:\n%s", secret, r.errOut)
		}
	}
}

// Retry-After is read in seconds and nothing else.
func TestRetryAfterParsing(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want int
	}{{"252", 252}, {" 5 ", 5}, {"", 0}, {"0", 0}, {"-3", 0}, {"Wed, 21 Oct 2026 07:28:00 GMT", 0}, {"1.5", 0}} {
		h := http.Header{}
		if tc.v != "" {
			h.Set("Retry-After", tc.v)
		}
		if got := retryAfter(h); int(got.Seconds()) != tc.want {
			t.Errorf("Retry-After %q = %v, want %ds", tc.v, got, tc.want)
		}
	}
}
