package config_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

type rig struct {
	store *config.Store
	path  string
	clock *fake.Clock
	ch    <-chan config.Change
}

func newRig(t *testing.T, content string, hooks *config.Hooks) *rig {
	t.Helper()
	root, path := nodeRoot(t, content)
	if hooks == nil {
		hooks = catalogHooks()
	}
	clock := fake.NewClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := config.Open(config.Options{Root: root, Clock: clock, Hooks: *hooks})
	ch, cancel := s.Subscribe()
	t.Cleanup(cancel)
	return &rig{store: s, path: path, clock: clock, ch: ch}
}

// edit rewrites node.yaml and moves its mtime so the next poll sees it.
func (r *rig) edit(t *testing.T, content string) {
	t.Helper()
	writeNode(t, r.path, content)
	bump(t, r.path)
}

func TestPollAppliesAValidEditWithinOnePoll(t *testing.T) {
	r := newRig(t, example(t), nil)
	if err := r.store.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.store.Stop(context.Background()) })

	r.edit(t, swap(t, example(t), "level: info   ", "level: debug  "))
	r.clock.Advance(config.DefaultPollInterval) // exactly one poll interval
	c := expectChange(t, r.ch)
	if c.Source != config.SourceReload || !reflect.DeepEqual(c.Changed, []string{"logging.level"}) || len(c.Restart) != 0 {
		t.Errorf("change = %+v", c)
	}
	if c.Old.Logging.Level != "info" || c.New.Logging.Level != "debug" || c.Status.Code != "" {
		t.Errorf("change = %+v", c)
	}
	if r.store.Get().Logging.Level != "debug" {
		t.Error("the edit must be live")
	}
}

func TestPollIntervalIsTenSecondsByDefault(t *testing.T) {
	if config.DefaultPollInterval != 10*time.Second {
		t.Fatalf("interval = %v, agent.md 3.4 says 10 s", config.DefaultPollInterval)
	}
	r := newRig(t, example(t), nil)
	if err := r.store.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.store.Stop(context.Background()) })
	r.edit(t, swap(t, example(t), "level: info   ", "level: debug  "))
	r.clock.Advance(9 * time.Second)
	expectNoChange(t, r.ch)
	r.clock.Advance(time.Second)
	expectChange(t, r.ch)
}

func TestInvalidEditIsIgnoredAndTheLastValidConfigStays(t *testing.T) {
	r := newRig(t, example(t), nil)
	r.edit(t, swap(t, example(t), "concurrency: 1  ", "concurrency: 9  "))
	if !r.store.CheckNow() {
		t.Fatal("the edit must be noticed")
	}
	c := expectChange(t, r.ch)
	if c.Status.Code != config.CodeNodeYAMLInvalid || c.Status.Severity != config.SeverityError || len(c.Changed) != 0 {
		t.Errorf("change = %+v", c)
	}
	if p, ok := findProblem(c.Status.Problems, "cloud.concurrency"); !ok || p.Line == 0 {
		t.Errorf("problems = %+v", c.Status.Problems)
	}
	if got := r.store.Get().Cloud.Concurrency; got != 1 {
		t.Errorf("last valid config must stay, concurrency = %d", got)
	}
	if st := r.store.Status(); st.Code != config.CodeNodeYAMLInvalid || st.Safe {
		t.Errorf("status = %+v (not safe defaults: a valid config was loaded at start)", st)
	}

	// Still broken, untouched: no second report.
	if r.store.CheckNow() {
		t.Error("an unchanged file is not looked at again")
	}
	expectNoChange(t, r.ch)

	// Fixed: applied, alarm cleared.
	r.edit(t, swap(t, example(t), "concurrency: 1  ", "concurrency: 2  "))
	r.store.CheckNow()
	c = expectChange(t, r.ch)
	if c.Status.Code != "" || !reflect.DeepEqual(c.Changed, []string{"cloud.concurrency"}) {
		t.Errorf("change = %+v", c)
	}
	if r.store.Get().Cloud.Concurrency != 2 {
		t.Error("the fix must be live")
	}
}

func TestSyntaxErrorEditIsIgnored(t *testing.T) {
	r := newRig(t, example(t), nil)
	r.edit(t, "schema_version: 1\nnode: [")
	r.store.CheckNow()
	c := expectChange(t, r.ch)
	if c.Status.Code != config.CodeNodeYAMLInvalid || r.store.Get().Node.ID != "ai-n3" {
		t.Errorf("change = %+v", c)
	}
}

func TestTouchWithoutEditIsIgnored(t *testing.T) {
	r := newRig(t, example(t), nil)
	bump(t, r.path) // new mtime, same bytes
	if r.store.CheckNow() {
		t.Error("a touch is not a change (the sha256 matches)")
	}
	expectNoChange(t, r.ch)
	if r.store.CheckNow() {
		t.Error("and the touch is remembered")
	}
}

func TestSameContentRewrittenIsNotAChange(t *testing.T) {
	r := newRig(t, example(t), nil)
	r.edit(t, example(t))
	if r.store.CheckNow() {
		t.Error("rewriting identical bytes is not a change")
	}
}

func TestCommentOnlyEditIsAppliedSilently(t *testing.T) {
	// A comment-only edit is applied (bytes differ) but changes no setting: no Change is sent.
	r := newRig(t, example(t), nil)
	r.edit(t, example(t)+"# a note from a human\n")
	if !r.store.CheckNow() {
		t.Fatal("the file changed")
	}
	expectNoChange(t, r.ch)
}

func TestRestartOnlyFieldsKeepTheRunningValueAndReportAGT016(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		path     string
		get      func(config.Config) any
		running  any
	}{
		{"node.id", "id: ai-n3 ", "id: ai-n4 ", "node.id", func(c config.Config) any { return c.Node.ID }, "ai-n3"},
		{"ports.agent", "agent: 7070 ", "agent: 7171 ", "ports.agent", func(c config.Config) any { return c.Ports.Agent }, 7070},
		{"ports.llama", "llama: 8080\n", "llama: 8181\n", "ports.llama", func(c config.Config) any { return c.Ports.Llama }, 8080},
		{"ports.kiwix", "kiwix: 8888", "kiwix: 8989", "ports.kiwix", func(c config.Config) any { return c.Ports.Kiwix }, 8888},
		{"ports.whisper", "whisper: 8081", "whisper: 8182", "ports.whisper", func(c config.Config) any { return c.Ports.Whisper }, 8081},
		{"cloud.url", "url: https://ai.marin.cr ", "url: https://ai2.marin.cr", "cloud.url", func(c config.Config) any { return c.Cloud.URL }, "https://ai.marin.cr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, example(t), nil)
			r.edit(t, swap(t, example(t), tt.old, tt.new))
			r.store.CheckNow()
			c := expectChange(t, r.ch)
			want := []config.RestartNeed{{Path: tt.path, Target: "agent"}}
			if !reflect.DeepEqual(c.Restart, want) {
				t.Errorf("Restart = %+v, want %+v", c.Restart, want)
			}
			if !reflect.DeepEqual(r.store.Status().Pending, want) {
				t.Errorf("Pending = %+v, want %+v", r.store.Status().Pending, want)
			}
			if got := tt.get(r.store.Get()); got != tt.running {
				t.Errorf("effective %s = %v: it must keep the running value %v until restart", tt.path, got, tt.running)
			}
			if st := r.store.Status(); st.Code != "" {
				t.Errorf("a pending restart is not an error: %+v", st)
			}

			// Putting the old value back clears the pending restart.
			r.edit(t, example(t))
			r.store.CheckNow()
			if p := r.store.Status().Pending; len(p) != 0 {
				t.Errorf("Pending after revert = %+v", p)
			}
		})
	}
}

func TestLiveFieldsApplyAtOnceWithoutARestartNotice(t *testing.T) {
	tests := []struct {
		name, old, new, path string
	}{
		{"cloud.allow", "catalog.refresh, agent.update", "catalog.refresh", "cloud.allow"},
		{"cloud.poll", "state_seconds: 60", "state_seconds: 120", "cloud.poll.state_seconds"},
		{"sync.*", "concurrency: 2 ", "concurrency: 3 ", "sync"},
		{"ui.language", "language: auto                    # auto", "language: es                      # auto", "ui.language"},
		{"portal.*", "kiosk: false", "kiosk: true", "portal"},
		{"logging.level", "level: info   ", "level: warn   ", "logging.level"},
		{"tailscale.manage", "manage: true ", "manage: false", "tailscale.manage"},
		{"tailscale.serve_pattern", "serve_pattern: B ", "serve_pattern: none", "tailscale.serve_pattern"},
		{"profile", "profile: small-8gb ", "profile: cpu-only ", "profile"},
		{"profile_overrides", "budget_bytes: null ", "budget_bytes: 1000", "profile_overrides.budget_bytes"},
		{"llama.legacy_aliases", "[qwen3.5-9b]", "[qwen3.5-9b, qwen-old]", "llama.legacy_aliases"},
		{"llama.load_timeout_seconds", "load_timeout_seconds: 300", "load_timeout_seconds: 600", "llama.load_timeout_seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, example(t), nil)
			r.edit(t, swap(t, example(t), tt.old, tt.new))
			r.store.CheckNow()
			c := expectChange(t, r.ch)
			if len(c.Restart) != 0 || len(r.store.Status().Pending) != 0 {
				t.Errorf("a live field must not ask for a restart: %+v", c.Restart)
			}
			if len(c.Changed) != 1 || !strings.HasPrefix(c.Changed[0], tt.path) {
				t.Errorf("Changed = %v, want %s", c.Changed, tt.path)
			}
		})
	}
}

func TestLlamaEnvChangeIsAppliedButNeedsAnExplicitLlamaRestart(t *testing.T) {
	r := newRig(t, example(t), nil)
	r.edit(t, swap(t, example(t), "extra_env: {}", `extra_env: {GGML_VK_SUBALLOCATION_BLOCK_SIZE: "4294967296"}`))
	r.store.CheckNow()
	c := expectChange(t, r.ch)
	want := []config.RestartNeed{{Path: "llama.extra_env", Target: "llama"}}
	if !reflect.DeepEqual(c.Restart, want) {
		t.Errorf("Restart = %+v, want %+v (aictl restart llama, never automatic)", c.Restart, want)
	}
	if r.store.Get().Llama.ExtraEnv["GGML_VK_SUBALLOCATION_BLOCK_SIZE"] != "4294967296" {
		t.Error("the new env must be in the effective config so the drop-in can be regenerated")
	}
	if len(r.store.Status().Pending) != 0 {
		t.Error("only agent restarts stay pending: the store cannot know when llama restarted")
	}
}

func TestOtherLlamaSettingsAlsoNeedALlamaRestart(t *testing.T) {
	for _, tt := range []struct{ old, new, path string }{
		{"mode: router ", "mode: single ", "llama.mode"},
		{"backend: cuda ", "backend: cpu  ", "llama.backend"},
		{"commit: f3586e66b", "commit: 0123abc  ", "llama.commit"},
		{"cuda_arch: null ", "cuda_arch: 75   ", "llama.cuda_arch"},
	} {
		r := newRig(t, example(t), nil)
		r.edit(t, swap(t, example(t), tt.old, tt.new))
		r.store.CheckNow()
		c := expectChange(t, r.ch)
		if want := []config.RestartNeed{{Path: tt.path, Target: "llama"}}; !reflect.DeepEqual(c.Restart, want) {
			t.Errorf("%s: Restart = %+v", tt.path, c.Restart)
		}
	}
}

func TestDeletedFileKeepsTheLastConfigAndReportsAGT001(t *testing.T) {
	r := newRig(t, example(t), nil)
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	r.store.CheckNow()
	c := expectChange(t, r.ch)
	if c.Status.Code != config.CodeNodeYAMLMissing || c.Status.Severity != config.SeverityError || r.store.Get().Node.ID != "ai-n3" {
		t.Errorf("change = %+v", c)
	}
	if r.store.Status().Safe {
		t.Error("the agent keeps its last config; it is not on safe defaults")
	}
	if r.store.CheckNow() {
		t.Error("still missing: nothing new")
	}

	// The file comes back (restored from .bak): applied, alarm cleared.
	r.edit(t, swap(t, example(t), "level: info   ", "level: debug  "))
	r.store.CheckNow()
	c = expectChange(t, r.ch)
	if c.Status.Code != "" || r.store.Get().Logging.Level != "debug" {
		t.Errorf("change = %+v", c)
	}
}

func TestFixingABrokenStartKeepsRestartRequiredFieldsPending(t *testing.T) {
	r := newRig(t, swap(t, example(t), "mode: router ", "mode: both   "), nil)
	if !r.store.Status().Safe {
		t.Fatal("must start on safe defaults")
	}
	r.edit(t, example(t))
	r.store.CheckNow()
	c := expectChange(t, r.ch)
	if c.Status.Code != "" || c.Status.Safe == false {
		t.Errorf("status = %+v: healthy file, but the agent still runs the safe defaults", c.Status)
	}
	got := r.store.Get()
	if got.Node.ID != "unconfigured" || got.CloudEnabled() {
		t.Errorf("node.id and cloud.url are restart-required, so they stay on the running values: %+v", got.Node)
	}
	if got.Logging.Level != "info" || !got.Tailscale.Manage {
		t.Errorf("live fields apply at once: %+v", got)
	}
	paths := map[string]bool{}
	for _, p := range r.store.Status().Pending {
		paths[p.Path] = true
	}
	if !paths["node.id"] || !paths["cloud.url"] || len(paths) != 2 {
		t.Errorf("Pending = %+v, want node.id and cloud.url", r.store.Status().Pending)
	}
}

func TestReloadNowRevalidatesAgainstTheCatalog(t *testing.T) {
	loaded := false
	hooks := &config.Hooks{ProfileExists: func(string) bool { return loaded }}
	r := newRig(t, example(t), hooks)
	if st := r.store.Status(); st.Code != config.CodeProfileMissing {
		t.Fatalf("status = %+v, want the missing-profile warning while the catalog is empty", st)
	}
	loaded = true
	r.store.ReloadNow()
	c := expectChange(t, r.ch)
	if c.Status.Code != "" || len(c.Changed) != 0 {
		t.Errorf("change = %+v", c)
	}
}

func TestSlowSubscriberSeesTheLatestChange(t *testing.T) {
	r := newRig(t, example(t), nil)
	levels := []string{"debug", "warn", "error", "info"}
	for i := 0; i < 40; i++ {
		r.edit(t, swap(t, example(t), "level: info   ", "level: "+levels[i%4]+"   "))
		r.store.CheckNow()
	}
	var last config.Change
	for drained := false; !drained; {
		select {
		case c := <-r.ch:
			last = c
		default:
			drained = true
		}
	}
	if last.New.Logging.Level != r.store.Get().Logging.Level {
		t.Errorf("a subscriber that fell behind must still end on the latest change: %q vs %q", last.New.Logging.Level, r.store.Get().Logging.Level)
	}
}

func TestSubscribeCancelClosesTheChannel(t *testing.T) {
	r := newRig(t, example(t), nil)
	ch, cancel := r.store.Subscribe()
	cancel()
	cancel() // idempotent
	if _, ok := <-ch; ok {
		t.Error("channel must be closed")
	}
}

func TestStartTwiceIsAnErrorAndStopEndsThePoll(t *testing.T) {
	r := newRig(t, example(t), nil)
	if r.store.Name() != "config" {
		t.Errorf("name = %q", r.store.Name())
	}
	if err := r.store.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
	if r.clock.Tickers() != 1 {
		t.Errorf("tickers = %d", r.clock.Tickers())
	}
	if err := r.store.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.clock.Tickers() != 0 {
		t.Error("Stop must stop the ticker")
	}
	r.edit(t, swap(t, example(t), "level: info   ", "level: debug  "))
	r.clock.Advance(time.Minute)
	expectNoChange(t, r.ch)
	if err := r.store.Stop(context.Background()); err != nil {
		t.Errorf("Stop twice: %v", err)
	}
}

func TestContextCancellationEndsThePoll(t *testing.T) {
	r := newRig(t, example(t), nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.store.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := r.store.Stop(stopCtx); err != nil {
		t.Errorf("the loop must have exited with the context: %v", err)
	}
}

func TestStatusIsACopy(t *testing.T) {
	r := newRig(t, example(t)+"\nfuture: 1\n", nil)
	st := r.store.Status()
	st.Problems[0].Message = "changed"
	if r.store.Status().Problems[0].Message == "changed" {
		t.Error("Status must hand out a copy")
	}
}

func TestEditsAreSeenThroughTheRealClockToo(t *testing.T) {
	root, path := nodeRoot(t, example(t))
	s := config.Open(config.Options{Root: root, Interval: 10 * time.Millisecond})
	ch, cancel := s.Subscribe()
	defer cancel()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()
	writeNode(t, path, swap(t, example(t), "level: info   ", "level: debug  "))
	bump(t, path)
	if c := expectChange(t, ch); c.New.Logging.Level != "debug" {
		t.Errorf("change = %+v", c)
	}
}
