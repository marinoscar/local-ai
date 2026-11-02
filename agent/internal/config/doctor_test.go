package config_test

import (
	"context"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The config store plugs into the built-in agt.config check through DoctorStatus.
func TestDoctorStatusFeedsTheAgtConfigCheck(t *testing.T) {
	run := func(t *testing.T, s *config.Store) []doctor.Result {
		t.Helper()
		reg := doctor.NewRegistry()
		if err := doctor.RegisterAgentChecks(reg, doctor.AgentSources{Config: s}); err != nil {
			t.Fatal(err)
		}
		rep, err := reg.Run(context.Background(), doctor.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return rep.Results
	}

	t.Run("healthy file passes", func(t *testing.T) {
		s, _ := openStore(t, example(t))
		if got := run(t, s); len(got) != 0 {
			t.Fatalf("results = %+v, want none", got)
		}
	})
	t.Run("missing file is LAI-AGT-001 (error)", func(t *testing.T) {
		s, _ := openStore(t, "")
		got := run(t, s)
		if len(got) != 1 || got[0].Code != config.CodeNodeYAMLMissing || got[0].Severity != doctor.Error {
			t.Fatalf("results = %+v", got)
		}
	})
	t.Run("invalid file is LAI-AGT-002 with the problems and the line", func(t *testing.T) {
		s, _ := openStore(t, swap(t, example(t), "mode: router ", "mode: both   "))
		got := run(t, s)
		if len(got) != 1 || got[0].Code != config.CodeNodeYAMLInvalid || got[0].Severity != doctor.Error {
			t.Fatalf("results = %+v", got)
		}
		problems, _ := got[0].Details["problems"].([]any)
		if len(problems) == 0 {
			t.Fatalf("details = %+v, want problems", got[0].Details)
		}
		first, _ := problems[0].(map[string]any)
		if first["path"] != "llama.mode" || first["line"] == nil {
			t.Errorf("problem = %+v", first)
		}
	})
	t.Run("unknown key is a LAI-AGT-002 warning", func(t *testing.T) {
		s, _ := openStore(t, example(t)+"\nfuture_feature: 1\n")
		got := run(t, s)
		if len(got) != 1 || got[0].Code != config.CodeNodeYAMLInvalid || got[0].Severity != doctor.Warning {
			t.Fatalf("results = %+v", got)
		}
	})
}
