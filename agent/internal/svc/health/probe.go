package health

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Defaults of the probes (companion-services.md section 4.6).
const (
	// DefaultInterval is how often a service's liveness probe runs.
	DefaultInterval = 30 * time.Second
	// DefaultProbeTimeout bounds one liveness or readiness call.
	DefaultProbeTimeout = 3 * time.Second
)

// Probe is what a service plugs into the supervisor: the four checks of the table of
// companion-services.md section 4.6. Every method must honour ctx, and must never log or return
// transcripts, synthesised text or locations (section 6).
type Probe interface {
	// Liveness reports whether the service answers (the HTTP request of kiwix and whisper, the
	// "binary executable" check of piper, the "map header parses" check of maps). The supervisor
	// has already checked that the unit is active. An error that carries a doctor code (Coded) names
	// the code of the "down" problem; otherwise the Spec's DownCode applies.
	Liveness(ctx context.Context) error
	// Content says whether there is anything to serve. It runs on every tick, also while the unit is
	// stopped, so it must be cheap and local. Missing content is never probed and never restarted.
	Content(ctx context.Context) Content
	// Readiness runs after a passing liveness probe and fills the record's detail, per-service fields
	// and problems.
	Readiness(ctx context.Context) Readiness
	// Deep is the slow check, run on demand only (GET /doctor, aictl doctor, the drill). It returns
	// the problems it found.
	Deep(ctx context.Context) []Problem
}

// FuncProbe is a Probe built from functions. A nil function is the neutral answer: liveness passes,
// content is present, readiness is empty and the deep check finds nothing. The service items set the
// functions they own (SVC-03 sets Content and Readiness of kiwix, SVC-06 those of whisper, ...).
type FuncProbe struct {
	LivenessFn  func(ctx context.Context) error
	ContentFn   func(ctx context.Context) Content
	ReadinessFn func(ctx context.Context) Readiness
	DeepFn      func(ctx context.Context) []Problem
}

// Liveness implements Probe.
func (p *FuncProbe) Liveness(ctx context.Context) error {
	if p.LivenessFn == nil {
		return nil
	}
	return p.LivenessFn(ctx)
}

// Content implements Probe.
func (p *FuncProbe) Content(ctx context.Context) Content {
	if p.ContentFn == nil {
		return Content{}
	}
	return p.ContentFn(ctx)
}

// Readiness implements Probe.
func (p *FuncProbe) Readiness(ctx context.Context) Readiness {
	if p.ReadinessFn == nil {
		return Readiness{}
	}
	return p.ReadinessFn(ctx)
}

// Deep implements Probe.
func (p *FuncProbe) Deep(ctx context.Context) []Problem {
	if p.DeepFn == nil {
		return nil
	}
	return p.DeepFn(ctx)
}

var _ Probe = (*FuncProbe)(nil)

// CodedError is a probe failure that names its doctor code.
type CodedError struct {
	Code string
	Err  error
}

// Coded wraps err with the doctor code the "down" problem should carry.
func Coded(code string, err error) error { return &CodedError{Code: code, Err: err} }

func (e *CodedError) Error() string { return fmt.Sprintf("%s: %v", e.Code, e.Err) }

// Unwrap returns the underlying error.
func (e *CodedError) Unwrap() error { return e.Err }

// codeOf returns the doctor code an error carries, "" when none.
func codeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// HTTPLiveness returns the liveness function of an HTTP service: the probe (a fixed loopback URL,
// 200 within its timeout) run by prober. services.HTTPProber is the real prober and never follows a
// redirect, reuses a connection or uses a proxy.
func HTTPLiveness(prober services.Prober, probe services.Probe) func(ctx context.Context) error {
	return func(ctx context.Context) error { return prober.Check(ctx, probe) }
}

// KiwixProbe returns the probe of kiwix with its liveness part: GET /library/catalog/v2/entries?count=1
// on the kiwix port answers 200 (companion-services.md section 4.6). The library readiness, content
// and deep checks are SVC-03's: it sets ContentFn, ReadinessFn and DeepFn on the result.
func KiwixProbe(prober services.Prober, ports services.Ports) *FuncProbe {
	return &FuncProbe{LivenessFn: HTTPLiveness(prober, httpProbe("kiwix", ports))}
}

// WhisperProbe returns the probe of whisper with its liveness part: GET / on the whisper port answers
// 200. The model readiness, content and drill are SVC-06's: it sets ContentFn, ReadinessFn and DeepFn.
func WhisperProbe(prober services.Prober, ports services.Ports) *FuncProbe {
	return &FuncProbe{LivenessFn: HTTPLiveness(prober, httpProbe("whisper", ports))}
}

func httpProbe(name string, ports services.Ports) services.Probe {
	for _, p := range services.Probes(ports) {
		if p.Service == name {
			return p
		}
	}
	return services.Probe{Service: name}
}

// KiwixSpec returns the Spec of kiwix: its unit, the "library isn't running" code and the HTTP
// liveness probe. enabled reads node.yaml kiwix.enabled (nil means always).
func KiwixSpec(prober services.Prober, ports services.Ports, enabled func() bool) Spec {
	return Spec{Name: "kiwix", Unit: services.UnitKiwix, Enabled: enabled, DownCode: CodeLibraryNotRunning, Probe: KiwixProbe(prober, ports)}
}

// WhisperSpec returns the Spec of whisper: its unit, the "speech recognition isn't running" code and
// the HTTP liveness probe. enabled reads node.yaml whisper.enabled (nil means always).
func WhisperSpec(prober services.Prober, ports services.Ports, enabled func() bool) Spec {
	return Spec{Name: "whisper", Unit: services.UnitWhisper, Enabled: enabled, DownCode: CodeSpeechNotRunning, Probe: WhisperProbe(prober, ports)}
}

// Spec describes one supervised service.
type Spec struct {
	// Name is kiwix, whisper, piper or maps (never llama: llama-router owns it).
	Name string
	// Unit is the systemd unit, empty for piper and maps (no process to control).
	Unit string
	// Enabled reports whether node.yaml turns the service on; nil means always. It is read on every
	// tick, so a reload of node.yaml takes effect within one tick.
	Enabled func() bool
	// Probe is required.
	Probe Probe
	// DownCode is the doctor code of "should run and does not respond" when the probe's error names
	// none: LAI-SVC-002 for kiwix, LAI-SVC-011 for whisper.
	DownCode string
	// Interval is how often liveness runs; zero means DefaultInterval.
	Interval time.Duration
}

func (sp Spec) validate() error {
	switch {
	case sp.Name == "":
		return errors.New("health: a service needs a name")
	case sp.Name == "llama":
		return errors.New("health: llama is owned by llama-router, not by the health supervisor")
	case sp.Probe == nil:
		return fmt.Errorf("health: service %q has no probe", sp.Name)
	}
	return nil
}

func (sp Spec) enabled() bool { return sp.Enabled == nil || sp.Enabled() }

func (sp Spec) interval() time.Duration {
	if sp.Interval > 0 {
		return sp.Interval
	}
	return DefaultInterval
}
