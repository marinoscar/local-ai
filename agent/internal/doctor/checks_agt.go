package doctor

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ConfigReport is the health of node.yaml as the doctor reads it. *config.Store provides it
// (DoctorStatus in internal/config/doctor.go): config imports this package for its codes, so this
// package cannot import config, and the report is plain data.
type ConfigReport struct {
	// Code is the code of the file's condition: LAI-AGT-001 (missing), LAI-AGT-002 (invalid or
	// unknown keys), the code of a lone non-blocking problem (LAI-CAT-003), or "" when healthy.
	Code string
	// Severity is the effective severity of Code ("error", or "warning" for a file that loaded
	// with problems); empty means the default of the code.
	Severity Severity
	// Problems is the details.problems[] list of the latest evaluation.
	Problems []ConfigProblem
	// Pending lists the settings whose new value needs a restart (LAI-AGT-016).
	Pending []ConfigPending
}

// ConfigProblem is one entry of a LAI-AGT-002 report.
type ConfigProblem struct {
	Path     string
	Message  string
	Code     string // a more specific code, when one exists (LAI-CLD-062)
	Severity string // "error" blocks loading, "warning" does not
	Line     int    // 1-based line in node.yaml, 0 when unknown
}

// ConfigPending is one setting that needs a restart. Target is "agent" or "llama".
type ConfigPending struct {
	Path   string
	Target string
}

// AgentSources are the agent-core sources the built-in agt.* checks read. Each is optional: a nil
// source skips the checks that need it.
type AgentSources struct {
	// Config is the config store (*config.Store).
	Config interface{ DoctorStatus() ConfigReport }
	// State is the state store (*state.Store): Findings reports a damaged or unwritable state.
	State interface{ Findings() []state.Finding }
	// Audit is the audit log (*audit.Log): Err is non-nil while the log cannot be written.
	Audit interface{ Err() error }
}

// Ids of the built-in checks.
const (
	CheckAgtConfig        = "agt.config"
	CheckAgtStateWritable = "agt.state_writable"
	CheckAgtStateIntact   = "agt.state_intact"
	CheckAgtAuditWritable = "agt.audit_writable"
)

// AgentChecks returns the built-in agent-core checks for the sources given. All are fast: they read
// memory the owning package already keeps current, and never touch the disk or the network.
//
//	agt.config          current node.yaml condition: LAI-AGT-001, LAI-AGT-002, LAI-CAT-003 and the
//	                    pending restarts (LAI-AGT-016)
//	agt.state_writable  state.KindUnwritable -> LAI-AGT-004
//	agt.state_intact    state.KindDamaged -> LAI-AGT-003 (critical when defaults were used)
//	agt.audit_writable  audit.Log.Err() != nil -> LAI-AGT-013
func AgentChecks(src AgentSources) []Check {
	var out []Check
	if src.Config != nil {
		out = append(out, Check{ID: CheckAgtConfig, Area: "AGT", Fast: true, Run: func(context.Context) []Result {
			return configResults(src.Config.DoctorStatus())
		}})
	}
	if src.State != nil {
		out = append(out,
			Check{ID: CheckAgtStateWritable, Area: "AGT", Fast: true, Run: func(context.Context) []Result {
				return stateResults(src.State.Findings(), state.KindUnwritable, AGT004)
			}},
			Check{ID: CheckAgtStateIntact, Area: "AGT", Fast: true, Run: func(context.Context) []Result {
				return stateResults(src.State.Findings(), state.KindDamaged, AGT003)
			}})
	}
	if src.Audit != nil {
		out = append(out, Check{ID: CheckAgtAuditWritable, Area: "AGT", Fast: true, Run: func(context.Context) []Result {
			if err := src.Audit.Err(); err != nil {
				return []Result{{Code: AGT013, Details: map[string]any{"error": err.Error()}}}
			}
			return nil
		}})
	}
	return out
}

// RegisterAgentChecks adds AgentChecks(src) to reg (DefaultRegistry when nil).
func RegisterAgentChecks(reg *Registry, src AgentSources) error {
	if reg == nil {
		reg = DefaultRegistry
	}
	for _, c := range AgentChecks(src) {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}

func configResults(rep ConfigReport) []Result {
	var out []Result
	if rep.Code != "" {
		r := Result{Code: rep.Code, Severity: rep.Severity}
		if len(rep.Problems) > 0 {
			problems := make([]any, 0, len(rep.Problems))
			for _, p := range rep.Problems {
				m := map[string]any{"path": p.Path, "message": p.Message, "severity": p.Severity}
				if p.Code != "" {
					m["code"] = p.Code
				}
				if p.Line > 0 {
					m["line"] = p.Line
				}
				problems = append(problems, m)
			}
			r.Details = map[string]any{"problems": problems}
		}
		out = append(out, r)
	}
	if len(rep.Pending) > 0 {
		pending := make([]any, 0, len(rep.Pending))
		for _, p := range rep.Pending {
			pending = append(pending, map[string]any{"path": p.Path, "target": p.Target})
		}
		out = append(out, Result{Code: AGT016, Details: map[string]any{"pending": pending}})
	}
	return out
}

// stateResults turns the findings of one kind into results of code. The severity is the finding's
// (LAI-AGT-003 is critical when defaults and offline mode were used).
func stateResults(findings []state.Finding, kind state.FindingKind, code string) []Result {
	var out []Result
	for _, f := range findings {
		if f.Kind != kind {
			continue
		}
		out = append(out, Result{Code: code, Severity: Severity(f.Severity), Details: copyDetails(f.Details)})
	}
	return out
}
