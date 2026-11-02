package hardware

import (
	"net/http"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// Route settings of GET /hardware (agent.md sections 3.8 and 3.8.6).
const (
	// RouteTimeout is the handler deadline of GET /hardware.
	RouteTimeout = 10 * time.Second
	// FreshMinInterval is the rate limit of ?fresh=1: one forced sample per this interval.
	FreshMinInterval = 2 * time.Second
)

// HandlerOptions configures the GET /hardware handler.
type HandlerOptions struct {
	// Now is the clock of the ?fresh=1 rate limit. Default time.Now.
	Now func() time.Time
	// FreshMinInterval overrides the rate limit. Default 2 s.
	FreshMinInterval time.Duration
}

// Handler serves GET /hardware.
type Handler struct {
	src Sampler
	now func() time.Time
	min time.Duration

	mu        sync.Mutex
	lastFresh time.Time
}

// NewHandler returns the handler of GET /hardware over a Sampler.
func NewHandler(src Sampler, opts HandlerOptions) *Handler {
	h := &Handler{src: src, now: opts.Now, min: opts.FreshMinInterval}
	if h.now == nil {
		h.now = time.Now
	}
	if h.min <= 0 {
		h.min = FreshMinInterval
	}
	return h
}

// ServeHTTP answers the cached snapshot. With ?fresh=1 it forces a sampling round first, at most
// once per FreshMinInterval across all clients: a request inside the window gets the snapshot of
// the previous forced sample (never an error), so a polling client cannot make the agent run
// nvidia-smi more than every 2 s. A sampler that has not sampled yet is asked to, once.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fresh, err := parseFresh(r.URL.Query().Get("fresh"))
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	var snap Snapshot
	if fresh && h.allowFresh() {
		snap = h.src.Refresh(r.Context())
	} else {
		snap = h.src.Snapshot()
		if snap.SampledAt.IsZero() && h.allowFresh() {
			snap = h.src.Refresh(r.Context())
		}
	}
	api.WriteJSON(w, r, http.StatusOK, snap)
}

// allowFresh reports whether a forced sample may run now, and books it.
func (h *Handler) allowFresh() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if !h.lastFresh.IsZero() && now.Sub(h.lastFresh) < h.min {
		return false
	}
	h.lastFresh = now
	return true
}

func parseFresh(v string) (bool, error) {
	switch v {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	}
	return false, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "fresh", "reason": "must be 1 or 0"})
}

// Route returns the registry entry of GET /hardware: operator level, 10 s deadline.
func Route(src Sampler, opts HandlerOptions) api.Route {
	return api.Route{
		Method:  http.MethodGet,
		Pattern: "/hardware",
		Auth:    []api.Level{api.AuthOp},
		Timeout: RouteTimeout,
		Handler: NewHandler(src, opts),
	}
}

// Register adds GET /hardware to reg (nil means api.DefaultRegistry). The wiring in internal/app
// calls it once the sampler exists; nothing here registers at init time.
func Register(reg *api.Registry, src Sampler, opts HandlerOptions) error {
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(Route(src, opts))
}
