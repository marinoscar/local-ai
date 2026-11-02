package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// lockedBuf is a bytes.Buffer a test may read while Run writes to it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// followRig is a fake agent with a scripted event stream, and a test command, "load", that does
// what `aictl load` will: POST the load, follow model.* events, print the result.
type followRig struct {
	t     *testing.T
	h     *harness
	ev    *fake.Events
	clock *cfake.Clock
	tty   *bool
	hook  func(context.Context) progress.InterruptAction
	reg   *Registry

	// onLoad runs inside POST /models/qwen/load, before the 202 is written: it may publish events
	// (which then reach the CLI before it knows the op id) and chooses the status of the request.
	onLoad func(w http.ResponseWriter) (handled bool)
	// state is what GET /models/qwen answers.
	mu    sync.Mutex
	state string
}

func newFollowRig(t *testing.T) *followRig {
	t.Helper()
	r := &followRig{t: t, h: newHarness(t, fake.Options{}), clock: cfake.NewClock(time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)), state: "loading"}
	r.ev = r.h.agent.Events(t, events.Options{})
	r.h.agent.Handle(http.MethodPost, "/models/qwen/load", func(w http.ResponseWriter, _ *http.Request) {
		if r.onLoad != nil && r.onLoad(w) {
			return
		}
		w.Header().Set("X-LocalAI-Op-Id", "op_1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"qwen","status":"loading","op_id":"op_1"}`)
	})
	r.h.agent.Handle(http.MethodGet, "/models/qwen", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		st := r.state
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"qwen","state":%q}`, st)
	})
	r.reg = NewRegistry()
	r.reg.MustAdd(&Command{Name: "load", Summary: "Load a model (test double of AICTL-05).", Timeout: 30 * time.Second, Run: r.run})
	return r
}

func (r *followRig) setState(s string) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
}

func (r *followRig) run(c *Call) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	fs := FollowSpec{
		Clock: r.clock, TTY: r.tty, OnInterrupt: r.hook,
		Request: func(ctx context.Context) (string, error) {
			var out struct {
				OpID string `json:"op_id"`
			}
			resp, err := cl.Do(ctx, http.MethodPost, "/models/qwen/load", map[string]any{"wait": false, "unload_others": true}, &out)
			if err != nil {
				return "", err
			}
			if out.OpID == "" {
				out.OpID = resp.OpID
			}
			return out.OpID, nil
		},
		Progress: progress.Spec{
			Types:    []string{"model.loading", "model.loaded", "model.failed"},
			AwaitKey: true,
			Match: func(ev progress.Event, key string) bool {
				return ev.OpID == key || ev.Type == events.TypeModelLoaded || ev.Type == events.TypeModelFailed
			},
			OnEvent: func(ev progress.Event) progress.Step {
				switch ev.Type {
				case events.TypeModelLoading:
					var d events.ModelLoading
					_ = json.Unmarshal(ev.Data, &d)
					pct := float64(progress.NoBar)
					if d.Progress != nil {
						pct = *d.Progress * 100
					}
					return progress.Step{Update: &progress.Update{Label: "Loading Qwen 3.5 9B", Percent: pct}}
				case events.TypeModelLoaded:
					var d events.ModelLoaded
					_ = json.Unmarshal(ev.Data, &d)
					return progress.Step{Done: true, Result: d.LoadMS}
				case events.TypeModelFailed:
					var d events.ModelFailed
					_ = json.Unmarshal(ev.Data, &d)
					return progress.Step{Err: &APIError{
						Status: http.StatusInternalServerError, Code: d.Code, Message: d.Message,
						Fix: "Choose a smaller model (`aictl models --loadable`) or check `aictl logs --unit llama`.",
					}}
				}
				return progress.Step{}
			},
			Reread: func(ctx context.Context) (progress.Step, error) {
				var m struct {
					State string `json:"state"`
				}
				if _, err := cl.Do(ctx, http.MethodGet, "/models/qwen", nil, &m); err != nil {
					return progress.Step{}, err
				}
				switch m.State {
				case "loaded":
					return progress.Step{Done: true, Result: int64(7100)}, nil
				case "unloaded":
					return progress.Step{Err: &APIError{Status: 500, Code: api.CodeInternal, Message: "The model is not loaded."}}, nil
				}
				return progress.Step{Update: &progress.Update{Label: "Loading Qwen 3.5 9B", Percent: progress.NoBar}}, nil
			},
		},
	}
	res, err := c.Follow(fs)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		var raw json.RawMessage
		if _, err := cl.Do(c.Ctx, http.MethodGet, "/models/qwen", nil, &raw); err != nil {
			return err
		}
		fmt.Fprintf(c.Stdout, "%s\n", raw)
		return nil
	}
	fmt.Fprintf(c.Stdout, "✓ Qwen 3.5 9B is ready. (%s)\n", progress.FormatElapsed(time.Duration(res.Value.(int64))*time.Millisecond, true))
	return nil
}

type followOut struct {
	code        int
	out, errOut string
}

func (r *followRig) exec(ctx context.Context, args ...string) followOut {
	var out, errb lockedBuf
	env := Env{
		Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string {
			return map[string]string{
				"LOCALAI_SOCKET": r.h.agent.SocketPath, "LOCALAI_URL": r.h.agent.URL, "LOCALAI_TOKEN_FILE": r.h.cfg.TokenFile,
				"LANG": "en_US.UTF-8",
			}[k]
		},
		Geteuid: func() int { return 1000 },
		Version: "0.0.0-dev",
		ClientConfig: func(c *Config) {
			c.Sleep = r.h.sleeps.Sleep
			c.Stderr = &errb
		},
	}
	code := Run(ctx, env, r.reg, args)
	return followOut{code, out.String(), errb.String()}
}

// execAsync runs the command and lets the test watch stderr while it waits.
func (r *followRig) execAsync(ctx context.Context, args ...string) (<-chan followOut, *lockedBuf) {
	var out, errb lockedBuf
	env := Env{
		Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string {
			return map[string]string{
				"LOCALAI_SOCKET": r.h.agent.SocketPath, "LOCALAI_URL": r.h.agent.URL, "LOCALAI_TOKEN_FILE": r.h.cfg.TokenFile,
				"LANG": "en_US.UTF-8",
			}[k]
		},
		Geteuid:      func() int { return 1000 },
		Version:      "0.0.0-dev",
		ClientConfig: func(c *Config) { c.Sleep = r.h.sleeps.Sleep },
	}
	ch := make(chan followOut, 1)
	go func() {
		code := Run(ctx, env, r.reg, args)
		ch <- followOut{code, out.String(), errb.String()}
	}()
	return ch, &errb
}

func untilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func finish(t *testing.T, ch <-chan followOut) followOut {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not return")
		return followOut{}
	}
}

// publishes the events of a successful load from inside the POST handler: they arrive before the CLI
// has the op id and must be held, then matched.
func (r *followRig) loadsOK() {
	r.onLoad = func(w http.ResponseWriter) bool {
		r.ev.Publish(events.TypeModelLoading, "op_1", events.ModelLoading{Model: "qwen", Unloading: []string{"small-vlm"}, OpID: "op_1"})
		p := 0.5
		r.ev.Publish(events.TypeModelLoading, "op_1", events.ModelLoading{Model: "qwen", OpID: "op_1", Progress: &p})
		r.ev.Publish(events.TypeModelLoading, "op_other", events.ModelLoading{Model: "other", OpID: "op_other"})
		r.ev.Publish(events.TypeModelLoaded, "op_1", events.ModelLoaded{Model: "qwen", LoadMS: 7100})
		return false
	}
}

func requestsOf(r *followRig) []string {
	var out []string
	for _, q := range r.h.agent.Requests() {
		out = append(out, q.Method+" "+q.Path)
	}
	return out
}

// Human mode off a terminal: the stream is opened before the POST, progress is one line per state
// change on stderr, and the command's own final line is on stdout.
func TestFollowLoadHumanNonTTY(t *testing.T) {
	r := newFollowRig(t)
	r.loadsOK()
	res := r.exec(context.Background(), "load")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
	}
	if want := "… Loading Qwen 3.5 9B\n"; res.errOut != want {
		t.Errorf("stderr = %q, want %q", res.errOut, want)
	}
	if want := "✓ Qwen 3.5 9B is ready. (7.1 s)\n"; res.out != want {
		t.Errorf("stdout = %q, want %q", res.out, want)
	}
	reqs := requestsOf(r)
	ei, pi := -1, -1
	for i, q := range reqs {
		switch {
		case strings.HasPrefix(q, "GET /events") && ei < 0:
			ei = i
		case q == "POST /models/qwen/load":
			pi = i
		}
	}
	if ei < 0 || pi < 0 || ei > pi {
		t.Fatalf("the subscription must be opened before the mutating request: %v", reqs)
	}
	if !strings.Contains(reqs[ei], "types=model.loading,model.loaded,model.failed") {
		t.Errorf("request = %q", reqs[ei])
	}
}

// On a terminal: one updating line (throttled), cleared at the end, then the result on stdout.
func TestFollowLoadTTY(t *testing.T) {
	r := newFollowRig(t)
	yes := true
	r.tty = &yes
	r.loadsOK()
	res := r.exec(context.Background(), "load")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
	}
	// The clock never moves, so the 50 % update falls inside the redraw throttle of the first draw.
	want := "\x1b[?25l\r⠋ Loading Qwen 3.5 9B  (0 s)\x1b[K\r\x1b[K\x1b[?25h"
	if res.errOut != want {
		t.Errorf("stderr = %q, want %q", res.errOut, want)
	}
	if res.out != "✓ Qwen 3.5 9B is ready. (7.1 s)\n" {
		t.Errorf("stdout = %q", res.out)
	}
}

// --json: nothing on stderr, the command prints the final document; -q draws no progress.
func TestFollowLoadJSONAndQuiet(t *testing.T) {
	r := newFollowRig(t)
	r.loadsOK()
	r.setState("loaded")
	res := r.exec(context.Background(), "--json", "load")
	if res.code != 0 || res.errOut != "" {
		t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.out), &doc); err != nil || doc["state"] != "loaded" || strings.Count(strings.TrimSpace(res.out), "\n") != 0 {
		t.Fatalf("stdout must be exactly one JSON document, got %q (%v)", res.out, err)
	}

	r = newFollowRig(t)
	r.loadsOK()
	res = r.exec(context.Background(), "-q", "load")
	if res.code != 0 || res.errOut != "" || res.out != "✓ Qwen 3.5 9B is ready. (7.1 s)\n" {
		t.Fatalf("-q: exit %d, stdout %q, stderr %q", res.code, res.out, res.errOut)
	}
}

// A rollback (model.failed with rolled_back) ends with the problem layout and exit 1; with --json the
// envelope is on stdout and stderr is empty.
func TestFollowRollback(t *testing.T) {
	r := newFollowRig(t)
	r.onLoad = func(http.ResponseWriter) bool {
		r.ev.Publish(events.TypeModelLoading, "op_1", events.ModelLoading{Model: "qwen", OpID: "op_1"})
		r.ev.Publish(events.TypeModelFailed, "op_1", events.ModelFailed{
			Model: "qwen", Code: "LAI-LLM-009", Message: "The model Qwen 3.6 35B failed to start. The previous model was started again.",
			RolledBack: true, Restored: []string{"small-vlm"},
		})
		return false
	}
	res := r.exec(context.Background(), "load")
	if res.code != ExitFailed {
		t.Fatalf("exit %d, want 1; stderr %q", res.code, res.errOut)
	}
	want := "… Loading Qwen 3.5 9B\n" +
		"✗ The model Qwen 3.6 35B failed to start. The previous model was started again.  [LAI-LLM-009]\n" +
		"  → Fix: Choose a smaller model (`aictl models --loadable`) or check `aictl logs --unit llama`.\n"
	if res.errOut != want {
		t.Errorf("stderr:\n%s\nwant:\n%s", res.errOut, want)
	}
	if res.out != "" {
		t.Errorf("stdout = %q", res.out)
	}

	res = r.exec(context.Background(), "--json", "load")
	if res.code != ExitFailed || res.errOut != "" || !strings.Contains(res.out, `"code":"LAI-LLM-009"`) {
		t.Errorf("--json: exit %d, stdout %q, stderr %q", res.code, res.out, res.errOut)
	}
}

// Ctrl-C: stop waiting, one line, exit 130, and the agent sees no cancel and no second request.
func TestFollowCtrlC(t *testing.T) {
	still := "i Still running in the background. Check with `aictl status`.\n"
	t.Run("human", func(t *testing.T) {
		r := newFollowRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch, errb := r.execAsync(ctx, "load")
		r.ev.WaitActive(1)
		untilTrue(t, "the POST", func() bool { return r.h.agent.Count("/models/qwen/load") == 1 })
		r.ev.Publish(events.TypeModelLoading, "op_1", events.ModelLoading{Model: "qwen", OpID: "op_1"})
		untilTrue(t, "the progress line", func() bool { return strings.Contains(errb.String(), "Loading Qwen") })
		cancel() // Ctrl-C

		res := finish(t, ch)
		if res.code != ExitInterrupted {
			t.Fatalf("exit %d, want 130", res.code)
		}
		if res.errOut != "… Loading Qwen 3.5 9B\n"+still {
			t.Errorf("stderr = %q", res.errOut)
		}
		if strings.Contains(res.errOut, "LAI-CLI-010") || res.out != "" {
			t.Errorf("a human Ctrl-C prints one line and no problem layout: stderr %q stdout %q", res.errOut, res.out)
		}
		for _, q := range requestsOf(r) {
			switch {
			case q == "GET /health", strings.HasPrefix(q, "GET /events"), q == "POST /models/qwen/load":
			default:
				t.Errorf("Ctrl-C must send nothing to the agent, saw %q", q)
			}
		}
		if n := r.h.agent.Count("/models/qwen/load"); n != 1 {
			t.Errorf("load requests = %d", n)
		}
	})
	t.Run("json", func(t *testing.T) {
		r := newFollowRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch, _ := r.execAsync(ctx, "--json", "load")
		untilTrue(t, "the POST", func() bool { return r.h.agent.Count("/models/qwen/load") == 1 })
		r.ev.WaitActive(1)
		cancel()
		res := finish(t, ch)
		if res.code != ExitInterrupted || res.errOut != "" || !strings.Contains(res.out, `"code":"LAI-CLI-010"`) {
			t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.out, res.errOut)
		}
	})
}

// Ark sync's hook: Ctrl-C asks, and answering "keep waiting" carries on to the real end.
func TestFollowOnInterruptKeepsWaiting(t *testing.T) {
	r := newFollowRig(t)
	asked := make(chan struct{}, 1)
	r.hook = func(context.Context) progress.InterruptAction {
		asked <- struct{}{}
		return progress.KeepWaiting
	}
	ch, _ := r.execAsync(context.Background(), "load")
	untilTrue(t, "the POST", func() bool { return r.h.agent.Count("/models/qwen/load") == 1 })
	r.ev.WaitActive(1)
	// A press reaches the hook through the signal channel Follow installs.
	untilTrue(t, "the hook to be asked", func() bool {
		sendSIGINT(t)
		select {
		case <-asked:
			return true
		case <-time.After(20 * time.Millisecond):
			return false
		}
	})
	r.ev.Publish(events.TypeModelLoaded, "op_1", events.ModelLoaded{Model: "qwen", LoadMS: 7100})
	res := finish(t, ch)
	if res.code != 0 || !strings.Contains(res.out, "is ready") {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.out, res.errOut)
	}
}

// The deadline ends the wait with exit 7 and the standard problem layout.
func TestFollowDeadline(t *testing.T) {
	r := newFollowRig(t)
	res := r.exec(context.Background(), "--timeout", "150ms", "load")
	if res.code != ExitTimeout || !strings.Contains(res.errOut, "[LAI-CLI-004]") || !strings.Contains(res.errOut, "aictl status") {
		t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
	}
	for _, q := range requestsOf(r) {
		if strings.HasPrefix(q, "DELETE") {
			t.Errorf("a timeout must send no cancel: %q", q)
		}
	}
}

// The agent restarts mid-wait and stays down a moment: the CLI notices, re-reads the real state, and
// reports it (here: the load finished).
func TestFollowAgentRestartReportsRealState(t *testing.T) {
	r := newFollowRig(t)
	ch, errb := r.execAsync(context.Background(), "load")
	untilTrue(t, "the POST", func() bool { return r.h.agent.Count("/models/qwen/load") == 1 })
	r.ev.WaitActive(1)
	r.ev.Publish(events.TypeModelLoading, "op_1", events.ModelLoading{Model: "qwen", OpID: "op_1"})
	untilTrue(t, "the progress line", func() bool { return strings.Contains(errb.String(), "Loading Qwen") })

	r.setState("loaded")
	r.ev.Restart(true) // the stream drops, ids restart, 503 "starting" until the test says otherwise
	res := finish(t, ch)
	if res.code != 0 || res.out != "✓ Qwen 3.5 9B is ready. (7.1 s)\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.out, res.errOut)
	}
	if !strings.Contains(res.errOut, "i Lost the connection to the agent. Waiting for it to come back.\n") {
		t.Errorf("stderr = %q", res.errOut)
	}
}

// Failures before the wait starts keep their usual codes: a request the agent refuses closes the
// subscription; a stream the agent refuses is final.
func TestFollowFailuresBeforeTheWait(t *testing.T) {
	t.Run("request refused", func(t *testing.T) {
		r := newFollowRig(t)
		r.onLoad = func(w http.ResponseWriter) bool {
			fake.WriteEnvelope(w, http.StatusConflict, api.CodeBusy, "Another operation is running.", "Wait for it to finish.", nil)
			return true
		}
		res := r.exec(context.Background(), "load")
		if res.code != ExitRefused || !strings.Contains(res.errOut, "Another operation is running.  ["+api.CodeBusy+"]") {
			t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
		}
		r.ev.WaitActive(0)
	})
	t.Run("stream refused", func(t *testing.T) {
		r := newFollowRig(t)
		r.h.agent.RespondError(http.MethodGet, "/events", http.StatusForbidden, api.CodePeerNotOperator, "Not allowed.", "Ask an operator.", nil)
		res := r.exec(context.Background(), "load")
		if res.code != ExitDenied || !strings.Contains(res.errOut, "Not allowed.  ["+api.CodePeerNotOperator+"]") {
			t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
		}
		if r.h.agent.Count("/models/qwen/load") != 0 {
			t.Error("the request must not be sent when the subscription fails")
		}
	})
	t.Run("agent down", func(t *testing.T) {
		r := newFollowRig(t)
		r.h.agent.Close()
		res := r.exec(context.Background(), "load")
		if res.code != ExitUnreachable || !strings.Contains(res.errOut, "[LAI-CLI-001]") {
			t.Fatalf("exit %d, stderr %q", res.code, res.errOut)
		}
	})
}

func TestMarkFatal(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		fatal bool
	}{
		{"nil", nil, false},
		{"agent unreachable is retried", NewCLIError(LangEN, CodeUnreachable, ""), false},
		{"an answer from the agent is final", &APIError{Status: 404, Code: api.CodeNotFound}, true},
		{"a CLI error is final", NewCLIError(LangEN, CodeBadResponse, ""), true},
		{"deadline is final", context.DeadlineExceeded, true},
		{"cancellation is final", fmt.Errorf("x: %w", context.Canceled), true},
		{"a plain network error is retried", fmt.Errorf("connection reset"), false},
	}
	for _, tt := range tests {
		got := markFatal(tt.err)
		if (got != nil && progress.IsFatal(got)) != tt.fatal {
			t.Errorf("%s: fatal = %v, want %v", tt.name, progress.IsFatal(got), tt.fatal)
		}
		if tt.err != nil && got == nil {
			t.Errorf("%s: markFatal dropped the error", tt.name)
		}
	}
}

func TestLocaleUTF8(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tt := range []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"LANG": "en_US.UTF-8"}, true},
		{map[string]string{"LANG": "en_US.utf8"}, true},
		{map[string]string{"LANG": "C"}, false},
		{map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false},
	} {
		if got := localeUTF8(get(tt.env)); got != tt.want {
			t.Errorf("%v: %v, want %v", tt.env, got, tt.want)
		}
	}
}
