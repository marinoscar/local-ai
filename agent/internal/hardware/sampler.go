package hardware

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Default cadences and caps (agent.md sections 3.1 and 4.4).
const (
	// FastInterval is the cadence of the GPU, RAM, CPU, thermal and power probes.
	FastInterval = 10 * time.Second
	// SlowInterval is the cadence of the disk probe and of OOM detection.
	SlowInterval = 60 * time.Second
	// SampleCap bounds one sampling round, and the synchronous first sample: a probe that has not
	// answered by then keeps its last value, marked stale.
	SampleCap = 5 * time.Second
)

// Sampler is the interface other packages use (router fit rules, status, the cloud heartbeat, bench):
// they never import the concrete type, so tests substitute hardware/fake.
type Sampler interface {
	// Snapshot returns the last sampled view. It never blocks on a probe.
	Snapshot() Snapshot
	// Refresh forces a sampling round (bounded by SampleCap and ctx) and returns the new view.
	Refresh(ctx context.Context) Snapshot
	// SampleGPUs reads the GPUs once, in-process, without touching the cached view. aictl bench
	// calls it every 250 ms for the memory curve.
	SampleGPUs(ctx context.Context) ([]GPU, error)
}

// Options configures a Service. Every field is optional: the zero value samples the real machine.
type Options struct {
	// FS is the root filesystem the probes read, with paths relative to "/". Default os.DirFS("/").
	FS fs.FS
	// Exec runs the external programs. Default execx.Exec.
	Exec execx.Runner
	// LookPath resolves a program name inside the fixed PATH. Default execx.Lookup.
	LookPath func(name string) (string, error)
	// Statfs measures a filesystem. Default is the real statfs(2).
	Statfs StatfsFunc
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Logger receives probe failures, once per failure episode. Default slog.Default().
	Logger *slog.Logger

	// ExecTimeout is the deadline of one external program. Default ExecTimeout (3 s).
	ExecTimeout time.Duration
	// FastEvery and SlowEvery are the cadences. Defaults FastInterval and SlowInterval.
	FastEvery, SlowEvery time.Duration
	// SampleCap bounds a sampling round. Default SampleCap (5 s).
	SampleCap time.Duration

	// DriveRoots lists the mounted ark drive content roots (a drive_root disk entry each). The
	// drive watcher provides it; nil means none.
	DriveRoots func() []string
	// ProfileStorage returns the profile's storage.min_free_bytes. nil, or ok false, skips LAI-HW-007
	// (there is no catalog yet).
	ProfileStorage func() (minFree int64, ok bool)
	// ConfiguredBackend returns llama.backend from node.yaml for LAI-HW-017. nil or "" skips it.
	ConfiguredBackend func() string
	// OnPowerChange is called, outside any lock, when the source, battery percent or charging flag
	// changes. The first observation is a baseline and raises nothing. The wiring publishes it as
	// the hardware.power event.
	OnPowerChange func(PowerEvent)
}

// Service is the real Sampler: probes on a timer, a cached Snapshot, and the evaluated results.
type Service struct {
	opts Options
	e    *env
	nv   *nvidiaProbe
	amd  *amdProbe
	oom  *oomDetector
	self *selfTracker

	sem chan struct{} // one sampling round at a time

	mu sync.RWMutex
	st state
	ev evaluator

	failMu  sync.Mutex
	failing map[string]bool // probes whose current failure episode was already logged

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

var _ Sampler = (*Service)(nil)

var errLate = errors.New("probe did not answer within the sampling cap")

// state is the cached result of every probe, with its staleness.
type state struct {
	at time.Time

	nvidia         []GPU
	nvidiaStale    bool
	nvidiaFailures int
	amd            []GPU
	amdStale       bool
	amdAccess      *AMDAccess

	ram     RAM
	cpu     CPU
	thermal Thermal
	power   Power
	self    Self

	disks map[string]Disk // by path
	order []string        // path order of the last target list

	lastPower PowerEvent
	havePower bool

	results []Result
}

// New returns a Service. Call Prime, then Start.
func New(opts Options) *Service {
	if opts.FS == nil {
		opts.FS = os.DirFS("/")
	}
	if opts.Exec == nil {
		opts.Exec = execx.Exec{}
	}
	if opts.LookPath == nil {
		opts.LookPath = execx.Lookup
	}
	if opts.Statfs == nil {
		opts.Statfs = osStatfs
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ExecTimeout <= 0 {
		opts.ExecTimeout = ExecTimeout
	}
	if opts.FastEvery <= 0 {
		opts.FastEvery = FastInterval
	}
	if opts.SlowEvery <= 0 {
		opts.SlowEvery = SlowInterval
	}
	if opts.SampleCap <= 0 {
		opts.SampleCap = SampleCap
	}
	e := &env{fs: opts.FS, run: opts.Exec, look: opts.LookPath, execTimeout: opts.ExecTimeout}
	return &Service{
		opts: opts, e: e,
		nv: newNvidiaProbe(e), amd: newAMDProbe(e), oom: newOOMDetector(e), self: &selfTracker{},
		sem: make(chan struct{}, 1),
		st:  state{disks: map[string]Disk{}},
		ev:  evaluator{},
	}
}

// DetectBackend is the backend detection of agent.md section 4.4.3 on the real machine, for config's
// DetectBackend hook when no Service exists yet.
func DetectBackend() string { return New(Options{}).DetectBackend() }

// DetectBackend returns cuda, vulkan or cpu from what is present now: the NVIDIA driver (or an
// nvidia-smi that lists a GPU) and AMD cards in sysfs. It does not wait for the sampler.
func (s *Service) DetectBackend() string {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.SampleCap)
	defer cancel()
	nvidia, amd := s.presence(ctx)
	return BackendFor(nvidia, amd)
}

// presence reports whether an NVIDIA and an AMD GPU are present. NVIDIA counts when the kernel
// driver file exists, or when nvidia-smi is installed and lists at least one GPU.
func (s *Service) presence(ctx context.Context) (nvidia, amd bool) {
	switch {
	case s.nv.driverLoaded():
		nvidia = true
	case s.nv.hasTool():
		if out, err := s.nv.queryGPUs(ctx); err == nil {
			if gpus, err := parseNvidiaGPUs(out); err == nil && len(gpus) > 0 {
				nvidia = true
			}
		}
	}
	return nvidia, s.amd.present()
}

// probe is one unit of a sampling round: it does its I/O off the lock and returns what to apply.
type probe struct {
	name string
	run  func(ctx context.Context) func(*state)
}

// runRound runs the probes concurrently, each with the round's deadline. A probe that has not
// answered by the deadline is reported through late, so its section is marked stale; its goroutine
// finishes on its own (every exec has its own timeout) and its answer is dropped.
func runRound(ctx context.Context, cap time.Duration, probes []probe) (applies []func(*state), late []string) {
	ctx, cancel := context.WithTimeout(ctx, cap)
	defer cancel()
	type answer struct {
		name  string
		apply func(*state)
	}
	ch := make(chan answer, len(probes))
	for _, p := range probes {
		go func(p probe) { ch <- answer{p.name, p.run(ctx)} }(p)
	}
	got := map[string]bool{}
	for range probes {
		select {
		case a := <-ch:
			got[a.name] = true
			if a.apply != nil {
				applies = append(applies, a.apply)
			}
		case <-ctx.Done():
			for _, p := range probes {
				if !got[p.name] {
					late = append(late, p.name)
				}
			}
			return applies, late
		}
	}
	return applies, nil
}

func (s *Service) acquire(ctx context.Context) bool {
	select {
	case s.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) release() { <-s.sem }

// Prime takes the first sample synchronously (startup step 9, agent.md section 4.1), capped by
// SampleCap, so the first GET /hardware and the router's first fit decision have data.
func (s *Service) Prime(ctx context.Context) { s.sampleAll(ctx) }

// Refresh implements Sampler: fast probes and disks, bounded by SampleCap and ctx.
func (s *Service) Refresh(ctx context.Context) Snapshot {
	s.sampleAll(ctx)
	return s.Snapshot()
}

func (s *Service) sampleAll(ctx context.Context) {
	if !s.acquire(ctx) {
		return
	}
	defer s.release()
	s.sampleFast(ctx)
	s.sampleDisks(ctx)
}

// Snapshot implements Sampler.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// Results returns the findings of the last sample: the LAI-HW codes (and LAI-LLM-027) that are
// raised now, ordered by code.
func (s *Service) Results() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Result{}, s.st.results...)
}

// LastOOM returns the most recent OOM kill of llama-server seen, from the journal or the service watcher.
func (s *Service) LastOOM() (OOMEvent, bool) { return s.oom.lastEvent() }

// ObserveServiceResult is the service watcher's hook: it passes the Result of localai-llama when
// the unit stops. Result=oom-kill raises LAI-LLM-027 like a journal line does.
func (s *Service) ObserveServiceResult(result string) {
	if _, ok := s.oom.observeService(result, s.opts.Now()); ok {
		s.mu.Lock()
		s.reevaluateLocked(false, false)
		s.mu.Unlock()
	}
}

// SampleGPUs implements Sampler: one in-process read of every GPU. It fails only when a present
// GPU source fails; with no GPU at all it returns an empty list.
func (s *Service) SampleGPUs(ctx context.Context) ([]GPU, error) {
	var out []GPU
	var errs []error
	if s.nv.present() {
		g, err := s.nv.sample(ctx)
		if err != nil && s.nv.driverLoaded() {
			errs = append(errs, err)
		}
		out = append(out, g...)
	}
	g, err := s.amd.sample(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	out = append(out, g...)
	return out, errors.Join(errs...)
}

// sampleFast runs the GPU, RAM, CPU, thermal and power probes concurrently and applies the answers.
func (s *Service) sampleFast(ctx context.Context) {
	probes := []probe{
		{"nvidia", s.probeNVIDIA},
		{"amd", s.probeAMD},
		{"ram", s.probeRAM},
		{"cpu", s.probeCPU},
		{"thermal", func(context.Context) func(*state) {
			t := s.e.readThermal()
			return func(st *state) { st.thermal = t }
		}},
		{"power", func(ctx context.Context) func(*state) {
			p := s.e.readPower(ctx)
			p.Profile = s.e.readProfile(ctx)
			return func(st *state) { st.power = p }
		}},
		{"self", func(context.Context) func(*state) {
			v, err := s.self.read(s.e, s.opts.Now())
			if err != nil {
				return nil // the agent's own numbers are best effort
			}
			return func(st *state) { st.self = v }
		}},
	}
	applies, late := runRound(ctx, s.opts.SampleCap, probes)

	var power *PowerEvent
	s.mu.Lock()
	for _, a := range applies {
		a(&s.st)
	}
	for _, name := range late {
		s.markStaleLocked(name)
	}
	s.st.at = s.opts.Now().UTC()
	if p := eventOf(s.st.power); !s.st.havePower {
		s.st.lastPower, s.st.havePower = p, true // the first observation is a baseline
	} else if !samePower(s.st.lastPower, p) {
		s.st.lastPower = p
		power = &p
	}
	s.reevaluateLocked(true, false)
	s.mu.Unlock()

	if power != nil && s.opts.OnPowerChange != nil {
		s.opts.OnPowerChange(*power)
	}
}

func (s *Service) markStaleLocked(name string) {
	switch name {
	case "nvidia":
		s.st.nvidiaStale = len(s.st.nvidia) > 0 || s.nv.driverLoaded()
		s.st.nvidiaFailures++
	case "amd":
		s.st.amdStale = len(s.st.amd) > 0
	case "ram":
		s.st.ram.Stale = true
	case "cpu":
		s.st.cpu.Stale = true
	case "thermal":
		s.st.thermal.Stale = true
	case "power":
		s.st.power.Stale = true
	}
	s.report(name, errLate)
}

func (s *Service) probeNVIDIA(ctx context.Context) func(*state) {
	if !s.nv.present() {
		return func(st *state) { st.nvidia, st.nvidiaStale, st.nvidiaFailures = nil, false, 0 }
	}
	gpus, err := s.nv.sample(ctx)
	return func(st *state) {
		if err != nil {
			st.nvidiaFailures++
			st.nvidiaStale = true
			s.report("nvidia", err)
			return
		}
		s.report("nvidia", nil)
		st.nvidia, st.nvidiaStale, st.nvidiaFailures = gpus, false, 0
	}
}

func (s *Service) probeAMD(ctx context.Context) func(*state) {
	if !s.amd.present() {
		return func(st *state) { st.amd, st.amdStale, st.amdAccess = nil, false, nil }
	}
	gpus, err := s.amd.sample(ctx)
	access := s.amd.access()
	return func(st *state) {
		st.amdAccess = &access
		if err != nil {
			st.amdStale = true
			s.report("amd", err)
			return
		}
		s.report("amd", nil)
		st.amd, st.amdStale = gpus, false
	}
}

func (s *Service) probeRAM(context.Context) func(*state) {
	r, err := s.e.readRAM()
	return func(st *state) {
		if err != nil {
			st.ram.Stale = true
			s.report("ram", err)
			return
		}
		s.report("ram", nil)
		st.ram = r
	}
}

func (s *Service) probeCPU(context.Context) func(*state) {
	c, err := s.e.readCPU()
	return func(st *state) {
		if err != nil {
			st.cpu.Stale = true
			s.report("cpu", err)
			return
		}
		s.report("cpu", nil)
		st.cpu = c
	}
}

// diskTargets lists what to measure: the internal root, every mounted drive root, then the state,
// opt and etc directories.
func (s *Service) diskTargets() []DiskTarget {
	targets := []DiskTarget{{InternalRootPath, RoleInternalRoot}}
	if s.opts.DriveRoots != nil {
		roots := append([]string(nil), s.opts.DriveRoots()...)
		sort.Strings(roots)
		for _, r := range roots {
			targets = append(targets, DiskTarget{r, RoleDriveRoot})
		}
	}
	return append(targets, DiskTarget{StatePath, RoleState}, DiskTarget{OptPath, RoleOpt}, DiskTarget{EtcPath, RoleEtc})
}

// sampleDisks measures the disk targets. A target that fails keeps its last value, marked stale; a
// target that never answered (a directory that does not exist) is left out.
func (s *Service) sampleDisks(ctx context.Context) {
	targets := s.diskTargets()
	probes := []probe{{"disks", func(context.Context) func(*state) {
		disks, errs := s.e.readDisks(s.opts.Statfs, targets)
		return func(st *state) {
			fresh := map[string]Disk{}
			for _, d := range disks {
				fresh[d.Path] = d
			}
			next := map[string]Disk{}
			var order []string
			for _, t := range targets {
				if d, ok := fresh[t.Path]; ok {
					next[t.Path] = d
					order = append(order, t.Path)
					s.report("disk "+t.Path, nil)
				} else if prev, ok := st.disks[t.Path]; ok {
					prev.Stale = true
					next[t.Path] = prev
					order = append(order, t.Path)
					s.report("disk "+t.Path, errs[t.Path])
				}
			}
			st.disks, st.order = next, order
		}
	}}}
	applies, late := runRound(ctx, s.opts.SampleCap, probes)
	s.mu.Lock()
	for _, a := range applies {
		a(&s.st)
	}
	if len(late) > 0 {
		for p, d := range s.st.disks {
			d.Stale = true
			s.st.disks[p] = d
		}
		s.report("disks", errLate)
	}
	s.reevaluateLocked(false, true)
	s.mu.Unlock()
}

// sampleOOM runs the journal check and re-evaluates when a kill was found.
func (s *Service) sampleOOM(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.SampleCap)
	defer cancel()
	events, err := s.oom.check(ctx, s.opts.Now())
	if err != nil {
		if !errors.Is(err, ErrToolMissing) {
			s.report("oom journal", err)
		}
		return
	}
	s.report("oom journal", nil)
	for _, ev := range events {
		s.opts.Logger.Warn("llama-server was killed for lack of memory",
			"component", "hardware", "pid", ev.PID, "code", CodeOOMKill)
	}
	s.mu.Lock()
	s.reevaluateLocked(false, false)
	s.mu.Unlock()
}

// reevaluateLocked recomputes the findings from the current state. fast and disks say which kind
// of sample just completed: only that kind advances the consecutive-sample counters, so a finding
// that needs 3 samples needs 3 sampling rounds however often the state is re-read. Caller holds s.mu.
func (s *Service) reevaluateLocked(fast, disks bool) {
	snap := s.snapshotLocked()
	in := evalInput{
		snap:              snap,
		now:               s.opts.Now(),
		tickFast:          fast,
		tickDisks:         disks,
		nvidiaDriver:      s.nv.driverLoaded(),
		nvidiaFailures:    s.st.nvidiaFailures,
		nvidiaPresent:     s.nv.driverLoaded() || len(s.st.nvidia) > 0,
		amdPresent:        len(s.st.amd) > 0 || s.st.amdAccess != nil,
		amdAccess:         s.st.amdAccess,
		configuredBackend: "",
	}
	if s.opts.ConfiguredBackend != nil {
		in.configuredBackend = s.opts.ConfiguredBackend()
	}
	if s.opts.ProfileStorage != nil {
		in.minFreeBytes, in.haveMinFree = s.opts.ProfileStorage()
	}
	if ev, ok := s.oom.lastEvent(); ok {
		in.lastOOM = &ev
	}
	s.st.results = s.ev.evaluate(in)
}

func (s *Service) snapshotLocked() Snapshot {
	snap := Snapshot{
		SampledAt: s.st.at,
		RAM:       s.st.ram,
		Power:     s.st.power,
		Thermal:   s.st.thermal,
		CPU:       s.st.cpu,
		Self:      s.st.self,
	}
	for _, g := range s.st.nvidia {
		g.Stale = g.Stale || s.st.nvidiaStale
		snap.GPUs = append(snap.GPUs, g)
	}
	for _, g := range s.st.amd {
		g.Stale = g.Stale || s.st.amdStale
		snap.GPUs = append(snap.GPUs, g)
	}
	for _, p := range s.st.order {
		snap.Disks = append(snap.Disks, s.st.disks[p])
	}
	nvidia := s.nv.driverLoaded() || len(s.st.nvidia) > 0
	snap.Backend = BackendFor(nvidia, len(s.st.amd) > 0)
	return snap.clone()
}

// Start begins the periodic sampling: fast probes every FastEvery, disks and OOM detection every
// SlowEvery. It returns at once; call Prime first for a synchronous first sample.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		ctx, s.cancel = context.WithCancel(ctx)
		s.done = make(chan struct{})
		go s.loop(ctx)
	})
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)
	fast := time.NewTicker(s.opts.FastEvery)
	slow := time.NewTicker(s.opts.SlowEvery)
	defer fast.Stop()
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			if s.acquire(ctx) {
				s.sampleFast(ctx)
				s.release()
			}
		case <-slow.C:
			if s.acquire(ctx) {
				s.sampleDisks(ctx)
				s.release()
			}
			s.sampleOOM(ctx)
		}
	}
}

// Stop ends the periodic sampling and waits for the loop to exit (shutdown step "stop periodic
// workers", agent.md section 4.12). It is safe to call without Start, and more than once.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
			<-s.done
		}
	})
}

// report logs a probe failure once per failure episode, and its recovery once. A nil err is a
// success. A failing probe keeps its last value, marked stale.
func (s *Service) report(what string, err error) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if s.failing == nil {
		s.failing = map[string]bool{}
	}
	switch {
	case err != nil && !s.failing[what]:
		s.failing[what] = true
		s.opts.Logger.Warn("hardware probe failed; keeping the last value", "component", "hardware", "probe", what, "err", err)
	case err == nil && s.failing[what]:
		delete(s.failing, what)
		s.opts.Logger.Info("hardware probe recovered", "component", "hardware", "probe", what)
	}
}

// Check is a fast doctor check: a stable id and a function that returns the findings of one group
// of codes. The functions only read what the sampler already evaluated, so they are cheap and never
// run a probe; AGENT-11's registry calls them every 60 s.
type Check struct {
	ID  string
	Run func(ctx context.Context) []Result
}

// Checks returns one Check per group (hw.gpu, hw.memory, hw.disk, hw.thermal, hw.power, hw.oom).
func (s *Service) Checks() []Check {
	ids := []string{CheckGPU, CheckMemory, CheckDisk, CheckThermal, CheckPower, CheckOOM}
	out := make([]Check, 0, len(ids))
	for _, id := range ids {
		id := id
		out = append(out, Check{ID: id, Run: func(context.Context) []Result {
			var rs []Result
			for _, r := range s.Results() {
				if r.Check == id {
					rs = append(rs, r)
				}
			}
			return rs
		}})
	}
	return out
}
