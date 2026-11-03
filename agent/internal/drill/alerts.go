package drill

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Alerts (DRILL-06, drill.md section 4.5). Both alerts are fast doctor checks over the Reporter's
// in-memory view, so the doctor manager's pass raises and clears them like every other alert, they
// are derived again after an agent restart (the Reporter reads the documents at startup), and a
// check never touches the disk or the network.
//
//   - drl.last_result: the last completed run ended in fail (LAI-DRL-001 as error) or warn (the same
//     code as a warning), with the failing check ids and codes in the details. The next completed run
//     that passes clears it. An aborted or skipped run is not a result and changes nothing; neither is
//     a manual_plug entry.
//   - drl.freshness: no run finished in 45 days (LAI-DRL-006, warning). The days count from the end of
//     the last completed run of any trigger except manual_plug, or from the day the agent first watched
//     the node when none ever ran.

// Ids of the alert checks.
const (
	CheckLastResult = "drl.last_result"
	CheckFreshness  = "drl.freshness"
)

// StaleAfter is how long a node may go without a finished drill before LAI-DRL-006 (drill.md section
// 4.5).
const StaleAfter = 45 * 24 * time.Hour

// AlertSource is what the alert checks read; *Reporter implements it.
type AlertSource interface {
	// LastRun is the last completed run of the drill, false when none is kept.
	LastRun() (LastRun, bool)
	// Anchor is the instant the 45-day count starts at, zero when there is none yet.
	Anchor() time.Time
}

var _ AlertSource = (*Reporter)(nil)

// AlertChecks returns the two fast doctor checks. clock is the one the age is measured on (nil is the
// wall clock).
func AlertChecks(src AlertSource, clock config.Clock) []doctor.Check {
	if clock == nil {
		clock = config.RealClock{}
	}
	return []doctor.Check{
		{ID: CheckLastResult, Area: "DRL", Fast: true, Run: func(context.Context) []doctor.Result {
			return lastResultFindings(src)
		}},
		{ID: CheckFreshness, Area: "DRL", Fast: true, Run: func(context.Context) []doctor.Result {
			return freshnessFindings(src, clock.Now())
		}},
	}
}

// RegisterAlertChecks adds the alert checks to reg (doctor.DefaultRegistry when nil). The daemon wiring
// calls it; nothing registers at init time.
func RegisterAlertChecks(reg *doctor.Registry, src AlertSource, clock config.Clock) error {
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	for _, c := range AlertChecks(src, clock) {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}

func lastResultFindings(src AlertSource) []doctor.Result {
	last, ok := src.LastRun()
	if !ok {
		return nil
	}
	var sev doctor.Severity
	switch last.Overall {
	case OverallFail:
		sev = doctor.Error
	case OverallWarn:
		sev = doctor.Warning
	default:
		return nil
	}
	return []doctor.Result{{
		Code: CodeProblems, Severity: sev,
		Details: map[string]any{
			"run_id":           last.RunID,
			"overall":          last.Overall,
			"finished_at":      last.FinishedAt.Format(time.RFC3339),
			"failed_check_ids": stringsAny(last.FailedCheckIDs),
			"failed_checks":    stringsAny(last.FailedChecks),
		},
	}}
}

func freshnessFindings(src AlertSource, now time.Time) []doctor.Result {
	anchor := src.Anchor()
	if anchor.IsZero() {
		return nil
	}
	age := now.Sub(anchor)
	if age < StaleAfter {
		return nil
	}
	return []doctor.Result{{
		Code: CodeStale,
		Details: map[string]any{
			"since": anchor.UTC().Format(time.RFC3339),
			"days":  int(age / (24 * time.Hour)),
		},
	}}
}

// stringsAny copies a string list into the []any a details map carries (never nil, so the JSON is an
// array).
func stringsAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
