package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

const (
	bakPath      = testPath + ".bak"
	testVersion  = "9.9.9-test"
	corruptStamp = "2026-11-03T10:00:00Z"
)

var (
	errEIO  error = syscall.EIO
	errBoom       = errors.New("boom")
)

type rawJSON = json.RawMessage

// fixture reads testdata/<name>.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeClock returns 2026-11-03T10:00:00Z, then one more second on every call.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	c, err := time.Parse(time.RFC3339, corruptStamp)
	if err != nil {
		panic(err)
	}
	return &fakeClock{t: c.Add(-time.Second)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// open opens a store over m with the fake clock and registers Close.
func open(t testing.TB, m *fake.MemFS, tweak ...func(*state.Options)) *state.Store {
	t.Helper()
	opts := state.Options{FS: m, AgentVersion: testVersion, Now: newClock().Now}
	for _, f := range tweak {
		f(&opts)
	}
	s, err := state.Open(testPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seeded returns a MemFS with the given files (path -> content) already on disk.
func seeded(files map[string][]byte) *fake.MemFS {
	m := fake.New()
	for p, c := range files {
		m.Seed(p, c)
	}
	return m
}

// onDisk decodes a file of m into the typed State.
func onDisk(t testing.TB, m *fake.MemFS, path string) state.State {
	t.Helper()
	b, ok := m.Contents(path)
	if !ok {
		t.Fatalf("%s does not exist; files: %s", path, m)
	}
	var st state.State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("%s is not valid state JSON: %v\n%s", path, err, b)
	}
	return st
}

// generic decodes JSON into any, for structural comparison of whole documents.
func generic(t testing.TB, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	return v
}

// withPrefix lists the names in the state directory that start with prefix.
func withPrefix(m *fake.MemFS, prefix string) []string {
	var out []string
	for _, n := range m.Names(testDir) {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

func setDefault(id string) func(*state.State) error {
	return func(s *state.State) error {
		s.Models.Default = &id
		return nil
	}
}

func defaultModel(t testing.TB, s state.State) string {
	t.Helper()
	if s.Models.Default == nil {
		t.Fatal("models.default is nil")
	}
	return *s.Models.Default
}

// codeOf is the doctor code the store's finding kinds map to (agent.md section 8.1).
var codeOf = map[state.FindingKind]string{
	state.KindDamaged:    "LAI-AGT-003",
	state.KindUnwritable: "LAI-AGT-004",
}
