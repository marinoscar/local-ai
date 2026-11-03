package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// The test arch is fixed so the tests run the same on amd64 and arm64 hosts.
const testArch = "amd64"

// fakeClock never sleeps: Sleep advances the time it reports and records the wait. The tests read the base
// time once and share it between what is served and what is expected.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	return nil
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var t time.Duration
	for _, d := range c.sleeps {
		t += d
	}
	return t
}

// fakeBinary is the content of a stand-in agent: the fake exec answers `version --json` from it.
func fakeBinary(version, arch string) []byte { return []byte("FAKE " + version + " " + arch + "\n") }

// fakeExec is the execx.Runner of the tests: no process is ever started. It records every call and answers
//   - systemctl and systemd-run with exit 0 (or the scripted failure),
//   - any other path as an agent binary, from the first line of that file: "FAKE <version> <arch>" reports
//     them as `version --json`, anything else exits 1.
type fakeExec struct {
	mu    sync.Mutex
	calls []execx.Cmd

	// onRestart runs when `systemctl --no-block restart localai-agent.service` is called; its error is the
	// answer. It may call runtime.Goexit to simulate the process dying.
	onRestart func() error
	// armExit is the exit code of systemd-run.
	armExit int
}

func (f *fakeExec) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	hook, armExit := f.onRestart, f.armExit
	f.mu.Unlock()
	switch filepath.Base(c.Path) {
	case "systemctl":
		if len(c.Args) == 3 && c.Args[1] == "restart" && hook != nil {
			if err := hook(); err != nil {
				return execx.Result{}, err
			}
		}
		return execx.Result{}, nil
	case "systemd-run":
		return execx.Result{ExitCode: armExit, Stderr: []byte("Unit localai-agent-rollback.service already exists.\nmore")}, nil
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return execx.Result{}, err
	}
	var v, arch string
	if n, _ := fmt.Sscanf(strings.SplitN(string(data), "\n", 2)[0], "FAKE %s %s", &v, &arch); n != 2 || len(c.Args) != 2 || c.Args[0] != "version" || c.Args[1] != "--json" {
		return execx.Result{ExitCode: 1, Stderr: []byte("broken")}, nil
	}
	out, _ := json.Marshal(map[string]string{"version": v, "commit": "test", "os": "linux", "arch": arch, "built_at": "unknown"})
	return execx.Result{Stdout: out}, nil
}

// argv lists the calls as "name arg arg" lines.
func (f *fakeExec) argv() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	}
	return out
}

func (f *fakeExec) count(prefix string) int {
	n := 0
	for _, l := range f.argv() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeExec) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

const (
	restartLine = "systemctl --no-block restart localai-agent.service"
	armLine     = "systemd-run --unit=localai-agent-rollback --on-active=120s --timer-property=AccuracySec=1s %s update-rollback-check"
)

// recAudit records audit events.
type recAudit struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (a *recAudit) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evs = append(a.evs, ev)
	return nil
}

func (a *recAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.evs {
		out = append(out, e.Action+":"+string(e.Result)+":"+e.Code)
	}
	return out
}

// recEvents records update.state events.
type recEvents struct {
	mu     sync.Mutex
	states []string
}

func (e *recEvents) Publish(typ, _ string, data any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(data)
	var d struct{ State string }
	_ = json.Unmarshal(b, &d)
	e.states = append(e.states, typ+":"+d.State)
	return nil
}

func (e *recEvents) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.states...)
}

// fakeState is an in-memory state store.
type fakeState struct {
	mu sync.Mutex
	st state.State
}

func (f *fakeState) Snapshot() state.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeState) MutateSync(fn func(*state.State) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := f.st
	if err := fn(&cp); err != nil {
		return err
	}
	f.st = cp
	return nil
}

// recAlerts records raised alerts.
type recAlerts struct {
	mu    sync.Mutex
	codes []string
	last  map[string]any
}

func (a *recAlerts) Raise(code string, details map[string]any, _ ...doctor.RaiseOption) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.codes = append(a.codes, code)
	a.last = details
	return nil
}

// rig is one node: a temp /opt/localai/bin with a running agent, a temp /var/lib/localai/update, a signer, a
// fake GitHub and a Runner wired to all of it.
type rig struct {
	t      *testing.T
	root   string
	paths  Paths
	signer *updatetest.Signer
	srv    *updatetest.Server
	clock  *fakeClock
	exec   *fakeExec
	audit  *recAudit
	events *recEvents
	state  *fakeState
	alerts *recAlerts
	mode   string
	start  time.Time
}

// newRig creates a node running version cur with a fake GitHub that has no release yet.
func newRig(t *testing.T, cur string) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{
		t: t, root: root,
		paths:  Paths{UpdateDir: filepath.Join(root, "var", "update"), BinDir: filepath.Join(root, "opt", "bin")},
		signer: updatetest.NewSigner(),
		clock:  newClock(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)),
		exec:   &fakeExec{},
		audit:  &recAudit{},
		events: &recEvents{},
		state:  &fakeState{},
		alerts: &recAlerts{},
		mode:   "online",
	}
	r.start = r.clock.Now()
	for _, d := range []string{r.paths.UpdateDir, r.paths.BinDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.writeAgent(fakeBinary(cur, testArch))
	r.srv = updatetest.NewServer("owner-a/repo-a")
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) writeAgent(content []byte) {
	r.t.Helper()
	if err := os.WriteFile(r.paths.Agent(), content, 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// release adds a signed release of version with fake binaries for both architectures.
func (r *rig) release(version string) *updatetest.Release {
	r.t.Helper()
	rel := updatetest.NewRelease(r.signer, version, map[string][]byte{
		BinaryName("amd64"): fakeBinary(version, "amd64"),
		BinaryName("arm64"): fakeBinary(version, "arm64"),
	})
	r.srv.Add(rel)
	return rel
}

// runner builds the Runner of a process running version cur.
func (r *rig) runner(cur string) *Runner {
	pub, err := ParsePublicKey(r.signer.PublicKeyFile())
	if err != nil {
		r.t.Fatal(err)
	}
	return &Runner{
		Paths:   r.paths,
		Current: cur,
		Arch:    testArch,
		Keys:    []PublicKey{pub},
		Source: Source{
			Repo: "owner-a/repo-a", Channel: ChannelStable, Client: r.srv.Client(),
			APIBase: r.srv.URL, DownloadBase: r.srv.URL,
			AllowHost: func(h string) bool { return h == "127.0.0.1" },
		},
		Exec: r.exec, Clock: r.clock,
		Mode:  func() string { return r.mode },
		Audit: r.audit, Events: r.events, State: r.state, Alerts: r.alerts,
		Systemctl: "/usr/bin/systemctl", SystemdRun: "/usr/bin/systemd-run",
	}
}

func (r *rig) agentContent() string {
	r.t.Helper()
	b, err := os.ReadFile(r.paths.Agent())
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *rig) pending() *Pending {
	r.t.Helper()
	b, err := os.ReadFile(r.paths.Pending())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		r.t.Fatal(err)
	}
	return &p
}

func (r *rig) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// downloads counts the requests for release files.
func (r *rig) downloads() int {
	n := 0
	for _, q := range r.srv.Requests() {
		if strings.Contains(q.Path, "/releases/download/") {
			n++
		}
	}
	return n
}

func (r *rig) requests() int { return len(r.srv.Requests()) }

// errCode asserts err is an *Error with code and returns it.
func errCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Code != code {
		t.Fatalf("want error %s, got %v", code, err)
	}
	return e
}

func skipWithoutUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("file modes are checked on linux")
	}
}

type (
	execCmd    = execx.Cmd
	execResult = execx.Result
)

// execFunc adapts a function to execx.Runner.
type execFunc func(ctx context.Context, c execCmd) (execResult, error)

func (f execFunc) Run(ctx context.Context, c execCmd) (execResult, error) { return f(ctx, c) }

// failFS is the real filesystem with one injected failure: a rename onto failRenameTo fails.
type failFS struct {
	OSFS
	failRenameTo string
}

func (f *failFS) Rename(oldpath, newpath string) error {
	if newpath == f.failRenameTo {
		return fmt.Errorf("injected: rename onto %s", filepath.Base(newpath))
	}
	return f.OSFS.Rename(oldpath, newpath)
}

// healthServer answers every request with a fixed body, over TLS.
type healthServer struct {
	*httptest.Server
	mu   sync.Mutex
	path string
}

func newHealthServer(t *testing.T, body string) *healthServer {
	t.Helper()
	h := &healthServer{}
	h.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.path = r.URL.Path
		h.mu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *healthServer) lastPath() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.path
}
