package events

import (
	"fmt"
	"sort"
	"strings"
)

// Filter selects event types: the `types` query of GET /events. The zero Filter matches every
// type (the default). A filter built from patterns matches a type when any pattern does; a pattern is
// an exact type (`mode.changed`) or a prefix ending in `*` (`model.*`, and `*` alone for everything).
//
// The control types resync and shutdown always pass every filter: a client must hear them whatever it
// asked for.
type Filter struct {
	restricted bool
	exact      map[string]struct{}
	prefixes   []string // without the trailing *, sorted, none covered by a shorter one
}

// AllTypes returns the filter that matches every type (the zero value, spelled out).
func AllTypes() Filter { return Filter{} }

// ParseFilter parses a comma-separated list of patterns. Spaces around a pattern are ignored and an
// empty element is skipped; an empty or blank list is the default filter, all types. A pattern may
// hold only lower-case letters, digits, `_`, `-` and `.`, plus one `*` at its end; anything else is
// an error naming the pattern.
func ParseFilter(list string) (Filter, error) {
	var pats []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			pats = append(pats, p)
		}
	}
	if len(pats) == 0 {
		return Filter{}, nil
	}
	return NewFilter(pats...)
}

// NewFilter builds a filter from patterns (see ParseFilter for their syntax). With no pattern it
// returns a filter that matches nothing but the control types; use AllTypes for everything.
func NewFilter(patterns ...string) (Filter, error) {
	f := Filter{restricted: true, exact: map[string]struct{}{}}
	for _, p := range patterns {
		if err := validPattern(p); err != nil {
			return Filter{}, err
		}
		if pre, ok := strings.CutSuffix(p, "*"); ok {
			f.prefixes = append(f.prefixes, pre)
		} else {
			f.exact[p] = struct{}{}
		}
	}
	f.normalize()
	return f, nil
}

func validPattern(p string) error {
	if p == "" {
		return fmt.Errorf("events: empty type pattern")
	}
	body := strings.TrimSuffix(p, "*")
	for _, c := range body {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return fmt.Errorf("events: invalid type pattern %q", p)
		}
	}
	return nil
}

// normalize sorts the prefixes, drops duplicates and the ones a shorter prefix already covers, and
// drops the exact types a prefix covers.
func (f *Filter) normalize() {
	sort.Strings(f.prefixes)
	var kept []string
	for _, p := range f.prefixes {
		covered := false
		for _, k := range kept {
			if strings.HasPrefix(p, k) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, p)
		}
	}
	f.prefixes = kept
	for e := range f.exact {
		if f.hasPrefixFor(e) {
			delete(f.exact, e)
		}
	}
}

func (f Filter) hasPrefixFor(typ string) bool {
	for _, p := range f.prefixes {
		if strings.HasPrefix(typ, p) {
			return true
		}
	}
	return false
}

// All reports whether the filter matches every type.
func (f Filter) All() bool {
	if !f.restricted {
		return true
	}
	for _, p := range f.prefixes {
		if p == "" {
			return true
		}
	}
	return false
}

// Match reports whether an event of type typ passes the filter.
func (f Filter) Match(typ string) bool {
	if typ == TypeResync || typ == TypeShutdown || !f.restricted {
		return true
	}
	if _, ok := f.exact[typ]; ok {
		return true
	}
	return f.hasPrefixFor(typ)
}

// Restrict returns the intersection of f with allowed: the types both match. A requested pattern
// that allowed does not cover is dropped silently (survival-portal.md section 3.3), so asking for `*`
// yields exactly allowed, and asking only for types outside it yields a filter that matches nothing.
func (f Filter) Restrict(allowed Filter) Filter {
	if !allowed.restricted {
		return f
	}
	if !f.restricted {
		return allowed
	}
	out := Filter{restricted: true, exact: map[string]struct{}{}}
	// requested exact types: kept when allowed covers them
	for e := range f.exact {
		if allowed.Match(e) {
			out.exact[e] = struct{}{}
		}
	}
	// allowed exact types: kept when the request covers them
	for e := range allowed.exact {
		if f.Match(e) {
			out.exact[e] = struct{}{}
		}
	}
	// prefixes: of two overlapping prefixes, the longer one is the intersection
	for _, rp := range f.prefixes {
		for _, ap := range allowed.prefixes {
			switch {
			case strings.HasPrefix(rp, ap):
				out.prefixes = append(out.prefixes, rp)
			case strings.HasPrefix(ap, rp):
				out.prefixes = append(out.prefixes, ap)
			}
		}
	}
	out.normalize()
	return out
}

// Patterns returns the filter as sorted patterns (`*` for all types; the zero filter is ["*"]).
// It is for logs and tests.
func (f Filter) Patterns() []string {
	if !f.restricted {
		return []string{"*"}
	}
	out := []string{}
	for e := range f.exact {
		out = append(out, e)
	}
	for _, p := range f.prefixes {
		out = append(out, p+"*")
	}
	sort.Strings(out)
	return out
}

// PortalTypes is the set of types a Portal principal may receive (survival-portal.md section 3.3):
// nothing outside it is ever sent to a Portal session, whatever it asks for.
func PortalTypes() Filter {
	f, err := NewFilter("mode.*", "model.*", "service.state", "drive.*", "alert.*")
	if err != nil {
		panic(err) // constant patterns
	}
	return f
}
