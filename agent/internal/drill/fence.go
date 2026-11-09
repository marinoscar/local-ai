package drill

import (
	"strings"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// FenceCheckID is check 1 (drill.md section 4.2), the check that proves the network fence works. The
// engine also hangs the fence's report on it (annotateFence).
const FenceCheckID = "drl.fence"

// maxBlockedChars bounds the "blocked" detail: the callers joined with "; " (drill.md section 4.4).
const maxBlockedChars = 500

// FenceDetails renders what a fence refused as the flat details of the check that carries
// LAI-DRL-091: {"blocked_count": 1, "blocked": "huggingface.co:443 (sync.resolver)"}. Only strings,
// numbers, booleans and null are allowed in a check's details (the cloud's DrillResultsV1, cloud-app.md
// section 3.6), so the callers are one "; "-joined string of at most 500 characters.
func FenceDetails(r FenceReport) map[string]any {
	d := map[string]any{"blocked_count": r.BlockedDials}
	if len(r.Callers) > 0 {
		d["blocked"] = clipChars(strings.Join(r.Callers, "; "), maxBlockedChars)
	}
	return d
}

func clipChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// annotateFence puts the fence's report on the fence check of the run, when it ran and the fence
// refused something: its details gain blocked_count and blocked, and a check that passed becomes a
// warning with LAI-DRL-091 (a feature tried to use the internet, which is what the check exists to
// find out). A failed check keeps its own code. recs is not modified.
func annotateFence(recs []record, r FenceReport) []record {
	if r.BlockedDials <= 0 {
		return recs
	}
	out := append([]record(nil), recs...)
	for i := range out {
		if out[i].ID != FenceCheckID {
			continue
		}
		details := make(map[string]any, len(out[i].Details)+2)
		for k, v := range out[i].Details {
			details[k] = v
		}
		for k, v := range FenceDetails(r) {
			details[k] = v
		}
		out[i].Details = details
		if out[i].Result == ResultPass {
			out[i].Result, out[i].Code = ResultWarn, CodeFenceDial
		}
	}
	return out
}

// SubprocessEnv returns the environment for a subprocess a check starts (drill.md section 4.4): base
// (KEY=VALUE, usually os.Environ()) without any proxy variable and with HTTP_PROXY and HTTPS_PROXY
// pointing at http://127.0.0.1:9, a closed port, so a tool that tries the network fails at once.
func (e *Env) SubprocessEnv(base []string) []string { return netx.DeadProxyEnv(base) }
