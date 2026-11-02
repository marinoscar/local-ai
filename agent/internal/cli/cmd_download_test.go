package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Tests of `aictl download` (AICTL-05, aictl.md section 4.6). The sync run of the agent is a fake whose
// start handler publishes the run's events (they reach aictl before it knows the run id).

const dlPath = "/models/" + smallID + "/download"

type downloadRig struct {
	*mgRig
	onStart func(ev *fake.Events)
}

func newDownloadRig(t *testing.T) *downloadRig {
	t.Helper()
	r := &downloadRig{mgRig: newMgRig(t, fake.Options{})}
	ev := r.events()
	r.agent().Respond(http.MethodGet, "/models/"+smallID, 200,
		`{"id":"`+smallID+`","display_name":"Small VLM","status":"absent","size_bytes":3200000000}`)
	r.agent().Handle(http.MethodPost, dlPath, func(w http.ResponseWriter, _ *http.Request) {
		if r.onStart != nil {
			r.onStart(ev)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-LocalAI-Op-Id", arkRunID)
		w.WriteHeader(202)
		io.WriteString(w, `{"id":"`+smallID+`","sync_run_id":"`+arkRunID+`","queued":true}`)
	})
	return r
}

// downloads publishes a run that downloads the model: progress, the item verified, the run done.
func downloads(result, code, state, totals string) func(*fake.Events) {
	return func(ev *fake.Events) {
		ev.Publish(events.TypeSyncProgress, arkRunID, events.SyncProgress{RunID: arkRunID, Root: "internal", Item: smallID,
			State: "downloading", BytesDone: 1600000000, BytesTotal: 3200000000, RateBPS: 40000000})
		ev.Publish(events.TypeSyncItemDone, arkRunID, events.SyncItemDone{RunID: arkRunID, Root: "internal", Item: smallID,
			Result: result, Version: "2026-10", Code: code})
		ev.Publish(events.TypeSyncDone, arkRunID, events.SyncDone{RunID: arkRunID, State: state, Totals: json.RawMessage(totals),
			Ready: map[string]string{"internal": "green"}})
	}
}

const (
	totalsOK     = `{"items":1,"done":1,"skipped":0,"failed":0,"bytes_total":3200000000,"bytes_done":3200000000}`
	totalsFailed = `{"items":1,"done":0,"skipped":0,"failed":1,"bytes_total":3200000000,"bytes_done":1600000000}`
)

func TestDownloadStarts(t *testing.T) {
	t.Run("without --wait it ends when the download is queued", func(t *testing.T) {
		r := newDownloadRig(t)
		got := r.run(mgArgs("download", smallID))
		checkRun(t, got, 0, "✓ Download of Small VLM (3.2 GB) has started. Follow it with `aictl ark status`.\n", "")
		if b := r.lastBody(http.MethodPost, dlPath); b != `{"allow_offline_mode":false}` {
			t.Errorf("body %s", b)
		}
		if n := len(r.events().Connections()); n != 0 {
			t.Errorf("events connections = %d, want none", n)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		got := newDownloadRig(t).run(mgArgs("--lang", "es", "download", smallID))
		checkRun(t, got, 0, "✓ Empezó la descarga de Small VLM (3.2 GB). Sígala con `aictl ark status`.\n", "")
	})
	t.Run("--root (a path or a root id, repeatable) and --allow-offline-mode are in the body", func(t *testing.T) {
		r := newDownloadRig(t)
		got := r.run(mgArgs("download", smallID, "--root", "/srv/localai", "--root", "ark-0123abcd", "--allow-offline-mode"))
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		if b := r.lastBody(http.MethodPost, dlPath); b != `{"allow_offline_mode":true,"roots":["/srv/localai","ark-0123abcd"]}` {
			t.Errorf("body %s", b)
		}
	})
	t.Run("--json is the 202 body", func(t *testing.T) {
		got := newDownloadRig(t).run(mgArgs("--json", "download", smallID))
		checkRun(t, got, 0, `{"id":"`+smallID+`","sync_run_id":"`+arkRunID+`","queued":true}`+"\n", "")
	})
	t.Run("without the model's details the id is shown", func(t *testing.T) {
		r := newDownloadRig(t)
		r.agent().RespondError(http.MethodGet, "/models/"+smallID, 404, "LAI-AGT-021", "No.", "Check.", nil)
		got := r.run(mgArgs("download", smallID))
		checkRun(t, got, 0, "✓ Download of "+smallID+" has started. Follow it with `aictl ark status`.\n", "")
	})
}

func TestDownloadRefusals(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		exit   int
	}{
		{"survival mode", 409, "LAI-SYN-010", ExitRefused},
		{"offline mode without --allow-offline-mode", 409, "LAI-AGT-023", ExitRefused},
		{"a sync is already running", 409, "LAI-SYN-005", ExitRefused},
		{"unknown model", 404, "LAI-AGT-021", ExitNotFound},
		{"a bug", 500, "LAI-AGT-099", ExitFailed},
	}
	for _, tc := range cases {
		for _, wait := range []bool{false, true} {
			name := tc.name
			args := []string{"download", smallID}
			if wait {
				name += " (--wait)"
				args = append(args, "--wait")
			}
			t.Run(name, func(t *testing.T) {
				r := newDownloadRig(t)
				r.agent().RespondError(http.MethodPost, dlPath, tc.status, tc.code, "Refused.", "Fix it.", nil)
				got := r.run(mgArgs(args...))
				if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") {
					t.Fatalf("%+v", got)
				}
			})
		}
	}
	t.Run("a bad id or root is exit 2 before any request", func(t *testing.T) {
		r := newDownloadRig(t)
		for _, a := range [][]string{{"download", "Small"}, {"download", smallID, "--root", "relative/dir"}, {"download"}} {
			if got := r.run(mgArgs(a...)); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
		if len(r.requestLines()) != 0 {
			t.Errorf("requests %v", r.requestLines())
		}
	})
	t.Run("the agent is down", func(t *testing.T) {
		got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgArgs("download", smallID))
		if got.code != ExitUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

func TestDownloadWait(t *testing.T) {
	t.Run("progress, then the result", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = downloads("verified", "", "done", totalsOK)
		got := r.run(mgArgs("download", smallID, "--wait"))
		checkRun(t, got, 0, "✓ Small VLM (3.2 GB) was downloaded.\n", "… Downloading Small VLM\n")
	})
	t.Run("Spanish", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = downloads("verified", "", "done", totalsOK)
		got := r.run(mgArgs("--lang", "es", "download", smallID, "--wait"))
		checkRun(t, got, 0, "✓ Se descargó Small VLM (3.2 GB).\n", "… Descargando Small VLM\n")
	})
	t.Run("--json is the final sync.done data and stderr is empty", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = downloads("verified", "", "done", totalsOK)
		got := r.run(mgArgs("--json", "download", smallID, "--wait"))
		want := `{"run_id":"` + arkRunID + `","state":"done","totals":` + totalsOK + `,"ready":{"internal":"green"}}` + "\n"
		checkRun(t, got, 0, want, "")
	})
	t.Run("the item failed: its code and fix, exit 1", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = downloads("failed", "LAI-SYN-031", "done", totalsFailed)
		got := r.run(mgArgs("download", smallID, "--wait"))
		if got.code != ExitFailed || got.out != "" || !strings.Contains(got.errOut, "[LAI-SYN-031]") ||
			!strings.Contains(flat(got.errOut), "The download of Small VLM did not finish.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("--json with a failure keeps the document and reports only the exit code", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = downloads("failed", "LAI-SYN-031", "done", totalsFailed)
		got := r.run(mgArgs("--json", "download", smallID, "--wait"))
		if got.code != ExitFailed || got.errOut != "" || !strings.Contains(got.out, `"state":"done"`) || strings.Count(got.out, "\n") != 1 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the run was stopped", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = func(ev *fake.Events) {
			ev.Publish(events.TypeSyncDone, arkRunID, events.SyncDone{RunID: arkRunID, State: "cancelled", Totals: json.RawMessage(totalsFailed)})
		}
		got := r.run(mgArgs("download", smallID, "--wait"))
		if got.code != ExitFailed || !strings.Contains(got.out, "The download was stopped.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("another run's events are not ours", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = func(ev *fake.Events) {
			ev.Publish(events.TypeSyncDone, "run-other", events.SyncDone{RunID: "run-other", State: "failed", Totals: json.RawMessage(`{}`)})
			downloads("verified", "", "done", totalsOK)(ev)
		}
		if got := r.run(mgArgs("download", smallID, "--wait")); got.code != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a lost stream: GET /ark/sync says the run ended", func(t *testing.T) {
		r := newDownloadRig(t)
		r.agent().Respond(http.MethodGet, "/ark/sync", 200, `{"run_id":"`+arkRunID+`","state":"done","totals":`+totalsOK+`}`)
		r.onStart = func(ev *fake.Events) { go ev.Drop() }
		got := r.run(mgArgs("download", smallID, "--wait"))
		if got.code != 0 || got.out != "✓ Small VLM (3.2 GB) was downloaded.\n" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("Ctrl-C stops the waiting and sends nothing else", func(t *testing.T) {
		r := newDownloadRig(t)
		r.onStart = func(ev *fake.Events) {
			ev.Publish(events.TypeSyncProgress, arkRunID, events.SyncProgress{RunID: arkRunID, Root: "internal", Item: smallID,
				State: "downloading", BytesDone: 1, BytesTotal: 100})
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := mgArgs("download", smallID, "--wait")
		o.ctx = ctx
		ch, stderr := r.runAsync(o)
		untilTrue(t, "the progress line", func() bool { return strings.Contains(stderr.String(), "… Downloading Small VLM") })
		cancel()
		got := finish(t, ch)
		if got.code != ExitInterrupted || !strings.Contains(got.errOut, "Still running in the background") {
			t.Fatalf("%+v", got)
		}
		for _, q := range r.requestLines() {
			if q != "GET /models/"+smallID && q != "POST "+dlPath {
				t.Errorf("unexpected request %q after Ctrl-C (no cancel is ever sent)", q)
			}
		}
	})
}
