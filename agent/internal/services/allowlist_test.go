package services_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

func TestAllowedTable(t *testing.T) {
	all := []services.Verb{
		services.VerbState, services.VerbSubscribe, services.VerbStart, services.VerbStop, services.VerbRestart,
		services.VerbEnable, services.VerbDisable, services.VerbMask, services.VerbUnmask,
	}
	set := func(vs ...services.Verb) map[services.Verb]bool {
		m := map[services.Verb]bool{}
		for _, v := range vs {
			m[v] = true
		}
		return m
	}
	std := set(services.VerbState, services.VerbSubscribe, services.VerbStart, services.VerbStop, services.VerbRestart, services.VerbEnable, services.VerbDisable)
	tests := []struct {
		unit string
		want map[services.Verb]bool
	}{
		{services.UnitLlama, std},
		{services.UnitKiwix, std},
		{services.UnitWhisper, std},
		{services.UnitMusic, set(services.VerbState, services.VerbSubscribe, services.VerbStart, services.VerbStop, services.VerbRestart)},
		{services.UnitSync, std},
		{services.UnitSyncTimer, std},
		{services.UnitDrill, std},
		{services.UnitDrillTimer, std},
		{services.UnitAgent, set(services.VerbRestart)},
		{services.UnitAgentRollback, set(services.VerbStop)},
		{services.UnitAgentRollbackTmr, set(services.VerbStop)},
		{services.UnitLlamaQwen, set(services.VerbState, services.VerbStart, services.VerbStop, services.VerbEnable, services.VerbDisable, services.VerbMask, services.VerbUnmask)},
		{services.UnitTailscaled, set(services.VerbState)},
		// Anything else is refused for every verb.
		{"sshd.service", set()},
		{"localai-agent.service.d", set()},
		{"", set()},
		{"localai-llama", set()},
		{"../localai-llama.service", set()},
		{"localai-llama.service --now", set()},
	}
	for _, tc := range tests {
		for _, v := range all {
			if got := services.Allowed(tc.unit, v); got != tc.want[v] {
				t.Errorf("Allowed(%q, %s) = %v, want %v", tc.unit, v, got, tc.want[v])
			}
		}
	}
	if !services.Allowed("anything.service", services.VerbDaemonReload) {
		t.Error("daemon-reload names no unit and is always allowed")
	}
}

func TestGuardReturnsViolationError(t *testing.T) {
	f := fake.New()
	g := services.NewGuard(f)
	ctx := context.Background()
	calls := map[string]func() error{
		"Stop agent":          func() error { return g.Stop(ctx, services.UnitAgent) },
		"Start agent":         func() error { return g.Start(ctx, services.UnitAgent) },
		"Restart tailscaled":  func() error { return g.Restart(ctx, services.UnitTailscaled) },
		"Mask llama":          func() error { return g.Mask(ctx, services.UnitLlama) },
		"Unmask whisper":      func() error { return g.Unmask(ctx, services.UnitWhisper) },
		"Enable sshd":         func() error { return g.Enable(ctx, "sshd.service") },
		"Disable agent":       func() error { return g.Disable(ctx, services.UnitAgent) },
		"Restart llama-qwen":  func() error { return g.Restart(ctx, services.UnitLlamaQwen) },
		"State agent":         func() error { _, err := g.State(ctx, services.UnitAgent); return err },
		"Subscribe with sshd": func() error { _, err := g.Subscribe(ctx, []string{services.UnitLlama, "sshd.service"}); return err },
		"Subscribe agent":     func() error { _, err := g.Subscribe(ctx, []string{services.UnitAgent}); return err },
	}
	for name, call := range calls {
		err := call()
		var v *services.ViolationError
		if !errors.As(err, &v) {
			t.Errorf("%s: err = %v, want a ViolationError", name, err)
			continue
		}
		if v.Code() != "LAI-AGT-099" || !strings.Contains(err.Error(), "LAI-AGT-099") {
			t.Errorf("%s: code %q, text %q", name, v.Code(), err)
		}
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("the inner manager saw %d calls from refused requests: %v", n, f.Calls())
	}
}

func TestGuardPassesAllowedCalls(t *testing.T) {
	f := fake.New()
	g := services.NewGuard(f, services.PanicOnViolation())
	ctx := context.Background()
	steps := []error{
		g.Start(ctx, services.UnitLlama),
		g.Restart(ctx, services.UnitAgent),
		g.Stop(ctx, services.UnitAgentRollbackTmr),
		g.Mask(ctx, services.UnitLlamaQwen),
		g.Unmask(ctx, services.UnitLlamaQwen),
		g.DaemonReload(ctx),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if _, err := g.State(ctx, services.UnitTailscaled); err != nil {
		t.Fatal(err)
	}
	ch, err := g.Subscribe(ctx, []string{services.UnitLlama, services.UnitKiwix})
	if err != nil || ch == nil {
		t.Fatalf("subscribe: %v", err)
	}
	if n := len(f.Calls()); n != 8 {
		t.Errorf("inner calls = %d, want 8: %v", n, f.Calls())
	}
}

func TestGuardPanicsWhenAskedTo(t *testing.T) {
	g := services.NewGuard(fake.New(), services.PanicOnViolation())
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "LAI-AGT-099") || !strings.Contains(msg, services.UnitAgent) {
			t.Fatalf("recovered %v", r)
		}
	}()
	_ = g.Stop(context.Background(), services.UnitAgent)
	t.Fatal("Stop(localai-agent.service) did not panic")
}
