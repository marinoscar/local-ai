package cli

import (
	"net/http"
	"strings"
	"testing"
	"time"

	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// --- the journal of a scheduled run ---------------------------------------------------------------------

// A scheduled run says what became of every item, one line each, then the summary: that is what
// `journalctl -u localai-sync` shows in the morning.
func TestScheduledSyncJournalsOneLinePerItem(t *testing.T) {
	events := func(r *arkRig) {
		r.progress("wikipedia-es-nopic", 0, 12_000_000_000, 0)
		r.itemDone("wikipedia-es-nopic", "verified", "", "")
		r.itemDone("docs-bundle", "verified", "", "")
		r.itemDone("wikipedia-en-nopic", "failed", "retries exhausted", "LAI-SYN-031")
		r.itemDone("stackexchange-electronics", "skipped", "budget", "LAI-SYN-041")
		r.itemDone("big-model", "quarantined", "checksum mismatch", "LAI-SYN-020")
		r.syncDone("done", `{"items":5,"done":2,"skipped":1,"failed":2,"bytes_total":1,"bytes_done":1}`, map[string]string{"internal": "yellow"})
	}
	t.Run("English", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { events(r) }
		got := r.run(mgArgs("ark", "sync", "--scheduled", "--wait"))
		if got.code != ExitWarnings {
			t.Fatalf("%+v", got)
		}
		for _, want := range []string{
			"✓ wikipedia-es-nopic: downloaded (2026-10)\n",
			"✓ docs-bundle: already up to date (2026-10)\n",
		} {
			if !strings.Contains(got.out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, got.out)
			}
		}
		for _, want := range []string{
			"! wikipedia-en-nopic: failed, retries exhausted [LAI-SYN-031]\n",
			"! stackexchange-electronics: left out, budget [LAI-SYN-041]\n",
			"! big-model: damaged, set aside, checksum mismatch [LAI-SYN-020]\n",
		} {
			if !strings.Contains(got.errOut, want) {
				t.Errorf("stderr lacks %q:\n%s", want, got.errOut)
			}
		}
		if n := strings.Count(got.out, "wikipedia-es-nopic"); n != 1 {
			t.Errorf("an item is named %d times on stdout, want once:\n%s", n, got.out)
		}
		if !strings.Contains(got.out, "Sync finished: 1 downloaded, 1 already up to date, 2 failed, 1 skipped.") {
			t.Errorf("no summary after the lines:\n%s", got.out)
		}
		if i, j := strings.Index(got.out, "docs-bundle"), strings.Index(got.out, "Sync finished"); i < 0 || j < i {
			t.Errorf("the summary must come after the items:\n%s", got.out)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { events(r) }
		got := r.run(mgArgs("--lang", "es", "ark", "sync", "--scheduled", "--wait"))
		if !strings.Contains(got.out, "✓ docs-bundle: ya estaba al día (2026-10)\n") || !strings.Contains(got.errOut, "! wikipedia-en-nopic: falló, retries exhausted [LAI-SYN-031]\n") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("an item of a drive names the drive", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() {
			r.ev.Publish(agentevents.TypeSyncItemDone, arkRunID, agentevents.SyncItemDone{RunID: arkRunID, Root: "ark-3f9c0a12", Item: "docs-bundle", Result: "verified", Version: "2026-10"})
			r.syncDone("done", okTotals, nil)
		}
		got := r.run(mgArgs("ark", "sync", "--scheduled"))
		if got.code != 0 || !strings.Contains(got.out, "✓ ark-3f9c0a12/docs-bundle: already up to date (2026-10)\n") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a manual run keeps its own output: no per-item lines", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { events(r) }
		got := r.run(mgArgs("ark", "sync", "--wait"))
		if strings.Contains(got.out, "docs-bundle: already up to date") || strings.Contains(got.errOut, "left out") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("--json prints the document and nothing else", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { events(r) }
		got := r.run(mgArgs("--json", "ark", "sync", "--scheduled"))
		if got.code != ExitWarnings || strings.Contains(got.out, "already up to date") || strings.Contains(got.errOut, "failed, retries") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("--quiet keeps the warnings off", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { events(r) }
		got := r.run(mgArgs("-q", "ark", "sync", "--scheduled"))
		if got.code != ExitWarnings || strings.Contains(got.errOut, "left out") {
			t.Errorf("%+v", got)
		}
	})
}

// The exit codes of the unit (aictl.md section 3.4): the whole table in one place, for a scheduled run.
func TestScheduledSyncExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		events func(r *arkRig)
		code   int
		out    string
	}{
		{"done, every item ok", func(r *arkRig) { r.itemDone("docs-bundle", "verified", "", ""); r.syncDone("done", okTotals, nil) }, 0, "✓ Sync finished"},
		{"done, an item failed", func(r *arkRig) {
			r.itemDone("x", "failed", "nope", "LAI-SYN-031")
			r.syncDone("done", `{"items":2,"done":1,"skipped":0,"failed":1}`, nil)
		}, ExitWarnings, "Sync finished"},
		{"done, an item left for time", func(r *arkRig) {
			r.syncDone("done", `{"items":2,"done":1,"skipped":1,"failed":0}`, nil)
		}, ExitWarnings, "1 skipped"},
		{"the run failed", func(r *arkRig) { r.syncDone("failed", `{"items":0,"done":0,"skipped":0,"failed":0}`, nil) }, ExitErrors, ""},
		{"stopped by the agent (the node left online mode)", func(r *arkRig) { r.syncDone("cancelled", `{}`, nil) }, ExitErrors, "Sync stopped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkRig(t)
			r.onStart = func() { tc.events(r) }
			got := r.run(mgArgs("ark", "sync", "--scheduled", "--wait"))
			if got.code != tc.code || !strings.Contains(got.out+got.errOut, tc.out) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// --- the bounded stop -------------------------------------------------------------------------------------

func withGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := scheduledStopGrace
	scheduledStopGrace = d
	t.Cleanup(func() { scheduledStopGrace = old })
}

func (r *arkRig) startScheduled(t *testing.T, args ...string) (<-chan followOut, *lockedBuf) {
	t.Helper()
	r.agent().Respond(http.MethodDelete, "/ark/sync", 202, `{"run_id":"`+arkRunID+`","state":"cancelling"}`)
	ch, errb := r.runAsync(mgArgs(append([]string{"ark", "sync", "--scheduled", "--wait"}, args...)...))
	untilTrue(t, "the request", func() bool { return r.agent().Count("/ark/sync") == 1 })
	r.ev.WaitActive(1)
	return ch, errb
}

// SIGTERM from `systemctl stop`: the cancel goes out once, however many times the signal is seen, and
// aictl waits until the run reports that it stopped.
func TestScheduledSyncSIGTERMCancelsOnceAndWaitsForTheStop(t *testing.T) {
	withGrace(t, 30*time.Second)
	r := newArkRig(t)
	ch, _ := r.startScheduled(t)
	untilTrue(t, "the cancel", func() bool {
		sendSIGINT(t)
		time.Sleep(20 * time.Millisecond)
		return r.agent().Count("/ark/sync") >= 2
	})
	sendSIGINT(t) // systemd resends nothing, a person might: still one cancel
	time.Sleep(100 * time.Millisecond)
	select {
	case got := <-ch:
		t.Fatalf("aictl left before the run stopped: %+v", got)
	default:
	}
	r.itemDone("docs-bundle", "verified", "", "")
	r.syncDone("cancelled", `{}`, nil)
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.out, "Sync stopped") || !strings.Contains(got.out, "docs-bundle: already up to date") {
		t.Fatalf("%+v", got)
	}
	var methods []string
	for _, q := range r.agent().Requests() {
		if q.Path == "/ark/sync" {
			methods = append(methods, q.Method)
		}
	}
	if strings.Join(methods, ",") != "POST,DELETE" {
		t.Errorf("requests: %v", methods)
	}
}

// A run that does not stop within the grace does not keep aictl (and so the unit) alive until systemd
// kills it: aictl says so and leaves.
func TestScheduledSyncLeavesWhenTheRunDoesNotStopInTime(t *testing.T) {
	withGrace(t, 200*time.Millisecond)
	r := newArkRig(t)
	ch, _ := r.startScheduled(t)
	untilTrue(t, "the cancel", func() bool {
		sendSIGINT(t)
		time.Sleep(20 * time.Millisecond)
		return r.agent().Count("/ark/sync") >= 2
	})
	started := time.Now()
	got := finish(t, ch)
	if got.code != ExitInterrupted || !strings.Contains(got.errOut, "did not stop within") {
		t.Fatalf("%+v", got)
	}
	if time.Since(started) > 3*time.Second {
		t.Errorf("took %v", time.Since(started))
	}
	if r.agent().Count("/ark/sync") != 2 {
		t.Errorf("requests: %v", r.requestLines())
	}
}

func TestScheduledSyncStopWhenTheAgentCannotBeToldLeavesAtOnce(t *testing.T) {
	withGrace(t, 30*time.Second)
	r := newArkRig(t)
	r.agent().RespondError(http.MethodDelete, "/ark/sync", 500, "LAI-AGT-099", "Broken.", "Fix.", nil)
	ch, _ := r.runAsync(mgArgs("ark", "sync", "--scheduled", "--wait"))
	untilTrue(t, "the request", func() bool { return r.agent().Count("/ark/sync") == 1 })
	r.ev.WaitActive(1)
	untilTrue(t, "the cancel attempt", func() bool {
		sendSIGINT(t)
		time.Sleep(20 * time.Millisecond)
		return r.agent().Count("/ark/sync") >= 2
	})
	got := finish(t, ch)
	if got.code != ExitInterrupted {
		t.Fatalf("%+v", got)
	}
}
