package checks

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// RestoreCheck is check 22, drl.restore: the state restore of drill.md section 4.3 ran before it
// (it is a post-restore check), and it verifies the result. The restore already read the node back and
// says OK; the check also looks at the node itself, so a model that went away after the restore is
// caught too. A node that is not as it was fails with LAI-DRL-090: it might be left without its
// normal brain, so the failure is red.
func RestoreCheck(d ModelDeps) drill.Check {
	return drill.Check{
		ID: "drl.restore", Area: "restore", Critical: true, Order: orderRestore, Phase: drill.PhasePostRestore,
		FailCode: doctor.DRL090,
		// The restore itself may have loaded models again; it ran before this check, not inside it.
		Run: func(ctx context.Context, env *drill.Env) drill.Outcome { return d.verifyRestore(ctx, env) },
	}
}

func (d ModelDeps) verifyRestore(ctx context.Context, env *drill.Env) drill.Outcome {
	r := env.Restored
	if r == nil {
		return drill.Outcome{Result: drill.ResultSkip, Details: map[string]any{"reason": "no_restorer"}}
	}
	details := map[string]any{"ok": r.OK, "loaded_models": join(r.LoadedModels), "default_model": r.DefaultModel}
	if !r.OK {
		details["reason"] = "not_restored"
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL090, Details: details}
	}
	live, err := d.Models.Resident(ctx)
	if err != nil {
		details["reason"] = "cannot_read_models"
		details["error"] = clip(err.Error(), 300)
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL090, Details: details}
	}
	if !equalSets(live, r.LoadedModels) {
		details["reason"] = "models_changed_after_restore"
		details["now_loaded"] = join(live)
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL090, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

func equalSets(a, b []string) bool {
	x, y := sorted(a), sorted(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
