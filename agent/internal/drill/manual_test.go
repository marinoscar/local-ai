package drill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

var manualIDPattern = regexp.MustCompile(`^drl-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}-manual$`)

type recObserver struct{ docs []*Document }

func (o *recObserver) Observe(d *Document) { o.docs = append(o.docs, d) }

func TestRunIDPatternAcceptsTheManualForms(t *testing.T) {
	for id, want := range map[string]bool{
		"drl-20261102T150000Z-ab12":          true,
		"drl-20261102T150000Z-ab12-manual":   true,
		"drl-20261102T150000Z-manual":        true,
		"drl-20261102T150000Z-AB12-manual":   false,
		"drl-20261102T150000Z-ab12-manual-x": false,
		"drl-20261102T150000Z-ab12-":         false,
		"drl-20261102T150000Z-ab123-manual":  false,
		"../drl-20261102T150000Z-ab12":       false,
	} {
		if got := ValidRunID(id); got != want {
			t.Errorf("ValidRunID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestRecordManualStoresAManualPlugDocument(t *testing.T) {
	obs := &recObserver{}
	r := newRig(t, func(_ *rig, d *Deps) { d.Observer = obs })
	// A real run first, so latest.json exists and must not move.
	r.add(passing("drl.a", 1))
	_, run := r.startWait(StartRequest{Trigger: TriggerScheduled})
	before := r.state.block(t)
	obs.docs = nil

	at := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	rec, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass, Notes: "Unplugged the charger and the node kept answering.", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if !manualIDPattern.MatchString(rec.RunID) || !strings.HasPrefix(rec.RunID, "drl-20261031T150000Z-") || !rec.At.Equal(at) {
		t.Fatalf("record = %+v", rec)
	}
	raw, err := r.eng.Get(rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Trigger != TriggerManualPlug || doc.Overall != OverallPass || doc.State != "" || len(doc.Checks) != 0 || doc.Checks == nil ||
		doc.SurvivalReady || doc.Notes != "Unplugged the charger and the node kept answering." ||
		doc.NodeID != "ai-n3" || doc.AgentVersion != "0.4.0" || doc.Mode != "online" || doc.Restored != nil ||
		!doc.StartedAt.Equal(at) || doc.FinishedAt == nil || !doc.FinishedAt.Equal(at) || doc.SchemaVersion != 1 {
		t.Fatalf("document = %+v", doc)
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	if _, has := generic["restored"]; has {
		t.Fatal("a manual_plug entry has no restore block")
	}

	// It never moves latest.json and never writes drill.last_run_at or last_result.
	latest, err := r.eng.Latest()
	if err != nil {
		t.Fatal(err)
	}
	var lat Document
	_ = json.Unmarshal(latest, &lat)
	if lat.RunID != run.RunID {
		t.Fatalf("latest = %s, want the real run %s", lat.RunID, run.RunID)
	}
	after := r.state.block(t)
	for _, k := range []string{"last_run_at", "last_result"} {
		if string(after[k]) != string(before[k]) {
			t.Errorf("state drill.%s changed: %s -> %s", k, before[k], after[k])
		}
	}
	// The observer heard of it, and no drill event went out for it.
	if len(obs.docs) != 1 || obs.docs[0].RunID != rec.RunID {
		t.Fatalf("observer = %+v", obs.docs)
	}
	for _, e := range r.pub.events() {
		if d, ok := e.Data.(events.DrillDone); ok && d.RunID == rec.RunID {
			t.Fatal("a manual_plug entry is not a run: no drill.done")
		}
	}
}

func TestRecordManualDefaultsAtToNowAndAcceptsTheLimits(t *testing.T) {
	r := newRig(t)
	rec, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualFail})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.At.Equal(t0) || !strings.HasPrefix(rec.RunID, "drl-20261101T043512Z-") {
		t.Fatalf("record = %+v", rec)
	}
	// Notes of exactly 2000 characters (multi-byte ones count as one each) are fine; 5 minutes ahead is fine.
	if _, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass, Notes: strings.Repeat("é", 2000), At: t0.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// A time with an offset is stored in UTC, to the second.
	rec, err = r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass, At: time.Date(2026, 10, 30, 10, 0, 0, 900, time.FixedZone("x", -6*3600))})
	if err != nil || !strings.HasPrefix(rec.RunID, "drl-20261030T160000Z-") {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	// Notes with line breaks and tabs are fine.
	if _, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass, Notes: "step 1\n\tstep 2\r\n"}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordManualValidation(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name         string
		req          ManualRequest
		field, cause string
	}{
		{"no result", ManualRequest{}, "result", "invalid_result"},
		{"warn is not a manual result", ManualRequest{Result: "warn"}, "result", "invalid_result"},
		{"upper case", ManualRequest{Result: "PASS"}, "result", "invalid_result"},
		{"notes over 2000 characters", ManualRequest{Result: "pass", Notes: strings.Repeat("a", 2001)}, "notes", "notes_too_long"},
		{"notes over 2000 multi-byte characters", ManualRequest{Result: "pass", Notes: strings.Repeat("é", 2001)}, "notes", "notes_too_long"},
		{"notes with a NUL", ManualRequest{Result: "pass", Notes: "a\x00b"}, "notes", "invalid_text"},
		{"notes with an escape", ManualRequest{Result: "pass", Notes: "a\x1b[31mb"}, "notes", "invalid_text"},
		{"notes that are not UTF-8", ManualRequest{Result: "pass", Notes: "a\xffb"}, "notes", "invalid_text"},
		{"at in the future", ManualRequest{Result: "pass", At: t0.Add(5*time.Minute + time.Second)}, "at", "in_the_future"},
		{"at over a year ago", ManualRequest{Result: "pass", At: t0.Add(-367 * 24 * time.Hour)}, "at", "too_old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.eng.RecordManual(context.Background(), tc.req)
			var inv *InvalidRequestError
			if !errors.As(err, &inv) || inv.Field != tc.field || inv.Reason != tc.cause {
				t.Fatalf("error = %v, want %s/%s", err, tc.field, tc.cause)
			}
		})
	}
	if names := r.fs.listing(testDir); len(names) != 0 {
		t.Fatalf("a refused record must store nothing: %v", names)
	}
}

func TestRecordManualTwiceInTheSameSecondGetsTwoIDs(t *testing.T) {
	r := newRig(t)
	// A generator that keeps drawing the same suffix: the second record walks to the next free one.
	r.rnd.fixed = []byte{0xab, 0x12}
	a, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualFail})
	if err != nil {
		t.Fatal(err)
	}
	if a.RunID == b.RunID || !manualIDPattern.MatchString(a.RunID) || !manualIDPattern.MatchString(b.RunID) {
		t.Fatalf("ids = %s and %s", a.RunID, b.RunID)
	}
	if _, err := r.eng.Get(a.RunID); err != nil {
		t.Fatalf("the first record must still be there: %v", err)
	}
}

func TestRecordManualFailures(t *testing.T) {
	r := newRig(t)
	r.fs.fail = func(op, _ string) error {
		if op == "write" {
			return errors.New("no space left on device")
		}
		return nil
	}
	if _, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass}); err == nil || errors.As(err, new(*InvalidRequestError)) {
		t.Fatalf("a failed write is an error of its own, got %v", err)
	}
	r.fs.fail = nil

	_ = r.eng.Shutdown(context.Background())
	if _, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass}); !errors.Is(err, ErrClosing) {
		t.Fatalf("a stopping engine refuses: %v", err)
	}
}

func TestRecordManualWhileADrillRuns(t *testing.T) {
	r := newRig(t)
	g := newGate()
	r.add(g.check("drl.hang", 1))
	res, err := r.eng.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	<-g.started
	rec, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass})
	if err != nil {
		t.Fatalf("recording while a drill runs is fine: %v", err)
	}
	if rec.RunID == res.RunID {
		t.Fatal("ids collide")
	}
	close(g.release)
	r.wait()
}

func TestServiceManualMapsToTheAPI(t *testing.T) {
	r := newRig(t)
	svc := NewService(r.eng)
	ctx := context.Background()

	res, err := svc.Manual(ctx, api.DrillManualRequest{Result: "pass", Notes: "ok"})
	if err != nil || !manualIDPattern.MatchString(res.RunID) {
		t.Fatalf("Manual = %+v, %v", res, err)
	}
	raw, err := svc.Results(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	_ = json.Unmarshal(raw, &doc)
	if doc.Trigger != TriggerManualPlug || doc.Notes != "ok" {
		t.Fatalf("document = %+v", doc)
	}

	_, err = svc.Manual(ctx, api.DrillManualRequest{Result: "pass", At: t0.Add(time.Hour)})
	ae := apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)
	if ae.Details["field"] != "at" || ae.Details["reason"] != "in_the_future" {
		t.Fatalf("details = %v", ae.Details)
	}
	_, err = svc.Manual(ctx, api.DrillManualRequest{Result: "maybe"})
	ae = apiErr(t, err, http.StatusBadRequest, api.CodeInvalidRequest)
	if ae.Details["field"] != "result" {
		t.Fatalf("details = %v", ae.Details)
	}
	_ = r.eng.Shutdown(ctx)
	_, err = svc.Manual(ctx, api.DrillManualRequest{Result: "pass"})
	apiErr(t, err, http.StatusServiceUnavailable, api.CodeStarting)
}

func TestManualEntriesCountInTheRetentionLikeAnyDocument(t *testing.T) {
	r := newRig(t)
	for i := 0; i < DefaultKeep+3; i++ {
		r.clock.Advance(time.Hour)
		if _, err := r.eng.RecordManual(context.Background(), ManualRequest{Result: ManualPass}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, name := range r.fs.listing(testDir) {
		if strings.HasSuffix(name, "-manual.json") {
			n++
		}
	}
	if n != DefaultKeep {
		t.Fatalf("%d manual documents kept, want %d", n, DefaultKeep)
	}
}
