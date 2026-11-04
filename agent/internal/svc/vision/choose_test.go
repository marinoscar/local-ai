package vision

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestChoose(t *testing.T) {
	text := Model{ID: "brain", Loadable: true}
	vlm := Model{ID: "vlm", Vision: true, Loadable: true}
	vlm2 := Model{ID: "vlm2", Vision: true, Loadable: true}
	broken := Model{ID: "vlm-nomm", Vision: true, Loadable: false}
	fitAll := func(string) bool { return true }
	fitNone := func(string) bool { return false }
	onlyFit := func(id string) FitFunc { return func(m string) bool { return m == id } }

	tests := []struct {
		name      string
		loaded    []Model
		profile   Profile
		fit       FitFunc
		modelsMax int
		want      Decision
	}{
		{"loaded vision model is used", []Model{text, vlm}, Profile{Survival: []Model{vlm2}}, fitNone, 1,
			Decision{Action: ActionUseLoaded, ModelID: "vlm"}},
		{"load alongside when it fits", []Model{text}, Profile{Survival: []Model{text, vlm}}, fitAll, 2,
			Decision{Action: ActionLoadAlongside, ModelID: "vlm"}},
		{"models_max reached asks to switch", []Model{text}, Profile{Survival: []Model{vlm}}, fitAll, 1,
			Decision{Action: ActionNeedsSwitch, ModelID: "vlm"}},
		{"fit fails asks to switch", []Model{text}, Profile{Survival: []Model{vlm}}, fitNone, 3,
			Decision{Action: ActionNeedsSwitch, ModelID: "vlm"}},
		{"nil fit asks to switch", []Model{text}, Profile{Survival: []Model{vlm}}, nil, 3,
			Decision{Action: ActionNeedsSwitch, ModelID: "vlm"}},
		{"nothing loaded, fits", nil, Profile{Survival: []Model{vlm}}, fitAll, 1,
			Decision{Action: ActionLoadAlongside, ModelID: "vlm"}},
		{"survival has no vision model: include is used", []Model{text}, Profile{Survival: []Model{text}, Include: []Model{text, vlm2}}, onlyFit("vlm2"), 2,
			Decision{Action: ActionLoadAlongside, ModelID: "vlm2"}},
		{"include fallback still subject to fit", []Model{text}, Profile{Survival: []Model{text}, Include: []Model{vlm2}}, fitNone, 2,
			Decision{Action: ActionNeedsSwitch, ModelID: "vlm2"}},
		{"survival vision not loadable, include one is the switch", []Model{text}, Profile{Survival: []Model{broken}, Include: []Model{vlm2}}, fitAll, 1,
			Decision{Action: ActionNeedsSwitch, ModelID: "vlm2"}},
		{"no vision model in the catalog", []Model{text}, Profile{Survival: []Model{text}, Include: []Model{text}}, fitAll, 2,
			Decision{Action: ActionNone, Code: doctor.SVC040}},
		{"vision model not loadable", []Model{text}, Profile{Survival: []Model{broken}}, fitAll, 2,
			Decision{Action: ActionNone, Code: doctor.SVC040}},
		{"empty everything", nil, Profile{}, nil, 0,
			Decision{Action: ActionNone, Code: doctor.SVC040}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Choose(tc.loaded, tc.profile, tc.fit, tc.modelsMax); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNeedsSwitchCarriesNoCode(t *testing.T) {
	d := Choose([]Model{{ID: "b"}}, Profile{Survival: []Model{{ID: "v", Vision: true, Loadable: true}}}, nil, 1)
	if d.Action != ActionNeedsSwitch || d.Code != "" {
		t.Fatalf("%+v", d)
	}
}
