package auth

import (
	"bytes"
	"github.com/marinoscar/local-ai/agent/internal/leakcheck"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// t0 is the fake clock's start. Nothing in the package reads the wall clock.
var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newClock() *cfake.Clock { return cfake.NewClock(t0) }

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newLogger returns a scrubbing JSON logger at debug level, as the daemon builds it.
func newLogger() (*slog.Logger, *syncBuf) {
	buf := &syncBuf{}
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	return logx.New(buf, lv), buf
}

// ownPolicy is a policy an unprivileged test user can satisfy: the file is owned by whoever runs
// the test.
func ownPolicy(mode os.FileMode) config.SecretPolicy {
	return config.SecretPolicy{Mode: mode, UID: os.Getuid(), GID: -1}
}

// recAudit records audit events.
type recAudit struct {
	mu     sync.Mutex
	events []audit.Event
	err    error
}

func (a *recAudit) Write(ev audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return a.err
}

func (a *recAudit) all() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.events...)
}

func (a *recAudit) actions() []string {
	var out []string
	for _, e := range a.all() {
		out = append(out, e.Action)
	}
	return out
}

// writeFile writes content with the mode, regardless of the umask.
func writeFile(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// bump gives the file a new mtime, as a rotation by another process would.
func bump(t testing.TB, path string, d time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	when := info.ModTime().Add(d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// fakeToken returns a well-formed token that is not the agent's: built at run time, so no literal
// token sits in the source (scripts/check-redaction.sh).
func fakeToken() string { return TokenPrefix + strings.Repeat("A", 43) }

// tempPaths returns token and PIN paths inside a fresh temp directory.
func tempPaths(t testing.TB) (token, pin string) {
	dir := t.TempDir()
	return filepath.Join(dir, "agent-token"), filepath.Join(dir, "admin-pin")
}

// mustNotContain fails when out holds any of the secrets.
// mustNotLeak is mustNotContain for a JSON log: it ignores generated fields (timestamps, durations,
// ids), whose digits can contain a short secret such as a PIN by chance.
func mustNotLeak(t testing.TB, what, log string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if leakcheck.Contains(log, s) {
			t.Errorf("%s contains a secret (%d bytes starting %q)", what, len(s), s[:min(len(s), 6)])
		}
	}
}

func mustNotContain(t testing.TB, what, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(out, s) {
			t.Errorf("%s contains a secret (%d bytes starting %q)", what, len(s), s[:min(len(s), 6)])
		}
	}
}
