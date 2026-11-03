package ready_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

type stubStatus struct{}

func (stubStatus) Build(context.Context, bool, statusapi.BuildOptions) statusapi.Status {
	return statusapi.Status{}
}

type stubConfig struct{ c config.Config }

func (s stubConfig) Get() config.Config { return s.c }

// TestCloudSnapshotCarriesReadyWithoutPaths: the real readiness service behind the real snapshot
// builder. The snapshot's ark block has the READY report of every root, once, with no mount path and no
// node id inside it, and not again until READY.json changes (ark-sync.md section 4.16).
func TestCloudSnapshotCarriesReadyWithoutPaths(t *testing.T) {
	r := newRig(t, func(d *ready.Deps) { d.NodeID = "ai-n3" })
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Node.ID = "ai-n3"
	sb, err := cloud.NewSnapshotBuilder(cloud.SnapshotOptions{
		Status: stubStatus{}, Ark: r.svc.CloudSource(), Config: stubConfig{cfg},
		BootID: "5d2f6a54-0f6e-4c53-9d1e-4b6f3f2a7c10", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sb.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Snapshot.Ark.Roots) != 2 {
		t.Fatalf("roots = %+v", b.Snapshot.Ark.Roots)
	}
	body := string(b.Body)
	for _, root := range r.roots {
		if strings.Contains(body, root.Path) {
			t.Errorf("the snapshot contains the path %s", root.Path)
		}
	}
	if strings.Count(body, `"node_id"`) != 1 {
		t.Errorf("node_id appears %d times, want only the top-level one", strings.Count(body, `"node_id"`))
	}
	if strings.Contains(body, `"path"`) {
		t.Error(`a "path" key is in the snapshot`)
	}
	for _, row := range b.Snapshot.Ark.Roots {
		if row.Ready == nil || row.ReadyGeneratedAt == nil || row.Ready.GeneratedAt != *row.ReadyGeneratedAt {
			t.Fatalf("root %s: ready %+v", row.ID, row.Ready)
		}
		if len(row.Ready.Items) == 0 || row.Ready.Root.ID != row.ID {
			t.Errorf("root %s: the READY report is incomplete: %+v", row.ID, row.Ready.Root)
		}
		if row.Tiers.T1 == "" || row.Tiers.T2 == "" || row.Tiers.T3 == "" {
			t.Errorf("root %s: tiers %+v", row.ID, row.Tiers)
		}
	}

	// The cloud took it: the next snapshot says ready null until a root's READY.json changes.
	b.Commit()
	b2, err := sb.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range b2.Snapshot.Ark.Roots {
		if row.Ready != nil {
			t.Errorf("root %s sent READY again unchanged", row.ID)
		}
		if row.ReadyGeneratedAt == nil {
			t.Errorf("root %s lost ready_generated_at", row.ID)
		}
	}
	var doc struct {
		Ark struct {
			Roots []map[string]json.RawMessage `json:"roots"`
		} `json:"ark"`
	}
	if err := json.Unmarshal(b2.Body, &doc); err != nil || string(doc.Ark.Roots[0]["ready"]) != "null" {
		t.Errorf("ready on the wire: %s (%v)", doc.Ark.Roots[0]["ready"], err)
	}
}
