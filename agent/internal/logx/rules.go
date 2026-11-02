package logx

import (
	"strings"
	"unicode/utf8"
)

// The substring rules of agent.md section 4.11, written as single-pass scanners instead of
// regular expressions: Go's regexp package is too slow for the "under 5 microseconds for a
// 200-byte message" budget once a rule matches. rules_test.go keeps the specification's
// regular expressions as an oracle and checks these scanners against them on random input:
//
//	(lat|nod|lai|pat|pg)_[A-Za-z0-9_-]{20,}      tokens
//	Bearer\s+\S+                                 bearer credentials
//	\b[0-9a-f]{64}\b                             64-hex keys
//	\b100\.\d{1,3}\.\d{1,3}\.\d{1,3}\b           CGNAT candidates, the 100.64/10 range checked in code
//	tail[0-9a-f]{6}                              the tailnet name
//
// Each scanner returns its input unchanged (and allocates nothing) when nothing matches.

// ScrubString applies every scrubbing rule of agent.md section 4.11 to s: bearer credentials,
// credential-prefixed tokens, 64-hex keys, tailnet addresses and the tailnet name. It is
// idempotent, safe for concurrent use, and returns s itself when nothing matches. The same
// function scrubs /logs output, logs.tail results and the cloud state push.
func ScrubString(s string) string { return scrub(s, false) }

// scrub is ScrubString; hexExempt skips the 64-hex rule (a value under a key ending in sha256)
// unless the value contains the live key.
func scrub(s string, hexExempt bool) string {
	if len(s) < 8 { // the shortest match of any rule
		return s
	}
	if strings.Contains(s, "Bearer") {
		s = scrubBearers(s)
	}
	s = scrubTokens(s)
	if !hexExempt || containsLiveKey(s) {
		s = scrubHexKeys(s)
	}
	if strings.Contains(s, "100.") {
		s = scrubCGNATs(s)
	}
	if strings.Contains(s, "tail") {
		s = scrubTailnetNames(s)
	}
	return s
}

func containsLiveKey(s string) bool {
	k := currentKey.Load()
	return k != nil && strings.Contains(strings.ToLower(s), *k)
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isWord is the \w class of RE2 (ASCII), which decides where \b falls.
func isWord(c byte) bool { return isAlnum(c) || c == '_' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || (c >= 'a' && c <= 'f') }

func isTokenChar(c byte) bool { return isAlnum(c) || c == '_' || c == '-' }

// isSpace is the \s class of RE2: [\t\n\f\r ].
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// replacer builds an output string lazily, so that an unchanged input costs nothing.
type replacer struct {
	src  string
	out  strings.Builder
	last int // src[:last] has been copied
	hit  bool
}

func (r *replacer) replace(from, to int, with string) {
	if !r.hit {
		r.hit = true
		r.out.Grow(len(r.src) + 16)
	}
	r.out.WriteString(r.src[r.last:from])
	r.out.WriteString(with)
	r.last = to
}

func (r *replacer) result() string {
	if !r.hit {
		return r.src
	}
	r.out.WriteString(r.src[r.last:])
	return r.out.String()
}

// scrubTokens rewrites (lat|nod|lai|pat|pg)_<20+ token characters> to <prefix>_…<last4>.
func scrubTokens(s string) string {
	r := replacer{src: s}
	for i := 0; i < len(s); {
		p := tokenPrefixLen(s, i)
		if p == 0 {
			i++
			continue
		}
		j := i + p
		for j < len(s) && isTokenChar(s[j]) {
			j++
		}
		if j-(i+p) < 20 {
			i++
			continue
		}
		r.replace(i, j, s[i:i+p]+"…"+s[j-4:j])
		i = j
	}
	return r.result()
}

// tokenPrefixLen returns the length of a credential prefix (with its underscore) starting at
// s[i], or 0.
func tokenPrefixLen(s string, i int) int {
	rest := s[i:]
	switch s[i] {
	case 'l':
		if strings.HasPrefix(rest, "lat_") || strings.HasPrefix(rest, "lai_") {
			return 4
		}
	case 'n':
		if strings.HasPrefix(rest, "nod_") {
			return 4
		}
	case 'p':
		if strings.HasPrefix(rest, "pat_") {
			return 4
		}
		if strings.HasPrefix(rest, "pg_") {
			return 3
		}
	}
	return 0
}

// scrubBearers rewrites Bearer\s+\S+ to "Bearer …<last4>". The rule's \S+ would swallow
// everything up to the next whitespace, so in "Bearer a;b=Bearer c" the second credential would be
// eaten as part of the first and left in clear. A match therefore ends where another "Bearer" word
// starts, and scanning resumes there.
func scrubBearers(s string) string {
	const word = "Bearer"
	r := replacer{src: s}
	for i := 0; ; {
		k := strings.Index(s[i:], word)
		if k < 0 {
			break
		}
		start := i + k
		j := start + len(word)
		ws := j
		for j < len(s) && isSpace(s[j]) {
			j++
		}
		if j == ws { // no whitespace after the word: not a credential ("Bearers", "Bearer:")
			i = start + len(word)
			continue
		}
		tokStart := j
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		if j == tokStart { // only whitespace until the end
			i = start + len(word)
			continue
		}
		if c := strings.Index(s[tokStart:j], word); c >= 0 {
			j = tokStart + c
		}
		r.replace(start, j, bearerText(s[tokStart:j]))
		i = j
	}
	return r.result()
}

// bearerText renders a scrubbed bearer credential. An already scrubbed one is kept as it is.
func bearerText(tok string) string {
	if strings.HasPrefix(tok, "…") {
		return "Bearer " + tok
	}
	if utf8.RuneCountInString(tok) <= 8 {
		return "Bearer …"
	}
	return "Bearer …" + lastRunes(tok, 4)
}

// scrubHexKeys rewrites a 64-digit lower-case hex string that stands alone (word boundaries on
// both sides) to <first4>…<last4>. A longer or shorter run of hex digits is not matched.
func scrubHexKeys(s string) string {
	r := replacer{src: s}
	for i := 0; i < len(s); {
		if !isHex(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isHex(s[j]) {
			j++
		}
		if j-i == 64 && (i == 0 || !isWord(s[i-1])) && (j == len(s) || !isWord(s[j])) {
			r.replace(i, j, s[i:i+4]+"…"+s[j-4:j])
		}
		i = j
	}
	return r.result()
}

// scrubCGNATs rewrites addresses of the Tailscale CGNAT range (100.64/10) to <tailnet-ip>.
func scrubCGNATs(s string) string {
	r := replacer{src: s}
	for i := 0; ; {
		k := strings.Index(s[i:], "100.")
		if k < 0 {
			break
		}
		start := i + k
		i = start + 1
		if start > 0 && isWord(s[start-1]) { // \b before the first octet
			continue
		}
		var oct [3]int
		j := start + 4
		ok := true
		for n := 0; n < 3 && ok; n++ {
			d := j
			v := 0
			for j < len(s) && isDigit(s[j]) && j-d < 4 {
				v = v*10 + int(s[j]-'0')
				j++
			}
			if j == d || j-d > 3 {
				ok = false
				break
			}
			oct[n] = v
			if n < 2 {
				if j >= len(s) || s[j] != '.' {
					ok = false
					break
				}
				j++
			}
		}
		if !ok || (j < len(s) && isWord(s[j])) { // \b after the last octet
			continue
		}
		if oct[0] < 64 || oct[0] > 127 || oct[1] > 255 || oct[2] > 255 {
			continue
		}
		r.replace(start, j, "<tailnet-ip>")
		i = j
	}
	return r.result()
}

// scrubTailnetNames rewrites tail[0-9a-f]{6} to <TAILNET>.
func scrubTailnetNames(s string) string {
	const word = "tail"
	r := replacer{src: s}
	for i := 0; ; {
		k := strings.Index(s[i:], word)
		if k < 0 {
			break
		}
		start := i + k
		end := start + len(word) + 6
		i = start + 1
		if end > len(s) {
			break
		}
		match := true
		for _, c := range []byte(s[start+len(word) : end]) {
			if !isHex(c) {
				match = false
				break
			}
		}
		if match {
			r.replace(start, end, "<TAILNET>")
			i = end
		}
	}
	return r.result()
}
