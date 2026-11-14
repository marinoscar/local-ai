package catalog

import (
	"reflect"
	"testing"
)

func TestEffectiveWorkloads(t *testing.T) {
	p := &Profile{Workloads: map[string]Workload{
		"coding": {Label: WorkloadLabel{EN: "Coding", ES: "Programación"}, Models: []string{"a"}},
		"chat":   {Label: WorkloadLabel{EN: "Chat", ES: "Charla"}, Models: []string{"b"}},
	}}
	node := map[string]Workload{
		"chat":  {Label: WorkloadLabel{EN: "Talk", ES: "Hablar"}, Models: []string{"c"}},
		"music": {Label: WorkloadLabel{EN: "Music", ES: "Música"}, Models: []string{"m"}},
	}
	got := EffectiveWorkloads(p, node)
	if names := WorkloadNames(got); !reflect.DeepEqual(names, []string{"chat", "coding", "music"}) {
		t.Fatalf("names = %v", names)
	}
	if got["chat"].Label.EN != "Talk" || got["chat"].Models[0] != "c" {
		t.Errorf("node.yaml must replace by name: %+v", got["chat"])
	}
	got["coding"].Models[0] = "x"
	if p.Workloads["coding"].Models[0] != "a" {
		t.Error("the result shares the profile's model list")
	}
	if len(EffectiveWorkloads(nil, nil)) != 0 {
		t.Error("nil profile and node give no workloads")
	}
}
