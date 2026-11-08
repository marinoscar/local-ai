package cli

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Issue #435: on a terminal the ark commands ask instead of telling the person to retype the command
// with a flag. Without a terminal, with --json or with --yes the refusal is unchanged.

// offlineArkRig answers the first POST /ark/sync with 409 LAI-SYN-011 and every later one by starting
// the run (the agent accepts allow_offline_mode).
func offlineArkRig(t *testing.T) *arkRig {
	t.Helper()
	r := newArkRig(t)
	r.twoDownloads()
	var n atomic.Int32
	r.agent().Handle(http.MethodPost, "/ark/sync", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			fake.WriteEnvelope(w, 409, "LAI-SYN-011", "Manual sync in offline mode needs confirmation.", "Answer yes when asked.", nil)
			return
		}
		if r.onStart != nil {
			r.onStart()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"run_id":"` + arkRunID + `","state":"running"}`))
	})
	return r
}

func (r *arkRig) syncBodies() []string {
	var out []string
	for _, q := range r.agent().Requests() {
		if q.Method == http.MethodPost && q.Path == "/ark/sync" {
			out = append(out, q.Body)
		}
	}
	return out
}

func TestArkSyncOfflineAsks(t *testing.T) {
	t.Run("yes: retried with allow_offline_mode", func(t *testing.T) {
		r := offlineArkRig(t)
		got := r.run(mgArgs("ark", "sync", "--wait").withTerm("y"))
		if got.code != 0 || !strings.Contains(got.errOut, "This node is offline. Sync anyway over the internet? [y/N] ") {
			t.Fatalf("%+v", got)
		}
		b := r.syncBodies()
		if len(b) != 2 || !strings.Contains(b[0], `"allow_offline_mode":false`) || !strings.Contains(b[1], `"allow_offline_mode":true`) {
			t.Errorf("bodies %v", b)
		}
		if strings.Contains(got.errOut, "LAI-SYN-011") {
			t.Errorf("the refusal was shown although the person was asked: %q", got.errOut)
		}
	})
	t.Run("Spanish", func(t *testing.T) {
		r := offlineArkRig(t)
		got := r.run(mgArgs("--lang", "es", "ark", "sync").withTerm("sí"))
		if got.code != 0 || !strings.Contains(got.errOut, "Este nodo está sin conexión. ¿Sincronizar de todos modos por internet? [s/N] ") {
			t.Fatalf("%+v", got)
		}
		if b := r.syncBodies(); len(b) != 2 {
			t.Errorf("bodies %v", b)
		}
	})
	t.Run("no (or Enter): nothing started, exit 0", func(t *testing.T) {
		for _, answer := range []string{"n", ""} {
			r := offlineArkRig(t)
			got := r.run(mgArgs("ark", "sync", "--wait").withTerm(answer))
			if got.code != 0 || !strings.Contains(got.out, "Nothing was started. This computer stays in offline mode.") {
				t.Fatalf("%q: %+v", answer, got)
			}
			if b := r.syncBodies(); len(b) != 1 {
				t.Errorf("%q: bodies %v", answer, b)
			}
		}
	})
	t.Run("no terminal, --json, --yes and --scheduled: the refusal, unchanged", func(t *testing.T) {
		cases := []struct {
			name string
			run  mgRun
			code int
		}{
			{"no terminal", mgArgs("ark", "sync", "--wait"), ExitRefused},
			{"--json", mgArgs("--json", "ark", "sync", "--wait").withTerm("y"), ExitRefused},
			{"--yes", mgArgs("--yes", "ark", "sync", "--wait").withTerm("y"), ExitRefused},
			{"without --wait", mgArgs("ark", "sync"), ExitRefused},
		}
		for _, tc := range cases {
			r := offlineArkRig(t)
			got := r.run(tc.run)
			if got.code != tc.code || !strings.Contains(got.out+got.errOut, "LAI-SYN-011") || strings.Contains(got.errOut, "Sync anyway") {
				t.Errorf("%s: %+v", tc.name, got)
			}
			if b := r.syncBodies(); len(b) != 1 {
				t.Errorf("%s: bodies %v", tc.name, b)
			}
		}
		r := offlineArkRig(t)
		got := r.run(mgArgs("ark", "sync", "--scheduled").withTerm("y"))
		if got.code != 0 || !strings.Contains(got.out, "Sync skipped") || len(r.syncBodies()) != 1 {
			t.Errorf("scheduled: %+v", got)
		}
	})
}

func TestArkImportPicksADrive(t *testing.T) {
	const two = `{"drives":[{"root_id":"ark-3f9c0a12","label":"AI-ARK","mount":"/media/person-a/AI-ARK","state":"ready"},` +
		`{"root_id":"ark-aabbccdd","label":"AI-ARK2","mount":"/media/person-a/AI-ARK2","state":"read_only"},` +
		`{"root_id":"ark-00000000","state":"unclean"}]}`
	rig := func() *mgRig {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodGet, "/ark/drives", 200, two)
		r.agent().Respond(http.MethodPost, "/ark/import", 202, `{"run_id":"`+arkRunID+`"}`)
		return r
	}
	t.Run("the number chosen is the drive used", func(t *testing.T) {
		r := rig()
		got := r.run(mgArgs("ark", "import").withTerm("2"))
		if got.code != 0 || !strings.Contains(r.lastBody(http.MethodPost, "/ark/import"), `"from_root":"ark-aabbccdd"`) {
			t.Fatalf("%+v", got)
		}
		for _, want := range []string{
			"Several AI-ARK drives are connected. Which one do you want to copy from?\n",
			"  1. AI-ARK (/media/person-a/AI-ARK)  ark-3f9c0a12\n",
			"  2. AI-ARK2 (/media/person-a/AI-ARK2)  ark-aabbccdd\n",
			"Type its number and press Enter (Enter alone stops): ",
		} {
			if !strings.Contains(got.errOut, want) {
				t.Errorf("stderr lacks %q:\n%s", want, got.errOut)
			}
		}
		if strings.Contains(got.errOut, "ark-00000000") {
			t.Error("a drive that cannot be read was offered")
		}
	})
	t.Run("a wrong answer is asked again", func(t *testing.T) {
		r := rig()
		got := r.run(mgArgs("--lang", "es", "ark", "import").withTerm("9", "uno", "1"))
		if got.code != 0 || strings.Count(got.errOut, "Escriba un número del 1 al 2.") != 2 ||
			!strings.Contains(r.lastBody(http.MethodPost, "/ark/import"), `"from_root":"ark-3f9c0a12"`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("three wrong answers: the LAI-ARK-015 error, nothing started", func(t *testing.T) {
		r := rig()
		got := r.run(mgArgs("ark", "import").withTerm("0", "3", "x"))
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-ARK-015]") || r.agent().Count("/ark/import") != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("Enter alone or the end of the input: nothing started, exit 0", func(t *testing.T) {
		for _, term := range []mgRun{mgArgs("ark", "import").withTerm(""), mgArgs("ark", "import").withTerm()} {
			r := rig()
			got := r.run(term)
			if got.code != 0 || !strings.Contains(got.out, "Nothing was copied.") || r.agent().Count("/ark/import") != 0 {
				t.Fatalf("%+v", got)
			}
		}
	})
	t.Run("no terminal, --json and --yes: exit 2 with the list, unchanged", func(t *testing.T) {
		for _, run := range []mgRun{mgArgs("ark", "import"), mgArgs("--json", "ark", "import").withTerm("1"), mgArgs("--yes", "ark", "import").withTerm("1")} {
			r := rig()
			got := r.run(run)
			if got.code != ExitUsage || !strings.Contains(got.out+got.errOut, "LAI-ARK-015") || strings.Contains(got.errOut, "Which one") ||
				r.agent().Count("/ark/import") != 0 {
				t.Errorf("%v: %+v", run.args, got)
			}
		}
	})
}
