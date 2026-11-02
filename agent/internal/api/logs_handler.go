package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Handler of GET /logs (agent.md section 3.8.23): the tail of a unit's journal, scrubbed.

// LogTailer reads the journal; *services.Journal implements it.
type LogTailer interface {
	Tail(ctx context.Context, unit string, lines int, since time.Time) (services.JournalTail, error)
}

// LogsDeps are what the /logs route needs.
type LogsDeps struct {
	Journal LogTailer
}

// LogsRoutes returns GET /logs (op, 10 s).
func LogsRoutes(d LogsDeps) []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/logs", Auth: []Level{AuthOp}, Timeout: 10 * time.Second, Handler: HandlerFunc(d.get)},
	}
}

// RegisterLogs adds GET /logs to reg (DefaultRegistry when nil). The wiring in internal/app calls it.
func RegisterLogs(reg *Registry, d LogsDeps) error {
	if d.Journal == nil {
		return errors.New("api: the logs route needs a journal reader")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(LogsRoutes(d)...)
}

type logLineJSON struct {
	At       string `json:"at"`
	Priority int    `json:"priority"`
	Message  string `json:"message"`
}

type logsResponse struct {
	Unit      string        `json:"unit"`
	Lines     []logLineJSON `json:"lines"`
	Truncated bool          `json:"truncated"`
}

func badQuery(field, reason string, extra map[string]any) *Error {
	details := map[string]any{"field": field, "reason": reason}
	for k, v := range extra {
		details[k] = v
	}
	return NewError(CodeInvalidRequest, http.StatusBadRequest, details)
}

func (d LogsDeps) get(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()

	short := q.Get("unit")
	if short == "" {
		return badQuery("unit", "missing", map[string]any{"allowed": logUnitNames})
	}
	unit, ok := services.LogUnit(short)
	if !ok {
		return badQuery("unit", "unknown_unit", map[string]any{"allowed": logUnitNames})
	}

	lines := services.DefaultTailLines
	if q.Has("lines") {
		n, err := strconv.Atoi(q.Get("lines"))
		if err != nil {
			return badQuery("lines", "not_an_integer", nil)
		}
		if n < services.MinTailLines || n > services.MaxTailLines {
			return badQuery("lines", "out_of_range", map[string]any{"min": services.MinTailLines, "max": services.MaxTailLines})
		}
		lines = n
	}

	var since time.Time
	if q.Has("since") {
		t, err := time.Parse(time.RFC3339, q.Get("since"))
		if err != nil {
			return badQuery("since", "not_rfc3339", nil)
		}
		since = t
	}

	tail, err := d.Journal.Tail(r.Context(), unit, lines, since)
	if err != nil {
		return mapTailError(err)
	}
	out := logsResponse{Unit: tail.Unit, Lines: make([]logLineJSON, 0, len(tail.Lines)), Truncated: tail.Truncated}
	for _, l := range tail.Lines {
		out.Lines = append(out.Lines, logLineJSON{At: rfc3339(l.At), Priority: l.Priority, Message: l.Message})
	}
	WriteJSON(w, r, http.StatusOK, out)
	return nil
}

// logUnitNames are the values of the unit parameter, for the error details.
var logUnitNames = []string{"agent", "llama", "kiwix", "whisper", "sync", "drill"}

// mapTailError turns a journal failure into an API error: a timeout is 504, anything else 500, both
// with the internal code (the details say what journalctl did).
func mapTailError(err error) error {
	var je *services.JournalError
	switch {
	case errors.Is(err, services.ErrTailRange):
		return badQuery("lines", "out_of_range", map[string]any{"min": services.MinTailLines, "max": services.MaxTailLines})
	case errors.Is(err, services.ErrTimeout):
		return NewError(CodeInternal, http.StatusGatewayTimeout, map[string]any{"reason": "journalctl_timeout"})
	case errors.As(err, &je):
		return NewError(CodeInternal, http.StatusInternalServerError, map[string]any{"reason": "journalctl_failed", "exit_code": je.ExitCode})
	}
	return err
}
