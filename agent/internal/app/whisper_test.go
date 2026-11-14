package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// whisper.enabled at startup: the unit is written and systemd reloaded, and with no whisper model in
// the rig's catalog the env is not written, the unit is stopped and never started or restarted.
func TestWhisperEnabledWritesTheUnitAndNeverStartsIt(t *testing.T) {
	r := newRig(t, func(r *rig) { r.writeNodeYAMLText(r.nodeYAML() + "whisper:\n  enabled: true\n") })
	r.start()
	r.waitStarted()
	unit := r.path("etc/systemd/system/localai-whisper.service")
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(unit)
		if err == nil && strings.Contains(string(b), "whisper-server") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the unit was not written: %v\nlog:\n%s", err, r.logText())
		}
		runtime.Gosched()
	}
	for {
		stopped := false
		for _, c := range r.sysd.Calls() {
			if c.Unit == services.UnitWhisper && (c.Verb == services.VerbStart || c.Verb == services.VerbRestart) {
				t.Fatalf("the whisper unit was started or restarted with no model: %v", c)
			}
			if c.Unit == services.UnitWhisper && c.Verb == services.VerbStop {
				stopped = true
			}
		}
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no model: the unit should have been stopped: %v", r.sysd.Calls())
		}
		runtime.Gosched()
	}
	if _, err := os.Stat(r.path("etc/localai/whisper.env")); err == nil {
		t.Error("whisper.env must not be written without a model")
	}
}

// A node with whisper off writes nothing.
func TestWhisperDisabledWritesNothing(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(r.path("etc/systemd/system/localai-whisper.service")); err == nil {
		t.Error("the whisper unit was written on a node with whisper off")
	}
}

// kiwix.enabled at startup: the unit is written (so an upgraded agent's new unit text reaches the disk) and
// systemd is never asked to start it; with kiwix off nothing is written.
func TestKiwixEnabledWritesTheUnitAtStartAndNeverStartsIt(t *testing.T) {
	r := newRig(t, func(r *rig) { r.writeNodeYAMLText(r.nodeYAML() + "kiwix:\n  enabled: true\n") })
	r.start()
	r.waitStarted()
	unit := r.path("etc/systemd/system/localai-kiwix.service")
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(unit)
		if err == nil && strings.Contains(string(b), "kiwix-serve") && strings.Contains(string(b), "AF_NETLINK") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the kiwix unit was not written at start: %v\nlog:\n%s", err, r.logText())
		}
		runtime.Gosched()
	}
	for _, c := range r.sysd.Calls() {
		if c.Unit == services.UnitKiwix && (c.Verb == services.VerbStart || c.Verb == services.VerbRestart) {
			t.Fatalf("the writer started or restarted kiwix: %v", c)
		}
	}
}

func TestKiwixDisabledWritesNoUnit(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(r.path("etc/systemd/system/localai-kiwix.service")); err == nil {
		t.Error("the kiwix unit was written on a node with kiwix off")
	}
}

// An agent upgrade that changes the kiwix unit (the first time: --nodatealiases) restarts a kiwix that
// was running with the old text, once, so undated library links (the Portal's first aid) work at once.
// The same unit text again restarts nothing.
func TestKiwixUnitChangedAtStartRestartsARunningKiwixOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     func(t *testing.T) string
		running bool
		want    int
	}{
		{name: "old unit, kiwix running", old: func(*testing.T) string { return "[Service]\nExecStart=/old\n" }, running: true, want: 1},
		{name: "old unit, kiwix stopped", old: func(*testing.T) string { return "[Service]\nExecStart=/old\n" }, running: false, want: 0},
		{name: "current unit, kiwix running", old: func(t *testing.T) string {
			b, err := kiwix.RenderUnit(0)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}, running: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.old(t)
			r := newRig(t, func(r *rig) {
				r.writeNodeYAMLText(r.nodeYAML() + "kiwix:\n  enabled: true\n")
				unit := r.path("etc/systemd/system/localai-kiwix.service")
				if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unit, []byte(old), 0o644); err != nil {
					t.Fatal(err)
				}
				state := "inactive"
				if tc.running {
					state = "active"
				}
				r.sysd.Set(services.UnitState{Unit: services.UnitKiwix, ActiveState: state, SubState: "running", UnitFileState: "enabled"})
			})
			r.start()
			r.waitStarted()
			restarts := func() int {
				n := 0
				for _, c := range r.sysd.Calls() {
					if c.Unit == services.UnitKiwix && c.Verb == services.VerbRestart {
						n++
					}
				}
				return n
			}
			deadline := time.Now().Add(5 * time.Second)
			for tc.want > 0 && restarts() < tc.want && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			if got := restarts(); got != tc.want {
				t.Fatalf("kiwix restarts = %d, want %d\nlog:\n%s", got, tc.want, r.logText())
			}
		})
	}
}
