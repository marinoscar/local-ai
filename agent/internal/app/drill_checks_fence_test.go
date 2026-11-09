package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/netx"
)

type pauseLog struct{ events []string }

func (p *pauseLog) Pause()  { p.events = append(p.events, "pause") }
func (p *pauseLog) Resume() { p.events = append(p.events, "resume") }

func TestFenceChecksAreRegisteredInOrderAndRunOnAnEmptyNode(t *testing.T) {
	reg := drill.NewRegistry()
	paths := Paths{Root: t.TempDir()}
	if err := registerFenceChecks(reg, paths); err != nil {
		t.Fatal(err)
	}
	if err := registerFenceChecks(reg, paths); err == nil {
		t.Fatal("registering the checks twice must be refused")
	}
	cs, err := reg.Select(context.Background(), nil)
	if err != nil || len(cs) != 2 || cs[0].ID != "drl.fence" || cs[1].ID != "drl.config.offline_lint" {
		t.Fatalf("checks = %v err = %v", cs, err)
	}

	// The real fence, the real shared transport, the real embedded bundle.
	link := &pauseLog{}
	f := newDrillFence(link)
	f.Enable()
	defer f.Disable()
	for _, c := range cs {
		out := c.Run(context.Background(), &drill.Env{})
		if out.Result != drill.ResultPass {
			t.Errorf("%s on an empty node: %+v", c.ID, out)
		}
	}
	rep := f.Disable()
	if rep.BlockedDials != 0 {
		t.Errorf("the checks themselves were counted as blocked dials: %+v", rep)
	}
	if len(link.events) != 2 || link.events[0] != "pause" || link.events[1] != "resume" {
		t.Errorf("link events = %v", link.events)
	}
	if netx.Default.Active() {
		t.Error("the fence is still on")
	}
}

func TestOfflineLintReadsTheCanonicalPathsUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	paths := Paths{Root: root}
	ini := paths.Join("/etc/localai/models.ini")
	if err := os.MkdirAll(filepath.Dir(ini), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ini, []byte("[m]\nhf-repo = org-alpha/model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := drill.NewRegistry()
	if err := registerFenceChecks(reg, paths); err != nil {
		t.Fatal(err)
	}
	cs, _ := reg.Select(context.Background(), []string{"drl.config.offline_lint"})
	out := cs[0].Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultFail || out.Details["models_ini"] != "1 finding(s)" {
		t.Fatalf("outcome = %+v", out)
	}
}

// The daemon as built: both checks are on the engine's registry, a run with just them passes, the
// fence was on and saw nothing, and the fence is off again afterwards.
func TestDaemonRunsTheFenceChecks(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	eng := d.Components().Drill
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := eng.Start(ctx, drill.StartRequest{Only: []string{"drl.fence", "drl.config.offline_lint"}, Trigger: drill.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := eng.Get(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var doc drill.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Overall != drill.OverallPass || len(doc.Checks) != 2 || !doc.NetworkFence.Enabled || doc.NetworkFence.BlockedDials != 0 {
		t.Fatalf("doc = overall %s fence %+v checks %+v", doc.Overall, doc.NetworkFence, doc.Checks)
	}
	if netx.Default.Active() {
		t.Error("the fence is still on after the run")
	}
}
