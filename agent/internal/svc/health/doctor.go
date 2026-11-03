package health

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Doctor check ids (agent.md section 4.13, companion-services.md section 4.6).
const (
	// CheckHealth is the fast check: the supervisor's records as doctor results. It reads cached
	// values only, so the alert manager can run it every 60 s.
	CheckHealth = "svc.health"
	// CheckDeep is the slow check: the deep probes of the services that are up. It runs on GET /doctor,
	// aictl doctor and the drill, never as an alert source.
	CheckDeep = "svc.deep"
)

// deepTimeout bounds the deep check: a transcription of the bundled WAVs and a synthesis can take a
// while on a CPU-only node.
const deepTimeout = 120 * time.Second

// Checks returns the two doctor checks of the supervisor.
func (s *Supervisor) Checks() []doctor.Check {
	return []doctor.Check{
		{ID: CheckHealth, Area: "SVC", Fast: true, Timeout: 5 * time.Second, Run: s.runHealth},
		{ID: CheckDeep, Area: "SVC", Timeout: deepTimeout, Run: s.runDeep},
	}
}

// RegisterDoctor adds the checks to reg (doctor.DefaultRegistry when nil). The daemon wiring calls it;
// the package registers nothing at init time.
func (s *Supervisor) RegisterDoctor(reg *doctor.Registry) error {
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	for _, c := range s.Checks() {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}

// runHealth turns every problem of every record into a result. LAI-SVC-051 carries its effective
// severity (error in survival mode, warning otherwise), as every problem carries the one the record
// has. A service in the state ok or degraded with no problem produces nothing.
func (s *Supervisor) runHealth(ctx context.Context) []doctor.Result {
	var out []doctor.Result
	for _, r := range s.Records(ctx) {
		for _, p := range r.Problems {
			d := map[string]any{"service": r.Name, "state": string(r.State)}
			if r.Unit != "" {
				d["unit"] = r.Unit
			}
			out = append(out, doctor.Result{Code: p.Code, Severity: p.Severity, Details: d})
		}
	}
	return out
}

// runDeep runs the deep probes and returns their problems.
func (s *Supervisor) runDeep(ctx context.Context) []doctor.Result {
	var out []doctor.Result
	for _, dr := range s.Deep(ctx) {
		for _, p := range dr.Problems {
			d := map[string]any{"service": dr.Service, "deep": true}
			if dr.Unit != "" {
				d["unit"] = dr.Unit
			}
			sev := p.Severity
			if !sev.Valid() {
				sev = defaultSeverity(p.Code)
			}
			out = append(out, doctor.Result{Code: p.Code, Severity: sev, Details: d})
		}
	}
	return out
}
