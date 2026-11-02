package auth

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Session cookie and lifetimes (agent.md sections 3.8.2 and 3.9.2).
const (
	// SessionCookie names the operator session cookie.
	SessionCookie = "localai_session"
	// SessionIdle is how long a session may go unused before it expires.
	SessionIdle = 12 * time.Hour
	// SessionAbsolute is the longest a session lives, however often it is used.
	SessionAbsolute = 7 * 24 * time.Hour

	// maxSessions bounds the memory a flood of POST /session can take; the least recently used go first.
	maxSessions = 256
	// tombstoneFor is how long an expired session is remembered, only to answer LAI-SEC-012 instead of
	// LAI-SEC-001 to the browser that still holds its cookie.
	tombstoneFor = 24 * time.Hour
	// sweepEvery is how often Lookup drops old entries.
	sweepEvery = time.Minute
)

// SessionState is what Lookup found.
type SessionState int

// The outcomes of Sessions.Lookup.
const (
	// SessionUnknown: no such session (never existed, deleted, or lost to a restart).
	SessionUnknown SessionState = iota
	// SessionValid: a live session; its idle timer was restarted.
	SessionValid
	// SessionExpired: the session existed and ran out (idle or absolute).
	SessionExpired
)

// Sessions is the in-memory store of operator sessions. A session is created only from an operator
// credential (a bearer token or a login code); the cookie value is 32 random bytes, and the store
// keeps only its sha256, so neither a lookup nor a memory dump yields a usable cookie.
type Sessions struct {
	clock Clock

	mu        sync.Mutex
	m         map[string]*session
	lastSweep time.Time
}

type session struct {
	created  time.Time
	lastSeen time.Time
	expired  time.Time // zero while live
}

// NewSessions returns an empty store.
func NewSessions(clock Clock) *Sessions {
	if clock == nil {
		clock = RealClock{}
	}
	return &Sessions{clock: clock, m: map[string]*session{}, lastSweep: clock.Now()}
}

// Create starts a session and returns the cookie value and the session's internal id.
func (s *Sessions) Create() (cookie, id string) {
	now := s.clock.Now()
	cookie = randomToken(32)
	id = secretKey(cookie)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if s.liveLocked() >= maxSessions {
		s.evictOldestLocked()
	}
	s.m[id] = &session{created: now, lastSeen: now}
	return cookie, id
}

// Lookup resolves a cookie value. A live session is touched (its idle timer restarts); an expired
// one is reported as such, once, and stays known for a day. A session expires when its idle time
// reaches SessionIdle or its age reaches SessionAbsolute (the boundary instant counts as expired).
func (s *Sessions) Lookup(cookie string) (id string, state SessionState) {
	if cookie == "" {
		return "", SessionUnknown
	}
	id = secretKey(cookie)
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	sess, ok := s.m[id]
	if !ok {
		return "", SessionUnknown
	}
	if sess.expired.IsZero() && s.runOutLocked(sess, now) {
		sess.expired = now
	}
	if !sess.expired.IsZero() {
		return id, SessionExpired
	}
	sess.lastSeen = now
	return id, SessionValid
}

// Delete ends a session by its internal id. It reports whether the session was live.
func (s *Sessions) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	delete(s.m, id)
	return ok && sess.expired.IsZero()
}

// DropAll ends every session.
func (s *Sessions) DropAll() {
	s.mu.Lock()
	s.m = map[string]*session{}
	s.mu.Unlock()
}

// Len returns the number of live sessions.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveLocked()
}

func (s *Sessions) runOutLocked(sess *session, now time.Time) bool {
	return !now.Before(sess.lastSeen.Add(SessionIdle)) || !now.Before(sess.created.Add(SessionAbsolute))
}

func (s *Sessions) liveLocked() int {
	n := 0
	for _, sess := range s.m {
		if sess.expired.IsZero() {
			n++
		}
	}
	return n
}

// sweepLocked marks the sessions that ran out and forgets those expired for more than tombstoneFor.
func (s *Sessions) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < sweepEvery {
		return
	}
	s.lastSweep = now
	for id, sess := range s.m {
		if sess.expired.IsZero() {
			if s.runOutLocked(sess, now) {
				sess.expired = now
			}
			continue
		}
		if now.Sub(sess.expired) >= tombstoneFor {
			delete(s.m, id)
		}
	}
}

func (s *Sessions) evictOldestLocked() {
	type entry struct {
		id   string
		seen time.Time
	}
	var live []entry
	for id, sess := range s.m {
		if sess.expired.IsZero() {
			live = append(live, entry{id, sess.lastSeen})
		}
	}
	if len(live) == 0 {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].seen.Before(live[j].seen) })
	delete(s.m, live[0].id)
}

// SessionCookieHeader is the Set-Cookie value that creates the cookie (agent.md section 3.8.2).
func SessionCookieHeader(value string) string {
	return fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Strict; Max-Age=%d", SessionCookie, value, int(SessionAbsolute/time.Second))
}

// ClearSessionCookieHeader is the Set-Cookie value that deletes it.
func ClearSessionCookieHeader() string {
	return SessionCookie + "=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0"
}
