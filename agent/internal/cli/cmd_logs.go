package cli

import (
	"context"
	"errors"
	"flag"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl logs [--unit u] [-n lines] [--since time] [-f]` (aictl.md section 4.15): the journal of one
// unit through GET /logs. The agent already scrubbed secrets. -f polls every 2 s with `since` set to
// the last line's time and prints only lines it has not printed yet.

const (
	logsDefaultLines = 100
	logsMaxLines     = 2000
)

// logUnits are the units GET /logs accepts (agent.md section 3.8.23).
var logUnits = []string{"agent", "llama", "kiwix", "whisper", "sync", "drill"}

// Tests change these: the poll interval of -f, the clock for a relative --since and the time zone
// the HH:MM:SS column is shown in.
var (
	logsFollowInterval = 2 * time.Second
	logsNow            = time.Now
	logsLocation       = time.Local
)

func init() {
	Register(&Command{
		Name:    "logs",
		Summary: "Show the log of the agent or of a service.",
		Usage:   "[--unit agent|llama|kiwix|whisper|sync|drill] [-n <lines>] [--since <time>] [-f]",
		Description: `Prints the newest lines of one unit's log, oldest first (default: the agent, 100 lines, at most 2000).
--since takes a time such as 2026-11-02T08:00:00Z or an age such as 15m or 2h.
-f keeps the command running and prints new lines as they arrive (Ctrl-C to stop).
Times are shown in this computer's time zone; with -v they are RFC 3339 in UTC.
--json prints the agent body; with -f it prints JSON Lines of {"at","priority","message"}.
The default timeout is 10 s per request; -f has no overall deadline unless --timeout is given.`,
		Flags: func(fs *flag.FlagSet) {
			fs.String("unit", "agent", "agent, llama, kiwix, whisper, sync or drill")
			fs.Int("n", logsDefaultLines, "number of lines (1 to 2000)")
			fs.String("since", "", "only lines after this time (RFC 3339) or age (15m, 2h)")
			fs.Bool("f", false, "keep running and print new lines")
		},
		Run: runLogs,
	})
}

func runLogs(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	unit := c.String("unit")
	if !contains(logUnits, unit) {
		return c.Err(CodeUsage, pick(c.Lang,
			"Unknown unit "+quote(unit)+". Use "+strings.Join(logUnits, ", ")+".",
			"Unidad desconocida "+quote(unit)+". Use "+strings.Join(logUnits, ", ")+"."))
	}
	n, _ := strconv.Atoi(c.String("n"))
	if n < 1 || n > logsMaxLines {
		return c.Err(CodeUsage, pick(c.Lang,
			"-n must be between 1 and 2000.", "-n debe estar entre 1 y 2000."))
	}
	since := ""
	if s := c.String("since"); s != "" {
		var err error
		if since, err = parseSince(s, logsNow()); err != nil {
			return c.Err(CodeUsage, pick(c.Lang,
				"Cannot read --since "+quote(s)+". Use a time like 2026-11-02T08:00:00Z or an age like 15m.",
				"No se entiende --since "+quote(s)+". Use una hora como 2026-11-02T08:00:00Z o una antigüedad como 15m."))
		}
	}
	q := func(since string, lines int) string {
		v := url.Values{"unit": {unit}, "lines": {strconv.Itoa(lines)}}
		if since != "" {
			v.Set("since", since)
		}
		return "/logs?" + v.Encode()
	}

	if c.Bool("f") {
		return logsFollow(c, func(since string, first bool) string {
			if first {
				return q(since, n)
			}
			return q(since, logsMaxLines)
		}, since)
	}

	ctx, cancel := roRequestContext(c)
	defer cancel()
	var lg wireLogs
	raw, err := roGetCtx(c, ctx, q(since, n), &lg)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderLogs(c.Out, c.Global.Verbose, lg.Lines)
	if lg.Truncated {
		c.Out.Warn(pick(c.Lang,
			"The output was cut at the agent's size limit. Ask for fewer lines with -n.",
			"La salida se cortó en el límite de tamaño del agente. Pida menos líneas con -n."))
	}
	return nil
}

// parseSince turns --since into the RFC 3339 time the agent wants: an RFC 3339 time as given, or an
// age ("15m", "2h") before now.
func parseSince(s string, now time.Time) (string, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return "", errors.New("bad since")
	}
	return now.Add(-d).UTC().Format(time.RFC3339), nil
}

// logLineText makes a log message safe to print: a control character (an escape sequence in a
// logged value could drive the terminal) becomes a space, a tab stays.
func logLineText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// logTime is the time column: HH:MM:SS in the local zone, or the RFC 3339 UTC time with -v. A time
// aictl cannot read is shown as sent.
func logTime(verbose bool, at string) string {
	t, ok := roWhen(at)
	if !ok {
		return at
	}
	if verbose {
		return out.Absolute(t)
	}
	return t.In(logsLocation).Format("15:04:05")
}

func renderLogs(p *out.Printer, verbose bool, lines []wireLogLine) {
	if len(lines) == 0 {
		p.Info(pick(p.Lang(), "No log lines.", "No hay líneas de registro."))
		return
	}
	for _, l := range lines {
		p.Result(logLine(verbose, l))
	}
}

func logLine(verbose bool, l wireLogLine) string {
	return logTime(verbose, l.At) + "  " + logLineText(l.Message)
}

// logsFollow prints the first batch and then polls. path builds the request for a given `since`;
// first says whether it is the opening request (-n lines) or a poll (up to 2000). Lines already
// printed are skipped: the journal's `since` is inclusive, so the lines of the last printed second
// come back; they are matched by content, so a new line in the same second is still printed.
func logsFollow(c *Call, path func(since string, first bool) string, since string) error {
	seen := map[string]int{} // lines printed at lastAt
	var lastAt time.Time
	lastRaw := since
	first := true
	for {
		ctx, cancel := roRequestContext(c)
		var lg wireLogs
		_, err := roGetCtx(c, ctx, path(lastRaw, first), &lg)
		cancel()
		if cerr := c.Ctx.Err(); cerr != nil {
			if errors.Is(cerr, context.Canceled) {
				return nil // Ctrl-C is how -f ends
			}
			return cerr
		}
		if err != nil {
			return err
		}
		first = false
		remaining := map[string]int{}
		for k, v := range seen {
			remaining[k] = v
		}
		for _, l := range lg.Lines {
			t, ok := roWhen(l.At)
			key := l.At + "\x00" + strconv.Itoa(l.Priority) + "\x00" + l.Message
			if ok {
				switch {
				case !lastAt.IsZero() && t.Before(lastAt):
					continue
				case t.Equal(lastAt):
					if remaining[key] > 0 {
						remaining[key]--
						continue
					}
					seen[key]++
				default:
					lastAt, lastRaw = t, l.At
					seen = map[string]int{key: 1}
					remaining = map[string]int{}
				}
			}
			if c.Global.JSON {
				if err := c.Out.JSON().Line(l); err != nil {
					return err
				}
			} else {
				c.Out.Result(logLine(c.Global.Verbose, l))
			}
		}
		if gw, ok := c.Stdout.(*guardWriter); ok && gw.broken {
			return nil // the reader went away (| head): stop following
		}
		select {
		case <-c.Ctx.Done():
			if errors.Is(c.Ctx.Err(), context.Canceled) {
				return nil
			}
			return c.Ctx.Err()
		case <-time.After(logsFollowInterval):
		}
	}
}
