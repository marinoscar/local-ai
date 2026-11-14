package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// aictl workloads and aictl workloads activate (aictl.md section 4.12a) against the fake agent.

const workloadsBody = `{"workloads":[` +
	`{"name":"chat","label":{"en":"Chat","es":"Conversación"},"models":["qwen3.5-9b-q4km"],"active":false,"fits":true,"missing":[]},` +
	`{"name":"coding","label":{"en":"Coding","es":"Programación"},"models":["qwen3.6-35b-a3b-q4km"],"active":true,"fits":true,"missing":[]},` +
	`{"name":"music","label":{"en":"Music","es":"Música"},"models":["acestep-v15-turbo"],"active":false,"fits":true,"missing":["acestep-v15-turbo"]}],` +
	`"active":"coding"}`

func TestWorkloadsList(t *testing.T) {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/workloads", 200, workloadsBody)
	got := r.run(mgArgs("workloads"))
	if got.code != ExitOK {
		t.Fatalf("exit %d: %s %s", got.code, got.out, got.errOut)
	}
	lines := strings.Split(strings.TrimSpace(got.out), "\n")
	if len(lines) != 4 || !strings.Contains(lines[1], "● Coding") || !strings.Contains(lines[3], "missing: acestep-v15-turbo") {
		t.Errorf("output:\n%s", got.out)
	}
	got = r.run(mgArgs("--json", "workloads"))
	if got.code != ExitOK || strings.TrimSpace(got.out) != workloadsBody {
		t.Errorf("--json: %d %q", got.code, got.out)
	}
	got = r.run(mgArgs("--lang", "es", "workloads"))
	if !strings.Contains(got.out, "Programación") || !strings.Contains(got.out, "falta:") {
		t.Errorf("Spanish output:\n%s", got.out)
	}
}

func TestWorkloadsActivate(t *testing.T) {
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodPost, "/workloads/chat/activate", 200,
		`{"op_id":"op_abcdefghijkl","active":"chat","loaded":["qwen3.5-9b-q4km"],"unloaded":["qwen3.6-35b-a3b-q4km"]}`)
	got := r.run(mgArgs("workloads", "activate", "chat"))
	if got.code != ExitOK || !strings.Contains(got.out, "now set for chat") || !strings.Contains(got.out, "Stopped qwen3.6-35b-a3b-q4km") {
		t.Fatalf("exit %d:\n%s\n%s", got.code, got.out, got.errOut)
	}
	if b := r.lastBody(http.MethodPost, "/workloads/chat/activate"); !strings.Contains(b, `"wait":true`) || !strings.Contains(b, `"force":false`) {
		t.Errorf("body %s", b)
	}

	r.agent().RespondError(http.MethodPost, "/workloads/nope/activate", 404, "LAI-LLM-050", "There is no workload with this name.", "", nil)
	if got := r.run(mgArgs("workloads", "activate", "nope")); got.code != ExitNotFound {
		t.Errorf("unknown: exit %d", got.code)
	}
	if got := r.run(mgArgs("workloads", "activate", "Bad_Name")); got.code != ExitUsage {
		t.Errorf("bad name: exit %d", got.code)
	}
	r.agent().RespondError(http.MethodPost, "/workloads/coding/activate", 409, "LAI-LLM-053", "Survival mode.", "", nil)
	if got := r.run(mgArgs("--json", "workloads", "activate", "coding")); got.code != ExitRefused {
		t.Errorf("survival without a terminal: exit %d", got.code)
	}
	r.agent().RespondError(http.MethodPost, "/workloads/music/activate", 500, "LAI-LLM-052", "Rolled back.", "", map[string]any{"restored": []string{"x"}})
	if got := r.run(mgArgs("workloads", "activate", "music")); got.code != ExitFailed {
		t.Errorf("rolled back: exit %d", got.code)
	}
}
