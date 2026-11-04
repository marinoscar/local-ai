package portal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/auth"
)

// The Portal session (survival-portal.md section 3.3): a restricted cookie the agent issues to a
// browser that opens /portal while the node is in survival mode. The store is memory only, so an
// agent restart ends every session; the SPA reloads /portal on 401 LAI-SVC-110.
const (
	// Cookie is the name of the Portal session cookie.
	Cookie = auth.PortalCookie
	// SessionTTL is the sliding expiry: last_seen is refreshed on every accepted request.
	SessionTTL = 12 * time.Hour
	// CookieMaxAge is the Max-Age of the cookie, in seconds.
	CookieMaxAge = 43200
)

// ModeSurvival is the only mode in which a Portal session is issued or accepted.
const ModeSurvival = "survival"

// SessionOptions configures Sessions. Every field is optional.
type SessionOptions struct {
	// Mode returns the current mode name. Nil means the node is always in survival mode (tests).
	Mode func() string
	// Now is the clock; default time.Now.
	Now func() time.Time
	// Rand supplies the 32 random bytes of a cookie; default crypto/rand.
	Rand io.Reader
	// Logger gets portal.session_issued (never the cookie value); default slog.Default.
	Logger *slog.Logger
	// Metrics receives localai_portal_sessions_active and the rate-limit counter; nil ignores them.
	Metrics *Metrics
}

type session struct {
	id       string // a short non-secret label derived from the cookie: audit, rate limits, logs
	issuedAt time.Time
	lastSeen time.Time
	hits     map[string][]time.Time // rate-limit windows by bucket
}

// Sessions is the store of Portal sessions. It implements auth.PortalSessions (and so the
// modes.PortalSessions the mode engine drops when leaving survival).
type Sessions struct {
	opt SessionOptions
	log *slog.Logger

	mu     sync.Mutex
	byHash map[[32]byte]*session
	byID   map[string]*session
}

var _ auth.PortalSessions = (*Sessions)(nil)

// NewSessions returns an empty store.
func NewSessions(o SessionOptions) *Sessions {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	s := &Sessions{opt: o, log: o.Logger, byHash: map[[32]byte]*session{}, byID: map[string]*session{}}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "portal")
	o.Metrics.SetSessionsGauge(s.Count)
	return s
}

func (s *Sessions) survival() bool { return s.opt.Mode == nil || s.opt.Mode() == ModeSurvival }

func hashOf(cookie string) [32]byte { return sha256.Sum256([]byte(cookie)) }

// labelOf is the session id other code sees: eight characters of the hash of the cookie, which say
// nothing about the cookie itself.
func labelOf(h [32]byte) string { return base64.RawURLEncoding.EncodeToString(h[:6]) }

// Issue creates a session and returns its cookie value: 32 random bytes as 43 base64url characters.
// The caller decides whether a session may be issued (Hook checks the mode).
func (s *Sessions) Issue() (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(s.opt.Rand, b[:]); err != nil {
		return "", fmt.Errorf("portal: the random source failed: %w", err)
	}
	cookie := base64.RawURLEncoding.EncodeToString(b[:])
	h := hashOf(cookie)
	now := s.opt.Now()
	sess := &session{id: labelOf(h), issuedAt: now, lastSeen: now, hits: map[string][]time.Time{}}
	s.mu.Lock()
	s.byHash[h] = sess
	s.byID[sess.id] = sess
	s.mu.Unlock()
	return cookie, nil
}

// Lookup implements auth.PortalSessions. A known cookie of a live session, while the node is in
// survival, resolves to the session id and refreshes last_seen. An expired session is forgotten.
func (s *Sessions) Lookup(cookie string) (string, bool) {
	if cookie == "" || len(cookie) > 128 {
		return "", false
	}
	h := hashOf(cookie)
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byHash[h]
	if !ok {
		return "", false
	}
	if now.Sub(sess.lastSeen) >= SessionTTL {
		s.dropLocked(h, sess)
		return "", false
	}
	if !s.survival() {
		return "", false // outside survival a cookie resolves to no principal
	}
	sess.lastSeen = now
	return sess.id, true
}

// Allowed implements auth.PortalSessions. The per-route scope is the Auth list of each route (a
// Portal principal satisfies only the routes that list "portal"); here the only request-level rule is
// that the node is in survival mode.
func (s *Sessions) Allowed(*http.Request) bool { return s.survival() }

// DropAll implements auth.PortalSessions and modes.PortalSessions: every session ends. The mode
// engine calls it synchronously when the node leaves survival.
func (s *Sessions) DropAll() {
	s.mu.Lock()
	s.byHash = map[[32]byte]*session{}
	s.byID = map[string]*session{}
	s.mu.Unlock()
}

// InvalidateAll is DropAll under the name of the spec.
func (s *Sessions) InvalidateAll() { s.DropAll() }

// Count is the number of live sessions (localai_portal_sessions_active). Expired ones are removed.
func (s *Sessions) Count() int {
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sess := range s.byHash {
		if now.Sub(sess.lastSeen) >= SessionTTL {
			s.dropLocked(h, sess)
		}
	}
	return len(s.byHash)
}

func (s *Sessions) dropLocked(h [32]byte, sess *session) {
	delete(s.byHash, h)
	delete(s.byID, sess.id)
}

// Hook is api.SPAOptions.PortalHook: on GET /portal in survival mode it sets the lai_portal cookie
// unless the request already carries a valid one. Outside survival it sets nothing. The Host check
// has already run (a request that fails it never reaches the page). The cookie value is never logged.
func (s *Sessions) Hook(w http.ResponseWriter, r *http.Request) {
	if !s.survival() {
		return
	}
	if c, err := r.Cookie(Cookie); err == nil {
		if _, ok := s.Lookup(c.Value); ok {
			return
		}
	}
	cookie, err := s.Issue()
	if err != nil {
		s.log.Error("a Portal session could not be issued", "error", err.Error())
		return
	}
	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Strict; Max-Age=%d", Cookie, cookie, CookieMaxAge))
	s.log.Info("portal.session_issued", "event", "portal.session_issued", "session", labelOf(hashOf(cookie)))
}
