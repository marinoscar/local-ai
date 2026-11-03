package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	modesfake "github.com/marinoscar/local-ai/agent/internal/modes/fake"
	"github.com/marinoscar/local-ai/agent/internal/router"
	routerfake "github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	svcfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
)

// The rig boots the real wiring of this package over fakes: a temp directory for /etc/localai,
// /var/lib/localai and /srv/localai, a fake systemd, the scripted llama-server of router/fake over
// real HTTP, the fake hardware sampler, the fake tailscale command line and a fake NOTIFY_SOCKET. The
// agent under test serves its real HTTP API on a loopback port and a unix socket, so a test talks to it
// as aictl does.

const (
	modelAlpha = "model-alpha-9b-q4"
	modelGamma = "model-gamma-vl-q4"
	rigNode    = "ai-n3"
)

// syncBuf is a log sink that is safe for the daemon's goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// lines returns the log lines decoded as JSON objects.
func (s *syncBuf) lines() []map[string]any {
	s.mu.Lock()
	raw := append([]byte(nil), s.b.Bytes()...)
	s.mu.Unlock()
	var out []map[string]any
	for _, l := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(l, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// has reports whether a log line has msg and, for every key of attrs, that value.
func (s *syncBuf) has(msg string, attrs map[string]any) bool {
	for _, l := range s.lines() {
		if l["msg"] != msg {
			continue
		}
		ok := true
		for k, v := range attrs {
			if fmt.Sprint(l[k]) != fmt.Sprint(v) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// stepLog records the OnStep events in order and can hold a step.
type stepLog struct {
	mu     sync.Mutex
	events []StepEvent
	hold   map[int]chan struct{} // step number -> released when closed
}

func (l *stepLog) onStep(e StepEvent) {
	l.mu.Lock()
	l.events = append(l.events, e)
	var ch chan struct{}
	if !e.End {
		ch = l.hold[e.N]
	}
	l.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// holdStep makes the beginning of step n block until the returned release is called.
func (l *stepLog) holdStep(n int) (release func()) {
	ch := make(chan struct{})
	l.mu.Lock()
	if l.hold == nil {
		l.hold = map[int]chan struct{}{}
	}
	l.hold[n] = ch
	l.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// began returns the numbers of the steps in the order they began.
func (l *stepLog) began() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int
	for _, e := range l.events {
		if !e.End {
			out = append(out, e.N)
		}
	}
	return out
}

// offline is a modes.HTTPDoer and cloud.Doer for a node with no network.
type offline struct{}

func (offline) Do(*http.Request) (*http.Response, error) { return nil, errors.New("no network") }

// rig is one node.
type rig struct {
	t     *testing.T
	root  string
	llama *routerfake.Router
	sysd  *svcfake.Manager
	hw    *hwfake.Sampler
	ts    *modesfake.TailscaleCLI
	note  *notifyListener
	logs  *syncBuf
	steps *stepLog
	key   string
	hooks *Hooks
	env   Env
	d     *Daemon
}

type rigOption func(*rig)

// noNodeYAML leaves node.yaml missing.
func noNodeYAML() rigOption { return func(r *rig) { _ = os.Remove(r.nodeYAMLPath()) } }

// withNodeYAML replaces the content of node.yaml (llama's port is filled in for %LLAMA%).
func withNodeYAML(text string) rigOption {
	return func(r *rig) { r.writeNodeYAMLText(strings.ReplaceAll(text, "%LLAMA%", r.llamaPort())) }
}

// withEnv edits the Env before the daemon is built.
func withEnv(f func(*Env)) rigOption { return func(r *rig) { f(&r.env) } }

func newRig(t *testing.T, opts ...rigOption) *rig {
	t.Helper()
	// A short root: the socket path must fit sun_path (108 bytes), which t.TempDir of a subtest does not.
	root, err := os.MkdirTemp("", "lai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	r := &rig{t: t, root: root, logs: &syncBuf{}, steps: &stepLog{}, hooks: NewHooks()}
	r.key = newKey(t)
	r.llama = routerfake.New(r.key)
	t.Cleanup(r.llama.Close)
	for _, id := range []string{modelAlpha, modelGamma} {
		r.llama.Add(routerfake.Model{ID: id, Source: "preset"})
	}
	r.sysd = svcfake.New()
	r.sysd.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "active", SubState: "running", UnitFileState: "enabled", MainPID: 4242, ActiveEnterTimestamp: t0})
	r.hw = hwfake.New(rigHardware())
	r.ts = modesfake.NewTailscaleCLI()
	r.note = newNotifyListener(t, false)

	r.mkdir("etc/localai", "etc/systemd/system", "var/lib/localai", "srv/localai", "run/localai")
	r.write("etc/localai/api-key", r.key+"\n", 0o600)
	r.writeCatalog()
	r.writeModels(modelAlpha, modelGamma)
	r.writeNodeYAMLText(r.nodeYAML())

	sock := filepath.Join(r.root, "run", "localai", "agent.sock")
	r.env = Env{
		Root: r.root, Version: "0.1.0-test", Geteuid: func() int { return 0 }, Unprivileged: true,
		Stdout: r.logs, Notifier: NewNotifier(r.note.name), Hooks: r.hooks, OnStep: r.steps.onStep,
		TCPAddr: "127.0.0.1:0", SocketPath: sock, SocketHooks: noChown(),
		Runner: r.ts, Lookup: r.ts.Lookup, Systemd: r.sysd, Hardware: r.hw,
		CloudHTTP: offline{}, ModesHTTP: offline{}, CatalogGitHub: failingGitHub{},
		LookupLocalAI: func() (int, int, bool) { return 0, 0, false },
		RouterTiming:  router.Timing{LoadPoll: time.Millisecond, EvictPoll: time.Millisecond, ProgressEvery: time.Millisecond, HealthWait: 5 * time.Second},
		Getenv:        func(string) string { return "" },
	}
	r.ts.Installed, r.ts.DaemonUp, r.ts.Backend = true, true, "Running"
	for _, o := range opts {
		o(r)
	}
	return r
}

// failingGitHub is a catalog source that is never reachable.
type failingGitHub struct{}

func (failingGitHub) FetchCatalog(context.Context, string) (*catalog.Bundle, error) {
	return nil, errors.New("no network")
}

// noChown is the operating-system seam of the socket setup for a test that is not root: the group
// resolves and the chown is a no-op.
func noChown() api.Hooks {
	return api.Hooks{
		LookupGID: func(string) (int, error) { return os.Getgid(), nil },
		Chown:     func(string, int, int) error { return nil },
	}
}

func newKey(t *testing.T) string {
	t.Helper()
	k, err := router.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rigHardware() hardware.Snapshot {
	return hardware.Snapshot{
		SampledAt: t0, Backend: hardware.BackendCUDA,
		GPUs: []hardware.GPU{{
			Index: 0, Vendor: hardware.VendorNVIDIA, Name: "GPU Example", Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 8192, VRAMUsedMiB: 100, Throttling: []string{},
		}},
		RAM:   hardware.RAM{TotalMiB: 14336, AvailableMiB: 12000},
		Power: hardware.Power{Source: hardware.SourceAC},
		Disks: []hardware.Disk{},
	}
}

// ---- files --------------------------------------------------------------------------------------

func (r *rig) path(rel string) string { return filepath.Join(r.root, filepath.FromSlash(rel)) }

func (r *rig) mkdir(rels ...string) {
	r.t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(r.path(rel), 0o755); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) write(rel, data string, mode os.FileMode) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.path(rel)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.path(rel), []byte(data), mode); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(rel string) string {
	r.t.Helper()
	b, err := os.ReadFile(r.path(rel))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *rig) nodeYAMLPath() string { return r.path("etc/localai/node.yaml") }

func (r *rig) llamaPort() string {
	u, _ := url.Parse(r.llama.URL())
	return u.Port()
}

func (r *rig) nodeYAML() string {
	return "schema_version: 1\nnode:\n  id: " + rigNode + "\n  display_name: Node Three\nprofile: small-8gb\n" +
		"tailscale:\n  manage: false\n  serve_pattern: none\nports:\n  agent: 7070\n  llama: " + r.llamaPort() +
		"\n  kiwix: 8888\n  whisper: 8081\n"
}

func (r *rig) writeNodeYAMLText(text string) { r.write("etc/localai/node.yaml", text, 0o640) }

var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// writeCatalog installs the valid catalog of internal/catalog (its sha256 values are tokens the
// redaction check never sees as hex) under /etc/localai/catalog.
func (r *rig) writeCatalog() {
	r.t.Helper()
	src := filepath.Join("..", "catalog", "testdata", "valid")
	dst := r.path("etc/localai/catalog")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := shaToken.ReplaceAllStringFunc(string(b), func(m string) string {
			return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
		})
		return os.WriteFile(target, []byte(out), 0o644)
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

// writeModels puts a verified copy of each model on the internal root: the files named by the catalog
// and a MANIFEST.json the presence rule accepts.
func (r *rig) writeModels(ids ...string) {
	r.t.Helper()
	cat, err := catalog.Load(r.path("etc/localai/catalog"))
	if err != nil {
		r.t.Fatal(err)
	}
	for _, id := range ids {
		m, ok := cat.Model(id)
		if !ok {
			r.t.Fatalf("the catalog has no model %s", id)
		}
		dir := router.ModelDir(r.path("srv/localai"), id)
		var files []map[string]string
		for _, f := range m.Files {
			p := filepath.Join(dir, filepath.FromSlash(f.Name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				r.t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
				r.t.Fatal(err)
			}
			files = append(files, map[string]string{"kind": f.Kind, "path": f.Name, "sha256": f.SHA256})
		}
		man, _ := json.Marshal(map[string]any{"item_id": id, "state": "verified", "verified_at": t0.Format(time.RFC3339), "files": files})
		if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), man, 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// ---- running ------------------------------------------------------------------------------------

// start builds the daemon and runs startup steps 1 to 11; it fails the test on an error.
func (r *rig) start() *Daemon {
	r.t.Helper()
	d, err := r.tryStart()
	if err != nil {
		r.t.Fatalf("start: %v", err)
	}
	return d
}

// tryStart builds the daemon and starts it, returning the startup error. The daemon is shut down when
// the test ends.
func (r *rig) tryStart() (*Daemon, error) {
	r.t.Helper()
	d := NewDaemon(r.env)
	r.d = d
	r.t.Cleanup(func() { d.Shutdown() })
	return d, d.Start(context.Background())
}

// waitStarted waits for startup step 17.
func (r *rig) waitStarted() {
	r.t.Helper()
	select {
	case <-r.d.Started():
	case <-time.After(30 * time.Second):
		r.t.Fatalf("the agent did not finish starting; steps: %+v\nlog: %s", r.d.Steps(), r.logText())
	}
}

func (r *rig) logText() string {
	var sb strings.Builder
	for _, l := range r.logs.lines() {
		b, _ := json.Marshal(l)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// base is the TCP base URL of the API.
func (r *rig) base() string {
	r.t.Helper()
	a := r.d.Components().API.TCPAddr()
	if a == nil {
		r.t.Fatal("the TCP listener is not up")
	}
	return "http://" + a.String() + "/api/v1"
}

func (r *rig) token() string {
	r.t.Helper()
	return strings.TrimSpace(r.read("etc/localai/agent-token"))
}

// reply is an HTTP answer.
type reply struct {
	status int
	header http.Header
	body   []byte
}

func (rp reply) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(rp.body, &m)
	return m
}

// do calls the API over TCP with the agent token (an operator).
func (r *rig) do(method, path string, body any) reply {
	r.t.Helper()
	return r.doAuth(method, path, body, true)
}

func (r *rig) doAuth(method, path string, body any, auth bool) reply {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.base()+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+r.token())
	}
	cl := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cl.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: b}
}

// errCode is the doctor code of the error envelope of an answer.
func errCode(rp reply) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rp.body, &e)
	return e.Error.Code
}

// hasAlert reports whether the alert manager holds an alert of code.
func hasAlert(d *Daemon, code string) bool {
	for _, a := range d.Components().Doctor.Active() {
		if a.Code == code {
			return true
		}
	}
	return false
}
