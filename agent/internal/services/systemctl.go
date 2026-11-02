package services

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// showProperties is the property list of `systemctl show -p` (agent.md section 4.3.2).
const showProperties = "ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState"

// maxShowOut caps systemctl's output: a show of eight properties is far smaller.
const maxShowOut = 64 << 10

// maxStderr is how much of systemctl's error text an error keeps.
const maxStderr = 512

// Systemctl is the Manager that runs systemctl (agent.md section 4.3.2): the fallback when the system
// bus is not reachable (a rescue shell, a container in CI). Every call is a fixed argv through execx.
type Systemctl struct {
	run  execx.Runner
	path string
	opt  options
}

// NewSystemctl returns a systemctl Manager running its commands through run.
func NewSystemctl(run execx.Runner, opts ...Option) *Systemctl {
	o := newOptions(opts)
	path := o.path
	if path == "" {
		path = "/usr/bin/systemctl"
		if p, err := execx.Lookup("systemctl"); err == nil {
			path = p
		}
	}
	return &Systemctl{run: run, path: path, opt: o}
}

// verbArgs maps a Manager verb to the systemctl sub-command.
var verbArgs = map[Verb]string{
	VerbStart: "start", VerbStop: "stop", VerbRestart: "restart",
	VerbEnable: "enable", VerbDisable: "disable", VerbMask: "mask", VerbUnmask: "unmask",
	VerbDaemonReload: "daemon-reload",
}

// verbArgv is the exact argv of a verb: `systemctl --no-pager --no-ask-password <verb> <unit>`
// (daemon-reload names no unit).
func verbArgv(verb Verb, unit string) []string {
	args := []string{"--no-pager", "--no-ask-password", verbArgs[verb]}
	if verb != VerbDaemonReload {
		args = append(args, unit)
	}
	return args
}

// showArgv is the exact argv of State: `systemctl show <unit> -p <properties>`.
func showArgv(unit string) []string { return []string{"show", unit, "-p", showProperties} }

// showEnv pins the time zone and the locale so that the timestamps of `systemctl show` parse the
// same on every machine.
func showEnv() []string { return []string{"PATH=" + execx.FixedPath, "TZ=UTC", "LC_ALL=C"} }

func (s *Systemctl) verb(ctx context.Context, verb Verb, unit string) error {
	timeout := s.opt.callTimeout
	if verb == VerbStart || verb == VerbStop || verb == VerbRestart {
		timeout = s.opt.jobTimeout
	}
	res, err := s.run.Run(ctx, execx.Cmd{Path: s.path, Args: verbArgv(verb, unit), Timeout: timeout, MaxOut: maxShowOut})
	if err != nil {
		return s.mapRunError(ctx, err, verb, unit)
	}
	if res.ExitCode != 0 {
		return &CommandError{Unit: unit, Verb: verb, ExitCode: res.ExitCode, Stderr: cleanStderr(res.Stderr)}
	}
	return nil
}

func (s *Systemctl) mapRunError(ctx context.Context, err error, verb Verb, unit string) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, execx.ErrTimeout):
		return fmt.Errorf("%w: systemctl %s %s", ErrTimeout, verb, unit)
	}
	return fmt.Errorf("services: systemctl %s %s: %w", verb, unit, err)
}

func cleanStderr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxStderr {
		s = s[:maxStderr]
	}
	return logx.ScrubString(s)
}

// State implements Manager.
func (s *Systemctl) State(ctx context.Context, unit string) (UnitState, error) {
	res, err := s.run.Run(ctx, execx.Cmd{Path: s.path, Args: showArgv(unit), Timeout: s.opt.callTimeout, MaxOut: maxShowOut, Env: showEnv()})
	if err != nil {
		return UnitState{}, s.mapRunError(ctx, err, VerbState, unit)
	}
	if res.ExitCode != 0 {
		return UnitState{}, &CommandError{Unit: unit, Verb: VerbState, ExitCode: res.ExitCode, Stderr: cleanStderr(res.Stderr)}
	}
	return ParseShow(unit, res.Stdout)
}

// Start implements Manager.
func (s *Systemctl) Start(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbStart, unit)
}

// Stop implements Manager.
func (s *Systemctl) Stop(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbStop, unit)
}

// Restart implements Manager.
func (s *Systemctl) Restart(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbRestart, unit)
}

// Enable implements Manager.
func (s *Systemctl) Enable(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbEnable, unit)
}

// Disable implements Manager.
func (s *Systemctl) Disable(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbDisable, unit)
}

// Mask implements Manager.
func (s *Systemctl) Mask(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbMask, unit)
}

// Unmask implements Manager.
func (s *Systemctl) Unmask(ctx context.Context, unit string) error {
	return s.verb(ctx, VerbUnmask, unit)
}

// DaemonReload implements Manager.
func (s *Systemctl) DaemonReload(ctx context.Context) error {
	return s.verb(ctx, VerbDaemonReload, "")
}

// Subscribe implements Manager by polling: it sends the state of every unit at once, then again
// whenever it differs from the last one sent, checking every poll interval. A poll that fails keeps
// the last state (the next poll tries again), so a busy systemd does not look like an outage.
func (s *Systemctl) Subscribe(ctx context.Context, units []string) (<-chan UnitState, error) {
	out := make(chan UnitState, 16)
	units = append([]string(nil), units...)
	go func() {
		defer close(out)
		last := map[string]UnitState{}
		poll := func() bool {
			for _, u := range units {
				st, err := s.State(ctx, u)
				if err != nil {
					continue
				}
				if prev, ok := last[u]; ok && prev.Same(st) {
					continue
				}
				last[u] = st
				select {
				case out <- st:
				case <-ctx.Done():
					return false
				}
			}
			return ctx.Err() == nil
		}
		if !poll() {
			return
		}
		t := s.opt.clock.NewTicker(s.opt.pollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C():
				if !poll() {
					return
				}
			}
		}
	}()
	return out, nil
}

var _ Manager = (*Systemctl)(nil)

// ParseShow parses the output of `systemctl show <unit> -p ...` (Key=Value lines) into a UnitState.
// A property that is absent (a timer has no MainPID) or unset stays zero; a missing ActiveState or a
// malformed number is an error, since the output is then not what the agent asked for.
func ParseShow(unit string, out []byte) (UnitState, error) {
	st := UnitState{Unit: unit}
	seenActive := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		var err error
		switch key {
		case "ActiveState":
			st.ActiveState, seenActive = val, val != ""
		case "SubState":
			st.SubState = val
		case "UnitFileState":
			st.UnitFileState = val
		case "MainPID":
			st.MainPID, err = parseCount(key, val)
		case "NRestarts":
			st.NRestarts, err = parseCount(key, val)
		case "ExecMainStatus":
			st.ExecMainStatus, err = parseCount(key, val)
		case "ActiveEnterTimestamp":
			st.ActiveEnterTimestamp, err = parseShowTime(key, val)
		case "ExecMainExitTimestamp":
			st.ExecMainExitTimestamp, err = parseShowTime(key, val)
		}
		if err != nil {
			return UnitState{}, err
		}
	}
	if err := sc.Err(); err != nil {
		return UnitState{}, fmt.Errorf("services: read systemctl show output: %w", err)
	}
	if !seenActive {
		return UnitState{}, fmt.Errorf("services: systemctl show %s: no ActiveState in the output", unit)
	}
	return st, nil
}

func parseCount(key, val string) (int, error) {
	if val == "" || val == "[not set]" || val == "n/a" {
		return 0, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("services: systemctl show: %s=%q is not a number", key, val)
	}
	return n, nil
}

// showTimeLayouts are the formats systemctl prints a timestamp in under TZ=UTC and LC_ALL=C, after the
// weekday is cut off: seconds, or microseconds with --timestamp=us.
var showTimeLayouts = []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05.999999 MST"}

func parseShowTime(key, val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" || val == "n/a" || val == "[not set]" || val == "0" {
		return time.Time{}, nil
	}
	// "Mon 2026-11-02 08:00:05 UTC": the weekday carries nothing.
	if i := strings.IndexByte(val, ' '); i == 3 {
		val = val[i+1:]
	}
	for _, layout := range showTimeLayouts {
		if t, err := time.Parse(layout, val); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("services: systemctl show: %s=%q is not a timestamp", key, val)
}
