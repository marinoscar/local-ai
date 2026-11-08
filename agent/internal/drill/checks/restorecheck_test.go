package checks

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func TestRestoreCheck(t *testing.T) {
	tests := []struct {
		name     string
		restored *drill.Restored
		live     []string
		readErr  error
		want     drill.Result
		reason   string
	}{
		{"restored and as snapshotted", &drill.Restored{LoadedModels: []string{"a", "b"}, DefaultModel: "a", OK: true}, []string{"b", "a"}, nil, drill.ResultPass, ""},
		{"nothing was loaded before", &drill.Restored{LoadedModels: []string{}, OK: true}, nil, nil, drill.ResultPass, ""},
		{"the restore says it failed", &drill.Restored{LoadedModels: []string{"a"}, DefaultModel: "a", OK: false}, []string{"a"}, nil, drill.ResultFail, "not_restored"},
		{"a model went away after the restore", &drill.Restored{LoadedModels: []string{"a", "b"}, OK: true}, []string{"a"}, nil, drill.ResultFail, "models_changed_after_restore"},
		{"an extra model is loaded", &drill.Restored{LoadedModels: []string{"a"}, OK: true}, []string{"a", "x"}, nil, drill.ResultFail, "models_changed_after_restore"},
		{"the router cannot be read", &drill.Restored{LoadedModels: []string{"a"}, OK: true}, nil, errors.New("down"), drill.ResultFail, "cannot_read_models"},
		{"no restorer is wired", nil, nil, nil, drill.ResultSkip, "no_restorer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := newNode(t, 3, "a", []string{"a"}, model("a", 5000, 6000, "chat"))
			n.models.resident, n.models.readErr = tt.live, tt.readErr
			c := RestoreCheck(n.deps())
			if c.ID != "drl.restore" || c.Order != 22 || !c.Critical || c.Phase != drill.PhasePostRestore || c.FailCode != doctor.DRL090 {
				t.Fatalf("check = %+v", c)
			}
			out := c.Run(context.Background(), &drill.Env{Restored: tt.restored})
			if out.Result != tt.want {
				t.Fatalf("outcome = %+v", out)
			}
			if tt.want == drill.ResultFail && out.Code != doctor.DRL090 {
				t.Fatalf("a failed restore is LAI-DRL-090: %+v", out)
			}
			if tt.reason != "" && out.Details["reason"] != tt.reason {
				t.Fatalf("details = %v", out.Details)
			}
		})
	}
}
