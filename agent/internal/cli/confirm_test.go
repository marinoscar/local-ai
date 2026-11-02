package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// confirmRegistry has commands that ask for a confirmation and report what they did on stdout.
func confirmRegistry() *Registry {
	reg := NewRegistry()
	say := func(c *Call, s string) {
		if c.Global.JSON {
			c.Out.JSON().Document(map[string]string{"result": s})
			return
		}
		c.Out.Result(s)
	}
	reg.MustAdd(&Command{
		Name: "wipe", Summary: "Asks yes or no.",
		Run: func(c *Call) error {
			ok, err := c.Confirm("Delete qwen3.6-35b-a3b-q4km from this computer (19.0 GB)?")
			if err != nil {
				return err
			}
			if !ok {
				say(c, "cancelled")
				return nil
			}
			say(c, "deleted")
			return nil
		},
	})
	reg.MustAdd(&Command{
		Name: "wipe-last", Summary: "Asks for the model id.",
		Run: func(c *Call) error {
			ok, err := c.ConfirmTyped("This is the last verified copy anywhere. Type the model id to confirm:", "qwen3.6-35b-a3b-q4km")
			if err != nil {
				return err
			}
			if !ok {
				say(c, "cancelled")
				return nil
			}
			say(c, "deleted")
			return nil
		},
	})
	return reg
}

func TestConfirm(t *testing.T) {
	const question = "Delete qwen3.6-35b-a3b-q4km from this computer (19.0 GB)? [y/N] "
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		term       *fake.Terminal
		stdin      string // used when term is nil
		wantCode   int
		wantOut    string
		wantPrompt bool // the question was written to stderr
		wantReads  int
	}{
		{name: "y", term: fake.Typed(true, "y"), wantOut: "deleted\n", wantPrompt: true, wantReads: 1},
		{name: "yes in capitals", term: fake.Typed(true, " YES "), wantOut: "deleted\n", wantPrompt: true, wantReads: 1},
		{name: "n", term: fake.Typed(true, "n"), wantOut: "cancelled\n", wantPrompt: true, wantReads: 1},
		{name: "Enter is No", term: fake.Typed(true, ""), wantOut: "cancelled\n", wantPrompt: true, wantReads: 1},
		{name: "anything else is No", term: fake.Typed(true, "sure"), wantOut: "cancelled\n", wantPrompt: true, wantReads: 1},
		{name: "s is not yes in English", term: fake.Typed(true, "s"), wantOut: "cancelled\n", wantPrompt: true, wantReads: 1},
		{name: "Ctrl-D is No", term: &fake.Terminal{TTY: true, Script: []fake.Input{{Err: io.EOF}}}, wantOut: "cancelled\n", wantPrompt: true, wantReads: 1},
		{name: "--yes asks nothing", args: []string{"--yes"}, term: fake.Typed(true), wantOut: "deleted\n", wantReads: 0},
		{name: "-y asks nothing", args: []string{"-y"}, term: fake.Typed(false), wantOut: "deleted\n", wantReads: 0},
		{name: "--yes with --json", args: []string{"--json", "--yes"}, term: fake.Typed(false), wantOut: `{"result":"deleted"}` + "\n", wantReads: 0},
		{name: "no terminal, no --yes", term: fake.Typed(false, "y"), wantCode: ExitUsage, wantReads: 0},
		{name: "closed stdin, no --yes", stdin: "", wantCode: ExitUsage},
		{name: "piped answer is never read", stdin: "y\n", wantCode: ExitUsage},
		{name: "--json on a terminal without --yes", args: []string{"--json"}, term: fake.Typed(true, "y"), wantCode: ExitUsage, wantReads: 0},
		{name: "Ctrl-C at the question", term: &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}, wantCode: ExitInterrupted, wantPrompt: true, wantReads: 1},
		{name: "Spanish s", env: map[string]string{"LANG": "es_CR.UTF-8"}, term: fake.Typed(true, "s"), wantOut: "deleted\n", wantPrompt: true, wantReads: 1},
		{name: "Spanish sí", args: []string{"--lang", "es"}, term: fake.Typed(true, "sí"), wantOut: "deleted\n", wantPrompt: true, wantReads: 1},
		{name: "Spanish y", args: []string{"--lang", "es"}, term: fake.Typed(true, "y"), wantOut: "deleted\n", wantPrompt: true, wantReads: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := runOpts{env: tc.env, stdin: tc.stdin}
			if tc.term != nil {
				o.term = tc.term
			}
			r := runCLI(t, confirmRegistry(), o, append(append([]string{}, tc.args...), "wipe")...)
			if r.code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout %q\nstderr %q", r.code, tc.wantCode, r.out, r.errOut)
			}
			if tc.wantCode == 0 && r.out != tc.wantOut {
				t.Errorf("stdout = %q, want %q", r.out, tc.wantOut)
			}
			spanish := tc.env["LANG"] != "" || strings.Contains(strings.Join(tc.args, " "), "es")
			q := question
			if spanish {
				q = strings.Replace(q, "[y/N]", "[s/N]", 1)
			}
			if got := strings.Contains(r.errOut, q); got != tc.wantPrompt {
				t.Errorf("question on stderr = %v, want %v: %q", got, tc.wantPrompt, r.errOut)
			}
			if tc.term != nil && tc.term.Reads() != tc.wantReads {
				t.Errorf("reads = %d, want %d", tc.term.Reads(), tc.wantReads)
			}
			if tc.wantCode == ExitUsage {
				if !strings.Contains(r.errOut+r.out, "LAI-CLI-007") {
					t.Errorf("exit 2 must carry LAI-CLI-007: %q %q", r.errOut, r.out)
				}
			}
			if tc.wantCode == ExitInterrupted && strings.Contains(r.errOut, "LAI-CLI") {
				t.Errorf("Ctrl-C at a prompt prints no problem: %q", r.errOut)
			}
		})
	}
}

// Without --yes and without a terminal the failure explains itself in plain words and with the
// code; with --json it is the envelope on stdout and stderr stays empty.
func TestConfirmRefusalOutput(t *testing.T) {
	r := runCLI(t, confirmRegistry(), runOpts{env: map[string]string{"LANG": "en_US.UTF-8"}}, "wipe")
	if r.code != ExitUsage || r.out != "" {
		t.Fatalf("exit %d out %q", r.code, r.out)
	}
	for _, want := range []string{"✗ This command needs a yes/no confirmation.  [LAI-CLI-007]", "→ Fix: Run it in a terminal, or add `--yes`."} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	es := runCLI(t, confirmRegistry(), runOpts{env: map[string]string{"LANG": "es_CR.UTF-8"}}, "wipe")
	if !strings.Contains(es.errOut, "[LAI-CLI-007]") || !strings.Contains(es.errOut, "→ Solución: Ejecútelo en una terminal o agregue `--yes`.") {
		t.Errorf("Spanish refusal:\n%s", es.errOut)
	}
	j := runCLI(t, confirmRegistry(), runOpts{term: fake.Typed(true, "y")}, "--json", "wipe")
	if j.code != ExitUsage || j.errOut != "" {
		t.Fatalf("json: exit %d stderr %q", j.code, j.errOut)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
			Fix  string `json:"fix"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(j.out), &env); err != nil || env.Error.Code != "LAI-CLI-007" || env.Error.Fix == "" {
		t.Errorf("envelope: %v %q", err, j.out)
	}
}

func TestConfirmTyped(t *testing.T) {
	const prompt = "This is the last verified copy anywhere. Type the model id to confirm: "
	tests := []struct {
		name       string
		args       []string
		term       *fake.Terminal
		wantCode   int
		wantOut    string
		wantPrompt bool
	}{
		{name: "exact id", term: fake.Typed(true, "qwen3.6-35b-a3b-q4km"), wantOut: "deleted\n", wantPrompt: true},
		{name: "id with spaces around", term: fake.Typed(true, "  qwen3.6-35b-a3b-q4km "), wantOut: "deleted\n", wantPrompt: true},
		{name: "wrong id", term: fake.Typed(true, "qwen3.6"), wantOut: "cancelled\n", wantPrompt: true},
		{name: "different case is wrong", term: fake.Typed(true, "QWEN3.6-35B-A3B-Q4KM"), wantOut: "cancelled\n", wantPrompt: true},
		{name: "y is not the id", term: fake.Typed(true, "y"), wantOut: "cancelled\n", wantPrompt: true},
		{name: "empty", term: fake.Typed(true, ""), wantOut: "cancelled\n", wantPrompt: true},
		{name: "Ctrl-D", term: &fake.Terminal{TTY: true, Script: []fake.Input{{Err: io.EOF}}}, wantOut: "cancelled\n", wantPrompt: true},
		{name: "--yes does not answer it on a terminal", args: []string{"--yes"}, term: fake.Typed(true, "nope"), wantOut: "cancelled\n", wantPrompt: true},
		{name: "--yes cannot answer it without a terminal", args: []string{"--yes"}, term: fake.Typed(false, "qwen3.6-35b-a3b-q4km"), wantCode: ExitUsage},
		{name: "no terminal", term: fake.Typed(false), wantCode: ExitUsage},
		{name: "--json and --yes", args: []string{"--json", "--yes"}, term: fake.Typed(true, "qwen3.6-35b-a3b-q4km"), wantCode: ExitUsage},
		{name: "Ctrl-C", term: &fake.Terminal{TTY: true, Script: []fake.Input{{Err: context.Canceled}}}, wantCode: ExitInterrupted, wantPrompt: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, confirmRegistry(), runOpts{term: tc.term}, append(append([]string{}, tc.args...), "wipe-last")...)
			if r.code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout %q\nstderr %q", r.code, tc.wantCode, r.out, r.errOut)
			}
			if tc.wantCode == ExitUsage {
				if !strings.Contains(r.errOut+r.out, "LAI-CLI-007") || !strings.Contains(r.errOut+r.out, "does not answer this question") {
					t.Errorf("refusal: %q %q", r.errOut, r.out)
				}
				if tc.term.Reads() != 0 {
					t.Errorf("a refused confirmation must read nothing, read %d", tc.term.Reads())
				}
				return
			}
			if r.out != tc.wantOut && tc.wantCode == 0 {
				t.Errorf("stdout = %q, want %q", r.out, tc.wantOut)
			}
			if got := strings.Contains(r.errOut, prompt); got != tc.wantPrompt {
				t.Errorf("prompt on stderr = %v: %q", got, r.errOut)
			}
		})
	}
}

// A reader-backed stdin (the default when no Terminal is injected) is never interactive, so a
// confirmation never consumes data meant for a command.
func TestStreamTerminalIsNotInteractive(t *testing.T) {
	st := newStreamTerminal(strings.NewReader("first\r\nsecond\nlast"))
	if st.Interactive() {
		t.Fatal("a stream is not interactive")
	}
	for _, want := range []string{"first", "second", "last"} {
		got, err := st.ReadLine(context.Background())
		if err != nil || got != want {
			t.Fatalf("ReadLine = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := st.ReadSecret(context.Background()); err != io.EOF {
		t.Errorf("end of input = %v, want io.EOF", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.ReadLine(ctx); err != context.Canceled {
		t.Errorf("cancelled read = %v", err)
	}
}

func TestReadLineBoundsRunawayInput(t *testing.T) {
	long := strings.Repeat("x", 3*maxLineBytes) + "\nnext\n"
	st := newStreamTerminal(strings.NewReader(long))
	got, err := st.ReadLine(context.Background())
	if err != nil || len(got) != maxLineBytes {
		t.Fatalf("len %d err %v, want %d bytes kept", len(got), err, maxLineBytes)
	}
	if next, _ := st.ReadLine(context.Background()); next != "next" {
		t.Errorf("the rest of the long line must be discarded, got %q", next)
	}
}
