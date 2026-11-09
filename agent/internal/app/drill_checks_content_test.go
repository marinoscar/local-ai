package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
)

// TestLoopbackRunsTheAgentsOwnHandlerAsTheInternalAdmin: the Portal's chat page is closed to an anonymous
// caller outside survival mode; the drill's loopback reaches it with no token, no cookie and no socket.
func TestLoopbackRunsTheAgentsOwnHandlerAsTheInternalAdmin(t *testing.T) {
	m := newMountsRig(t, true)
	m.start()
	m.waitStarted()
	lb := agentLoopback{handler: m.d.apiHandler}

	get := func(path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, checks.AgentOrigin+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := lb.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	if rp := m.raw("GET", "/chat/props", nil); rp.status != 403 {
		t.Fatalf("anonymous /chat/props = %d, want the locked page (403)", rp.status)
	}
	resp, body := get("/chat/props")
	if resp.StatusCode != 200 || body != "llama-ui" {
		t.Fatalf("loopback /chat/props = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("the loopback left a cookie: %q", resp.Header.Get("Set-Cookie"))
	}
	resp, body = get(checks.APIBase + "/portal/status?lang=en")
	if resp.StatusCode != 200 || !strings.Contains(body, `"schema_version"`) {
		t.Fatalf("loopback portal status = %d %.200s", resp.StatusCode, body)
	}
	if resp, _ := get(checks.APIBase + "/nothing-here"); resp.StatusCode != 404 {
		t.Errorf("an unknown API path = %d, want 404", resp.StatusCode)
	}
}

func TestLoopbackWithoutAServerAndWithACancelledContext(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, checks.AgentOrigin+"/x", nil)
	if _, err := (agentLoopback{handler: func() http.Handler { return nil }}).Do(req); err == nil {
		t.Error("no handler must be an error")
	}
	block := make(chan struct{})
	defer close(block)
	slow := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, checks.AgentOrigin+"/x", nil)
	if _, err := (agentLoopback{handler: func() http.Handler { return slow }}).Do(req); err == nil {
		t.Error("a cancelled request must return its error")
	}
}

// TestContentChecksRunOnTheDaemon: every check of DRILL-04, wired as the daemon wires them, runs against
// a real daemon (fake llama, fake systemd, an empty content root) and ends in a valid result without a
// panic. The rig has no content, so the file checks fail and the services answer; the point is the
// wiring.
func TestContentChecksRunOnTheDaemon(t *testing.T) {
	m := newMountsRig(t, true)
	m.start()
	m.waitStarted()
	reg := drill.NewRegistry()
	if err := m.d.registerContentDrillChecks(reg); err != nil {
		t.Fatal(err)
	}
	all, err := reg.Select(context.Background(), nil)
	if err != nil || len(all) != 14 {
		t.Fatalf("registered %d checks (%v), want 14", len(all), err)
	}
	env := &drill.Env{RunID: "run-test", Trigger: drill.TriggerManual, Mode: "online", Now: time.Now}
	got := map[string]drill.Outcome{}
	for _, c := range all {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out := c.Run(ctx, env)
		cancel()
		switch out.Result {
		case drill.ResultPass, drill.ResultWarn, drill.ResultFail, drill.ResultSkip:
		default:
			t.Errorf("%s returned %q", c.ID, out.Result)
		}
		got[c.ID] = out
	}
	// The chat page and its props are reachable by the drill's admin context, whatever else this bare
	// rig lacks.
	if failed, _ := got["drl.portal"].Details["failed"].(map[string]any); failed != nil {
		for _, part := range []string{"chat_props", "status"} {
			if _, bad := failed[part]; bad {
				t.Errorf("drl.portal could not reach %s: %v", part, failed)
			}
		}
	}
	// There is no content on the rig: check 3 must say so, not pass.
	if o := got["drl.files.manifest"]; o.Result == drill.ResultPass {
		t.Errorf("drl.files.manifest passed on an empty node: %+v", o)
	}
	t.Logf("results: %v", summarize(got))
}

func summarize(m map[string]drill.Outcome) map[string]string {
	out := map[string]string{}
	for id, o := range m {
		out[id] = string(o.Result) + " " + o.Code
	}
	return out
}
