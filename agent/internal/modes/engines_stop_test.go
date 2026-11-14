package modes_test

import (
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
)

// engines_stop (E11, agent.md section 4.2.1): the music engine stops before the survival models load,
// and an engine that does not stop is a warning, never a survival failure.
func TestEnginesStopRunsBeforeTheSurvivalModels(t *testing.T) {
	r := newRig(t, withMode(modes.Online))
	r.env.Models.StopWarns = []modes.Warning{{Code: doctor.LLM042, Message: "The acestep engine did not stop cleanly."}}
	res := r.mustRequest(modes.Survival, true)
	if !res.Done || res.Target != modes.Survival {
		t.Fatalf("result = %+v", res)
	}
	if !hasCode(res.Warnings, doctor.LLM042) {
		t.Errorf("warnings %v lack %s", codesOf(res.Warnings), doctor.LLM042)
	}
	calls := strings.Join(r.env.Rec.Calls(), ",")
	stop, load := strings.Index(calls, "engines.stop"), strings.Index(calls, "models.load")
	if stop < 0 || load < 0 || stop > load {
		t.Errorf("engines.stop must come before models.load: %s", calls)
	}
}
