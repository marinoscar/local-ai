package music

import (
	"context"
	"errors"
	"time"
)

// Engine states of GET /music/status (E11 contract section 3).
const (
	EngineStopped  = "stopped"
	EngineStarting = "starting"
	EngineReady    = "ready"
	EngineStopping = "stopping"
	EngineFailed   = "failed"
)

// Engine tells whether music is loaded: the state of the music engine and the model it runs ("" when
// none). This is the seam to the acestep engine adapter of the router (go-core, #508): the adapter
// knows "starting" and "stopping" from the unit it drives and can implement Engine itself. Until it is
// wired, HealthProbe answers from the shim's GET /health alone.
type Engine interface {
	EngineStatus() (state string, modelID string)
}

// EngineFunc adapts a function to Engine.
type EngineFunc func() (string, string)

// EngineStatus implements Engine.
func (f EngineFunc) EngineStatus() (string, string) { return f() }

// HealthProbe is an Engine over the shim's GET /health: ready is ready, loading is starting, error
// is failed, and no answer at all is stopped (the unit does not run).
type HealthProbe struct {
	Shim    Shim
	Timeout time.Duration // default 2 s
}

// EngineStatus implements Engine.
func (p HealthProbe) EngineStatus() (string, string) {
	t := p.Timeout
	if t <= 0 {
		t = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	h, err := p.Shim.Health(ctx)
	if err != nil {
		if errors.Is(err, ErrUnreachable) {
			return EngineStopped, ""
		}
		return EngineFailed, ""
	}
	switch h.Status {
	case HealthReady:
		return EngineReady, h.ModelID
	case HealthLoading:
		return EngineStarting, h.ModelID
	}
	return EngineFailed, h.ModelID
}
