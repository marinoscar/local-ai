package cli

import (
	"strings"
	"testing"
)

// The colour and symbol decisions of aictl.md sections 3.1 and 3.3, end to end through Run: a
// failing command's problem goes to stderr, coloured only when stdout is a terminal and nothing
// turns it off, and the symbol is printed either way.
func TestColourDecisionEndToEnd(t *testing.T) {
	const esc = "\x1b["
	utf8 := map[string]string{"LANG": "en_US.UTF-8"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{"LANG": "en_US.UTF-8"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name   string
		env    map[string]string
		tty    bool
		args   []string
		colour bool
	}{
		{"terminal", utf8, true, nil, true},
		{"stdout not a terminal", utf8, false, nil, false},
		{"--no-color", utf8, true, []string{"--no-color"}, false},
		{"NO_COLOR", with("NO_COLOR", "1"), true, nil, false},
		{"TERM=dumb", with("TERM", "dumb"), true, nil, false},
		{"TERM=xterm", with("TERM", "xterm-256color"), true, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, confirmRegistry(), runOpts{env: tc.env, tty: tc.tty}, append(append([]string{}, tc.args...), "wipe")...)
			if r.code != ExitUsage {
				t.Fatalf("exit %d", r.code)
			}
			if got := strings.Contains(r.errOut, esc); got != tc.colour {
				t.Errorf("colour = %v, want %v: %q", got, tc.colour, r.errOut)
			}
			if !strings.Contains(r.errOut, "✗") || !strings.Contains(r.errOut, "[LAI-CLI-007]") {
				t.Errorf("the symbol and the code are always printed: %q", r.errOut)
			}
		})
	}
	t.Run("coloured problem layout", func(t *testing.T) {
		r := runCLI(t, confirmRegistry(), runOpts{env: utf8, tty: true}, "wipe")
		want := "\x1b[31m✗\x1b[0m This command needs a yes/no confirmation.  \x1b[2m[LAI-CLI-007]\x1b[0m\n" +
			"  → Fix: Run it in a terminal, or add `--yes`.\n"
		if r.errOut != want {
			t.Errorf("stderr = %q\nwant     %q", r.errOut, want)
		}
	})
	t.Run("--json never colours, even on a terminal", func(t *testing.T) {
		r := runCLI(t, confirmRegistry(), runOpts{env: utf8, tty: true}, "--json", "wipe")
		if strings.Contains(r.out+r.errOut, esc) || r.errOut != "" {
			t.Errorf("out %q err %q", r.out, r.errOut)
		}
	})
}

// UTF-8 or ASCII symbols follow LC_ALL then LANG.
func TestSymbolSetFollowsTheLocale(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"UTF-8", map[string]string{"LANG": "en_US.UTF-8"}, "✗ This command needs a yes/no confirmation.  [LAI-CLI-007]\n  → Fix:"},
		{"C locale", map[string]string{"LANG": "C"}, "[x] This command needs a yes/no confirmation.  [LAI-CLI-007]\n  -> Fix:"},
		{"nothing set", nil, "[x] This command needs a yes/no confirmation.  [LAI-CLI-007]\n  -> Fix:"},
		{"LC_ALL wins", map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "C"}, "[x] This"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, confirmRegistry(), runOpts{env: tc.env}, "wipe")
			if !strings.HasPrefix(r.errOut, tc.want) {
				t.Errorf("stderr = %q, want prefix %q", r.errOut, tc.want)
			}
		})
	}
}

// --lang wins over $LANG, which wins over the default (English); the code is never translated.
func TestLanguageSelection(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string // the fix label
	}{
		{"default", nil, nil, "→ Fix:"},
		{"LANG es_CR.UTF-8", map[string]string{"LANG": "es_CR.UTF-8"}, nil, "→ Solución:"},
		{"LANG en", map[string]string{"LANG": "en_US.UTF-8"}, nil, "→ Fix:"},
		{"--lang es over LANG en", map[string]string{"LANG": "en_US.UTF-8"}, []string{"--lang", "es"}, "→ Solución:"},
		{"--lang en over LANG es", map[string]string{"LANG": "es_CR.UTF-8"}, []string{"--lang", "en"}, "→ Fix:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"LC_ALL": "C.UTF-8"}
			for k, v := range tc.env {
				env[k] = v
			}
			r := runCLI(t, confirmRegistry(), runOpts{env: env}, append(append([]string{}, tc.args...), "wipe")...)
			if !strings.Contains(r.errOut, tc.want) || !strings.Contains(r.errOut, "[LAI-CLI-007]") {
				t.Errorf("stderr = %q, want %q and the untranslated code", r.errOut, tc.want)
			}
		})
	}
}

// Every failure in --json mode is exactly one document on stdout with an empty stderr, whatever the
// severity of the code (LAI-CLI-010 is "info" in the registry and is still a failure).
func TestJSONFailureIsOneDocumentWithEmptyStderr(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "wipe"},
		{"--json", "no-such-command"},
		{"--json", "--lang", "fr", "wipe"},
		{"--json"},
	} {
		r := runCLI(t, confirmRegistry(), runOpts{}, args...)
		if r.code == 0 || r.errOut != "" {
			t.Errorf("%v: exit %d stderr %q", args, r.code, r.errOut)
		}
		if strings.Count(strings.TrimSuffix(r.out, "\n"), "\n") != 0 || !strings.HasPrefix(r.out, `{"error":{"code":"LAI-CLI-`) {
			t.Errorf("%v: stdout %q", args, r.out)
		}
	}
}

// If a command already wrote its one document and then fails, stdout keeps that document: no second
// document, nothing on stderr; only the exit code reports the failure.
func TestJSONFailureAfterTheDocumentKeepsOneDocument(t *testing.T) {
	reg := NewRegistry()
	reg.MustAdd(&Command{Name: "half", Summary: "Writes then fails.", Run: func(c *Call) error {
		if err := c.Out.JSON().Document(map[string]int{"n": 1}); err != nil {
			return err
		}
		return c.Err(CodeUsage, "")
	}})
	r := runCLI(t, reg, runOpts{}, "--json", "half")
	if r.code != ExitUsage || r.out != "{\"n\":1}\n" || r.errOut != "" {
		t.Errorf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

// -q hides what is not an error; -v adds the details and the request id under a problem.
func TestQuietAndVerboseProblems(t *testing.T) {
	reg := NewRegistry()
	reg.MustAdd(&Command{Name: "boom", Summary: "Fails with details.", Run: func(c *Call) error {
		e := c.Err(CodeUsage, "Unknown thing.").WithDetails("thing", "x")
		e.RequestID = "req_ABCDEFGHJKLM"
		return e
	}})
	plain := runCLI(t, reg, runOpts{}, "boom")
	if strings.Contains(plain.errOut, "details:") || strings.Contains(plain.errOut, "req_") {
		t.Errorf("details and request id only with -v: %q", plain.errOut)
	}
	v := runCLI(t, reg, runOpts{}, "-v", "boom")
	if !strings.Contains(v.errOut, `  details: {"thing":"x"}`) || !strings.Contains(v.errOut, "  request: req_ABCDEFGHJKLM") {
		t.Errorf("-v: %q", v.errOut)
	}
	q := runCLI(t, reg, runOpts{}, "-q", "boom")
	if !strings.Contains(q.errOut, "[LAI-CLI-005]") {
		t.Errorf("-q still shows errors: %q", q.errOut)
	}
}
