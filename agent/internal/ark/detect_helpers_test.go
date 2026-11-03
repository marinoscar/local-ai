package ark_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// ---- the fakes the detector runs on --------------------------------------------------------------

// fakeMounts is the mount table: a list the test sets, and a Changes channel the test signals.
type fakeMounts struct {
	mu      sync.Mutex
	entries []ark.MountEntry
	err     error
	reads   int
	changes chan struct{}
}

func newFakeMounts() *fakeMounts { return &fakeMounts{changes: make(chan struct{}, 64)} }

func (f *fakeMounts) Read() ([]ark.MountEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return append([]ark.MountEntry(nil), f.entries...), f.err
}

func (f *fakeMounts) Changes(context.Context) <-chan struct{} { return f.changes }

func (f *fakeMounts) set(entries ...ark.MountEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries, f.err = entries, nil
}

func (f *fakeMounts) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries, f.err = nil, err
}

func (f *fakeMounts) signal() {
	select {
	case f.changes <- struct{}{}:
	default:
	}
}

func (f *fakeMounts) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// fakeBlock is blkid: tags per device, scripted failures, and a record of every call.
type fakeBlock struct {
	mu       sync.Mutex
	info     map[string]ark.BlockInfo
	probeErr map[string]error
	labelled []string
	findErr  error
	probes   []string
	finds    int
}

func newFakeBlock() *fakeBlock {
	return &fakeBlock{info: map[string]ark.BlockInfo{}, probeErr: map[string]error{}}
}

func (f *fakeBlock) disk(device, label, uuid, typ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info[device] = ark.BlockInfo{Device: device, Label: label, UUID: uuid, Type: typ}
}

func (f *fakeBlock) Probe(_ context.Context, device string) (ark.BlockInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, device)
	if err := f.probeErr[device]; err != nil {
		return ark.BlockInfo{}, err
	}
	b, ok := f.info[device]
	if !ok {
		return ark.BlockInfo{Device: device}, nil
	}
	return b, nil
}

func (f *fakeBlock) FindByLabel(_ context.Context, label string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds++
	if f.findErr != nil {
		return nil, f.findErr
	}
	return append([]string(nil), f.labelled...), nil
}

func (f *fakeBlock) probeCount(device string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.probes {
		if p == device {
			n++
		}
	}
	return n
}

func (f *fakeBlock) findCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finds
}

// fakeErrors is sysfs errors_count per source device.
type fakeErrors struct {
	mu sync.Mutex
	n  map[string]int
}

func (f *fakeErrors) set(source string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = map[string]int{}
	}
	f.n[source] = n
}

func (f *fakeErrors) ErrorsCount(source, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[source], nil
}

// trace is the order in which the detector told the world things.
type trace struct {
	mu sync.Mutex
	ev []string
}

func (t *trace) add(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ev = append(t.ev, fmt.Sprintf(format, args...))
}

func (t *trace) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.ev...)
}

func (t *trace) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ev = nil
}

// fakeBus records what the detector publishes.
type fakeBus struct {
	tr      *trace
	mu      sync.Mutex
	mounted []events.DriveMounted
	gone    []events.DriveUnmounted
}

func (b *fakeBus) Publish(typ, opID string, data any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch d := data.(type) {
	case events.DriveMounted:
		b.mounted = append(b.mounted, d)
		b.tr.add("event mounted %s %s ro=%v", d.RootID, d.State, d.ReadOnly)
	case events.DriveUnmounted:
		b.gone = append(b.gone, d)
		b.tr.add("event unmounted %s safe=%v", d.RootID, d.SafeToUnplug)
	default:
		return fmt.Errorf("unexpected %T for %s", data, typ)
	}
	return nil
}

func (b *fakeBus) counts() (mounted, gone int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.mounted), len(b.gone)
}

// fakeAlerts records LAI-ARK-007.
type fakeAlerts struct {
	tr      *trace
	mu      sync.Mutex
	raised  []map[string]any
	cleared int
	active  bool
}

func (a *fakeAlerts) Raise(code string, details map[string]any, _ ...doctor.RaiseOption) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if code != doctor.ARK007 {
		return fmt.Errorf("unexpected alert %s", code)
	}
	a.raised = append(a.raised, details)
	a.active = true
	a.tr.add("alert raised %v", details["root_ids"])
	return nil
}

func (a *fakeAlerts) Clear(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if code != doctor.ARK007 {
		return
	}
	a.cleared++
	if a.active {
		a.active = false
		a.tr.add("alert cleared")
	}
}

func (a *fakeAlerts) isActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// drvFS is the ark.FS of the detector tests: the real temp directory, a record of every path the
// detector looked at, and scripted failures of OpenFile for the write probe.
type drvFS struct {
	ark.FS
	mu      sync.Mutex
	seen    []string
	openErr error // OpenFile of a ".probe" path fails with this
	probes  int   // OpenFile calls on a ".probe" path
}

func (f *drvFS) note(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, name)
}

func (f *drvFS) touched(under string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.seen {
		if p == under || strings.HasPrefix(p, under+string(filepath.Separator)) {
			out = append(out, p)
		}
	}
	return out
}

func (f *drvFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	f.note(name)
	if filepath.Base(name) == ".probe" {
		f.mu.Lock()
		f.probes++
		err := f.openErr
		f.mu.Unlock()
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: name, Err: err}
		}
	}
	return f.FS.OpenFile(name, flag, perm)
}

func (f *drvFS) Stat(name string) (os.FileInfo, error)      { f.note(name); return f.FS.Stat(name) }
func (f *drvFS) Lstat(name string) (os.FileInfo, error)     { f.note(name); return f.FS.Lstat(name) }
func (f *drvFS) ReadFile(name string) ([]byte, error)       { f.note(name); return f.FS.ReadFile(name) }
func (f *drvFS) ReadDir(name string) ([]os.DirEntry, error) { f.note(name); return f.FS.ReadDir(name) }

func (f *drvFS) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

// ---- the rig ----------------------------------------------------------------------------------------

type rig struct {
	t      *testing.T
	tmp    string
	mounts *fakeMounts
	block  *fakeBlock
	errs   *fakeErrors
	fs     *drvFS
	clock  *fake.Clock
	tr     *trace
	bus    *fakeBus
	alerts *fakeAlerts
	m      *ark.Manager

	mu     sync.Mutex
	active [][]string // the root ids of each OnActive call
	hooked []string
}

type rigOpts func(*ark.Options)

func newRig(t *testing.T, opts ...rigOpts) *rig {
	t.Helper()
	tr := &trace{}
	r := &rig{
		t: t, tmp: t.TempDir(), mounts: newFakeMounts(), block: newFakeBlock(), errs: &fakeErrors{},
		fs: &drvFS{FS: newFS()}, clock: fake.NewClock(epoch), tr: tr,
		bus: &fakeBus{tr: tr}, alerts: &fakeAlerts{tr: tr},
	}
	o := ark.Options{
		Mounts: r.mounts, Block: r.block, Errors: r.errs, FS: r.fs, Clock: r.clock,
		Statfs: func(string) (int64, int64, error) { return 2000, 800, nil },
		Events: r.bus, Alerts: r.alerts, Debounce: -1,
		Drive: ark.DriveOptions{NodeID: "node-a", AgentVersion: "0.0.0-test"},
		OnActive: func(roots []ark.Root) {
			var ids []string
			for _, rt := range roots {
				ids = append(ids, rt.ID)
			}
			r.mu.Lock()
			r.active = append(r.active, ids)
			r.mu.Unlock()
			tr.add("active %v", ids)
		},
	}
	for _, f := range opts {
		f(&o)
	}
	m, err := ark.NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	m.OnRemoved(func(id string) {
		r.mu.Lock()
		r.hooked = append(r.hooked, id)
		r.mu.Unlock()
		tr.add("hook %s", id)
	})
	r.m = m
	return r
}

// dir makes a mount point directory under the rig's temp directory.
func (r *rig) dir(name string) string {
	r.t.Helper()
	p := filepath.Join(r.tmp, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// entry builds a MountEntry through the real parser. opts are the mount options and the super
// options; the defaults are "rw,relatime" and "rw,errors=remount-ro".
func entry(t testing.TB, id int, devnum, mount, fstype, source string, opts ...string) ark.MountEntry {
	t.Helper()
	mo, so := "rw,relatime", "rw,errors=remount-ro"
	if len(opts) > 0 {
		mo = opts[0]
	}
	if len(opts) > 1 {
		so = opts[1]
	}
	line := fmt.Sprintf("%d 29 %s / %s %s shared:%d - %s %s %s\n", id, devnum, escape(mount), mo, id, fstype, source, so)
	es, err := ark.ParseMountInfo(strings.NewReader(line))
	if err != nil || len(es) != 1 {
		t.Fatalf("bad test mountinfo line %q: %v", line, err)
	}
	return es[0]
}

func escape(p string) string { return strings.NewReplacer(" ", `\040`, "\t", `\011`).Replace(p) }

// rootDisk is the always-mounted system disk: a candidate that blkid says has no label.
func (r *rig) rootDisk() ark.MountEntry {
	r.block.disk("/dev/nvme0n1p2", "", "9d3a7c10-0000-4000-8000-000000000001", "ext4")
	return entry(r.t, 29, "259:2", "/", "ext4", "/dev/nvme0n1p2")
}

// arkDisk declares an AI-ARK disk to blkid and returns its mount entry.
func (r *rig) arkDisk(id int, devnum, mount, source, uuid, typ string, opts ...string) ark.MountEntry {
	r.block.disk(source, ark.ArkLabel, uuid, typ)
	return entry(r.t, id, devnum, mount, typ, source, opts...)
}

// initDrive lays out and initializes a drive under mount: <mount>/localai with the layout and a
// ROOT.json carrying profile (empty means the default).
func (r *rig) initDrive(mount, uuid, profile string) {
	r.t.Helper()
	root, err := ark.NewDriveRoot(ark.DriveInfo{Mount: mount, FSUUID: uuid}, profile)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := ark.CreateLayout(newFS(), root.Path, ark.LayoutOptions{}); err != nil {
		r.t.Fatal(err)
	}
	rj := ark.NewRootJSON(root, ark.CreatedBy{NodeID: "node-x", AgentVersion: "0.0.0"}, "AI-ARK", epoch)
	if err := ark.WriteRootJSON(filepath.Join(root.Path, ark.RootJSONName), rj); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) refresh() {
	r.t.Helper()
	if err := r.m.Refresh(context.Background()); err != nil {
		r.t.Fatalf("Refresh: %v", err)
	}
}

// only returns the one drive, failing if there is not exactly one.
func (r *rig) only() ark.Drive {
	r.t.Helper()
	ds := r.m.Drives()
	if len(ds) != 1 {
		r.t.Fatalf("drives = %d: %+v", len(ds), ds)
	}
	return ds[0]
}

func (r *rig) hookedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.hooked...)
}

func (r *rig) activeCalls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.active...)
}

// recv waits for a subscriber event or fails the test.
func recv(t *testing.T, ch <-chan ark.DriveEvent, d time.Duration) ark.DriveEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("the subscription was closed")
		}
		return ev
	case <-time.After(d):
		t.Fatalf("no drive event within %v", d)
		return ark.DriveEvent{}
	}
}

// quiet fails if an event arrives within d.
func quiet(t *testing.T, ch <-chan ark.DriveEvent, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected drive event %+v", ev)
	case <-time.After(d):
	}
}

func hasProblem(d ark.Drive, code string) bool {
	for _, p := range d.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

var _ = syscall.EROFS
