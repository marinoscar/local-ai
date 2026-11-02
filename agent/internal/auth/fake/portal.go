// Package fake holds the test doubles of internal/auth: a Portal session store and a group lookup.
package fake

import (
	"net/http"
	"sync"
)

// Portal is a fake auth.PortalSessions. Sessions maps a cookie value to a session id; Allow decides
// Allowed (nil means every request is allowed).
type Portal struct {
	mu       sync.Mutex
	sessions map[string]string

	// Allow is consulted by Allowed; nil allows everything.
	Allow func(r *http.Request) bool
	// Dropped counts DropAll calls.
	Dropped int
}

// NewPortal returns an empty store.
func NewPortal() *Portal { return &Portal{sessions: map[string]string{}} }

// Add makes cookie a known Portal session with the given id.
func (p *Portal) Add(cookie, id string) {
	p.mu.Lock()
	p.sessions[cookie] = id
	p.mu.Unlock()
}

// Lookup implements auth.PortalSessions.
func (p *Portal) Lookup(cookie string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.sessions[cookie]
	return id, ok
}

// Allowed implements auth.PortalSessions.
func (p *Portal) Allowed(r *http.Request) bool {
	if p.Allow == nil {
		return true
	}
	return p.Allow(r)
}

// DropAll implements auth.PortalSessions: every session is forgotten.
func (p *Portal) DropAll() {
	p.mu.Lock()
	p.sessions = map[string]string{}
	p.Dropped++
	p.mu.Unlock()
}
