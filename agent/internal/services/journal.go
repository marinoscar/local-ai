package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Limits of the journald tail (agent.md section 3.8.23).
const (
	// MinTailLines and MaxTailLines bound the lines parameter; DefaultTailLines is used when it is absent.
	MinTailLines     = 1
	MaxTailLines     = 2000
	DefaultTailLines = 200
	// JournalTimeout bounds one journalctl run.
	JournalTimeout = 5 * time.Second
	// JournalMaxOut caps what is read from journalctl; past it the tail is cut and Truncated is set.
	JournalMaxOut = 4 << 20
)

// defaultPriority is what a line gets when journald recorded none: informational.
const defaultPriority = 6

// ErrTailRange is returned by Tail for a lines value outside MinTailLines..MaxTailLines.
var ErrTailRange = errors.New("services: lines must be between 1 and 2000")

// JournalLine is one journal entry as GET /logs shows it.
type JournalLine struct {
	// At is the entry's realtime timestamp (UTC).
	At time.Time
	// Priority is the syslog priority, 0 (emergency) to 7 (debug).
	Priority int
	// Message is the MESSAGE field, scrubbed of secrets.
	Message string
}

// JournalTail is the result of Journal.Tail.
type JournalTail struct {
	// Unit is the systemd unit the lines belong to.
	Unit string
	// Lines are the entries, oldest first.
	Lines []JournalLine
	// Truncated is set when journalctl's output was cut at JournalMaxOut.
	Truncated bool
}

// Journal reads the journal through journalctl (agent.md section 3.8.23). It backs GET /logs, the
// logs.tail cloud job (AGENT-18) and OOM detection (AGENT-10).
type Journal struct {
	run  execx.Runner
	path string
}

// NewJournal returns a Journal that runs journalctl through run. An empty path means the journalctl
// that execx.Lookup finds in the fixed PATH, or /usr/bin/journalctl.
func NewJournal(run execx.Runner, path string) *Journal {
	if path == "" {
		path = "/usr/bin/journalctl"
		if p, err := execx.Lookup("journalctl"); err == nil {
			path = p
		}
	}
	return &Journal{run: run, path: path}
}

// JournalError is a journalctl run that exited non-zero.
type JournalError struct {
	Unit     string
	ExitCode int
	Stderr   string
}

func (e *JournalError) Error() string {
	return fmt.Sprintf("services: journalctl -u %s: exit %d: %s", e.Unit, e.ExitCode, e.Stderr)
}

// journalArgv is the exact argv of Tail: `journalctl -u <unit> -n <lines> [--since <t>] -o json
// --no-pager`. The unit and the time are separate elements, never joined into one string.
func journalArgv(unit string, lines int, since time.Time) []string {
	args := []string{"-u", unit, "-n", strconv.Itoa(lines)}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Truncate(time.Second).Format("2006-01-02 15:04:05")+" UTC")
	}
	return append(args, "-o", "json", "--no-pager")
}

// logUnitAllowed reports whether the unit is one of the GET /logs units.
func logUnitAllowed(unit string) bool {
	for _, u := range logUnits {
		if u == unit {
			return true
		}
	}
	return false
}

// Tail returns the last lines entries of a unit's journal, optionally only those since a time (the
// zero time means no limit). unit is a full unit name from the GET /logs table (LogUnit); anything
// else is a *ViolationError. lines must be within MinTailLines..MaxTailLines (ErrTailRange).
func (j *Journal) Tail(ctx context.Context, unit string, lines int, since time.Time) (JournalTail, error) {
	if !logUnitAllowed(unit) {
		return JournalTail{}, &ViolationError{Unit: unit, Verb: VerbLogs}
	}
	if lines < MinTailLines || lines > MaxTailLines {
		return JournalTail{}, ErrTailRange
	}
	res, err := j.run.Run(ctx, execx.Cmd{
		Path: j.path, Args: journalArgv(unit, lines, since), Timeout: JournalTimeout, MaxOut: JournalMaxOut,
	})
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return JournalTail{}, ctx.Err()
		case errors.Is(err, execx.ErrTimeout):
			return JournalTail{}, fmt.Errorf("%w: journalctl -u %s", ErrTimeout, unit)
		}
		return JournalTail{}, fmt.Errorf("services: journalctl -u %s: %w", unit, err)
	}
	if res.ExitCode != 0 {
		return JournalTail{}, &JournalError{Unit: unit, ExitCode: res.ExitCode, Stderr: cleanStderr(res.Stderr)}
	}
	out := res.Stdout
	if res.Truncated {
		// The cap cut the output somewhere: the last line is usually partial, so it is dropped.
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
	}
	return JournalTail{Unit: unit, Lines: ParseJournal(out), Truncated: res.Truncated}, nil
}

// journalRecord is the part of a `journalctl -o json` line the agent reads. The fields stay raw
// because journald encodes them in several ways.
type journalRecord struct {
	Realtime json.RawMessage `json:"__REALTIME_TIMESTAMP"`
	Priority json.RawMessage `json:"PRIORITY"`
	Message  json.RawMessage `json:"MESSAGE"`
}

// ParseJournal parses `journalctl -o json` output (one JSON object per line) into lines, oldest first.
// Every field is decoded first and the scrubber runs on the decoded MESSAGE, never on the raw line: a
// Bearer match runs to the next whitespace and would eat the closing quote of the JSON string. A line
// that is not a JSON object (journalctl's "-- No entries --") or has no usable timestamp is skipped.
func ParseJournal(out []byte) []JournalLine {
	lines := []JournalLine{}
	for _, raw := range bytes.Split(out, []byte{'\n'}) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		at, ok := journalTime(rec.Realtime)
		if !ok {
			continue
		}
		lines = append(lines, JournalLine{At: at, Priority: journalPriority(rec.Priority), Message: logx.ScrubString(journalText(rec.Message))})
	}
	return lines
}

// journalTime decodes __REALTIME_TIMESTAMP: microseconds since the epoch, as a string (journalctl's
// usual form) or a number.
func journalTime(raw json.RawMessage) (time.Time, bool) {
	s := strings.Trim(string(raw), `"`)
	us, err := strconv.ParseInt(s, 10, 64)
	if err != nil || us <= 0 {
		return time.Time{}, false
	}
	return time.UnixMicro(us).UTC(), true
}

// journalPriority decodes PRIORITY ("6" as a string, or a number) and clamps it to 0..7.
func journalPriority(raw json.RawMessage) int {
	n, err := strconv.Atoi(strings.Trim(string(raw), `"`))
	if err != nil || n < 0 || n > 7 {
		return defaultPriority
	}
	return n
}

// journalText decodes a journal field value. journald emits a string, or, when the value is not valid
// UTF-8 or holds control bytes, an array of byte values; a field that occurs more than once is an array
// of those; a value over 4 KiB is null (journalctl without --all). Several values are joined with a
// newline. The result is valid UTF-8.
func journalText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.ToValidUTF8(s, "�")
	}
	var bytesArr []int
	if json.Unmarshal(raw, &bytesArr) == nil {
		return bytesText(bytesArr)
	}
	var many []json.RawMessage
	if json.Unmarshal(raw, &many) == nil {
		parts := make([]string, 0, len(many))
		for _, m := range many {
			parts = append(parts, journalText(m))
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func bytesText(vals []int) string {
	b := make([]byte, 0, len(vals))
	for _, v := range vals {
		if v < 0 || v > 255 {
			v = '?'
		}
		b = append(b, byte(v))
	}
	return strings.ToValidUTF8(string(b), "�")
}
