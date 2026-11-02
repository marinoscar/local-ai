package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

const examplePath = "testdata/node-ai-n3.yaml"

// example returns the agent.md 3.4 example verbatim.
func example(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// swap replaces exactly one occurrence of old in s; it fails the test if old is not there once.
func swap(t testing.TB, s, old, repl string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("swap: %q occurs %d times in the example, want 1", old, n)
	}
	return strings.Replace(s, old, repl, 1)
}

// nodeRoot makes a temp filesystem root with /etc/localai/node.yaml holding content (none when
// content is empty) and returns the root and the node.yaml path.
func nodeRoot(t testing.TB, content string) (root, path string) {
	t.Helper()
	root = t.TempDir()
	path = config.NodeYAMLPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		writeNode(t, path, content)
	}
	return root, path
}

func writeNode(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

// bump moves a file's mtime forward so the poll sees an edit even when the size is unchanged.
var bumpClock = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func bump(t testing.TB, path string) {
	t.Helper()
	bumpClock = bumpClock.Add(time.Minute)
	if err := os.Chtimes(path, bumpClock, bumpClock); err != nil {
		t.Fatal(err)
	}
}

func findProblem(ps config.Problems, path string) (config.Problem, bool) {
	for _, p := range ps {
		if p.Path == path {
			return p, true
		}
	}
	return config.Problem{}, false
}

func listDir(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
