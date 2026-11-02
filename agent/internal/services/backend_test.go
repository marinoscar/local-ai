package services_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

type logBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.String()
}

func testLogger() (*slog.Logger, *logBuf) {
	buf := &logBuf{}
	lv := new(slog.LevelVar)
	return logx.New(buf, lv), buf
}

// busDialer hands out the scripted buses in order, then fails.
func busDialer(buses ...*fake.Bus) (services.Dialer, *int) {
	n := 0
	var mu sync.Mutex
	return func(context.Context) (services.Bus, error) {
		mu.Lock()
		defer mu.Unlock()
		if n >= len(buses) || buses[n] == nil {
			n++
			return nil, errors.New("dial unix /run/dbus/system_bus_socket: connect: no such file or directory")
		}
		n++
		return buses[n-1], nil
	}, &n
}

const showArgsTail = "ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState"

func TestAutoChoosesDBusWhenTheBusIsReachable(t *testing.T) {
	log, buf := testLogger()
	bus := fake.NewBus()
	dial, _ := busDialer(bus)
	run := execfake.New() // any systemctl run would fail the test
	a := services.NewAuto(context.Background(), dial, run, services.WithLogger(log), services.WithSystemctlPath(sysctl))
	if a.Backend() != services.BackendDBus {
		t.Fatalf("backend = %q", a.Backend())
	}
	if !strings.Contains(buf.String(), `"services.backend":"dbus"`) {
		t.Errorf("the backend must be logged as services.backend=dbus: %s", buf)
	}
	if err := a.Start(context.Background(), services.UnitLlama); err != nil {
		t.Fatal(err)
	}
	if _, err := a.State(context.Background(), services.UnitLlama); err != nil {
		t.Fatal(err)
	}
	if n := len(run.Calls()); n != 0 {
		t.Errorf("systemctl ran %d times while D-Bus was up", n)
	}
	if got := bus.Calls(); len(got) == 0 || got[0] != "StartUnit "+services.UnitLlama {
		t.Errorf("bus calls = %v", got)
	}
}

func TestAutoFallsBackToSystemctlWhenTheBusIsNotReachable(t *testing.T) {
	log, buf := testLogger()
	dial, _ := busDialer(nil)
	run := execfake.New().Expect(execfake.Key(sysctl, "--no-pager", "--no-ask-password", "restart", services.UnitKiwix), execx.Result{}, nil)
	a := services.NewAuto(context.Background(), dial, run, services.WithLogger(log), services.WithSystemctlPath(sysctl))
	if a.Backend() != services.BackendSystemctl {
		t.Fatalf("backend = %q", a.Backend())
	}
	if !strings.Contains(buf.String(), `"services.backend":"systemctl"`) {
		t.Errorf("the backend must be logged as services.backend=systemctl: %s", buf)
	}
	if err := a.Restart(context.Background(), services.UnitKiwix); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls()) != 1 {
		t.Errorf("calls = %v", run.Calls())
	}
}

func TestAutoWithNoDialerUsesSystemctl(t *testing.T) {
	a := services.NewAuto(context.Background(), nil, execfake.New(), services.WithSystemctlPath(sysctl))
	if a.Backend() != services.BackendSystemctl {
		t.Fatalf("backend = %q", a.Backend())
	}
}

func TestAutoFailsOverWhenTheConnectionIsLostAndRedials(t *testing.T) {
	log, buf := testLogger()
	first, second := fake.NewBus(), fake.NewBus()
	dial, dials := busDialer(first, second)
	argv := []string{"--no-pager", "--no-ask-password", "start", services.UnitWhisper}
	run := execfake.New().Expect(execfake.Key(sysctl, argv...), execx.Result{}, nil)
	a := services.NewAuto(context.Background(), dial, run,
		services.WithLogger(log), services.WithSystemctlPath(sysctl), services.WithRedialEvery(150*time.Millisecond))
	ctx := context.Background()

	// The connection drops: the call goes through systemctl at once, and the change is logged.
	first.Lose()
	if err := a.Start(ctx, services.UnitWhisper); err != nil {
		t.Fatal(err)
	}
	if a.Backend() != services.BackendSystemctl || len(run.Calls()) != 1 {
		t.Fatalf("backend %q, systemctl calls %d", a.Backend(), len(run.Calls()))
	}
	if !strings.Contains(buf.String(), `"services.backend":"systemctl"`) || !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("a lost connection must log a warning with the new backend: %s", buf)
	}
	// Before the redial interval no new dial is attempted.
	if err := a.Start(ctx, services.UnitWhisper); err != nil {
		t.Fatal(err)
	}
	if *dials != 1 {
		t.Errorf("dials = %d before the interval", *dials)
	}
	// After it, the connection is dialed again and D-Bus serves the call.
	time.Sleep(200 * time.Millisecond)
	if err := a.Start(ctx, services.UnitWhisper); err != nil {
		t.Fatal(err)
	}
	if a.Backend() != services.BackendDBus || *dials != 2 {
		t.Fatalf("backend %q after %d dials", a.Backend(), *dials)
	}
	if got := second.Calls(); len(got) != 1 || got[0] != "StartUnit "+services.UnitWhisper {
		t.Errorf("second bus calls = %v", got)
	}
	if len(run.Calls()) != 2 {
		t.Errorf("systemctl calls = %d, want 2 (the two while D-Bus was down)", len(run.Calls()))
	}
}

func TestAutoDoesNotFailOverOnAJobFailure(t *testing.T) {
	bus := fake.NewBus()
	bus.JobResult("StartUnit", services.UnitLlama, "failed")
	dial, _ := busDialer(bus)
	run := execfake.New()
	a := services.NewAuto(context.Background(), dial, run, services.WithSystemctlPath(sysctl))
	err := a.Start(context.Background(), services.UnitLlama)
	var je *services.JobError
	if !errors.As(err, &je) || je.Result != "failed" {
		t.Fatalf("err = %v", err)
	}
	if a.Backend() != services.BackendDBus || len(run.Calls()) != 0 {
		t.Errorf("a failed job is not a lost connection: backend %q, systemctl calls %d", a.Backend(), len(run.Calls()))
	}
}

func TestAutoStateFailsOverToSystemctl(t *testing.T) {
	bus := fake.NewBus()
	dial, _ := busDialer(bus)
	show := execfake.Key(sysctl, "show", services.UnitLlama, "-p", showArgsTail)
	run := execfake.New().Expect(show, execx.Result{Stdout: fixture(t, "systemctl-show-active.txt")}, nil)
	a := services.NewAuto(context.Background(), dial, run, services.WithSystemctlPath(sysctl))
	bus.Lose()
	st, err := a.State(context.Background(), services.UnitLlama)
	if err != nil || st.MainPID != 2214 {
		t.Fatalf("state = %+v, %v", st, err)
	}
}

func TestAutoSubscribeSurvivesAnOutage(t *testing.T) {
	first := fake.NewBus()
	first.SetUnit(services.UnitLlama,
		map[string]any{"ActiveState": "active", "SubState": "running", "UnitFileState": "enabled"},
		map[string]any{"MainPID": uint32(1)})
	dial, _ := busDialer(first, nil)
	show := execfake.Key(sysctl, "show", services.UnitLlama, "-p", showArgsTail)
	run := execfake.New().Expect(show, execx.Result{Stdout: fixture(t, "systemctl-show-failed.txt")}, nil)
	a := services.NewAuto(context.Background(), dial, run,
		services.WithSystemctlPath(sysctl), services.WithRedialEvery(80*time.Millisecond), services.WithPollEvery(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Subscribe(ctx, []string{services.UnitLlama})
	if err != nil {
		t.Fatal(err)
	}
	if st := recv(t, ch); st.ActiveState != "active" {
		t.Fatalf("first = %+v", st)
	}
	// The connection drops: the subscription carries on by polling and reports the truth.
	first.Lose()
	if st := recv(t, ch); st.ActiveState != "failed" || st.ExecMainStatus != 137 {
		t.Fatalf("after the outage = %+v", st)
	}
	cancel()
	for range ch {
	}
}

func TestAutoClose(t *testing.T) {
	bus := fake.NewBus()
	dial, _ := busDialer(bus)
	a := services.NewAuto(context.Background(), dial, execfake.New(), services.WithSystemctlPath(sysctl))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bus.Done():
	default:
		t.Error("Close must close the connection")
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
