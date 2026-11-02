package auth

import (
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// OperatorsGroup is the group whose members may use the agent (overview section 3.1).
const OperatorsGroup = "localai-operators"

// GroupCacheTTL is how long a group-membership answer is remembered. aictl calls the socket
// many times a second; a changed membership applies after this (the spec asks the user to log out
// and in anyway).
const GroupCacheTTL = 30 * time.Second

// GroupLookup is the seam over the user database (os/user), so tests need no real users.
type GroupLookup interface {
	// InGroup reports whether uid belongs to the named group. An error means "could not tell"; the
	// caller then treats the user as not a member (fail closed).
	InGroup(uid int, group string) (bool, error)
	// GroupID returns the numeric id of a group, to own the token file.
	GroupID(group string) (int, error)
}

// OSGroups is GroupLookup over os/user (the pure-Go /etc/passwd and /etc/group reader in a static build).
type OSGroups struct{}

// InGroup implements GroupLookup: uid's group ids (primary included) must contain the group's id.
func (OSGroups) InGroup(uid int, group string) (bool, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return false, err
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return false, err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == g.Gid {
			return true, nil
		}
	}
	return u.Gid == g.Gid, nil
}

// GroupID implements GroupLookup.
func (OSGroups) GroupID(group string) (int, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(g.Gid)
}

// groupCache remembers InGroup answers for GroupCacheTTL, errors included (a broken /etc/group
// must not be re-parsed on every request).
type groupCache struct {
	lookup GroupLookup
	group  string
	clock  Clock

	mu sync.Mutex
	m  map[int]cachedMember
}

type cachedMember struct {
	member bool
	at     time.Time
}

func newGroupCache(l GroupLookup, group string, clock Clock) *groupCache {
	return &groupCache{lookup: l, group: group, clock: clock, m: map[int]cachedMember{}}
}

func (c *groupCache) isOperator(uid int) (bool, error) {
	now := c.clock.Now()
	c.mu.Lock()
	if e, ok := c.m[uid]; ok && now.Sub(e.at) < GroupCacheTTL {
		c.mu.Unlock()
		return e.member, nil
	}
	c.mu.Unlock()
	member, err := c.lookup.InGroup(uid, c.group)
	if err != nil {
		member = false
	}
	c.mu.Lock()
	if len(c.m) > 1024 { // bounded: a socket reachable by many uids
		c.m = map[int]cachedMember{}
	}
	c.m[uid] = cachedMember{member: member, at: now}
	c.mu.Unlock()
	return member, err
}

// socketPrincipal maps SO_PEERCRED to a principal (agent.md section 3.9.1 step 5): uid 0 is admin, a
// member of localai-operators is operator, anyone else is refused with 403 LAI-SEC-013. A peer whose
// credentials could not be read is refused too.
func (s *Service) socketPrincipal(peer *api.PeerCred) (api.Principal, *api.Error) {
	if peer == nil {
		return api.Anonymous(), api.NewError(api.CodePeerNotOperator, 403, map[string]any{"reason": "peer_unknown"})
	}
	via := "uid=" + strconv.Itoa(peer.UID)
	if peer.UID == 0 {
		return api.Principal{Kind: api.KindAdmin, Credential: api.CredSocket, Via: via, UID: 0}, nil
	}
	member, err := s.groups.isOperator(peer.UID)
	if err != nil {
		s.log.Warn("cannot look up the groups of a socket peer; refusing it", "uid", peer.UID, "error", err.Error())
	}
	if !member {
		return api.Anonymous(), api.NewError(api.CodePeerNotOperator, 403, map[string]any{"uid": peer.UID})
	}
	return api.Principal{Kind: api.KindOperator, Credential: api.CredSocket, Via: via, UID: peer.UID}, nil
}
