package app

import "github.com/marinoscar/local-ai/agent/internal/portal"

// portalStore is the store of Portal sessions (survival-portal.md section 3.3), created on first
// use: the auth service, the mode engine and the API guard all share it.
func (d *Daemon) portalStore() *portal.Sessions {
	d.portalInit()
	return d.pSessions
}

// portalMetrics are the Portal counters (status requests, sessions, rate limits).
func (d *Daemon) portalMetrics() *portal.Metrics {
	d.portalInit()
	return d.pMetrics
}

func (d *Daemon) portalInit() {
	d.portalOnce.Do(func() {
		d.pMetrics = portal.NewMetrics()
		d.pSessions = portal.NewSessions(portal.SessionOptions{Mode: d.modeName, Now: d.env.Clock.Now, Logger: d.log, Metrics: d.pMetrics})
	})
}
