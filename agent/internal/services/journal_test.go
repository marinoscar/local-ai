package services_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

const journalctl = "/usr/bin/journalctl"

// testToken is a credential-shaped string built at run time, so no fixture holds a literal token.
func testToken() string { return "lat_" + strings.Repeat("Qx7", 10) }

func journalFixture(t *testing.T, name string) []byte {
	t.Helper()
	return []byte(strings.ReplaceAll(string(fixture(t, name)), "@@TOKEN@@", testToken()))
}

func journalRunner(out []byte, args ...string) *execfake.Runner {
	return execfake.New().Expect(execfake.Key(journalctl, args...), execx.Result{Stdout: out}, nil)
}

func TestJournalArgvGolden(t *testing.T) {
	since := time.Date(2026, 11, 2, 8, 0, 0, 999_000_000, time.UTC)
	tests := []struct {
		name  string
		lines int
		since time.Time
		want  []string
	}{
		{"no since", 5, time.Time{}, []string{"-u", services.UnitLlama, "-n", "5", "-o", "json", "--no-pager"}},
		{"since is its own element", 200, since, []string{"-u", services.UnitLlama, "-n", "200", "--since", "2026-11-02 08:00:00 UTC", "-o", "json", "--no-pager"}},
		{"since in another zone is sent as UTC", 1, since.In(time.FixedZone("x", -6*3600)), []string{"-u", services.UnitLlama, "-n", "1", "--since", "2026-11-02 08:00:00 UTC", "-o", "json", "--no-pager"}},
		{"max lines", 2000, time.Time{}, []string{"-u", services.UnitLlama, "-n", "2000", "-o", "json", "--no-pager"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := journalRunner(nil, tc.want...)
			if _, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitLlama, tc.lines, tc.since); err != nil {
				t.Fatal(err)
			}
			c := r.Calls()[0]
			if c.Path != journalctl || !reflect.DeepEqual(c.Args, tc.want) {
				t.Errorf("argv = %s %q\nwant   %s %q", c.Path, c.Args, journalctl, tc.want)
			}
			if c.Timeout != 5*time.Second || c.MaxOut != 4<<20 {
				t.Errorf("timeout %v, cap %d: want 5s and 4 MiB", c.Timeout, c.MaxOut)
			}
		})
	}
}

func TestJournalTailParsesAndScrubs(t *testing.T) {
	out := journalFixture(t, "journal-llama.jsonl")
	r := journalRunner(out, "-u", services.UnitLlama, "-n", "50", "-o", "json", "--no-pager")
	tail, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitLlama, 50, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tail.Unit != services.UnitLlama || tail.Truncated {
		t.Fatalf("tail = %+v", tail)
	}
	// One entry per fixture line: a credential in a message must not cost the line.
	if len(tail.Lines) != 8 {
		t.Fatalf("%d lines, want 8: %+v", len(tail.Lines), tail.Lines)
	}
	l := tail.Lines
	if !l[0].At.Equal(time.Date(2026, 11, 2, 8, 0, 5, 250_000_000, time.UTC)) || l[0].Priority != 6 || l[0].Message != "llama_server: starting on 127.0.0.1:8080" {
		t.Errorf("line 0 = %+v", l[0])
	}
	if l[2].Priority != 4 || l[7].Priority != 6 {
		t.Errorf("priorities = %d, %d", l[2].Priority, l[7].Priority)
	}
	// A byte-array MESSAGE is decoded to text.
	if want := "ready ✓\x1b[0m"; l[3].Message != want {
		t.Errorf("byte-array MESSAGE = %q, want %q", l[3].Message, want)
	}
	// Both credentials are gone, and the surrounding text survives.
	for i, line := range l {
		if strings.Contains(line.Message, testToken()) || strings.Contains(line.Message, "Qx7Qx7Qx7") {
			t.Errorf("line %d leaks the token: %q", i, line.Message)
		}
	}
	if !strings.HasPrefix(l[4].Message, "request headers: ") || !strings.Contains(l[4].Message, "Bearer ") {
		t.Errorf("line 4 = %q", l[4].Message)
	}
	if want := "key file /etc/localai/api-key unreadable: lat_…Qx7 rejected"; !strings.HasPrefix(l[5].Message, "key file /etc/localai/api-key unreadable: lat_…") || !strings.HasSuffix(l[5].Message, " rejected") {
		t.Errorf("line 5 = %q, want shape %q", l[5].Message, want)
	}
	// No PRIORITY: informational. A null MESSAGE (over 4 KiB): empty.
	if l[6].Priority != 6 || l[6].Message != "" {
		t.Errorf("line 6 = %+v", l[6])
	}
	// A field that occurs twice is joined.
	if l[7].Message != "first value\nsecond value" {
		t.Errorf("line 7 = %q", l[7].Message)
	}
}

// The scrubber must run on decoded fields: on the raw line, a Bearer match runs to the next whitespace
// and swallows the rest of the line (the closing quote of the string and every field after it). The
// decoded-field path keeps the entry's own priority, time and message.
func TestJournalScrubbingTheRawLineWouldDamageIt(t *testing.T) {
	raw := strings.Split(string(journalFixture(t, "journal-llama.jsonl")), "\n")[4]
	if !strings.Contains(raw, `"_PID":"2214"`) {
		t.Fatal("the fixture line lacks its trailing fields")
	}
	if damaged := logx.ScrubString(raw); strings.Contains(damaged, `"_PID":"2214"`) {
		t.Log("the scrubber no longer swallows the rest of a raw line; the decoded-field rule is still the contract")
	}
	got := services.ParseJournal([]byte(raw))
	if len(got) != 1 || got[0].Priority != 7 || !got[0].At.Equal(time.Date(2026, 11, 2, 8, 0, 15, 0, time.UTC)) {
		t.Fatalf("the decoded-field path lost the entry: %+v", got)
	}
	if strings.Contains(got[0].Message, testToken()) {
		t.Errorf("token leaked: %q", got[0].Message)
	}
}

func TestParseJournalSkipsWhatIsNotAnEntry(t *testing.T) {
	got := services.ParseJournal(fixture(t, "journal-junk.jsonl"))
	if len(got) != 1 || got[0].Message != "after the junk" || got[0].Priority != 5 {
		t.Fatalf("lines = %+v", got)
	}
	empty := services.ParseJournal(fixture(t, "journal-noentries.jsonl"))
	if empty == nil || len(empty) != 0 {
		t.Fatalf("no entries must give an empty, non-nil slice, got %#v", empty)
	}
	if got := services.ParseJournal(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil input: %#v", got)
	}
}

func TestParseJournalFieldForms(t *testing.T) {
	tests := []struct {
		name string
		line string
		msg  string
		prio int
		ok   bool
	}{
		{"numeric timestamp and priority", `{"__REALTIME_TIMESTAMP":1793606405000000,"PRIORITY":3,"MESSAGE":"x"}`, "x", 3, true},
		{"priority out of range", `{"__REALTIME_TIMESTAMP":"1793606405000000","PRIORITY":"9","MESSAGE":"x"}`, "x", 6, true},
		{"invalid UTF-8 in a string", "{\"__REALTIME_TIMESTAMP\":\"1793606405000000\",\"PRIORITY\":\"6\",\"MESSAGE\":\"a\xffb\"}", "a�b", 6, true},
		{"byte array with an invalid sequence", `{"__REALTIME_TIMESTAMP":"1793606405000000","MESSAGE":[97,255,98]}`, "a�b", 6, true},
		{"byte array with an out-of-range value", `{"__REALTIME_TIMESTAMP":"1793606405000000","MESSAGE":[97,300,98]}`, "a?b", 6, true},
		{"empty array", `{"__REALTIME_TIMESTAMP":"1793606405000000","MESSAGE":[]}`, "", 6, true},
		{"array of byte arrays", `{"__REALTIME_TIMESTAMP":"1793606405000000","MESSAGE":[[97],[98]]}`, "a\nb", 6, true},
		{"unknown MESSAGE type", `{"__REALTIME_TIMESTAMP":"1793606405000000","MESSAGE":{"a":1}}`, "", 6, true},
		{"zero timestamp", `{"__REALTIME_TIMESTAMP":"0","MESSAGE":"x"}`, "", 0, false},
		{"non-numeric timestamp", `{"__REALTIME_TIMESTAMP":"soon","MESSAGE":"x"}`, "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := services.ParseJournal([]byte(tc.line + "\n"))
			if !tc.ok {
				if len(got) != 0 {
					t.Fatalf("accepted: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Message != tc.msg || got[0].Priority != tc.prio {
				t.Fatalf("got %+v, want message %q priority %d", got, tc.msg, tc.prio)
			}
		})
	}
}

func TestJournalTailTruncation(t *testing.T) {
	line := func(msg string) string {
		return `{"__REALTIME_TIMESTAMP":"1793606405000000","PRIORITY":"6","MESSAGE":"` + msg + `"}` + "\n"
	}
	t.Run("a partial last line is dropped and Truncated is set", func(t *testing.T) {
		out := line("one") + line("two") + `{"__REALTIME_TIMESTAMP":"1793606405000000","PRIORITY":"6","MESSAGE":"thr`
		r := execfake.New().Expect(execfake.Key(journalctl, "-u", services.UnitKiwix, "-n", "10", "-o", "json", "--no-pager"),
			execx.Result{Stdout: []byte(out), Truncated: true}, nil)
		tail, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitKiwix, 10, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if !tail.Truncated || len(tail.Lines) != 2 || tail.Lines[1].Message != "two" {
			t.Fatalf("tail = %+v", tail)
		}
	})
	t.Run("a cut exactly on a newline still sets Truncated", func(t *testing.T) {
		r := execfake.New().Expect(execfake.Key(journalctl, "-u", services.UnitKiwix, "-n", "10", "-o", "json", "--no-pager"),
			execx.Result{Stdout: []byte(line("one")), Truncated: true}, nil)
		tail, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitKiwix, 10, time.Time{})
		if err != nil || !tail.Truncated || len(tail.Lines) != 1 {
			t.Fatalf("tail = %+v, %v", tail, err)
		}
	})
	t.Run("a cut inside the first line leaves nothing", func(t *testing.T) {
		r := execfake.New().Expect(execfake.Key(journalctl, "-u", services.UnitKiwix, "-n", "10", "-o", "json", "--no-pager"),
			execx.Result{Stdout: []byte(`{"__REALTIME`), Truncated: true}, nil)
		tail, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitKiwix, 10, time.Time{})
		if err != nil || !tail.Truncated || len(tail.Lines) != 0 || tail.Lines == nil {
			t.Fatalf("tail = %+v, %v", tail, err)
		}
	})
}

func TestJournalTailRefusals(t *testing.T) {
	r := execfake.New()
	j := services.NewJournal(r, journalctl)
	ctx := context.Background()
	for _, n := range []int{0, -1, 2001, 100000} {
		if _, err := j.Tail(ctx, services.UnitLlama, n, time.Time{}); !errors.Is(err, services.ErrTailRange) {
			t.Errorf("lines=%d: err = %v", n, err)
		}
	}
	for _, unit := range []string{"sshd.service", "", "localai-llama", "localai-llama.service; rm -rf /", "-f"} {
		_, err := j.Tail(ctx, unit, 10, time.Time{})
		var v *services.ViolationError
		if !errors.As(err, &v) {
			t.Errorf("unit %q: err = %v", unit, err)
		}
	}
	for _, short := range []string{"agent", "llama", "kiwix", "whisper", "sync", "drill"} {
		u, ok := services.LogUnit(short)
		if !ok || !strings.HasPrefix(u, "localai-") || !strings.HasSuffix(u, ".service") {
			t.Errorf("LogUnit(%q) = %q, %v", short, u, ok)
		}
	}
	if _, ok := services.LogUnit("sshd"); ok {
		t.Error("LogUnit accepted sshd")
	}
	if n := len(r.Calls()); n != 0 {
		t.Errorf("journalctl ran %d times for refused requests", n)
	}
}

func TestJournalTailErrors(t *testing.T) {
	key := execfake.Key(journalctl, "-u", services.UnitLlama, "-n", "5", "-o", "json", "--no-pager")
	t.Run("non-zero exit", func(t *testing.T) {
		r := execfake.New().Expect(key, execx.Result{ExitCode: 1, Stderr: []byte("Failed to open journal: Permission denied. token " + testToken())}, nil)
		_, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitLlama, 5, time.Time{})
		var je *services.JournalError
		if !errors.As(err, &je) || je.ExitCode != 1 || strings.Contains(je.Stderr, testToken()) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		r := execfake.New().Expect(key, execx.Result{}, execx.ErrTimeout)
		if _, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitLlama, 5, time.Time{}); !errors.Is(err, services.ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := execfake.New().Expect(key, execx.Result{}, errors.New("killed"))
		if _, err := services.NewJournal(r, journalctl).Tail(ctx, services.UnitLlama, 5, time.Time{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cannot start", func(t *testing.T) {
		boom := errors.New("exec: no such file")
		r := execfake.New().Expect(key, execx.Result{}, boom)
		if _, err := services.NewJournal(r, journalctl).Tail(context.Background(), services.UnitLlama, 5, time.Time{}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}
