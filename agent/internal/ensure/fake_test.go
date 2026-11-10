package ensure

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	frozen bool // golden files need the same time every time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.frozen {
		c.t = c.t.Add(time.Second) // every reading moves on, so durations are never zero
	}
	return c.t
}

type fakeMachine struct {
	mu      sync.Mutex
	tools   map[string]string
	missing []string // packages dpkg does not have
	pkgErr  error
	whisper bool
	active  map[string]bool
	asked   int
}

func (m *fakeMachine) ToolVersion(tool string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tools[tool]
}
func (m *fakeMachine) MissingPackages(_ context.Context, names []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked++
	if m.pkgErr != nil {
		return nil, m.pkgErr
	}
	var out []string
	for _, n := range names {
		for _, x := range m.missing {
			if x == n {
				out = append(out, n)
			}
		}
	}
	return out, nil
}
func (m *fakeMachine) WhisperInstalled() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.whisper }
func (m *fakeMachine) ServiceActive(_ context.Context, s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[s]
}

type toolOffer struct {
	version string
	err     error
}

type fakeKit struct {
	mu          sync.Mutex
	tools       map[string]toolOffer
	pkgs        map[string]bool
	pkgErr      error
	prebuilt    string
	prebuiltErr error
	src         bool
	srcErr      error
	root        string
}

func (k *fakeKit) src0() Source { return Source{Root: k.root, KitDir: filepath.Join(k.root, "kit")} }
func (k *fakeKit) Tool(_ context.Context, tool string) (Source, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	o, ok := k.tools[tool]
	if !ok {
		return Source{}, "", ErrKitMissing
	}
	if o.err != nil {
		return Source{}, "", o.err
	}
	return k.src0(), o.version, nil
}
func (k *fakeKit) Packages(context.Context) (Source, map[string]bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pkgErr != nil {
		return Source{}, nil, k.pkgErr
	}
	return k.src0(), k.pkgs, nil
}
func (k *fakeKit) WhisperPrebuilt(context.Context, WhisperWant) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.prebuiltErr != nil {
		return "", k.prebuiltErr
	}
	return k.prebuilt, nil
}
func (k *fakeKit) WhisperSource(context.Context, WhisperWant) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.src, k.srcErr
}

// fakeInstaller records what it was asked, in the order it was asked, and changes the fake machine the way
// the real install would.
type fakeInstaller struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error // by call prefix: "tool:pmtiles", "packages", "restore", "build", "restart"
	// block makes the call wait for the context (a cancelled pass) when its prefix is in the set.
	block   map[string]bool
	started chan string
	m       *fakeMachine
	kit     *fakeKit
}

func (f *fakeInstaller) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}
func (f *fakeInstaller) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *fakeInstaller) gate(ctx context.Context, key string) error {
	if f.started != nil {
		select {
		case f.started <- key:
		default:
		}
	}
	if f.block[key] {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.fail[key]
}
func (f *fakeInstaller) InstallTool(ctx context.Context, kitDir, tool string) (ToolOutcome, error) {
	f.record(fmt.Sprintf("tool:%s:%s", tool, kitDir))
	if err := f.gate(ctx, "tool:"+tool); err != nil {
		return ToolOutcome{}, err
	}
	prev := f.m.tools[tool]
	v := f.kit.tools[tool].version
	f.m.tools[tool] = v
	return ToolOutcome{Version: v, Previous: prev}, nil
}
func (f *fakeInstaller) InstallPackages(ctx context.Context, kitDir string, pkgs []string) (PackagesOutcome, error) {
	sorted := append([]string(nil), pkgs...)
	sort.Strings(sorted)
	f.record(fmt.Sprintf("packages:%s:%s", kitDir, strings.Join(sorted, ",")))
	if err := f.gate(ctx, "packages"); err != nil {
		return PackagesOutcome{}, err
	}
	f.m.missing = nil
	return PackagesOutcome{Installed: pkgs}, nil
}
func (f *fakeInstaller) RestoreWhisper(ctx context.Context, file string) error {
	f.record("restore:" + file)
	if err := f.gate(ctx, "restore"); err != nil {
		return err
	}
	f.m.whisper = true
	return nil
}
func (f *fakeInstaller) BuildWhisper(ctx context.Context) error {
	f.record("build")
	if err := f.gate(ctx, "build"); err != nil {
		return err
	}
	f.m.whisper = true
	return nil
}
func (f *fakeInstaller) RestartService(ctx context.Context, s string) error {
	f.record("restart:" + s)
	return f.gate(ctx, "restart")
}

type fakeAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *fakeAudit) Write(e audit.Event) error {
	a.mu.Lock()
	a.events = append(a.events, e)
	a.mu.Unlock()
	return nil
}
func (a *fakeAudit) Events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.events...)
}

type alertLog struct {
	mu    sync.Mutex
	state map[string]bool
	calls []string
}

func (a *alertLog) Alert(code string, raise bool, _ map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == nil {
		a.state = map[string]bool{}
	}
	a.state[code] = raise
	a.calls = append(a.calls, fmt.Sprintf("%s=%v", code, raise))
}
func (a *alertLog) Raised(code string) bool { a.mu.Lock(); defer a.mu.Unlock(); return a.state[code] }

// rig is a node with everything fake.
type rig struct {
	t       *testing.T
	clock   *fakeClock
	machine *fakeMachine
	kit     *fakeKit
	inst    *fakeInstaller
	audit   *fakeAudit
	alerts  *alertLog
	journal *Journal
	wants   Wants
	auto    Auto
	gate    error
	svc     *Service
}

// newRig builds a node that wants the three tools, the packages and the whisper program, and has none of
// them, with a kit that holds all of it. A test changes what it needs and calls build().
func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clock: newClock(), audit: &fakeAudit{}, alerts: &alertLog{}, auto: AutoOn}
	r.machine = &fakeMachine{tools: map[string]string{}, missing: []string{"cmake", "git", "curl"}, active: map[string]bool{}}
	r.kit = &fakeKit{root: "/srv/localai",
		tools:    map[string]toolOffer{kit.ToolKiwix: {version: "3.7.0"}, kit.ToolPmtiles: {version: "1.22"}, kit.ToolPiper: {version: "2023.11.14-2"}},
		pkgs:     map[string]bool{"cmake": true, "git": true, "curl": true, "build-essential": true},
		prebuilt: "/srv/localai/kit/prebuilt/whisper.tar.zst",
	}
	r.inst = &fakeInstaller{fail: map[string]error{}, block: map[string]bool{}, m: r.machine, kit: r.kit}
	r.wants = Wants{
		Tools:     map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: true, kit.ToolPiper: true},
		Packages:  []string{"cmake", "git", "curl", "build-essential"},
		Essential: []string{"cmake", "git", "build-essential"},
		Whisper:   true, WhisperWant: WhisperWant{Backend: "cpu"},
	}
	return r
}

func (r *rig) build() *rig {
	r.t.Helper()
	j, _, err := OpenJournal(nil, filepath.Join(r.t.TempDir(), "ensure", JournalFile), r.clock.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	r.journal = j
	r.svc = r.service(j)
	return r
}

func (r *rig) service(j *Journal) *Service {
	return New(Deps{
		Settings: func() Settings { return Settings{Auto: r.auto} },
		Wants:    func(context.Context) (Wants, error) { return r.wants, nil },
		Planner:  &Planner{Machine: r.machine, Kit: r.kit, Clock: r.clock},
		Executor: &Executor{Installer: r.inst, Kit: r.kit, Audit: r.audit, Clock: r.clock,
			WhisperWant: func() WhisperWant { return r.wants.WhisperWant }},
		Journal: j,
		Gate:    func() error { return r.gate },
		Alert:   r.alerts.Alert,
		Audit:   r.audit, Clock: r.clock,
		NewRunID: func() string { return "ens-test" },
		PlanTTL:  time.Nanosecond,
		Retry:    5 * time.Millisecond, MaxRetries: 3,
	})
}

func (r *rig) planner() *Planner { return &Planner{Machine: r.machine, Kit: r.kit, Clock: r.clock} }

func stepOf(t *testing.T, p Plan, id string) Step {
	t.Helper()
	s, ok := p.Step(id)
	if !ok {
		t.Fatalf("no step %s in %v", id, ids(p))
	}
	return s
}

func ids(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.ID+"="+string(s.Status))
	}
	return out
}
