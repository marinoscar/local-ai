package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of the read-only commands (AICTL-04) against the fake agent: golden output in English and
// Spanish from fixture bodies (testdata/bodies), --json equal to the agent body, exit codes, and the
// failure paths (agent down, 401/403, 503 retry, partial data, huge output, closed pipe).
//
// Regenerate the golden files with: go test ./internal/cli -run Golden -update-readonly

var updateReadonly = flag.Bool("update-readonly", false, "rewrite testdata/golden for the read-only commands")

var agoPattern = regexp.MustCompile(`\{\{ago:([0-9a-z]+)\}\}`)

// roNow is the clock the fixture times come from; a test of the helper replaces it.
var roNow = time.Now

var (
	anchorsMu sync.Mutex
	anchors   = map[string]time.Time{}
)

// roAnchor is the one "now" of a test: every fixture time of that test (the body the fake serves and
// the body the test expects) is computed from it, so a second boundary between the two evaluations
// cannot make them differ. It is forgotten when the test ends, so each run starts from a fresh clock.
func roAnchor(t testing.TB) time.Time {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	if a, ok := anchors[t.Name()]; ok {
		return a
	}
	a := roNow()
	anchors[t.Name()] = a
	t.Cleanup(func() {
		anchorsMu.Lock()
		delete(anchors, t.Name())
		anchorsMu.Unlock()
	})
	return a
}

// roBody loads testdata/bodies/<name>.json. {{ago:17h}} becomes a time that long (plus a margin
// that keeps the rounded-down relative text stable) before now, and {{masked}} the masked
// credential the agent sends. All the times of one test come from one clock reading (roAnchor).
func roBody(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "bodies", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s := agoPattern.ReplaceAllStringFunc(string(b), func(m string) string {
		d, err := time.ParseDuration(agoPattern.FindStringSubmatch(m)[1])
		if err != nil {
			t.Fatalf("%s: bad duration in %s", name, m)
		}
		return roAnchor(t).Add(-d - 20*time.Second).UTC().Format(time.RFC3339)
	})
	s = strings.ReplaceAll(s, "{{sha256}}", strings.Repeat("ab", 32))
	return strings.ReplaceAll(s, "{{masked}}", "nod_"+"…a1b2")
}

// roFix is a fake agent plus the plumbing to run the real command tree against it.
type roFix struct {
	t  *testing.T
	h  *harness
	ag *fake.Agent
}

func newROFix(t *testing.T, o fake.Options) *roFix {
	t.Helper()
	h := newHarness(t, o)
	return &roFix{t: t, h: h, ag: h.agent}
}

// serve answers GET path (no query) with a fixture.
func (f *roFix) serve(path, fixture string) {
	f.ag.Respond(http.MethodGet, path, 200, roBody(f.t, fixture))
}

// exec runs aictl in-process against the fake with a context the test may cancel.
func (f *roFix) exec(ctx context.Context, o runOpts, args ...string) runResult {
	f.t.Helper()
	env := map[string]string{
		"LANG":               "en_US.UTF-8",
		"LOCALAI_SOCKET":     f.ag.SocketPath,
		"LOCALAI_URL":        f.ag.URL,
		"LOCALAI_TOKEN_FILE": f.h.cfg.TokenFile,
	}
	for k, v := range o.env {
		env[k] = v
	}
	var out, errb bytes.Buffer
	var stdout = &out
	e := Env{
		Stdin: strings.NewReader(o.stdin), Stdout: stdout, Stderr: &errb,
		Getenv:  func(k string) string { return env[k] },
		Geteuid: func() int { return o.euid }, StdoutIsTTY: o.tty, Version: "0.0.0-dev",
		ClientConfig: func(c *Config) {
			c.Sleep = f.h.sleeps.Sleep
			c.Stderr = &errb
		},
	}
	if o.out != nil {
		code := Run(ctx, withStdout(e, o.out), DefaultRegistry, args)
		return runResult{code, "", errb.String()}
	}
	code := Run(ctx, e, DefaultRegistry, args)
	return runResult{code, out.String(), errb.String()}
}

func withStdout(e Env, w interface{ Write([]byte) (int, error) }) Env {
	e.Stdout = w
	return e
}

func (f *roFix) run(args ...string) runResult {
	f.t.Helper()
	return f.exec(context.Background(), runOpts{}, args...)
}

func (f *roFix) runWith(o runOpts, args ...string) runResult {
	f.t.Helper()
	return f.exec(context.Background(), o, args...)
}

// utcLogs shows log times in UTC for the test and restores the zone afterwards.
func utcLogs(t *testing.T) {
	t.Helper()
	old := logsLocation
	logsLocation = time.UTC
	t.Cleanup(func() { logsLocation = old })
}

func checkGolden(t *testing.T, name string, r runResult) {
	t.Helper()
	var b strings.Builder
	b.WriteString("== stdout ==\n" + r.out)
	if r.errOut != "" {
		b.WriteString("== stderr ==\n" + r.errOut)
	}
	fmt.Fprintf(&b, "== exit %d ==\n", r.code)
	got := strings.ReplaceAll(b.String(), runtime.GOOS+"/"+runtime.GOARCH, "<os>/<arch>")
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateReadonly {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-readonly): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s differs.\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
	for _, line := range strings.Split(r.out+r.errOut, "\n") {
		// Prose is wrapped at 80 columns; log lines and table rows are exempt where a path or an id
		// is wider than the line (never cut), and the version line is a build string.
		if w := len([]rune(line)); w > 80 && !strings.HasPrefix(line, "aictl ") && !strings.HasPrefix(name, "logs.") && !strings.HasPrefix(name, "ark.drives.") {
			t.Errorf("%s: line wider than 80 columns (%d): %q", name, w, line)
		}
	}
}

type goldenCase struct {
	name   string
	args   []string
	routes map[string]string // path -> fixture
	env    map[string]string
	exit   int
}

func goldenCases() []goldenCase {
	st := map[string]string{"/status": "status.online", "/models": "models"}
	surv := map[string]string{"/status": "status.survival", "/models": "models"}
	es := func(a ...string) []string { return append([]string{"--lang", "es"}, a...) }
	return []goldenCase{
		{"status.online.en", []string{"status"}, st, nil, 0},
		{"status.online.es", es("status"), st, nil, 0},
		{"status.online.ascii", []string{"status"}, st, map[string]string{"LANG": "C"}, 0},
		{"status.survival.en", []string{"status"}, surv, nil, 0},
		{"status.survival.es", es("status"), surv, nil, 0},
		{"status.minimal.en", []string{"status"}, map[string]string{"/status": "status.minimal"}, nil, 0},
		{"models.en", []string{"models"}, map[string]string{"/models": "models"}, nil, 0},
		{"models.es", es("models"), map[string]string{"/models": "models"}, nil, 0},
		{"models.all.en", []string{"models", "--all"}, map[string]string{"/models": "models"}, nil, 0},
		{"model.show.en", []string{"models", "show", "qwen3.5-9b-q4km"}, map[string]string{"/models/qwen3.5-9b-q4km": "model.qwen"}, nil, 0},
		{"model.show.es", es("models", "show", "qwen3.5-9b-q4km"), map[string]string{"/models/qwen3.5-9b-q4km": "model.qwen"}, nil, 0},
		{"default.en", []string{"default"}, map[string]string{"/models": "models"}, nil, 0},
		{"default.es", es("default"), map[string]string{"/models": "models"}, nil, 0},
		{"services.en", []string{"services"}, map[string]string{"/services": "services"}, nil, 0},
		{"services.es", es("services"), map[string]string{"/services": "services"}, nil, 0},
		{"logs.en", []string{"logs"}, map[string]string{"/logs": "logs"}, nil, 0},
		{"logs.es", es("logs", "--unit", "llama"), map[string]string{"/logs": "logs"}, nil, 0},
		{"logs.verbose.en", []string{"-v", "logs"}, map[string]string{"/logs": "logs"}, nil, 0},
		{"doctor.mixed.en", []string{"doctor"}, map[string]string{"/doctor": "doctor.mixed"}, nil, ExitErrors},
		{"doctor.mixed.es", es("doctor"), map[string]string{"/doctor": "doctor.mixed"}, nil, ExitErrors},
		{"doctor.mixed.all.verbose.en", []string{"-v", "doctor", "--all"}, map[string]string{"/doctor": "doctor.mixed"}, nil, ExitErrors},
		{"doctor.warn.en", []string{"doctor"}, map[string]string{"/doctor": "doctor.warn"}, nil, ExitWarnings},
		{"doctor.ok.en", []string{"doctor"}, map[string]string{"/doctor": "doctor.ok"}, nil, 0},
		{"doctor.ok.es", es("doctor", "--all"), map[string]string{"/doctor": "doctor.ok"}, nil, 0},
		{"version.en", []string{"version"}, map[string]string{"/status": "status.online"}, nil, 0},
		{"version.es", es("version"), map[string]string{"/status": "status.online"}, nil, 0},
		{"ark.en", []string{"ark"}, map[string]string{"/ark": "ark", "/ark/sync": "ark.sync"}, nil, 0},
		{"ark.es", es("ark", "status"), map[string]string{"/ark": "ark", "/ark/sync": "ark.sync"}, nil, 0},
		{"ark.drives.en", []string{"ark", "drives"}, map[string]string{"/ark/drives": "drives"}, nil, 0},
		{"ark.drives.es", es("ark", "drives"), map[string]string{"/ark/drives": "drives"}, nil, 0},
		{"cloud.en", []string{"cloud", "status"}, map[string]string{"/cloud": "cloud"}, nil, 0},
		{"cloud.es", es("cloud", "status"), map[string]string{"/cloud": "cloud"}, nil, 0},
		{"cloud.notenrolled.en", []string{"cloud", "status"}, map[string]string{"/cloud": "cloud.notenrolled"}, nil, 0},
		{"profile.show.en", []string{"profile", "show"}, map[string]string{"/profile": "profile"}, nil, 0},
		{"profile.show.es", es("profile", "show"), map[string]string{"/profile": "profile"}, nil, 0},
		{"profile.suggest.en", []string{"profile", "suggest"}, map[string]string{"/profile": "profile", "/status": "status.online"}, nil, 0},
		{"profile.suggest.es", es("profile", "suggest"), map[string]string{"/profile": "profile", "/status": "status.online"}, nil, 0},
	}
}

func TestGolden(t *testing.T) {
	utcLogs(t)
	for _, gc := range goldenCases() {
		t.Run(gc.name, func(t *testing.T) {
			f := newROFix(t, fake.Options{})
			for p, fx := range gc.routes {
				f.serve(p, fx)
			}
			r := f.runWith(runOpts{env: gc.env}, gc.args...)
			if r.code != gc.exit {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, gc.exit, r.out, r.errOut)
			}
			checkGolden(t, gc.name, r)
		})
	}
}

func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%q", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("JSON differs\n got %s\nwant %s", got, want)
	}
	dec := json.NewDecoder(strings.NewReader(got))
	var first any
	if err := dec.Decode(&first); err != nil || dec.More() {
		t.Errorf("--json must be exactly one document, got %q", got)
	}
}

// --json prints the agent body unchanged (equal after re-marshal) with an empty stderr, and the
// documented composites for the commands whose JSON is not the body.
func TestJSONIsTheAgentBody(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		route   string
		fixture string
		extra   map[string]string
	}{
		{"status", []string{"status"}, "/status", "status.online", nil},
		{"models", []string{"models"}, "/models", "models", nil},
		{"models all", []string{"models", "--all"}, "/models", "models", nil},
		{"models show", []string{"models", "show", "qwen3.5-9b-q4km"}, "/models/qwen3.5-9b-q4km", "model.qwen", nil},
		{"services", []string{"services"}, "/services", "services", nil},
		{"logs", []string{"logs"}, "/logs", "logs", nil},
		{"doctor ok", []string{"doctor"}, "/doctor", "doctor.ok", nil},
		{"drives", []string{"ark", "drives"}, "/ark/drives", "drives", nil},
		{"cloud", []string{"cloud", "status"}, "/cloud", "cloud", nil},
		{"profile show", []string{"profile", "show"}, "/profile", "profile", nil},
		{"profile suggest", []string{"profile", "suggest"}, "/profile", "profile", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newROFix(t, fake.Options{})
			f.serve(tc.route, tc.fixture)
			r := f.run(append([]string{"--json"}, tc.args...)...)
			if r.code != 0 || r.errOut != "" {
				t.Fatalf("exit %d stderr %q", r.code, r.errOut)
			}
			sameJSON(t, r.out, roBody(t, tc.fixture))
		})
	}

	t.Run("ark status is the two bodies", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/ark", "ark")
		f.serve("/ark/sync", "ark.sync")
		r := f.run("--json", "ark")
		if r.code != 0 || r.errOut != "" {
			t.Fatalf("exit %d stderr %q", r.code, r.errOut)
		}
		sameJSON(t, r.out, `{"ark":`+roBody(t, "ark")+`,"sync":`+roBody(t, "ark.sync")+`}`)
	})
	t.Run("ark status without a last run has sync null", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/ark", "ark")
		f.ag.RespondError("GET", "/ark/sync", 404, api.CodeNotFound, "No run.", "None.", nil)
		r := f.run("--json", "ark", "status")
		if r.code != 0 {
			t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
		sameJSON(t, r.out, `{"ark":`+roBody(t, "ark")+`,"sync":null}`)
	})
	t.Run("default is {default}", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/models", "models")
		r := f.run("--json", "default")
		sameJSON(t, r.out, `{"default":"qwen3.5-9b-q4km"}`)
	})
	t.Run("default with none set", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.ag.Respond("GET", "/models", 200, `{"default": null, "models": []}`)
		r := f.run("--json", "default")
		sameJSON(t, r.out, `{"default":""}`)
		r = f.run("default")
		if r.code != 0 || !strings.Contains(r.out, "No default model is set") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("version", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/status", "status.online")
		r := f.run("--json", "version")
		var v struct {
			CLI struct {
				Version, Commit, OS, Arch string
			} `json:"cli"`
			Agent          *struct{ Version string } `json:"agent"`
			LlamaBuild     string                    `json:"llama_build"`
			CatalogVersion string                    `json:"catalog_version"`
		}
		if err := json.Unmarshal([]byte(r.out), &v); err != nil || r.code != 0 || r.errOut != "" {
			t.Fatalf("exit %d err %v stdout %q stderr %q", r.code, err, r.out, r.errOut)
		}
		if v.CLI.Version != "0.0.0-dev" || v.CLI.OS != runtime.GOOS || v.Agent == nil || v.Agent.Version != "0.0.0-dev" ||
			v.LlamaBuild != "f3586e66b-cuda" || v.CatalogVersion != "2026.11.02-1" {
			t.Errorf("version document: %s", r.out)
		}
	})
}

// doctor exits 0 / 8 / 9 by what it found, with and without --json, and -q hides warnings.
func TestDoctorExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		body    string
		want    int
	}{
		{"all fine", "doctor.ok", "", 0},
		{"info only", "", `{"summary":{"ok":3,"info":1},"results":[{"check":"x","code":"LAI-NET-011","severity":"info","title":"t","fix":"f"}]}`, 0},
		{"warnings", "doctor.warn", "", ExitWarnings},
		{"error", "", `{"summary":{"ok":3,"error":1},"results":[{"check":"x","code":"LAI-LLM-001","severity":"error","title":"t","fix":"f"}]}`, ExitErrors},
		{"critical", "", `{"summary":{"critical":1},"results":[]}`, ExitErrors},
		{"error beats warning", "doctor.mixed", "", ExitErrors},
		{"summary missing, results decide", "", `{"results":[{"check":"x","code":"LAI-HW-012","severity":"warning","title":"t","fix":"f"}]}`, ExitWarnings},
	}
	for _, tc := range cases {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s json=%v", tc.name, asJSON), func(t *testing.T) {
				f := newROFix(t, fake.Options{})
				body := tc.body
				if tc.fixture != "" {
					body = roBody(t, tc.fixture)
				}
				f.ag.Respond("GET", "/doctor", 200, body)
				args := []string{"doctor"}
				if asJSON {
					args = []string{"--json", "doctor"}
				}
				r := f.run(args...)
				if r.code != tc.want {
					t.Fatalf("exit %d, want %d (stderr %q)", r.code, tc.want, r.errOut)
				}
				if asJSON {
					sameJSON(t, r.out, body)
					if r.errOut != "" {
						t.Errorf("stderr must stay empty with --json, got %q", r.errOut)
					}
				}
			})
		}
	}
	t.Run("quiet hides warnings and the summary prose", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/doctor", "doctor.warn")
		r := f.run("-q", "doctor")
		if r.code != ExitWarnings || strings.Contains(r.out, "battery") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
}

// Problems are listed critical, error, warning, info, each with its code and a fix line, and
// the summary comes last.
func TestDoctorGroupingOrder(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.serve("/doctor", "doctor.mixed")
	r := f.run("doctor")
	order := []string{"[LAI-AGT-004]", "[LAI-LLM-001]", "[LAI-HW-012]", "[LAI-NET-011]", "41 checks passed"}
	last := -1
	for _, s := range order {
		i := strings.Index(r.out, s)
		if i < 0 || i <= last {
			t.Fatalf("%q out of order or missing (at %d, previous %d):\n%s", s, i, last, r.out)
		}
		last = i
	}
	if n := strings.Count(r.out, "→ Fix:"); n != 4 {
		t.Errorf("%d fix lines, want 4", n)
	}
	if r.errOut != "" {
		t.Errorf("findings are results and belong on stdout; stderr = %q", r.errOut)
	}
}

func TestDoctorQuery(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.serve("/doctor", "doctor.ok")
	if r := f.run("doctor", "--area", "hw,net", "--area", "LLM", "--fast"); r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.errOut)
	}
	if got := f.ag.Requests()[len(f.ag.Requests())-1].Path; got != "/doctor?area=HW&area=NET&area=LLM&fast=1" {
		t.Errorf("request %q", got)
	}
	if r := f.run("doctor", "--area", "not an area"); r.code != ExitUsage {
		t.Errorf("a bad area is a usage error, got exit %d", r.code)
	}
}

func TestModelsQueryAndFilters(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.serve("/models", "models")
	lastPath := func() string { rs := f.ag.Requests(); return rs[len(rs)-1].Path }

	if r := f.run("models", "--loadable", "--engine", "whisper"); r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.errOut)
	}
	if lastPath() != "/models?engine=whisper&loadable=1" {
		t.Errorf("request %q", lastPath())
	}
	// The default view is client side: in the profile, or stored; --all shows the whole catalog.
	r := f.run("models")
	if lastPath() != "/models" || strings.Contains(r.out, "other-catalog-model") || !strings.Contains(r.out, "qwen3.5-9b-q4km") {
		t.Errorf("default filter wrong (request %q):\n%s", lastPath(), r.out)
	}
	if r := f.run("models", "--all"); !strings.Contains(r.out, "other-catalog-model") {
		t.Errorf("--all must list the whole catalog:\n%s", r.out)
	}
	if r := f.run("models", "--engine", "tts"); r.code != ExitUsage || !strings.Contains(r.errOut, "Unknown engine") {
		t.Errorf("unknown engine: exit %d %q", r.code, r.errOut)
	}
	if r := f.run("models", "extra"); r.code != ExitUsage {
		t.Errorf("stray argument: exit %d", r.code)
	}
	if r := f.run("models", "show"); r.code != ExitUsage {
		t.Errorf("show without an id: exit %d", r.code)
	}
	f.ag.RespondError("GET", "/models/nope", 404, api.CodeNotFound, "No such model.", "Run `aictl models`.", nil)
	if r := f.run("models", "show", "nope"); r.code != ExitNotFound || !strings.Contains(r.errOut, "No such model.") {
		t.Errorf("unknown model: exit %d %q", r.code, r.errOut)
	}
	// An id with odd characters is escaped, never a path of its own.
	f.run("models", "show", "../status")
	if lastPath() != "/models/..%2Fstatus" {
		t.Errorf("id not escaped: %q", lastPath())
	}
}

func TestServicesRejectsAnUnknownSubcommand(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.serve("/models", "models")
	f.serve("/services", "services")
	if r := f.run("services", "bogus"); r.code != ExitUsage {
		t.Errorf("services bogus: exit %d", r.code)
	}
}

// ---- failure paths -----------------------------------------------------------------------------

func TestAgentDown(t *testing.T) {
	f := newROFix(t, fake.Options{NoSocket: true, NoTCP: true})
	for _, args := range [][]string{{"status"}, {"models"}, {"services"}, {"logs"}, {"doctor"}, {"ark"}, {"ark", "drives"},
		{"cloud", "status"}, {"profile", "show"}, {"default"}} {
		r := f.run(args...)
		if r.code != ExitUnreachable || r.out != "" || !strings.Contains(r.errOut, "[LAI-CLI-001]") || !strings.Contains(r.errOut, "→ Fix:") {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, r.code, r.out, r.errOut)
		}
		j := f.run(append([]string{"--json"}, args...)...)
		var env struct{ Error struct{ Code string } }
		if j.code != ExitUnreachable || j.errOut != "" || json.Unmarshal([]byte(j.out), &env) != nil || env.Error.Code != CodeUnreachable {
			t.Errorf("%v --json: exit %d stdout %q stderr %q", args, j.code, j.out, j.errOut)
		}
	}
	t.Run("version still succeeds", func(t *testing.T) {
		r := f.run("version")
		if r.code != 0 || r.errOut != "" || !strings.Contains(r.out, "aictl 0.0.0-dev (") || !strings.HasSuffix(r.out, "\nagent  not reachable\n") {
			t.Errorf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
		r = f.run("--lang", "es", "version")
		if !strings.HasSuffix(r.out, "agent  no responde\n") {
			t.Errorf("es: %q", r.out)
		}
		j := f.run("--json", "version")
		var v struct {
			Agent          *struct{} `json:"agent"`
			LlamaBuild     *string   `json:"llama_build"`
			CatalogVersion *string   `json:"catalog_version"`
		}
		if j.code != 0 || json.Unmarshal([]byte(j.out), &v) != nil || v.Agent != nil || v.LlamaBuild != nil || v.CatalogVersion != nil ||
			!strings.Contains(j.out, `"agent":null`) {
			t.Errorf("--json: exit %d %q", j.code, j.out)
		}
	})
}

func TestAccessRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"not authenticated 401", 401, api.CodeUnauthenticated},
		{"not an operator 403", 403, api.CodePeerNotOperator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newROFix(t, fake.Options{})
			f.ag.RespondError("GET", "/status", tc.status, tc.code, "You may not do that.", "Ask an administrator.", nil)
			r := f.run("status")
			if r.code != ExitDenied || !strings.Contains(r.errOut, "["+tc.code+"]") || strings.Contains(r.errOut, fmt.Sprint(tc.status)) {
				t.Errorf("exit %d stderr %q", r.code, r.errOut)
			}
			j := f.run("--json", "status")
			if j.code != ExitDenied || j.errOut != "" || !strings.Contains(j.out, tc.code) {
				t.Errorf("--json: exit %d out %q err %q", j.code, j.out, j.errOut)
			}
		})
	}
}

// 503 "starting" is retried by the framework and the command then succeeds; any other 503 is exit 3.
func TestStartingAgentIsRetried(t *testing.T) {
	f := newROFix(t, fake.Options{})
	var mu sync.Mutex
	n := 0
	f.ag.Handle("GET", "/services", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k <= 2 {
			fake.WriteEnvelope(w, 503, api.CodeStarting, "Starting.", "Wait.", nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, roBody(t, "services"))
	})
	r := f.run("services")
	if r.code != 0 || !strings.Contains(r.out, "llama") || f.h.sleeps.count() != 2 {
		t.Fatalf("exit %d out %q err %q sleeps %d", r.code, r.out, r.errOut, f.h.sleeps.count())
	}
	f.ag.RespondError("GET", "/doctor", 503, api.CodeRouterDown, "Router down.", "Restart it.", nil)
	if r := f.run("doctor"); r.code != ExitUnreachable || !strings.Contains(r.errOut, "["+api.CodeRouterDown+"]") {
		t.Errorf("other 503: exit %d %q", r.code, r.errOut)
	}
}

func TestUndecodableAnswerIsExit1(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.ag.Respond("GET", "/status", 200, `[1,2,3]`)
	r := f.run("status")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+CodeBadResponse+"]") || !strings.Contains(r.errOut, "request: req_") {
		t.Errorf("exit %d stderr %q", r.code, r.errOut)
	}
	f.ag.Respond("GET", "/models", 200, `not json`)
	if r := f.run("models"); r.code != ExitFailed {
		t.Errorf("exit %d", r.code)
	}
}

// A body missing most of its sections still prints what it has, and a time that cannot be read is
// left out; the model names, the last sync and the hardware lines are optional extras.
func TestPartialData(t *testing.T) {
	t.Run("status without sections", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/status", "status.minimal")
		r := f.run("status")
		if r.code != 0 || !strings.HasPrefix(r.out, "Node ai-n3\n") || !strings.Contains(r.out, "Mode  online\n") || strings.Contains(r.out, "since") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
		if strings.Contains(r.out, "Graphics") || strings.Contains(r.out, "Cloud") || strings.Contains(r.out, "0.0 GB") {
			t.Errorf("a missing section must not print as zeros:\n%s", r.out)
		}
	})
	t.Run("brain falls back to ids when /models fails", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/status", "status.online")
		f.ag.RespondError("GET", "/models", 500, api.CodeInternal, "Broken.", "Report it.", nil)
		r := f.run("status")
		if r.code != 0 || r.errOut != "" || !strings.Contains(r.out, "✓ qwen3.5-9b-q4km — default, loaded") {
			t.Errorf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
	})
	t.Run("ark status without a last run", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/ark", "ark")
		f.ag.RespondError("GET", "/ark/sync", 404, api.CodeNotFound, "No run.", "None.", nil)
		r := f.run("ark")
		if r.code != 0 || !strings.Contains(r.out, "82% ready") && !strings.Contains(r.out, "ready") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("version when /status fails after /health", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.ag.RespondError("GET", "/status", 500, api.CodeInternal, "Broken.", "Report it.", nil)
		r := f.run("version")
		if r.code != 0 || !strings.HasSuffix(r.out, "\nagent 0.0.0-dev\n") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("profile suggest without /status uses the match reasons", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/profile", "profile")
		f.ag.RespondError("GET", "/status", 500, api.CodeInternal, "Broken.", "Report it.", nil)
		r := f.run("profile", "suggest")
		want := `✓ This computer matches "small-8gb" (backend cuda; vram 8192 MiB in 7000..11999).`
		if r.code != 0 || strings.Join(strings.Fields(r.out), " ") != want {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("profile in use differs from the suggestion", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.ag.Respond("GET", "/profile", 200, `{"profile":"cpu-only","suggested":"small-8gb","match_reasons":["backend cuda"]}`)
		f.ag.Respond("GET", "/status", 200, `{}`)
		r := f.run("profile", "suggest")
		if r.code != 0 || !strings.Contains(r.out, `looks like "small-8gb"`) || !strings.Contains(r.out, `uses "cpu-only"`) {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("services as a bare array", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.ag.Respond("GET", "/services", 200, `[{"name":"llama","active":"active","sub":"running","healthy":true}]`)
		r := f.run("services")
		if r.code != 0 || !strings.Contains(r.out, "llama") || !strings.Contains(r.out, "running") {
			t.Errorf("exit %d out %q", r.code, r.out)
		}
	})
}

// A newer agent may add fields anywhere: they are ignored, never an error.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	f := newROFix(t, fake.Options{})
	extra := func(fixture string) string {
		return strings.Replace(roBody(t, fixture), "{", `{"future_field": {"a": [1, 2]}, `, 1)
	}
	f.ag.Respond("GET", "/status", 200, extra("status.online"))
	f.ag.Respond("GET", "/models", 200, extra("models"))
	f.ag.Respond("GET", "/doctor", 200, extra("doctor.ok"))
	f.ag.Respond("GET", "/cloud", 200, extra("cloud"))
	for _, args := range [][]string{{"status"}, {"models"}, {"doctor"}, {"cloud", "status"}} {
		if r := f.run(args...); r.code != 0 || r.errOut != "" || r.out == "" {
			t.Errorf("%v: exit %d stderr %q", args, r.code, r.errOut)
		}
	}
}

// A service without a unit (voices, maps) is shown by the supervisor's own state, not as stopped.
func TestServicesWithoutUnits(t *testing.T) {
	f := newROFix(t, fake.Options{})
	f.ag.Respond("GET", "/services", 200, `{"services":[
	  {"name":"llama","unit":"localai-llama.service","active":"active","sub":"running","healthy":true,"state":"ok"},
	  {"name":"piper","unit":null,"healthy":true,"state":"ok"},
	  {"name":"maps","unit":null,"healthy":false,"state":"missing_content"},
	  {"name":"kiwix","unit":"localai-kiwix.service","active":"activating","sub":"start","healthy":false,"state":"starting","enabled":false}]}`)
	r := f.run("services")
	for _, want := range []string{"piper    available", "maps     stopped ", "kiwix    starting   …", "disabled"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
}

// Hundreds of rows and thousands of log lines go through unchanged and unwrapped.
func TestHugeOutput(t *testing.T) {
	t.Run("logs 2000 long lines", func(t *testing.T) {
		utcLogs(t)
		f := newROFix(t, fake.Options{})
		var lines []wireLogLine
		long := strings.Repeat("x", 3000)
		for i := 0; i < 2000; i++ {
			lines = append(lines, wireLogLine{At: "2026-11-02T08:00:00Z", Priority: 6, Message: fmt.Sprintf("%04d %s", i, long)})
		}
		b, _ := json.Marshal(map[string]any{"unit": "x", "lines": lines, "truncated": true})
		f.ag.Respond("GET", "/logs", 200, string(b))
		r := f.run("logs", "-n", "2000")
		got := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
		if r.code != 0 || len(got) != 2000 || !strings.HasSuffix(got[1999], long) || !strings.HasPrefix(got[0], "08:00:00  0000 ") {
			t.Fatalf("exit %d, %d lines", r.code, len(got))
		}
		if !strings.Contains(r.errOut, "cut at the agent's size limit") {
			t.Errorf("truncated flag not reported: %q", r.errOut)
		}
		if q := f.ag.Requests(); q[len(q)-1].Path != "/logs?lines=2000&unit=agent" {
			t.Errorf("request %q", q[len(q)-1].Path)
		}
	})
	t.Run("500 doctor results and 1000 models", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		var res []map[string]any
		for i := 0; i < 500; i++ {
			res = append(res, map[string]any{"check": fmt.Sprintf("c.%d", i), "code": "LAI-HW-012", "severity": "warning", "title": "Running on battery", "fix": "Plug in."})
		}
		b, _ := json.Marshal(map[string]any{"summary": map[string]int{"ok": 1, "warning": 500}, "results": res})
		f.ag.Respond("GET", "/doctor", 200, string(b))
		r := f.run("doctor")
		if r.code != ExitWarnings || strings.Count(r.out, "[LAI-HW-012]") != 500 || !strings.Contains(r.out, "500 warnings") {
			t.Errorf("exit %d, %d findings", r.code, strings.Count(r.out, "[LAI-HW-012]"))
		}
		var ms []map[string]any
		for i := 0; i < 1000; i++ {
			ms = append(ms, map[string]any{"id": fmt.Sprintf("m-%d", i), "in_profile": true, "size_bytes": 1e9, "status": "stored", "loadable": true})
		}
		b, _ = json.Marshal(map[string]any{"models": ms})
		f.ag.Respond("GET", "/models", 200, string(b))
		r = f.run("models")
		if r.code != 0 || len(strings.Split(strings.TrimRight(r.out, "\n"), "\n")) != 1001 {
			t.Errorf("exit %d", r.code)
		}
	})
}

// A closed pipe ends the command with exit 0 and no message.
func TestClosedPipe(t *testing.T) {
	utcLogs(t)
	f := newROFix(t, fake.Options{})
	f.serve("/logs", "logs")
	f.serve("/status", "status.online")
	f.serve("/models", "models")
	f.serve("/doctor", "doctor.ok")
	for _, args := range [][]string{{"logs"}, {"status"}, {"models"}, {"doctor", "--all"}} {
		bp := &brokenPipe{}
		r := f.runWith(runOpts{out: bp}, args...)
		if r.code != ExitOK || r.errOut != "" || bp.calls != 1 {
			t.Errorf("%v: exit %d stderr %q, stdout written %d times", args, r.code, r.errOut, bp.calls)
		}
	}
	t.Run("logs -f stops following", func(t *testing.T) {
		old := logsFollowInterval
		logsFollowInterval = time.Millisecond
		defer func() { logsFollowInterval = old }()
		bp := &brokenPipe{}
		done := make(chan runResult, 1)
		go func() { done <- f.runWith(runOpts{out: bp}, "logs", "-f") }()
		select {
		case r := <-done:
			if r.code != ExitOK || r.errOut != "" {
				t.Errorf("exit %d stderr %q", r.code, r.errOut)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("logs -f kept running after the pipe closed")
		}
	})
}

// ---- version skew, flags and help -------------------------------------------------------------

func TestVersionSkewWarns(t *testing.T) {
	f := newROFix(t, fake.Options{Version: "9.9.9"})
	f.serve("/status", "status.online")
	r := f.run("version")
	if r.code != 0 || !strings.Contains(r.out, "agent 9.9.9") || !strings.Contains(r.errOut, "[LAI-CLI-003]") {
		t.Errorf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
	j := f.run("--json", "version")
	if j.code != 0 || strings.Contains(j.errOut, "LAI-CLI-003") == false {
		// the warning is for stderr even with --json when the command succeeded
		t.Errorf("--json skew: exit %d err %q", j.code, j.errOut)
	}
}

func TestCommandsAreRegisteredAndDocumented(t *testing.T) {
	for _, name := range []string{"status", "models", "default", "services", "logs", "doctor", "version", "ark", "cloud", "profile"} {
		if _, ok := DefaultRegistry.Lookup(name); !ok {
			t.Errorf("command %q is not registered", name)
		}
	}
	f := newROFix(t, fake.Options{})
	r := f.run("help", "logs")
	for _, s := range []string{"--unit", "-n", "--since", "-f", "Default timeout"} {
		if !strings.Contains(r.out, s) && s != "Default timeout" {
			t.Errorf("help logs lacks %q:\n%s", s, r.out)
		}
	}
	if r := f.run("help", "doctor"); !strings.Contains(r.out, "-area") || !strings.Contains(r.out, "Default timeout: 1m0s") {
		t.Errorf("help doctor:\n%s", r.out)
	}
	if r := f.run("help", "profile"); !strings.Contains(r.out, "show") || !strings.Contains(r.out, "suggest") {
		t.Errorf("help profile:\n%s", r.out)
	}
}

func TestStatusRejectsStrayArguments(t *testing.T) {
	f := newROFix(t, fake.Options{})
	for _, args := range [][]string{{"status", "now"}, {"doctor", "x"}, {"logs", "x"}, {"version", "x"}, {"ark", "x"}, {"ark", "drives", "x"}} {
		if r := f.run(args...); r.code != ExitUsage || !strings.Contains(r.errOut, "[LAI-CLI-005]") {
			t.Errorf("%v: exit %d %q", args, r.code, r.errOut)
		}
	}
}

// No read-only command file spells a doctor code: the codes come from the agent's answers, and the
// codes aictl raises are constants (scripts/check-doctor-codes.sh enforces the same for CI).
func TestNoCodeLiteralsInReadOnlyCommands(t *testing.T) {
	codeRE := regexp.MustCompile(`LAI-[A-Z]+-[0-9]{3}`)
	for _, p := range []string{"cmd_status.go", "cmd_models.go", "cmd_default.go", "cmd_services.go", "cmd_logs.go", "cmd_doctor.go",
		"cmd_version.go", "cmd_ark_read.go", "cmd_cloud_status.go", "cmd_profile_read.go", "readonly.go", "readonly_wire.go"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if m := codeRE.Find(b); m != nil {
			t.Errorf("%s spells a doctor code literal %s", p, m)
		}
	}
}

// The fixture times of a test come from one clock reading, however many times the body is built:
// a clock that moves a second on every call (the second boundary that made the served and the
// expected JSON differ) must not change them.
func TestFixtureTimesShareOneClockReading(t *testing.T) {
	old := roNow
	base := time.Date(2026, 11, 2, 16, 29, 25, 0, time.UTC)
	calls := 0
	roNow = func() time.Time { calls++; return base.Add(time.Duration(calls) * time.Second) }
	defer func() { roNow = old }()

	first := roBody(t, "models")
	for i := 0; i < 5; i++ {
		if again := roBody(t, "models"); again != first {
			t.Fatalf("the same fixture built twice in one test differs:\n%s\n%s", first, again)
		}
	}
	if calls != 1 {
		t.Errorf("the clock was read %d times for one test, want 1", calls)
	}
	if !strings.Contains(first, "2026-11-01T10:29:06Z") { // anchor +1 s, minus 30 h and the 20 s margin
		t.Errorf("fixture time not derived from the injected clock:\n%s", first)
	}
	// A different test gets its own reading.
	t.Run("sub", func(t *testing.T) {
		if roBody(t, "models") == first {
			t.Error("a new test must take a new clock reading")
		}
	})
}
