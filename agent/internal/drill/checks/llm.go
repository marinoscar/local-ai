package checks

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// answerPrompt is the prompt of check 5 (drill.md section 4.2).
const answerPrompt = "Reply with exactly one word: READY"

// answerTokens is max_tokens of check 5.
const answerTokens = 16

// SpeedFloor is the generation speed check 5 expects: 2 tokens per second on a cpu-only node, 8 on
// one with a GPU (drill.md section 4.2).
func SpeedFloor(backend string) float64 {
	if backend == "" || backend == "cpu" {
		return FloorCPU
	}
	return FloorGPU
}

// LLMAnswerChecks is the expander of check 5: one drl.llm.answer.<id> per loadable model of
// profile.survival.models, in the profile's order. A model that cannot load on this node is not a
// check; check 7 reports it. Only the survival default runs in a --quick run: the others are
// recorded as skipped, which does not count against survival_ready.
func LLMAnswerChecks(d ModelDeps) drill.Expander {
	return func(context.Context) ([]drill.Check, error) {
		in := d.Inputs()
		def := defaultModel(in)
		var out []drill.Check
		for _, sm := range survivalModels(in, d.Presence) {
			if !sm.Loadable {
				continue
			}
			sm := sm
			out = append(out, drill.Check{
				ID: "drl.llm.answer." + sm.ID, Area: "llm", Critical: true, Order: orderAnswer,
				// One load, then up to two completions.
				Timeout:     loadTimeout(in) + 2*CompletionTimeout + 30*time.Second,
				FailCode:    doctor.DRL020,
				SkipInQuick: sm.ID != def,
				Run:         func(ctx context.Context, env *drill.Env) drill.Outcome { return d.answer(ctx, sm) },
			})
		}
		return out, nil
	}
}

// answer is check 5 for one model: load it, ask for READY, judge the answer and the speed.
func (d ModelDeps) answer(ctx context.Context, sm survivalModel) drill.Outcome {
	in := d.Inputs()
	details := map[string]any{"model": sm.ID}

	info, err := d.Models.Load(ctx, sm.ID, true)
	if err != nil {
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL020, Details: loadFailureDetails(sm.ID, err)}
	}
	details["load_ms"] = info.LoadMS
	if info.AlreadyLoaded {
		details["already_loaded"] = true
	}

	// A wrong answer is asked again once; a second one is the failure (drill.md implementation note).
	var last router.Completion
	var lastErr error
	ok := false
	attempts := 0
	for attempts < 2 && !ok {
		attempts++
		last, lastErr = d.ask(ctx, router.CompletionRequest{
			Model: sm.ID, Prompt: answerPrompt, MaxTokens: answerTokens, DisableThinking: sm.Reasoning,
		})
		if ctx.Err() != nil {
			break
		}
		ok = lastErr == nil && containsFold(last.Text, "READY")
	}
	details["attempts"] = attempts
	details["answer"] = clip(last.Text, 120)
	if !ok {
		details["reason"] = "wrong_answer"
		if lastErr != nil {
			details["reason"] = "no_answer"
			details["error"] = clip(lastErr.Error(), 300)
		}
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL022, Details: details}
	}

	floor := SpeedFloor(in.Fit.Backend)
	details["floor_tokens_per_s"] = floor
	if !last.HasTimings {
		// The server sent no timings: the answer is right and the speed cannot be judged.
		details["speed_known"] = false
		return drill.Outcome{Result: drill.ResultPass, Details: details}
	}
	details["speed_known"] = true
	details["tokens_per_s"] = round1(last.TokensPerSecond)
	if last.TokensPerSecond < floor {
		return drill.Outcome{Result: drill.ResultWarn, Code: doctor.DRL021, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

// ask sends one completion under the completion timeout.
func (d ModelDeps) ask(ctx context.Context, req router.CompletionRequest) (router.Completion, error) {
	cctx, cancel := context.WithTimeout(ctx, CompletionTimeout)
	defer cancel()
	req.Timeout = CompletionTimeout
	return d.Chat.Complete(cctx, req)
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToUpper(s), strings.ToUpper(sub))
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
