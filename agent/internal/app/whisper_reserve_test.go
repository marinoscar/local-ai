package app

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

// W_res of the router's fit rules (llama-router.md section 4.3.1): the resolved model's min_vram_mib while
// whisper is on the gpu and its unit is not running; 0 on the cpu, with no model, and once it runs.
func TestWhisperReserveForTheFitRules(t *testing.T) {
	const unit = services.UnitWhisper
	cases := []struct {
		name    string
		reserve int64
		state   string // "" = no state set (inactive/dead)
		fail    bool
		want    int64
	}{
		{"gpu, unit not running yet: reserved", 1500, "", false, 1500},
		{"gpu, unit starting: still reserved", 1500, services.ActiveActivating, false, 1500},
		{"gpu, unit running: already in G_others, not counted twice", 1500, services.ActiveActive, false, 0},
		{"gpu, unit failed: reserved again", 1500, "failed", false, 1500},
		{"cpu (or no model): nothing to reserve", 0, "", false, 0},
		{"cpu, unit running: nothing", 0, services.ActiveActive, false, 0},
		{"unit state unreadable: counts as not running", 1500, "", true, 1500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fake.New()
			if tc.state != "" {
				m.Set(services.UnitState{Unit: unit, ActiveState: tc.state})
			}
			var mgr services.Manager = m
			if tc.fail {
				mgr = failingState{m}
			}
			d := &Daemon{c: &Components{Systemd: mgr}}
			d.whisper.reserve.Store(tc.reserve)
			if got := d.whisperReserveMiB(); got != tc.want {
				t.Errorf("W_res = %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("without systemd the reservation stands", func(t *testing.T) {
		d := &Daemon{c: &Components{}}
		d.whisper.reserve.Store(900)
		if got := d.whisperReserveMiB(); got != 900 {
			t.Errorf("W_res = %d", got)
		}
	})
}

// failingState is a Manager whose State always fails.
type failingState struct{ *fake.Manager }

func (failingState) State(_ context.Context, _ string) (services.UnitState, error) {
	return services.UnitState{}, errors.New("systemd did not answer")
}

// The reservation reaches the router's fit rules: the inputs every load plans with, and the model list's
// `fits` view, both ask the daemon, and the reservation drops to 0 once the unit runs.
func TestWhisperReserveIsPassedToTheRouterInputs(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	d.whisper.reserve.Store(1500)
	if got := d.routerInputs().Fit.WhisperReserveMiB; got != 1500 {
		t.Errorf("router inputs W_res = %d, want 1500", got)
	}
	if d.merger == nil || d.merger.WhisperReserveMiB == nil || d.merger.WhisperReserveMiB() != 1500 {
		t.Error("the model list does not get W_res")
	}
	r.sysd.Set(services.UnitState{Unit: services.UnitWhisper, ActiveState: services.ActiveActive})
	if got := d.routerInputs().Fit.WhisperReserveMiB; got != 0 {
		t.Errorf("whisper running: W_res = %d, want 0", got)
	}
}
