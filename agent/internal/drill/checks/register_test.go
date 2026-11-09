package checks

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func TestRegisterAddsTheModelChecksInOrder(t *testing.T) {
	n := visionNode(t)
	reg := drill.NewRegistry()
	if err := Register(reg, n.deps()); err != nil {
		t.Fatal(err)
	}
	cs, err := reg.Select(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"drl.llm.answer.text-a", "drl.llm.answer.vis-c", "drl.vlm.photo.vis-c", "drl.llm.fit", "drl.restore"}
	if got := ids(cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	// --only selects by prefix, as for every group.
	only, err := reg.Select(context.Background(), []string{"drl.llm.answer"})
	if err != nil || len(only) != 2 {
		t.Fatalf("--only drl.llm.answer = %v, %v", ids(only), err)
	}
}

func TestRegisterRefusesMissingDependencies(t *testing.T) {
	n := visionNode(t)
	for name, mutate := range map[string]func(*ModelDeps){
		"models":   func(d *ModelDeps) { d.Models = nil },
		"chat":     func(d *ModelDeps) { d.Chat = nil },
		"inputs":   func(d *ModelDeps) { d.Inputs = nil },
		"presence": func(d *ModelDeps) { d.Presence = nil },
		"photo":    func(d *ModelDeps) { d.Photo = nil },
	} {
		d := n.deps()
		mutate(&d)
		err := Register(drill.NewRegistry(), d)
		if err == nil || !strings.Contains(err.Error(), "need") {
			t.Errorf("without %s: %v", name, err)
		}
	}
}

func TestRegisterTwiceIsRefused(t *testing.T) {
	n := visionNode(t)
	reg := drill.NewRegistry()
	if err := Register(reg, n.deps()); err != nil {
		t.Fatal(err)
	}
	if err := Register(reg, n.deps()); err == nil {
		t.Fatal("a second registration must fail")
	}
}
