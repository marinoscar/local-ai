package app

import (
	"testing"
)

func TestSmokeBoot(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	rp := r.doAuth("GET", "/health", nil, false)
	t.Logf("health %d %s", rp.status, rp.body)
	t.Logf("steps %+v", r.d.Steps())
	t.Logf("log:\n%s", r.logText())
	t.Logf("notify %v", r.d.env.Notifier.Sent())
	t.Logf("llama calls %v", r.llama.Requests())
}
