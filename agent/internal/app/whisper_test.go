package app

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
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
