package health_test

import (
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

func TestBudgetSlidingWindow(t *testing.T) {
	type step struct {
		at     time.Duration // since t0
		record bool          // record a restart at this time
		allow  bool          // what Allow says before the record
		count  int           // restarts in the window after the step
	}
	for _, tc := range []struct {
		name  string
		max   int
		steps []step
	}{
		{"three in an hour then refused", 3, []step{
			{0, true, true, 1}, {10 * time.Minute, true, true, 2}, {20 * time.Minute, true, true, 3},
			{30 * time.Minute, false, false, 3}, {59 * time.Minute, false, false, 3},
		}},
		{"the oldest leaves at exactly one hour", 3, []step{
			{0, true, true, 1}, {10 * time.Minute, true, true, 2}, {20 * time.Minute, true, true, 3},
			{time.Hour - time.Second, false, false, 3},
			{time.Hour, false, true, 2},
			{time.Hour + 10*time.Minute, false, true, 1},
			{2 * time.Hour, false, true, 0},
		}},
		{"a restart that expired gives its place back", 1, []step{
			{0, true, true, 1}, {time.Hour, true, true, 1}, {time.Hour + time.Minute, false, false, 1},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := health.NewBudget(tc.max, time.Hour)
			for i, s := range tc.steps {
				now := t0.Add(s.at)
				if got := b.Allow(now); got != s.allow {
					t.Fatalf("step %d: Allow = %v, want %v", i, got, s.allow)
				}
				if s.record {
					b.Record(now)
				}
				if got := b.Count(now); got != s.count {
					t.Fatalf("step %d: Count = %d, want %d", i, got, s.count)
				}
			}
		})
	}
}

func TestBudgetDefaults(t *testing.T) {
	b := health.NewBudget(0, 0)
	if b.Max() != health.DefaultMaxRestarts {
		t.Errorf("max %d", b.Max())
	}
	for i := 0; i < health.DefaultMaxRestarts; i++ {
		b.Record(t0)
	}
	if b.Allow(t0.Add(health.DefaultRestartWindow - time.Nanosecond)) {
		t.Error("allowed inside the default window")
	}
	if !b.Allow(t0.Add(health.DefaultRestartWindow)) {
		t.Error("refused after the default window")
	}
}
