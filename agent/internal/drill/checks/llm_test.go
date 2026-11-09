package checks

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func oneModelNode(t *testing.T, backend string) *node {
	t.Helper()
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat"))
	n.in.Fit.Backend = backend
	if backend == "cpu" {
		n.in.Fit.GTotalMiB = 0
	}
	return n
}

func answerCheck(t *testing.T, n *node, id string) drill.Check {
	t.Helper()
	return find(t, expand(t, LLMAnswerChecks(n.deps())), "drl.llm.answer."+id)
}

func TestLLMAnswerPass(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.models.loadInfo = drill.LoadInfo{LoadMS: 4200}
	n.chat.seq = []answer{{text: "READY", tps: 31.44}}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultPass || out.Code != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["model"] != "text-a" || out.Details["load_ms"] != int64(4200) || out.Details["tokens_per_s"] != 31.4 || out.Details["answer"] != "READY" {
		t.Fatalf("details = %v", out.Details)
	}
	if got := n.models.loaded(); !reflect.DeepEqual(got, []string{"text-a"}) || !n.models.evicts[0] {
		t.Fatalf("loads = %v evicts %v: the load goes through the orchestrator, with eviction", got, n.models.evicts)
	}
	rq := n.chat.requests()
	if len(rq) != 1 || rq[0].Prompt != "Reply with exactly one word: READY" || rq[0].MaxTokens != 16 || rq[0].Model != "text-a" || rq[0].DisableThinking {
		t.Fatalf("request = %+v", rq)
	}
}

func TestLLMAnswerIsCaseInsensitive(t *testing.T) {
	for _, text := range []string{"READY", "ready", "Ready.", "  rEaDy\n", "I am ready"} {
		n := oneModelNode(t, "cuda")
		n.chat.seq = []answer{{text: text, tps: 20}}
		if out := runChk(t, answerCheck(t, n, "text-a"), nil); out.Result != drill.ResultPass {
			t.Errorf("answer %q: %+v", text, out)
		}
	}
}

func TestLLMAnswerSpeedFloor(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		tps     float64
		want    drill.Result
		code    string
	}{
		{"cpu-only above the 2 tok/s floor", "cpu", 2.5, drill.ResultPass, ""},
		{"cpu-only exactly at the floor", "cpu", 2, drill.ResultPass, ""},
		{"cpu-only below the floor", "cpu", 1.5, drill.ResultWarn, doctor.DRL021},
		{"gpu at 5 tok/s is slow", "cuda", 5, drill.ResultWarn, doctor.DRL021},
		{"vulkan at 5 tok/s is slow", "vulkan", 5, drill.ResultWarn, doctor.DRL021},
		{"gpu exactly at the floor", "cuda", 8, drill.ResultPass, ""},
		{"gpu fast", "cuda", 40, drill.ResultPass, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := oneModelNode(t, tt.backend)
			n.chat.seq = []answer{{text: "READY", tps: tt.tps}}
			out := runChk(t, answerCheck(t, n, "text-a"), nil)
			if out.Result != tt.want || out.Code != tt.code {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestSpeedFloorByBackend(t *testing.T) {
	if SpeedFloor("cpu") != 2 || SpeedFloor("") != 2 || SpeedFloor("cuda") != 8 || SpeedFloor("vulkan") != 8 {
		t.Fatal("floors: cpu-only 2 tok/s, any GPU 8 tok/s")
	}
}

func TestLLMAnswerWithoutTimingsCannotJudgeSpeed(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.chat.seq = []answer{{text: "READY"}}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultPass || out.Details["speed_known"] != false {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestLLMAnswerRetriesOnceThenPasses(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.chat.seq = []answer{{text: "Hello!", tps: 20}, {text: "READY", tps: 20}}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultPass || out.Details["attempts"] != 2 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestLLMAnswerWrongTwiceFailsWith022(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.chat.seq = []answer{{text: "Hello!", tps: 20}, {text: "Sure, anything else?", tps: 20}}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL022 || out.Details["attempts"] != 2 || out.Details["reason"] != "wrong_answer" {
		t.Fatalf("outcome = %+v", out)
	}
	if got := len(n.chat.requests()); got != 2 {
		t.Fatalf("a wrong answer is asked exactly twice, got %d", got)
	}
	if out.Details["answer"] != "Sure, anything else?" {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestLLMAnswerNoAnswerTwiceFailsWith022(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.chat.seq = []answer{{err: errors.New("router down")}}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL022 || out.Details["reason"] != "no_answer" || out.Details["error"] == nil {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestLLMAnswerLoadFailureIs020AndNothingIsAsked(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.models.failLoad["text-a"] = &drill.ModelError{Code: "LAI-LLM-012", Message: "the model did not start"}
	out := runChk(t, answerCheck(t, n, "text-a"), nil)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL020 || out.Details["load_code"] != "LAI-LLM-012" || out.Details["reason"] != "load_failed" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(n.chat.requests()) != 0 {
		t.Fatal("a model that did not load is not asked anything")
	}
}

func TestLLMAnswerLoadFailureDoesNotStopTheNextModel(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a", "text-b"}, model("text-a", 5000, 6000, "chat"), model("text-b", 3000, 4000, "chat"))
	n.models.failLoad["text-a"] = errors.New("out of memory")
	n.chat.seq = []answer{{text: "READY", tps: 20}}
	cs := expand(t, LLMAnswerChecks(n.deps()))
	a := runChk(t, cs[0], nil)
	b := runChk(t, cs[1], nil)
	if a.Result != drill.ResultFail || a.Code != doctor.DRL020 || b.Result != drill.ResultPass {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
}

func TestLLMAnswerDisablesThinkingForReasoningModels(t *testing.T) {
	m := model("think-a", 5000, 6000, "reasoning")
	m.ClientHints = &catalog.ClientHints{Reasoning: true}
	n := newNode(t, 1, "think-a", []string{"think-a"}, m)
	n.chat.seq = []answer{{text: "READY", tps: 20}}
	runChk(t, answerCheck(t, n, "think-a"), nil)
	if rq := n.chat.requests(); len(rq) != 1 || !rq[0].DisableThinking {
		t.Fatalf("requests = %+v", rq)
	}
}

func TestLLMAnswerExpanderOneCheckPerLoadableModel(t *testing.T) {
	big := model("big-d", 20000, 6000, "chat") // does not fit an 8 GB card
	n := newNode(t, 1, "text-a", []string{"text-a", "big-d", "text-a", "vis-c", "gone-e"},
		model("text-a", 5000, 6000, "chat"), big, model("vis-c", 5200, 6000, "chat", "vision"))
	n.in.Profile.Survival.Models = []string{"text-a", "big-d", "text-a", "vis-c", "gone-e"}
	cs := expand(t, LLMAnswerChecks(n.deps()))
	if got, want := ids(cs), []string{"drl.llm.answer.text-a", "drl.llm.answer.vis-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %v, want %v (not loadable and unknown models are check 7's, duplicates count once)", got, want)
	}
	for _, c := range cs {
		if c.Order != 5 || !c.Critical || c.Area != "llm" || c.FailCode != doctor.DRL020 {
			t.Errorf("check %s = %+v", c.ID, c)
		}
		if c.Timeout < 300*time.Second+120*time.Second {
			t.Errorf("timeout %v must cover a load and a completion", c.Timeout)
		}
	}
}

func TestLLMAnswerQuickRunsOnlyTheDefault(t *testing.T) {
	n := newNode(t, 1, "text-b", []string{"text-a", "text-b"}, model("text-a", 5000, 6000, "chat"), model("text-b", 3000, 4000, "chat"))
	cs := expand(t, LLMAnswerChecks(n.deps()))
	a, b := find(t, cs, "drl.llm.answer.text-a"), find(t, cs, "drl.llm.answer.text-b")
	if !a.SkipInQuick || b.SkipInQuick {
		t.Fatalf("--quick must skip everything but the default: a.SkipInQuick=%v b.SkipInQuick=%v", a.SkipInQuick, b.SkipInQuick)
	}
}

func TestLLMAnswerLoadTimeoutFollowsTheNodeConfig(t *testing.T) {
	n := oneModelNode(t, "cuda")
	n.in.Llama.LoadTimeoutSeconds = 600
	c := answerCheck(t, n, "text-a")
	if c.Timeout < 2*600*time.Second {
		t.Fatalf("timeout %v must cover the configured load timeout, doubled for a drive", c.Timeout)
	}
}
