package services_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

func newDBus(b *fake.Bus, opts ...services.Option) *services.DBus {
	return services.NewDBus(b, opts...)
}

func TestDBusJobResults(t *testing.T) {
	const unit = services.UnitLlama
	tests := []struct {
		name    string
		result  string
		silent  bool
		wantErr bool
		timeout bool
		job     *services.JobError
	}{
		{name: "done is success", result: "done"},
		{name: "failed is an error with the result string", result: "failed", wantErr: true, job: &services.JobError{Unit: unit, Result: "failed"}},
		{name: "canceled", result: "canceled", wantErr: true, job: &services.JobError{Unit: unit, Result: "canceled"}},
		{name: "dependency", result: "dependency", wantErr: true, job: &services.JobError{Unit: unit, Result: "dependency"}},
		{name: "systemd job timeout", result: "timeout", wantErr: true, timeout: true, job: &services.JobError{Unit: unit, Result: "timeout"}},
		{name: "no JobRemoved in time", silent: true, wantErr: true, timeout: true},
	}
	verbs := []struct {
		name   string
		method string
		call   func(m *services.DBus) error
	}{
		{"start", "StartUnit", func(m *services.DBus) error { return m.Start(context.Background(), unit) }},
		{"stop", "StopUnit", func(m *services.DBus) error { return m.Stop(context.Background(), unit) }},
		{"restart", "RestartUnit", func(m *services.DBus) error { return m.Restart(context.Background(), unit) }},
	}
	for _, v := range verbs {
		for _, tc := range tests {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				b := fake.NewBus()
				if tc.silent {
					b.NeverFinish(v.method, unit)
				} else {
					b.JobResult(v.method, unit, tc.result)
				}
				err := v.call(newDBus(b, services.WithTimeouts(80*time.Millisecond, 0)))
				if (err != nil) != tc.wantErr {
					t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
				}
				if got := errors.Is(err, services.ErrTimeout); got != tc.timeout {
					t.Errorf("errors.Is(err, ErrTimeout) = %v, want %v (err %v)", got, tc.timeout, err)
				}
				if tc.job != nil {
					var je *services.JobError
					if !errors.As(err, &je) || je.Result != tc.job.Result || je.Unit != unit {
						t.Errorf("err = %v, want a JobError with result %q", err, tc.job.Result)
					}
					if !strings.Contains(err.Error(), tc.job.Result) {
						t.Errorf("the error text must carry systemd's result string: %v", err)
					}
				}
				if got := b.Calls(); len(got) != 1 || got[0] != v.method+" "+unit {
					t.Errorf("calls = %v", got)
				}
			})
		}
	}
}

func TestDBusJobIgnoresOtherJobs(t *testing.T) {
	const unit = services.UnitKiwix
	b := fake.NewBus()
	b.NeverFinish("StartUnit", unit)
	m := newDBus(b, services.WithTimeouts(2*time.Second, 0))
	errc := make(chan error, 1)
	go func() { errc <- m.Start(context.Background(), unit) }()
	// Wait until the job exists, then send a JobRemoved for another job (ignored) and the right one.
	waitCalls(t, b, 1)
	b.Emit(services.Signal{Name: "JobRemoved", Job: "/org/freedesktop/systemd1/job/99", Unit: "other.service", Result: "failed"})
	b.Emit(services.Signal{Name: "PropertiesChanged", Path: services.UnitPath(unit)})
	select {
	case err := <-errc:
		t.Fatalf("returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	b.Emit(services.Signal{Name: "JobRemoved", Job: "/org/freedesktop/systemd1/job/1", Unit: unit, Result: "done"})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func waitCalls(t *testing.T, b *fake.Bus, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(b.Calls()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("bus saw %v, wanted %d calls", b.Calls(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDBusCallErrors(t *testing.T) {
	const unit = services.UnitLlama
	t.Run("a bus error on StartUnit is returned", func(t *testing.T) {
		b := fake.NewBus()
		boom := errors.New("org.freedesktop.systemd1.NoSuchUnit")
		b.Fail("StartUnit", boom)
		err := newDBus(b).Start(context.Background(), unit)
		if !errors.Is(err, boom) || errors.Is(err, services.ErrBusLost) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a lost connection is ErrBusLost", func(t *testing.T) {
		b := fake.NewBus()
		b.Lose()
		if err := newDBus(b).Start(context.Background(), unit); !errors.Is(err, services.ErrBusLost) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a connection lost while waiting for the job", func(t *testing.T) {
		b := fake.NewBus()
		b.NeverFinish("StopUnit", unit)
		m := newDBus(b, services.WithTimeouts(5*time.Second, 0))
		errc := make(chan error, 1)
		go func() { errc <- m.Stop(context.Background(), unit) }()
		waitCalls(t, b, 1)
		b.Lose()
		if err := <-errc; !errors.Is(err, services.ErrBusLost) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		b := fake.NewBus()
		b.NeverFinish("StartUnit", unit)
		ctx, cancel := context.WithCancel(context.Background())
		m := newDBus(b, services.WithTimeouts(5*time.Second, 0))
		errc := make(chan error, 1)
		go func() { errc <- m.Start(ctx, unit) }()
		waitCalls(t, b, 1)
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDBusUnitFilesCallThenReload(t *testing.T) {
	const unit = services.UnitLlamaQwen
	tests := []struct {
		name   string
		call   func(m *services.DBus) error
		method string
	}{
		{"enable", func(m *services.DBus) error { return m.Enable(context.Background(), unit) }, "EnableUnitFiles"},
		{"disable", func(m *services.DBus) error { return m.Disable(context.Background(), unit) }, "DisableUnitFiles"},
		{"mask", func(m *services.DBus) error { return m.Mask(context.Background(), unit) }, "MaskUnitFiles"},
		{"unmask", func(m *services.DBus) error { return m.Unmask(context.Background(), unit) }, "UnmaskUnitFiles"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewBus()
			if err := tc.call(newDBus(b)); err != nil {
				t.Fatal(err)
			}
			want := []string{tc.method + " " + unit, "Reload "}
			if got := b.Calls(); !reflect.DeepEqual(got, want) {
				t.Errorf("calls = %q, want %q", got, want)
			}
		})
		t.Run(tc.name+" fails without a reload", func(t *testing.T) {
			b := fake.NewBus()
			boom := errors.New("File exists")
			b.Fail(tc.method, boom)
			if err := tc.call(newDBus(b)); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if got := b.Calls(); len(got) != 1 {
				t.Errorf("calls = %q, want only the failed one", got)
			}
		})
	}
	b := fake.NewBus()
	if err := newDBus(b).DaemonReload(context.Background()); err != nil || !reflect.DeepEqual(b.Calls(), []string{"Reload "}) {
		t.Errorf("DaemonReload: %v, calls %q", err, b.Calls())
	}
	b.Fail("Reload", errors.New("denied"))
	if err := newDBus(b).DaemonReload(context.Background()); err == nil {
		t.Error("a failed Reload must be reported")
	}
}

func TestDBusState(t *testing.T) {
	b := fake.NewBus()
	b.SetUnit(services.UnitLlama,
		map[string]any{
			"ActiveState": "active", "SubState": "running", "UnitFileState": "enabled",
			"ActiveEnterTimestamp": uint64(1_793_606_405_000_000),
		},
		map[string]any{
			"MainPID": uint32(2214), "NRestarts": uint32(3), "ExecMainStatus": int32(1),
			"ExecMainExitTimestamp": uint64(1_793_606_398_000_000),
		})
	b.SetUnit(services.UnitSyncTimer,
		map[string]any{"ActiveState": "active", "SubState": "waiting", "UnitFileState": "enabled", "ActiveEnterTimestamp": uint64(0)}, nil)
	m := newDBus(b)

	st, err := m.State(context.Background(), services.UnitLlama)
	if err != nil {
		t.Fatal(err)
	}
	want := services.UnitState{
		Unit: services.UnitLlama, ActiveState: "active", SubState: "running", MainPID: 2214, NRestarts: 3, ExecMainStatus: 1,
		ActiveEnterTimestamp:  time.UnixMicro(1_793_606_405_000_000).UTC(),
		ExecMainExitTimestamp: time.UnixMicro(1_793_606_398_000_000).UTC(),
		UnitFileState:         "enabled",
	}
	if !st.Same(want) {
		t.Errorf("state = %+v\nwant   %+v", st, want)
	}
	if got := b.Calls(); len(got) != 3 || got[0] != "LoadUnit "+services.UnitLlama {
		t.Errorf("calls = %q (LoadUnit, then GetAll Unit and Service)", got)
	}

	// A timer has no Service interface: it must not be asked for it.
	b.Fail("GetAll", nil)
	tm, err := m.State(context.Background(), services.UnitSyncTimer)
	if err != nil || tm.ActiveState != "active" || tm.MainPID != 0 || !tm.ActiveEnterTimestamp.IsZero() {
		t.Fatalf("timer state = %+v, %v", tm, err)
	}
	for _, c := range b.Calls() {
		if strings.Contains(c, "localai_2dsync_2etimer") && strings.HasSuffix(c, "systemd1.Service") {
			t.Errorf("asked a timer for the Service interface: %s", c)
		}
	}

	// A unit with no file is the not-found stub: inactive, dead, no error.
	ghost, err := m.State(context.Background(), services.UnitKiwix)
	if err != nil || ghost.ActiveState != "inactive" || ghost.SubState != "dead" {
		t.Fatalf("not-found unit = %+v, %v", ghost, err)
	}
}

func TestDBusStateErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, method := range []string{"LoadUnit", "GetAll"} {
		b := fake.NewBus()
		b.Fail(method, boom)
		if _, err := newDBus(b).State(context.Background(), services.UnitLlama); !errors.Is(err, boom) {
			t.Errorf("%s failing: err = %v", method, err)
		}
	}
	b := fake.NewBus()
	b.SetUnit(services.UnitLlama, map[string]any{"SubState": "running"}, map[string]any{})
	if _, err := newDBus(b).State(context.Background(), services.UnitLlama); err == nil {
		t.Error("a state without ActiveState must be an error")
	}
	b = fake.NewBus()
	b.Lose()
	if _, err := newDBus(b).State(context.Background(), services.UnitLlama); !errors.Is(err, services.ErrBusLost) {
		t.Errorf("lost bus: err = %v", err)
	}
}

func TestDBusSubscribe(t *testing.T) {
	b := fake.NewBus()
	setState := func(unit, active, sub string) {
		b.SetUnit(unit,
			map[string]any{"ActiveState": active, "SubState": sub, "UnitFileState": "enabled"},
			map[string]any{"MainPID": uint32(0), "NRestarts": uint32(0), "ExecMainStatus": int32(0)})
	}
	setState(services.UnitLlama, "active", "running")
	setState(services.UnitKiwix, "inactive", "dead")
	m := newDBus(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := m.Subscribe(ctx, []string{services.UnitLlama, services.UnitKiwix})
	if err != nil {
		t.Fatal(err)
	}
	// The matches are added once, for the unit objects.
	wantPaths := []string{services.UnitPath(services.UnitLlama), services.UnitPath(services.UnitKiwix)}
	if got := b.Watched(); !reflect.DeepEqual(got, wantPaths) {
		t.Errorf("watched = %q, want %q", got, wantPaths)
	}
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		st := recv(t, ch)
		got[st.Unit] = st.ActiveState
	}
	if got[services.UnitLlama] != "active" || got[services.UnitKiwix] != "inactive" {
		t.Fatalf("initial states = %v", got)
	}

	// A PropertiesChanged re-reads the unit and sends the new state.
	setState(services.UnitLlama, "failed", "failed")
	b.Emit(services.Signal{Name: "PropertiesChanged", Path: services.UnitPath(services.UnitLlama)})
	if st := recv(t, ch); st.Unit != services.UnitLlama || st.ActiveState != "failed" {
		t.Fatalf("after change = %+v", st)
	}
	// A repeat of the same state, a signal for an unwatched unit and a JobRemoved send nothing.
	b.Emit(services.Signal{Name: "PropertiesChanged", Path: services.UnitPath(services.UnitLlama)})
	b.Emit(services.Signal{Name: "PropertiesChanged", Path: services.UnitPath("sshd.service")})
	b.Emit(services.Signal{Name: "JobRemoved", Job: "/j/1", Result: "done"})
	expectNone(t, ch)

	cancel()
	for range ch {
	}
}

func TestDBusSubscribeEndsWhenTheBusIsLost(t *testing.T) {
	b := fake.NewBus()
	ch, err := newDBus(b).Subscribe(context.Background(), []string{services.UnitLlama})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
	b.Lose()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected state")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel stayed open after the connection was lost")
	}
}

func TestDBusSubscribeWatchFailure(t *testing.T) {
	b := fake.NewBus()
	boom := errors.New("AccessDenied")
	b.Fail("Watch", boom)
	if _, err := newDBus(b).Subscribe(context.Background(), []string{services.UnitLlama}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnitPathEscaping(t *testing.T) {
	tests := map[string]string{
		"localai-llama.service":        "/org/freedesktop/systemd1/unit/localai_2dllama_2eservice",
		"localai-agent-rollback.timer": "/org/freedesktop/systemd1/unit/localai_2dagent_2drollback_2etimer",
		"tailscaled.service":           "/org/freedesktop/systemd1/unit/tailscaled_2eservice",
		"x:y.service":                  "/org/freedesktop/systemd1/unit/x_3ay_2eservice",
		"a_b.service":                  "/org/freedesktop/systemd1/unit/a_5fb_2eservice",
		"sys-devices-x\\x2dy.device":   "/org/freedesktop/systemd1/unit/sys_2ddevices_2dx_5cx2dy_2edevice",
		"UPPER9.service":               "/org/freedesktop/systemd1/unit/UPPER9_2eservice",
	}
	for unit, want := range tests {
		if got := services.UnitPath(unit); got != want {
			t.Errorf("UnitPath(%q) = %q, want %q", unit, got, want)
		}
	}
}
