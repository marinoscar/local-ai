package adapt

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

func TestReadyRunsAnswersTheReadinessReportsRunSource(t *testing.T) {
	r := newRig(t)
	src := ReadyRuns{Engine: r.eng}
	if info, done := src.LastRuns(ark.InternalID); info != nil || !done.IsZero() {
		t.Fatalf("before any run: %+v, %v", info, done)
	}
	got, err := Starter{Engine: r.eng}.Start(bg, api.ArkSyncRequest{Trigger: api.ArkTriggerScheduled})
	if err != nil {
		t.Fatal(err)
	}
	st := r.wait(got.RunID)
	info, done := src.LastRuns(ark.InternalID)
	if info == nil || info.RunID != got.RunID || info.Trigger != run.TriggerScheduled || info.State != run.StateDone || !info.FinishedAt.Equal(*st.FinishedAt) || !done.Equal(*st.FinishedAt) {
		t.Fatalf("LastRuns = %+v, %v", info, done)
	}
	if info, _ := src.LastRuns("ark-ffffffff"); info != nil {
		t.Fatalf("a root that no run touched: %+v", info)
	}
}
