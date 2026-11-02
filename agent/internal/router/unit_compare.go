package router

import (
	"sort"
	"strings"
)

// unitDirectives returns the directives of one section of a unit file in file order, as
// name -> values in order of appearance. Blank lines and comment lines (# or ;) are ignored, and a
// line that ends in a backslash is joined with the next one the way systemd does, so a re-wrapped
// ExecStart compares equal to the original.
func unitDirectives(unit []byte, section string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	var pending string // a continued line being assembled
	continued := false
	for _, raw := range strings.Split(string(unit), "\n") {
		line := strings.TrimSpace(raw)
		if continued {
			// A comment line inside a continuation is dropped by systemd.
			if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			pending += " " + line
		} else {
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
			pending = line
		}
		if strings.HasSuffix(pending, `\`) {
			pending = strings.TrimSpace(strings.TrimSuffix(pending, `\`))
			continued = true
			continue
		}
		continued = false
		line = pending
		if line[0] == '[' && line[len(line)-1] == ']' {
			cur = line[1 : len(line)-1]
			continue
		}
		if cur != section {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		out[name] = append(out[name], strings.TrimSpace(value))
	}
	return out
}

// ChangedSettings names the [Service] directives that differ between two renderings of the unit,
// sorted: a directive added, removed or given other values (repeated directives compare in order).
// Comments and the [Unit] and [Install] sections do not count: they change nothing a running
// process has been started with.
func ChangedSettings(old, new []byte) []string {
	a, b := unitDirectives(old, "Service"), unitDirectives(new, "Service")
	seen := map[string]bool{}
	var out []string
	for name, av := range a {
		seen[name] = true
		if !equalValues(av, b[name]) {
			out = append(out, name)
		}
	}
	for name := range b {
		if !seen[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func equalValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RestartRequired reports whether a running llama must be restarted for the new unit to take
// effect (llama-router.md section 3.2): the [Service] section differs from the old one, which is
// what the process was started from (an ExecStart with other arguments, another environment, other
// limits). A new file (no old content) needs no restart: nothing was started from it. Whether to
// restart is never decided here; the agent raises LAI-AGT-016 and the operator restarts llama.
func RestartRequired(old, new []byte) bool {
	if len(strings.TrimSpace(string(old))) == 0 {
		return false
	}
	return len(ChangedSettings(old, new)) > 0
}
