package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Helpers shared by the state-changing commands of AICTL-06 (key, mode, ark, catalog refresh,
// profile set, cloud, ui-code). Nothing here knows a route; each command file owns its wire types.

// multiFlag is a flag that may be given more than once (`--root a --root b`). Each Run builds its own
// (Flags is called once per Run), so values never leak between runs.
type multiFlag struct{ vals []string }

func (m *multiFlag) String() string { return strings.Join(m.vals, ",") }

func (m *multiFlag) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("must not be empty")
	}
	m.vals = append(m.vals, s)
	return nil
}

// multi returns every value of the repeatable flag name, in the order given (never nil).
func (c *Call) multi(name string) []string {
	if f := c.Flags.Lookup(name); f != nil {
		if m, ok := f.Value.(*multiFlag); ok {
			return append([]string{}, m.vals...)
		}
	}
	return []string{}
}

// noArgs is the usage check of a command that takes no positional argument.
func (c *Call) noArgs() error {
	if len(c.Args) > 0 {
		return c.Err(CodeUsage, pick(c.Lang,
			fmt.Sprintf("Unexpected argument %q.", c.Args[0]), fmt.Sprintf("Argumento inesperado %q.", c.Args[0])))
	}
	return nil
}

// exactArgs checks the number of positional arguments; usage says what was expected.
func (c *Call) exactArgs(n int, usage string) error {
	switch {
	case len(c.Args) < n:
		return c.Err(CodeUsage, pick(c.Lang, "Missing argument: aictl "+strings.Join(c.Path, " ")+" "+usage,
			"Falta un argumento: aictl "+strings.Join(c.Path, " ")+" "+usage))
	case len(c.Args) > n:
		return c.Err(CodeUsage, pick(c.Lang,
			fmt.Sprintf("Unexpected argument %q.", c.Args[n]), fmt.Sprintf("Argumento inesperado %q.", c.Args[n])))
	}
	return nil
}

// usageErr is a CodeUsage error with its own fix sentence.
func (c *Call) usageErr(msg, fix string) *CLIError {
	e := c.Err(CodeUsage, msg)
	if fix != "" {
		e.Fix = fix
	}
	return e
}

// call sends one request and decodes a 2xx body into v (nil: ignore it). It returns the raw body so
// --json can print it unchanged. An answer that is not the expected JSON is LAI-CLI-009.
func (c *Call) call(method, path string, in, v any) (json.RawMessage, *Response, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, nil, err
	}
	return c.callWith(cl, c.Ctx, method, path, in, v)
}

func (c *Call) callWith(cl *Client, ctx context.Context, method, path string, in, v any) (json.RawMessage, *Response, error) {
	var raw json.RawMessage
	resp, err := cl.Do(ctx, method, path, in, &raw)
	if err != nil {
		return nil, resp, err
	}
	if v != nil && len(raw) > 0 {
		if uerr := json.Unmarshal(raw, v); uerr != nil {
			e := c.Err(CodeBadResponse, "").WithDetails("reason", "unexpected JSON shape")
			if resp != nil {
				e.RequestID = resp.RequestID
			}
			return nil, resp, e
		}
	}
	return raw, resp, nil
}

// emitRaw prints an agent body as the one --json document.
func (c *Call) emitRaw(raw json.RawMessage) error {
	if len(raw) == 0 {
		return c.Out.JSON().Document(map[string]any{})
	}
	return c.Out.JSON().Raw(raw)
}

// withDefaultTimeout limits ctx to d unless the user chose a deadline with --timeout. Commands whose
// default depends on what they do (`mode` shows in 10 s but changes in 20 min) use it for the short
// part; an expired deadline is LAI-CLI-004 like any other.
func (c *Call) withDefaultTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if c.Global.TimeoutSet {
		return c.Ctx, func() {}
	}
	return context.WithTimeout(c.Ctx, d)
}

// codeProblem describes an agent code (one that came inside an event or a body, not as an error) in
// the user's language from the doctor table, with the agent's own sentence as the fallback.
func (c *Call) codeProblem(code, fallback string) out.Problem {
	p := out.Problem{Severity: out.SevWarning, Code: code, Message: fallback}
	if title, fix, ok := doctor.Text(code, string(c.Lang)); ok {
		p.Message, p.Fix = title, fix
	}
	if d, ok := doctor.Lookup(code); ok {
		switch d.Severity {
		case doctor.Info:
			p.Severity = out.SevInfo
		case doctor.Error:
			p.Severity = out.SevError
		case doctor.Critical:
			p.Severity = out.SevCritical
		}
	}
	p.Message = sentence(p.Message)
	p.Fix = sentence(p.Fix)
	return p
}

// sentence makes s end with a full stop ("" stays "").
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// codeError is a CLIError for a doctor code with the doctor table's text and fix, for refusals
// aictl detects itself before asking the agent (several drives, an unknown profile, ...). exit is
// the exit code of the failure.
func (c *Call) codeError(code string, exit int, extra string) *CLIError {
	e := &CLIError{Code: code, Exit: exit}
	if title, fix, ok := doctor.Text(code, string(c.Lang)); ok {
		e.Message, e.Fix = sentence(title), sentence(fix)
	}
	if extra != "" {
		e.Message = strings.TrimSpace(e.Message + " " + extra)
	}
	return e
}

// sizeText is a byte count in the decimal units of the style guide.
func sizeText(n int64) string { return out.Size(n) }
