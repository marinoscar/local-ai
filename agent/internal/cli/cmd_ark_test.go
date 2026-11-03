package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

const arkRunID = "run-20261102T031500Z-a1f3"

// arkRig is a fake agent whose sync, verify and import routes start a run: onStart publishes the
// run's events from inside the handler (so they reach aictl before it knows the run id), then the
// handler answers 202.
type arkRig struct {
	*mgRig
	onStart func()
	status  int
	body    string
}

func newArkRig(t *testing.T) *arkRig {
	t.Helper()
	r := &arkRig{mgRig: newMgRig(t, fake.Options{}), status: 202, body: `{"run_id":"` + arkRunID + `","state":"running","events":"/api/v1/events?types=sync.*"}`}
	r.events()
	start := func(w http.ResponseWriter, _ *http.Request) {
		if r.onStart != nil {
			r.onStart()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LocalAI-Op-Id", arkRunID)
		w.WriteHeader(r.status)
		io.WriteString(w, r.body)
	}
	for _, p := range []string{"/ark/sync", "/ark/verify", "/ark/import"} {
		r.agent().Handle(http.MethodPost, p, start)
	}
	return r
}

func (r *arkRig) progress(item string, done, total, rate int64) {
	r.ev.Publish(events.TypeSyncProgress, arkRunID, events.SyncProgress{RunID: arkRunID, Root: "internal", Item: item,
		State: "downloading", BytesDone: done, BytesTotal: total, RateBPS: rate})
}

func (r *arkRig) itemDone(item, result, reason, code string) {
	r.ev.Publish(events.TypeSyncItemDone, arkRunID, events.SyncItemDone{RunID: arkRunID, Root: "internal", Item: item,
		Result: result, Version: "2026-10", Reason: reason, Code: code})
}

func (r *arkRig) syncDone(state, totals string, ready map[string]string) {
	r.ev.Publish(events.TypeSyncDone, arkRunID, events.SyncDone{RunID: arkRunID, State: state, Totals: json.RawMessage(totals), Ready: ready})
}

const okTotals = `{"items":16,"done":16,"skipped":0,"failed":0,"bytes_total":14500000000,"bytes_done":14500000000}`

// A finished run with two downloads and fourteen items already there.
func (r *arkRig) twoDownloads() {
	r.onStart = func() {
		r.progress("wikipedia-es-nopic", 0, 12_000_000_000, 0)
		r.progress("wikipedia-es-nopic", 4_800_000_000, 12_000_000_000, 20_000_000)
		r.itemDone("wikipedia-es-nopic", "verified", "", "")
		r.progress("osm-central-america", 2_500_000_000, 2_500_000_000, 30_000_000)
		r.itemDone("osm-central-america", "verified", "", "")
		r.syncDone("done", okTotals, map[string]string{"internal": "green"})
	}
}

func TestArkSyncWait(t *testing.T) {
	t.Run("human: a line per item, the summary, exit 0", func(t *testing.T) {
		r := newArkRig(t)
		r.twoDownloads()
		got := r.run(mgArgs("ark", "sync", "--wait"))
		checkRun(t, got, 0,
			"✓ Sync finished: 2 downloaded, 14 already up to date, 0 failed. Internal disk is\n  ready.\n",
			"… Downloading wikipedia-es-nopic\n… Downloading osm-central-america\n")
		const body = `{"allow_offline_mode":false,"dry_run":false,"items":[],"roots":[],"trigger":"manual"}`
		if b := r.lastBody(http.MethodPost, "/ark/sync"); b != body {
			t.Errorf("body %s", b)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newArkRig(t)
		r.twoDownloads()
		got := r.run(mgArgs("--lang", "es", "ark", "sync", "--wait"))
		checkRun(t, got, 0,
			"✓ Sincronización terminada: 2 descargados, 14 ya estaban al día, 0 con error. El\n  disco interno está listo.\n",
			"… Descargando wikipedia-es-nopic\n… Descargando osm-central-america\n")
	})
	t.Run("--json is the sync.done data", func(t *testing.T) {
		r := newArkRig(t)
		r.twoDownloads()
		got := r.run(mgArgs("--json", "ark", "sync", "--wait"))
		checkRun(t, got, 0, `{"run_id":"`+arkRunID+`","state":"done","totals":`+okTotals+`,"ready":{"internal":"green"}}`+"\n", "")
	})
	t.Run("--quiet prints no progress and no summary", func(t *testing.T) {
		r := newArkRig(t)
		r.twoDownloads()
		checkRun(t, r.run(mgArgs("-q", "ark", "sync", "--wait")), 0, "", "")
	})
	t.Run("roots, items and the flags reach the body", func(t *testing.T) {
		r := newArkRig(t)
		r.twoDownloads()
		got := r.run(mgArgs("ark", "sync", "--wait", "--root", "internal", "--root", "ark-3f9c0a12", "--item", "wikipedia-es-nopic",
			"--item", "gutenberg-en", "--allow-offline-mode"))
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		const want = `{"allow_offline_mode":true,"dry_run":false,"items":["wikipedia-es-nopic","gutenberg-en"],"roots":["internal","ark-3f9c0a12"],"trigger":"manual"}`
		if b := r.lastBody(http.MethodPost, "/ark/sync"); b != want {
			t.Errorf("body %s", b)
		}
	})
	t.Run("without --wait it returns when the run has started", func(t *testing.T) {
		r := newArkRig(t)
		got := r.run(mgArgs("ark", "sync"))
		want := "✓ Sync started (run " + arkRunID + "). It keeps running in the\n  background; follow it with `aictl ark status`.\n"
		checkRun(t, got, 0, want, "")
		if n := len(r.events().Connections()); n != 0 {
			t.Errorf("events connections = %d", n)
		}
		raw := r.run(mgArgs("--json", "ark", "sync"))
		checkRun(t, raw, 0, r.body+"\n", "")
	})
}

// The three exit codes of a followed run: 0 all ok, 8 failed or skipped items (last good copies are
// kept), 9 the run failed or was stopped.
func TestArkFinalExitCodes(t *testing.T) {
	failedTotals := `{"items":16,"done":14,"skipped":0,"failed":1,"bytes_total":1,"bytes_done":1}`
	cases := []struct {
		name   string
		events func(r *arkRig)
		exit   int
		out    string // substring
		errOut string // substring
	}{
		{"all ok", func(r *arkRig) { r.syncDone("done", okTotals, nil) }, 0, "✓ Sync finished: 0 downloaded, 16 already up to date, 0 failed.", ""},
		{"an item failed", func(r *arkRig) {
			r.itemDone("wikipedia-en-nopic", "failed", "retries exhausted", "LAI-SYN-031")
			r.syncDone("done", failedTotals, map[string]string{"internal": "yellow"})
		}, ExitWarnings, "! Sync finished: 0 downloaded, 14 already up to date, 1 failed. Internal disk is usable but needs attention (yellow).",
			"! wikipedia-en-nopic: Download failed after all retries.  [LAI-SYN-031]"},
		{"items were skipped", func(r *arkRig) {
			r.itemDone("gutenberg-en", "skipped", "budget", "")
			r.syncDone("done", `{"items":16,"done":15,"skipped":1,"failed":0}`, nil)
		}, ExitWarnings, "! Sync finished: 0 downloaded, 15 already up to date, 0 failed, 1 skipped.", ""},
		{"the run failed", func(r *arkRig) { r.syncDone("failed", `{"items":16,"done":3,"skipped":0,"failed":13}`, nil) },
			ExitErrors, "", "✗ Sync did not finish. Run `aictl doctor` to see why."},
		{"the agent stopped the run", func(r *arkRig) { r.syncDone("cancelled", okTotals, nil) },
			ExitErrors, "i Sync stopped. Partial downloads are kept and continue next time.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newArkRig(t)
			r.onStart = func() { tc.events(r) }
			got := r.run(mgArgs("ark", "sync", "--wait"))
			if got.code != tc.exit || !strings.Contains(got.out, tc.out) || !strings.Contains(got.errOut, tc.errOut) {
				t.Fatalf("exit %d, want %d\nstdout %s\nstderr %s", got.code, tc.exit, got.out, got.errOut)
			}
			// The same code with --json: one document, the exit code carries the result.
			j := newArkRig(t)
			j.onStart = func() { tc.events(j) }
			jg := j.run(mgArgs("--json", "ark", "sync", "--wait"))
			if jg.code != tc.exit || jg.errOut != "" || strings.Count(jg.out, "\n") != 1 {
				t.Fatalf("json: %+v", jg)
			}
		})
	}
}

func TestArkVerifyAndImportSummaries(t *testing.T) {
	t.Run("verify --wait with a mismatch", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() {
			r.itemDone("wikipedia-en-nopic", "quarantined", "checksum mismatch", "LAI-SYN-025")
			r.syncDone("done", `{"items":25,"done":24,"skipped":0,"failed":1}`, map[string]string{"internal": "red"})
		}
		got := r.run(mgArgs("ark", "verify", "--wait", "--full", "--root", "internal"))
		if got.code != ExitWarnings || !strings.HasPrefix(got.out, "! Check finished: 24 verified, 1 with problems. Internal disk is not ready (red)") ||
			!strings.Contains(got.errOut, "wikipedia-en-nopic: A stored file no longer matches its checksum") ||
			!strings.Contains(got.errOut, "[LAI-SYN-025]") {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, "/ark/verify"); b != `{"full":true,"items":[],"roots":["internal"]}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("verify without --wait", func(t *testing.T) {
		r := newArkRig(t)
		got := r.run(mgArgs("ark", "verify"))
		if got.code != 0 || !strings.HasPrefix(got.out, "✓ Check started (run "+arkRunID+")") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("import --wait", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() {
			r.progress("qwen3.5-9b-q4km", 5_900_000_000, 5_900_000_000, 400_000_000)
			r.itemDone("qwen3.5-9b-q4km", "verified", "", "")
			r.syncDone("done", `{"items":3,"done":3,"skipped":0,"failed":0}`, map[string]string{"internal": "green"})
		}
		got := r.run(mgArgs("ark", "import", "--from", "ark-3f9c0a12", "--wait", "--add-to-profile", "--catalog", "--item", "qwen3.5-9b-q4km"))
		checkRun(t, got, 0, "✓ Import finished: 1 copied, 2 already here, 0 failed. Internal disk is ready.\n", "… Copying qwen3.5-9b-q4km\n")
		const want = `{"add_to_profile":true,"catalog":true,"dry_run":false,"from_root":"ark-3f9c0a12","items":["qwen3.5-9b-q4km"]}`
		if b := r.lastBody(http.MethodPost, "/ark/import"); b != want {
			t.Errorf("body %s", b)
		}
	})
}

// --- --scheduled ----------------------------------------------------------------------------------------

// A scheduled run that is skipped exits 0 with an "i Sync skipped" line, never 3, whichever way the
// agent says so.
func TestArkScheduledSkips(t *testing.T) {
	t.Run("refused 409: the mode, the slot", func(t *testing.T) {
		cases := []struct {
			code, mode, want string
		}{
			{"LAI-SYN-010", "survival", "i Sync skipped: the node is in survival mode.\n"},
			{"LAI-SYN-011", "offline", "i Sync skipped: the node is in offline mode.\n"},
			{"LAI-SYN-005", "online", "i Sync skipped: another sync is already running.\n"},
		}
		for _, tc := range cases {
			r := newArkRig(t)
			r.agent().SetHealth("0.0.0-dev", tc.mode, false)
			r.agent().RespondError(http.MethodPost, "/ark/sync", 409, tc.code, "Refused.", "Fix.", nil)
			got := r.run(mgArgs("ark", "sync", "--scheduled", "--wait"))
			checkRun(t, got, 0, tc.want, "")
			if b := r.lastBody(http.MethodPost, "/ark/sync"); !strings.Contains(b, `"trigger":"scheduled"`) {
				t.Errorf("body %s", b)
			}
		}
	})
	t.Run("the same refusals are errors for a person", func(t *testing.T) {
		r := newArkRig(t)
		r.agent().RespondError(http.MethodPost, "/ark/sync", 409, "LAI-SYN-010", "Manual sync refused in survival mode.", "Leave survival mode first.", nil)
		got := r.run(mgArgs("ark", "sync", "--wait"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "[LAI-SYN-010]") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("answered as a skipped run", func(t *testing.T) {
		for reason, want := range map[string]string{
			"offline":  "i Sync skipped: offline.\n",
			"battery":  "i Sync skipped: the computer is running on battery.\n",
			"disabled": "i Sync skipped: it is turned off (sync.enabled is false).\n",
			"no_roots": "i Sync skipped: there is no place to store the content.\n",
		} {
			r := newArkRig(t)
			r.status, r.body = 200, `{"run_id":"`+arkRunID+`","state":"skipped","reason":"`+reason+`"}`
			got := r.run(mgArgs("ark", "sync", "--scheduled"))
			checkRun(t, got, 0, want, "")
		}
	})
	t.Run("skipped after it started: the reason is read from the run", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { r.syncDone("skipped", `{}`, nil) }
		r.agent().Respond(http.MethodGet, "/ark/sync", 200, `{"run_id":"`+arkRunID+`","trigger":"scheduled","state":"skipped","reason":"offline"}`)
		got := r.run(mgArgs("ark", "sync", "--scheduled"))
		checkRun(t, got, 0, "i Sync skipped: offline.\n", "")
	})
	t.Run("Spanish and --json", func(t *testing.T) {
		r := newArkRig(t)
		r.status, r.body = 200, `{"run_id":"`+arkRunID+`","state":"skipped","reason":"battery"}`
		checkRun(t, r.run(mgArgs("--lang", "es", "ark", "sync", "--scheduled")), 0, "i Sincronización omitida: el equipo funciona con batería.\n", "")
		checkRun(t, r.run(mgArgs("--json", "ark", "sync", "--scheduled")), 0, `{"run_id":"`+arkRunID+`","state":"skipped","reason":"battery"}`+"\n", "")
	})
	t.Run("a finished scheduled run exits 0, 8 or 9 like --wait", func(t *testing.T) {
		r := newArkRig(t)
		r.onStart = func() { r.syncDone("done", `{"items":3,"done":2,"skipped":0,"failed":1}`, nil) }
		got := r.run(mgArgs("ark", "sync", "--scheduled"))
		if got.code != ExitWarnings {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("exit 3 is only the agent being unreachable", func(t *testing.T) {
		r := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true})
		got := r.run(mgArgs("ark", "sync", "--scheduled"))
		if got.code != ExitUnreachable || !strings.Contains(got.errOut, "[LAI-CLI-001]") {
			t.Fatalf("%+v", got)
		}
	})
}

// A real run lost to the agent restarting reports how it really ended (GET /ark/sync).
func TestArkRereadAfterTheStreamIsLost(t *testing.T) {
	r := newArkRig(t)
	r.onStart = func() { r.progress("wikipedia-es-nopic", 1, 2, 1) }
	r.agent().Respond(http.MethodGet, "/ark/sync", 200, `{"run_id":"`+arkRunID+`","state":"done","totals":`+okTotals+`}`)
	ch, errb := r.runAsync(mgArgs("ark", "sync", "--wait"))
	untilTrue(t, "the progress line", func() bool { return strings.Contains(errb.String(), "Downloading wikipedia-es-nopic") })
	r.ev.Restart(false)
	got := finish(t, ch)
	if got.code != 0 || !strings.HasPrefix(got.out, "✓ Sync finished:") {
		t.Fatalf("%+v", got)
	}
}

// --- Ctrl-C ----------------------------------------------------------------------------------------------

func TestArkSyncCtrlC(t *testing.T) {
	newRun := func(t *testing.T, answer string) (*arkRig, <-chan followOut, *lockedBuf) {
		r := newArkRig(t)
		r.agent().Respond(http.MethodDelete, "/ark/sync", 202, `{"run_id":"`+arkRunID+`","state":"cancelling"}`)
		o := mgArgs("ark", "sync", "--wait").withTerm(answer)
		ch, errb := r.runAsync(o)
		untilTrue(t, "the request", func() bool { return r.agent().Count("/ark/sync") == 1 })
		r.ev.WaitActive(1)
		return r, ch, errb
	}
	press := func(t *testing.T, errb *lockedBuf) {
		untilTrue(t, "the question", func() bool {
			sendSIGINT(t)
			time.Sleep(20 * time.Millisecond)
			return strings.Contains(errb.String(), "Cancel the sync too? [y/N] ")
		})
	}
	t.Run("yes: the sync is cancelled and aictl waits for it to stop", func(t *testing.T) {
		r, ch, errb := newRun(t, "y")
		press(t, errb)
		untilTrue(t, "DELETE /ark/sync", func() bool { return r.agent().Count("/ark/sync") == 2 })
		r.syncDone("cancelled", `{}`, nil)
		got := finish(t, ch)
		if got.code != ExitInterrupted || !strings.Contains(got.out, "i Sync stopped.") {
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
	})
	t.Run("no: stop waiting, the sync runs on, nothing is sent", func(t *testing.T) {
		r, ch, errb := newRun(t, "n")
		press(t, errb)
		got := finish(t, ch)
		if got.code != ExitInterrupted || !strings.Contains(got.errOut, "Still running in the background") {
			t.Fatalf("%+v", got)
		}
		if r.agent().Count("/ark/sync") != 1 {
			t.Errorf("a cancel was sent: %v", r.requestLines())
		}
	})
	t.Run("Spanish question", func(t *testing.T) {
		r := newArkRig(t)
		ch, errb := r.runAsync(mgArgs("--lang", "es", "ark", "sync", "--wait").withTerm("n"))
		untilTrue(t, "the request", func() bool { return r.agent().Count("/ark/sync") == 1 })
		r.ev.WaitActive(1)
		untilTrue(t, "the question", func() bool {
			sendSIGINT(t)
			time.Sleep(20 * time.Millisecond)
			return strings.Contains(errb.String(), "¿Cancelar también la sincronización? [s/N] ")
		})
		finish(t, ch)
	})
	t.Run("under systemd nobody is asked: SIGTERM cancels at once", func(t *testing.T) {
		r := newArkRig(t)
		r.agent().Respond(http.MethodDelete, "/ark/sync", 202, `{"run_id":"`+arkRunID+`","state":"cancelling"}`)
		ch, _ := r.runAsync(mgArgs("ark", "sync", "--scheduled")) // no terminal
		untilTrue(t, "the request", func() bool { return r.agent().Count("/ark/sync") == 1 })
		r.ev.WaitActive(1)
		untilTrue(t, "the cancel", func() bool {
			sendSIGINT(t)
			time.Sleep(20 * time.Millisecond)
			return r.agent().Count("/ark/sync") == 2
		})
		r.syncDone("cancelled", `{}`, nil)
		if got := finish(t, ch); got.code != ExitInterrupted {
			t.Fatalf("%+v", got)
		}
	})
}

// The live line of a transfer: the bar, then the sizes, the rate and the time left.
func TestArkProgressUpdate(t *testing.T) {
	c := &Call{Lang: LangEN}
	u := c.arkProgressUpdate(kindSync, events.SyncProgress{Root: "internal", Item: "wikipedia-es-nopic", State: "downloading",
		BytesDone: 4_800_000_000, BytesTotal: 12_000_000_000, RateBPS: 20_000_000})
	if u.Label != "Downloading wikipedia-es-nopic" || u.Percent != 40 || u.Detail != "4.8/12.0 GB  20.0 MB/s  6 min left" {
		t.Fatalf("%+v", u)
	}
	u = c.arkProgressUpdate(kindImport, events.SyncProgress{Item: "x", BytesTotal: 0})
	if u.Label != "Copying x" || u.Percent != progress.NoBar || u.Detail != "" {
		t.Fatalf("%+v", u)
	}
	u = c.arkProgressUpdate(kindSync, events.SyncProgress{Item: "x", State: "verifying", BytesDone: 1, BytesTotal: 2})
	if u.Label != "Checking x" {
		t.Fatalf("%+v", u)
	}
	es := &Call{Lang: LangES}
	if u := es.arkProgressUpdate(kindSync, events.SyncProgress{Item: "x", BytesDone: 1, BytesTotal: 2, RateBPS: 1}); !strings.HasPrefix(u.Label, "Descargando ") {
		t.Fatalf("%+v", u)
	}
}

// --- --dry-run ---------------------------------------------------------------------------------------------

const planBody = `{"root":"internal","profile":"small-8gb","catalog_version":"2026.11.02-1","budget_bytes":220000000000,"planned_bytes":186400000000,` +
	`"fs_free_bytes":291700000000,"min_free_bytes":25000000000,"peak_bytes":249100000000,"items":[` +
	`{"pos":1,"id":"kit-llama-cpp","kind":"kit","tier":"k","action":"keep","from_version":"f3586e66b0","to_version":"f3586e66b0","final_bytes":400000000,"need_bytes":0,"source":"network"},` +
	`{"pos":5,"id":"small-vlm-q4km","kind":"model","tier":1,"action":"download","to_version":"89abcdef0123","final_bytes":5200000000,"need_bytes":5200000000,"source":"local_copy"},` +
	`{"pos":7,"id":"wikipedia-en-nopic","kind":"kiwix","tier":1,"action":"update","from_version":"2026-07","to_version":"2026-10","final_bytes":48700000000,"need_bytes":48700000000,"source":"network","reason":"refresh_due"},` +
	`{"pos":19,"id":"stackexchange-electronics","kind":"kiwix","tier":2,"action":"skip","final_bytes":3900000000,"reason":"budget"},` +
	`{"pos":20,"id":"osm-north-america","kind":"maps","action":"orphan","to_version":"20260501","final_bytes":11200000000}]}`

func TestArkDryRun(t *testing.T) {
	plan := func(t *testing.T) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/ark/sync", 200, planBody)
		r.agent().Respond(http.MethodPost, "/ark/import", 200, planBody)
		return r
	}
	t.Run("human", func(t *testing.T) {
		got := plan(t).run(mgArgs("ark", "sync", "--dry-run"))
		want := `Plan for root "internal" · profile small-8gb · catalog 2026.11.02-1
Budget 220 GB · planned 186 GB · free now 292 GB · keep free ≥ 25.0 GB

 #  Item                       Tier  Action    Version        Size  Note
 1  kit-llama-cpp              k     keep      f3586e66b0   400 MB
 5  small-vlm-q4km             1     download  89abcdef01   5.2 GB  local copy
 7  wikipedia-en-nopic         1     update    2026-10     48.7 GB  refresh due
19  stackexchange-electronics  2     skip      -            3.9 GB  budget
20  osm-north-america          -     orphan    20260501    11.2 GB  orphan

An orphan is in the ark but not in the profile; ` + "`aictl ark prune --orphans`" + ` removes it.
Downloads: 2 items, 48.7 GB from the internet, 5.2 GB local copy.
Peak disk use during sync: 249 GB (free will not drop below 42.6 GB).
Nothing was changed (dry run).
`
		if got.code != 0 || got.errOut != "" {
			t.Fatalf("%+v", got)
		}
		if got.out != want {
			t.Fatalf("stdout:\n%s\nwant:\n%s", got.out, want)
		}
	})
	t.Run("--json is the body, and the request says dry_run", func(t *testing.T) {
		r := plan(t)
		checkRun(t, r.run(mgArgs("--json", "ark", "sync", "--dry-run", "--root", "internal")), 0, planBody+"\n", "")
		if b := r.lastBody(http.MethodPost, "/ark/sync"); !strings.Contains(b, `"dry_run":true`) || !strings.Contains(b, `"roots":["internal"]`) {
			t.Errorf("body %s", b)
		}
	})
	t.Run("a local build is not from the internet", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/ark/sync", 200, `{"root":"internal","profile":"small-8gb","catalog_version":"2026.11.02-1","offline":true,"items":[`+
			`{"pos":1,"id":"docs-bundle","kind":"docs","tier":"k","action":"download","to_version":"catalog-2026.11.02-1","final_bytes":1000000,"need_bytes":1000000,"source":"local_build"},`+
			`{"pos":2,"id":"kit-llama-cpp","kind":"kit","tier":"k","action":"skip","reason":"unresolved"}]}`)
		got := r.run(mgArgs("ark", "sync", "--dry-run"))
		if got.code != 0 || !strings.Contains(got.out, "built from local files") || !strings.Contains(got.out, "Downloads: 1 items, 0 B from the internet, 1.0 MB local copy.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("several plans", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/ark/sync", 200, `{"plans":[`+planBody+`,`+strings.Replace(planBody, `"root":"internal"`, `"root":"ark-3f9c0a12"`, 1)+`]}`)
		got := r.run(mgArgs("ark", "sync", "--dry-run"))
		if got.code != 0 || strings.Count(got.out, "Plan for root") != 2 || strings.Count(got.out, "Nothing was changed") != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("import --dry-run uses the one drive", func(t *testing.T) {
		r := plan(t)
		r.agent().Respond(http.MethodGet, "/ark/drives", 200, `{"drives":[{"root_id":"ark-3f9c0a12","state":"ready"},{"root_id":null,"state":"unusable"}]}`)
		got := r.run(mgArgs("ark", "import", "--dry-run"))
		if got.code != 0 || !strings.HasPrefix(got.out, `Plan for root "internal"`) {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, "/ark/import"); !strings.Contains(b, `"from_root":"ark-3f9c0a12"`) || !strings.Contains(b, `"dry_run":true`) {
			t.Errorf("body %s", b)
		}
	})
	t.Run("a closed pipe is not a failure", func(t *testing.T) {
		r := plan(t)
		o := mgArgs("ark", "sync", "--dry-run")
		env := r.env(o)
		env.Stdout, env.Stderr = &brokenPipe{}, io.Discard
		if code := Run(context.Background(), env, mgRegistry(), o.args); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}

// --- import: which drive ---------------------------------------------------------------------------------------

func TestArkImportDriveChoice(t *testing.T) {
	drives := func(body string) *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/ark/drives", 200, body)
		r.agent().Respond(http.MethodPost, "/ark/import", 202, `{"run_id":"`+arkRunID+`"}`)
		return r
	}
	t.Run("several drives: exit 2 with the list, nothing started", func(t *testing.T) {
		r := drives(`{"drives":[{"root_id":"ark-3f9c0a12","state":"ready"},{"root_id":"ark-aabbccdd","state":"read_only"}]}`)
		got := r.run(mgArgs("ark", "import"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-ARK-015]") ||
			!strings.Contains(got.errOut, "ark-3f9c0a12,") || !strings.Contains(got.errOut, "ark-aabbccdd.") || r.agent().Count("/ark/import") != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--from names one of them", func(t *testing.T) {
		r := drives(`{"drives":[{"root_id":"ark-3f9c0a12","state":"ready"},{"root_id":"ark-aabbccdd","state":"ready"}]}`)
		got := r.run(mgArgs("ark", "import", "--from", "ark-aabbccdd"))
		if got.code != 0 || !strings.Contains(r.lastBody(http.MethodPost, "/ark/import"), `"from_root":"ark-aabbccdd"`) || r.agent().Count("/ark/drives") != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no drive: not found, exit 5", func(t *testing.T) {
		r := drives(`{"drives":[{"root_id":"ark-3f9c0a12","state":"not_mounted"},{"root_id":null,"state":"unusable"}]}`)
		got := r.run(mgArgs("ark", "import"))
		if got.code != ExitNotFound || !strings.Contains(got.errOut, "No ark drive is connected.") {
			t.Fatalf("%+v", got)
		}
	})
}

// --- cancel ---------------------------------------------------------------------------------------------------------

func TestArkSyncCancel(t *testing.T) {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodDelete, "/ark/sync", 202, `{"run_id":"`+arkRunID+`","state":"cancelling"}`)
	checkRun(t, r.run(mgArgs("ark", "sync", "--cancel")), 0,
		"✓ The sync is stopping. Downloads in progress are kept and continue next time.\n", "")
	checkRun(t, r.run(mgArgs("--json", "ark", "sync", "--cancel")), 0, `{"run_id":"`+arkRunID+`","state":"cancelling"}`+"\n", "")
	checkRun(t, r.run(mgArgs("--lang", "es", "ark", "sync", "--cancel")), 0,
		"✓ La sincronización se está deteniendo. Las descargas en curso se conservan y\n  continúan la próxima vez.\n", "")
}

// --- usage -----------------------------------------------------------------------------------------------------------

func TestArkUsageErrors(t *testing.T) {
	cases := [][]string{
		{"ark", "sync", "--root", "nope"},
		{"ark", "sync", "--root", "ark-XYZ"},
		{"ark", "sync", "--item", "Not An Id"},
		{"ark", "verify", "--item", "x"},
		{"ark", "import", "--from", "/media/drive"},
		{"ark", "sync", "extra"},
		{"ark", "sync", "--root"},
	}
	for _, a := range cases {
		r := newArkRig(t)
		got := r.run(mgArgs(a...))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") || len(r.requestLines()) != 0 {
			t.Errorf("%v: %+v, requests %v", a, got, r.requestLines())
		}
	}
	// A path is fine for --root.
	r := newArkRig(t)
	if got := r.run(mgArgs("ark", "sync", "--root", "/media/person-a/AI-ARK")); got.code != 0 {
		t.Errorf("path root: %+v", got)
	}
}

func TestArkRefusals(t *testing.T) {
	for _, tc := range []struct {
		code, want string
	}{
		{"LAI-SYN-010", "Leave survival mode first"},
		{"LAI-SYN-011", "--allow-offline-mode"},
		{"LAI-SYN-005", "Wait for it to finish"},
	} {
		r := newArkRig(t)
		r.agent().RespondError(http.MethodPost, "/ark/sync", 409, tc.code, "Refused.", tc.want+".", nil)
		got := r.run(mgArgs("ark", "sync", "--wait"))
		if got.code != ExitRefused || !strings.Contains(got.errOut, "["+tc.code+"]") || !strings.Contains(got.errOut, tc.want) {
			t.Errorf("%s: %+v", tc.code, got)
		}
	}
}

// --- drives set-profile ---------------------------------------------------------------------------------------------

func TestArkDrivesSetProfile(t *testing.T) {
	t.Run("as root", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPut, "/ark/drives/ark-3f9c0a12/profile", 200, `{"root_id":"ark-3f9c0a12","profile":"ark-drive-full"}`)
		got := r.run(mgArgs("ark", "drives", "set-profile", "ark-3f9c0a12", "ark-drive-full"))
		checkRun(t, got, 0, "✓ The drive ark-3f9c0a12 now follows the profile ark-drive-full. Run\n  `aictl ark sync` with the drive connected to fill it.\n", "")
		if b := r.lastBody(http.MethodPut, "/ark/drives/ark-3f9c0a12/profile"); b != `{"profile":"ark-drive-full"}` {
			t.Errorf("body %s", b)
		}
		j := r.run(mgArgs("--json", "ark", "drives", "set-profile", "ark-3f9c0a12", "ark-drive-full"))
		checkRun(t, j, 0, `{"root_id":"ark-3f9c0a12","profile":"ark-drive-full"}`+"\n", "")
	})
	t.Run("an operator is asked for the PIN", func(t *testing.T) {
		a := newAdminAgent(t)
		a.h.agent.Handle(http.MethodPut, "/ark/drives/ark-3f9c0a12/profile", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-LocalAI-Pin-Grant") == testGrant {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"root_id":"ark-3f9c0a12","profile":"ark-drive-full"}`)
				return
			}
			fake.WriteEnvelope(w, 403, api.CodeAdminRequired, "This action needs the admin PIN.", "Enter the admin PIN.", map[string]any{"pin_required": true})
		})
		r := &mgRig{t: t, h: a.h}
		got := r.run(mgArgs("ark", "drives", "set-profile", "ark-3f9c0a12", "ark-drive-full").withTerm(testPIN))
		if got.code != 0 || a.verifies() != 1 {
			t.Fatalf("%+v verifies %d", got, a.verifies())
		}
		secretFree(t, testPIN, got.out, got.errOut)
	})
	t.Run("an unknown profile is exit 6", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().RespondError(http.MethodPut, "/ark/drives/ark-3f9c0a12/profile", 422, "LAI-CAT-003", "The profile does not exist.", "Run `aictl profile suggest`.", nil)
		if got := r.run(mgArgs("ark", "drives", "set-profile", "ark-3f9c0a12", "nope")); got.code != ExitRefused {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a malformed id is exit 2 and sends nothing", func(t *testing.T) {
		for _, a := range [][]string{{"x", "ark-drive-full"}, {"ark-3f9c0a12", "Bad Profile"}, {"ark-3f9c0a12"}} {
			r := newMgRig(t, fake.Options{})
			got := r.run(mgArgs(append([]string{"ark", "drives", "set-profile"}, a...)...))
			if got.code != ExitUsage || len(r.requestLines()) != 0 {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
}
