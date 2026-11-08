package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// `aictl ark hf-token set|status` (issue #435). The token must never appear in argv, output, errors,
// --json or the audit log.

const testHFToken = "hf_PlaceholderTokenValue0123456789"

// hfNoLeak fails when the token is anywhere the person or a log could see it.
func hfNoLeak(t *testing.T, f *localFixture, r runResult) {
	t.Helper()
	secretFree(t, testHFToken, r.out, r.errOut)
	if b, err := os.ReadFile(filepath.Join(f.env.Paths.StateDir, "audit.jsonl")); err == nil && strings.Contains(string(b), testHFToken) {
		t.Error("the token is in the audit log")
	}
}

func debugOpts(term Terminal) runOpts {
	o := localOpts(term)
	o.env["LOCALAI_CLI_DEBUG"] = "1"
	return o
}

func TestHFTokenSetInteractive(t *testing.T) {
	applyLate(DefaultRegistry)
	f := newLocalFixture(t)
	term := fake.Typed(true, "  "+testHFToken+"  ")
	r := runCLI(t, DefaultRegistry, debugOpts(term), "ark", "hf-token", "set")
	if r.code != 0 || unwrap(r.out) != "✓ The Hugging Face token was saved. The next sync uses it for models that need a login.\n" {
		t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
	}
	if !strings.Contains(r.errOut, "Hugging Face token (it is not shown): ") {
		t.Errorf("prompt missing: %q", r.errOut)
	}
	if k := term.Kinds(); len(k) != 1 || k[0] != "secret" {
		t.Errorf("read kinds %v, want one read without echo", k)
	}
	p := filepath.Join(f.env.Paths.EtcDir, "hf-token")
	if readFile(t, p) != testHFToken+"\n" || fileMode(t, p) != 0o600 {
		t.Errorf("file %q mode %o", readFile(t, p), fileMode(t, p))
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "hf_token.set" || lines[0]["result"] != "ok" {
		t.Errorf("audit %v", lines)
	}
	hfNoLeak(t, f, r)

	st := runCLI(t, DefaultRegistry, localOpts(nil), "ark", "hf-token", "status")
	if st.code != 0 || st.out != "✓ A Hugging Face token is set.\n" {
		t.Errorf("status %+v", st)
	}
	js := runCLI(t, DefaultRegistry, localOpts(nil), "--json", "ark", "hf-token", "status")
	if js.code != 0 || js.out != `{"set":true}`+"\n" {
		t.Errorf("status json %+v", js)
	}
	hfNoLeak(t, f, st)
}

func TestHFTokenSetStdin(t *testing.T) {
	applyLate(DefaultRegistry)
	f := newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, debugOpts(fake.Typed(false, testHFToken)), "--json", "ark", "hf-token", "set", "--stdin")
	want := `{"path":"` + filepath.Join(f.env.Paths.EtcDir, "hf-token") + `","set":true}` + "\n"
	if r.code != 0 || r.out != want || r.errOut != "" {
		t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
	}
	if readFile(t, filepath.Join(f.env.Paths.EtcDir, "hf-token")) != testHFToken+"\n" {
		t.Error("file content")
	}
	hfNoLeak(t, f, r)
}

func TestHFTokenSetRefusals(t *testing.T) {
	cases := []struct {
		name string
		opts func() runOpts
		args []string
		code int
		want string
	}{
		{"the token as an argument is never echoed", func() runOpts { return localOpts(nil) }, []string{"ark", "hf-token", "set", testHFToken}, ExitUsage, "never given on the command line"},
		{"no terminal and no --stdin", func() runOpts { return localOpts(nil) }, []string{"ark", "hf-token", "set"}, ExitUsage, "There is no terminal to ask for the token."},
		{"--json without --stdin", func() runOpts { return localOpts(fake.Typed(true, testHFToken)) }, []string{"--json", "ark", "hf-token", "set"}, ExitUsage, "There is no terminal"},
		{"empty", func() runOpts { return localOpts(fake.Typed(true, "")) }, []string{"ark", "hf-token", "set"}, ExitUsage, "No token was entered"},
		{"a space inside", func() runOpts { return localOpts(fake.Typed(false, "hf_abc def ghi")) }, []string{"ark", "hf-token", "set", "--stdin"}, ExitUsage, "does not look like a Hugging Face token"},
		{"too short", func() runOpts { return localOpts(fake.Typed(false, "hf_x")) }, []string{"ark", "hf-token", "set", "--stdin"}, ExitUsage, "does not look like"},
		{"not root", func() runOpts { o := localOpts(fake.Typed(true, testHFToken)); o.euid = 1000; return o }, []string{"ark", "hf-token", "set"}, ExitDenied, "LAI-CLI-006"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applyLate(DefaultRegistry)
			f := newLocalFixture(t)
			r := runCLI(t, DefaultRegistry, tc.opts(), tc.args...)
			if r.code != tc.code || !strings.Contains(unwrap(r.out+r.errOut), tc.want) {
				t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
			}
			hfNoLeak(t, f, r)
			f.untouched()
		})
	}
}

func TestHFTokenStatus(t *testing.T) {
	applyLate(DefaultRegistry)
	f := newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "ark", "hf-token", "status")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "i No Hugging Face token is set. Models that need a login are skipped [LAI-SYN-034]. Add one with `sudo aictl ark hf-token set`.") {
		t.Fatalf("%+v", r)
	}
	p := filepath.Join(f.env.Paths.EtcDir, "hf-token")
	if err := os.WriteFile(p, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, DefaultRegistry, localOpts(nil), "--json", "ark", "hf-token", "status"); r.out != `{"set":false}`+"\n" {
		t.Errorf("white space only: %+v", r)
	}
	os.Remove(p)
	if err := os.Symlink("/nonexistent", p); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, DefaultRegistry, localOpts(nil), "ark", "hf-token", "status"); r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") {
		t.Errorf("a link: %+v", r)
	}
	o := localOpts(nil)
	o.euid = 1000
	if r := runCLI(t, DefaultRegistry, o, "ark", "hf-token", "status"); r.code != ExitDenied {
		t.Errorf("not root: %+v", r)
	}
}
