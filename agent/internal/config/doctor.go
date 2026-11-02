package config

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// DoctorStatus reports the health of node.yaml in the doctor's terms. It is what the built-in
// agt.config check reads (doctor.AgentSources.Config); doctor cannot import this package, so the
// report is plain data.
func (s *Store) DoctorStatus() doctor.ConfigReport { return doctorReport(s.Status()) }

func doctorReport(st Status) doctor.ConfigReport {
	rep := doctor.ConfigReport{Code: st.Code, Severity: doctor.Severity(st.Severity)}
	for _, p := range st.Problems {
		rep.Problems = append(rep.Problems, doctor.ConfigProblem{
			Path: p.Path, Message: p.Message, Code: p.Code, Severity: string(p.Severity), Line: p.Line,
		})
	}
	for _, n := range st.Pending {
		rep.Pending = append(rep.Pending, doctor.ConfigPending{Path: n.Path, Target: n.Target})
	}
	return rep
}
