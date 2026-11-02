package fake

import (
	"errors"
	"sync"
)

// Groups is a fake auth.GroupLookup: Members maps a group name to the uids in it.
type Groups struct {
	mu      sync.Mutex
	members map[string]map[int]bool
	gids    map[string]int

	// Err, when set, makes InGroup fail (the lookup could not tell).
	Err error
	// Calls counts InGroup calls, to prove the cache works.
	Calls int
}

// NewGroups returns an empty database.
func NewGroups() *Groups {
	return &Groups{members: map[string]map[int]bool{}, gids: map[string]int{}}
}

// Add puts uid in group and gives the group an id.
func (g *Groups) Add(group string, gid int, uids ...int) *Groups {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.members[group] == nil {
		g.members[group] = map[int]bool{}
	}
	for _, u := range uids {
		g.members[group][u] = true
	}
	g.gids[group] = gid
	return g
}

// Remove takes uid out of group.
func (g *Groups) Remove(group string, uid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.members[group], uid)
}

// InGroup implements auth.GroupLookup.
func (g *Groups) InGroup(uid int, group string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls++
	if g.Err != nil {
		return false, g.Err
	}
	return g.members[group][uid], nil
}

// GroupID implements auth.GroupLookup.
func (g *Groups) GroupID(group string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if id, ok := g.gids[group]; ok {
		return id, nil
	}
	return -1, errors.New("fake: unknown group " + group)
}
