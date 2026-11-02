package auth

import (
	"crypto/subtle"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// PIN grant rules (agent.md section 3.9.2).
const (
	// GrantPrefix starts every PIN grant.
	GrantPrefix = "pg_"
	// GrantHeader carries a grant on a request.
	GrantHeader = "X-LocalAI-Pin-Grant"
	// GrantTTL is how long a grant is valid: a fixed five minutes, reusable until then.
	GrantTTL = 5 * time.Minute

	maxGrants = 1024
)

// Grants is the in-memory store of PIN grants. A grant is bound to the principal that earned it
// (a session, a bearer token, a socket uid): presented by anyone else it is just invalid. Only the
// sha256 of the grant is kept.
type Grants struct {
	clock Clock

	mu sync.Mutex
	m  map[string]grant
}

type grant struct {
	binding string
	expires time.Time
}

// NewGrants returns an empty store.
func NewGrants(clock Clock) *Grants {
	if clock == nil {
		clock = RealClock{}
	}
	return &Grants{clock: clock, m: map[string]grant{}}
}

// Mint issues a grant for binding.
func (g *Grants) Mint(binding string) (token string, expires time.Time) {
	now := g.clock.Now()
	token = GrantPrefix + randomToken(32)
	expires = now.Add(GrantTTL)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.purgeLocked(now)
	if len(g.m) >= maxGrants {
		g.evictSoonestLocked()
	}
	g.m[secretKey(token)] = grant{binding: binding, expires: expires}
	return token, expires
}

// Valid reports whether token is a live grant bound to binding. An unknown, expired or foreign grant
// is simply false (the caller answers "grant_expired" for all three, so a grant's existence is not
// revealed). The binding compare is constant time.
func (g *Grants) Valid(token, binding string) bool {
	if !strings.HasPrefix(token, GrantPrefix) || binding == "" {
		return false
	}
	now := g.clock.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[secretKey(token)]
	if !ok {
		return false
	}
	if !now.Before(gr.expires) {
		delete(g.m, secretKey(token))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(gr.binding), []byte(binding)) == 1
}

// DropBinding revokes every grant of a principal (its session ended).
func (g *Grants) DropBinding(binding string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, gr := range g.m {
		if gr.binding == binding {
			delete(g.m, k)
		}
	}
}

// DropPrefix revokes every grant whose binding starts with prefix ("bearer:" after a token rotation).
func (g *Grants) DropPrefix(prefix string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, gr := range g.m {
		if strings.HasPrefix(gr.binding, prefix) {
			delete(g.m, k)
		}
	}
}

// DropAll revokes every grant.
func (g *Grants) DropAll() {
	g.mu.Lock()
	g.m = map[string]grant{}
	g.mu.Unlock()
}

// Len returns the number of live grants.
func (g *Grants) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.purgeLocked(g.clock.Now())
	return len(g.m)
}

func (g *Grants) purgeLocked(now time.Time) {
	for k, gr := range g.m {
		if !now.Before(gr.expires) {
			delete(g.m, k)
		}
	}
}

func (g *Grants) evictSoonestLocked() {
	type entry struct {
		key string
		at  time.Time
	}
	all := make([]entry, 0, len(g.m))
	for k, gr := range g.m {
		all = append(all, entry{k, gr.expires})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	delete(g.m, all[0].key)
}

// Binding is the key a PIN grant is bound to: the session id, or "bearer" plus the token
// fingerprint, or the socket uid (agent.md section 3.9.2). A principal that cannot hold a grant
// (anonymous, Portal) has no binding.
func Binding(p api.Principal) string {
	switch p.Credential {
	case api.CredSession:
		if p.SessionID != "" {
			return "session:" + p.SessionID
		}
	case api.CredBearer:
		if p.TokenFingerprint != "" {
			return "bearer:" + p.TokenFingerprint
		}
	case api.CredSocket:
		return "uid:" + strconv.Itoa(p.UID)
	}
	return ""
}
