package drill

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// idPattern is a check id: drl. then a name; model ids add dots, dashes and digits
// (drl.llm.answer.qwen3.5-9b-q4km).
var idPattern = regexp.MustCompile(`^drl\.[A-Za-z0-9][A-Za-z0-9._-]*$`)

// maxOnlyEntries and maxIDLen bound a --only list.
const (
	maxOnlyEntries = 64
	maxIDLen       = 200
)

// Expander makes the checks of a group at the start of a run, for the checks whose number depends on
// the node: one drl.llm.answer.<id> per survival model. It must not touch the router or the network;
// it reads the profile and the catalog.
type Expander func(ctx context.Context) ([]Check, error)

type entry struct {
	check  *Check
	expand Expander
	group  string
}

// Registry is the ordered check registry. Later issues add their checks to it with Add and AddGroup
// when the daemon is wired; nothing registers at init time. It is safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	entries []entry
	ids     map[string]bool
	groups  map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{ids: map[string]bool{}, groups: map[string]bool{}}
}

func validateCheck(c Check) error {
	switch {
	case !idPattern.MatchString(c.ID) || len(c.ID) > maxIDLen:
		return fmt.Errorf("drill: check id %q must look like drl.<area>.<name>", c.ID)
	case strings.TrimSpace(c.Area) == "":
		return fmt.Errorf("drill: check %s has no area", c.ID)
	case c.Run == nil:
		return fmt.Errorf("drill: check %s has no Run", c.ID)
	case c.Order < 1:
		return fmt.Errorf("drill: check %s has no Order (the number in drill.md section 4.2)", c.ID)
	case c.Timeout < 0:
		return fmt.Errorf("drill: check %s has a negative timeout", c.ID)
	case c.Phase != PhaseMain && c.Phase != PhasePostRestore:
		return fmt.Errorf("drill: check %s has an unknown phase", c.ID)
	}
	return nil
}

// Add registers a check. A bad id, a missing Run, Area or Order, and a duplicate id are errors.
func (r *Registry) Add(c Check) error {
	if err := validateCheck(c); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids[c.ID] {
		return fmt.Errorf("drill: check %s is registered twice", c.ID)
	}
	r.ids[c.ID] = true
	r.entries = append(r.entries, entry{check: &c})
	return nil
}

// AddGroup registers an Expander under a name used in error messages. The checks it returns are
// validated when a run starts, with the same rules as Add.
func (r *Registry) AddGroup(name string, x Expander) error {
	if strings.TrimSpace(name) == "" || x == nil {
		return fmt.Errorf("drill: a check group needs a name and an expander")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.groups[name] {
		return fmt.Errorf("drill: check group %s is registered twice", name)
	}
	r.groups[name] = true
	r.entries = append(r.entries, entry{expand: x, group: name})
	return nil
}

// UnknownCheckError is returned by Select when a --only entry matches no check.
type UnknownCheckError struct {
	IDs []string
}

func (e *UnknownCheckError) Error() string {
	return "drill: no such check: " + strings.Join(e.IDs, ", ")
}

// Select returns the checks of a run in execution order: the registered and the expanded checks,
// stable-sorted by Order. only filters by id: an entry selects the check with that id and, at a dot
// boundary, the checks under it (drl.stt selects drl.stt.en and drl.stt.es). An entry that selects
// nothing is an *UnknownCheckError.
func (r *Registry) Select(ctx context.Context, only []string) ([]Check, error) {
	r.mu.Lock()
	entries := append([]entry(nil), r.entries...)
	seen := make(map[string]bool, len(r.ids))
	r.mu.Unlock()

	var all []Check
	for _, e := range entries {
		if e.check != nil {
			all = append(all, *e.check)
			seen[e.check.ID] = true
			continue
		}
		cs, err := e.expand(ctx)
		if err != nil {
			return nil, fmt.Errorf("drill: check group %s: %w", e.group, err)
		}
		for _, c := range cs {
			if err := validateCheck(c); err != nil {
				return nil, fmt.Errorf("drill: check group %s: %w", e.group, err)
			}
			if seen[c.ID] {
				return nil, fmt.Errorf("drill: check group %s: %s is registered twice", e.group, c.ID)
			}
			seen[c.ID] = true
			all = append(all, c)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Order < all[j].Order })

	if len(only) == 0 {
		return all, nil
	}
	var unknown []string
	matched := make([]bool, len(only))
	var out []Check
	for _, c := range all {
		hit := false
		for i, o := range only {
			if c.ID == o || strings.HasPrefix(c.ID, o+".") {
				matched[i] = true
				hit = true
			}
		}
		if hit {
			out = append(out, c)
		}
	}
	for i, o := range only {
		if !matched[i] {
			unknown = append(unknown, o)
		}
	}
	if len(unknown) > 0 {
		return nil, &UnknownCheckError{IDs: unknown}
	}
	return out, nil
}

// NormalizeOnly trims and de-duplicates a --only list, keeping the order. It reports a list that is
// too long, or has an empty or oversized entry, as an error naming what is wrong.
func NormalizeOnly(only []string) ([]string, error) {
	if len(only) > maxOnlyEntries {
		return nil, fmt.Errorf("only has %d entries, at most %d", len(only), maxOnlyEntries)
	}
	seen := map[string]bool{}
	var out []string
	for _, o := range only {
		o = strings.TrimSpace(o)
		switch {
		case o == "":
			return nil, fmt.Errorf("only has an empty check id")
		case len(o) > maxIDLen:
			return nil, fmt.Errorf("a check id in only is longer than %d characters", maxIDLen)
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}
