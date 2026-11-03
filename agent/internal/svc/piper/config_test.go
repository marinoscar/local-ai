package piper

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

func TestParseBlockDefaultsAndOverrides(t *testing.T) {
	d, err := ParseBlock(nil)
	if err != nil || d != DefaultConfig() {
		t.Fatalf("empty block = %+v, %v", d, err)
	}
	if !d.Enabled || d.Version != "2023.11.14-2" || d.LengthScale != 1.05 || d.DefaultVoice.EN != "auto" || d.DefaultVoice.ES != "auto" {
		t.Errorf("defaults = %+v", d)
	}
	c, err := ParseBlock(config.RawBlock("enabled: false\nlength_scale: 1.2\ndefault_voice: {es: piper-es-medium}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled || c.LengthScale != 1.2 || c.DefaultVoice.ES != "piper-es-medium" || c.DefaultVoice.EN != "auto" || c.Version != DefaultVersion {
		t.Errorf("overrides = %+v", c)
	}
	if _, err := ParseBlock(config.RawBlock("enabled: [")); err == nil {
		t.Error("a block that does not parse must be an error")
	}
	if c.DefaultVoiceFor("fr") != "auto" || c.DefaultVoiceFor("es") != "piper-es-medium" {
		t.Error("DefaultVoiceFor")
	}
}

func TestFromConfigFallsBackToDefaults(t *testing.T) {
	if got := FromConfig(config.Config{}); got != DefaultConfig() {
		t.Errorf("no block: %+v", got)
	}
	cfg := config.Config{Blocks: map[string]config.RawBlock{"piper": config.RawBlock("length_scale: 1.5")}}
	if got := FromConfig(cfg); got.LengthScale != 1.5 {
		t.Errorf("block read: %+v", got)
	}
	cfg.Blocks["piper"] = config.RawBlock("{{{")
	if got := FromConfig(cfg); got != DefaultConfig() {
		t.Errorf("broken block: %+v", got)
	}
}

func TestBlockValidator(t *testing.T) {
	cases := []struct {
		name, block string
		wantPaths   []string
	}{
		{"the documented block", "enabled: true\nversion: \"2023.11.14-2\"\nlength_scale: 1.05\ndefault_voice: {en: auto, es: auto}\n", nil},
		{"empty", "", nil},
		{"named defaults", "default_voice: {en: piper-en-medium, es: piper-es-medium}", nil},
		{"unknown key", "volume: 11", []string{""}},
		{"length scale too low", "length_scale: 0.1", []string{"length_scale"}},
		{"length scale too high", "length_scale: 9", []string{"length_scale"}},
		{"length scale at the edges", "length_scale: 0.3", nil},
		{"bad version", "version: \"../../etc\"", []string{"version"}},
		{"empty version", "version: \"\"", []string{"version"}},
		{"default voice with a slash", "default_voice: {en: \"a/b\"}", []string{"default_voice.en"}},
		{"empty default voice", "default_voice: {es: \"\"}", []string{"default_voice.es"}},
		{"wrong type", "enabled: maybe", []string{""}},
		{"two problems", "length_scale: 0\nversion: x y", []string{"version", "length_scale"}},
	}
	for _, c := range cases {
		probs := BlockValidator{}.ValidateBlock(config.RawBlock(c.block))
		if len(probs) != len(c.wantPaths) {
			t.Errorf("%s: %d problems %+v, want %d", c.name, len(probs), probs, len(c.wantPaths))
			continue
		}
		for i, p := range probs {
			if p.Path != c.wantPaths[i] || p.Severity != config.SeverityError || p.Message == "" {
				t.Errorf("%s: problem %d = %+v, want path %q as an error", c.name, i, p, c.wantPaths[i])
			}
		}
	}
}

func TestValidatorRunsThroughTheConfigLoader(t *testing.T) {
	good := "schema_version: 1\nnode: {id: ai-n3}\nprofile: cpu-only\npiper:\n  length_scale: 1.1\n"
	bad := "schema_version: 1\nnode: {id: ai-n3}\nprofile: cpu-only\npiper:\n  length_scale: 100\n"
	h := &config.Hooks{Blocks: map[string]config.BlockValidator{"piper": BlockValidator{}}}
	if _, probs := config.Load([]byte(good), h); probs.HasErrors() {
		t.Errorf("good file: %+v", probs)
	}
	_, probs := config.Load([]byte(bad), h)
	found := false
	for _, p := range probs.Errors() {
		found = found || p.Path == "piper.length_scale"
	}
	if !found {
		t.Errorf("bad file: want an error at piper.length_scale, got %+v", probs)
	}
}

func TestLimiterQueuesAndRefuses(t *testing.T) {
	l := NewLimiter(2, 4)
	queued := make(chan struct{}, 8)
	l.onWait = func() { queued <- struct{}{} }
	ctx := context.Background()

	var releases []func()
	for i := 0; i < 2; i++ {
		rel, err := l.Acquire(ctx)
		if err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	if run, q := l.Stats(); run != 2 || q != 0 {
		t.Errorf("stats = %d running, %d queued", run, q)
	}

	// Four more wait, each one counted by the limiter before we go on.
	type result struct {
		rel func()
		err error
	}
	results := make(chan result, 4)
	for i := 0; i < 4; i++ {
		go func() { rel, err := l.Acquire(ctx); results <- result{rel, err} }()
		<-queued
	}
	if run, q := l.Stats(); run != 2 || q != 4 {
		t.Errorf("stats = %d running, %d queued, want 2 and 4", run, q)
	}
	// The seventh request is refused at once.
	if _, err := l.Acquire(ctx); err != ErrBusy {
		t.Errorf("7th request: err = %v, want ErrBusy", err)
	}
	// Free the slots one by one: the queue drains in order and the limits hold.
	for i := 0; i < 4; i++ {
		releases[0]()
		releases = releases[1:]
		r := <-results
		if r.err != nil {
			t.Fatalf("queued request failed: %v", r.err)
		}
		releases = append(releases, r.rel)
		if run, _ := l.Stats(); run > 2 {
			t.Fatalf("%d running, the limit is 2", run)
		}
	}
	for _, rel := range releases {
		rel()
	}
	if run, q := l.Stats(); run != 0 || q != 0 {
		t.Errorf("after everything: %d running, %d queued", run, q)
	}
}

func TestLimiterWaitEndsWithTheContext(t *testing.T) {
	l := NewLimiter(1, 1)
	hold, _ := l.Acquire(context.Background())
	defer hold()
	queued := make(chan struct{})
	l.onWait = func() { close(queued) }
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := l.Acquire(ctx); errc <- err }()
	<-queued
	cancel()
	if err := <-errc; err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, q := l.Stats(); q != 0 {
		t.Errorf("a cancelled waiter must leave the queue, %d left", q)
	}
	// An already cancelled context does not even try.
	if _, err := l.Acquire(ctx); err != context.Canceled {
		t.Errorf("cancelled context: %v", err)
	}
}

func TestLimiterIsSafeUnderContention(t *testing.T) {
	l := NewLimiter(2, 100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	running, peak := 0, 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			mu.Lock()
			running--
			mu.Unlock()
			rel()
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Errorf("peak concurrency %d, want at most 2", peak)
	}
}

func TestLimiterDefaults(t *testing.T) {
	l := NewLimiter(0, -1)
	if cap(l.slots) != 2 || l.maxQ != 4 {
		t.Errorf("defaults = %d running, %d queued", cap(l.slots), l.maxQ)
	}
	if l := NewLimiter(3, 0); cap(l.slots) != 3 || l.maxQ != 0 {
		t.Errorf("explicit zero queue = %d, %d", cap(l.slots), l.maxQ)
	}
	_ = strings.Repeat
}
