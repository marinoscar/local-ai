package checks

import (
	"context"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/drill"
)

func TestContentChecksAreTheListedOnesInOrder(t *testing.T) {
	want := []struct {
		id       string
		order    int
		critical bool
	}{
		{"drl.files.manifest", 3, true},
		{"drl.files.hash", 4, true},
		{"drl.kiwix.search", 8, true},
		{"drl.portal.firstaid", 9, false},
		{"drl.stt.en", 10, true},
		{"drl.stt.es", 10, true},
		{"drl.tts.en", 11, true},
		{"drl.tts.es", 11, true},
		{"drl.speech.roundtrip", 12, false},
		{"drl.maps.tile", 13, true},
		{"drl.maps.search", 14, false},
		{"drl.portal", 15, true},
		{"drl.docs", 16, true},
		{"drl.services", 19, false},
	}
	checks := ContentChecks(ContentDeps{})
	if len(checks) != len(want) {
		t.Fatalf("%d checks, want %d", len(checks), len(want))
	}
	for i, w := range want {
		c := checks[i]
		if c.ID != w.id || c.Order != w.order || c.Critical != w.critical {
			t.Errorf("check %d = %s order %d critical %v, want %s %d %v", i, c.ID, c.Order, c.Critical, w.id, w.order, w.critical)
		}
		if c.Run == nil || c.Area == "" {
			t.Errorf("%s has no Run or Area", c.ID)
		}
	}
}

// TestRegisterContentSelectsLikeTheEngine: the registry accepts every check, orders them, and --only
// selects them by id and by prefix (drl.stt selects both languages).
func TestRegisterContentSelectsLikeTheEngine(t *testing.T) {
	reg := drill.NewRegistry()
	if err := RegisterContent(reg, ContentDeps{}); err != nil {
		t.Fatal(err)
	}
	all, err := reg.Select(context.Background(), nil)
	if err != nil || len(all) != 14 {
		t.Fatalf("Select = %d checks, %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Order < all[i-1].Order {
			t.Errorf("%s (%d) runs after %s (%d)", all[i].ID, all[i].Order, all[i-1].ID, all[i-1].Order)
		}
	}
	got, err := reg.Select(context.Background(), []string{"drl.stt", "drl.files.hash"})
	if err != nil || len(got) != 3 {
		t.Fatalf("Select(drl.stt, drl.files.hash) = %d checks, %v", len(got), err)
	}
	if err := RegisterContent(reg, ContentDeps{}); err == nil {
		t.Error("registering twice must fail")
	}
	if err := RegisterContent(nil, ContentDeps{}); err == nil {
		t.Error("a nil registry must fail")
	}
}

// TestEveryCheckSkipsWhenItsPortsAreMissing: a node that could not build a piece still runs the rest.
func TestEveryCheckSkipsWhenItsPortsAreMissing(t *testing.T) {
	for _, c := range ContentChecks(ContentDeps{}) {
		out := c.Run(context.Background(), testEnv(false))
		if out.Result != drill.ResultSkip || out.Details["reason"] != "not_available" {
			t.Errorf("%s with no ports: %+v", c.ID, out)
		}
	}
}
