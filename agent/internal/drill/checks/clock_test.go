package checks

import (
	"context"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

var clkNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func clockOutcome(now time.Time, newest NewestVerified) drill.Outcome {
	return Clock(newest).Run(context.Background(), &drill.Env{Now: func() time.Time { return now }})
}

func at(t time.Time) NewestVerified { return func() (time.Time, bool) { return t, true } }

func TestClockTable(t *testing.T) {
	tests := []struct {
		name   string
		now    time.Time
		newest NewestVerified
		want   drill.Result
		code   string
	}{
		{"after the newest verification", clkNow, at(clkNow.Add(-48 * time.Hour)), drill.ResultPass, ""},
		{"exactly at it", clkNow, at(clkNow), drill.ResultPass, ""},
		{"a few minutes behind another computer's clock", clkNow, at(clkNow.Add(2 * time.Minute)), drill.ResultPass, ""},
		{"just inside the tolerance", clkNow, at(clkNow.Add(ClockSkewTolerance)), drill.ResultPass, ""},
		{"just outside the tolerance", clkNow, at(clkNow.Add(ClockSkewTolerance + time.Second)), drill.ResultWarn, doctor.DRL081},
		{"before the newest verified_at", clkNow, at(clkNow.Add(72 * time.Hour)), drill.ResultWarn, doctor.DRL081},
		{"reset to a default date", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), at(clkNow), drill.ResultWarn, doctor.DRL081},
		{"nothing verified yet", clkNow, func() (time.Time, bool) { return time.Time{}, false }, drill.ResultPass, ""},
		{"a zero time is nothing", clkNow, at(time.Time{}), drill.ResultPass, ""},
		{"no content index", clkNow, nil, drill.ResultPass, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := clockOutcome(tt.now, tt.newest)
			if out.Result != tt.want || out.Code != tt.code {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestClockDetailsAreRFC3339(t *testing.T) {
	newest := time.Date(2026, 11, 10, 8, 30, 0, 0, time.FixedZone("x", 3600))
	out := clockOutcome(clkNow, at(newest))
	if out.Details["now"] != "2026-11-08T12:00:00Z" || out.Details["newest_verified_at"] != "2026-11-10T07:30:00Z" {
		t.Fatalf("details = %v", out.Details)
	}
	if out := clockOutcome(clkNow, func() (time.Time, bool) { return time.Time{}, false }); out.Details["newest_verified_at"] != nil {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestClockCheckIdentity(t *testing.T) {
	c := Clock(nil)
	if c.ID != "drl.clock" || c.Order != 21 || c.Critical || c.Area != "clock" {
		t.Fatalf("check = %+v", c)
	}
}
