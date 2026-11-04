package detect

import (
	"context"
	"strconv"
	"strings"
)

// Listener is one listening TCP socket.
type Listener struct {
	Addr    string // "127.0.0.1", "0.0.0.0", "::" ...
	Port    int
	Process string // "" when ss could not say (not root)
}

// ParseSS parses `ss -Hltnp` output.
func ParseSS(text string) []Listener {
	var out []Listener
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		local := f[3]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		l := Listener{Addr: strings.Trim(local[:i], "[]"), Port: port}
		if j := strings.Index(line, `(("`); j >= 0 {
			rest := line[j+3:]
			if k := strings.Index(rest, `"`); k >= 0 {
				l.Process = rest[:k]
			}
		}
		out = append(out, l)
	}
	return out
}

// Listeners runs ss. ok is false when ss is missing or fails.
func (e Env) Listeners(ctx context.Context) ([]Listener, bool) {
	out, code, err := e.run(ctx, "ss", "-Hltnp")
	if err != nil || code != 0 {
		return nil, false
	}
	return ParseSS(out), true
}

// PortRule says which processes may hold a port (the node's own services).
type PortRule struct {
	Port int
	Ours []string
}

// PortRules are the ports the node needs. 8080 is held only by the legacy llama-qwen or our router.
var PortRules = []PortRule{
	{7070, []string{"aiagent", "localai-agent"}},
	{8888, []string{"kiwix-serve", "localai-kiwix"}},
	{8081, []string{"whisper-server", "localai-whisper"}},
	{8080, []string{"llama-server", "llama-qwen", "localai-llama"}},
}

// PortConflict is a needed port held by a program that is not ours.
type PortConflict struct {
	Port    int
	Process string
}

// PortConflicts returns every needed port that a foreign program holds on a loopback or wildcard
// address. A holder whose name ss could not report is "an unknown program".
func PortConflicts(ls []Listener) []PortConflict {
	var out []PortConflict
	for _, r := range PortRules {
		for _, l := range ls {
			if l.Port != r.Port || !relevantAddr(l.Addr) {
				continue
			}
			ours := false
			for _, n := range r.Ours {
				if l.Process == n {
					ours = true
				}
			}
			if !ours {
				p := l.Process
				if p == "" {
					p = "an unknown program"
				}
				out = append(out, PortConflict{r.Port, p})
				break
			}
		}
	}
	return out
}

func relevantAddr(a string) bool {
	switch a {
	case "127.0.0.1", "0.0.0.0", "*", "::", "::1":
		return true
	}
	return false
}
