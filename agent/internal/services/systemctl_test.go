package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

const sysctl = "/usr/bin/systemctl"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func newSystemctl(r *execfake.Runner, opts ...services.Option) *services.Systemctl {
	return services.NewSystemctl(r, append([]services.Option{services.WithSystemctlPath(sysctl)}, opts...)...)
}

func TestSystemctlArgvGolden(t *testing.T) {
	const unit = "localai-kiwix.service"
	pre := []string{"--no-pager", "--no-ask-password"}
	tests := []struct {
		name    string
		call    func(m *services.Systemctl) error
		args    []string
		timeout time.Duration
	}{
		{"start", func(m *services.Systemctl) error { return m.Start(context.Background(), unit) }, append(pre, "start", unit), services.JobTimeout},
		{"stop", func(m *services.Systemctl) error { return m.Stop(context.Background(), unit) }, append(pre, "stop", unit), services.JobTimeout},
		{"restart", func(m *services.Systemctl) error { return m.Restart(context.Background(), unit) }, append(pre, "restart", unit), services.JobTimeout},
		{"enable", func(m *services.Systemctl) error { return m.Enable(context.Background(), unit) }, append(pre, "enable", unit), services.CallTimeout},
		{"disable", func(m *services.Systemctl) error { return m.Disable(context.Background(), unit) }, append(pre, "disable", unit), services.CallTimeout},
		{"mask", func(m *services.Systemctl) error { return m.Mask(context.Background(), unit) }, append(pre, "mask", unit), services.CallTimeout},
		{"unmask", func(m *services.Systemctl) error { return m.Unmask(context.Background(), unit) }, append(pre, "unmask", unit), services.CallTimeout},
		{"daemon-reload", func(m *services.Systemctl) error { return m.DaemonReload(context.Background()) }, append(pre, "daemon-reload"), services.CallTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := execfake.New().Expect(execfake.Key(sysctl, tc.args...), execx.Result{}, nil)
			if err := tc.call(newSystemctl(r)); err != nil {
				t.Fatal(err)
			}
			calls := r.Calls()
			if len(calls) != 1 {
				t.Fatalf("%d commands run, want 1", len(calls))
			}
			c := calls[0]
			if c.Path != sysctl || !reflect.DeepEqual(c.Args, tc.args) {
				t.Errorf("argv = %s %q, want %s %q", c.Path, c.Args, sysctl, tc.args)
			}
			if c.Timeout != tc.timeout || c.MaxOut <= 0 {
				t.Errorf("timeout %v max %d, want %v and a cap", c.Timeout, c.MaxOut, tc.timeout)
			}
			if c.Env != nil {
				t.Errorf("verbs run with the minimal environment, got %v", c.Env)
			}
		})
	}
}

func TestSystemctlStateGolden(t *testing.T) {
	const unit = "localai-llama.service"
	want := []string{"show", unit, "-p", "ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState"}
	r := execfake.New().Expect(execfake.Key(sysctl, want...), execx.Result{Stdout: fixture(t, "systemctl-show-active.txt")}, nil)
	st, err := newSystemctl(r).State(context.Background(), unit)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Calls()[0]
	if !reflect.DeepEqual(c.Args, want) {
		t.Errorf("argv = %q, want %q", c.Args, want)
	}
	if c.Timeout != services.CallTimeout {
		t.Errorf("timeout = %v", c.Timeout)
	}
	// The time zone and the locale are pinned, so the timestamps parse the same everywhere.
	env := strings.Join(c.Env, " ")
	if !strings.Contains(env, "TZ=UTC") || !strings.Contains(env, "LC_ALL=C") || !strings.Contains(env, "PATH="+execx.FixedPath) {
		t.Errorf("env = %v", c.Env)
	}
	if st.Unit != unit || !st.Active() || st.MainPID != 2214 {
		t.Errorf("state = %+v", st)
	}
}

func TestParseShowFixtures(t *testing.T) {
	tests := []struct {
		file string
		want services.UnitState
	}{
		{"systemctl-show-active.txt", services.UnitState{
			ActiveState: "active", SubState: "running", MainPID: 2214, NRestarts: 2, ExecMainStatus: 0,
			ExecMainExitTimestamp: utc("2026-11-02T07:59:58Z"), ActiveEnterTimestamp: utc("2026-11-02T08:00:05Z"), UnitFileState: "enabled"}},
		{"systemctl-show-failed.txt", services.UnitState{
			ActiveState: "failed", SubState: "failed", NRestarts: 5, ExecMainStatus: 137,
			ExecMainExitTimestamp: utc("2026-11-02T08:10:41Z"), ActiveEnterTimestamp: utc("2026-11-02T08:00:05Z"), UnitFileState: "enabled"}},
		{"systemctl-show-notfound.txt", services.UnitState{ActiveState: "inactive", SubState: "dead"}},
		{"systemctl-show-timer.txt", services.UnitState{
			ActiveState: "active", SubState: "waiting", ActiveEnterTimestamp: utc("2026-11-02T03:00:00Z"), UnitFileState: "enabled"}},
		{"systemctl-show-old-systemd.txt", services.UnitState{
			ActiveState: "active", SubState: "running", MainPID: 311, NRestarts: 0,
			ActiveEnterTimestamp: utc("2026-11-01T22:10:00.123456Z"), UnitFileState: "enabled-runtime"}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			got, err := services.ParseShow("u.service", fixture(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			tc.want.Unit = "u.service"
			if !got.Same(tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseShowRejectsBadOutput(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"no ActiveState", "SubState=running\nMainPID=1\n"},
		{"empty ActiveState", "ActiveState=\nSubState=dead\n"},
		{"bad number", "ActiveState=active\nMainPID=abc\n"},
		{"bad timestamp", "ActiveState=active\nActiveEnterTimestamp=yesterday\n"},
		{"not systemctl output", "Failed to connect to bus: No such file or directory\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := services.ParseShow("u.service", []byte(tc.in)); err == nil {
				t.Errorf("ParseShow(%q) succeeded", tc.in)
			}
		})
	}
}

func TestUnitStateHelpers(t *testing.T) {
	enter := utc("2026-11-02T08:00:05Z")
	tests := []struct {
		active    string
		wantSince bool
	}{{"active", true}, {"activating", true}, {"reloading", true}, {"inactive", false}, {"failed", false}, {"deactivating", false}}
	for _, tc := range tests {
		st := services.UnitState{ActiveState: tc.active, ActiveEnterTimestamp: enter}
		if got := !st.Since().IsZero(); got != tc.wantSince {
			t.Errorf("Since() of %s set = %v, want %v", tc.active, got, tc.wantSince)
		}
	}
	if !(services.UnitState{UnitFileState: "enabled-runtime"}).Enabled() || (services.UnitState{UnitFileState: "masked"}).Enabled() {
		t.Error("Enabled()")
	}
}

func TestSystemctlErrors(t *testing.T) {
	const unit = services.UnitLlama
	startKey := execfake.Key(sysctl, "--no-pager", "--no-ask-password", "start", unit)
	t.Run("non-zero exit is a CommandError with scrubbed stderr", func(t *testing.T) {
		stderr := "Job for localai-llama.service failed. Authorization: Bearer lat_" + strings.Repeat("A", 30) + "\n"
		r := execfake.New().Expect(startKey, execx.Result{ExitCode: 1, Stderr: []byte(stderr)}, nil)
		err := newSystemctl(r).Start(context.Background(), unit)
		var ce *services.CommandError
		if !errors.As(err, &ce) || ce.ExitCode != 1 || ce.Verb != services.VerbStart || ce.Unit != unit {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(ce.Stderr, strings.Repeat("A", 30)) || !strings.Contains(ce.Stderr, "failed") {
			t.Errorf("stderr not scrubbed or lost: %q", ce.Stderr)
		}
		if errors.Is(err, services.ErrTimeout) {
			t.Error("a failed job is not a timeout")
		}
	})
	t.Run("long stderr is cut", func(t *testing.T) {
		r := execfake.New().Expect(startKey, execx.Result{ExitCode: 1, Stderr: []byte(strings.Repeat("x", 5000))}, nil)
		var ce *services.CommandError
		if err := newSystemctl(r).Start(context.Background(), unit); !errors.As(err, &ce) || len(ce.Stderr) > 512 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("execx timeout is ErrTimeout", func(t *testing.T) {
		r := execfake.New().Expect(startKey, execx.Result{}, execx.ErrTimeout)
		if err := newSystemctl(r).Start(context.Background(), unit); !errors.Is(err, services.ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled context wins", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := execfake.New().Expect(startKey, execx.Result{}, errors.New("killed"))
		if err := newSystemctl(r).Start(ctx, unit); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a program that cannot start is wrapped", func(t *testing.T) {
		boom := errors.New("fork/exec: permission denied")
		r := execfake.New().Expect(startKey, execx.Result{}, boom)
		if err := newSystemctl(r).Start(context.Background(), unit); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("state: non-zero exit and unparsable output", func(t *testing.T) {
		show := execfake.Key(sysctl, "show", unit, "-p", "ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState")
		r := execfake.New().Expect(show, execx.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus")}, nil)
		var ce *services.CommandError
		if _, err := newSystemctl(r).State(context.Background(), unit); !errors.As(err, &ce) {
			t.Fatalf("err = %v", err)
		}
		r = execfake.New().Expect(show, execx.Result{Stdout: []byte("garbage\n")}, nil)
		if _, err := newSystemctl(r).State(context.Background(), unit); err == nil {
			t.Fatal("garbage output accepted")
		}
	})
}

func TestSystemctlSubscribePolls(t *testing.T) {
	const unit = services.UnitKiwix
	show := execfake.Key(sysctl, "show", unit, "-p", "ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState")
	r := execfake.New().Expect(show, execx.Result{Stdout: fixture(t, "systemctl-show-active.txt")}, nil)
	clock := cfgfake.NewClock(time.Unix(1_700_000_000, 0))
	m := newSystemctl(r, services.WithClock(clock), services.WithPollEvery(5*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := m.Subscribe(ctx, []string{unit})
	if err != nil {
		t.Fatal(err)
	}
	first := recv(t, ch)
	if first.ActiveState != "active" || first.Unit != unit {
		t.Fatalf("first = %+v", first)
	}
	// A poll with an unchanged state sends nothing; a changed one sends the new state.
	waitTickers(t, clock)
	clock.Advance(5 * time.Second)
	expectNone(t, ch)
	r.Expect(show, execx.Result{Stdout: fixture(t, "systemctl-show-failed.txt")}, nil)
	clock.Advance(5 * time.Second)
	if got := recv(t, ch); got.ActiveState != "failed" || got.ExecMainStatus != 137 {
		t.Fatalf("after change = %+v", got)
	}
	// A failing poll keeps the last state quietly.
	r.Expect(show, execx.Result{ExitCode: 1}, nil)
	clock.Advance(5 * time.Second)
	expectNone(t, ch)
	cancel()
	for range ch { // drains until the channel closes
	}
}

func waitTickers(t *testing.T, c *cfgfake.Clock) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.Tickers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the poller never started its ticker")
		}
		time.Sleep(time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan services.UnitState) services.UnitState {
	t.Helper()
	select {
	case st, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return st
	case <-time.After(2 * time.Second):
		t.Fatal("no state received")
	}
	return services.UnitState{}
}

func expectNone(t *testing.T, ch <-chan services.UnitState) {
	t.Helper()
	select {
	case st := <-ch:
		t.Fatalf("unexpected state %+v", st)
	case <-time.After(60 * time.Millisecond):
	}
}
