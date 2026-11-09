package checks

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// photoPrompt is the question of check 6 (drill.md section 4.2).
const photoPrompt = "What digit is written in this image? Answer with the digit only."

// photoTokens is max_tokens of check 6.
const photoTokens = 16

// sevenWord matches the digit written in words, in English or Spanish.
var sevenWord = regexp.MustCompile(`(?i)\b(seven|siete)\b`)

// VLMPhotoChecks is the expander of check 6: one drl.vlm.photo.<id> per vision model of
// profile.survival.models that can load on this node. With none, a single drl.vlm.photo is recorded
// as skipped (details.reason no_vision_model) so the document says the photo test did not apply.
// In a --quick run only the survival default is tested.
func VLMPhotoChecks(d ModelDeps) drill.Expander {
	return func(context.Context) ([]drill.Check, error) {
		in := d.Inputs()
		def := defaultModel(in)
		var out []drill.Check
		for _, sm := range survivalModels(in, d.Presence) {
			if !sm.Vision || !sm.Loadable {
				continue
			}
			sm := sm
			out = append(out, drill.Check{
				ID: "drl.vlm.photo." + sm.ID, Area: "vision", Critical: true, Order: orderPhoto,
				Timeout:     loadTimeout(in) + CompletionTimeout + 30*time.Second,
				FailCode:    doctor.DRL023,
				SkipInQuick: sm.ID != def,
				Run:         func(ctx context.Context, env *drill.Env) drill.Outcome { return d.photo(ctx, sm) },
			})
		}
		if len(out) == 0 {
			out = append(out, drill.Check{
				ID: "drl.vlm.photo", Area: "vision", Order: orderPhoto,
				Run: func(context.Context, *drill.Env) drill.Outcome {
					return drill.Outcome{Result: drill.ResultSkip, Details: map[string]any{"reason": "no_vision_model"}}
				},
			})
		}
		return out, nil
	}
}

// photo is check 6 for one model: with the model loaded, show it seven.png and ask which digit it is.
func (d ModelDeps) photo(ctx context.Context, sm survivalModel) drill.Outcome {
	details := map[string]any{"model": sm.ID}
	info, err := d.Models.Load(ctx, sm.ID, true)
	if err != nil {
		// The brain did not start: that is check 5's failure too, with its own code.
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL020, Details: loadFailureDetails(sm.ID, err)}
	}
	details["load_ms"] = info.LoadMS

	ans, err := d.ask(ctx, router.CompletionRequest{
		Model: sm.ID, Prompt: photoPrompt, Image: d.Photo, MaxTokens: photoTokens, DisableThinking: sm.Reasoning,
	})
	details["answer"] = clip(ans.Text, 120)
	switch {
	case err != nil:
		details["reason"] = "no_answer"
		details["error"] = clip(err.Error(), 300)
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL023, Details: details}
	case strings.Contains(ans.Text, "7"):
		return drill.Outcome{Result: drill.ResultPass, Details: details}
	case sevenWord.MatchString(ans.Text):
		details["reason"] = "digit_in_words"
		return drill.Outcome{Result: drill.ResultWarn, Code: doctor.DRL023, Details: details}
	case strings.TrimSpace(ans.Text) == "":
		details["reason"] = "no_answer"
	default:
		details["reason"] = "wrong_answer"
	}
	return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL023, Details: details}
}
