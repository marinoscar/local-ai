package checks

import (
	"errors"
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func visionNode(t *testing.T) *node {
	t.Helper()
	return newNode(t, 1, "text-a", []string{"text-a", "vis-c"},
		model("text-a", 5000, 6000, "chat"), model("vis-c", 5200, 6000, "chat", "vision"))
}

func photoCheck(t *testing.T, n *node) drill.Check {
	t.Helper()
	return find(t, expand(t, VLMPhotoChecks(n.deps())), "drl.vlm.photo.vis-c")
}

func TestVLMPhotoAnswers(t *testing.T) {
	tests := []struct {
		name   string
		answer answer
		want   drill.Result
		code   string
		reason string
	}{
		{"the digit", answer{text: "7"}, drill.ResultPass, "", ""},
		{"a sentence with the digit", answer{text: "The digit is 7."}, drill.ResultPass, "", ""},
		{"seven in words only", answer{text: "Seven"}, drill.ResultWarn, doctor.DRL023, "digit_in_words"},
		{"seven in a sentence", answer{text: "It is the number seven."}, drill.ResultWarn, doctor.DRL023, "digit_in_words"},
		{"siete", answer{text: "siete"}, drill.ResultWarn, doctor.DRL023, "digit_in_words"},
		{"a wrong digit", answer{text: "5"}, drill.ResultFail, doctor.DRL023, "wrong_answer"},
		{"words that only contain seven", answer{text: "seventeen"}, drill.ResultFail, doctor.DRL023, "wrong_answer"},
		{"an empty answer", answer{text: "  "}, drill.ResultFail, doctor.DRL023, "no_answer"},
		{"an error", answer{err: errors.New("image not supported")}, drill.ResultFail, doctor.DRL023, "no_answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := visionNode(t)
			n.chat.seq = []answer{tt.answer}
			out := runChk(t, photoCheck(t, n), nil)
			if out.Result != tt.want || out.Code != tt.code {
				t.Fatalf("outcome = %+v", out)
			}
			if tt.reason != "" && out.Details["reason"] != tt.reason {
				t.Fatalf("details = %v", out.Details)
			}
		})
	}
}

func TestVLMPhotoSendsTheImageAndTheQuestion(t *testing.T) {
	n := visionNode(t)
	n.models.loadInfo = drill.LoadInfo{LoadMS: 9000}
	n.chat.seq = []answer{{text: "7"}}
	out := runChk(t, photoCheck(t, n), nil)
	if out.Details["load_ms"] != int64(9000) || out.Details["answer"] != "7" {
		t.Fatalf("details = %v", out.Details)
	}
	rq := n.chat.requests()
	if len(rq) != 1 || rq[0].Model != "vis-c" || rq[0].Prompt != "What digit is written in this image? Answer with the digit only." ||
		string(rq[0].Image) != "png-bytes" {
		t.Fatalf("request = %+v", rq)
	}
	if got := n.models.loaded(); !reflect.DeepEqual(got, []string{"vis-c"}) {
		t.Fatalf("loads = %v", got)
	}
}

func TestVLMPhotoLoadFailureIs020(t *testing.T) {
	n := visionNode(t)
	n.models.failLoad["vis-c"] = &drill.ModelError{Code: "LAI-LLM-012", Message: "did not start"}
	out := runChk(t, photoCheck(t, n), nil)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL020 || len(n.chat.requests()) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestVLMPhotoOnlyForVisionModelsOfTheSurvivalList(t *testing.T) {
	n := visionNode(t)
	cs := expand(t, VLMPhotoChecks(n.deps()))
	if got := ids(cs); !reflect.DeepEqual(got, []string{"drl.vlm.photo.vis-c"}) {
		t.Fatalf("checks = %v", got)
	}
	c := cs[0]
	if c.Order != 6 || !c.Critical || c.Area != "vision" || c.FailCode != doctor.DRL023 {
		t.Fatalf("check = %+v", c)
	}
	if !c.SkipInQuick {
		t.Fatal("the survival default is text-a, so --quick skips the photo test")
	}
}

func TestVLMPhotoQuickRunsTheVisionDefault(t *testing.T) {
	n := visionNode(t)
	n.in.Profile.Survival.DefaultModel = "vis-c"
	if c := photoCheck(t, n); c.SkipInQuick {
		t.Fatal("a vision default is tested in --quick")
	}
}

func TestVLMPhotoReasoningModelHasThinkingDisabled(t *testing.T) {
	m := model("vis-c", 5200, 6000, "chat", "vision")
	m.ClientHints = &catalog.ClientHints{Reasoning: true}
	n := newNode(t, 1, "vis-c", []string{"vis-c"}, m)
	n.chat.seq = []answer{{text: "7"}}
	runChk(t, photoCheck(t, n), nil)
	if rq := n.chat.requests(); len(rq) != 1 || !rq[0].DisableThinking {
		t.Fatalf("requests = %+v", rq)
	}
}

func TestVLMPhotoWithoutAVisionModelIsOneSkippedCheck(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a"}, model("text-a", 5000, 6000, "chat"))
	cs := expand(t, VLMPhotoChecks(n.deps()))
	if got := ids(cs); !reflect.DeepEqual(got, []string{"drl.vlm.photo"}) {
		t.Fatalf("checks = %v", got)
	}
	out := runChk(t, cs[0], nil)
	if out.Result != drill.ResultSkip || out.Details["reason"] != "no_vision_model" || cs[0].Critical {
		t.Fatalf("outcome = %+v critical %v", out, cs[0].Critical)
	}
}

func TestVLMPhotoSkipsAVisionModelThatCannotLoad(t *testing.T) {
	n := newNode(t, 1, "text-a", []string{"text-a", "vis-big"},
		model("text-a", 5000, 6000, "chat"), model("vis-big", 30000, 6000, "chat", "vision"))
	if got := ids(expand(t, VLMPhotoChecks(n.deps()))); !reflect.DeepEqual(got, []string{"drl.vlm.photo"}) {
		t.Fatalf("checks = %v: a vision model that does not fit is check 7's to report", got)
	}
}
