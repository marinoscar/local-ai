package ark

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// This file is the drive detector (ark-sync.md section 4.1.2 and 4.1.4): it watches the mount table,
// finds the AI-ARK drives, classifies each one, keeps the active set of roots and says when a drive
// came or went. Everything it touches is behind an interface of this package (MountSource,
// BlockProber, ErrorCounter, FS, StatfsFunc, EventPublisher, AlertSink), so the tests run it on
// fixtures with a fake clock and no root.
//
// The detector never mounts, formats or repairs anything: it reads the mount table, runs blkid, reads
// sysfs and the drive's ROOT.json, and writes only what OpenDriveRoot writes (a recreated ROOT.json
// on a writable drive that has content) and a one-file write probe.

// Defaults of Options.
const (
	// DefaultScanInterval is the fallback re-read of the mount table and the scan for unmounted
	// drives (ark-sync.md section 4.1.2 steps 1 and 5).
	DefaultScanInterval = 30 * time.Second
	// DefaultDebounce is how long the detector waits after a mount-table change before it reads the
	// table, so the burst a desktop automounter causes is one pass.
	DefaultDebounce = 500 * time.Millisecond
	// DefaultLostTTL is how long LAI-ARK-007 stays raised after a surprise removal when the drive
	// does not come back.
	DefaultLostTTL = 24 * time.Hour
	// DefaultEjectTimeout is how long a drive may stay "ejecting" with no FinishEject or AbortEject.
	DefaultEjectTimeout = 10 * time.Minute

	probeName = ".probe"
	stagingNm = ".staging"
)

// MountSource is the mount table. ProcMounts is the real one.
type MountSource interface {
	// Read returns the current mount table. When the error is not nil but entries came back, the
	// error only reports skipped lines and the entries are valid.
	Read() ([]MountEntry, error)
	// Changes signals every change of the mount table (poll(2) POLLPRI|POLLERR on mountinfo). It
	// delivers a value per change, may coalesce a burst, and is closed when ctx is done (or, for a
	// source that cannot be watched, at once; the detector then runs on its timer alone).
	Changes(ctx context.Context) <-chan struct{}
}

// StatfsFunc returns the size and the free space (for an unprivileged writer) of the filesystem that
// holds path. OSStatfs is the real one.
type StatfsFunc func(path string) (size, free int64, err error)

// EventPublisher is the part of the event bus the detector uses: *events.Bus implements it.
type EventPublisher interface {
	Publish(eventType, opID string, data any) error
}

// AlertSink raises and clears the alert that is not derived from a state: LAI-ARK-007. *doctor.Manager
// implements it.
type AlertSink interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
	Clear(code string)
}

// DriveEvent is what Subscribe delivers: the same facts as the drive.mounted and drive.unmounted
// events on the bus (ark-sync.md section 3.6).
type DriveEvent struct {
	// Type is events.TypeDriveMounted or events.TypeDriveUnmounted.
	Type   string
	RootID string
	// Mount, State and ReadOnly are set for drive.mounted.
	Mount    string
	State    DriveState
	ReadOnly bool
	// SafeToUnplug is set for drive.unmounted: false means a surprise removal.
	SafeToUnplug bool
}

// Detector is what the rest of the agent (the sync engine, the API, the eject, init and mount
// routes) sees of the drive layer. *Manager implements it.
type Detector interface {
	// Drives returns every AI-ARK drive known now: the mounted ones in mount id order, then root id
	// (the best-copy order), then the plugged-in ones that are not mounted. The caller owns the
	// slice.
	Drives() []Drive
	// Drive returns the drive with this root id. When two drives share one (a clone), it is the
	// one that is in use (the earlier mount).
	Drive(rootID string) (Drive, bool)
	// ActiveRoots returns the roots of the drives that are mounted, initialized and usable (ready,
	// read_only, unclean), in best-copy order. An ejecting drive is not in it.
	ActiveRoots() []Root
	// Subscribe returns a channel of drive events. Delivery never blocks the detector: a subscriber
	// that does not keep up loses events (the channel holds 32). It is closed when the detector
	// stops.
	Subscribe() <-chan DriveEvent
	// OnRemoved registers a hook called with the root id of a drive that left the active set,
	// before the drive.unmounted event is published: the place to cancel transfers that write to it.
	// Hooks run on the detector's goroutine and must return quickly.
	OnRemoved(fn func(rootID string))
	// Refresh re-reads the mount table and rescans for unmounted drives now (after a mount or an
	// init), and returns when the new state is published.
	Refresh(ctx context.Context) error
}

// Ejector is the part of the Manager the eject route (SYNC-03) drives. The eject sequence itself
// (ark-sync.md section 4.1.5) is not the detector's; it only keeps the state honest while it runs.
type Ejector interface {
	// BeginEject sets the drive to ejecting and takes it out of the active set, without an event.
	BeginEject(rootID string) error
	// AbortEject undoes BeginEject (the eject was refused or failed) and re-reads the drive.
	AbortEject(ctx context.Context, rootID string)
	// FinishEject records that the drive was unmounted on purpose: it leaves the state, hooks run,
	// and drive.unmounted is published with safe_to_unplug true. No LAI-ARK-007 is raised.
	FinishEject(ctx context.Context, rootID string)
}

// Errors of the Ejector.
var (
	// ErrNoSuchDrive: no active drive has this root id.
	ErrNoSuchDrive = errors.New("ark: no such drive")
	// ErrAlreadyEjecting: the drive is already being ejected.
	ErrAlreadyEjecting = errors.New("ark: the drive is already being ejected")
)

// Options configures a Manager. Mounts, Block, FS and Clock are required; the rest has defaults.
type Options struct {
	Mounts MountSource
	Block  BlockProber
	// Errors reads ext4's errors_count; nil means no errors are ever seen.
	Errors ErrorCounter
	FS     FS
	// Statfs defaults to OSStatfs.
	Statfs StatfsFunc
	Clock  config.Clock
	// Events receives drive.mounted and drive.unmounted; nil publishes nothing.
	Events EventPublisher
	// Alerts raises LAI-ARK-007; nil raises nothing.
	Alerts AlertSink
	Logger *slog.Logger

	// ScanInterval (default 30 s), Debounce (default 500 ms; negative means none), LostTTL (default
	// 24 h) and EjectTimeout (default 10 min).
	ScanInterval, Debounce, LostTTL, EjectTimeout time.Duration

	// Drive is passed to OpenDriveRoot: the default profile, the node id and agent version recorded in a
	// recreated ROOT.json. Its Now defaults to the Clock.
	Drive DriveOptions

	// OnActive, when set, receives the new list of active roots every time it changes, before the
	// events of that change are published (the app keeps ArkRoots with it). It runs on the
	// detector's goroutine.
	OnActive func(roots []Root)
}

// Manager is the Detector. Run it with Run (or Start and Stop as a worker).
type Manager struct {
	o   Options
	log *slog.Logger

	// refreshMu serialises refreshes and guards the fields below it.
	refreshMu sync.Mutex
	cache     map[string]BlockInfo // devnum|source -> blkid, for mounted candidates
	unmounted map[string]BlockInfo // device -> blkid, as of the last scan
	active    map[string]activeRec
	ejecting  map[string]ejectRec
	lost      map[string]time.Time
	probed    map[string]bool // mountid:devnum -> the write probe passed

	// mu guards the published snapshot.
	mu     sync.RWMutex
	drives []Drive
	roots  []Root

	subMu   sync.Mutex
	subs    []chan DriveEvent
	removed []func(string)
	closed  bool

	cancel context.CancelFunc
	done   chan struct{}
}

type activeRec struct {
	devNum   string
	mount    string
	state    DriveState
	readOnly bool
	root     Root
	drive    Drive
}

type ejectRec struct {
	at    time.Time
	drive Drive
}

var _ Detector = (*Manager)(nil)
var _ Ejector = (*Manager)(nil)

// NewManager checks the options and returns a Manager that has not read anything yet.
func NewManager(o Options) (*Manager, error) {
	var missing []string
	if o.Mounts == nil {
		missing = append(missing, "Mounts")
	}
	if o.Block == nil {
		missing = append(missing, "Block")
	}
	if o.FS == nil {
		missing = append(missing, "FS")
	}
	if o.Clock == nil {
		missing = append(missing, "Clock")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ark: the drive detector needs %v", missing)
	}
	if o.Statfs == nil {
		o.Statfs = OSStatfs
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ScanInterval <= 0 {
		o.ScanInterval = DefaultScanInterval
	}
	if o.Debounce == 0 {
		o.Debounce = DefaultDebounce
	}
	if o.LostTTL <= 0 {
		o.LostTTL = DefaultLostTTL
	}
	if o.EjectTimeout <= 0 {
		o.EjectTimeout = DefaultEjectTimeout
	}
	if o.Drive.Now == nil {
		o.Drive.Now = o.Clock.Now
	}
	if o.Drive.Logger == nil {
		o.Drive.Logger = o.Logger
	}
	return &Manager{
		o: o, log: o.Logger.With("component", "ark"),
		cache: map[string]BlockInfo{}, unmounted: map[string]BlockInfo{},
		active: map[string]activeRec{}, ejecting: map[string]ejectRec{},
		lost: map[string]time.Time{}, probed: map[string]bool{},
	}, nil
}

// ---- the Detector interface ----------------------------------------------------------------------

// Drives implements Detector.
func (m *Manager) Drives() []Drive {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Drive, len(m.drives))
	for i, d := range m.drives {
		out[i] = d.clone()
	}
	return out
}

// Drive implements Detector.
func (m *Manager) Drive(rootID string) (Drive, bool) {
	if rootID == "" {
		return Drive{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, d := range m.drives {
		if d.RootID == rootID {
			return d.clone(), true
		}
	}
	return Drive{}, false
}

// ActiveRoots implements Detector.
func (m *Manager) ActiveRoots() []Root {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Root(nil), m.roots...)
}

// Subscribe implements Detector.
func (m *Manager) Subscribe() <-chan DriveEvent {
	ch := make(chan DriveEvent, 32)
	m.subMu.Lock()
	defer m.subMu.Unlock()
	if m.closed {
		close(ch)
		return ch
	}
	m.subs = append(m.subs, ch)
	return ch
}

// OnRemoved implements Detector.
func (m *Manager) OnRemoved(fn func(rootID string)) {
	if fn == nil {
		return
	}
	m.subMu.Lock()
	m.removed = append(m.removed, fn)
	m.subMu.Unlock()
}

// Refresh implements Detector.
func (m *Manager) Refresh(ctx context.Context) error { return m.refresh(ctx, true) }

// ---- running -------------------------------------------------------------------------------------

// Name implements the app's Worker.
func (m *Manager) Name() string { return "ark-drives" }

// Start implements the app's Worker: it runs the detector in the background until Stop or ctx is
// done. The first pass runs before Start returns, so a drive that is already plugged in is known
// (and its drive.mounted published) by the time the listeners start.
func (m *Manager) Start(ctx context.Context) error {
	if m.cancel != nil {
		return errors.New("ark: the drive detector is already running")
	}
	ctx, m.cancel = context.WithCancel(ctx)
	m.done = make(chan struct{})
	ready := make(chan struct{})
	go func() {
		defer close(m.done)
		m.run(ctx, func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-ctx.Done():
	}
	return nil
}

// Stop implements the app's Worker.
func (m *Manager) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run does the first pass, then re-reads the mount table on every change (after the debounce) and
// every ScanInterval, until ctx is done. It closes the subscribers' channels when it returns.
func (m *Manager) Run(ctx context.Context) { m.run(ctx, nil) }

func (m *Manager) run(ctx context.Context, started func()) {
	defer m.closeSubs()
	// The ticker exists before the first pass, so a test that waits for that pass can advance a
	// manual clock without racing the ticker's creation.
	tick := m.o.Clock.NewTicker(m.o.ScanInterval)
	defer tick.Stop()
	changes := m.o.Mounts.Changes(ctx)

	if err := m.refresh(ctx, true); err != nil {
		m.log.Warn("the first drive scan failed", "error", err.Error())
	}
	if started != nil {
		started()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				changes = nil // not watchable: the timer is the only trigger
				continue
			}
			if !m.settle(ctx, changes) {
				return
			}
			if err := m.refresh(ctx, false); err != nil && ctx.Err() == nil {
				m.log.Warn("the drive scan after a mount change failed", "error", err.Error())
			}
		case <-tick.C():
			if err := m.refresh(ctx, true); err != nil && ctx.Err() == nil {
				m.log.Warn("the periodic drive scan failed", "error", err.Error())
			}
		}
	}
}

// settle waits for the mount table to stop changing: it returns after Debounce with no further
// signal. It reports false when ctx ended.
func (m *Manager) settle(ctx context.Context, changes <-chan struct{}) bool {
	if m.o.Debounce < 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(m.o.Debounce)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case _, ok := <-changes:
			if !ok {
				return ctx.Err() == nil
			}
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(m.o.Debounce)
		case <-t.C:
			return true
		}
	}
}

func (m *Manager) closeSubs() {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, ch := range m.subs {
		close(ch)
	}
	m.subs = nil
}

// ---- the pass ------------------------------------------------------------------------------------

type mountedArk struct {
	e MountEntry
	b BlockInfo
}

func (m *Manager) refresh(ctx context.Context, scan bool) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	return m.refreshLocked(ctx, scan)
}

func (m *Manager) refreshLocked(ctx context.Context, scan bool) error {
	entries, err := m.o.Mounts.Read()
	if err != nil {
		if len(entries) == 0 {
			return fmt.Errorf("read the mount table: %w", err)
		}
		m.log.Warn("some lines of the mount table were skipped", "error", err.Error())
	}
	now := m.o.Clock.Now()

	// 1. The mounted candidates, and blkid for each one not seen before (cached by device numbers and
	// source until the mount is gone).
	cands := Candidates(entries)
	live := make(map[string]bool, len(cands))
	var arks []mountedArk
	for _, e := range cands {
		key := e.DevNum() + "|" + e.Source
		live[key] = true
		b, ok := m.cache[key]
		if !ok {
			b, err = m.o.Block.Probe(ctx, e.Source)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Not cached: the next pass asks again. A mount whose label cannot be read is not
				// listed (it may be any disk), and the failure is logged.
				m.log.Warn("blkid could not read a mounted device", "device", e.Source, "mount", e.MountPoint, "error", err.Error())
				continue
			}
			m.cache[key] = b
		}
		if IsArkLabel(b.Label) {
			arks = append(arks, mountedArk{e: e, b: b})
		}
	}
	for k := range m.cache {
		if !live[k] {
			delete(m.cache, k)
		}
	}

	// 2. Classify the mounted ark drives, earliest mount first (so the earlier one of two clones is
	// the one in use).
	var drives []Drive
	roots := map[string]Root{}     // root id|devnum -> its Root, for the drives in the active set
	firstOf := map[string]string{} // root id -> devnum of the first drive that has it
	probedLive := map[string]bool{}
	for _, a := range arks {
		id := DriveRootID(a.b)
		dup := false
		if id != "" && a.e.FSType == "ext4" {
			if first, ok := firstOf[id]; ok && first != a.e.DevNum() {
				dup = true
			} else {
				firstOf[id] = a.e.DevNum()
			}
		}
		d, root := m.classifyMounted(ctx, a, dup, probedLive)
		if root != nil {
			roots[d.RootID+"|"+d.DevNum] = *root
		}
		drives = append(drives, d)
	}
	for k := range m.probed {
		if !probedLive[k] {
			delete(m.probed, k)
		}
	}
	sort.SliceStable(drives, func(i, j int) bool {
		if drives[i].MountID != drives[j].MountID {
			return drives[i].MountID < drives[j].MountID
		}
		return drives[i].RootID < drives[j].RootID
	})

	// 3. Plugged-in drives that are not mounted.
	mountedSrc := map[string]bool{}
	for _, e := range entries {
		if e.IsDevice() {
			mountedSrc[e.Source] = true
		}
	}
	if scan {
		devices, ferr := m.o.Block.FindByLabel(ctx, ArkLabel)
		switch {
		case ferr != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.log.Warn("the scan for unmounted AI-ARK drives failed; the last answer stays", "error", ferr.Error())
		default:
			next := map[string]BlockInfo{}
			for _, dev := range devices {
				if mountedSrc[dev] {
					continue
				}
				b, perr := m.o.Block.Probe(ctx, dev)
				if perr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					m.log.Warn("blkid could not read an unmounted AI-ARK device", "device", dev, "error", perr.Error())
					continue
				}
				next[dev] = b
			}
			m.unmounted = next
		}
	}
	var loose []string
	for dev := range m.unmounted {
		if !mountedSrc[dev] {
			loose = append(loose, dev)
		}
	}
	sort.Strings(loose)
	for _, dev := range loose {
		b := m.unmounted[dev]
		if !IsArkLabel(b.Label) {
			continue
		}
		v := ClassifyUnmounted(b)
		drives = append(drives, Drive{
			RootID: DriveRootID(b), Device: dev, FSType: b.Type, Label: b.Label, UUID: b.UUID,
			State: v.State, Problems: v.Problems,
		})
	}

	// 4. The active set, with the drives being ejected carried over.
	next := map[string]activeRec{}
	var ordered []string
	for i := range drives {
		d := &drives[i]
		if !d.Active() {
			continue
		}
		root := roots[d.RootID+"|"+d.DevNum]
		next[d.RootID] = activeRec{devNum: d.DevNum, mount: d.Mount, state: d.State, readOnly: d.ReadOnly, root: root, drive: *d}
		ordered = append(ordered, d.RootID)
	}
	for id, rec := range m.ejecting {
		if now.Sub(rec.at) > m.o.EjectTimeout {
			m.log.Warn("an eject never finished; the drive is treated as removed", "root", id)
			delete(m.ejecting, id)
			continue
		}
		prev, wasActive := m.active[id]
		if !wasActive {
			delete(m.ejecting, id)
			continue
		}
		found := false
		for i := range drives {
			if drives[i].RootID == id && drives[i].DevNum == prev.devNum {
				drives[i].State = StateEjecting
				drives[i].ReadOnly = true
				found = true
			}
		}
		if !found {
			gone := rec.drive
			gone.State, gone.ReadOnly = StateEjecting, true
			drives = append(drives, gone)
		}
		next[id] = prev
	}
	var activeRoots []Root
	for _, id := range ordered {
		if _, ej := m.ejecting[id]; ej {
			continue
		}
		activeRoots = append(activeRoots, next[id].root)
	}

	// 5. Publish the snapshot, then tell the world about the differences.
	prev := m.active
	m.active = next
	m.mu.Lock()
	m.drives, m.roots = drives, activeRoots
	m.mu.Unlock()

	m.announce(prev, next, mountedDevNums(cands), now)
	return nil
}

func mountedDevNums(cands []MountEntry) map[string]bool {
	out := make(map[string]bool, len(cands))
	for _, e := range cands {
		out[e.DevNum()] = true
	}
	return out
}

// classifyMounted runs all six checks of ark-sync.md section 4.1.2 on one mounted ark drive. The
// first four are Classify; the last two read the drive (never a drive Classify said not to touch).
// The Root is returned for a drive in the active set.
func (m *Manager) classifyMounted(ctx context.Context, a mountedArk, dup bool, probedLive map[string]bool) (Drive, *Root) {
	d := Drive{
		RootID: DriveRootID(a.b), Device: a.e.Source, Mount: a.e.MountPoint, FSType: a.b.Type, Label: a.b.Label,
		UUID: a.b.UUID, DevNum: a.e.DevNum(), MountID: a.e.MountID,
	}
	if d.FSType == "" {
		d.FSType = a.e.FSType
	}
	if size, free, err := m.o.Statfs(a.e.MountPoint); err == nil {
		d.SizeBytes, d.FreeBytes = size, free
	}

	errs := 0
	if a.b.Type == "ext4" && a.e.FSType == "ext4" && !dup && m.o.Errors != nil {
		n, err := m.o.Errors.ErrorsCount(a.e.Source, a.e.DevNum())
		switch {
		case err == nil:
			errs = n
		case errors.Is(err, ErrErrorsUnknown):
			m.log.Debug("no ext4 error counter for the drive; assuming none", "device", a.e.Source)
		default:
			m.log.Warn("the ext4 error counter could not be read", "device", a.e.Source, "error", err.Error())
		}
	}
	d.FSErrors = errs

	v := Classify(ClassifyInput{Entry: a.e, Block: a.b, FSErrors: errs, DuplicateUUID: dup})
	d.State, d.ReadOnly, d.Problems = v.State, v.ReadOnly, append([]Problem(nil), v.Problems...)
	if v.Never {
		return d, nil
	}

	// Check 5: initialized. OpenDriveRoot never creates a root; it only recreates a ROOT.json that
	// went missing from a drive that has content, and only when the drive is writable.
	info := DriveInfo{Mount: a.e.MountPoint, FSUUID: a.b.UUID, MountID: a.e.MountID, Label: a.b.Label, ReadOnly: v.ReadOnly}
	root, probs, err := OpenDriveRoot(m.o.FS, info, m.o.Drive)
	d.Problems = append(d.Problems, probs...)
	var rp *Root
	switch {
	case err != nil && root.ID == "":
		d.State, d.Detail = StateUnusable, err.Error()
		m.log.Warn("an AI-ARK drive has no usable id", "device", a.e.Source, "error", err.Error())
		return d, nil
	case err != nil && root.State == StateUnusable:
		// ROOT.json is damaged, of a newer schema, or unreadable. It is left alone.
		d.State, d.ReadOnly, d.Detail = StateUnusable, true, err.Error()
		m.log.Warn("the ROOT.json of an AI-ARK drive cannot be used", "root", root.ID, "mount", a.e.MountPoint, "error", err.Error())
		return d, nil
	case err != nil:
		// The recreated ROOT.json could not be written: serve the drive read-only.
		d.Initialized = true
		d.Profile = root.Profile
		if d.State == StateReady {
			d.State = StateReadOnly
		}
		d.ReadOnly = true
		if !d.HasProblem(CodeDriveReadOnly) {
			d.Problems = append(d.Problems, newProblem(CodeDriveReadOnly, m.driveDetails(d, "errno", errnoText(err))))
		}
	case root.State == StateUninitialized:
		if d.State == StateReady {
			d.State = StateUninitialized
		}
		return d, nil
	default:
		d.Initialized = true
		d.Profile = root.Profile
	}

	// Check 6: writable. Never on a drive that is read-only, unclean or uninitialized (the probe
	// would create localai/ on a drive nobody set up).
	if d.State == StateReady {
		key := fmt.Sprintf("%d:%s", a.e.MountID, a.e.DevNum())
		probedLive[key] = true
		if !m.probed[key] {
			if perr := m.probeWritable(root.Path); perr != nil {
				d.State, d.ReadOnly = StateReadOnly, true
				d.Problems = append(d.Problems, newProblem(CodeDriveReadOnly, m.driveDetails(d, "errno", errnoText(perr))))
				m.log.Warn("an AI-ARK drive cannot be written", "root", d.RootID, "mount", d.Mount, "error", perr.Error())
			} else {
				m.probed[key] = true
			}
		}
	}

	root.State = d.State
	root.ReadOnly = d.ReadOnly
	root.Unclean = v.Unclean
	rp = &root
	return d, rp
}

func (m *Manager) driveDetails(d Drive, kv ...any) map[string]any {
	out := map[string]any{"device": d.Device, "mount": d.Mount}
	if d.RootID != "" {
		out["root_id"] = d.RootID
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// probeWritable creates and removes <root>/.staging/.probe (ark-sync.md section 4.1.2, check 6). A
// root with no .staging directory is probed in the root directory itself, so a missing directory is
// not mistaken for a read-only drive. A full disk (ENOSPC, EDQUOT) is not a read-only drive: the
// drive can still be read and cleaned up, so it passes.
func (m *Manager) probeWritable(rootPath string) error {
	dir := filepath.Join(rootPath, stagingNm)
	if st, err := m.o.FS.Stat(dir); err != nil || !st.IsDir() {
		dir = rootPath
	}
	p := filepath.Join(dir, probeName)
	_ = m.o.FS.Remove(p) // a probe left by a crash
	f, err := m.o.FS.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if isFull(err) {
			return nil
		}
		return err
	}
	cerr := f.Close()
	if rerr := m.o.FS.Remove(p); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		m.log.Warn("the write probe file could not be removed", "path", p, "error", rerr.Error())
	}
	return cerr
}

// ---- events, hooks, alerts ------------------------------------------------------------------------

// announce compares the previous and the new active sets and emits what changed: removals first
// (hooks, alert, event), then arrivals and state changes. OnActive runs before any event.
func (m *Manager) announce(prev, next map[string]activeRec, mountedDev map[string]bool, now time.Time) {
	var removed, arrived []string
	for id, p := range prev {
		if n, ok := next[id]; !ok || n.devNum != p.devNum {
			removed = append(removed, id)
		}
	}
	for id, n := range next {
		if _, ej := m.ejecting[id]; ej {
			continue
		}
		p, ok := prev[id]
		if !ok || p.devNum != n.devNum || p.state != n.state || p.readOnly != n.readOnly || p.mount != n.mount {
			arrived = append(arrived, id)
		}
	}
	sort.Strings(removed)
	sort.Strings(arrived)

	if m.o.OnActive != nil && (len(removed) > 0 || len(arrived) > 0 || m.rootsDiffer(prev, next)) {
		m.mu.RLock()
		roots := append([]Root(nil), m.roots...)
		m.mu.RUnlock()
		m.o.OnActive(roots)
	}

	for _, id := range removed {
		p := prev[id]
		surprise := !mountedDev[p.devNum]
		m.callRemoved(id)
		if surprise {
			m.lost[id] = now
			m.log.Warn("an AI-ARK drive was removed without ejecting", "root", id, "mount", p.mount, "code", CodeDriveUnplugged)
		} else {
			m.log.Info("an AI-ARK drive left the active set while still mounted", "root", id, "mount", p.mount)
		}
		m.emit(DriveEvent{Type: events.TypeDriveUnmounted, RootID: id, SafeToUnplug: !surprise})
	}
	for _, id := range arrived {
		n := next[id]
		if n.state == StateReady {
			delete(m.lost, id) // the drive came back clean: its LAI-ARK-007 is over
		}
		m.log.Info("drive mounted", "root", id, "mount", n.mount, "state", string(n.state))
		m.emit(DriveEvent{Type: events.TypeDriveMounted, RootID: id, Mount: n.mount, State: n.state, ReadOnly: n.readOnly})
	}
	m.settleLost(now)
}

func (m *Manager) rootsDiffer(prev, next map[string]activeRec) bool {
	for id, n := range next {
		p, ok := prev[id]
		if !ok || p.root.Unclean != n.root.Unclean || p.root.Profile != n.root.Profile {
			return true
		}
	}
	return false
}

// settleLost expires old LAI-ARK-007 entries and raises or clears the alert to match.
func (m *Manager) settleLost(now time.Time) {
	for id, at := range m.lost {
		if now.Sub(at) >= m.o.LostTTL {
			delete(m.lost, id)
		}
	}
	if m.o.Alerts == nil {
		return
	}
	if len(m.lost) == 0 {
		m.o.Alerts.Clear(CodeDriveUnplugged)
		return
	}
	ids := make([]string, 0, len(m.lost))
	for id := range m.lost {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := m.o.Alerts.Raise(CodeDriveUnplugged, map[string]any{"root_ids": ids}); err != nil {
		m.log.Warn("the alert for a removed drive could not be raised", "error", err.Error())
	}
}

func (m *Manager) callRemoved(id string) {
	m.subMu.Lock()
	hooks := make([]func(string), len(m.removed))
	copy(hooks, m.removed)
	m.subMu.Unlock()
	for _, fn := range hooks {
		fn(id)
	}
}

// emit publishes ev on the bus and to the subscribers. Neither can block the detector.
func (m *Manager) emit(ev DriveEvent) {
	if m.o.Events != nil {
		var err error
		switch ev.Type {
		case events.TypeDriveMounted:
			err = m.o.Events.Publish(ev.Type, "", events.DriveMounted{RootID: ev.RootID, Mount: ev.Mount, State: string(ev.State), ReadOnly: ev.ReadOnly})
		case events.TypeDriveUnmounted:
			err = m.o.Events.Publish(ev.Type, "", events.DriveUnmounted{RootID: ev.RootID, SafeToUnplug: ev.SafeToUnplug})
		}
		if err != nil {
			m.log.Warn("a drive event was refused by the bus", "type", ev.Type, "error", err.Error())
		}
	}
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
			m.log.Warn("a drive event subscriber is not keeping up; an event was dropped", "type", ev.Type, "root", ev.RootID)
		}
	}
}

// ---- eject bookkeeping -----------------------------------------------------------------------------

// BeginEject implements Ejector.
func (m *Manager) BeginEject(rootID string) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	rec, ok := m.active[rootID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoSuchDrive, rootID)
	}
	if _, ej := m.ejecting[rootID]; ej {
		return fmt.Errorf("%w: %q", ErrAlreadyEjecting, rootID)
	}
	m.ejecting[rootID] = ejectRec{at: m.o.Clock.Now(), drive: rec.drive}

	m.mu.Lock()
	var roots []Root
	for _, r := range m.roots {
		if r.ID != rootID {
			roots = append(roots, r)
		}
	}
	m.roots = roots
	for i := range m.drives {
		if m.drives[i].RootID == rootID && m.drives[i].DevNum == rec.devNum {
			m.drives[i].State, m.drives[i].ReadOnly = StateEjecting, true
		}
	}
	m.mu.Unlock()
	if m.o.OnActive != nil {
		m.o.OnActive(append([]Root(nil), roots...))
	}
	return nil
}

// AbortEject implements Ejector.
func (m *Manager) AbortEject(ctx context.Context, rootID string) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if _, ok := m.ejecting[rootID]; !ok {
		return
	}
	delete(m.ejecting, rootID)
	// The active record is still there, so the pass sees no change of the set and publishes no
	// event; the roots list gets the drive back and OnActive is told.
	if err := m.refreshLocked(ctx, false); err != nil {
		m.log.Warn("the drive scan after a cancelled eject failed", "error", err.Error())
	}
	if m.o.OnActive != nil {
		m.o.OnActive(m.ActiveRoots())
	}
}

// FinishEject implements Ejector.
func (m *Manager) FinishEject(ctx context.Context, rootID string) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	_, wasActive := m.active[rootID]
	delete(m.ejecting, rootID)
	delete(m.active, rootID)
	delete(m.lost, rootID)
	if wasActive {
		m.callRemoved(rootID)
		m.emit(DriveEvent{Type: events.TypeDriveUnmounted, RootID: rootID, SafeToUnplug: true})
	}
	if err := m.refreshLocked(ctx, false); err != nil {
		m.log.Warn("the drive scan after an eject failed", "error", err.Error())
	}
	m.settleLost(m.o.Clock.Now())
}

// ---- small helpers ----------------------------------------------------------------------------------

// errnoText is the system error text of err without the operation and path ("read-only file
// system"), which is what the problem details carry.
func errnoText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return err.Error()
}

// isFull reports a full disk or an exhausted quota.
func isFull(err error) bool { return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) }
