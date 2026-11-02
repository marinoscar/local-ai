package logx

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The oracle: the five rules exactly as agent.md section 4.11 writes them, as regular
// expressions. The scanners in rules.go must behave like them on any input.
var (
	oracleToken  = regexp.MustCompile(`(lat|nod|lai|pat|pg)_[A-Za-z0-9_-]{20,}`)
	oracleBearer = regexp.MustCompile(`Bearer\s+\S+`)
	oracleHex    = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	oracleIP     = regexp.MustCompile(`\b100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.\d{1,3}\.\d{1,3}\b`)
	oracleTail   = regexp.MustCompile(`tail[0-9a-f]{6}`)
)

func oracleScrub(s string) string {
	s = oracleBearer.ReplaceAllStringFunc(s, func(m string) string {
		tok := strings.TrimLeft(m[len("Bearer"):], " \t\r\n\f")
		return bearerText(tok)
	})
	s = oracleToken.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.IndexByte(m, '_')
		return m[:i+1] + "…" + m[len(m)-4:]
	})
	s = oracleHex.ReplaceAllStringFunc(s, func(m string) string { return m[:4] + "…" + m[len(m)-4:] })
	s = oracleIP.ReplaceAllStringFunc(s, func(m string) string {
		for _, o := range strings.Split(m, ".") {
			if n, _ := strconv.Atoi(o); n > 255 {
				return m
			}
		}
		return "<tailnet-ip>"
	})
	return oracleTail.ReplaceAllString(s, "<TAILNET>")
}

// fragments are the building blocks of the random inputs: every rule's keywords, boundaries and
// near misses.
var fragments = []string{
	"lat_", "nod_", "lai_", "pat_", "pg_", "localai_", "pg_dump_", "Bearer", "Bearer ", "bearer ", "Bearers", " ", "  ", "\t", "\n", "\v",
	"100.", "100.64.", "100.127.", "100.128.", "100.63.", "100.99.", "1100.", "x100.", "1", "7", "64", "127", "255", "256", "999", "1234", ".", ":", "/",
	"tail", "tails", "tailscale", "0a1b2c", "0A1B2C", "abcdef", "12345", "g", "_", "-", "X", "Z9", "…", "é", ";", "=", "\"", "(", ")",
	strings.Repeat("a", 19), strings.Repeat("a", 20), strings.Repeat("Ab3", 8), strings.Repeat("9f", 31), strings.Repeat("9f", 32), strings.Repeat("9f", 33),
	strings.Repeat("e", 63), strings.Repeat("e", 64), strings.Repeat("e", 65),
}

func randomInput(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.Intn(10) + 1; n > 0; n-- {
		b.WriteString(fragments[rng.Intn(len(fragments))])
	}
	return b.String()
}

// TestScanners_AgreeWithTheSpecRegexes compares the whole pipeline with the regular expressions.
// Inputs with two "Bearer" words are left out: there the scanner deliberately differs from the
// regular expression, which would swallow the second credential (see TestScrubString_TwoBearers).
func TestScanners_AgreeWithTheSpecRegexes(t *testing.T) {
	rng := rand.New(rand.NewSource(20261102))
	for i := 0; i < 30000; i++ {
		in := randomInput(rng)
		if strings.Count(in, "Bearer") > 1 {
			continue
		}
		if got, want := scrub(in, false), oracleScrub(in); got != want {
			t.Fatalf("input %q\n scanner %q\n  oracle %q", in, got, want)
		}
	}
}

func TestScrubString_TwoBearers(t *testing.T) {
	a, b := "Ab3dE6gH9jK2mN5pQ8sT1vWx", "Zy8xW7vU6tS5rQ4pO3nM2lKj"
	for _, sep := range []string{" ", ";", ",", ";b=", "\n"} {
		in := "Bearer " + a + sep + "Bearer " + b
		out := ScrubString(in)
		if strings.Contains(out, hidden(a)) || strings.Contains(out, hidden(b)) || strings.Contains(out, b[:10]) {
			t.Errorf("a credential survived with separator %q: %q", sep, out)
		}
	}
	in := "Bearer " + a + ";Bearer " + b
	if out := ScrubString(in); out != ScrubString(out) {
		t.Errorf("not idempotent: %q", out)
	}
}

func TestScrubString_BearerEdgeCases(t *testing.T) {
	for in, want := range map[string]string{
		"Bearer":                        "Bearer",
		"Bearers abcdefghijklmnop":      "Bearers abcdefghijklmnop",
		"Bearer\tabcdefghijkl and more": "Bearer …ijkl and more",
		"Bearer   abcdefghij":           "Bearer …ghij",
		"Bearer ":                       "Bearer ",
		"Bearer …abcd tail":             "Bearer …abcd tail",
		"Bearer abcdefgh":               "Bearer …", // 8 characters or fewer: nothing of it is kept
		"Bearer ééééééééé":              "Bearer …éééé",
	} {
		if got := ScrubString(in); got != want {
			t.Errorf("ScrubString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanners_UnchangedInputIsReturnedAsIs(t *testing.T) {
	in := "model qwen loaded in 183 ms on unit localai-llama.service, status 200, nothing secret here"
	allocs := testing.AllocsPerRun(100, func() { _ = ScrubString(in) })
	if allocs != 0 {
		t.Fatalf("a clean message allocated %v times", allocs)
	}
}
