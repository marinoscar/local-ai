package ready_test

import (
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
)

func TestReportStartHere(t *testing.T) {
	cv := "2026.11.02-1"
	size := int64(5_870_000_000)
	r := ready.Report{
		GeneratedAt: "2026-11-02T04:02:10Z", GeneratedBy: ready.GeneratedBy{NodeID: "ai-n3", AgentVersion: "0.4.0"},
		Root:    ready.RootBlock{ID: "ark-3f9c0a12", Kind: "ark_drive", Path: "/media/person-a/AI-ARK/localai", CatalogVersion: &cv},
		Overall: ready.Colour("yellow"), SurvivalCapable: true,
		LastSync: &ready.LastSync{RunID: "run-1", Trigger: "scheduled", FinishedAt: "2026-11-02T04:02:09Z", State: "done"},
		Items: []ready.Item{
			{ID: "qwen3.5-9b-q4km", Status: ready.StatusVerified, Version: "0123456789ab", Bytes: &size},
			{ID: "wikipedia-en-nopic", Status: ready.StatusSkipped},
		},
	}
	got := r.StartHere(func(id string) string {
		if id == "qwen3.5-9b-q4km" {
			return "Qwen 3.5 9B"
		}
		return ""
	})
	if got.Overall != "yellow" || !got.SurvivalCapable || got.CatalogVersion != cv || got.WrittenBy != "ai-n3" {
		t.Fatalf("%+v", got)
	}
	if !got.LastSync.Equal(time.Date(2026, 11, 2, 4, 2, 9, 0, time.UTC)) || !got.GeneratedAt.Equal(time.Date(2026, 11, 2, 4, 2, 10, 0, time.UTC)) {
		t.Errorf("times %v %v", got.LastSync, got.GeneratedAt)
	}
	if len(got.Items) != 2 || got.Items[0].Name != "Qwen 3.5 9B" || got.Items[0].Bytes != size || got.Items[0].Status != "verified" || got.Items[1].Name != "" || got.Items[1].Bytes != 0 {
		t.Errorf("items %+v", got.Items)
	}

	t.Run("a last sync that did not end done is not a sync date", func(t *testing.T) {
		r.LastSync.State = "skipped"
		if got := r.StartHere(nil); !got.LastSync.IsZero() {
			t.Errorf("last sync %v", got.LastSync)
		}
	})
	t.Run("no last sync and no catalog", func(t *testing.T) {
		r.LastSync, r.Root.CatalogVersion = nil, nil
		got := r.StartHere(nil)
		if !got.LastSync.IsZero() || got.CatalogVersion != "" || got.Items[0].Name != "" {
			t.Errorf("%+v", got)
		}
	})
}
