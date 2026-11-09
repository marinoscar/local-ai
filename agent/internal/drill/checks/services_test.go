package checks

import (
	"context"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

type fakeHealth struct {
	records []health.Record
	deep    []health.DeepResult
}

func (f fakeHealth) Records(context.Context) []health.Record  { return f.records }
func (f fakeHealth) Deep(context.Context) []health.DeepResult { return f.deep }

func rec(name string, st health.State, codes ...string) health.Record {
	r := health.Record{Name: name, State: st}
	for _, c := range codes {
		r.Problems = append(r.Problems, health.Problem{Code: c, Severity: doctor.Warning})
	}
	return r
}

func TestServices(t *testing.T) {
	cases := []struct {
		name   string
		h      fakeHealth
		result drill.Result
		code   string
	}{
		{"every service ok", fakeHealth{records: []health.Record{rec("kiwix", health.StateOK), rec("whisper", health.StateOK), rec("piper", health.StateOK), rec("maps", health.StateOK)},
			deep: []health.DeepResult{{Service: "kiwix"}, {Service: "maps"}}}, drill.ResultPass, ""},
		{"a service is degraded", fakeHealth{records: []health.Record{rec("kiwix", health.StateOK), rec("whisper", health.StateDegraded, doctor.SVC013)}}, drill.ResultWarn, doctor.SVC013},
		{"a service is down", fakeHealth{records: []health.Record{rec("piper", health.StateDown, doctor.SVC021)}}, drill.ResultWarn, doctor.SVC021},
		{"a deep check finds a problem", fakeHealth{records: []health.Record{rec("kiwix", health.StateOK)},
			deep: []health.DeepResult{{Service: "kiwix", Problems: []health.Problem{{Code: doctor.SVC004, Severity: doctor.Warning}}}}}, drill.ResultWarn, doctor.SVC004},
		{"disabled, without content and starting are not findings", fakeHealth{records: []health.Record{
			rec("kiwix", health.StateDisabled), rec("maps", health.StateMissingContent, doctor.SVC030), rec("piper", health.StateStarting)}}, drill.ResultPass, ""},
		{"no service known", fakeHealth{}, drill.ResultPass, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := run(t, NewServicesCheck(ServicesDeps{Health: tc.h}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
		})
	}
	c := NewServicesCheck(ServicesDeps{})
	if c.ID != "drl.services" || c.Critical || c.Order != 19 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
}
