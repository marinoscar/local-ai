package jobs

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// LogsTailResult is the result of logs.tail.
type LogsTailResult struct {
	Unit      string   `json:"unit"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// Limits of the result: a line is cut at the schema's 2000 characters, and the whole result stays
// well under cloud.MaxResultBytes: when the lines do not fit, the oldest are dropped and truncated is
// set.
const (
	maxLogLineChars = 2000
	logBudgetBytes  = 900 << 10
)

// newLogsTail is GET /logs: the tail of a unit's journal through the same reader as the route (which
// scrubs every message after decoding it), each line "<rfc3339> <message>", scrubbed once more here.
// The unit is looked up in the closed table of services.LogUnit: nothing from the params reaches
// journalctl but through it.
func newLogsTail(d Deps) cloud.Handler {
	return typed[LogsTailParams]{
		name: cloud.TypeLogsTail, cancellable: true, decode: decodeLogsTail,
		run: func(ctx context.Context, job cloud.Job, p LogsTailParams) (cloud.Outcome, error) {
			unit, ok := services.LogUnit(p.Unit)
			if !ok {
				return cloud.Outcome{}, jobErr(doctor.AGT020, "There is no log of that unit.")
			}
			tail, err := d.Logs.Tail(ctx, unit, p.Lines, time.Time{})
			if err != nil {
				if ctx.Err() != nil {
					return cloud.Outcome{}, ctx.Err()
				}
				job.Logger.Warn("the journal could not be read", "unit", p.Unit, "error", logx.ScrubString(err.Error()))
				return cloud.Outcome{}, jobErr(doctor.AGT099, "The log could not be read.")
			}
			lines := make([]string, 0, len(tail.Lines))
			for _, l := range tail.Lines {
				lines = append(lines, cutRunes(logx.ScrubString(l.At.UTC().Format(time.RFC3339)+" "+l.Message), maxLogLineChars))
			}
			truncated := tail.Truncated
			if len(lines) > p.Lines {
				lines, truncated = lines[len(lines)-p.Lines:], true
			}
			total := 0
			start := len(lines)
			for start > 0 && total+len(lines[start-1])+8 <= logBudgetBytes {
				start--
				total += len(lines[start]) + 8
			}
			if start > 0 {
				lines, truncated = lines[start:], true
			}
			return cloud.Outcome{Result: LogsTailResult{Unit: cutRunes(tail.Unit, 64), Lines: lines, Truncated: truncated}}, nil
		},
	}
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
