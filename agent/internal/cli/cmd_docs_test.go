package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/docsrender"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// docsFixture is a machine for `docs render`: a docs tree (with the survival card template), the example
// inventory next to it, and /etc/localai with a local inventory. Every value is invented.
type docsFixture struct {
	root, src, etc string
}

const fixtureExample = `schema_version: 1
secret_fields: [tailnet_ip, owner_email]
lab:
  owner_email: "<ADMIN_EMAIL>"
nodes:
  - id: ai-n3
    tailnet_ip: "<TAILNET_IP_AI_N3>"
    desktop_user: "<DESKTOP_USER>"
`

const fixtureLocal = `schema_version: 1
lab:
  owner_email: "person-a@example.test"
nodes:
  - id: ai-n3
    tailnet_ip: "192.0.2.23"
    desktop_user: "casey"
`

const fixtureCard = `# Card

| Computer <NODE_ID> | Computadora <NODE_ID> |
|---|---|
| Log in as <DESKTOP_USER>. Call <EMERGENCY_NUMBER>. | Entre como <DESKTOP_USER>. |

Printed <RENDER_DATE>
`

func newDocsFixture(t *testing.T, withInventory bool) *docsFixture {
	t.Helper()
	f := &docsFixture{root: t.TempDir()}
	f.src = filepath.Join(f.root, "docs")
	f.etc = filepath.Join(f.root, "etc")
	write := func(p, body string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(f.src, "runbooks", "00-survival-card.md"), fixtureCard)
	write(filepath.Join(f.src, "runbooks", "01-start.md"), "# Start\n\nSSH to <TAILNET_IP_AI_N3> as <DESKTOP_USER>. [Card](00-survival-card.md)\n")
	write(filepath.Join(f.src, "specs", "s.md"), "# Spec S\n")
	write(filepath.Join(f.root, "inventory.example.yaml"), fixtureExample)
	write(filepath.Join(f.etc, "node.yaml"), "node:\n  id: ai-n3\nportal:\n  emergency_number: \"112\"\n")
	if withInventory {
		write(filepath.Join(f.etc, "inventory.local.yaml"), fixtureLocal)
	}
	return f
}

type runnerFunc func(execx.Cmd) (execx.Result, error)

func (f runnerFunc) Run(_ context.Context, c execx.Cmd) (execx.Result, error) { return f(c) }

// pdfRunner is a fake Chromium that writes a PDF of n pages for every page (cardPages for the card).
func pdfRunner(pages, cardPages int) runnerFunc {
	return func(c execx.Cmd) (execx.Result, error) {
		var pdf, html string
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--print-to-pdf="); ok {
				pdf = v
			}
			if v, ok := strings.CutPrefix(a, "file://"); ok {
				html = v
			}
		}
		n := pages
		if strings.Contains(filepath.Base(html), "survival-card") {
			n = cardPages
		}
		body := "%PDF-1.4\n/Type /Pages\n" + strings.Repeat("/Type /Page\n", n)
		return execx.Result{}, os.WriteFile(pdf, []byte(body), 0o600)
	}
}

func (f *docsFixture) useEnv(t *testing.T, mod func(*docsEnvironment)) {
	t.Helper()
	saved := docsEnv
	t.Cleanup(func() { docsEnv = saved })
	docsEnv = docsEnvironment{
		Runner: pdfRunner(1, 1),
		Lookup: func(string) (string, error) { return "", execx.ErrNotFound },
		Now:    func() time.Time { return time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC) },
		Getwd:  func() (string, error) { return f.root, nil },
		// A host name no inventory entry has.
		Hostname:     func() (string, error) { return "test-host", nil },
		EtcDir:       f.etc,
		ContentRoot:  filepath.Join(f.root, "srv"),
		InstallState: filepath.Join(f.root, "none.json"),
		Drives:       func() []string { return nil },
		TmpDir:       t.TempDir(),
		Owner:        func() *docsrender.Owner { return nil },
	}
	if mod != nil {
		mod(&docsEnv)
	}
}

func (f *docsFixture) render(t *testing.T, args ...string) runResult {
	t.Helper()
	return runCLI(t, DefaultRegistry, runOpts{}, append([]string{"docs", "render"}, args...)...)
}

// flat joins the wrapped lines of the output into one.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDocsRenderHuman(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	out := filepath.Join(f.root, "out")
	r := f.render(t, "--src", f.src, "--out", out, "--no-pdf")
	if r.code != ExitOK || r.errOut != "" {
		t.Fatalf("exit %d\nstderr %q\nstdout %q", r.code, r.errOut, r.out)
	}
	if !strings.Contains(flat(r.out), "Rendered 1 runbooks and 1 specs to") {
		t.Errorf("summary: %q", r.out)
	}
	if !strings.Contains(flat(r.out), "(HTML only)") || !strings.Contains(flat(r.out), "survival-card-ai-n3.html") {
		t.Errorf("stdout: %q", r.out)
	}
	// The inventory came from <etc>/inventory.local.yaml; the card is for the node of node.yaml.
	if s := readFile(t, filepath.Join(out, "01-start.html")); !strings.Contains(s, "SSH to 192.0.2.23 as casey") {
		t.Errorf("page not filled: %s", s)
	}
	card := readFile(t, filepath.Join(out, "survival-card-ai-n3.html"))
	if !strings.Contains(card, "Computer ai-n3") || !strings.Contains(card, "Call 112.") || !strings.Contains(card, "Printed 2026-11-02") {
		t.Errorf("card: %s", card)
	}
	if strings.Contains(card, "192.0.2") {
		t.Error("the card shows an address")
	}
}

func TestDocsRenderJSON(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	out := filepath.Join(f.root, "out")
	r := runCLI(t, DefaultRegistry, runOpts{}, "--json", "docs", "render", "--src", f.src, "--out", out, "--no-pdf")
	if r.code != ExitOK || r.errOut != "" {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, r.out)
	}
	want := map[string]any{"outputs": []any{out}, "pages": float64(2), "pdf": float64(0), "inventory": true, "survival_card": "survival-card-ai-n3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json = %v\nwant   %v", got, want)
	}
}

func TestDocsRenderMissingInventoryWarns(t *testing.T) {
	f := newDocsFixture(t, false)
	f.useEnv(t, nil)
	out := filepath.Join(f.root, "out")
	r := f.render(t, "--src", f.src, "--out", out, "--no-pdf")
	if r.code != ExitOK || !strings.Contains(r.errOut, "LAI-CLI-013") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	if s := readFile(t, filepath.Join(out, "01-start.html")); !strings.Contains(s, "&lt;TAILNET_IP_AI_N3&gt;") {
		t.Errorf("placeholders must stay: %s", s)
	}
	// --json: the warning goes to stderr, stdout stays one document with inventory false.
	r = runCLI(t, DefaultRegistry, runOpts{}, "--json", "docs", "render", "--src", f.src, "--out", filepath.Join(f.root, "out2"), "--no-pdf")
	if r.code != ExitOK || !strings.Contains(r.out, `"inventory":false`) {
		t.Errorf("json: exit %d %q", r.code, r.out)
	}
}

func TestDocsRenderInventoryFlagAndLookupOrder(t *testing.T) {
	f := newDocsFixture(t, true)
	other := filepath.Join(f.root, "other.yaml")
	if err := os.WriteFile(other, []byte(strings.ReplaceAll(fixtureLocal, "casey", "robin")), 0o600); err != nil {
		t.Fatal(err)
	}
	// A mounted drive's inventory comes after /etc/localai.
	drive := filepath.Join(f.root, "media", "AI-ARK")
	if err := os.MkdirAll(drive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drive, "inventory.local.yaml"), []byte(strings.ReplaceAll(fixtureLocal, "casey", "drive-user")), 0o600); err != nil {
		t.Fatal(err)
	}
	f.useEnv(t, func(e *docsEnvironment) { e.Drives = func() []string { return []string{drive} } })

	check := func(label, out, want string) {
		t.Helper()
		if s := readFile(t, filepath.Join(out, "01-start.html")); !strings.Contains(s, "as "+want) {
			t.Errorf("%s: expected %s in %s", label, want, s)
		}
	}
	// 1. /etc/localai first.
	out1 := filepath.Join(f.root, "o1")
	if r := f.render(t, "--src", f.src, "--out", out1, "--no-pdf"); r.code != ExitOK {
		t.Fatal(r.errOut)
	}
	check("etc", out1, "casey")
	// 2. --inventory beats everything.
	out2 := filepath.Join(f.root, "o2")
	if r := f.render(t, "--src", f.src, "--out", out2, "--no-pdf", "--inventory", other); r.code != ExitOK {
		t.Fatal(r.errOut)
	}
	check("flag", out2, "robin")
	// 3. With /etc/localai/inventory.local.yaml gone, the drive is next.
	if err := os.Remove(filepath.Join(f.etc, "inventory.local.yaml")); err != nil {
		t.Fatal(err)
	}
	out3 := filepath.Join(f.root, "o3")
	if r := f.render(t, "--src", f.src, "--out", out3, "--no-pdf"); r.code != ExitOK {
		t.Fatal(r.errOut)
	}
	check("drive", out3, "drive-user")
	// 4. Then ./inventory.local.yaml.
	f.useEnv(t, nil) // no drive
	if err := os.WriteFile(filepath.Join(f.root, "inventory.local.yaml"), []byte(strings.ReplaceAll(fixtureLocal, "casey", "cwd-user")), 0o600); err != nil {
		t.Fatal(err)
	}
	out4 := filepath.Join(f.root, "o4")
	if r := f.render(t, "--src", f.src, "--out", out4, "--no-pdf"); r.code != ExitOK {
		t.Fatal(r.errOut)
	}
	check("cwd", out4, "cwd-user")
}

func TestDocsRenderSrcDefaultAndRequired(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	out := filepath.Join(f.root, "out")
	// In a checkout (./docs/runbooks exists) --src may be left out.
	if r := f.render(t, "--out", out, "--no-pdf"); r.code != ExitOK {
		t.Fatalf("default --src: exit %d %q", r.code, r.errOut)
	}
	// Elsewhere it is required: exit 2 with the usage code.
	empty := t.TempDir()
	f.useEnv(t, func(e *docsEnvironment) { e.Getwd = func() (string, error) { return empty, nil } })
	r := f.render(t, "--out", filepath.Join(f.root, "out2"), "--no-pdf")
	if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-005") || !strings.Contains(r.errOut, "--src") {
		t.Errorf("exit %d stderr %q", r.code, r.errOut)
	}
	if _, err := os.Stat(filepath.Join(f.root, "out2")); err == nil {
		t.Error("output written without a source")
	}
	if r := f.render(t, "--src", f.src, "--out", filepath.Join(f.root, "out3"), "extra"); r.code != ExitUsage {
		t.Errorf("a stray argument: exit %d", r.code)
	}
}

func TestDocsRenderRefusesGitCheckout(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	repo := filepath.Join(f.root, "checkout")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repo, "out")
	r := f.render(t, "--src", f.src, "--out", out, "--no-pdf")
	if r.code != ExitUsage || !strings.Contains(flat(r.errOut), "git checkout") {
		t.Errorf("exit %d stderr %q", r.code, r.errOut)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("something was written inside the checkout")
	}
	// A second, good folder does not rescue the run: nothing is written anywhere.
	good := filepath.Join(f.root, "good")
	r = f.render(t, "--src", f.src, "--out", good, "--out", out, "--no-pdf")
	if r.code != ExitUsage {
		t.Errorf("exit %d", r.code)
	}
	if _, err := os.Stat(good); err == nil {
		t.Error("the good folder was written although the run was refused")
	}
}

func TestDocsRenderNoChromiumIsInfoAndExitZero(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	out := filepath.Join(f.root, "out")
	r := f.render(t, "--src", f.src, "--out", out) // PDFs wanted, no browser installed
	if r.code != ExitOK || !strings.Contains(r.errOut, "LAI-CLI-014") || !strings.Contains(flat(r.out), "(HTML only)") {
		t.Errorf("exit %d\nstderr %q\nstdout %q", r.code, r.errOut, r.out)
	}
	if _, err := os.Stat(filepath.Join(out, "survival-card-ai-n3.html")); err != nil {
		t.Error("the HTML card is missing")
	}
	// --no-pdf is silent about it.
	if r := f.render(t, "--src", f.src, "--out", filepath.Join(f.root, "out2"), "--no-pdf"); strings.Contains(r.errOut, "LAI-CLI-014") {
		t.Errorf("--no-pdf still warned: %q", r.errOut)
	}
}

func TestDocsRenderWithChromium(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, func(e *docsEnvironment) {
		e.Lookup = func(name string) (string, error) {
			if name == "chromium" {
				return "/usr/bin/chromium", nil
			}
			return "", execx.ErrNotFound
		}
	})
	out := filepath.Join(f.root, "out")
	r := f.render(t, "--src", f.src, "--out", out)
	if r.code != ExitOK || r.errOut != "" || !strings.Contains(flat(r.out), "(PDF: 3 files)") {
		t.Fatalf("exit %d\nstderr %q\nstdout %q", r.code, r.errOut, r.out)
	}
	if _, err := os.Stat(filepath.Join(out, "survival-card-ai-n3.pdf")); err != nil {
		t.Error("the card PDF is missing")
	}
}

func TestDocsRenderCardTooLong(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, func(e *docsEnvironment) {
		e.Runner = pdfRunner(1, 2)
		e.Lookup = func(string) (string, error) { return "/usr/bin/chromium", nil }
	})
	out := filepath.Join(f.root, "out")
	r := f.render(t, "--src", f.src, "--out", out)
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-015") {
		t.Errorf("exit %d stderr %q", r.code, r.errOut)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output written although the card overflows")
	}
}

func TestDocsRenderCardSecretIsRefused(t *testing.T) {
	cases := []struct{ name, extra, ph string }{
		{"built-in list", "SSH <TAILNET_IP_AI_N3>", "TAILNET_IP_AI_N3"},
		{"a built-in e-mail field", "Mail <ADMIN_EMAIL>", "ADMIN_EMAIL"},
		{"secret_fields of the example", "Mail <OWNER_CONTACT>", "OWNER_CONTACT"},
	}
	for _, c := range cases {
		f := newDocsFixture(t, true)
		f.useEnv(t, nil)
		if c.ph == "OWNER_CONTACT" {
			// a field named in secret_fields, with a name that is on no built-in list
			ex := strings.Replace(fixtureExample, "secret_fields: [tailnet_ip, owner_email]", "secret_fields: [tailnet_ip, owner_email, contact]", 1) +
				"extra:\n  contact: \"<OWNER_CONTACT>\"\n"
			if err := os.WriteFile(filepath.Join(f.root, "inventory.example.yaml"), []byte(ex), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		card := filepath.Join(f.src, "runbooks", "00-survival-card.md")
		if err := os.WriteFile(card, []byte(fixtureCard+"\n"+c.extra+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(f.root, "out")
		r := f.render(t, "--src", f.src, "--out", out, "--no-pdf")
		if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-016") || !strings.Contains(r.errOut, "<"+c.ph+">") {
			t.Errorf("%s: exit %d stderr %q", c.name, r.code, r.errOut)
		}
		if strings.Contains(r.errOut+r.out, "192.0.2") || strings.Contains(r.errOut+r.out, "example.test") {
			t.Errorf("%s: a value leaked into the output", c.name)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: output written", c.name)
		}
	}
}

func TestDocsRenderFileErrors(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	// A missing source folder, and an output below a regular file.
	r := f.render(t, "--src", filepath.Join(f.root, "nowhere"), "--out", filepath.Join(f.root, "out"), "--no-pdf")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") || !strings.Contains(r.errOut, "nowhere") {
		t.Errorf("missing source: exit %d stderr %q", r.code, r.errOut)
	}
	blocker := filepath.Join(f.root, "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = f.render(t, "--src", f.src, "--out", filepath.Join(blocker, "docs"), "--no-pdf")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") {
		t.Errorf("unwritable output: exit %d stderr %q", r.code, r.errOut)
	}
	// A malformed inventory.
	if err := os.WriteFile(filepath.Join(f.etc, "inventory.local.yaml"), []byte("nodes: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = f.render(t, "--src", f.src, "--out", filepath.Join(f.root, "out2"), "--no-pdf")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") || !strings.Contains(r.errOut, "inventory.local.yaml") {
		t.Errorf("bad inventory: exit %d stderr %q", r.code, r.errOut)
	}
}

func TestDocsRenderDefaultOutputsIncludeTheDrive(t *testing.T) {
	f := newDocsFixture(t, true)
	drive := filepath.Join(f.root, "media", "AI-ARK")
	f.useEnv(t, func(e *docsEnvironment) { e.Drives = func() []string { return []string{drive} } })
	r := runCLI(t, DefaultRegistry, runOpts{}, "--json", "docs", "render", "--src", f.src, "--no-pdf")
	if r.code != ExitOK {
		t.Fatalf("exit %d %q", r.code, r.errOut)
	}
	want := fmt.Sprintf(`"outputs":[%q,%q]`, filepath.Join(f.root, "srv", "docs"), filepath.Join(drive, "localai", "docs"))
	if !strings.Contains(r.out, want) {
		t.Errorf("stdout %s\nwant %s", r.out, want)
	}
	for _, p := range []string{filepath.Join(f.root, "srv", "docs", "index.json"), filepath.Join(drive, "localai", "docs", "index.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestDocsRenderSpanishSummary(t *testing.T) {
	f := newDocsFixture(t, true)
	f.useEnv(t, nil)
	r := runCLI(t, DefaultRegistry, runOpts{}, "--lang", "es", "docs", "render", "--src", f.src, "--out", filepath.Join(f.root, "out"), "--no-pdf")
	if r.code != ExitOK || !strings.Contains(r.out, "Se generaron") || !strings.Contains(flat(r.out), "solo HTML") {
		t.Errorf("exit %d stdout %q", r.code, r.out)
	}
}

func TestArkDrivesIn(t *testing.T) {
	mounts := strings.Join([]string{
		"/dev/nvme0n1p2 / ext4 rw,relatime 0 0",
		"/dev/sda1 /media/user/AI-ARK ext4 rw,nosuid 0 0",
		"/dev/sdb1 /media/user/OTHER vfat rw 0 0",
		"/dev/sdc1 /run/media/some\\040one/AI-ARK ext4 rw 0 0",
		"",
		"short",
	}, "\n")
	got := arkDrivesIn(mounts)
	want := []string{"/media/user/AI-ARK", "/run/media/some one/AI-ARK"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("arkDrivesIn = %v, want %v", got, want)
	}
	if len(arkDrivesIn("")) != 0 {
		t.Error("empty /proc/mounts")
	}
}

func TestDocsHelpListsRender(t *testing.T) {
	r := runCLI(t, DefaultRegistry, runOpts{}, "help", "docs")
	if r.code != ExitOK || !strings.Contains(r.out, "render") {
		t.Errorf("exit %d stdout %q", r.code, r.out)
	}
	r = runCLI(t, DefaultRegistry, runOpts{}, "help", "docs", "render")
	for _, want := range []string{"--src", "--inventory", "--out", "--no-pdf", "[local]"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("help lacks %s: %s", want, r.out)
		}
	}
}
