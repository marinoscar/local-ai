package health

import "time"

// Defaults of the restart budget (companion-services.md section 4.6): at most 3 restarts per hour
// per service.
const (
	DefaultMaxRestarts   = 3
	DefaultRestartWindow = time.Hour
)

// Budget is a sliding-window restart budget: at most Max restarts in any Window. It lives in memory
// (it resets when the agent restarts, which is acceptable) and is not safe for concurrent use: the
// supervisor holds its lock around every call. An attempt that fails still counts, so a unit that
// cannot be restarted at all does not make the supervisor retry for ever.
type Budget struct {
	max    int
	window time.Duration
	times  []time.Time
}

// NewBudget returns a budget of max restarts per window. A non-positive max or window takes the default.
func NewBudget(max int, window time.Duration) *Budget {
	if max <= 0 {
		max = DefaultMaxRestarts
	}
	if window <= 0 {
		window = DefaultRestartWindow
	}
	return &Budget{max: max, window: window}
}

// prune drops the restarts that left the window: one made exactly a window ago no longer counts.
func (b *Budget) prune(now time.Time) {
	i := 0
	for i < len(b.times) && now.Sub(b.times[i]) >= b.window {
		i++
	}
	if i > 0 {
		b.times = append(b.times[:0], b.times[i:]...)
	}
}

// Allow reports whether another restart fits in the window at now.
func (b *Budget) Allow(now time.Time) bool {
	b.prune(now)
	return len(b.times) < b.max
}

// Record notes a restart made at now.
func (b *Budget) Record(now time.Time) {
	b.prune(now)
	b.times = append(b.times, now)
}

// Count returns the restarts inside the window at now.
func (b *Budget) Count(now time.Time) int {
	b.prune(now)
	return len(b.times)
}

// Max returns the budget's limit.
func (b *Budget) Max() int { return b.max }
