package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// Helpers shared by the read-only commands (AICTL-04: status, models, default, services, logs,
// doctor, version, ark, cloud status, profile show/suggest). Every one of them is a thin renderer
// of an agent GET route: it fetches the body, prints it unchanged under --json, or renders it for
// people. The wire structs (wire*.go) decode only the fields a renderer uses and ignore the rest, so
// a newer agent never breaks an older aictl.

// readOnlyTimeout is the default deadline of a read-only command (aictl.md section 4).
const readOnlyTimeout = 10 * time.Second

// roFetchCtx GETs path under ctx and returns the body unchanged. When v is not nil the body is also
// decoded into it: v is a pointer to a struct (unknown fields are ignored), or a func([]byte) error
// for a body with more than one accepted shape. A body that does not decode is the exit-1 bad-response
// error (CodeBadResponse), carrying the request id so the user can report it. Transport, the retry
// while the agent starts, the version check and the error envelope are the client's.
func roFetchCtx(c *Call, ctx context.Context, path string, v any) (json.RawMessage, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	resp, err := cl.Do(ctx, http.MethodGet, path, nil, &raw)
	if err != nil {
		return nil, err
	}
	var derr error
	switch d := v.(type) {
	case nil:
	case func([]byte) error:
		derr = d(raw)
	default:
		derr = json.Unmarshal(raw, v)
	}
	if derr != nil {
		e := c.Err(CodeBadResponse, "").WithDetails("path", path, "reason", derr.Error())
		if resp != nil {
			e.RequestID = resp.RequestID
		}
		return raw, e
	}
	return raw, nil
}

// roGet fetches path and decodes it into v.
func roGet(c *Call, path string, v any) (json.RawMessage, error) {
	return roFetchCtx(c, c.Ctx, path, v)
}

// roGetCtx is roGet under ctx.
func roGetCtx(c *Call, ctx context.Context, path string, v any) (json.RawMessage, error) {
	return roFetchCtx(c, ctx, path, v)
}

// roEmit writes the agent body as the --json document. It reports a closed pipe as a plain error.
func roEmit(c *Call, body []byte) error { return c.Out.JSON().Raw(body) }

// roNoArgs rejects positional arguments for a command that takes none.
func roNoArgs(c *Call) error {
	if len(c.Args) > 0 {
		return c.Err(CodeUsage, pick(c.Lang,
			"Unexpected argument "+quote(c.Args[0])+".",
			"Argumento inesperado "+quote(c.Args[0])+"."))
	}
	return nil
}

func quote(s string) string { return `"` + s + `"` }

// roWhen parses an RFC 3339 time from the agent; ok is false for "", null and anything malformed
// (a renderer then leaves the time out rather than failing).
func roWhen(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// roAgo renders an agent time for people ("12 hours ago"); "" when it cannot be read.
func roAgo(p *out.Printer, s string) string {
	t, ok := roWhen(s)
	if !ok {
		return ""
	}
	return p.When(t)
}

// roSeverity maps the agent's severity word to the output layer's; unknown words are errors so a
// new severity is never hidden.
func roSeverity(s string) out.Severity {
	switch strings.ToLower(s) {
	case "info":
		return out.SevInfo
	case "warning", "warn":
		return out.SevWarning
	case "critical":
		return out.SevCritical
	}
	return out.SevError
}

// roPad right-pads s with spaces to width (display width, not bytes).
func roPad(s string, width int) string {
	if w := out.DisplayWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// roDash is the empty-cell mark of a table: an em dash, or "-" where symbols are ASCII.
func roDash(p *out.Printer) string {
	if p.Symbols().OK == out.UTF8Symbols.OK {
		return "—"
	}
	return "-"
}

// roDot is the separator between the parts of a summary line: "·", or "-" in ASCII.
func roDot(p *out.Printer) string {
	if p.Symbols().OK == out.UTF8Symbols.OK {
		return " · "
	}
	return " - "
}

// roEm is the dash that joins a name to what is said about it: " — ", or " - " in ASCII.
func roEm(p *out.Printer) string {
	if p.Symbols().OK == out.UTF8Symbols.OK {
		return " — "
	}
	return " - "
}

// roDegC is the temperature unit: "°C", or "C" where symbols are ASCII.
func roDegC(p *out.Printer) string {
	if p.Symbols().OK == out.UTF8Symbols.OK {
		return "°C"
	}
	return "C"
}
