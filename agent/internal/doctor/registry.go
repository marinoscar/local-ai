package doctor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Defaults of the runner (agent.md section 3.8.27).
const (
	// DefaultTimeout is the timeout of a check that sets none.
	DefaultTimeout = 10 * time.Second
	// MaxConcurrent is how many checks run at once.
	MaxConcurrent = 4
	// FastBudget is the total time of a `?fast=1` run.
	FastBudget = 5 * time.Second
)

// Check is one doctor check. Domain packages build their checks over interfaces of their own and
// register them with Register (or Registry.Add); the check never imports the HTTP layer.
type Check struct {
	// ID is "<area>.<name>" in lower case, for example "hw.battery" or "llm.router_health". It is
	// what Result.Check carries and what `passed` lists.
	ID string
	// Area is the code area the check belongs to ("HW", "LLM", ...): the `?area=` filter matches it.
	// It need not be the area of every code the check returns.
	Area string
	// Fast marks a check cheap and local enough to run every 60 s as an alert source (no network
	// round trip beyond loopback, no process spawn that can hang): it is included by `?fast=1`.
	Fast bool
	// Timeout bounds one run; zero means DefaultTimeout. When it passes, the runner abandons the
	// check and reports LAI-AGT-099 with details.reason "timeout", so Run must honour ctx.
	Timeout time.Duration
	// Run executes the check and returns its results. An empty slice, or results without a Code,
	// mean "all good". It may return several problem results (one per finding). Hysteresis (a
	// battery flapping around 20 %) is the check's job; the alert manager only raises and clears.
	Run func(ctx context.Context) []Result
}

// Result is what a check found. A result without a Code is an "ok" result.
type Result struct {
	// Check is the id of the check that produced it; the runner sets it.
	Check string `json:"check"`
	// Code is the doctor code of the problem, "" when the check passed.
	Code string `json:"code,omitempty"`
	// Severity is the effective severity. Left empty, the runner uses the default of the code; a
	// check sets it to escalate or relax (LAI-AGT-003 is critical when defaults were used).
	Severity Severity `json:"severity,omitempty"`
	// Title and Fix are the texts of the code in the language of the request. A check leaves them
	// empty (the runner fills them from the table); it sets them only for a message built from the
	// situation, and then they are not translated.
	Title string `json:"title,omitempty"`
	Fix   string `json:"fix,omitempty"`
	// Details are the facts that make the result useful (battery_percent, problems[], ...). Never a
	// secret.
	Details map[string]any `json:"details,omitempty"`
}

// IsOK reports whether r is an "ok" result.
func (r Result) IsOK() bool { return r.Code == "" }

var checkID = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`)

// Registry holds the checks. It is safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	checks  map[string]Check
	running map[string]bool // checks whose goroutine has not returned (even if abandoned)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checks: map[string]Check{}, running: map[string]bool{}}
}

// DefaultRegistry is the registry packages add their checks to; the agent's runner and alert manager
// use it unless told otherwise.
var DefaultRegistry = NewRegistry()

// Register adds c to DefaultRegistry and panics on error, like http.Handle: a bad check is a
// programming error found at start-up.
func Register(c Check) {
	if err := DefaultRegistry.Add(c); err != nil {
		panic("doctor: " + err.Error())
	}
}

// Add validates and registers c. A bad id or area, a missing Run, a negative timeout or a duplicate
// id is an error.
func (r *Registry) Add(c Check) error {
	if !checkID.MatchString(c.ID) {
		return fmt.Errorf("check id %q must look like \"area.name\" (lower case)", c.ID)
	}
	if !ValidArea(c.Area) {
		return fmt.Errorf("check %q: unknown area %q", c.ID, c.Area)
	}
	if c.Run == nil {
		return fmt.Errorf("check %q: no Run function", c.ID)
	}
	if c.Timeout < 0 {
		return fmt.Errorf("check %q: negative timeout", c.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.checks[c.ID]; dup {
		return fmt.Errorf("check %q is registered twice", c.ID)
	}
	r.checks[c.ID] = c
	return nil
}

// Remove unregisters a check (tests, and a check whose feature was switched off at runtime). It
// reports whether the check existed.
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.checks[id]
	delete(r.checks, id)
	return ok
}

// Checks returns the registered checks sorted by id.
func (r *Registry) Checks() []Check {
	r.mu.Lock()
	out := make([]Check, 0, len(r.checks))
	for _, c := range r.checks {
		out = append(out, c)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ErrInvalidArea is returned (wrapped) by Run for an area that is not one of Areas.
var ErrInvalidArea = errors.New("doctor: unknown area")
