package drill

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeLink struct {
	mu  sync.Mutex
	log []string
}

func (l *fakeLink) Pause()  { l.add("pause") }
func (l *fakeLink) Resume() { l.add("resume") }
func (l *fakeLink) add(s string) {
	l.mu.Lock()
	l.log = append(l.log, s)
	l.mu.Unlock()
}
func (l *fakeLink) calls() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.log, ",")
}

// orderFence records its calls on the same log as the link, to prove the order.
type orderFence struct {
	link   *fakeLink
	report FenceReport
	panics bool
}

func (f *orderFence) Enable() { f.link.add("fence-on") }
func (f *orderFence) Disable() FenceReport {
	f.link.add("fence-off")
	if f.panics {
		panic("fence broke")
	}
	return f.report
}

func TestPausingFenceOrderAndReport(t *testing.T) {
	link := &fakeLink{}
	f := &PausingFence{Inner: &orderFence{link: link, report: FenceReport{BlockedDials: 2, Callers: []string{"cloud"}}}, Link: link}
	f.Enable()
	rep := f.Disable()
	if got := link.calls(); got != "pause,fence-on,fence-off,resume" {
		t.Fatalf("calls = %s", got)
	}
	if rep.BlockedDials != 2 || len(rep.Callers) != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPausingFenceWithoutPartsAndAfterAPanic(t *testing.T) {
	link := &fakeLink{}
	only := &PausingFence{Link: link}
	only.Enable()
	if rep := only.Disable(); rep.BlockedDials != 0 || link.calls() != "pause,resume" {
		t.Fatalf("link only: %v %s", rep, link.calls())
	}
	inner := &orderFence{link: &fakeLink{}, report: FenceReport{BlockedDials: 1}}
	noLink := &PausingFence{Inner: inner}
	noLink.Enable()
	if rep := noLink.Disable(); rep.BlockedDials != 1 {
		t.Fatalf("fence only: %+v", rep)
	}

	link = &fakeLink{}
	boom := &PausingFence{Inner: &orderFence{link: link, panics: true}, Link: link}
	boom.Enable()
	func() {
		defer func() { _ = recover() }()
		boom.Disable()
	}()
	if got := link.calls(); got != "pause,fence-on,fence-off,resume" {
		t.Fatalf("the link must resume even when the inner fence panics: %s", got)
	}
}

// TestEngineRunPausesTheLinkForTheWholeRun: through the engine, the link is paused before the first
// check and resumed after the last one, and also when the run is aborted.
func TestEngineRunPausesTheLinkForTheWholeRun(t *testing.T) {
	link := &fakeLink{}
	r := newRig(t, func(_ *rig, d *Deps) { d.Fence = &PausingFence{Inner: &orderFence{link: link}, Link: link} })
	r.add(Check{ID: "drl.a", Order: 1, Run: func(context.Context, *Env) Outcome {
		link.add("check")
		return Outcome{Result: ResultPass}
	}})
	r.startWait(StartRequest{})
	if got := link.calls(); got != "pause,fence-on,check,fence-off,resume" {
		t.Fatalf("calls = %s", got)
	}

	link2 := &fakeLink{}
	r2 := newRig(t, func(_ *rig, d *Deps) { d.Fence = &PausingFence{Inner: &orderFence{link: link2}, Link: link2} })
	g := newGate()
	r2.add(g.check("drl.hang", 1))
	if _, err := r2.eng.Start(context.Background(), StartRequest{}); err != nil {
		t.Fatal(err)
	}
	<-g.started
	if got := link2.calls(); got != "pause,fence-on" {
		t.Fatalf("during the run: %s", got)
	}
	if _, err := r2.eng.Abort(""); err != nil {
		t.Fatal(err)
	}
	r2.wait()
	if got := link2.calls(); got != "pause,fence-on,fence-off,resume" {
		t.Fatalf("after the abort: %s", got)
	}
}
