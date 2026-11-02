package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"syscall"
)

// This file is the minimal problem output the framework needs for its own errors and warnings
// (aictl.md section 3.3 "How a problem is shown", section 3.5 for --json). The full output layer
// (colour, tables, sizes and times, confirmations) is AICTL-02 and replaces the printing here
// without changing reporter's callers.

// problem is an error or warning ready to show: the fields of the agent's envelope plus the request
// id.
type problem struct {
	Severity  string
	Code      string
	Message   string
	Fix       string
	Details   map[string]any
	RequestID string
}

// problemOf describes err. An unexpected error (not an *APIError or *CLIError) shows its text with
// no code; commands should return typed errors.
func problemOf(err error) problem {
	var ae *APIError
	if errors.As(err, &ae) {
		return problem{Severity: sevError, Code: ae.Code, Message: ae.Message, Fix: ae.Fix, Details: ae.Details, RequestID: ae.RequestID}
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		sev := sevError
		if info, ok := codeTable[ce.Code]; ok {
			sev = info.Severity
		}
		return problem{Severity: sev, Code: ce.Code, Message: ce.Message, Fix: ce.Fix, Details: ce.Details, RequestID: ce.RequestID}
	}
	return problem{Severity: sevError, Message: err.Error()}
}

// envelope is the error document of --json mode: the agent's shape, built by the CLI when the
// failure is its own.
func (p problem) envelope() []byte {
	d := p.Details
	if d == nil {
		d = map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": p.Code, "message": p.Message, "fix": p.Fix, "details": d,
	}})
	return b
}

// reporter prints the framework's problems.
type reporter struct {
	env Env
	g   Global
}

func (r reporter) utf8() bool {
	for _, k := range []string{"LC_ALL", "LANG"} {
		if v := strings.ToUpper(r.env.Getenv(k)); v != "" {
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return false
}

func (r reporter) symbol(sev string) string {
	u := r.utf8()
	switch sev {
	case sevWarning:
		if u {
			return "!"
		}
		return "[!]"
	case sevInfo:
		if u {
			return "i"
		}
		return "[i]"
	}
	if u {
		return "✗"
	}
	return "[x]"
}

// show prints one problem in the layout of section 3.3.
func (r reporter) show(p problem) {
	w := r.env.Stderr
	line := r.symbol(p.Severity) + " " + p.Message
	if p.Code != "" {
		line += "  [" + p.Code + "]"
	}
	fmt.Fprintln(w, line)
	if p.Fix != "" {
		label := "→ Fix:"
		if r.g.Lang == LangES {
			label = "→ Solución:"
		}
		fmt.Fprintf(w, "  %s %s\n", label, p.Fix)
	}
	if r.g.Verbose && len(p.Details) > 0 {
		if b, err := json.Marshal(p.Details); err == nil {
			fmt.Fprintf(w, "  details: %s\n", b)
		}
	}
	if p.RequestID != "" && (r.g.Verbose || p.Code == CodeBadResponse) {
		fmt.Fprintf(w, "  request: %s\n", p.RequestID)
	}
}

// fail reports err and returns its exit code. An *ExitError and a broken pipe print nothing. With
// --json the envelope goes to stdout and stderr stays empty.
func (r reporter) fail(err error) int {
	code := ExitFor(err)
	var ee *ExitError
	if errors.As(err, &ee) || errors.Is(err, syscall.EPIPE) {
		return code
	}
	p := problemOf(err)
	if r.g.JSON {
		fmt.Fprintf(r.env.Stdout, "%s\n", p.envelope())
		return code
	}
	r.show(p)
	return code
}

// warn prints queued warnings to stderr after a command, unless --quiet, or --json with a failed
// command.
func (r reporter) warn(ws []*CLIError, failed bool) {
	if r.g.Quiet || (r.g.JSON && failed) {
		return
	}
	for _, w := range ws {
		r.show(problemOf(w))
	}
}
