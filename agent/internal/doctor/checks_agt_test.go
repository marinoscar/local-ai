package doctor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	auditfake "github.com/marinoscar/local-ai/agent/internal/audit/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

type fakeConfig struct{ rep doctor.ConfigReport }

func (f *fakeConfig) DoctorStatus() doctor.ConfigReport { return f.rep }

type fakeState struct{ findings []state.Finding }

func (f *fakeState) Findings() []state.Finding { return f.findings }

type fakeAudit struct{ err error }

func (f *fakeAudit) Err() error { return f.err }

func runAgent(t *testing.T, src doctor.AgentSources) doctor.Report {
	t.Helper()
	reg := doctor.NewRegistry()
	if err := doctor.RegisterAgentChecks(reg, src); err != nil {
		t.Fatal(err)
	}
	rep, err := reg.Run(context.Background(), doctor.RunOptions{Areas: []string{"AGT"}, FastOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestAgentChecksAreFastAndSkipMissingSources(t *testing.T) {
	all := doctor.AgentChecks(doctor.AgentSources{Config: &fakeConfig{}, State: &fakeState{}, Audit: &fakeAudit{}})
	var ids []string
	for _, c := range all {
		ids = append(ids, c.ID)
		if !c.Fast || c.Area != "AGT" {
			t.Errorf("%s: fast=%v area=%s, want a fast AGT check", c.ID, c.Fast, c.Area)
		}
	}
	want := []string{doctor.CheckAgtConfig, doctor.CheckAgtStateWritable, doctor.CheckAgtStateIntact, doctor.CheckAgtAuditWritable}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if got := doctor.AgentChecks(doctor.AgentSources{}); len(got) != 0 {
		t.Errorf("no sources must give no checks, got %d", len(got))
	}
	if got := doctor.AgentChecks(doctor.AgentSources{State: &fakeState{}}); len(got) != 2 {
		t.Errorf("state source gives %d checks, want 2", len(got))
	}

	reg := doctor.NewRegistry()
	src := doctor.AgentSources{Config: &fakeConfig{}}
	if err := doctor.RegisterAgentChecks(reg, src); err != nil {
		t.Fatal(err)
	}
	if err := doctor.RegisterAgentChecks(reg, src); err == nil {
		t.Error("registering twice must fail")
	}
}

func TestConfigCheckResults(t *testing.T) {
	tests := []struct {
		name string
		rep  doctor.ConfigReport
		want []string // code/severity
	}{
		{"healthy", doctor.ConfigReport{}, nil},
		{"missing", doctor.ConfigReport{Code: doctor.AGT001, Severity: doctor.Error}, []string{"LAI-AGT-001/error"}},
		{"invalid", doctor.ConfigReport{Code: doctor.AGT002, Severity: doctor.Error, Problems: []doctor.ConfigProblem{
			{Path: "llama.mode", Message: "must be router or single", Severity: "error", Line: 12},
			{Path: "cloud.url", Message: "must start with https://", Code: doctor.CLD062, Severity: "error"},
		}}, []string{"LAI-AGT-002/error"}},
		{"unknown key is a warning", doctor.ConfigReport{Code: doctor.AGT002, Severity: doctor.Warning}, []string{"LAI-AGT-002/warning"}},
		{"lone catalog problem", doctor.ConfigReport{Code: doctor.CAT003, Severity: doctor.Warning}, []string{"LAI-CAT-003/warning"}},
		{"pending restart", doctor.ConfigReport{Pending: []doctor.ConfigPending{{Path: "ports.agent", Target: "agent"}}}, []string{"LAI-AGT-016/info"}},
		{"invalid and pending", doctor.ConfigReport{Code: doctor.AGT002, Pending: []doctor.ConfigPending{{Path: "llama.mode", Target: "llama"}}},
			[]string{"LAI-AGT-002/error", "LAI-AGT-016/info"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := runAgent(t, doctor.AgentSources{Config: &fakeConfig{rep: tc.rep}})
			var got []string
			for _, r := range rep.Results {
				got = append(got, r.Code+"/"+string(r.Severity))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("results = %v, want %v", got, tc.want)
			}
			if len(tc.want) == 0 && !reflect.DeepEqual(rep.Passed, []string{"agt.config"}) {
				t.Errorf("passed = %v", rep.Passed)
			}
		})
	}

	t.Run("details carry the problems with path, line and specific code", func(t *testing.T) {
		rep := runAgent(t, doctor.AgentSources{Config: &fakeConfig{rep: tests[2].rep}})
		problems, _ := rep.Results[0].Details["problems"].([]any)
		if len(problems) != 2 {
			t.Fatalf("details = %+v", rep.Results[0].Details)
		}
		first, second := problems[0].(map[string]any), problems[1].(map[string]any)
		if first["path"] != "llama.mode" || first["line"] != 12 || first["code"] != nil {
			t.Errorf("first = %+v", first)
		}
		if second["code"] != doctor.CLD062 || second["line"] != nil {
			t.Errorf("second = %+v", second)
		}
	})
	t.Run("details carry the pending settings", func(t *testing.T) {
		rep := runAgent(t, doctor.AgentSources{Config: &fakeConfig{rep: tests[5].rep}})
		pending, _ := rep.Results[0].Details["pending"].([]any)
		if len(pending) != 1 || !reflect.DeepEqual(pending[0], map[string]any{"path": "ports.agent", "target": "agent"}) {
			t.Errorf("details = %+v", rep.Results[0].Details)
		}
	})
}

func TestStateAndAuditChecksMapFindingsToCodes(t *testing.T) {
	tests := []struct {
		name  string
		state []state.Finding
		audit error
		want  []string // check=code/severity
	}{
		{"all healthy", nil, nil, nil},
		{"unwritable is LAI-AGT-004", []state.Finding{
			{Kind: state.KindUnwritable, Severity: state.SeverityCritical, Details: map[string]any{"path": "/var/lib/localai/state.json"}},
		}, nil, []string{"agt.state_writable=LAI-AGT-004/critical"}},
		{"damaged with the backup is LAI-AGT-003 warning", []state.Finding{
			{Kind: state.KindDamaged, Severity: state.SeverityWarning, Details: map[string]any{"source": "bak"}},
		}, nil, []string{"agt.state_intact=LAI-AGT-003/warning"}},
		{"damaged with defaults is LAI-AGT-003 critical", []state.Finding{
			{Kind: state.KindDamaged, Severity: state.SeverityCritical, Details: map[string]any{"source": "defaults"}},
		}, nil, []string{"agt.state_intact=LAI-AGT-003/critical"}},
		{"both state findings", []state.Finding{
			{Kind: state.KindDamaged, Severity: state.SeverityWarning},
			{Kind: state.KindUnwritable, Severity: state.SeverityCritical},
		}, nil, []string{"agt.state_intact=LAI-AGT-003/warning", "agt.state_writable=LAI-AGT-004/critical"}},
		{"audit log error is LAI-AGT-013", nil, errors.New("disk full"), []string{"agt.audit_writable=LAI-AGT-013/warning"}},
		{"everything at once", []state.Finding{{Kind: state.KindUnwritable, Severity: state.SeverityCritical}}, errors.New("x"),
			[]string{"agt.state_writable=LAI-AGT-004/critical", "agt.audit_writable=LAI-AGT-013/warning"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := runAgent(t, doctor.AgentSources{State: &fakeState{tc.state}, Audit: &fakeAudit{tc.audit}})
			got := map[string]bool{}
			for _, r := range rep.Results {
				got[r.Check+"="+r.Code+"/"+string(r.Severity)] = true
			}
			if len(got) != len(tc.want) {
				t.Fatalf("results = %v, want %v", got, tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("missing %s in %v", w, got)
				}
			}
		})
	}
}

// The real state store and the real audit log feed the checks: a damaged state file, then an audit
// log on a full disk.
func TestRealStateAndAuditFeedTheChecks(t *testing.T) {
	const statePath = "/var/lib/localai/state.json"
	mem := statefake.New()
	mem.Seed(statePath, []byte("{not json"))
	st, err := state.Open(statePath, state.Options{FS: mem})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	afs := auditfake.New()
	afs.DiskFree = 0
	log, err := audit.Open("/var/lib/localai/audit.jsonl", audit.WithFS(afs), audit.WithoutRotation())
	if err == nil {
		t.Log("audit.Open succeeded on a full disk; the write below fails instead")
	}
	_ = log.Write(audit.Event{Action: "test"})

	rep := runAgent(t, doctor.AgentSources{State: st, Audit: log})
	got := map[string]doctor.Severity{}
	for _, r := range rep.Results {
		got[r.Code] = r.Severity
	}
	if got[doctor.AGT003] != doctor.Critical {
		t.Errorf("a state file damaged beyond its backup must be LAI-AGT-003 critical: %+v", rep.Results)
	}
	if got[doctor.AGT013] != doctor.Warning {
		t.Errorf("a full disk must give LAI-AGT-013 (warning): %+v", rep.Results)
	}
}

// The whole path of the issue: a fast check fails, the manager raises the alert and publishes the
// event, the check passes, the alert clears.
func TestAgentCheckDrivesAnAlert(t *testing.T) {
	cfg := &fakeConfig{rep: doctor.ConfigReport{Code: doctor.AGT001, Severity: doctor.Error}}
	reg := doctor.NewRegistry()
	if err := doctor.RegisterAgentChecks(reg, doctor.AgentSources{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	m := doctor.NewManager(doctor.ManagerOptions{Registry: reg})
	m.Tick(context.Background())
	active := m.Active()
	if len(active) != 1 || active[0].Code != doctor.AGT001 || active[0].Severity != doctor.Error {
		t.Fatalf("active = %+v", active)
	}
	cfg.rep = doctor.ConfigReport{}
	m.Tick(context.Background())
	if len(m.Active()) != 0 {
		t.Fatalf("active = %+v", m.Active())
	}
}
