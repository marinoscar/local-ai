package build

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// testSHA is a full commit sha built at run time (no 40-hex literal in the source).
var testSHA = "f3586e66b" + strings.Repeat("a1", 15) + "c"

// testNow is read once and shared by the clock and by every expected value.
var testNow = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

const (
	testShort9 = "f3586e66b"
	testID     = "f3586e66b-cuda"
	oldID      = "0a1b2c3d4-cuda"
	testModel  = "m-9b"
)

// ---- clock -------------------------------------------------------------------------------

// manualClock never waits: Sleep advances it.
type manualClock struct {
	mu   sync.Mutex
	t    time.Time
	nap  int
	slep time.Duration
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.nap++
	c.slep += d
}

// ---- exec --------------------------------------------------------------------------------

type cmdFn func(c execx.Cmd) (res execx.Result, err error, handled bool)

// fakeExec records every command and answers through handlers (the newest first). A command no
// handler knows is an error, so a test fails loudly on an unexpected program.
type fakeExec struct {
	mu       sync.Mutex
	calls    []execx.Cmd
	handlers []cmdFn
}

func (f *fakeExec) on(h cmdFn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append([]cmdFn{h}, f.handlers...)
}

func (f *fakeExec) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	hs := append([]cmdFn(nil), f.handlers...)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return execx.Result{}, err
	}
	for _, h := range hs {
		if res, err, ok := h(c); ok {
			return res, err
		}
	}
	return execx.Result{}, fmt.Errorf("fake: unexpected command: %s %s", c.Path, strings.Join(c.Args, " "))
}

func (f *fakeExec) all() []execx.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execx.Cmd(nil), f.calls...)
}

// line renders a command as "<program> <args>" with the program as its base name.
func line(c execx.Cmd) string {
	return strings.TrimSpace(filepath.Base(c.Path) + " " + strings.Join(c.Args, " "))
}

func (f *fakeExec) lines() []string {
	var out []string
	for _, c := range f.all() {
		out = append(out, line(c))
	}
	return out
}

func (f *fakeExec) ran(prefix string) bool {
	for _, l := range f.lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func okOut(s string) execx.Result    { return execx.Result{Stdout: []byte(s)} }
func okErrOut(s string) execx.Result { return execx.Result{Stderr: []byte(s)} }
func exitWith(n int, errOut string) execx.Result {
	return execx.Result{ExitCode: n, Stderr: []byte(errOut)}
}

// ---- fakes of the seams ------------------------------------------------------------------

type fakeAgent struct {
	mu       sync.Mutex
	up       bool
	errs     []error // answers of RestartLlama, in order; nil once exhausted
	restarts int
	onCall   func(n int)
}

func (a *fakeAgent) Up(context.Context) bool { return a.up }

func (a *fakeAgent) RestartLlama(context.Context) error {
	a.mu.Lock()
	a.restarts++
	n := a.restarts
	var err error
	if len(a.errs) > 0 {
		err, a.errs = a.errs[0], a.errs[1:]
	}
	hook := a.onCall
	a.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return err
}

type fakeProbe struct {
	mu          sync.Mutex
	healthFn    func(n int) error
	def         string
	defErr      error
	statusFn    func(n int) (string, error)
	loadErr     error
	completeErr error
	nHealth     int
	nStatus     int
	loads       []string
	completions []int
}

func newProbe() *fakeProbe { return &fakeProbe{def: testModel} }

func (p *fakeProbe) Health(context.Context) error {
	p.mu.Lock()
	p.nHealth++
	n, fn := p.nHealth, p.healthFn
	p.mu.Unlock()
	if fn != nil {
		return fn(n)
	}
	return nil
}

func (p *fakeProbe) DefaultModel(context.Context) (string, error) { return p.def, p.defErr }

func (p *fakeProbe) ModelStatus(context.Context, string) (string, error) {
	p.mu.Lock()
	p.nStatus++
	n, fn := p.nStatus, p.statusFn
	p.mu.Unlock()
	if fn != nil {
		return fn(n)
	}
	return StatusLoaded, nil
}

func (p *fakeProbe) LoadModel(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loads = append(p.loads, id)
	return p.loadErr
}

func (p *fakeProbe) Complete(_ context.Context, _ string, maxTokens int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completions = append(p.completions, maxTokens)
	return p.completeErr
}

type auditRec struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *auditRec) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}

type recRec struct {
	ids []string
	err error
}

func (r *recRec) RecordBuild(id string) error { r.ids = append(r.ids, id); return r.err }

// ---- ELF fixtures ------------------------------------------------------------------------

// elfFixture builds a minimal 64-bit ELF with an optional dynamic section. dynamic false gives a
// static program (no .dynamic); otherwise runpath and rpath, when not empty, become DT_RUNPATH and
// DT_RPATH.
func elfFixture(dynamic bool, runpath, rpath string) []byte {
	const (
		ehsize  = 64
		shentsz = 64
	)
	var dynstr bytes.Buffer
	dynstr.WriteByte(0)
	addStr := func(s string) uint64 {
		off := uint64(dynstr.Len())
		dynstr.WriteString(s)
		dynstr.WriteByte(0)
		return off
	}
	var dyn bytes.Buffer
	putDyn := func(tag, val uint64) {
		_ = binary.Write(&dyn, binary.LittleEndian, tag)
		_ = binary.Write(&dyn, binary.LittleEndian, val)
	}
	if dynamic {
		if runpath != "" {
			putDyn(29, addStr(runpath)) // DT_RUNPATH
		}
		if rpath != "" {
			putDyn(15, addStr(rpath)) // DT_RPATH
		}
		putDyn(0, 0)
	}

	var shstr bytes.Buffer
	shstr.WriteByte(0)
	name := func(s string) uint32 {
		off := uint32(shstr.Len())
		shstr.WriteString(s)
		shstr.WriteByte(0)
		return off
	}
	nDynstr, nDynamic, nShstr := name(".dynstr"), name(".dynamic"), name(".shstrtab")

	type sec struct {
		name    uint32
		typ     uint32
		off     uint64
		size    uint64
		link    uint32
		entsize uint64
	}
	var data bytes.Buffer
	secs := []sec{{}}
	place := func(b []byte) uint64 {
		off := uint64(ehsize + data.Len())
		data.Write(b)
		return off
	}
	if dynamic {
		secs = append(secs, sec{name: nDynstr, typ: 3, off: place(dynstr.Bytes()), size: uint64(dynstr.Len())})
		secs = append(secs, sec{name: nDynamic, typ: 6, off: place(dyn.Bytes()), size: uint64(dyn.Len()), link: 1, entsize: 16})
	}
	shstrIdx := len(secs)
	secs = append(secs, sec{name: nShstr, typ: 3, off: place(shstr.Bytes()), size: uint64(shstr.Len())})

	shoff := uint64(ehsize + data.Len())
	var out bytes.Buffer
	ident := [16]byte{0x7f, 'E', 'L', 'F', 2, 1, 1}
	out.Write(ident[:])
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(2))  // ET_EXEC
	w(uint16(62)) // EM_X86_64
	w(uint32(1))
	w(uint64(0)) // entry
	w(uint64(0)) // phoff
	w(shoff)
	w(uint32(0))
	w(uint16(ehsize))
	w(uint16(56))
	w(uint16(0)) // phnum
	w(uint16(shentsz))
	w(uint16(len(secs)))
	w(uint16(shstrIdx))
	out.Write(data.Bytes())
	for _, s := range secs {
		w(s.name)
		w(s.typ)
		w(uint64(0)) // flags
		w(uint64(0)) // addr
		w(s.off)
		w(s.size)
		w(s.link)
		w(uint32(0)) // info
		w(uint64(1)) // addralign
		w(s.entsize)
	}
	return out.Bytes()
}

// ---- the harness -------------------------------------------------------------------------

// harness is a node in a temporary directory with every program faked.
type harness struct {
	t    *testing.T
	root string
	exec *fakeExec
	clk  *manualClock
	m    *Manager

	agent  *fakeAgent
	probe  *fakeProbe
	audit  *auditRec
	rec    *recRec
	prevID string

	// What the fake programs answer.
	nvidiaOut    string
	configure    execx.Result
	buildRes     execx.Result
	buildErr     error
	skipBinaries bool
	elf          []byte
	version      string
	devices      string
	versionRes   *execx.Result
	devicesRes   *execx.Result
	systemctl    execx.Result
	tools        map[string]bool

	linkAtRestart []string
	opts          Options
	req           Request
}

type hopt func(*harness)

func withoutPrevious() hopt { return func(h *harness) { h.prevID = "" } }
func withoutAgent() hopt    { return func(h *harness) { h.agent.up = false } }
func withoutSource() hopt {
	return func(h *harness) { _ = os.RemoveAll(filepath.Join(h.root, "src", testSHA)) }
}
func withBackend(b string) hopt {
	return func(h *harness) { h.req.Backend = b }
}

// newHarness builds the node: a previous build in use, the source tree already extracted, a
// healthy router, an agent that is up. Options change it before the Manager is made.
func newHarness(t *testing.T, opts ...hopt) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{
		t: t, root: root, exec: &fakeExec{}, clk: &manualClock{t: testNow},
		agent: &fakeAgent{up: true}, probe: newProbe(), audit: &auditRec{}, rec: &recRec{},
		prevID:    oldID,
		nvidiaOut: "7.5\n",
		version:   "version: 6000 (f3586e6)\nbuilt with cc (Debian 15.2.0-1) 15.2.0 for x86_64-linux-gnu\n",
		devices:   "Available devices:\n  CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design (8192 MiB, 7013 MiB free)\n",
		elf:       elfFixture(true, "", ""),
		tools:     map[string]bool{"cmake": true, "ninja": true, "cc": true, "c++": true, "nvcc": true, "glslc": true, "git": true, "nvidia-smi": true, "systemctl": true},
		req:       Request{Backend: BackendCUDA},
	}
	if err := os.MkdirAll(filepath.Join(root, "src", testSHA), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(root, "src", testSHA, "CMakeLists.txt"), "project(llama)\n")
	for _, o := range opts {
		o(h)
	}
	if h.prevID != "" {
		h.makeBuild(h.prevID, testNow.Add(-48*time.Hour))
		if err := os.Symlink("builds/"+h.prevID, filepath.Join(root, "current")); err != nil {
			t.Fatal(err)
		}
	}
	h.exec.on(h.world)
	h.agent.onCall = func(int) {
		target, _ := os.Readlink(filepath.Join(h.root, "current"))
		h.linkAtRestart = append(h.linkAtRestart, target)
	}
	h.opts = Options{
		Root:    root,
		BuiltBy: "aiagent 0.1.0",
		Node:    func() config.LlamaBlock { return config.LlamaBlock{Commit: testSHA, Backend: BackendCUDA} },
		Exec:    h.exec,
		Lookup: func(name string) (string, error) {
			if h.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", execx.ErrNotFound
		},
		Agent: h.agent, Probe: h.probe, Recorder: h.rec, Audit: h.audit,
		Clock: h.clk.Now, Sleep: h.clk.Sleep,
		FreeBytes:       func(string) (uint64, error) { return 10 << 30, nil },
		MemAvailableMiB: func() (int64, error) { return 10240, nil },
		NumCPU:          func() int { return 8 },
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

// manager makes (or remakes) the Manager from the current options.
func (h *harness) manager() *Manager {
	h.m = New(h.opts)
	return h.m
}

func (h *harness) build() (Result, error) {
	h.t.Helper()
	if h.m == nil {
		h.manager()
	}
	return h.m.Build(context.Background(), h.req)
}

func (h *harness) write(path, content string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// makeBuild creates builds/<id> with a BUILD.json built at the given time.
func (h *harness) makeBuild(id string, at time.Time) {
	h.t.Helper()
	dir := filepath.Join(h.root, "builds", id)
	h.write(filepath.Join(dir, "bin", "llama-server"), "x")
	h.write(filepath.Join(dir, "BUILD.json"), fmt.Sprintf(`{"build_id": %q, "built_at": %q}`, id, at.UTC().Format(time.RFC3339)))
}

func (h *harness) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(h.root, rel))
	return err == nil
}

func (h *harness) link() string {
	target, err := os.Readlink(filepath.Join(h.root, "current"))
	if err != nil {
		return ""
	}
	return target
}

// world answers the programs of a build.
func (h *harness) world(c execx.Cmd) (execx.Result, error, bool) {
	base := filepath.Base(c.Path)
	arg := func(i int) string {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return ""
	}
	switch base {
	case "nvidia-smi":
		return okOut(h.nvidiaOut), nil, true
	case "cmake":
		switch arg(0) {
		case "-S":
			return h.configure, nil, true
		case "--build":
			if h.buildErr != nil {
				return h.buildRes, h.buildErr, true
			}
			if h.buildRes.ExitCode == 0 && !h.skipBinaries {
				for _, n := range binaries {
					p := filepath.Join(arg(1), "bin", n)
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						return execx.Result{}, err, true
					}
					if err := os.WriteFile(p, append(append([]byte{}, h.elf...), []byte(n)...), 0o755); err != nil {
						return execx.Result{}, err, true
					}
				}
			}
			return h.buildRes, nil, true
		}
	case "cc":
		return okOut("cc (Debian 15.2.0-1) 15.2.0\nCopyright (C) 2025 Free Software Foundation, Inc.\n"), nil, true
	case "nvcc":
		return okOut("nvcc: NVIDIA (R) Cuda compiler driver\nCuda compilation tools, release 12.4, V12.4.131\nBuild cuda_12.4.r12.4/compiler.34097967_0\n"), nil, true
	case "llama-server":
		switch arg(0) {
		case "--version":
			if h.versionRes != nil {
				return *h.versionRes, nil, true
			}
			return okErrOut(h.version), nil, true
		case "--list-devices":
			if h.devicesRes != nil {
				return *h.devicesRes, nil, true
			}
			return okErrOut(h.devices), nil, true
		}
	case "systemctl":
		return h.systemctl, nil, true
	}
	return execx.Result{}, nil, false
}

// fixture helpers shared by several files.

func mustErr(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *build.Error, got %T: %v", err, err)
	}
	return e
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
