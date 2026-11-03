package events

import (
	"encoding/json"
	"testing"
)

func TestServiceStateUnitIsNullWithoutAUnit(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ServiceState
		want string
	}{
		{"unit", ServiceState{Name: "kiwix", Unit: "localai-kiwix.service", From: "ok", To: "down", Active: "failed", Sub: "failed", Code: "LAI-SVC-002"},
			`{"name":"kiwix","unit":"localai-kiwix.service","from":"ok","to":"down","active":"failed","sub":"failed","healthy":false,"code":"LAI-SVC-002"}`},
		{"no unit", ServiceState{Name: "piper", From: "starting", To: "ok", Healthy: true},
			`{"name":"piper","from":"starting","to":"ok","active":"","sub":"","healthy":true,"unit":null}`},
	} {
		raw, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		// Compare as objects: the order of the keys is not part of the contract.
		var got, want map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Errorf("%s: %s, want %s", tc.name, raw, tc.want)
			continue
		}
		for k, v := range want {
			if gv, ok := got[k]; !ok || gv != v {
				t.Errorf("%s: key %q is %v in %s, want %v", tc.name, k, gv, raw, v)
			}
		}
	}
}

func TestServiceStatePointerAndBusRoundTrip(t *testing.T) {
	b := New(Options{})
	defer b.Close()
	for _, v := range []any{ServiceState{Name: "maps", To: "ok"}, &ServiceState{Name: "kiwix", Unit: "localai-kiwix.service"}} {
		if err := b.Publish(TypeServiceState, "", v); err != nil {
			t.Errorf("publish %T: %v", v, err)
		}
	}
}
