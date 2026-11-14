package app

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #483: /etc/localai must let operators pass through to agent-token, and nothing else may change.
func TestEnsureOperatorTraverse(t *testing.T) {
	cases := []struct {
		name    string
		mode    os.FileMode
		want    os.FileMode
		changed bool
	}{
		{"old node 0750 gets the pass-through bit", 0o750, 0o751, true},
		{"already 0751 is left alone", 0o751, 0o751, false},
		{"0755 is left alone (never narrowed here)", 0o755, 0o755, false},
		{"0700 gets only the others bit", 0o700, 0o701, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "localai")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.mode); err != nil {
				t.Fatal(err)
			}
			old, changed, err := ensureOperatorTraverse(dir)
			if err != nil {
				t.Fatal(err)
			}
			if old != tc.mode || changed != tc.changed {
				t.Fatalf("old %04o changed %v, want %04o %v", old, changed, tc.mode, tc.changed)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.want {
				t.Fatalf("mode %04o, want %04o", got, tc.want)
			}
		})
	}
}

func TestEnsureOperatorTraverseMissingAndNotADir(t *testing.T) {
	root := t.TempDir()
	if _, changed, err := ensureOperatorTraverse(filepath.Join(root, "absent")); err != nil || changed {
		t.Fatalf("missing folder: changed %v err %v", changed, err)
	}
	f := filepath.Join(root, "file")
	if err := os.WriteFile(f, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := ensureOperatorTraverse(f); err == nil || changed {
		t.Fatalf("a file must be refused and left alone: changed %v err %v", changed, err)
	}
	if info, _ := os.Stat(f); info.Mode().Perm() != 0o640 {
		t.Fatalf("file mode changed to %04o", info.Mode().Perm())
	}
}
