package vision

import (
	"context"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakeSource []Entry

func (f fakeSource) VisionModels(context.Context) []Entry { return f }

func TestEvaluate(t *testing.T) {
	good := Entry{ID: "good", Ctx: 8192, WeightsPresent: true, MmprojPresent: true, Loadable: true}
	tests := []struct {
		name    string
		entries []Entry
		want    []string // code:model
	}{
		{"healthy", []Entry{good}, nil},
		{"no vision model at all", nil, []string{doctor.SVC040 + ":"}},
		{"not downloaded", []Entry{{ID: "vlm", Ctx: 8192}}, []string{doctor.SVC040 + ":"}},
		{"missing mmproj", []Entry{{ID: "vlm", Ctx: 16384, WeightsPresent: true}},
			[]string{doctor.SVC041 + ":vlm", doctor.SVC040 + ":"}},
		{"one good one broken", []Entry{good, {ID: "vlm2", Ctx: 8192, WeightsPresent: true}},
			[]string{doctor.SVC041 + ":vlm2"}},
		{"ctx below 8192 warns", []Entry{{ID: "small", Ctx: 4096, WeightsPresent: true, MmprojPresent: true, Loadable: true}},
			[]string{doctor.CAT013 + ":small"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range Evaluate(tc.entries) {
				m, _ := r.Details["model"].(string)
				got = append(got, r.Code+":"+m)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSeverities(t *testing.T) {
	rs := Evaluate([]Entry{{ID: "v", Ctx: 2048, WeightsPresent: true}})
	sev := map[string]doctor.Severity{}
	for _, r := range rs {
		sev[r.Code] = r.Severity
	}
	if sev[doctor.SVC041] != doctor.Error || sev[doctor.SVC040] != doctor.Warning || sev[doctor.CAT013] != doctor.Warning {
		t.Fatalf("%v", sev)
	}
}

func TestRegister(t *testing.T) {
	reg := doctor.NewRegistry()
	if err := Register(reg, fakeSource{{ID: "v", Ctx: 8192, WeightsPresent: true}}); err != nil {
		t.Fatal(err)
	}
	var found *doctor.Check
	for _, c := range reg.Checks() {
		if c.ID == CheckID {
			c := c
			found = &c
		}
	}
	if found == nil {
		t.Fatal("check not registered")
	}
	res := found.Run(context.Background())
	if len(res) == 0 || res[0].Code != doctor.SVC041 {
		t.Fatalf("%+v", res)
	}
	if err := Register(reg, fakeSource{}); err == nil {
		t.Fatal("second registration should fail")
	}
}

func TestFromCatalog(t *testing.T) {
	cat := catalog.New("t", []catalog.Model{
		{ID: "brain", Roles: []string{"chat"}},
		{ID: "vlm", Roles: []string{"chat", "vision"}},
	}, nil, nil)
	p := &catalog.Profile{}
	p.Survival.Models = []string{"brain", "vlm"}
	p.Include.Models = []string{"vlm", "gone"}
	got := FromCatalog(cat, p, func(m *catalog.Model) bool { return m.ID == "vlm" })
	if len(got.Survival) != 2 || got.Survival[0].Vision || !got.Survival[1].Vision || !got.Survival[1].Loadable {
		t.Fatalf("%+v", got)
	}
	if len(got.Include) != 1 {
		t.Fatalf("%+v", got.Include)
	}
	if got := FromCatalog(cat, nil, nil); len(got.Survival)+len(got.Include) != 0 {
		t.Fatalf("%+v", got)
	}
}
