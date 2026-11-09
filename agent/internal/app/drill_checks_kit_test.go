package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// runDrillOnly starts a drill of the given checks, waiting while a model operation of the startup still
// holds llama busy, and returns its results by check id.
func runDrillOnly(t *testing.T, d *Daemon, only ...string) map[string]drill.CheckResult {
	t.Helper()
	eng := d.Components().Drill
	var res drill.StartResult
	var err error
	for i := 0; i < 100; i++ {
		if res, err = eng.Start(context.Background(), drill.StartRequest{Only: only}); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
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
	out := map[string]drill.CheckResult{}
	for _, c := range doc.Checks {
		out[c.ID] = c
	}
	return out
}

// The daemon registers checks 17, 18, 20 and 21 with the real kit verifier, build preflight,
// hardening checks and content index. The rig is a node with models and no complete kit, no commit
// pinned, a desktop chassis and verified content from 2026-11-03.
func TestDaemonDrillKitPowerAndClockChecks(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	got := runDrillOnly(t, d, "drl.kit", "drl.power", "drl.clock")

	if c := got["drl.kit.present"]; c.Result != drill.ResultFail || c.Code != "LAI-DRL-070" || c.Details["roots_checked"] != float64(1) {
		t.Errorf("kit.present = %+v", c)
	}
	c := got["drl.kit.rebuild_dry_run"]
	if c.Result != drill.ResultFail || c.Code != "LAI-DRL-071" || c.Details["llama_ok"] != false {
		t.Errorf("kit.rebuild_dry_run = %+v", c)
	}
	if failed, _ := c.Details["llama_failed"].(string); !strings.Contains(failed, "commit") {
		t.Errorf("the failed preflight check is listed: %v", c.Details)
	}
	if c.Details["whisper_skipped"] == nil {
		t.Errorf("whisper is not configured in the rig, so it is not part of the check: %v", c.Details)
	}
	if c := got["drl.power"]; c.Result != drill.ResultSkip || c.Details["reason"] != "not_a_laptop" {
		t.Errorf("power = %+v", c)
	}
	if c := got["drl.clock"]; c.Result != drill.ResultPass || c.Details["newest_verified_at"] == nil {
		t.Errorf("clock = %+v", c)
	}
}

// A laptop chassis on a node whose hardening was never applied warns.
func TestDaemonDrillPowerOnALaptopWithoutHardeningWarns(t *testing.T) {
	r := newRig(t)
	r.write("sys/class/dmi/id/chassis_type", "10\n", 0o644)
	d := r.start()
	r.waitStarted()
	c := runDrillOnly(t, d, "drl.power")["drl.power"]
	if c.Result != drill.ResultWarn || c.Code != "LAI-DRL-080" || c.Details["hardening"] != "not_applied" {
		t.Fatalf("power = %+v", c)
	}
}

func TestDaemonNewestVerifiedReadsTheContentIndex(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	at, ok := d.newestVerified()
	if !ok || !at.Equal(t0) {
		t.Fatalf("newestVerified = %v, %v", at, ok)
	}
}
