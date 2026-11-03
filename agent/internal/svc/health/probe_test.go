package health_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// fakeProber records the probes it is asked to run and answers err.
type fakeProber struct {
	probes []services.Probe
	err    error
}

func (f *fakeProber) Check(_ context.Context, p services.Probe) error {
	f.probes = append(f.probes, p)
	return f.err
}

func TestHTTPLivenessProbes(t *testing.T) {
	ports := services.Ports{Llama: 8080, Kiwix: 8888, Whisper: 8081}
	for _, tc := range []struct {
		name  string
		probe func(services.Prober, services.Ports) *health.FuncProbe
		want  services.Probe
	}{
		{"kiwix", health.KiwixProbe, services.Probe{Service: "kiwix", URL: "http://127.0.0.1:8888/library/catalog/v2/entries?count=1", Timeout: 3 * time.Second, Interval: 30 * time.Second}},
		{"whisper", health.WhisperProbe, services.Probe{Service: "whisper", URL: "http://127.0.0.1:8081/", Timeout: 3 * time.Second, Interval: 30 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProber{}
			p := tc.probe(f, ports)
			if err := p.Liveness(bg); err != nil {
				t.Fatal(err)
			}
			if len(f.probes) != 1 || f.probes[0] != tc.want {
				t.Errorf("probed %+v, want %+v", f.probes, tc.want)
			}
			f.err = errors.New("answered 503")
			if err := p.Liveness(bg); err == nil {
				t.Error("a failing prober must fail liveness")
			}
			// The neutral answers of the parts the service items fill in later.
			if c := p.Content(bg); c.Missing {
				t.Errorf("content %+v", c)
			}
			if r := p.Readiness(bg); r.Detail != "" || len(r.Problems) != 0 || r.Books != nil {
				t.Errorf("readiness %+v", r)
			}
			if d := p.Deep(bg); d != nil {
				t.Errorf("deep %v", d)
			}
		})
	}
}

func TestSpecsOfKiwixAndWhisper(t *testing.T) {
	f := &fakeProber{}
	ports := services.Ports{Kiwix: 1, Whisper: 2}
	k, w := health.KiwixSpec(f, ports, nil), health.WhisperSpec(f, ports, func() bool { return false })
	if k.Name != "kiwix" || k.Unit != services.UnitKiwix || k.DownCode != doctor.SVC002 || k.Enabled != nil {
		t.Errorf("kiwix spec %+v", k)
	}
	if w.Name != "whisper" || w.Unit != services.UnitWhisper || w.DownCode != doctor.SVC011 || w.Enabled == nil || w.Enabled() {
		t.Errorf("whisper spec %+v", w)
	}
}

func TestFuncProbeUsesItsFunctions(t *testing.T) {
	p := &health.FuncProbe{
		LivenessFn:  func(context.Context) error { return health.Coded(doctor.SVC034, errDown) },
		ContentFn:   func(context.Context) health.Content { return health.Content{Missing: true, Code: doctor.SVC030} },
		ReadinessFn: func(context.Context) health.Readiness { return health.Readiness{Detail: "x"} },
		DeepFn: func(context.Context) []health.Problem {
			return []health.Problem{{Code: doctor.SVC031, Severity: doctor.Warning}}
		},
	}
	if err := p.Liveness(bg); err == nil || !errors.Is(err, errDown) {
		t.Errorf("liveness %v", err)
	}
	var ce *health.CodedError
	if !errors.As(p.Liveness(bg), &ce) || ce.Code != doctor.SVC034 || ce.Error() == "" {
		t.Errorf("coded error %v", ce)
	}
	if !p.Content(bg).Missing || p.Readiness(bg).Detail != "x" || len(p.Deep(bg)) != 1 {
		t.Error("the functions were not used")
	}
}
