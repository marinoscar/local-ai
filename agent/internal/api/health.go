package api

import "net/http"

// Health is the body of GET /health (agent.md section 3.8.1).
type Health struct {
	// OK is true whenever the agent answers: a damaged or unwritable state degrades into memory and
	// is reported as an alert, never by refusing to serve. The key stays in the body so that
	// clients keep one shape.
	OK bool `json:"ok"`
	// Version is the agent version (aiagent version).
	Version string `json:"version"`
	// Mode is the current mode: online, offline or survival.
	Mode string `json:"mode"`
	// Starting is true until startup step 17 completes (Server.SetStarting).
	Starting bool `json:"starting"`
}

// healthHandler answers from memory: it reads the mode through a function the caller provides and
// the starting flag the server owns, and never touches an upstream. A missing mode source reports
// "offline", the state store's default.
func (s *Server) healthHandler() http.Handler {
	return HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		mode := "offline"
		if s.opts.Mode != nil {
			if m := s.opts.Mode(); m != "" {
				mode = m
			}
		}
		WriteJSON(w, r, http.StatusOK, Health{OK: true, Version: s.opts.Version, Mode: mode, Starting: s.starting.Load()})
		return nil
	})
}
