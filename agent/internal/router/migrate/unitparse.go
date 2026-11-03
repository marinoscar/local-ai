// Package migrate replaces the hand-written llama-qwen.service of a legacy node with the agent's
// localai-llama.service in place: same port, same API key, same Tailscale Serve and public URL, and the
// old model name kept as an alias (llama-router.md section 4.9, ROUTER-09).
//
// The engine runs the steps M0 to M10 and the rollback R1 to R6 of the spec. Every step is idempotent and
// journaled in progress.json, so a run that is killed anywhere resumes (or rolls back) to a consistent
// state, and the legacy service keeps serving until step M6 and is restored by any failure after it.
//
// Everything outside the process sits behind an interface (ports.go): the filesystem, systemd, programs
// (execx), the router's HTTP API, the build manager, node.yaml, state.json and the audit log. The tests
// run the whole migration on in-memory fakes with a manual clock and no sleeping. The real wiring is in
// internal/router/migrate/host. The migration runs as `aiagent migrate` from a root shell, never inside
// the daemon: the daemon has ProtectHome=read-only, and this moves files out of /home.
package migrate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Credential is one LoadCredential= entry: the credential id and the file it reads.
type Credential struct {
	ID   string
	Path string
}

// EnvVar is one Environment= assignment.
type EnvVar struct{ Name, Value string }

// LegacyUnit is what `systemctl cat llama-qwen.service` says, drop-ins included: the later setting
// wins, an empty assignment resets a list, as in systemd.
type LegacyUnit struct {
	User, Group         string
	SupplementaryGroups string
	WorkingDirectory    string
	Credentials         []Credential
	// Env is the effective environment in order; a name set twice keeps its last value.
	Env              []EnvVar
	EnvironmentFiles []string
	// ExecStart is the effective command: the binary and its arguments, quotes resolved, specifiers
	// (%d, %h, ...) left as written.
	ExecStart []string
	// Sources are the files the text came from (the "# /path" header lines of systemctl cat).
	Sources []string
	// DropIns are the sources that are drop-ins (under llama-qwen.service.d/).
	DropIns []string
}

// ErrUnitSyntax is returned for a unit that cannot be understood (an unterminated quote, no ExecStart).
var ErrUnitSyntax = errors.New("migrate: cannot read the legacy unit")

// ParseUnit reads the output of `systemctl cat llama-qwen.service`.
func ParseUnit(text string) (*LegacyUnit, error) {
	u := &LegacyUnit{}
	section := ""
	pending, continuing := "", false
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		var line string
		if continuing {
			// A continuation: the backslash-newline of the previous line became a space.
			line = pending + " " + strings.TrimSpace(raw)
			pending, continuing = "", false
		} else {
			trimmed := strings.TrimSpace(raw)
			if strings.HasPrefix(trimmed, "# /") {
				src := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
				u.Sources = append(u.Sources, src)
				if strings.Contains(src, ".service.d/") {
					u.DropIns = append(u.DropIns, src)
				}
				section = ""
				continue
			}
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
				continue
			}
			line = trimmed
		}
		if strings.HasSuffix(line, `\`) {
			pending, continuing = strings.TrimRight(strings.TrimSuffix(line, `\`), " \t"), true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || section != "Service" {
			continue
		}
		if err := u.set(strings.TrimSpace(key), strings.TrimSpace(val)); err != nil {
			return nil, err
		}
	}
	if continuing {
		return nil, fmt.Errorf("%w: a line ends with a backslash and nothing follows", ErrUnitSyntax)
	}
	if len(u.ExecStart) == 0 {
		return nil, fmt.Errorf("%w: no ExecStart= found", ErrUnitSyntax)
	}
	return u, nil
}

func (u *LegacyUnit) set(key, val string) error {
	switch key {
	case "User":
		u.User = val
	case "Group":
		u.Group = val
	case "SupplementaryGroups":
		u.SupplementaryGroups = val
	case "WorkingDirectory":
		u.WorkingDirectory = strings.TrimPrefix(val, "-")
	case "LoadCredential":
		if val == "" {
			u.Credentials = nil
			return nil
		}
		id, path, ok := strings.Cut(val, ":")
		if !ok || id == "" || path == "" {
			return fmt.Errorf("%w: LoadCredential= needs <id>:<path>", ErrUnitSyntax)
		}
		u.Credentials = append(u.Credentials, Credential{ID: id, Path: path})
	case "Environment":
		if val == "" {
			u.Env = nil
			return nil
		}
		toks, err := splitWords(val)
		if err != nil {
			return err
		}
		for _, t := range toks {
			n, v, ok := strings.Cut(t, "=")
			if !ok || n == "" {
				continue // systemd skips an assignment without a name
			}
			u.setEnv(n, v)
		}
	case "EnvironmentFile":
		if val == "" {
			u.EnvironmentFiles = nil
			return nil
		}
		u.EnvironmentFiles = append(u.EnvironmentFiles, strings.TrimPrefix(val, "-"))
	case "ExecStart":
		if val == "" {
			u.ExecStart = nil
			return nil
		}
		toks, err := splitWords(strings.TrimLeft(val, "@-:+!"))
		if err != nil {
			return err
		}
		if len(toks) > 0 {
			u.ExecStart = toks
		}
	}
	return nil
}

func (u *LegacyUnit) setEnv(name, value string) {
	for i := range u.Env {
		if u.Env[i].Name == name {
			u.Env[i].Value = value
			return
		}
	}
	u.Env = append(u.Env, EnvVar{Name: name, Value: value})
}

// splitWords splits the way systemd splits ExecStart and Environment: white space separates words,
// single and double quotes group (a quote of the other kind is literal inside), and a backslash escapes
// the next character (inside double quotes too).
func splitWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	in := false // a word is open (so "" yields an empty word)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t':
			if in {
				out = append(out, b.String())
				b.Reset()
				in = false
			}
		case c == '\'' || c == '"':
			in = true
			closed := false
			for i++; i < len(rs); i++ {
				d := rs[i]
				if d == c {
					closed = true
					break
				}
				if d == '\\' && c == '"' && i+1 < len(rs) {
					i++
					b.WriteRune(unescape(rs[i]))
					continue
				}
				b.WriteRune(d)
			}
			if !closed {
				return nil, fmt.Errorf("%w: an unterminated quote in %q", ErrUnitSyntax, clip(s, 80))
			}
		case c == '\\' && i+1 < len(rs):
			in = true
			i++
			b.WriteRune(unescape(rs[i]))
		default:
			in = true
			b.WriteRune(c)
		}
	}
	if in {
		out = append(out, b.String())
	}
	return out, nil
}

func unescape(r rune) rune {
	switch r {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	}
	return r
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// atoi parses a non-negative decimal.
func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil && n >= 0
}
