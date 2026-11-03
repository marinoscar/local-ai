package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kitready"
	"github.com/marinoscar/local-ai/agent/internal/router"
	rbuild "github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
	"github.com/marinoscar/local-ai/agent/internal/sync/run/adapt"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// This file wires the ark side of the node (ark-sync.md, rebuild-kit.md): the sync journal and the
// best-copy index, the downloader, the item state machine, the planner, the run engine, the readiness
// service behind GET /ark and the kit verifier behind its kit rows. The packages only expose small
// interfaces and adapters; this is the one place that composes them (the seam rule: internal/api does
// not import the planner, the engine or the readiness service).
//
// The stack is built in startup step 9, before the listeners, because the journal replay and the
// recovery of a crashed swap (Engine.Resume) must be over before anything can start a run or serve
// content. Nothing here needs the internet except the downloader, which only the sync engine drives.

// ArkRoots is the list of mounted content roots: the internal root (as the monitor last saw it) and
// the ark drives. The drive manager (SYNC-02) owns the drives; it calls SetDrives before it publishes
// drive.mounted or drive.unmounted, so the handler of those events sees the new list.
type ArkRoots struct {
	monitor *ark.Monitor
	mu      sync.RWMutex
	drives  []ark.Root
}

// List returns the mounted roots, internal first.
func (r *ArkRoots) List() []ark.Root {
	out := []ark.Root{r.monitor.Root()}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append(out, r.drives...)
}

// SetDrives replaces the mounted ark drives.
func (r *ArkRoots) SetDrives(drives []ark.Root) {
	r.mu.Lock()
	r.drives = append([]ark.Root(nil), drives...)
	r.mu.Unlock()
}

// arkStack is everything the ark wiring built. A field is nil when its piece could not be built; the
// daemon then runs without it (the model routes answer LAI-ARK-001, /ark/sync is not registered).
type arkStack struct {
	d *Daemon

	roots  *ArkRoots
	index  *ark.Index
	jr     *journal.Journal
	ret    *retention.Retention
	engine *run.Engine
	ready  *ready.Service
	kit    *kitready.Source
	plans  *plan.Gatherer
	bucket *throttle.TokenBucket
	// extras writes START-HERE.html and the inventory copy on the ark drives (ark_extras.go); the
	// drive manager calls its Write for a drive it has just initialised.
	extras *driveExtras

	// resolvers and itemResolvers are the registries of the source resolvers: the planner half
	// (plan.Resolvers) and the engine half (run.ItemResolvers). registerSources fills them with the
	// git_mirror, apt_closure and repo_docs producers (ark_sources.go); an item of any other source
	// type is planned and fetched from what the catalog pins.
	resolvers     *plan.Resolvers
	itemResolvers *run.ItemResolvers
	sources       *arkSources
	// webSources are the huggingface, github_release and url resolvers (ark_web_sources.go).
	webSources *arkWebSources

	facts *nodeFacts

	// drives is the AI-ARK drive detector (ark_drives.go); watchDrives says whether it is started.
	drives      *ark.Manager
	watchDrives bool
}

// arkIndex builds the best-copy index. It cannot fail: an index with no roots is empty, and the
// piper routes and the model views read it whether or not the sync stack comes up.
func (d *Daemon) arkIndex() *ark.Index { return ark.NewIndex(config.OSFS{}) }

// syncDir is /var/lib/localai/sync: the journal, last-run.json and the run slot.
func (d *Daemon) syncDir() string { return d.c.Paths.Join(varDir + "/sync") }

// syncSettings is the sync block of node.yaml now, the defaults when it is absent or invalid (the
// config store refused such a file, so the last valid one is in use).
func (d *Daemon) syncSettings() run.Settings {
	s, err := run.ParseSettings(d.c.Config.Get().Blocks["sync"])
	if err != nil {
		return run.DefaultSettings()
	}
	return s
}

// netState is the node's mode and whether the internet is reachable (connectivity is not "none").
func (d *Daemon) netState() run.NetState {
	if d.env.NetState != nil {
		return d.env.NetState()
	}
	if d.mode == nil {
		return run.NetState{Mode: string(plan.ModeOffline)}
	}
	info := d.mode.Info()
	return run.NetState{Mode: string(info.Effective), Online: info.Connectivity != state.ConnNone}
}

// buildArk builds the sync stack over the roots. The index is passed in because the piper routes share
// it. A failure raises nothing fatal: it is logged and the stack stays nil.
func (d *Daemon) buildArk(ctx context.Context, idx *ark.Index) (*arkStack, error) {
	c, env, clock, log := d.c, d.env, d.env.Clock, d.log
	cfg := c.Config
	fsys := config.OSFS{}
	lg := log.With("component", "sync")

	s := &arkStack{d: d, index: idx, roots: &ArkRoots{monitor: c.ArkMonitor}, facts: &nodeFacts{d: d}}
	s.resolvers, s.itemResolvers = plan.NewResolvers(), run.NewItemResolvers()
	roots := s.roots.List

	jr, rec, err := journal.Open(ctx, journal.Options{
		Dir: d.syncDir(), Now: clock.Now, Logger: lg, Scan: journal.ScanRoots(fsys, roots),
	})
	if err != nil {
		return nil, fmt.Errorf("sync journal: %w", err)
	}
	s.jr = jr
	for _, p := range rec.Problems {
		lg.Info("the sync journal was repaired", "code", p.Code, "details", p.Details)
	}
	if _, err := idx.Rebuild(ctx, roots()); err != nil {
		return nil, fmt.Errorf("best-copy index: %w", err)
	}

	s.ret, err = retention.New(retention.Deps{
		FS: retention.OSFS{}, Roots: roots, Copies: idx, Journal: jr.Items, Index: idx, Items: jr.Items, Now: clock.Now, Logger: lg,
	})
	if err != nil {
		return nil, fmt.Errorf("retention: %w", err)
	}

	// One bucket shared by every download; node.yaml sync.bandwidth_limit moves its rate (applySettings).
	s.bucket = throttle.NewBucket(0, nil)
	s.applySettings()
	httpc := env.SyncHTTP
	if httpc == nil {
		httpc = download.NewHTTPClient(download.ClientConfig{Version: env.Version, Token: d.hfLogin})
	}
	dl := download.New(download.Options{
		HTTP: httpc, Bucket: s.bucket, Space: s.ret, Logger: lg,
		OnRootFault: func(rootID string, e *download.Error, readOnly bool) {
			lg.Error("a content root turned out unwritable; writes to it stop", "root", rootID, "code", e.Code, "read_only", readOnly)
		},
	})
	// The content validators of the item machine (the ZIM's internal MD5, pmtiles verify); the library
	// source types register theirs in registerLibrarySources.
	validators := item.NewValidators()
	machine, err := item.New(item.Deps{
		FS: item.OSFS{}, Journal: jr, Fetcher: dl, Index: idx, Remover: s.ret, Roots: roots, Validators: validators,
		VerifyReread: func() bool { return d.syncSettings().VerifyReread },
		Now:          clock.Now, NodeID: cfg.Get().Node.ID, AgentVersion: env.Version, Logger: lg,
	})
	if err != nil {
		return nil, fmt.Errorf("item machine: %w", err)
	}

	if err := s.registerSources(dl); err != nil {
		return nil, fmt.Errorf("source resolvers: %w", err)
	}
	if err := s.registerWebSources(dl, httpc); err != nil {
		return nil, fmt.Errorf("web source resolvers: %w", err)
	}
	if err := s.registerLibrarySources(validators); err != nil {
		return nil, fmt.Errorf("library source resolvers: %w", err)
	}

	s.plans, err = plan.NewGatherer(plan.Deps{
		FS: fsys, Catalog: func() *catalog.Catalog { return c.Catalog.Get().Catalog },
		Settings: func() plan.NodeSettings {
			cf := cfg.Get()
			return plan.NodeSettings{Profile: cf.Profile, Overrides: catalog.Overrides(cf.ProfileOverrides)}
		},
		Roots: roots, Copies: idx, Journal: jr.Items, Statfs: plan.OSStatfs,
		Node: s.nodeInfo, Network: func() plan.Network { n := d.netState(); return plan.Network{Mode: n.Mode, Online: n.Online} },
		Resolvers: s.resolvers, ResolveTimeout: sourceResolveTimeout, Now: clock.Now, Logger: lg,
	})
	if err != nil {
		return nil, fmt.Errorf("planner: %w", err)
	}

	// The readiness service and the engine need each other (READY after a swap; the run record behind
	// READY): the engine is bound late.
	late := &lateRuns{}
	s.kit = &kitready.Source{
		Verifier: kit.Verifier{FS: kit.OSFS{}}, Roots: s.kitRoots, Node: s.facts.kitNode, Keys: update.EmbeddedKeys(),
		Now: clock.Now, ReverifyDays: d.syncSettings().ReverifyDays,
	}
	s.ready, err = ready.New(ready.Deps{
		FS: fsys, Clock: clock, Plans: s.plans, Roots: roots, Journal: jr.Items, Runs: late, Kit: s.kit,
		Loadable:     s.loadable,
		Modes:        readyModes(c.State),
		ReverifyDays: func() int { return d.syncSettings().ReverifyDays },
		SyncState:    late.SyncState,
		NodeID:       cfg.Get().Node.ID, AgentVersion: env.Version, Logger: lg,
	})
	if err != nil {
		return nil, fmt.Errorf("readiness service: %w", err)
	}

	s.extras = s.newDriveExtras()
	s.engine, err = run.New(run.Deps{
		Journal: jr, Machine: machine, Planner: adapt.GathererPlanner{G: s.plans}, Copies: idx, FS: fsys, JournalDir: d.syncDir(),
		Settings: d.syncSettings, Net: d.netState, Power: run.PowerFunc(d.onBattery), Catalog: catalogRefresh{d},
		Resolvers: s.itemResolvers, Retention: s.ret,
		Unknown: func(root ark.Root) ([]string, error) { return jr.Items.UnknownStaging(fsys, root) },
		Events:  c.Bus, Ready: s.readyRefresher(), Lock: run.FileLock{Path: filepath.Join(d.syncDir(), "lock")},
		Hooks: run.Hooks{Extras: s.extras.Run},
		Clock: syncClock{clock}, Logger: lg,
	})
	if err != nil {
		return nil, fmt.Errorf("sync engine: %w", err)
	}
	late.e.Store(s.engine)

	// Finish the swaps a crashed run left in flight, now that the journal and the index exist and
	// before the listeners or the claim loop can start a run.
	for _, r := range s.engine.Resume(ctx, rec) {
		lg.Info("a swap interrupted by a restart was finished", "root", r.Root, "item", r.Item, "version", r.Version, "code", r.Code)
	}
	s.buildDrives()
	return s, nil
}

// applySettings moves the shared bandwidth bucket to node.yaml sync.bandwidth_limit.
func (s *arkStack) applySettings() {
	if s == nil || s.bucket == nil {
		return
	}
	bps, err := throttle.ParseLimit(s.d.syncSettings().BandwidthLimit)
	if err != nil {
		bps = 0
	}
	s.bucket.SetRate(bps)
}

// hfLogin is the Hugging Face login the downloader sends to huggingface.co only: the content of
// sync.hf_token_file when that file exists, "" otherwise. It is read at request time and never kept.
func (d *Daemon) hfLogin() string {
	p := d.syncSettings().HFTokenFile
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(d.c.Paths.Join(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// onBattery is the engine's Power: the node runs on its battery (a desktop with no battery never does).
func (d *Daemon) onBattery() bool {
	if d.c.Hardware == nil {
		return false
	}
	return d.c.Hardware.Snapshot().Power.Source == hardware.SourceBattery
}

// nodeInfo is the machine's facts for the planner: the backend, the memory, and the Ubuntu codename and
// architecture that filter the kit items.
func (s *arkStack) nodeInfo() plan.NodeInfo {
	d := s.d
	backend := d.c.Config.Get().Llama.Backend
	snap := d.c.Hardware.Snapshot()
	info := plan.NodeInfo{Backend: backend, VRAMMiB: snap.FitMemory(backend), RAMMiB: snap.RAM.TotalMiB}
	info.Codename, info.Arch = s.facts.release()
	return info
}

// kitRoots are the mounted roots the kit verifier reads.
func (s *arkStack) kitRoots() []kit.StatusRoot {
	var out []kit.StatusRoot
	for _, r := range s.roots.List() {
		out = append(out, kit.StatusRoot{ID: r.ID, Path: r.Path, Internal: r.Kind == ark.KindInternal})
	}
	return out
}

// loadable says whether a catalog model fits this node (the survival requirements of READY count only
// the models that can load here), by the same rule as the model views.
func (s *arkStack) loadable(modelID string) bool {
	d := s.d
	cat := d.c.Catalog.Get().Catalog
	m, ok := cat.Model(modelID)
	if !ok {
		return false
	}
	var rcfg catalog.Router
	if p := d.profile(); p != nil {
		rcfg = p.Router
	}
	env := router.NewEnv(d.c.Hardware.Snapshot(), d.c.Config.Get().Llama.Backend, rcfg, 0)
	ok, _ = env.Loadable(m, true)
	return ok
}

// lateRuns is ready.RunSource and the SyncState of the readiness service over an engine that is built
// after it.
type lateRuns struct{ e atomic.Pointer[run.Engine] }

// LastRuns implements ready.RunSource.
func (l *lateRuns) LastRuns(rootID string) (*ready.RunInfo, time.Time) {
	if e := l.e.Load(); e != nil {
		return adapt.ReadyRuns{Engine: e}.LastRuns(rootID)
	}
	return nil, time.Time{}
}

// LastDriveSync implements ready.DriveSyncSource.
func (l *lateRuns) LastDriveSync() time.Time {
	if e := l.e.Load(); e != nil {
		return e.LastDriveSync()
	}
	return time.Time{}
}

// SyncState is the engine's state for status.ark.sync ("" while it is not built).
func (l *lateRuns) SyncState() string {
	if e := l.e.Load(); e != nil {
		return e.SyncState()
	}
	return ""
}

// catalogRefresh is run.CatalogRefresher over the daemon's catalog fetcher: the plan of a run is made
// from the freshest catalog the node can get (the cloud, then GitHub); a failure is not fatal.
type catalogRefresh struct{ d *Daemon }

// Refresh implements run.CatalogRefresher.
func (c catalogRefresh) Refresh(ctx context.Context) error {
	_, err := refresher(c).Refresh(ctx)
	return err
}

// syncClock is run.Clock over the daemon's clock: AfterFunc waits on a ticker of it, like the step
// deadlines, so a test with a manual clock decides when it fires.
type syncClock struct{ c config.Clock }

// Now implements run.Clock.
func (s syncClock) Now() time.Time { return s.c.Now() }

// AfterFunc implements run.Clock.
func (s syncClock) AfterFunc(d time.Duration, f func()) func() bool {
	t := s.c.NewTicker(max(d, time.Millisecond))
	stop := make(chan struct{})
	var done atomic.Bool
	go func() {
		select {
		case <-t.C():
			if done.CompareAndSwap(false, true) {
				t.Stop()
				f()
			}
		case <-stop:
		}
	}()
	return func() bool {
		if done.CompareAndSwap(false, true) {
			close(stop)
			t.Stop()
			return true
		}
		return false
	}
}

// readyRefresher is the engine's ReadyRefresher: before READY.json is regenerated for a root, the kit
// index of that root (kit/MANIFEST.json and kit/README.txt) is regenerated too, so both describe what
// is on the disk (kit.Refresher; the engine has no per-item hook, and a regeneration after every swap
// and at the end of a run is what the spec asks for). A root with no kit folder, or a read-only one, is
// left alone.
func (s *arkStack) readyRefresher() run.ReadyRefresher {
	return kitRefreshing{
		inner: adapt.Ready{Service: s.ready}, roots: s.roots.List, log: s.d.log.With("component", "sync"), pins: s.facts.nodePins,
		refresher: kit.Refresher{
			FS: config.OSFS{}, Now: s.d.env.Clock.Now,
			Expected:       s.expectedKit,
			CatalogVersion: func() string { return s.d.c.Catalog.Get().Catalog.Version },
		},
	}
}

// expectedKit are the kit items the profile of a root wants, so the index can list what is missing.
func (s *arkStack) expectedKit(root ark.Root) []catalog.ContentItem {
	in, err := s.plans.Gather(context.Background(), root.ID, plan.GatherOptions{})
	if err != nil {
		return nil
	}
	var out []catalog.ContentItem
	for _, it := range plan.DesiredItems(in.Catalog, in.Profile, in.Overrides, in.Root, in.Node).Items {
		if it.Content != nil && kit.IsKitItem(*it.Content) {
			out = append(out, *it.Content)
		}
	}
	return out
}

type kitRefreshing struct {
	inner     run.ReadyRefresher
	refresher kit.Refresher
	// pins returns this node's own pin entries; nil, or ok false, reports nothing.
	pins  func() (kit.Pins, bool)
	roots func() []ark.Root
	log   *slog.Logger
}

// Refresh implements run.ReadyRefresher.
func (k kitRefreshing) Refresh(ctx context.Context, rootIDs []string) (map[string]string, error) {
	want := map[string]bool{}
	for _, id := range rootIDs {
		want[id] = true
	}
	for _, r := range k.roots() {
		if !want[r.ID] || r.ReadOnly {
			continue
		}
		if fi, err := os.Stat(filepath.Join(r.Path, kit.DirName)); err != nil || !fi.IsDir() {
			continue
		}
		if err := k.refresher.Refresh(ctx, r); err != nil {
			k.log.Warn("the kit index could not be regenerated", "root", r.ID, "code", doctor.KIT046, "error", err.Error())
		}
		k.reportPins(r)
	}
	return k.inner.Refresh(ctx, rootIDs)
}

// reportPins adds this node's entries to kit/catalog/pins.json of a root it synced (rebuild-kit.md
// section 3.3: unenrolled, each node merges itself into the pins of the roots it syncs, so a drive keeps
// the sources for every node of the lab). Only a root that already has kit/catalog is touched (the
// kit-catalog item put it there); a damaged or newer file is left exactly as it is (LAI-KIT-045).
func (k kitRefreshing) reportPins(r ark.Root) {
	if k.pins == nil {
		return
	}
	dir := filepath.Join(r.Path, kit.DirName, kit.PartCatalog)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	own, ok := k.pins()
	if !ok {
		return
	}
	now := time.Now()
	if k.refresher.Now != nil {
		now = k.refresher.Now()
	}
	if _, err := kit.UpdatePins(k.refresher.FS, filepath.Join(dir, kit.PinsFileName), own, now); err != nil {
		k.log.Warn("this node could not be added to the kit pins", "root", r.ID, "code", kit.CodePinsDamaged, "damaged", kit.IsPinsDamaged(err), "error", err.Error())
	}
}

// arkUnavailable stands in for the planner and the readiness view of a daemon whose sync stack could
// not be built: every call answers 503 LAI-AGT-024, like the routes did before the engine was wired.
type arkUnavailable struct{}

var _ api.ArkSyncPlanner = arkUnavailable{}
var _ api.ArkReadyView = arkUnavailable{}
var _ api.ArkPruner = arkUnavailable{}

func (arkUnavailable) err() error {
	return api.NewError(api.CodeStarting, http.StatusServiceUnavailable, map[string]any{"reason": "sync_engine_not_ready"})
}

// DryRun implements api.ArkSyncPlanner.
func (u arkUnavailable) DryRun(context.Context, api.ArkSyncRequest) (any, error) { return nil, u.err() }

// Ready implements api.ArkReadyView.
func (u arkUnavailable) Ready(context.Context, string) ([]any, error) { return nil, u.err() }

// Prune implements api.ArkPruner.
func (u arkUnavailable) Prune(context.Context, api.ArkPruneRequest) (api.ArkPruneResult, error) {
	return api.ArkPruneResult{}, u.err()
}

// arkJobs is jobs.Ark over the engine. The cloud's ark.verify waits for the re-verification (SYNC-13): a
// command of that type is refused with LAI-CLD-057, like one of a type this node does not run.
type arkJobs struct{ adapt.Jobs }

// StartVerify implements jobs.Ark.
func (arkJobs) StartVerify(context.Context, jobs.ArkRequest) (jobs.ArkVerifyStarted, error) {
	return jobs.ArkVerifyStarted{}, &cloud.JobError{Code: doctor.CLD057, Message: "This node does not run ark.verify yet."}
}

// ---- facts of this computer ---------------------------------------------------------------------

// nodeFacts reads what the kit completeness rules and the planner need to know about this computer:
// node.yaml (already loaded by the config store), os-release and the architecture, and, on a CUDA node,
// the driver (nvidia-smi). The driver and the release are read once and kept for a while: nvidia-smi
// takes a moment and READY asks at every regeneration. It mirrors what `aictl kit status` reads
// (kitReadFacts in internal/cli) without importing the CLI.
type nodeFacts struct {
	d *Daemon

	mu            sync.Mutex
	codename      string
	arch          string
	releaseRead   bool
	driverCUDA    string
	driverBranch  string
	driverAt      time.Time
	driverBackend string
}

// factsTTL is how long the driver facts are kept.
const factsTTL = 10 * time.Minute

// nvidiaSmiTimeout bounds each nvidia-smi call.
const nvidiaSmiTimeout = 15 * time.Second

// release is the Ubuntu codename and the kit's architecture word; both empty when unknown.
func (f *nodeFacts) release() (codename, arch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseRead && f.codename != "" {
		return f.codename, f.arch
	}
	cn, ar, err := kit.MachineRelease(config.OSFS{}, f.d.c.Paths.Join("/etc/os-release"), runtime.GOARCH)
	f.releaseRead = true
	switch {
	case err == nil:
		f.codename, f.arch = cn, ar
	case runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64":
		f.arch = runtime.GOARCH
	}
	return f.codename, f.arch
}

// kitNode implements kitready.Source.Node.
func (f *nodeFacts) kitNode() *kit.NodeFacts {
	cfg := f.d.c.Config.Get()
	out := &kit.NodeFacts{
		AgentVersion: f.d.env.Version, NodeID: cfg.Node.ID, LlamaCommit: cfg.Llama.Commit, Backend: cfg.Llama.Backend,
	}
	if cfg.Llama.CudaArch != nil {
		out.CudaArch = *cfg.Llama.CudaArch
	}
	if raw, ok := cfg.Blocks["whisper"]; ok {
		var w struct {
			Commit string `yaml:"commit"`
		}
		if raw.Decode(&w) == nil {
			out.WhisperCommit = w.Commit
		}
	}
	out.Codename, out.Arch = f.release()
	if out.Backend == hardware.BackendCUDA {
		out.DriverCUDA, out.NvidiaBranch = f.driver(out.Backend)
	}
	if out.NodeID == "" && out.Codename == "" && out.Arch == "" && out.LlamaCommit == "" {
		return nil
	}
	return out
}

// driver is the CUDA version the installed driver supports and its branch ("595"), "" for either when
// nvidia-smi is missing or does not answer.
func (f *nodeFacts) driver(backend string) (cuda, branch string) {
	now := f.d.env.Clock.Now()
	f.mu.Lock()
	if f.driverBackend == backend && now.Sub(f.driverAt) < factsTTL && !f.driverAt.IsZero() {
		cuda, branch = f.driverCUDA, f.driverBranch
		f.mu.Unlock()
		return cuda, branch
	}
	f.mu.Unlock()
	cuda, branch = f.readDriver(context.Background())
	f.mu.Lock()
	f.driverCUDA, f.driverBranch, f.driverAt, f.driverBackend = cuda, branch, now, backend
	f.mu.Unlock()
	return cuda, branch
}

var (
	fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	agentTagOK = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+.][0-9A-Za-z.-]+)?$`)
)

// buildCommit is the full commit a build was made from: the "commit" of its BUILD.json (node.yaml keeps
// only the first 9 characters, and a pin needs all 40). "" when there is no build or it is unreadable.
func buildCommit(path string) string {
	raw, err := config.OSFS{}.ReadFile(path)
	if err != nil {
		return ""
	}
	var b struct {
		Commit string `json:"commit"`
	}
	if json.Unmarshal(raw, &b) != nil || !fullCommit.MatchString(b.Commit) {
		return ""
	}
	return b.Commit
}

// nodePins is what this node reports into kit/catalog/pins.json (kit.Report): its llama.cpp and
// whisper.cpp commits (from the BUILD.json of the build in use), the agent tag, the Ubuntu codename and
// architecture, and on a CUDA node the driver branch. A fact that is unknown or not in the form a pin
// takes is left out. ok is false when nothing is known.
func (f *nodeFacts) nodePins() (kit.Pins, bool) {
	facts := f.kitNode()
	if facts == nil || facts.NodeID == "" {
		return kit.Pins{}, false
	}
	p := kit.NodePins{
		NodeID: facts.NodeID, Codename: facts.Codename, Arch: facts.Arch, NvidiaBranch: facts.NvidiaBranch,
		LlamaCommit:   buildCommit(f.d.c.Paths.BuildJSON()),
		WhisperCommit: buildCommit(f.d.c.Paths.Join(whisper.Root + "/current/BUILD.json")),
	}
	if agentTagOK.MatchString(facts.AgentVersion) {
		p.AgentTag = "node-v" + facts.AgentVersion
	}
	pins, err := kit.Report(p, f.d.env.Clock.Now())
	if err != nil {
		f.d.log.Warn("this node's kit pins are not valid and are not reported", "code", kit.CodePinsDamaged, "error", err.Error())
		return kit.Pins{}, false
	}
	return pins, true
}

// readDriver asks nvidia-smi for the CUDA version the driver supports (the "CUDA Version: X.Y" of its
// header) and the major version of the driver (the branch the NVIDIA closure is for).
func (f *nodeFacts) readDriver(ctx context.Context) (cuda, branch string) {
	d := f.d
	lookup := d.env.Lookup
	if lookup == nil {
		lookup = execx.Lookup
	}
	bin, err := lookup("nvidia-smi")
	if err != nil {
		return "", ""
	}
	query := func(args ...string) string {
		res, err := d.env.Runner.Run(ctx, execx.Cmd{Path: bin, Args: args, Timeout: nvidiaSmiTimeout, MaxOut: 64 << 10})
		if err != nil || res.ExitCode != 0 {
			return ""
		}
		return string(res.Stdout)
	}
	cuda = rbuild.ParseDriverCUDA(query())
	if v := strings.TrimSpace(strings.SplitN(query("--query-gpu=driver_version", "--format=csv,noheader"), "\n", 2)[0]); v != "" {
		branch, _, _ = strings.Cut(v, ".")
	}
	return cuda, branch
}

// ---- the drive events ---------------------------------------------------------------------------

// rootGoner is the part of the sync engine the drive events use.
type rootGoner interface{ RootGone(rootID string) bool }

// driveReady is the part of the readiness service the drive events use.
type driveReady interface {
	Mounted(ctx context.Context, rootID string) error
	Unmounted(rootID string)
}

// driveIndex is the part of the best-copy index the drive events use.
type driveIndex interface {
	Rebuild(ctx context.Context, roots []ark.Root) ([]ark.Problem, error)
}

// driveEvents reacts to drive.mounted and drive.unmounted (ark-sync.md section 4.1): a drive that
// appeared is read into the index and gets a READY report; a drive that went away stops the transfers
// writing to it (their items end skipped, root_gone, LAI-ARK-007), leaves the index and loses its
// report. The other roots go on.
type driveEvents struct {
	engine rootGoner
	ready  driveReady
	index  driveIndex
	roots  func() []ark.Root
	log    *slog.Logger
	// observe, when set, is told after an event has been handled (tests wait on it instead of sleeping).
	observe func(typ, rootID string)
}

// handle applies one event; an event of another type is ignored.
func (h driveEvents) handle(ctx context.Context, typ string, rootID string) {
	switch typ {
	case "drive.unmounted":
		active := h.engine.RootGone(rootID)
		h.ready.Unmounted(rootID)
		if _, err := h.index.Rebuild(ctx, h.roots()); err != nil {
			h.log.Warn("the best-copy index could not be rebuilt after a drive left", "root", rootID, "error", err.Error())
		}
		h.log.Info("a content root is gone", "root", rootID, "code", doctor.ARK007, "run_was_using_it", active)
	case "drive.mounted":
		if _, err := h.index.Rebuild(ctx, h.roots()); err != nil {
			h.log.Warn("the best-copy index could not be rebuilt after a drive arrived", "root", rootID, "error", err.Error())
		}
		if err := h.ready.Mounted(ctx, rootID); err != nil {
			h.log.Warn("the readiness report of a new drive could not be written", "root", rootID, "error", err.Error())
		}
	default:
		return
	}
	if h.observe != nil {
		h.observe(typ, rootID)
	}
}

// ---- views, speech and whisper -------------------------------------------------------------------

// arkView is the ark view of /status: the readiness service. A nil interface (not a typed nil) when the
// stack did not come up, so the builder renders its empty state.
func (d *Daemon) arkView() statusapi.ArkView {
	if d.ark == nil {
		return nil
	}
	return d.ark.ready
}

// contentView says what the roots hold of each catalog content item (GET /catalog).
func (d *Daemon) contentView() statusapi.ContentView {
	if d.ark == nil {
		return nil
	}
	return d.ark.ready
}

// piperDeps are the dependencies of the speech routes and the piper health probe: the node.yaml piper
// block as it is now, the catalog's voices (those of the profile first) and the best-copy index, which
// is what says a voice is installed.
func (d *Daemon) piperDeps(m *health.Metrics) piper.Deps {
	var cred *execx.Credential
	if d.haveLocalAI && !d.env.Unprivileged {
		cred = &execx.Credential{UID: uint32(d.localaiUID), GID: uint32(d.localaiGID)}
	}
	return piper.Deps{
		Config:  func() piper.Config { return piper.FromConfig(d.c.Config.Get()) },
		Catalog: piperVoices{d}, Copies: d.c.ArkIndex, Clock: d.env.Clock, Metrics: m, Logger: d.log,
		Root: d.c.Paths.Join(piper.DefaultRoot), WorkDir: d.c.Paths.Join(piper.DefaultWorkDir), Credential: cred,
	}
}

// piperVoices is piper.VoiceCatalog over the catalog in use and the node's profile.
type piperVoices struct{ d *Daemon }

// PiperVoices implements piper.VoiceCatalog.
func (p piperVoices) PiperVoices() []piper.CatalogVoice {
	var planned []string
	if prof := p.d.profile(); prof != nil {
		planned = prof.Include.Models
	}
	return piper.CatalogVoices(p.d.c.Catalog.Get().Catalog, planned)
}

// whisperBackend is the backend whisper should have been built for: whisper.backend of node.yaml, with
// "auto" (or none) meaning llama.backend.
func whisperBackend(cfg config.Config) string {
	var b struct {
		Backend string `yaml:"backend"`
	}
	if raw, ok := cfg.Blocks["whisper"]; ok {
		_ = raw.Decode(&b)
	}
	if b.Backend == "" || b.Backend == "auto" {
		return cfg.Llama.Backend
	}
	return b.Backend
}

// driveWorker is the app Worker that feeds the drive events of the bus to driveEvents.
type driveWorker struct {
	bus    *events.Bus
	h      driveEvents
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (w *driveWorker) Name() string { return "ark-drive-events" }

// Start implements Worker: it subscribes before it returns, so an event published after Start is seen.
func (w *driveWorker) Start(ctx context.Context) error {
	filter, err := events.ParseFilter(events.TypeDriveMounted + "," + events.TypeDriveUnmounted)
	if err != nil {
		return err
	}
	ctx, w.cancel = context.WithCancel(ctx)
	ch, err := w.bus.Subscribe(ctx, filter, 0)
	if err != nil {
		w.cancel()
		return err
	}
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		for ev := range ch {
			switch ev.Type {
			case events.TypeDriveMounted:
				var m events.DriveMounted
				if json.Unmarshal(ev.Data, &m) == nil {
					w.h.handle(ctx, ev.Type, m.RootID)
				}
			case events.TypeDriveUnmounted:
				var u events.DriveUnmounted
				if json.Unmarshal(ev.Data, &u) == nil {
					w.h.handle(ctx, ev.Type, u.RootID)
				}
			}
		}
	}()
	return nil
}

// Stop implements Worker.
func (w *driveWorker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// driveWorker builds the worker of the stack's drive events.
func (s *arkStack) driveWorker() *driveWorker {
	return &driveWorker{bus: s.d.c.Bus, h: driveEvents{
		engine: s.engine, ready: s.ready, index: s.index, roots: s.roots.List, log: s.d.log.With("component", "ark"),
		observe: s.d.env.OnDriveEvent,
	}}
}
