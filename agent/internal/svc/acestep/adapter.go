package acestep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/netx"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// The engine states the adapter reports (GET /music/status engine_state, E11 contract section 3).
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateReady    = "ready"
	StateStopping = "stopping"
	StateFailed   = "failed"
)

// Shim health values (GET /health of the shim, E11 contract section 2).
const (
	HealthLoading = "loading"
	HealthReady   = "ready"
	HealthError   = "error"
)

// Timing defaults of the adapter.
const (
	DefaultPoll        = time.Second
	DefaultStopTimeout = 30 * time.Second
	// DefaultFreeTimeout bounds the wait for the graphics memory after the unit stopped, polled every
	// DefaultFreePoll (llama-router.md section 4.15.3).
	DefaultFreeTimeout = 15 * time.Second
	DefaultFreePoll    = 500 * time.Millisecond
	healthTimeout      = 3 * time.Second
)

// Units is what the adapter needs from systemd; internal/services.Manager (behind the allowlist
// guard) implements it.
type Units interface {
	State(ctx context.Context, unit string) (services.UnitState, error)
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
}

// Health is the shim's GET /health answer.
type Health struct {
	Status  string  `json:"status"`
	ModelID string  `json:"model_id"`
	Device  string  `json:"device"`
	DType   string  `json:"dtype"`
	VRAMMiB *int64  `json:"vram_mib"`
	Error   *string `json:"error"`
}

// HealthProber asks the shim how it is.
type HealthProber interface {
	Health(ctx context.Context) (Health, error)
}

// HTTPHealth probes GET http://127.0.0.1:<port>/health through a loopback-only, fenced transport.
type HTTPHealth struct {
	// Port returns ports.music of node.yaml; nil or 0 means 8082.
	Port func() int
	// Client is the HTTP client; nil means a loopback-only client behind netx.
	Client *http.Client
}

var defaultHealthClient = &http.Client{
	Transport: netx.NewTransport("svc.acestep", netx.LoopbackDial(time.Second)),
	Timeout:   healthTimeout,
}

// Health implements HealthProber.
func (h HTTPHealth) Health(ctx context.Context) (Health, error) {
	port := DefaultPort
	if h.Port != nil {
		if p := h.Port(); p > 0 {
			port = p
		}
	}
	c := h.Client
	if c == nil {
		c = defaultHealthClient
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Health{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("acestep: GET /health answered %d", resp.StatusCode)
	}
	var out Health
	if err := json.Unmarshal(body, &out); err != nil {
		return Health{}, fmt.Errorf("acestep: GET /health: %w", err)
	}
	return out, nil
}

// Options configures NewAdapter. Units, Health and Env are required.
type Options struct {
	Units  Units
	Health HealthProber
	// Env writes music.env (EnvFile.Write).
	Env func(Env) error
	// EnvBase gives what node.yaml and the machine fix in music.env (port, device, dtype, queue);
	// ModelID and ModelDir are filled per start. nil means the defaults.
	EnvBase func() Env
	// Installed is nil when the runtime is installed; default: the runtime's interpreter exists.
	Installed func() error
	// GPUHeld, when set, reports the graphics memory the unit's processes still hold (and whether it
	// could be measured). A stop waits until it is 0; without it the stop waits for the unit only.
	GPUHeld func(ctx context.Context) (mib int64, known bool)
	// Poll is the cadence of the waits; StopTimeout bounds the wait for the unit to stop, FreeTimeout
	// the wait for its graphics memory afterwards (polled every FreePoll).
	Poll, StopTimeout, FreeTimeout, FreePoll time.Duration
	Clock             config.Clock
	// Sleep waits between polls; tests advance a clock in it.
	Sleep  func(ctx context.Context, d time.Duration)
	Logger *slog.Logger
}

// Status is the engine's state for GET /music/status.
type Status struct {
	State   string
	ModelID string
	// VRAMMiB is the last graphics memory the shim reported; 0 when unknown.
	VRAMMiB int64
	// Error is the last failure, in English (never lyrics or file content).
	Error string
}

// Adapter is the router.EngineAdapter of engine acestep. The orchestrator calls Start, Stop and
// Resident under modelOpsMu; Cached and Status read the cached picture without I/O and are safe at
// any time.
type Adapter struct {
	opt Options
	log *slog.Logger

	mu sync.Mutex
	st Status
}

var _ router.EngineAdapter = (*Adapter)(nil)

// NewAdapter returns an Adapter.
func NewAdapter(opt Options) (*Adapter, error) {
	if opt.Units == nil || opt.Health == nil || opt.Env == nil {
		return nil, errors.New("acestep: the adapter needs Units, Health and Env")
	}
	if opt.Poll <= 0 {
		opt.Poll = DefaultPoll
	}
	if opt.StopTimeout <= 0 {
		opt.StopTimeout = DefaultStopTimeout
	}
	if opt.FreeTimeout <= 0 {
		opt.FreeTimeout = DefaultFreeTimeout
	}
	if opt.FreePoll <= 0 {
		opt.FreePoll = DefaultFreePoll
	}
	if opt.Clock == nil {
		opt.Clock = config.RealClock{}
	}
	if opt.Sleep == nil {
		opt.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if opt.Installed == nil {
		opt.Installed = Runtime{}.Installed
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Adapter{opt: opt, log: l.With("component", "acestep"), st: Status{State: StateStopped}}, nil
}

// RuntimeInstalled is nil when the interpreter of the runtime at path is a program. stat nil means
// os.Stat.
func RuntimeInstalled(stat func(string) (os.FileInfo, error), path string) error {
	if stat == nil {
		stat = os.Stat
	}
	fi, err := stat(path)
	switch {
	case err != nil:
		return fmt.Errorf("acestep: the music runtime is not installed (%s): %w", path, err)
	case !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0:
		return fmt.Errorf("acestep: %s is not a program", path)
	}
	return nil
}

// Engine implements router.EngineAdapter.
func (a *Adapter) Engine() string { return catalog.EngineAcestep }

// Installed implements router.EngineAdapter.
func (a *Adapter) Installed() error { return a.opt.Installed() }

// Status returns the cached engine state.
func (a *Adapter) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st
}

// Cached returns what the engine holds as last seen, without I/O (GET /models).
func (a *Adapter) Cached() []router.EngineModel {
	s := a.Status()
	switch s.State {
	case StateStarting, StateReady:
		if s.ModelID != "" {
			return []router.EngineModel{{ID: s.ModelID, Ready: s.State == StateReady, VRAMMiB: s.VRAMMiB}}
		}
	}
	return nil
}

func (a *Adapter) set(state, model, msg string) {
	a.mu.Lock()
	a.st.State, a.st.ModelID, a.st.Error = state, model, msg
	if state != StateReady {
		a.st.VRAMMiB = 0
	}
	a.mu.Unlock()
}

// running reports whether a unit state holds a process (or is about to).
func running(s services.UnitState) bool {
	switch s.ActiveState {
	case services.ActiveActive, services.ActiveActivating, services.ActiveReloading, services.ActiveDeactivating:
		return true
	}
	return false
}

// Resident implements router.EngineAdapter: nothing when the unit is not running (a process that was
// killed is noticed here), else the model the engine runs, ready when the shim says so.
func (a *Adapter) Resident(ctx context.Context) ([]router.EngineModel, error) {
	us, err := a.opt.Units.State(ctx, UnitName)
	if err != nil {
		return nil, err
	}
	cur := a.Status()
	if !running(us) {
		if cur.State == StateReady || cur.State == StateStarting {
			a.log.Warn("the music engine is not running any more", "model", cur.ModelID, "active", us.ActiveState)
			a.set(StateStopped, "", "the engine stopped")
		}
		return nil, nil
	}
	h, herr := a.opt.Health.Health(ctx)
	model := cur.ModelID
	if herr == nil && h.ModelID != "" {
		model = h.ModelID
	}
	if model == "" {
		return nil, nil // a unit nobody started for a model; it is stopped by the next load or unload
	}
	em := router.EngineModel{ID: model, Ready: herr == nil && h.Status == HealthReady}
	if herr == nil && h.VRAMMiB != nil && *h.VRAMMiB > 0 {
		em.VRAMMiB = *h.VRAMMiB
	}
	a.mu.Lock()
	a.st.ModelID = model
	if em.Ready {
		a.st.State, a.st.VRAMMiB = StateReady, em.VRAMMiB
	} else if a.st.State == StateStopped {
		a.st.State = StateStarting
	}
	a.mu.Unlock()
	return []router.EngineModel{em}, nil
}

func engineErr(code router.Code, cause string, err error) *router.EngineError {
	return &router.EngineError{Code: code, Cause: cause, Err: err}
}

// Start implements router.EngineAdapter: stop what runs, write music.env for the model, start the
// unit and wait for the shim to answer ready for this model. A unit that fails or exits while
// starting is LAI-LLM-040; a shim that is not ready within the timeout is LAI-LLM-041 (the unit is
// left for the orchestrator's rollback to stop).
func (a *Adapter) Start(ctx context.Context, req router.EngineStart) error {
	if req.Model == nil {
		return engineErr(router.CodeEngineStart, "no_model", errors.New("acestep: no model"))
	}
	id := req.Model.ID
	if us, err := a.opt.Units.State(ctx, UnitName); err == nil && running(us) {
		if err := a.stopAndWait(ctx); err != nil {
			a.set(StateFailed, "", err.Error())
			return engineErr(router.CodeEngineStart, "replace", err)
		}
	}
	a.set(StateStarting, id, "")
	env := Env{}
	if a.opt.EnvBase != nil {
		env = a.opt.EnvBase()
	}
	env.ModelID, env.ModelDir = id, req.Dir
	if err := a.opt.Env(env); err != nil {
		a.set(StateFailed, "", err.Error())
		return engineErr(router.CodeEngineStart, "env", err)
	}
	if err := a.opt.Units.Start(ctx, UnitName); err != nil {
		a.set(StateFailed, "", err.Error())
		return engineErr(router.CodeEngineStart, "unit", err)
	}
	start := a.opt.Clock.Now()
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = router.DefaultLoadTimeout
	}
	for {
		if us, err := a.opt.Units.State(ctx, UnitName); err == nil && !running(us) {
			msg := fmt.Sprintf("the engine exited while starting (%s, status %d)", us.ActiveState, us.ExecMainStatus)
			a.set(StateFailed, "", msg)
			return engineErr(router.CodeEngineStart, "exited", errors.New(msg))
		}
		if h, err := a.opt.Health.Health(ctx); err == nil {
			switch {
			case h.Status == HealthReady && (h.ModelID == "" || h.ModelID == id):
				a.mu.Lock()
				a.st = Status{State: StateReady, ModelID: id}
				if h.VRAMMiB != nil {
					a.st.VRAMMiB = *h.VRAMMiB
				}
				a.mu.Unlock()
				a.log.Info("music engine ready", "model", id, "device", h.Device, "dtype", h.DType)
				return nil
			case h.Status == HealthError:
				msg := "the engine could not load the model"
				if h.Error != nil {
					msg += ": " + clip(*h.Error, 200)
				}
				a.set(StateFailed, id, msg)
				return engineErr(router.CodeEngineStart, "engine_error", errors.New(msg))
			}
		}
		if ctx.Err() != nil {
			return router.ErrInterrupted
		}
		if a.opt.Clock.Now().Sub(start) >= timeout {
			a.set(StateFailed, id, "the engine was not ready in time")
			return engineErr(router.CodeEngineNotReady, "health", fmt.Errorf("acestep: not ready after %s", timeout))
		}
		a.opt.Sleep(ctx, a.opt.Poll)
	}
}

// Stop implements router.EngineAdapter: stop the unit (whatever model it runs; id is only for the
// log) and wait until it is gone and, when it can be measured, its graphics memory is free.
func (a *Adapter) Stop(ctx context.Context, id string) error {
	us, err := a.opt.Units.State(ctx, UnitName)
	if err == nil && !running(us) {
		a.set(StateStopped, "", "")
		return nil
	}
	if err := a.stopAndWait(ctx); err != nil {
		// The model counts as unloaded (llama-router.md section 4.15.3 step 4); the next plan
		// measures G_others again, so a stuck process makes it evict more, never over-commit.
		a.set(StateStopped, "", err.Error())
		a.log.Warn("the music engine did not stop cleanly", "model", id, "code", string(router.CodeEngineNotFreed), "error", err)
		return engineErr(router.CodeEngineNotFreed, "stop", err)
	}
	a.set(StateStopped, "", "")
	return nil
}

// stopAndWait stops the unit and waits for the process and its graphics memory to be gone.
func (a *Adapter) stopAndWait(ctx context.Context) error {
	cur := a.Status()
	a.set(StateStopping, cur.ModelID, "")
	if err := a.opt.Units.Stop(ctx, UnitName); err != nil {
		return fmt.Errorf("acestep: stop %s: %w", UnitName, err)
	}
	start := a.opt.Clock.Now()
	for {
		if us, err := a.opt.Units.State(ctx, UnitName); err == nil && !running(us) {
			break
		}
		if a.opt.Clock.Now().Sub(start) >= a.opt.StopTimeout {
			return fmt.Errorf("acestep: %s did not stop within %s", UnitName, a.opt.StopTimeout)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.opt.Sleep(ctx, a.opt.Poll)
	}
	if a.opt.GPUHeld == nil {
		return nil
	}
	start = a.opt.Clock.Now()
	for {
		held, known := a.opt.GPUHeld(ctx)
		if !known || held <= 0 {
			return nil
		}
		if a.opt.Clock.Now().Sub(start) >= a.opt.FreeTimeout {
			return fmt.Errorf("acestep: %d MiB of graphics memory still held %s after the stop", held, a.opt.FreeTimeout)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.opt.Sleep(ctx, a.opt.FreePoll)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
