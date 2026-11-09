package checks

import (
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func fitOutcome(t *testing.T, n *node) drill.Outcome {
	t.Helper()
	return runChk(t, FitCheck(n.deps()), nil)
}

func TestFitAllModelsFit(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a", "vis-c"},
		model("text-a", 5000, 6000, "chat"), model("vis-c", 5200, 6000, "chat", "vision"))
	out := fitOutcome(t, n)
	if out.Result != drill.ResultPass || out.Code != "" || out.Details["fit"] != "text-a, vis-c" || out.Details["backend"] != "cuda" {
		t.Fatalf("outcome = %+v", out)
	}
	if _, has := out.Details["not_loadable"]; has {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestFitFallbackModelWouldBeUsed(t *testing.T) {
	// The preferred model is too big for the card; models_max 1, so the second one stands in.
	n := newNode(t, 1, "text-b", []string{"big-d", "text-b"},
		model("big-d", 30000, 6000, "chat"), model("text-b", 3000, 4000, "chat"))
	out := fitOutcome(t, n)
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL024 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["fallback_used"] != true || out.Details["fit"] != "text-b" || !strings.HasPrefix(out.Details["not_loadable"].(string), "big-d (LAI-") {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestFitUnreachedModelThatDoesNotFitIsNoFallback(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a", "big-d"},
		model("text-a", 5000, 6000, "chat"), model("big-d", 30000, 6000, "chat"))
	out := fitOutcome(t, n)
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL024 || out.Details["fallback_used"] != false {
		t.Fatalf("outcome = %+v: the listed model is still reported, but no fallback is reached", out)
	}
}

func TestFitNothingFits(t *testing.T) {
	n := newNode(t, 1, "big-d", []string{"big-d", "big-e"},
		model("big-d", 30000, 6000, "chat"), model("big-e", 40000, 6000, "chat"))
	out := fitOutcome(t, n)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL024 || out.Details["reason"] != "nothing_fits" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFitFilesMissingMeansItDoesNotFit(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat"))
	n.pres["text-a"] = false
	out := fitOutcome(t, n)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL024 || !strings.Contains(out.Details["not_loadable"].(string), "LAI-LLM-008") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFitWithoutASurvivalList(t *testing.T) {
	n := newNode(t, 1, "text-a", nil, model("text-a", 5000, 6000, "chat"))
	out := fitOutcome(t, n)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL024 || out.Details["reason"] != "no_survival_models" {
		t.Fatalf("outcome = %+v", out)
	}
	n.in.Profile = nil
	if out := fitOutcome(t, n); out.Result != drill.ResultFail || out.Code != doctor.DRL024 {
		t.Fatalf("a node without a profile: %+v", out)
	}
}

func TestFitNotesATightModelButStillPasses(t *testing.T) {
	// Fits the static rules (5000 + 512 <= 8192) but other programs hold 3000 MiB of the card.
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat"))
	n.in.Fit.GOthersMiB = 3000
	out := fitOutcome(t, n)
	if out.Result != drill.ResultPass || out.Details["tight"] != "text-a" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFitCreditsTheModelsLoadedNow(t *testing.T) {
	// A cpu-only node: 7000 MiB are needed and 6000 are available (2048 stay reserved), so text-a is
	// tight; with text-b loaded, unloading it first credits its 4000 MiB and text-a is not tight.
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 0, 7000, "chat"), model("text-b", 0, 4000, "chat"))
	n.in.Fit.Backend, n.in.Fit.GTotalMiB = "cpu", 0
	n.in.Fit.RAvailMiB = 6000
	n.in.Fit.RTotalMiB = 16384
	if out := fitOutcome(t, n); out.Details["tight"] != "text-a" {
		t.Fatalf("with nothing to credit the model is tight: %+v", out)
	}
	n.models.resident = []string{"text-b"}
	out := fitOutcome(t, n)
	if out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFitCheckIdentity(t *testing.T) {
	c := FitCheck(newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat")).deps())
	if c.ID != "drl.llm.fit" || c.Order != 7 || c.Critical || c.FailCode != doctor.DRL024 {
		t.Fatalf("check = %+v", c)
	}
}

func TestFitSurvivesAnUnreadableModelList(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat"))
	n.models.readErr = errors.New("router down")
	if out := fitOutcome(t, n); out.Result != drill.ResultPass {
		t.Fatalf("the fit rules need no router: %+v", out)
	}
}
