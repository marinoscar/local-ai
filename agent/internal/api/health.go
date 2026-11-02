package api

import "net/http"

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
