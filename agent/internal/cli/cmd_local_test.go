package cli

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// localFixture is a node in a temp directory: /etc/localai, /var/lib/localai, /srv/localai and the
// systemd folder, with the user running the tests standing in for root and for the groups.
type localFixture struct {
	t   *testing.T
	env local.Env
	run *execfake.Runner
}

func newLocalFixture(t *testing.T) *localFixture {
	t.Helper()
	root := t.TempDir()
	p := local.Paths{
		EtcDir:       filepath.Join(root, "etc", "localai"),
		StateDir:     filepath.Join(root, "var", "lib", "localai"),
		InternalRoot: filepath.Join(root, "srv", "localai"),
		SystemdDir:   filepath.Join(root, "etc", "systemd", "system"),
		Mountinfo:    filepath.Join(root, "mountinfo"),
	}
	for _, d := range []string{p.EtcDir, p.StateDir, p.SystemdDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := execfake.New()
	gid := os.Getgid()
	f := &localFixture{t: t, run: run, env: local.Env{
		Paths: p, RootUID: os.Getuid(), RootGID: gid,
		LookupGroup: func(string) (int, error) { return gid, nil },
		LookupUser:  func(string) (int, error) { return os.Getuid(), nil },
		GroupName:   func(int) string { return "testgroup" },
		UserName:    func(int) string { return "testowner" },
		Now:         func() time.Time { return time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC) },
		Runner:      run,
		LookPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}}
	old := localEnv
	localEnv = func() local.Env { return f.env }
	t.Cleanup(func() { localEnv = old })
	return f
}

func (f *localFixture) auditLines() []map[string]any {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.env.Paths.StateDir, "audit.jsonl"))
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			f.t.Fatalf("audit line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

// untouched asserts that nothing was created under the etc and state folders.
func (f *localFixture) untouched() {
	f.t.Helper()
	for _, dir := range []string{f.env.Paths.EtcDir, f.env.Paths.StateDir} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			f.t.Errorf("%s was touched: %v", dir, entries)
		}
	}
}

var localShaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// writeLocalCatalog writes a valid catalog (the testdata of package catalog) with version into dir.
func writeLocalCatalog(t *testing.T, dir, version string) string {
	t.Helper()
	src := filepath.Join("..", "catalog", "testdata", "valid")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := localShaToken.ReplaceAllStringFunc(string(b), func(m string) string {
			return strings.Repeat(localShaToken.FindStringSubmatch(m)[1], 64)
		})
		return os.WriteFile(dst, []byte(s), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CATALOG_VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// unwrap joins lines the output layer wrapped at 80 columns (aictl.md section 3.3), so a test can
// compare a message with the one-line form of the spec.
func unwrap(s string) string { return regexp.MustCompile(`\n +`).ReplaceAllString(s, " ") }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileMode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// local opts: root by default, English, no agent configured (a socket that does not exist).
func localOpts(term Terminal) runOpts {
	return runOpts{
		env:  map[string]string{"LANG": "en_US.UTF-8", "LOCALAI_SOCKET": "/nonexistent/agent.sock", "LOCALAI_URL": "http://127.0.0.1:1"},
		term: term,
	}
}

// ---- pin set ----

func TestPinSetInteractive(t *testing.T) {
	f := newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "4821", "4821")), "pin", "set")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if unwrap(r.out) != "✓ The admin PIN was changed. Keep it somewhere safe (for example, on paper with the ark drive).\n" {
		t.Errorf("stdout %q", r.out)
	}
	if !strings.Contains(r.errOut, "New admin PIN (4 to 8 digits): ") || !strings.Contains(r.errOut, "Repeat: ") {
		t.Errorf("prompts missing on stderr: %q", r.errOut)
	}
	if strings.Contains(r.out+r.errOut, "4821") {
		t.Error("the PIN was echoed")
	}
	pinFile := filepath.Join(f.env.Paths.EtcDir, "admin-pin")
	if fileMode(t, pinFile) != 0o600 {
		t.Errorf("mode %o", fileMode(t, pinFile))
	}
	ok, err := auth.VerifyPIN(readFile(t, pinFile), "4821")
	if err != nil || !ok {
		t.Errorf("the agent's verifier refuses the new PIN: %v %v", ok, err)
	}
	if !strings.HasPrefix(readFile(t, pinFile), "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Error("not the PHC format with the exact parameters")
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "pin.set" || lines[0]["result"] != "ok" {
		t.Fatalf("audit %v", lines)
	}
	if a := lines[0]["actor"].(map[string]any); a["kind"] != "cli-root" || a["uid"] != float64(0) {
		t.Errorf("actor %v", a)
	}
	if strings.Contains(readFile(t, filepath.Join(f.env.Paths.StateDir, "audit.jsonl")), "4821") {
		t.Error("the PIN is in the audit log")
	}
}

func TestPinSetStdin(t *testing.T) {
	for _, args := range [][]string{{"--pin-stdin", "pin", "set"}, {"pin", "set", "--pin-stdin"}} {
		f := newLocalFixture(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false, "4821")), args...)
		if r.code != 0 || r.errOut != "" {
			t.Fatalf("%v: exit %d stderr %q", args, r.code, r.errOut)
		}
		if ok, _ := auth.VerifyPIN(readFile(t, filepath.Join(f.env.Paths.EtcDir, "admin-pin")), "4821"); !ok {
			t.Errorf("%v: PIN not set", args)
		}
	}
}

func TestPinSetStdinJSON(t *testing.T) {
	newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false, "4821")), "--json", "pin", "set", "--pin-stdin")
	if r.code != 0 || r.out != `{"changed":true}`+"\n" || r.errOut != "" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

func TestPinSetRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		term Terminal
		args []string
		code int
		want string
	}{
		{"too short", fake.Typed(true, "123"), nil, ExitUsage, "4 to 8 digits"},
		{"too long", fake.Typed(true, "123456789"), nil, ExitUsage, "4 to 8 digits"},
		{"letters", fake.Typed(true, "12ab"), nil, ExitUsage, "4 to 8 digits"},
		{"the entries differ", fake.Typed(true, "4821", "4822"), nil, ExitUsage, "not the same"},
		{"nothing typed", fake.Typed(true, ""), nil, ExitDenied, "No PIN was entered"},
		{"stdin: too short", fake.Typed(false, "12"), []string{"--pin-stdin"}, ExitUsage, "4 to 8 digits"},
		{"stdin: empty", fake.Typed(false), []string{"--pin-stdin"}, ExitDenied, "No PIN was entered"},
		{"no terminal and no --pin-stdin", fake.Typed(false, "4821", "4821"), nil, ExitDenied, "LAI-CLI-008"},
		{"--json without --pin-stdin", fake.Typed(true, "4821", "4821"), []string{"--json"}, ExitDenied, "LAI-CLI-008"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLocalFixture(t)
			args := append(append([]string{}, c.args...), "pin", "set")
			if len(c.args) > 0 && c.args[0] == "--pin-stdin" {
				args = []string{"pin", "set", "--pin-stdin"}
			}
			r := runCLI(t, DefaultRegistry, localOpts(c.term), args...)
			if r.code != c.code || !strings.Contains(r.out+r.errOut, c.want) {
				t.Fatalf("exit %d (want %d), want %q in:\n%s%s", r.code, c.code, c.want, r.out, r.errOut)
			}
			f.untouched()
		})
	}
}

func TestPinSetPromptIsNotShownOnPipe(t *testing.T) {
	newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false, "4821")), "pin", "set", "--pin-stdin")
	if strings.Contains(r.errOut, "New admin PIN") {
		t.Errorf("a prompt was written for a pipe: %q", r.errOut)
	}
}

func TestPinSetNeedsRootBeforeTouchingAnything(t *testing.T) {
	f := newLocalFixture(t)
	o := localOpts(fake.Typed(true, "4821", "4821"))
	o.euid = 1000
	r := runCLI(t, DefaultRegistry, o, "pin", "set")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") || !strings.Contains(r.errOut, "sudo aictl") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	f.untouched()
	if o.term.(*fake.Terminal).Reads() != 0 {
		t.Error("it asked for a PIN before checking root")
	}
}

func TestPinSetWriteFailureIsLAICLI012AndAudited(t *testing.T) {
	f := newLocalFixture(t)
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "admin-pin"), []byte("old-hash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env.FS = cfake.New(config.OSFS{}).FailOn("rename", 1) // the write is interrupted before the rename
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "4821", "4821")), "pin", "set")
	got := unwrap(r.errOut)
	if r.code != ExitFailed || !strings.Contains(got, "LAI-CLI-012") || !strings.Contains(got, "Could not write") || !strings.Contains(got, "admin-pin") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if strings.Contains(r.out, "✓") {
		t.Error("success line for a failure")
	}
	if readFile(t, filepath.Join(f.env.Paths.EtcDir, "admin-pin")) != "old-hash\n" {
		t.Error("the old PIN file changed")
	}
	entries, _ := os.ReadDir(f.env.Paths.EtcDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "pin.set" || lines[0]["result"] != "fail" || lines[0]["code"] != "LAI-CLI-012" {
		t.Errorf("audit %v", lines)
	}
}

func TestPinSetMissingConfigFolderNamesTheFolder(t *testing.T) {
	f := newLocalFixture(t)
	f.env.Paths.EtcDir = filepath.Join(f.env.Paths.EtcDir, "missing", "deeper")
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "4821", "4821")), "pin", "set")
	got := unwrap(r.errOut)
	if r.code != ExitFailed || !strings.Contains(got, "LAI-CLI-012") || !strings.Contains(got, "deeper (it does not exist)") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if strings.Contains(got, "no such file") || strings.Contains(got, "open ") {
		t.Errorf("a Go error chain leaked: %s", got)
	}
}

func TestLocalCommandsRefuseWhileAnotherRuns(t *testing.T) {
	f := newLocalFixture(t)
	release, err := local.Lock(f.env)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, c := range []struct {
		args []string
		term Terminal
	}{
		{[]string{"pin", "set"}, fake.Typed(true, "4821", "4821")},
		{[]string{"token", "rotate"}, nil},
		{[]string{"catalog", "restore"}, nil},
	} {
		if c.args[0] == "catalog" {
			writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.25-1")
		}
		r := runCLI(t, DefaultRegistry, localOpts(c.term), c.args...)
		if r.code != ExitRefused || !strings.Contains(r.errOut, "LAI-CLI-012") || !strings.Contains(r.errOut, "Another aictl command") {
			t.Errorf("%v: exit %d\n%s", c.args, r.code, r.errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(f.env.Paths.EtcDir, "admin-pin")); err == nil {
		t.Error("a PIN was written while locked")
	}
}

func TestPinSetSpanish(t *testing.T) {
	newLocalFixture(t)
	o := localOpts(fake.Typed(true, "4821", "4821"))
	o.env["LANG"] = "es_CR.UTF-8"
	r := runCLI(t, DefaultRegistry, o, "pin", "set")
	if r.code != 0 || !strings.Contains(r.out, "El PIN de administrador se cambió") || !strings.Contains(r.errOut, "Nuevo PIN de administrador") {
		t.Errorf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
}

// ---- token rotate ----

func TestTokenRotate(t *testing.T) {
	f := newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "token", "rotate")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if unwrap(r.out) != "✓ A new agent token was created. ai-desktop picks it up by itself. Scripts that copied the old token must read the file again.\n" {
		t.Errorf("stdout %q", r.out)
	}
	tok := readFile(t, filepath.Join(f.env.Paths.EtcDir, "agent-token"))
	if !regexp.MustCompile(`^` + auth.TokenPrefix + `[A-Za-z0-9_-]{43}\n$`).MatchString(tok) {
		t.Errorf("format wrong (length %d)", len(tok))
	}
	if strings.Contains(r.out+r.errOut, strings.TrimSpace(tok)) {
		t.Error("the token was printed")
	}
	if fileMode(t, filepath.Join(f.env.Paths.EtcDir, "agent-token")) != 0o640 {
		t.Error("mode is not 0640")
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "token.rotate" || lines[0]["result"] != "ok" || lines[0]["actor"].(map[string]any)["kind"] != "cli-root" {
		t.Errorf("audit %v", lines)
	}
	if strings.Contains(readFile(t, filepath.Join(f.env.Paths.StateDir, "audit.jsonl")), strings.TrimSpace(tok)) {
		t.Error("the token is in the audit log")
	}
	// JSON
	r = runCLI(t, DefaultRegistry, localOpts(nil), "--json", "token", "rotate")
	if want := `{"path":"` + filepath.Join(f.env.Paths.EtcDir, "agent-token") + `","rotated":true}` + "\n"; r.code != 0 || r.errOut != "" || r.out != want {
		t.Errorf("json: exit %d %q %q", r.code, r.out, r.errOut)
	}
}

func TestTokenRotateNeedsRootAndTheGroup(t *testing.T) {
	f := newLocalFixture(t)
	o := localOpts(nil)
	o.euid = 1000
	if r := runCLI(t, DefaultRegistry, o, "token", "rotate"); r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") {
		t.Errorf("not root: exit %d\n%s", r.code, r.errOut)
	}
	f.untouched()

	f.env.LookupGroup = func(string) (int, error) { return -1, os.ErrNotExist }
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "agent-token"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, DefaultRegistry, localOpts(nil), "token", "rotate")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") || !strings.Contains(r.errOut, `"localai-operators" does not exist`) {
		t.Errorf("missing group: exit %d\n%s", r.code, r.errOut)
	}
	if readFile(t, filepath.Join(f.env.Paths.EtcDir, "agent-token")) != "old\n" {
		t.Error("the token changed although the group is missing")
	}
}

// ---- catalog restore ----

func TestCatalogRestoreFromTheInternalKit(t *testing.T) {
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.EtcDir, "catalog"), "2026.10.01-1")
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	r := runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "restore")
	if r.code != 0 || r.out != "✓ Restored catalog 2026.10.20-1 from the ark kit on the internal disk.\n" || r.errOut != "" {
		t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
	}
	if v := strings.TrimSpace(readFile(t, filepath.Join(f.env.Paths.EtcDir, "catalog", "CATALOG_VERSION"))); v != "2026.10.20-1" {
		t.Errorf("live version %q", v)
	}
	if v := strings.TrimSpace(readFile(t, filepath.Join(f.env.Paths.EtcDir, "catalog.prev", "CATALOG_VERSION"))); v != "2026.10.01-1" {
		t.Errorf("catalog.prev %q", v)
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "catalog.restore" || lines[0]["result"] != "ok" ||
		lines[0]["target"].(map[string]any)["version"] != "2026.10.20-1" {
		t.Errorf("audit %v", lines)
	}
}

func TestCatalogRestoreJSON(t *testing.T) {
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	r := runCLI(t, DefaultRegistry, localOpts(nil), "--json", "catalog", "restore")
	if r.code != 0 || r.out != `{"from":"kit","previous":"","version":"2026.10.20-1"}`+"\n" || r.errOut != "" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

func TestCatalogRestoreNoKitIsExit5(t *testing.T) {
	f := newLocalFixture(t)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "restore")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "LAI-KIT-040") || !strings.Contains(r.errOut, "aictl ark") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if got := f.auditLines(); len(got) != 1 || got[0]["result"] != "fail" {
		t.Errorf("audit %v", got)
	}
}

func TestCatalogRestoreInvalidKitIsLAICAT002AndKeepsTheCatalog(t *testing.T) {
	f := newLocalFixture(t)
	live := writeLocalCatalog(t, filepath.Join(f.env.Paths.EtcDir, "catalog"), "2026.10.01-1")
	before := readFile(t, filepath.Join(live, "models.yaml"))
	kit := writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	if err := os.WriteFile(filepath.Join(kit, "CATALOG_VERSION"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "restore")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CAT-002") || !strings.Contains(r.errOut, "LAI-CAT-019") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if readFile(t, filepath.Join(live, "models.yaml")) != before || strings.TrimSpace(readFile(t, filepath.Join(live, "CATALOG_VERSION"))) != "2026.10.01-1" {
		t.Error("the catalog in use changed")
	}
	if _, err := os.Stat(live + ".new"); err == nil {
		t.Error("catalog.new left behind")
	}
	if got := f.auditLines(); len(got) != 1 || got[0]["code"] != "LAI-CAT-002" {
		t.Errorf("audit %v", got)
	}
}

func TestCatalogRestoreNeedsRoot(t *testing.T) {
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	o := localOpts(nil)
	o.euid = 1000
	r := runCLI(t, DefaultRegistry, o, "catalog", "restore")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if _, err := os.Stat(filepath.Join(f.env.Paths.EtcDir, "catalog")); err == nil {
		t.Error("a catalog was written by a non-root user")
	}
}

// catalogAgent answers GET /catalog like an agent that picks the new catalog up after a few polls.
func catalogAgent(t *testing.T, version, source string, notYet int) (runOpts, *fake.Agent) {
	t.Helper()
	h := newHarness(t, fake.Options{})
	calls := 0
	h.agent.Handle(http.MethodGet, "/catalog", func(w http.ResponseWriter, r *http.Request) {
		calls++
		v, s := "2026.01.01-1", "offline"
		if calls > notYet {
			v, s = version, source
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + v + `","source":"` + s + `","models":[],"content":[]}`))
	})
	o := runOpts{
		env:  map[string]string{"LANG": "en_US.UTF-8", "LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL},
		h:    h,
		term: fake.Typed(true, "y"),
	}
	return o, h.agent
}

func fastWait(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldT, oldE := catalogWaitTimeout, catalogWaitEvery
	catalogWaitTimeout, catalogWaitEvery = timeout, time.Millisecond
	t.Cleanup(func() { catalogWaitTimeout, catalogWaitEvery = oldT, oldE })
}

func TestCatalogRestoreWaitsForTheRunningAgent(t *testing.T) {
	fastWait(t, 5*time.Second)
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	o, agent := catalogAgent(t, "2026.10.20-1", "offline", 3)
	r := runCLI(t, DefaultRegistry, o, "catalog", "restore")
	if r.code != 0 || !strings.HasPrefix(r.out, "✓ Restored catalog 2026.10.20-1") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if n := agent.Count("/catalog"); n < 4 {
		t.Errorf("polled GET /catalog %d times, want at least 4 (3 stale answers, then the new version)", n)
	}
}

func TestCatalogRestoreDoesNotWaitWhenTheAgentIsDown(t *testing.T) {
	fastWait(t, 5*time.Second)
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	h := newHarness(t, fake.Options{NoSocket: true, NoTCP: true})
	o := runOpts{env: map[string]string{"LANG": "en_US.UTF-8", "LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL}, h: h}
	start := time.Now()
	r := runCLI(t, DefaultRegistry, o, "catalog", "restore")
	if r.code != 0 || r.errOut != "" || time.Since(start) > 3*time.Second {
		t.Fatalf("exit %d after %v\n%s%s", r.code, time.Since(start), r.out, r.errOut)
	}
}

func TestCatalogRestoreWarnsWhenTheAgentNeverReportsIt(t *testing.T) {
	fastWait(t, 150*time.Millisecond)
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.20-1")
	o, _ := catalogAgent(t, "2026.10.20-1", "offline", 1<<30)
	r := runCLI(t, DefaultRegistry, o, "catalog", "restore")
	if r.code != 0 || !strings.Contains(r.errOut, "has not reported it yet") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
}

// ---- catalog use --test ----

func TestCatalogUseTestThenRestore(t *testing.T) {
	fastWait(t, 5*time.Second)
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.EtcDir, "catalog"), "2026.10.01-1")
	draft := writeLocalCatalog(t, t.TempDir(), "2026.11.03-1")
	o, agent := catalogAgent(t, "2026.11.03-1", "test", 2)
	o.term = fake.Typed(true, "y")
	r := runCLI(t, DefaultRegistry, o, "catalog", "use", draft, "--test")
	want := "✓ Test catalog 2026.11.03-1 is in use (from " + draft + "). Undo with `sudo aictl catalog restore`.\n"
	if r.code != 0 || unwrap(r.out) != want {
		t.Fatalf("exit %d\n%q\nwant %q\n%s", r.code, r.out, want, r.errOut)
	}
	if !strings.Contains(r.errOut, "[y/N]") {
		t.Errorf("no confirmation was asked: %q", r.errOut)
	}
	live := filepath.Join(f.env.Paths.EtcDir, "catalog")
	if _, err := os.Stat(filepath.Join(live, "TEST")); err != nil {
		t.Error("no TEST marker")
	}
	if v := strings.TrimSpace(readFile(t, filepath.Join(f.env.Paths.EtcDir, "catalog.prev", "CATALOG_VERSION"))); v != "2026.10.01-1" {
		t.Errorf("catalog.prev %q", v)
	}
	if agent.Count("/catalog") < 3 {
		t.Errorf("did not wait for source test: %d polls", agent.Count("/catalog"))
	}
	if got := f.auditLines(); len(got) != 1 || got[0]["action"] != "catalog.use_test" || got[0]["result"] != "ok" {
		t.Errorf("audit %v", got)
	}

	// restore puts catalog.prev back, without asking and without the kit
	writeLocalCatalog(t, filepath.Join(f.env.Paths.InternalRoot, "kit", "catalog"), "2026.10.25-1")
	r = runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "restore")
	if r.code != 0 || !strings.HasPrefix(unwrap(r.out), "✓ Restored catalog 2026.10.01-1, the one that was in use before the test catalog.") {
		t.Fatalf("restore: exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if _, err := os.Stat(filepath.Join(live, "TEST")); err == nil {
		t.Error("TEST marker survived")
	}
}

func TestCatalogUseTestJSON(t *testing.T) {
	f := newLocalFixture(t)
	writeLocalCatalog(t, filepath.Join(f.env.Paths.EtcDir, "catalog"), "2026.10.01-1")
	draft := writeLocalCatalog(t, t.TempDir(), "2026.11.03-1")
	r := runCLI(t, DefaultRegistry, localOpts(nil), "--json", "--yes", "catalog", "use", "--test", draft)
	if r.code != 0 || r.out != `{"previous":"2026.10.01-1","source":"test","version":"2026.11.03-1"}`+"\n" || r.errOut != "" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

func TestCatalogUseTestYesAfterTheCommand(t *testing.T) {
	for _, flagArgs := range [][]string{{"--yes"}, {"-y"}} {
		f := newLocalFixture(t)
		draft := writeLocalCatalog(t, t.TempDir(), "2026.11.03-1")
		args := append([]string{"catalog", "use", draft, "--test"}, flagArgs...)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), args...)
		if r.code != 0 || !strings.HasPrefix(r.out, "✓ Test catalog 2026.11.03-1") {
			t.Fatalf("%v: exit %d\n%s%s", flagArgs, r.code, r.out, r.errOut)
		}
		if _, err := os.Stat(filepath.Join(f.env.Paths.EtcDir, "catalog", "TEST")); err != nil {
			t.Errorf("%v: not installed", flagArgs)
		}
	}
}

func TestCatalogUseTestRefusals(t *testing.T) {
	broken := func(t *testing.T) string {
		d := writeLocalCatalog(t, t.TempDir(), "2026.11.03-1")
		if err := os.WriteFile(filepath.Join(d, "CATALOG_VERSION"), []byte("nonsense\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := []struct {
		name  string
		args  func(t *testing.T) []string
		term  Terminal
		euid  int
		code  int
		check func(r runResult) bool
	}{
		{"--test is required", func(t *testing.T) []string {
			return []string{"catalog", "use", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"), "--yes"}
		}, nil, 0, ExitUsage, func(r runResult) bool { return strings.Contains(r.errOut, "needs --test") }},
		{"no directory", func(*testing.T) []string { return []string{"catalog", "use", "--test"} }, nil, 0, ExitUsage,
			func(r runResult) bool { return strings.Contains(r.errOut, "directory is required") }},
		{"two directories", func(*testing.T) []string { return []string{"catalog", "use", "a", "b", "--test"} }, nil, 0, ExitUsage,
			func(r runResult) bool { return strings.Contains(r.errOut, "one directory only") }},
		{"not root", func(t *testing.T) []string {
			return []string{"catalog", "use", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"), "--test", "--yes"}
		}, nil, 1000, ExitDenied, func(r runResult) bool { return strings.Contains(r.errOut, "LAI-CLI-006") }},
		{"failing lint", func(t *testing.T) []string { return []string{"catalog", "use", broken(t), "--test", "--yes"} }, nil, 0, ExitUsage,
			func(r runResult) bool {
				return regexp.MustCompile(`(?m)^\S+CATALOG_VERSION:  LAI-CAT-019  `).MatchString(r.out) && strings.Contains(r.out, "✗ 1 problem found.")
			}},
		{"no terminal without --yes", func(t *testing.T) []string {
			return []string{"catalog", "use", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"), "--test"}
		}, fake.Typed(false), 0, ExitUsage, func(r runResult) bool { return strings.Contains(r.errOut, "LAI-CLI-007") }},
		{"declined", func(t *testing.T) []string {
			return []string{"catalog", "use", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"), "--test"}
		}, fake.Typed(true, ""), 0, ExitOK, func(r runResult) bool { return strings.Contains(r.out, "Nothing was changed") }},
		{"declined with n", func(t *testing.T) []string {
			return []string{"catalog", "use", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"), "--test"}
		}, fake.Typed(true, "n"), 0, ExitOK, func(r runResult) bool { return strings.Contains(r.out, "Nothing was changed") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLocalFixture(t)
			live := writeLocalCatalog(t, filepath.Join(f.env.Paths.EtcDir, "catalog"), "2026.10.01-1")
			before := readFile(t, filepath.Join(live, "models.yaml"))
			o := localOpts(c.term)
			o.euid = c.euid
			r := runCLI(t, DefaultRegistry, o, c.args(t)...)
			if r.code != c.code || !c.check(r) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", r.code, c.code, r.out, r.errOut)
			}
			if readFile(t, filepath.Join(live, "models.yaml")) != before || strings.TrimSpace(readFile(t, filepath.Join(live, "CATALOG_VERSION"))) != "2026.10.01-1" {
				t.Error("the catalog in use changed")
			}
			for _, n := range []string{"catalog.prev", "catalog.new"} {
				if _, err := os.Stat(filepath.Join(f.env.Paths.EtcDir, n)); err == nil {
					t.Errorf("%s was created", n)
				}
			}
			if len(f.auditLines()) != 0 {
				t.Error("an audit line was written for a command that changed nothing")
			}
		})
	}
}

// ---- catalog lint conformance (aictl.md 4.11.1, catalog.md 6; the cloud depends on it) ----

func TestCatalogLintOutputConformsToTheSpec(t *testing.T) {
	root := writeCatalog(t)
	breakRevision(t, root)
	if err := os.WriteFile(filepath.Join(root, "catalog", "CATALOG_VERSION"), []byte("nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	// human: <file>:<yaml-path>  LAI-CAT-0NN  <message>, then the summary
	code, out, errOut := run("catalog", "lint", "catalog/")
	if code != ExitUsage || errOut != "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	line := regexp.MustCompile(`^[^\s:]+:[^\s]*  LAI-CAT-0\d\d  \S.*$`)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout %q", out)
	}
	for _, l := range lines[:len(lines)-1] {
		if !line.MatchString(l) {
			t.Errorf("problem line does not match <file>:<yaml-path>  LAI-CAT-0NN  <message>: %q", l)
		}
	}
	if !regexp.MustCompile(`^✗ \d+ problems? found\.$`).MatchString(lines[len(lines)-1]) {
		t.Errorf("summary line %q", lines[len(lines)-1])
	}

	// --json: exactly {"valid": bool, "problems": [{"code","file","path","message"}]}
	code, out, errOut = run("catalog", "lint", "--json", "catalog/")
	if code != ExitUsage || errOut != "" {
		t.Fatalf("json exit %d stderr %q", code, errOut)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil || len(top) != 2 {
		t.Fatalf("top level %q: %v", out, err)
	}
	var valid bool
	var problems []map[string]string
	if json.Unmarshal(top["valid"], &valid) != nil || valid || json.Unmarshal(top["problems"], &problems) != nil || len(problems) < 2 {
		t.Fatalf("shape wrong: %q", out)
	}
	for _, p := range problems {
		if len(p) != 4 || p["code"] == "" || p["file"] == "" || p["message"] == "" || strings.HasPrefix(p["file"], "catalog") {
			t.Errorf("problem keys %v (file must be relative to the catalog directory)", p)
		}
		if _, ok := p["path"]; !ok {
			t.Errorf("problem without a path key: %v", p)
		}
	}
}
