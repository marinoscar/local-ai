package cli

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

const testToken = "lat_testtoken0123456789"

// writeToken writes a token file with a trailing newline (the agent tolerates one) and returns its path.
func writeToken(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent-token")
	if err := os.WriteFile(p, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sleeper records the waits the client asked for instead of waiting.
type sleeper struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d)
}

// dialErr is the error net.Dialer returns for a failed connect with errno e.
func dialErr(e syscall.Errno) error {
	return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", e)}
}

// harness builds a Client against a fake agent with a token file and a recording sleeper.
type harness struct {
	agent  *fake.Agent
	sleeps *sleeper
	warns  []*CLIError
	stderr bytes.Buffer
	reads  int
	cfg    Config
}

func newHarness(t *testing.T, o fake.Options) *harness {
	t.Helper()
	h := &harness{agent: fake.New(t, o), sleeps: &sleeper{}}
	tokenPath := writeToken(t)
	h.cfg = Config{
		Socket:    h.agent.SocketPath,
		URL:       h.agent.URL,
		TokenFile: tokenPath,
		Version:   "0.0.0-dev",
		Stderr:    &h.stderr,
		Sleep:     h.sleeps.Sleep,
		Warn:      func(e *CLIError) { h.warns = append(h.warns, e) },
		ReadFile: func(p string) ([]byte, error) {
			h.reads++
			return os.ReadFile(p)
		},
	}
	return h
}

func (h *harness) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(h.cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// unreadable makes the token file unreadable the way a user outside the group sees it.
func (h *harness) unreadable() {
	h.cfg.ReadFile = func(p string) ([]byte, error) {
		h.reads++
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
	}
}
