package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("line is not JSON: %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func TestNew_NoTimeKeyAndStandardKeys(t *testing.T) {
	t.Setenv(EnvLevel, "")
	var buf bytes.Buffer
	log := New(&buf, nil)
	log.Info("agent.started", "component", "api", "req_id", "req_4f2k9m1q8z0c", "duration_ms", 12)
	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines", len(lines))
	}
	m := lines[0]
	if _, has := m["time"]; has {
		t.Fatalf("output has a time key: %v", m)
	}
	if m["level"] != "INFO" || m["msg"] != "agent.started" || m["component"] != "api" || m["req_id"] != "req_4f2k9m1q8z0c" || m["duration_ms"] != float64(12) {
		t.Fatalf("unexpected line: %v", m)
	}
}

func TestNew_TimeIsDroppedInsideGroupsOnlyAtTopLevel(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, nil).Info("m", slog.Group("g", slog.String("time", "10:00")))
	m := decodeLines(t, &buf)[0]
	if m["g"].(map[string]any)["time"] != "10:00" {
		t.Fatalf("a nested time attribute must be kept: %v", m)
	}
}

func TestNew_DefaultLevelIsInfo(t *testing.T) {
	t.Setenv(EnvLevel, "")
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	log := New(&buf, lv)
	log.Debug("hidden")
	log.Info("shown")
	if lv.Level() != slog.LevelInfo {
		t.Fatalf("level = %v", lv.Level())
	}
	if lines := decodeLines(t, &buf); len(lines) != 1 || lines[0]["msg"] != "shown" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestNew_LevelFromEnvironment(t *testing.T) {
	for env, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug, " warn ": slog.LevelWarn, "error": slog.LevelError, "info": slog.LevelInfo,
	} {
		t.Setenv(EnvLevel, env)
		lv := new(slog.LevelVar)
		New(&bytes.Buffer{}, lv)
		if lv.Level() != want {
			t.Errorf("%s=%q: level = %v, want %v", EnvLevel, env, lv.Level(), want)
		}
	}
}

func TestNew_InvalidEnvironmentFallsBackToInfoWithAWarning(t *testing.T) {
	t.Setenv(EnvLevel, "verbose")
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	New(&buf, lv)
	if lv.Level() != slog.LevelInfo {
		t.Fatalf("level = %v, want info", lv.Level())
	}
	lines := decodeLines(t, &buf)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || !strings.Contains(lines[0]["msg"].(string), EnvLevel) {
		t.Fatalf("expected one warning about %s, got %v", EnvLevel, lines)
	}
}

func TestSetLevel_ChangesAtRuntime(t *testing.T) {
	t.Setenv(EnvLevel, "info")
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	log := New(&buf, lv)

	log.Debug("d1")
	if err := SetLevel(lv, "debug"); err != nil {
		t.Fatal(err)
	}
	log.Debug("d2")
	if err := SetLevel(lv, "error"); err != nil {
		t.Fatal(err)
	}
	log.Warn("w1")
	log.Error("e1")

	var msgs []string
	for _, m := range decodeLines(t, &buf) {
		msgs = append(msgs, m["msg"].(string))
	}
	if got := strings.Join(msgs, ","); got != "d2,e1" {
		t.Fatalf("messages = %s, want d2,e1", got)
	}
}

func TestSetLevel_InvalidKeepsTheLevel(t *testing.T) {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelWarn)
	if err := SetLevel(lv, "loud"); err == nil {
		t.Fatal("expected an error")
	}
	if lv.Level() != slog.LevelWarn {
		t.Fatalf("level changed to %v", lv.Level())
	}
}

func TestNew_ScrubsMessagesAndAttributes(t *testing.T) {
	t.Setenv(EnvLevel, "")
	var buf bytes.Buffer
	log := New(&buf, nil)
	tok := fakeToken("lat")
	log.Info("key shown "+tok, "api_key", tok, "peer", fakeTailnetIP(70))
	out := buf.String()
	if strings.Contains(out, hidden(tok)) || strings.Contains(out, fakeTailnetIP(70)) {
		t.Fatalf("secret in output: %s", out)
	}
}

func TestNew_ConcurrentUseIsSafe(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, nil)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				log.Info("m", "api_key", fakeToken("nod"), "n", j)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("interleaved or broken line: %q", ln)
		}
	}
}
