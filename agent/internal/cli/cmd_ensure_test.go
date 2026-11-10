package cli

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl ensure` (aictl.md section 4.23d, ADR-0010). Regenerate the golden files with:
// go test ./internal/cli -run Ensure -update-ensure

var updateEnsure = flag.Bool("update-ensure", false, "rewrite the golden files of aictl ensure")

func ensureGolden(t *testing.T, name string, got followOut) {
	t.Helper()
	text := "exit " + itoa(got.code) + "\n--- stdout\n" + got.out + "--- stderr\n" + got.errOut
	path := filepath.Join("testdata", "ensure", name+".golden")
	if *updateEnsure {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-ensure): %v", path, err)
	}
	if string(want) != text {
		t.Errorf("%s differs from the golden file:\n%s", path, text)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// view builds a body of GET /ensure the way the agent writes it.
func ensureView(state, message, next string, steps ...string) string {
	body := `{"state":"` + state + `","auto":"on","message":"` + message + `","next_action":{"kind":"` + next + `","text":"Press Install what's missing."},` +
		`"progress":{"done":0,"total":` + itoa(len(steps)) + `},"steps":[` + strings.Join(steps, ",") + `],"run":null,"dry_run":false,"checked_at":"2026-11-10T12:00:00Z"}`
	return body
}

func ensureStep(id, status, title, detail, extra string) string {
	return `{"id":"` + id + `","kind":"install_tool","status":"` + status + `","needs_confirmation":false,"title":"` + title + `","detail":"` + detail + `"` + extra + `}`
}

var (
	stepKiwixPending = ensureStep("tool.kiwix-tools", "pending", "Library tools (kiwix-tools)", "Install version 3.7 from the kit.", "")
	stepPiperPending = ensureStep("tool.piper", "pending", "Voice tools (piper)", "Install version 2023.11.14-2 from the kit.", "")
	stepKiwixDone    = ensureStep("tool.kiwix-tools", "done", "Library tools (kiwix-tools)", "Installed from the kit (3.7).", "")
	stepPiperDone    = ensureStep("tool.piper", "done", "Voice tools (piper)", "Installed from the kit (2023.11.14-2).", "")
	stepBuildConfirm = `{"id":"whisper.build","kind":"build_whisper","status":"needs_confirm","needs_confirmation":true,"title":"Speech-to-text program (build from source)","detail":"Building it takes a long time, so it starts only when you confirm."}`
	stepPiperFailed  = ensureStep("tool.piper", "failed", "Voice tools (piper)", "It did not install.", `,"code":"LAI-KIT-047","error":"piper: could not be installed"`)
)

const completeBody = `{"state":"complete","auto":"on","message":"Complete: this computer has everything it needs.","next_action":{"kind":"none","text":"Nothing to do."},` +
	`"progress":{"done":0,"total":0},"steps":[],"run":null,"dry_run":false,"checked_at":"2026-11-10T12:00:00Z"}`

// sequence answers the successive GETs with the bodies in order, then the last one for ever.
func (r *mgRig) sequence(bodies ...string) *atomic.Int32 {
	var n atomic.Int32
	r.agent().Handle(http.MethodGet, "/ensure", func(w http.ResponseWriter, _ *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(bodies[i]))
	})
	return &n
}

func newEnsureRig(t *testing.T) *mgRig {
	t.Helper()
	old := ensurePoll
	ensurePoll = time.Millisecond
	t.Cleanup(func() { ensurePoll = old })
	return newMgRig(t, fake.Options{})
}

func TestEnsureWhenNothingIsMissing(t *testing.T) {
	r := newEnsureRig(t)
	r.sequence(completeBody)
	got := r.run(mgArgs("ensure"))
	ensureGolden(t, "complete-en", got)
	if got.code != 0 || strings.Join(r.requestLines(), ",") != "GET /ensure" {
		t.Errorf("%d %v", got.code, r.requestLines())
	}
	ensureGolden(t, "complete-es", r.run(mgArgs("--lang", "es", "ensure")))
	j := r.run(mgArgs("--json", "ensure"))
	checkRun(t, j, 0, completeBody+"\n", "")
}

func TestEnsureInstallsWhatIsMissingAndFollowsIt(t *testing.T) {
	r := newEnsureRig(t)
	idle := ensureView("needs_you", "Needs you: 2 things are ready to install. Press Install what's missing.", "press_button", stepKiwixPending, stepPiperPending)
	running := strings.Replace(ensureView("installing", "Installing what's missing (2 of 2)", "wait", stepKiwixDone, strings.Replace(stepPiperPending, "pending", "running", 1)), `"progress":{"done":0,"total":2}`, `"progress":{"done":1,"total":2,"current":"tool.piper"}`, 1)
	done := ensureView("complete", "Complete: this computer has everything it needs.", "none", stepKiwixDone, stepPiperDone)
	r.sequence(idle, running, running, done)
	r.agent().Respond(http.MethodPost, "/ensure", 202, strings.Replace(running, "(2 of 2)", "(1 of 2)", 1))
	got := r.run(mgArgs("ensure"))
	ensureGolden(t, "install-en", got)
	if got.code != 0 {
		t.Fatalf("exit %d", got.code)
	}
	if lines := strings.Join(r.requestLines(), ","); !strings.HasPrefix(lines, "GET /ensure,POST /ensure,GET /ensure") {
		t.Errorf("requests %s", lines)
	}
	if b := r.lastBody(http.MethodPost, "/ensure"); b != `{}` {
		t.Errorf("a plain run sends no confirmation: %s", b)
	}
}

func TestEnsureInSpanish(t *testing.T) {
	r := newEnsureRig(t)
	idle := strings.ReplaceAll(ensureView("needs_you", "Necesita su ayuda: 2 cosas están listas para instalar.", "press_button", stepKiwixPending), "Install version 3.7 from the kit.", "Instalar la versión 3.7 desde el kit.")
	done := ensureView("complete", "Completo: esta computadora tiene todo lo que necesita.", "none", stepKiwixDone)
	r.sequence(idle, done)
	r.agent().Respond(http.MethodPost, "/ensure", 202, ensureView("installing", "Instalando lo que falta (1 de 1)", "wait", stepKiwixPending))
	got := r.run(mgArgs("--lang", "es", "ensure"))
	ensureGolden(t, "install-es", got)
}

func TestEnsureDryRunChangesNothing(t *testing.T) {
	r := newEnsureRig(t)
	r.agent().Respond(http.MethodPost, "/ensure", 200, strings.Replace(ensureView("needs_you", "Needs you: 2 things are ready to install. Press Install what's missing.", "press_button", stepKiwixPending, stepPiperPending), `"dry_run":false`, `"dry_run":true`, 1))
	got := r.run(mgArgs("ensure", "--dry-run"))
	ensureGolden(t, "dry-run-en", got)
	if b := r.lastBody(http.MethodPost, "/ensure"); b != `{"dry_run":true}` {
		t.Errorf("body %s", b)
	}
	if got.code != 0 {
		t.Errorf("exit %d", got.code)
	}
	j := r.run(mgArgs("--json", "ensure", "--dry-run"))
	if j.code != 0 || !strings.Contains(j.out, `"dry_run":true`) || j.errOut != "" {
		t.Errorf("%+v", j)
	}
}

func TestEnsureNeverCompilesWithoutAConfirmation(t *testing.T) {
	r := newEnsureRig(t)
	needs := ensureView("needs_you", "Needs you: building the speech-to-text program takes a long time and needs your OK.", "confirm", stepBuildConfirm)
	r.sequence(needs)
	t.Run("a script without --yes sends no confirmation and does not even start", func(t *testing.T) {
		got := r.run(mgArgs("ensure"))
		ensureGolden(t, "confirm-script-en", got)
		if got.code != ExitWarnings || strings.Contains(strings.Join(r.requestLines(), ","), "POST") {
			t.Errorf("%d %v", got.code, r.requestLines())
		}
	})
	t.Run("--yes confirms it", func(t *testing.T) {
		r := newEnsureRig(t)
		r.sequence(needs, ensureView("installing", "Installing what's missing (1 of 1)", "wait", stepBuildConfirm), ensureView("complete", "Complete: this computer has everything it needs.", "none"))
		r.agent().Respond(http.MethodPost, "/ensure", 202, ensureView("installing", "Installing what's missing (1 of 1)", "wait", stepBuildConfirm))
		got := r.run(mgArgs("ensure", "--yes"))
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, "/ensure"); b != `{"confirm":["whisper.build"]}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("at a terminal the person is asked, and no is respected", func(t *testing.T) {
		r := newEnsureRig(t)
		r.sequence(needs)
		got := r.run(mgArgs("ensure").withTerm("n"))
		ensureGolden(t, "confirm-no-en", got)
		if got.code != ExitWarnings || strings.Contains(strings.Join(r.requestLines(), ","), "POST") {
			t.Errorf("%d %v", got.code, r.requestLines())
		}
	})
	t.Run("at a terminal yes starts it", func(t *testing.T) {
		r := newEnsureRig(t)
		r.sequence(needs, ensureView("complete", "Complete: this computer has everything it needs.", "none"))
		r.agent().Respond(http.MethodPost, "/ensure", 200, ensureView("complete", "Complete: this computer has everything it needs.", "none"))
		got := r.run(mgArgs("ensure").withTerm("y"))
		if got.code != 0 || r.lastBody(http.MethodPost, "/ensure") != `{"confirm":["whisper.build"]}` {
			t.Errorf("%+v", got)
		}
	})
}

func TestEnsureAFailedStepIsExit9WithItsCodeAndFix(t *testing.T) {
	r := newEnsureRig(t)
	idle := ensureView("needs_you", "Needs you: 2 things are ready to install. Press Install what's missing.", "press_button", stepKiwixPending, stepPiperPending)
	failed := ensureView("needs_you", "Needs you: piper could not be installed.", "fix", stepKiwixDone, stepPiperFailed)
	r.sequence(idle)
	r.agent().Respond(http.MethodPost, "/ensure", 200, failed)
	got := r.run(mgArgs("ensure"))
	ensureGolden(t, "failed-en", got)
	if got.code != ExitErrors {
		t.Errorf("exit %d", got.code)
	}
}

func TestEnsureCtrlCStopsThePassAndKeepsWhatWasInstalled(t *testing.T) {
	r := newEnsureRig(t)
	idle := ensureView("needs_you", "Needs you: 1 thing is ready to install. Press Install what's missing.", "press_button", stepKiwixPending)
	running := ensureView("installing", "Installing what's missing (1 of 1)", "wait", strings.Replace(stepKiwixPending, "pending", "running", 1))
	n := r.sequence(idle, running)
	r.agent().Respond(http.MethodPost, "/ensure", 202, running)
	r.agent().Respond(http.MethodDelete, "/ensure", 202, ensureView("complete", "Complete", "none"))
	ch, _ := r.runAsync(mgArgs("ensure"))
	untilTrue(t, "the pass is followed", func() bool { return n.Load() >= 3 })
	time.Sleep(20 * time.Millisecond) // the command is in its loop: its handler for Ctrl-C is installed
	sendSIGINT(t)
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.out, "Stopped. What was already installed stays") {
		t.Fatalf("%+v", got)
	}
}

func TestEnsureErrors(t *testing.T) {
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("ensure"))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a sync run is active", func(t *testing.T) {
		r := newEnsureRig(t)
		r.sequence(ensureView("needs_you", "Needs you: 1 thing is ready to install. Press Install what's missing.", "press_button", stepKiwixPending))
		r.agent().RespondError(http.MethodPost, "/ensure", 409, "LAI-SYN-005", "A run is already active.", "Wait for it to finish.", map[string]any{"run_id": "run-1"})
		got := r.run(mgArgs("ensure"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-SYN-005]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an argument is a usage error before any request", func(t *testing.T) {
		r := newEnsureRig(t)
		got := r.run(mgArgs("ensure", "kiwix"))
		if got.code != ExitUsage || len(r.requestLines()) != 0 {
			t.Errorf("%+v %v", got, r.requestLines())
		}
	})
	t.Run("the agent has no such route (an older agent)", func(t *testing.T) {
		r := newEnsureRig(t)
		r.agent().RespondError(http.MethodGet, "/ensure", 404, "LAI-AGT-021", "Not found.", "Update the agent.", map[string]any{"reason": "unknown_route"})
		if got := r.run(mgArgs("ensure")); got.code != ExitNotFound {
			t.Errorf("%+v", got)
		}
	})
}

func TestEnsureIsInTheMenu(t *testing.T) {
	found := false
	var walk func(items []menuItem)
	walk = func(items []menuItem) {
		for _, it := range items {
			if len(it.args) == 1 && it.args[0] == "ensure" && it.en == "Install what is missing (from the kit)" && it.es != "" {
				found = true
			}
			walk(it.sub)
		}
	}
	walk(menuTree())
	if !found {
		t.Error("the menu has no entry for aictl ensure")
	}
}
