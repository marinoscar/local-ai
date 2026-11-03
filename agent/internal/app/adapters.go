package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// This file holds the adapters of the wiring: small types that connect the interface one package
// declares to the value another package provides. Packages cannot import each other freely (api
// cannot import router, catalog or modes; hardware imports api), so internal/app is the one place
// that adapts. Each adapter is a few lines and has a test.

// ---- late binding -------------------------------------------------------------------------------

// lazyViews holds the views that two subsystems need of each other (the status builder reads the
// cloud link, the link reads the status builder). The daemon sets them once the second subsystem
// exists; until then each answers its documented empty state.
type lazyViews struct {
	cloud atomic.Pointer[cloud.Link]
}

// Cloud implements statusapi.CloudView.
func (v *lazyViews) Cloud() statusapi.CloudInfo {
	if l := v.cloud.Load(); l != nil {
		return l.Cloud()
	}
	return statusapi.CloudInfo{State: string(cloud.StateUnenrolled)}
}

// claimSlot is the cloud.ClaimLoop the link starts, as the daemon wires it before the command plane
// (#36) exists: it forwards to the loop that UseClaimPlane supplies, and does nothing until then.
// The link finds Probe by interface assertion, so the slot has it and forwards it when the loop does.
type claimSlot struct {
	mu       sync.Mutex
	loop     cloud.ClaimLoop
	inflight func() (int, map[string]any)
}

func (s *claimSlot) set(p ClaimPlane) {
	s.mu.Lock()
	s.loop, s.inflight = p.Loop, p.Inflight
	s.mu.Unlock()
}

func (s *claimSlot) get() (cloud.ClaimLoop, func() (int, map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loop, s.inflight
}

// Start implements cloud.ClaimLoop.
func (s *claimSlot) Start(ctx context.Context) {
	if l, _ := s.get(); l != nil {
		l.Start(ctx)
	}
}

// Stop implements cloud.ClaimLoop.
func (s *claimSlot) Stop(ctx context.Context) error {
	if l, _ := s.get(); l != nil {
		return l.Stop(ctx)
	}
	return nil
}

// Probe forwards to the loop's Probe when it has one.
func (s *claimSlot) Probe(ctx context.Context) {
	if l, _ := s.get(); l != nil {
		if p, ok := l.(interface{ Probe(context.Context) }); ok {
			p.Probe(ctx)
		}
	}
}

// Inflight is the cloud.Deps.Inflight of the daemon: the count of commands in flight (0 with no
// command plane).
func (s *claimSlot) Inflight() (int, map[string]any) {
	if _, f := s.get(); f != nil {
		return f()
	}
	return 0, nil
}

// lateLoader is the router.ModelLoader the preset generator reloads models with: the orchestrator,
// which is built after the generator (the orchestrator calls the generator for SetDefault).
type lateLoader struct {
	o atomic.Pointer[router.Orchestrator]
}

func (l *lateLoader) LoadHeld(ctx context.Context, id string) error {
	if o := l.o.Load(); o != nil {
		return o.LoadHeld(ctx, id)
	}
	return errors.New("app: the orchestrator is not built yet")
}

// ---- systemd ------------------------------------------------------------------------------------

// llamaUnit is router.LlamaUnit and router.LlamaRestarter over the guarded systemd Manager: the
// three-line adapters the router package documents.
type llamaUnit struct{ m services.Manager }

func (u llamaUnit) State(ctx context.Context) (router.LlamaUnitState, error) {
	st, err := u.m.State(ctx, services.UnitLlama)
	if err != nil {
		return router.LlamaUnitState{}, err
	}
	switch st.ActiveState {
	case "active", "activating", "reloading":
		return router.LlamaUnitState{Running: true, Enabled: st.Enabled()}, nil
	}
	return router.LlamaUnitState{Enabled: st.Enabled()}, nil
}

func (u llamaUnit) Start(ctx context.Context) error { return u.m.Start(ctx, services.UnitLlama) }

// RestartLlama implements router.LlamaRestarter.
func (u llamaUnit) RestartLlama(ctx context.Context) error {
	return u.m.Restart(ctx, services.UnitLlama)
}

// ---- alerts -------------------------------------------------------------------------------------

// authAlert is auth.AlertFunc over the alert manager (the PIN lockout).
func authAlert(d *Daemon) func(code, severity string, details map[string]any) {
	return func(code, severity string, details map[string]any) {
		d.raiseSeverity(code, doctor.Severity(severity), details)
	}
}

// hardwareChecks adapts the checks of the hardware sampler to doctor checks. The sampler's Result has
// the shape of a doctor result; the runner fills in the texts, so only the code, severity and
// details are carried over.
func hardwareChecks(s *hardware.Service) []doctor.Check {
	var out []doctor.Check
	for _, hc := range s.Checks() {
		hc := hc
		out = append(out, doctor.Check{ID: hc.ID, Area: "HW", Fast: true, Run: func(ctx context.Context) []doctor.Result {
			var rs []doctor.Result
			for _, r := range hc.Run(ctx) {
				rs = append(rs, doctor.Result{Code: r.Code, Severity: doctor.Severity(r.Severity), Details: r.Details})
			}
			return rs
		}})
	}
	return out
}

// ---- hardware -----------------------------------------------------------------------------------

// hardwareWorker runs the real hardware sampler as an app Worker.
type hardwareWorker struct{ s *hardware.Service }

func (w hardwareWorker) Name() string                    { return "hardware" }
func (w hardwareWorker) Start(ctx context.Context) error { w.s.Start(ctx); return nil }
func (w hardwareWorker) Stop(context.Context) error      { w.s.Stop(); return nil }

// powerSource is drill.PowerSource over the hardware snapshot.
type powerSource struct{ h hardware.Sampler }

func (p powerSource) Battery() (drill.BatteryStatus, bool) {
	pw := p.h.Snapshot().Power
	if pw.BatteryPercent == nil {
		return drill.BatteryStatus{}, false
	}
	return drill.BatteryStatus{Percent: *pw.BatteryPercent, OnBattery: pw.Source == hardware.SourceBattery}, true
}

// ---- drill --------------------------------------------------------------------------------------

// llamaActivity is drill.LlamaActivity: llama counts as busy while a model operation holds the
// model-operations lock or a model is loading. The spec's other two signals, a slot processing and a
// chat request in the last ten minutes, need a router /slots client and the chat proxy's last-chat
// tracker, which do not exist yet; they join here when they do.
type llamaActivity struct {
	lock *router.OpLock
	view interface{ View() router.View }
}

func (a llamaActivity) Busy(context.Context) (bool, error) {
	if a.lock != nil {
		if _, held := a.lock.Holder(); held {
			return true, nil
		}
	}
	if a.view != nil {
		for _, m := range a.view.View().Models {
			if m.Status == router.StatusLoading {
				return true, nil
			}
		}
	}
	return false, nil
}

// gauges keeps the drill gauges for node.counters.
type gauges struct {
	mu sync.Mutex
	m  map[string]any
}

func (g *gauges) Set(name string, labels map[string]string, value float64) {
	key := name
	if len(labels) > 0 {
		var parts []string
		for k, v := range labels {
			parts = append(parts, k+"="+v)
		}
		sortStrings(parts)
		key += "{" + strings.Join(parts, ",") + "}"
	}
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]any{}
	}
	g.m[key] = value
	g.mu.Unlock()
}

func (g *gauges) Counters() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]any, len(g.m))
	for k, v := range g.m {
		out[k] = v
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---- the sync engine stub -----------------------------------------------------------------------

// noSyncEngine is the modelsapi.SyncEngine of a daemon without the ark sync engine (SYNC-05 to
// SYNC-08 are a later epic; UseSyncEngine replaces it). It answers what the node truthfully is: with
// no engine there is no writable content root as far as the model routes can tell, so a download is
// refused with modelsapi.ErrNoWritableRoot (422 LAI-ARK-001) and a delete removes nothing and says
// the same, never a plain remove that would skip the keep-last rule. It never panics.
type noSyncEngine struct{}

var _ modelsapi.SyncEngine = noSyncEngine{}

func (noSyncEngine) StartModelDownload(context.Context, modelsapi.SyncDownload) (modelsapi.SyncRun, error) {
	return modelsapi.SyncRun{}, modelsapi.ErrNoWritableRoot
}

func (noSyncEngine) DeleteModel(context.Context, modelsapi.SyncDelete) (modelsapi.SyncDeleteResult, error) {
	return modelsapi.SyncDeleteResult{}, modelsapi.ErrNoWritableRoot
}

// ---- catalog refresh ----------------------------------------------------------------------------

// refresher is statusapi.CatalogRefresher: a catalog.Fetcher built per refresh from the current
// node.yaml (the channel and base URL are live settings) and the live cloud link. A successful
// refresh updates the catalog store the routes, the router and the sync planner read.
type refresher struct{ d *Daemon }

func (r refresher) Refresh(ctx context.Context) (*catalog.RefreshResult, error) {
	return r.d.fetcher().Refresh(ctx)
}

// fetcher builds the catalog fetcher of the current node.yaml: the cloud first when the link can
// serve (it answers ErrNotLinked otherwise, which the fetcher treats as a source that failed), then
// GitHub at the configured base URL. KitDirs are the kit catalogs of the mounted roots.
func (d *Daemon) fetcher() *catalog.Fetcher {
	cfg := d.c.Config.Get()
	f := &catalog.Fetcher{Dir: d.c.Paths.Catalog(), KitDirs: d.kitDirs(), Channel: cfg.Catalog.Channel}
	if d.c.Cloud != nil {
		f.Cloud = d.c.Cloud
	}
	if d.env.CatalogGitHub != nil {
		f.GitHub = d.env.CatalogGitHub
	} else {
		f.GitHub = catalog.NewGitHub(cfg.Catalog.BaseURL)
	}
	return f
}

// kitDirs are the <root>/kit/catalog directories of the mounted content roots, internal root first.
func (d *Daemon) kitDirs() []string {
	var out []string
	for _, r := range d.roots().Ordered() {
		out = append(out, filepath.Join(r, "kit", "catalog"))
	}
	return out
}

// roots are the mounted content roots: the internal root and, when the drive watcher exists, the
// ark drives (SYNC epic).
func (d *Daemon) roots() router.Roots {
	return router.Roots{Internal: d.c.Paths.InternalRoot()}
}

// ---- api glue -----------------------------------------------------------------------------------

// eventsStopping returns the Stopping channel of the server for the SSE handler, which is
// registered before the server exists.
func (d *Daemon) eventsStopping() <-chan struct{} {
	if d.c.API == nil {
		return nil
	}
	return d.c.API.Stopping()
}

// healthOverTransport probes GET /api/v1/health on the unix socket when it is bound, else on TCP:
// the watchdog's "the HTTP server answered an internal /health probe".
func (d *Daemon) healthOverTransport(ctx context.Context) error {
	srv := d.c.API
	if srv == nil {
		return errors.New("the api server is not built")
	}
	tcp, sock := srv.BindErrors()
	var client *http.Client
	var url string
	switch {
	case sock == nil:
		path := srv.SocketPath()
		client = &http.Client{Transport: unixTransport(path), Timeout: 5 * time.Second}
		url = "http://agent" + api.BasePath + "/health"
	case tcp == nil && srv.TCPAddr() != nil:
		client = &http.Client{Timeout: 5 * time.Second}
		url = "http://" + srv.TCPAddr().String() + api.BasePath + "/health"
	default:
		return errors.New("no listener is up")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("/health answered " + resp.Status)
	}
	return nil
}

// unixTransport dials the unix socket at path for every request, whatever the URL says.
func unixTransport(path string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "unix", path)
		},
		DisableKeepAlives: true,
	}
}
