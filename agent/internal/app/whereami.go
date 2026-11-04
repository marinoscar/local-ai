package app

import (
	"context"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/maps"
	"github.com/marinoscar/local-ai/agent/internal/maps/gps"
)

// "Where am I" (companion-services.md section 4.4.5, issue SVC-12) plugs in through the extension
// points of hooks.go: the routes and the doctor check at the route phase, the GPS reader as a Worker.
// The service built by the route hook is looked up by the worker factory through whereAmI.

var whereAmI sync.Map // *Components -> *whereAmIParts

type whereAmIParts struct {
	svc *maps.Service
	mgr *gps.Manager
}

func init() {
	RegisterRoutes("maps.whereami", func(reg *api.Registry, c *Components) error {
		cfg := func() gps.Config { return gpsConfig(c.Config.Get()) }
		tracker := gps.NewTracker(nil)
		svc := &maps.Service{Tracker: tracker, Config: cfg, Manual: maps.StateStore{S: c.State}}
		whereAmI.Store(c, &whereAmIParts{svc: svc, mgr: &gps.Manager{Config: cfg, Tracker: tracker}})
		h := &maps.Handler{Service: svc, Origins: func() []string { return maps.SameOrigins(c.Config.Get().Ports.Agent) }}
		if err := reg.Add(h.Routes()...); err != nil {
			return err
		}
		return c.Checks.Add(svc.Check())
	})
	RegisterWorker("maps.gps", func(c *Components) (Worker, error) {
		v, ok := whereAmI.Load(c)
		if !ok {
			return nil, nil
		}
		return &gpsWorker{mgr: v.(*whereAmIParts).mgr}, nil
	})
}

// gpsConfig reads maps.gps of node.yaml; a value that does not parse reads as off, so a typo never
// makes the agent open a serial port the owner did not choose.
func gpsConfig(cfg config.Config) gps.Config {
	var b struct {
		GPS string `yaml:"gps"`
	}
	if raw, ok := cfg.Blocks["maps"]; ok {
		_ = raw.Decode(&b)
	}
	c, err := gps.ParseConfig(b.GPS)
	if err != nil {
		return gps.Config{Mode: gps.ModeOff}
	}
	return c
}

// gpsWorker runs the GPS manager.
type gpsWorker struct {
	mgr    *gps.Manager
	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Worker.
func (w *gpsWorker) Name() string { return "maps-gps" }

// Start implements Worker.
func (w *gpsWorker) Start(ctx context.Context) error {
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() { defer close(w.done); w.mgr.Run(ctx) }()
	return nil
}

// Stop implements Worker.
func (w *gpsWorker) Stop(ctx context.Context) error {
	if w.cancel != nil {
		w.cancel()
		select {
		case <-w.done:
		case <-ctx.Done():
		}
	}
	return nil
}
