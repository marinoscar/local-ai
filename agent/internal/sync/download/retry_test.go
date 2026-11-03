package download

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	fixed := func(v float64) func() float64 { return func() float64 { return v } }
	steps := DefaultBackoff
	for _, tc := range []struct {
		name  string
		b     Backoff
		retry int
		want  time.Duration
	}{
		{"first retry, no jitter offset", Backoff{Rand: fixed(0.5)}, 0, 5 * time.Second},
		{"second", Backoff{Rand: fixed(0.5)}, 1, 15 * time.Second},
		{"third", Backoff{Rand: fixed(0.5)}, 2, 45 * time.Second},
		{"fourth", Backoff{Rand: fixed(0.5)}, 3, 2 * time.Minute},
		{"fifth", Backoff{Rand: fixed(0.5)}, 4, 5 * time.Minute},
		{"sixth", Backoff{Rand: fixed(0.5)}, 5, 10 * time.Minute},
		{"capped at the last step", Backoff{Rand: fixed(0.5)}, 99, 10 * time.Minute},
		{"negative index is the first", Backoff{Rand: fixed(0.5)}, -3, 5 * time.Second},
		{"jitter floor is -20 %", Backoff{Rand: fixed(0)}, 0, 4 * time.Second},
		{"jitter ceiling is +20 %", Backoff{Rand: fixed(1)}, 0, 6 * time.Second},
		{"custom jitter", Backoff{Jitter: 0.5, Rand: fixed(1)}, 0, 7500 * time.Millisecond},
		{"negative jitter means none", Backoff{Jitter: -1, Rand: fixed(0)}, 0, 5 * time.Second},
		{"custom steps", Backoff{Steps: []time.Duration{time.Second}, Rand: fixed(0.5)}, 3, time.Second},
	} {
		if got := tc.b.Delay(tc.retry); got != tc.want {
			t.Errorf("%s: Delay(%d) = %v, want %v", tc.name, tc.retry, got, tc.want)
		}
	}
	if (Backoff{}).Retries() != 6 || len(steps) != 6 {
		t.Error("the schedule has six retries (seven attempts)")
	}
	// The real random source stays within +/- 20 %.
	for i := 0; i < 2000; i++ {
		d := Backoff{}.Delay(0)
		if d < 4*time.Second || d > 6*time.Second {
			t.Fatalf("jittered delay %v outside 4s..6s", d)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := epoch
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7", 7 * time.Second, true},
		{" 120 ", 2 * time.Minute, true},
		{"0", 0, true},
		{epoch.Add(45 * time.Second).Format(http.TimeFormat), 45 * time.Second, true},
		{epoch.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"", 0, false},
		{"soon", 0, false},
		{"-5", 0, false},
		{"99999999999", 0, false},
		{"1.5", 0, false},
	} {
		got, ok := ParseRetryAfter(tc.in, now)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestErrorSentinelsAndText(t *testing.T) {
	e := newError(KindMismatch, CodeSize, "downloaded %d bytes", 5)
	var wrapped error = fmt.Errorf("fetch: %w", e)
	if !errors.Is(wrapped, ErrMismatch) || errors.Is(wrapped, ErrDisk) || errors.Is(wrapped, ErrPermanent) {
		t.Error("sentinels must match by kind only")
	}
	if !strings.HasPrefix(e.Error(), CodeSize+": ") || !e.Permanent {
		t.Errorf("Error() = %q, Permanent %v", e.Error(), e.Permanent)
	}
	if (&Error{Kind: KindDisk, Code: CodeDisk}).Error() == "" || errors.Unwrap(e) == nil {
		t.Error("Error text or Unwrap missing")
	}
	if newError(KindDisk, CodeDisk, "x").Permanent || newError(KindExhausted, CodeRetries, "x").Permanent {
		t.Error("a disk skip and exhausted retries are resumable, not permanent")
	}
}

// userinfo is built at run time: no credential-shaped URL is ever written in the repo.
var userinfo = "person" + ":" + "value"

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://files.example.test/a/b.bin":                     "https://files.example.test/a/b.bin",
		"https://files.example.test/a?sig=SECRET#frag":           "https://files.example.test/a",
		"https://" + userinfo + "@files.example.test:8443/a?x=1": "https://files.example.test:8443/a",
		"local:ark-0a1b2c3d/item":                                "(invalid url)",
		"::not a url::":                                          "(invalid url)",
		"":                                                       "(invalid url)",
		"https://files.example.test/a%20b?token=SECRET&y=%zz": "https://files.example.test/a b",
	} {
		got := RedactURL(in)
		if strings.Contains(got, "SECRET") || strings.Contains(got, "?") {
			t.Errorf("RedactURL(%q) = %q leaks", in, got)
		}
		if in != "https://files.example.test/a%20b?token=SECRET&y=%zz" && got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
	if labelURL("local:ark-0a1b2c3d/item") != "local:ark-0a1b2c3d/item" {
		t.Error("a non-http label is kept as is")
	}
}

func TestShortHash(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef": "0123…ef",
		"abc":              "abc",
		"":                 "",
	} {
		if got := ShortHash(in); got != want {
			t.Errorf("ShortHash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	for _, tc := range []struct {
		in         string
		start, end int64
		total      int64
		ok         bool
	}{
		{"bytes 100-199/1000", 100, 199, 1000, true},
		{"bytes 0-0/1", 0, 0, 1, true},
		{"bytes 5-9/*", 5, 9, -1, true},
		{"bytes */1000", 0, 0, 1000, true},
		{"bytes */*", 0, 0, -1, true},
		{"", 0, 0, -1, false},
		{"items 1-2/3", 0, 0, -1, false},
		{"bytes 1-2", 0, 0, -1, false},
		{"bytes 9-5/10", 0, 0, -1, false},
		{"bytes a-b/c", 0, 0, -1, false},
		{"bytes 1-2/-3", 0, 0, -1, false},
		{"bytes -5/10", 0, 0, -1, false},
	} {
		s, e, tot, ok := parseContentRange(tc.in)
		if ok != tc.ok || (ok && (s != tc.start || e != tc.end || tot != tc.total)) {
			t.Errorf("parseContentRange(%q) = %d %d %d %v", tc.in, s, e, tot, ok)
		}
	}
}

func TestScrubQuery(t *testing.T) {
	for in, want := range map[string]string{
		`Get "https://h.example.test/a?sig=SECRET": EOF`: `Get "https://h.example.test/a?(redacted)": EOF`,
		"no query here": "no query here",
		"a?b c?d":       "a?(redacted) c?(redacted)",
	} {
		if got := scrubQuery(in); got != want {
			t.Errorf("scrubQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
