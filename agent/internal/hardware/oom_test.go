package hardware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

func journalKey(since time.Time) string {
	return execfake.Key("/usr/bin/journalctl", "-k", "-n", "200", "-o", "json", "--since="+since.UTC().Format("2006-01-02 15:04:05")+" UTC")
}

func TestParseJournalOOMKeepsOnlyLlamaServerKills(t *testing.T) {
	events := parseJournalOOM(testdata(t, "journal/oom.jsonl"))
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly the llama-server kill (other process, binary message and truncated line skipped)", events)
	}
	ev := events[0]
	if ev.PID != 4242 || ev.Process != "llama-server" || ev.Source != OOMSourceJournal {
		t.Errorf("event = %+v", ev)
	}
	if want := time.UnixMicro(1793631240123456).UTC(); !ev.At.Equal(want) {
		t.Errorf("at = %v, want %v", ev.At, want)
	}
}

func TestParseJournalOOMAcceptsTheCgroupSpelling(t *testing.T) {
	got := parseJournalOOM([]byte(`{"__REALTIME_TIMESTAMP":"5","MESSAGE":"Memory cgroup out of memory: Killed process 77 (llama-server) total-vm:1kB"}` + "\n"))
	if len(got) != 1 || got[0].PID != 77 {
		t.Errorf("events = %+v", got)
	}
}

func TestParseJournalOOMGarbage(t *testing.T) {
	for _, in := range []string{"", "\n\n", "not json at all", `{"MESSAGE": 5}`, `{"MESSAGE": null}`, `{}`,
		`{"MESSAGE":"Out of memory: Killed process 99999999999999999999 (llama-server)"}`} {
		if got := parseJournalOOM([]byte(in)); len(got) != 0 {
			t.Errorf("parseJournalOOM(%q) = %+v", in, got)
		}
	}
	// A record without a timestamp still counts; the detector stamps it.
	if got := parseJournalOOM([]byte(`{"MESSAGE":"Out of memory: Killed process 5 (llama-server)"}`)); len(got) != 1 || !got[0].At.IsZero() {
		t.Errorf("no timestamp: %+v", got)
	}
}

func TestOOMDetectorWindowsAndDedupes(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	r := execfake.New().
		Expect(journalKey(now.Add(-time.Minute)), ok(string(testdata(t, "journal/oom.jsonl"))), nil).
		// The next window starts at the previous check; the journal repeats the first kill.
		Expect(journalKey(now), ok(string(testdata(t, "journal/oom-overlap.jsonl"))), nil)
	d := newOOMDetector(testEnv(nil, r))

	ev1, err := d.check(context.Background(), now)
	if err != nil || len(ev1) != 1 || ev1[0].PID != 4242 {
		t.Fatalf("first check = %+v, %v (the first window is the last minute)", ev1, err)
	}
	ev2, err := d.check(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ev2) != 0 && ev2[0].PID == 4242 {
		t.Errorf("the overlapping kill was reported twice: %+v", ev2)
	}
	if len(ev2) != 1 || ev2[0].PID != 4300 {
		t.Errorf("second check = %+v, want only the new kill", ev2)
	}
	last, ok := d.lastEvent()
	if !ok || last.PID != 4300 {
		t.Errorf("last event = %+v", last)
	}
}

func TestOOMDetectorFailureKeepsTheWindowOpen(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	key := journalKey(now.Add(-time.Minute))
	cases := []struct {
		name string
		run  *execfake.Runner
		look func(string) (string, error)
		is   error
	}{
		{"journalctl missing", execfake.New(), only(), ErrToolMissing},
		{"not permitted", execfake.New().Expect(key, exit(1, "Hint: You are currently not seeing messages from other users"), nil), nil, nil},
		{"timeout", execfake.New().Expect(key, execx.Result{}, execx.ErrTimeout), nil, execx.ErrTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(nil, c.run)
			if c.look != nil {
				e.look = c.look
			}
			d := newOOMDetector(e)
			if _, err := d.check(context.Background(), now); err == nil || (c.is != nil && !errors.Is(err, c.is)) {
				t.Fatalf("err = %v", err)
			}
			if !d.since.IsZero() {
				t.Error("a failed check moved the window forward and would lose kills")
			}
		})
	}
}

func TestOOMDetectorArgvIsFixed(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	r := execfake.New().Expect(journalKey(now.Add(-time.Minute)), ok(""), nil)
	if _, err := newOOMDetector(testEnv(nil, r)).check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	c := r.Calls()[0]
	if c.Path != "/usr/bin/journalctl" || len(c.Args) != 6 || c.Args[0] != "-k" || c.Args[1] != "-n" || c.Args[2] != "200" {
		t.Errorf("call = %+v", c)
	}
}

func TestObserveServiceResult(t *testing.T) {
	d := newOOMDetector(testEnv(nil, nil))
	for _, res := range []string{"success", "exit-code", "timeout", "", "signal"} {
		if _, ok := d.observeService(res, t0); ok {
			t.Errorf("result %q must not count as an OOM kill", res)
		}
	}
	ev, ok := d.observeService("oom-kill", t0)
	if !ok || ev.Source != OOMSourceService || ev.Process != llamaProcess {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
	if last, ok := d.lastEvent(); !ok || !last.At.Equal(t0) {
		t.Errorf("last = %+v", last)
	}
	// An older event never replaces a newer one.
	d.observeService("oom-kill", t0.Add(-time.Hour))
	if last, _ := d.lastEvent(); !last.At.Equal(t0) {
		t.Errorf("last = %+v", last)
	}
}
