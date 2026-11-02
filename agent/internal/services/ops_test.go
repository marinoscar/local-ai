package services_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

var llama = services.Service{Name: "llama", Unit: services.UnitLlama}

func newController(m services.Manager) *services.Controller {
	return &services.Controller{Manager: m, WaitFor: 400 * time.Millisecond, Poll: 5 * time.Millisecond}
}

func TestControllerReachesTheStateAsked(t *testing.T) {
	tests := []struct {
		action services.Action
		before string
		want   string
	}{
		{services.ActionStart, "inactive", "active"},
		{services.ActionStart, "active", "active"}, // starting a running unit is a no-op, not an error
		{services.ActionRestart, "active", "active"},
		{services.ActionStop, "active", "inactive"},
		{services.ActionStop, "inactive", "inactive"},
	}
	for _, tc := range tests {
		t.Run(string(tc.action)+" from "+tc.before, func(t *testing.T) {
			m := fake.New()
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: tc.before, SubState: "x"})
			st, err := newController(m).Do(context.Background(), llama, tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if st.ActiveState != tc.want || st.Unit != llama.Unit {
				t.Errorf("state = %+v", st)
			}
			calls := m.Calls()
			if len(calls) == 0 || calls[0].Verb != services.Verb(tc.action) || calls[0].Unit != llama.Unit {
				t.Errorf("calls = %v", calls)
			}
		})
	}
}

func TestControllerWaitsForTheFinalState(t *testing.T) {
	t.Run("activating then active", func(t *testing.T) {
		m := fake.New()
		m.Freeze(llama.Unit) // the verbs succeed and change nothing: the test drives the state
		m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "activating", SubState: "start"})
		go func() {
			time.Sleep(40 * time.Millisecond)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "active", SubState: "running"})
		}()
		st, err := newController(m).Do(context.Background(), llama, services.ActionStart)
		if err != nil || st.ActiveState != "active" {
			t.Fatalf("state %+v, err %v", st, err)
		}
	})
	t.Run("deactivating then inactive", func(t *testing.T) {
		m := fake.New()
		m.Freeze(llama.Unit)
		m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "deactivating", SubState: "stop-sigterm"})
		go func() {
			time.Sleep(40 * time.Millisecond)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "inactive", SubState: "dead"})
		}()
		st, err := newController(m).Do(context.Background(), llama, services.ActionStop)
		if err != nil || st.ActiveState != "inactive" {
			t.Fatalf("state %+v, err %v", st, err)
		}
	})
	t.Run("a stop that leaves the unit failed counts as stopped", func(t *testing.T) {
		m := fake.New()
		m.Freeze(llama.Unit)
		m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "failed", SubState: "failed"})
		if st, err := newController(m).Do(context.Background(), llama, services.ActionStop); err != nil || st.ActiveState != "failed" {
			t.Fatalf("state %+v, err %v", st, err)
		}
	})
	t.Run("a restart waits through auto-restart", func(t *testing.T) {
		m := fake.New()
		m.Freeze(llama.Unit)
		m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "activating", SubState: "auto-restart"})
		go func() {
			time.Sleep(40 * time.Millisecond)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "active", SubState: "running"})
		}()
		if _, err := newController(m).Do(context.Background(), llama, services.ActionRestart); err != nil {
			t.Fatal(err)
		}
	})
}

func TestControllerFailures(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(m *fake.Manager)
		action services.Action
		reason string
	}{
		{"systemd job failed", func(m *fake.Manager) {
			m.Fail(services.VerbStart, llama.Unit, &services.JobError{Unit: llama.Unit, Verb: services.VerbStart, Result: "failed"})
		}, services.ActionStart, services.ReasonJobFailed},
		{"systemctl exited non-zero", func(m *fake.Manager) {
			m.Fail(services.VerbRestart, llama.Unit, &services.CommandError{Unit: llama.Unit, Verb: services.VerbRestart, ExitCode: 1})
		}, services.ActionRestart, services.ReasonJobFailed},
		{"the job timed out", func(m *fake.Manager) {
			m.Fail(services.VerbStart, llama.Unit, services.ErrTimeout)
		}, services.ActionStart, services.ReasonTimeout},
		{"the unit lands in failed", func(m *fake.Manager) {
			m.Freeze(llama.Unit)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "failed", SubState: "failed", ExecMainStatus: 1})
		}, services.ActionStart, services.ReasonWrongState},
		{"the unit stays inactive after a start", func(m *fake.Manager) { m.Freeze(llama.Unit) }, services.ActionStart, services.ReasonWrongState},
		{"never becomes active in time", func(m *fake.Manager) {
			m.Freeze(llama.Unit)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "activating", SubState: "start"})
		}, services.ActionStart, services.ReasonTimeout},
		{"never stops in time", func(m *fake.Manager) {
			m.Freeze(llama.Unit)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "deactivating", SubState: "stop-sigterm"})
		}, services.ActionStop, services.ReasonTimeout},
		{"systemd cannot be reached for the verb", func(m *fake.Manager) {
			m.Fail(services.VerbStop, llama.Unit, errors.New("dial unix: no such file"))
		}, services.ActionStop, services.ReasonUnavailable},
		{"the bus is lost while waiting", func(m *fake.Manager) {
			m.Freeze(llama.Unit)
			m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "activating", SubState: "start"})
			m.Fail(services.VerbState, llama.Unit, services.ErrBusLost)
		}, services.ActionStart, services.ReasonUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := fake.New()
			tc.setup(m)
			c := newController(m)
			c.WaitFor = 120 * time.Millisecond
			_, err := c.Do(context.Background(), llama, tc.action)
			var ae *services.ActionError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want an ActionError", err)
			}
			if ae.Reason != tc.reason || ae.Service != llama || ae.Action != tc.action {
				t.Errorf("ActionError = %+v, want reason %s", ae, tc.reason)
			}
			if ae.Error() == "" {
				t.Error("empty error text")
			}
		})
	}
}

func TestControllerReportsTheLastStateOfAFailedAction(t *testing.T) {
	m := fake.New()
	m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "failed", SubState: "failed", ExecMainStatus: 7})
	m.Fail(services.VerbStart, llama.Unit, &services.JobError{Unit: llama.Unit, Verb: services.VerbStart, Result: "failed"})
	st, err := newController(m).Do(context.Background(), llama, services.ActionStart)
	if err == nil || st.ActiveState != "failed" || st.ExecMainStatus != 7 {
		t.Fatalf("state %+v, err %v", st, err)
	}
	var je *services.JobError
	if !errors.As(err, &je) || je.Result != "failed" {
		t.Errorf("the job error must stay reachable: %v", err)
	}
}

func TestControllerCancellation(t *testing.T) {
	m := fake.New()
	m.Freeze(llama.Unit)
	m.Set(services.UnitState{Unit: llama.Unit, ActiveState: "activating", SubState: "start"})
	ctx, cancel := context.WithCancel(context.Background())
	c := newController(m)
	c.WaitFor = 5 * time.Second
	errc := make(chan error, 1)
	go func() { _, err := c.Do(ctx, llama, services.ActionStart); errc <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Do ignored the cancelled context")
	}
}

func TestControllerRejectsAnUnknownAction(t *testing.T) {
	m := fake.New()
	if _, err := newController(m).Do(context.Background(), llama, "explode"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if len(m.Calls()) != 0 {
		t.Errorf("calls = %v", m.Calls())
	}
	if services.Action("explode").Valid() || !services.ActionStart.Valid() || !services.ActionStop.Valid() || !services.ActionRestart.Valid() {
		t.Error("Action.Valid")
	}
}

func TestByNameAndByUnit(t *testing.T) {
	for _, name := range []string{"llama", "kiwix", "whisper"} {
		s, ok := services.ByName(name)
		if !ok || s.Name != name {
			t.Errorf("ByName(%s) = %+v, %v", name, s, ok)
		}
		if u, ok := services.ByUnit(s.Unit); !ok || u != s {
			t.Errorf("ByUnit(%s) = %+v, %v", s.Unit, u, ok)
		}
	}
	for _, bad := range []string{"", "sync", "agent", "LLAMA", "localai-llama.service"} {
		if _, ok := services.ByName(bad); ok {
			t.Errorf("ByName(%q) matched", bad)
		}
	}
	if _, ok := services.ByUnit(services.UnitSync); ok {
		t.Error("ByUnit matched the sync unit")
	}
}
