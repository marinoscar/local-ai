package piper

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// ServiceName is the name of piper in the health supervisor and in GET /services.
const ServiceName = "piper"

// Thresholds of the deep check (companion-services.md section 4.6): the synthesized sample must be
// louder than -40 dBFS RMS and last between 0.3 and 5 s.
const (
	DeepMinRMSDBFS = -40.0
	DeepMinSeconds = 0.3
	DeepMaxSeconds = 5.0
	deepTextEN     = "Test."
	deepTextES     = "Prueba."
	deepSpeed      = 1.0
)

// Spec returns the supervisor Spec of piper: no unit (it runs per request, so there is nothing to
// restart), the "voice program isn't installed" code as the code of a failed liveness, and the
// probe below. Put it in health.Options.Specs; enabled follows node.yaml piper.enabled.
func (s *Service) Spec() health.Spec {
	return health.Spec{
		Name:     ServiceName,
		Unit:     "",
		Enabled:  func() bool { return s.cfg().Enabled },
		DownCode: doctor.SVC020,
		Probe:    s.Probe(),
	}
}

// Probe returns the four checks of piper (companion-services.md section 4.6):
//
//   - Liveness: the program exists and is executable, and at least one voice is installed;
//   - Content: no installed voice means there is nothing to serve (LAI-SVC-022);
//   - Readiness: one installed voice per Portal language, en and es (LAI-SVC-021 for each missing);
//   - Deep: synthesize "Test." and "Prueba." with each language's default voice and measure them.
func (s *Service) Probe() *health.FuncProbe {
	return &health.FuncProbe{
		LivenessFn:  s.liveness,
		ContentFn:   s.content,
		ReadinessFn: s.readiness,
		DeepFn:      s.deep,
	}
}

func (s *Service) liveness(ctx context.Context) error {
	if err := s.rt.CheckBinary(); err != nil {
		return health.Coded(doctor.SVC020, err)
	}
	if len(s.voices.All()) == 0 {
		return health.Coded(doctor.SVC022, errors.New("no voice is installed"))
	}
	return ctx.Err()
}

func (s *Service) content(context.Context) health.Content {
	if len(s.voices.All()) == 0 {
		return health.Content{Missing: true, Code: doctor.SVC022}
	}
	return health.Content{}
}

func (s *Service) readiness(context.Context) health.Readiness {
	all := s.voices.All()
	r := health.Readiness{Voices: make([]string, 0, len(all))}
	have := map[string]bool{}
	for _, v := range all {
		r.Voices = append(r.Voices, v.ID)
		have[v.Language] = true
	}
	r.Detail = fmt.Sprintf("%d voices", len(all))
	if len(all) == 1 {
		r.Detail = "1 voice"
	}
	for _, lang := range []string{LangEN, LangES} {
		if !have[lang] {
			r.Problems = append(r.Problems, problem(doctor.SVC021))
		}
	}
	return r
}

func (s *Service) deep(ctx context.Context) []health.Problem {
	if err := s.rt.CheckBinary(); err != nil {
		return []health.Problem{problem(doctor.SVC020)}
	}
	all := s.voices.All()
	if len(all) == 0 {
		return []health.Problem{problem(doctor.SVC022)}
	}
	cfg := s.cfg()
	defaults := Defaults(all, cfg)
	var out []health.Problem
	for _, d := range []struct{ lang, text string }{{LangEN, deepTextEN}, {LangES, deepTextES}} {
		id, ok := defaults[d.lang]
		if !ok {
			continue // readiness reports the missing language
		}
		var v Voice
		for _, c := range all {
			if c.ID == id {
				v = c
			}
		}
		if err := ctx.Err(); err != nil {
			return out
		}
		release, err := s.limiter.Acquire(ctx)
		if err != nil {
			continue // the node is busy speaking: a check must not add to it, and busy is no finding
		}
		pcm, rate, err := s.rt.Synthesize(ctx, v, d.text, cfg, deepSpeed)
		release()
		if ctx.Err() != nil {
			return out
		}
		if err != nil {
			out = appendOnce(out, problem(codeOf(err)))
			continue
		}
		secs, rms := PCMStats(pcm, rate)
		if math.IsInf(rms, -1) || rms <= DeepMinRMSDBFS || secs < DeepMinSeconds || secs > DeepMaxSeconds {
			out = appendOnce(out, problem(doctor.SVC023))
		}
	}
	return out
}

// codeOf is the doctor code of a synthesis failure.
func codeOf(err error) string {
	var se *SynthError
	if errors.As(err, &se) {
		return se.Code()
	}
	return doctor.SVC023
}

func problem(code string) health.Problem {
	sev := doctor.Error
	if c, ok := doctor.Lookup(code); ok && c.Severity.Valid() {
		sev = c.Severity
	}
	return health.Problem{Code: code, Severity: sev}
}

func appendOnce(ps []health.Problem, p health.Problem) []health.Problem {
	for _, x := range ps {
		if x.Code == p.Code {
			return ps
		}
	}
	return append(ps, p)
}
