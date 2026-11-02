package logx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// Every fake credential in this file is built at run time from pieces, so that no literal in the
// source matches scripts/check-redaction.sh (the repository is public). None of them is real.

// fakeToken returns prefix + "_" + a 24-character body, for example lat_AbCd....
func fakeToken(prefix string) string { return prefix + "_" + "Ab3dE6gH9jK2mN5pQ8sT1vWx" }

// fakeHex returns a 64-digit lower-case hex string made of seed repeated.
func fakeHex(seed string) string { return strings.Repeat(seed, 64/len(seed)) }

func fakeTailnetIP(second int) string { return fmt.Sprintf("%d.%d.%d.%d", 100, second, 7, 9) }

func fakeTailnetName() string { return "tail" + "0a1b2c" }

// hidden returns the part of secret that a scrubbed rendering must not contain.
func hidden(secret string) string {
	if len(secret) > 16 {
		return secret[6 : len(secret)-5]
	}
	return secret
}

type credential struct {
	name   string
	secret string // the full value as it would be logged
	gone   string // text that must not survive scrubbing
}

func credentials() []credential {
	var cs []credential
	for _, p := range []string{"lat", "nod", "lai", "pat", "pg"} {
		t := fakeToken(p)
		cs = append(cs, credential{p + " token", t, hidden(t)})
	}
	b := "Ab3dE6gH9jK2mN5pQ8sT1vWx"
	cs = append(cs,
		credential{"bearer", "Bearer " + b, hidden(b)},
		credential{"64-hex key", fakeHex("9f3c"), hidden(fakeHex("9f3c"))},
		credential{"tailnet ip", fakeTailnetIP(64), fakeTailnetIP(64)},
		credential{"tailnet ip top of range", fakeTailnetIP(127), fakeTailnetIP(127)},
		credential{"tailnet name", fakeTailnetName(), fakeTailnetName()},
	)
	return cs
}

func TestScrubString_EveryCredentialFamily(t *testing.T) {
	for _, c := range credentials() {
		t.Run(c.name, func(t *testing.T) {
			for _, tmpl := range []string{"%s", "prefix %s suffix", "a=%s;b=%s", "(%s)", "x\n%s\ny"} {
				in := strings.ReplaceAll(tmpl, "%s", c.secret)
				out := ScrubString(in)
				if strings.Contains(out, c.gone) {
					t.Fatalf("ScrubString(%q) = %q still contains the secret", tmpl, out)
				}
				if out == in {
					t.Fatalf("ScrubString(%q) changed nothing", tmpl)
				}
				if again := ScrubString(out); again != out {
					t.Fatalf("not idempotent: %q then %q", out, again)
				}
			}
		})
	}
}

func TestScrubString_Renderings(t *testing.T) {
	tok := fakeToken("lat")
	cases := []struct{ name, in, want string }{
		{"token keeps prefix and last4", "key is " + tok + " ok", "key is lat_…vWx" + "" + " ok"},
		{"bearer", "Authorization: Bearer " + "Ab3dE6gH9jK2mN5pQ8sT1vWx", "Authorization: Bearer …1vWx"},
		{"bearer short", "Bearer abc", "Bearer …"},
		{"hex", "k=" + fakeHex("9f3c"), "k=9f3c…9f3c"},
		{"tailnet ip", "peer " + fakeTailnetIP(100) + ":7070 up", "peer <tailnet-ip>:7070 up"},
		{"tailnet name", "host x." + fakeTailnetName() + ".example", "host x.<TAILNET>.example"},
		{"clean text untouched", "model loaded in 1234 ms", "model loaded in 1234 ms"},
	}
	// The expectation for the token case is built from the token itself, not typed.
	cases[0].want = "key is lat_…" + tok[len(tok)-4:] + " ok"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScrubString(c.in); got != c.want {
				t.Fatalf("ScrubString(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestScrubString_NearMissesStay(t *testing.T) {
	for _, in := range []string{
		"100.63.255.255",            // just below 100.64/10
		"100.128.0.1",               // just above it
		"100.64.1",                  // not an address
		"100.64.1." + "999",         // not an address
		"1100.64.1.1",               // inside another number
		"lat_short",                 // body under 20 characters
		"localai_requests_total",    // metric name: 'lai_' body is only 14 characters
		"pinned spinner monkey",     // not keys, plain text
		"tailscale tailnet",         // no 6 hex digits after 'tail'
		strings.Repeat("ab", 31),    // 62 hex digits
		strings.Repeat("ab", 33),    // 66 hex digits: no word boundary at 64
		"bearer lowercase is prose", // the rule is case-sensitive, as specified
		"",
	} {
		if got := ScrubString(in); got != in {
			t.Errorf("ScrubString(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestScrubString_ManyInOneString(t *testing.T) {
	in := fmt.Sprintf("a %s b %s c %s d %s e %s", fakeToken("nod"), fakeTailnetIP(90), fakeTailnetName(), fakeHex("0e1d"), fakeTailnetIP(70))
	out := ScrubString(in)
	for _, c := range []string{hidden(fakeToken("nod")), fakeTailnetIP(90), fakeTailnetName(), hidden(fakeHex("0e1d")), fakeTailnetIP(70)} {
		if strings.Contains(out, c) {
			t.Fatalf("%q survived in %q", c, out)
		}
	}
}

// specKeyRE is the key rule exactly as agent.md section 4.11 writes it.
var specKeyRE = regexp.MustCompile(`(?i)(^|_)(token|key|apikey|secret|pin|password|passwd|credential|authorization|cookie|grant)(_|$)`)

func specSensitive(key string) bool {
	k := strings.ToLower(key)
	k = strings.NewReplacer("-", "_", ".", "_").Replace(k)
	if strings.HasSuffix(k, "_synced") || strings.Contains(k, "fingerprint") || strings.HasSuffix(k, "_sha256") {
		return false
	}
	return specKeyRE.MatchString(k)
}

func TestSensitiveKey_Table(t *testing.T) {
	for key, want := range map[string]bool{
		"token": true, "api_key": true, "apikey": true, "API-KEY": true, "agent-token": true, "agent.token": true,
		"pin": true, "admin_pin": true, "password": true, "passwd": true, "secret": true, "credential": true,
		"Authorization": true, "cookie": true, "grant": true, "llama_key": true, "key": true, "refresh_token_id": true,
		"pinned": false, "spinner": false, "monkey": false, "keyboard": false, "tokens": false, "keys": false,
		"model": false, "principal": false, "code": false, "duration_ms": false, "path": false, "grants": false,
		"key_synced": false, "cloud_synced": false, "api_key_fingerprint": false, "fingerprint": false,
		"token_fingerprint_short": false, "file_sha256": false, "key_sha256": false, "sha256": false,
		"": false,
	} {
		if got := SensitiveKey(key); got != want {
			t.Errorf("SensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// The hand-written matcher must agree with the regular expression of the spec on any key.
func TestSensitiveKey_AgreesWithSpecRegex(t *testing.T) {
	words := []string{"token", "key", "apikey", "secret", "pin", "password", "passwd", "credential", "authorization",
		"cookie", "grant", "api", "agent", "pinned", "keys", "synced", "sha256", "fingerprint", "x", "KEY", "Token", ""}
	seps := []string{"_", "-", ".", "", "__"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(4) + 1; n > 0; n-- {
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteString(seps[rng.Intn(len(seps))])
		}
		key := b.String()
		if got, want := SensitiveKey(key), specSensitive(key); got != want {
			t.Fatalf("SensitiveKey(%q) = %v, spec regex says %v", key, got, want)
		}
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "…",
		"short":               "…",
		"12345678":            "…",
		"123456789":           "…6789",
		fakeToken("lat"):      "lat_…vWx" + "",
		"pg_" + "abcdefghij":  "pg_…ghij",
		"Bearer abcdefghijkl": "…ijkl",
		"lat_…9xQz":           "lat_…9xQz", // a fingerprint stays itself
	} {
		if in == fakeToken("lat") {
			want = "lat_…" + in[len(in)-4:]
		}
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- the handler -------------------------------------------------------------------------

type logErr struct{ msg string }

func (e logErr) Error() string { return e.msg }

type payload struct {
	Name   string `json:"name"`
	Token  string `json:"token"`
	Detail string `json:"detail"`
	N      int    `json:"n"`
}

// capture returns a logger writing through the scrubber into the returned buffer, and a decoder.
func capture(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level, ReplaceAttr: dropTime})
	return slog.New(Scrub(h)), &buf
}

func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", lines[len(lines)-1], err)
	}
	return m
}

func TestScrubHandler_EveryFamilyInMessageAndAttributes(t *testing.T) {
	for _, c := range credentials() {
		t.Run(c.name, func(t *testing.T) {
			log, buf := capture(slog.LevelDebug)
			// message
			log.Info("saw " + c.secret + " in the message")
			// plain attribute under an innocent key
			log.Info("m", "detail", "value "+c.secret)
			// nested group, and a group opened with WithGroup
			log.Info("m", slog.Group("outer", slog.String("a", c.secret), slog.Group("inner", slog.String("b", c.secret))))
			log.WithGroup("g").Info("m", "c", c.secret)
			// attributes bound with With
			log.With("bound", c.secret, slog.Group("bg", "d", c.secret)).Info("m")
			// an error whose text carries the secret, a []byte, a struct and a map
			log.Info("m", "error", logErr{"failed with " + c.secret})
			log.Info("m", "err", errors.New("wrapped: "+c.secret))
			log.Info("m", "raw", []byte(c.secret))
			log.Info("m", "obj", payload{Name: "x", Detail: c.secret})
			log.Info("m", "obj", map[string]string{"anything": c.secret})
			log.Info("m", "list", []string{"ok", c.secret})
			out := buf.String()
			if strings.Contains(out, c.gone) {
				t.Fatalf("secret survived:\n%s", out)
			}
			// every line must still be valid JSON
			for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
				if !json.Valid([]byte(ln)) {
					t.Fatalf("invalid JSON line: %q", ln)
				}
			}
		})
	}
}

func TestScrubHandler_KeyNameRedaction(t *testing.T) {
	log, buf := capture(slog.LevelInfo)
	secret := "correct horse battery staple"
	log.Info("m",
		"api_key", secret, "agent-token", secret, "pin", "123456", "Password", secret, "pinned", "model-a",
		"spinner", "busy", "monkey", "see", "key_synced", true, "api_key_fingerprint", "lat_…9xQz",
		"cloud_synced", "yes", "pin_attempts", 3, "grant", 42, "cookie", secret)
	m := lastLine(t, buf)
	for _, k := range []string{"api_key", "agent-token", "Password", "cookie"} {
		if m[k] != "…"+secret[len(secret)-4:] {
			t.Errorf("%s = %v, want a redacted value", k, m[k])
		}
	}
	if m["pin"] != "…" {
		t.Errorf("pin = %v, want …", m["pin"])
	}
	// non-string values under a secret key are redacted too, as strings
	if m["pin_attempts"] != "…" || m["grant"] != "…" {
		t.Errorf("non-string secrets not redacted: pin_attempts=%v grant=%v", m["pin_attempts"], m["grant"])
	}
	// whole words only, and the exemptions
	if m["pinned"] != "model-a" || m["spinner"] != "busy" || m["monkey"] != "see" {
		t.Errorf("a non-key was redacted: %v", m)
	}
	if m["key_synced"] != true || m["cloud_synced"] != "yes" || m["api_key_fingerprint"] != "lat_…9xQz" {
		t.Errorf("an exempt key was redacted: %v", m)
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), "123456") {
		t.Fatalf("secret in output: %s", buf.String())
	}
}

func TestScrubHandler_KeyRuleInNestedGroupsAndWithAttrs(t *testing.T) {
	log, buf := capture(slog.LevelInfo)
	secret := "a long enough secret value"
	log.With(slog.Group("req", "authorization", "Bearer "+secret, "path", "/key")).
		WithGroup("deep").
		Info("m", slog.Group("hdr", slog.String("cookie", secret), slog.String("host", "127.0.0.1:7070")))
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("secret survived: %s", out)
	}
	if !strings.Contains(out, `"/key"`) || !strings.Contains(out, "127.0.0.1:7070") {
		t.Fatalf("innocent values were damaged: %s", out)
	}
}

func TestScrubHandler_StructuredValuesKeyRedaction(t *testing.T) {
	log, buf := capture(slog.LevelInfo)
	log.Info("m", "obj", payload{Name: "x", Token: "plain-looking-secret", N: 7})
	out := buf.String()
	if strings.Contains(out, "plain-looking-secret") {
		t.Fatalf("a secret field of a struct survived: %s", out)
	}
	if !strings.Contains(out, `"n":7`) || !strings.Contains(out, `"name":"x"`) {
		t.Fatalf("other fields were damaged: %s", out)
	}
	// an untouched structured value keeps its type (no round trip)
	log.Info("m", "obj", payload{Name: "x", N: 7})
	if m := lastLine(t, buf); m["obj"].(map[string]any)["n"] != float64(7) {
		t.Fatalf("clean struct damaged: %v", m)
	}
}

func TestScrubHandler_LogValuerIsResolved(t *testing.T) {
	log, buf := capture(slog.LevelInfo)
	log.Info("m", "who", slog.AnyValue(valuer{fakeToken("nod")}))
	if strings.Contains(buf.String(), hidden(fakeToken("nod"))) {
		t.Fatalf("LogValuer output not scrubbed: %s", buf.String())
	}
}

type valuer struct{ s string }

func (v valuer) LogValue() slog.Value { return slog.StringValue("resolved " + v.s) }

// ---- the sha256 exemption ----------------------------------------------------------------

func TestScrubHandler_Sha256Exemption(t *testing.T) {
	digest := fakeHex("5a1f")
	liveKey := fakeHex("c0de")
	t.Cleanup(func() { SetCurrentKey("") })

	// without a live key: a digest under *sha256 is kept, the same hex under another key is not
	SetCurrentKey("")
	log, buf := capture(slog.LevelInfo)
	log.Info("m", "file_sha256", digest, "sha256", digest, "unit_sha256", digest, "other", digest, "model_digest", digest)
	m := lastLine(t, buf)
	for _, k := range []string{"file_sha256", "sha256", "unit_sha256"} {
		if m[k] != digest {
			t.Errorf("%s was scrubbed (%v), a sha256 digest must be exempt", k, m[k])
		}
	}
	for _, k := range []string{"other", "model_digest"} {
		if m[k] == digest || strings.Contains(fmt.Sprint(m[k]), hidden(digest)) {
			t.Errorf("%s was not scrubbed", k)
		}
	}

	// with a live key: that exact value is not exempt, a digest still is
	SetCurrentKey(strings.ToUpper(liveKey)) // case does not matter
	buf.Reset()
	log.Info("m", "key_sha256", liveKey, "file_sha256", digest, "label_sha256", "sha256:"+liveKey, "nested", slog.Group("g", "x_sha256", liveKey))
	out := buf.String()
	if strings.Contains(out, hidden(liveKey)) {
		t.Fatalf("the live key survived under a sha256 key: %s", out)
	}
	if m := lastLine(t, buf); m["file_sha256"] != digest {
		t.Fatalf("an unrelated digest was scrubbed while a key is set: %v", m["file_sha256"])
	}

	// the exemption also applies inside structured values
	buf.Reset()
	log.Info("m", "obj", map[string]string{"file_sha256": digest, "key_sha256": liveKey})
	if strings.Contains(buf.String(), hidden(liveKey)) {
		t.Fatalf("live key survived inside a map: %s", buf.String())
	}
	if !strings.Contains(buf.String(), digest) {
		t.Fatalf("digest inside a map was scrubbed: %s", buf.String())
	}
}

func TestSetCurrentKey_IsNeverLogged(t *testing.T) {
	t.Cleanup(func() { SetCurrentKey("") })
	SetCurrentKey(fakeHex("c0de"))
	log, buf := capture(slog.LevelInfo)
	log.Info("rotated")
	if strings.Contains(buf.String(), "c0de") {
		t.Fatal("the key leaked into the log")
	}
}

// ---- properties --------------------------------------------------------------------------

func FuzzScrubString(f *testing.F) {
	for _, c := range credentials() {
		f.Add("x " + c.secret + " y")
	}
	f.Add("Bearer Bearer Bearer abcdefghij")
	f.Add("lat_" + "…")
	f.Add(fakeTailnetIP(64) + " " + fakeTailnetIP(127) + " " + fakeTailnetIP(128))
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		out := ScrubString(s)
		if again := ScrubString(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, out, again)
		}
		// a planted token is always scrubbed, whatever surrounds it
		tok := fakeToken("pat")
		if strings.Contains(s, hidden(tok)) {
			return // the fuzzer typed part of the token itself; the check below would be meaningless
		}
		if got := ScrubString(s + " " + tok + " " + s); strings.Contains(got, hidden(tok)) {
			t.Fatalf("planted token survived around %q: %q", s, got)
		}
	})
}

func TestScrubString_RandomTextNeverLeaksPlantedSecrets(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []rune("abcdef0123456789 _-:=.,;\"'()[]{}<>/\n\tBearer lat nod pat tail 100.64")
	creds := credentials()
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := rng.Intn(30); n > 0; n-- {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		c := creds[rng.Intn(len(creds))]
		// a space on both sides keeps the planted value a separate word, as in real logs
		in := b.String() + " " + c.secret + " " + b.String()
		if out := ScrubString(in); strings.Contains(out, c.gone) {
			t.Fatalf("%s survived in %q -> %q", c.name, in, out)
		}
	}
}

// ---- cost --------------------------------------------------------------------------------

func benchMessage(withSecret bool) string {
	m := "request handled for model qwen3-30b-a3b on unit localai-llama.service in 183 ms, status 200, route /models/load ok"
	if withSecret {
		m = "request from " + fakeTailnetIP(80) + " carried token " + fakeToken("lat") + " for model qwen3-30b-a3b on unit llama ok"
	}
	for len(m) < 200 {
		m += " ok"
	}
	return m[:200]
}

func BenchmarkScrubString_200BytesClean(b *testing.B) {
	m := benchMessage(false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ScrubString(m)
	}
}

func BenchmarkScrubString_200BytesWithSecrets(b *testing.B) {
	m := benchMessage(true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ScrubString(m)
	}
}

// TestScrubString_CostBudget keeps the "under 5 microseconds for a 200-byte message" note of the
// issue honest for the common (clean) case. The bound is deliberately loose for slow CI machines
// and the race detector; the benchmarks above give the real numbers.
func TestScrubString_CostBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	m := benchMessage(false)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ScrubString(m)
		}
	})
	if per := res.NsPerOp(); per > 50_000 {
		t.Fatalf("a clean 200-byte message took %d ns to scrub (budget 5 µs, test bound 50 µs)", per)
	}
}
