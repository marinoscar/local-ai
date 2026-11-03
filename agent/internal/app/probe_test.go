package app

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// The probe of the post-update health check (agent.md section 4.10 step 9): the first problem it finds
// is returned, and a healthy node passes.
func TestPostUpdateProbe(t *testing.T) {
	llamaUnit := func(active, fileState string) services.UnitState {
		return services.UnitState{Unit: services.UnitLlama, ActiveState: active, UnitFileState: fileState}
	}
	tests := []struct {
		name    string
		prepare func(r *rig, fs *statefake.MemFS)
		want    string // a fragment of the problem; "" is a pass
	}{
		{name: "a healthy node passes"},
		{name: "the router is down but the llama unit is running", prepare: func(r *rig, _ *statefake.MemFS) { r.llama.Down(true) }},
		{name: "the router is down, the unit is not running and not meant to", prepare: func(r *rig, _ *statefake.MemFS) {
			r.llama.Down(true)
			r.sysd.Set(llamaUnit("inactive", "disabled"))
		}},
		{name: "the router is down and the enabled unit is not running", want: "llama unit is not running", prepare: func(r *rig, _ *statefake.MemFS) {
			r.llama.Down(true)
			r.sysd.Set(llamaUnit("inactive", "enabled"))
		}},
		{name: "the state store cannot write", want: "not writable", prepare: func(r *rig, fs *statefake.MemFS) {
			fs.Hook = func(op statefake.Op) error {
				if op.Kind != statefake.KindRead && op.Kind != statefake.KindReadDir && op.Kind != statefake.KindExists {
					return syscall.EROFS
				}
				return nil
			}
			// A write attempt is what makes the store notice.
			_ = r.d.Components().State.Mutate(func(s *state.State) error { s.Agent.RecentStarts = append(s.Agent.RecentStarts, t0); return nil })
		}},
		{name: "the node was linked, is online and the link is not linked", want: "cloud link is", prepare: func(r *rig, _ *statefake.MemFS) {
			b, _ := json.Marshal(cloud.LinkRecord{URL: "https://cloud.example.test", ServerNodeID: "n-1", Name: "ai-n3", RegisteredAt: t0})
			r.write("var/lib/localai/cloud/link.json", string(b), 0o600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := statefake.New()
			r := newRig(t, withEnv(func(e *Env) { e.StateFS = fs }))
			d := r.start()
			r.waitStarted()
			r.waitStepEnd(14)
			if tc.prepare != nil {
				tc.prepare(r, fs)
			}
			err := d.postUpdateProbe(context.Background())
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("a healthy node failed the probe: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("probe = %v, want a problem mentioning %q", err, tc.want)
			}
		})
	}
	t.Run("a listener is not up", func(t *testing.T) {
		taken, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer taken.Close()
		r := newRig(t, withEnv(func(e *Env) { e.TCPAddr = taken.Addr().String() }))
		d := r.start()
		r.waitStarted()
		if err := d.postUpdateProbe(context.Background()); err == nil || !strings.Contains(err.Error(), "listener") {
			t.Errorf("probe = %v, want the listener problem", err)
		}
	})
}

// The update clock's Sleep waits on the daemon's clock and ends with its context.
func TestUpdateClock(t *testing.T) {
	clock := cfgfakeClock()
	u := updateClock{clock}
	if !u.Now().Equal(t0) {
		t.Errorf("Now = %v", u.Now())
	}
	done := make(chan error, 1)
	go func() { done <- u.Sleep(context.Background(), 5*1e9) }()
	for clock.Tickers() == 0 {
		runtime.Gosched()
	}
	clock.Advance(5 * 1e9)
	if err := <-done; err != nil {
		t.Errorf("Sleep = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := u.Sleep(ctx, 5*1e9); err == nil {
		t.Error("Sleep on a cancelled context must return its error")
	}
	if err := u.Sleep(context.Background(), 0); err != nil {
		t.Errorf("Sleep(0) = %v", err)
	}
}
