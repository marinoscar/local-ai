package checks

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// ClockSkewTolerance is how far the clock may be behind the newest verified_at before check 21 warns.
// Content is verified by several computers (a drive carries what another node synced), and two
// clocks that were set by hand differ by minutes; a clock that is days behind is wrong.
const ClockSkewTolerance = 5 * time.Minute

// NewestVerified returns the newest verified_at of any item on the mounted content roots (the best
// copies' manifests, from the in-memory index); ok is false when there is none.
type NewestVerified func() (at time.Time, ok bool)

// Clock is check 21, drl.clock: the clock is plausible, meaning it is not before the newest
// `verified_at` of the content on the mounted roots. A clock that reads earlier than the moment
// something was verified is wrong (a dead CMOS battery after a long power-off sends it back to a
// default date), and ages, the sync schedule and TLS all depend on it.
//
//   - pass: the clock is at or after the newest verified_at (within ClockSkewTolerance), or no item has
//     been verified yet (nothing to compare with: details.newest_verified_at is then absent);
//   - warn (LAI-DRL-081): the clock is earlier than that.
//
// Details are `{now, newest_verified_at}` in RFC 3339.
func Clock(newest NewestVerified) drill.Check {
	return drill.Check{
		ID: "drl.clock", Area: "clock", Order: 21,
		Run: func(_ context.Context, env *drill.Env) drill.Outcome { return clockCheck(env, newest) },
	}
}

func clockCheck(env *drill.Env, newest NewestVerified) drill.Outcome {
	now := env.Now().UTC()
	details := map[string]any{"now": now.Format(time.RFC3339)}
	if newest == nil {
		details["reason"] = "no_content_index"
		return drill.Outcome{Result: drill.ResultPass, Details: details}
	}
	at, ok := newest()
	if !ok || at.IsZero() {
		return drill.Outcome{Result: drill.ResultPass, Details: details}
	}
	at = at.UTC()
	details["newest_verified_at"] = at.Format(time.RFC3339)
	if now.Add(ClockSkewTolerance).Before(at) {
		return drill.Outcome{Result: drill.ResultWarn, Code: doctor.DRL081, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}
