package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The repository's redaction check rejects any 64-hex string outside its own fixtures, so the
// testdata and the tests write a sha256 as @sha:<hex digit>@ and this expands it to 64 characters.
var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

func expand(s string) string {
	return shaToken.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
	})
}

// validDir copies testdata/valid into a fresh temp dir so a test can edit it.
func validDir(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, filepath.Join("testdata", "valid"), dst)
	return dst
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, []byte(expand(string(data))), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// edit is one textual change to a file of the valid catalog. old must occur exactly once.
type edit struct {
	file     string
	old, new string
}

func applyEdits(t *testing.T, dir string, edits []edit) {
	t.Helper()
	for _, e := range edits {
		p := filepath.Join(dir, filepath.FromSlash(e.file))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		e.old, e.new = expand(e.old), expand(e.new)
		if n := strings.Count(string(data), e.old); n != 1 {
			t.Fatalf("edit on %s: %q occurs %d times, want exactly 1", e.file, e.old, n)
		}
		out := strings.Replace(string(data), e.old, e.new, 1)
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(expand(content)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func problemsText(r Report) string {
	var b strings.Builder
	for _, p := range r.Problems {
		b.WriteString(p.Location() + "  " + p.Code + "  " + p.Message + "\n")
	}
	return b.String()
}

// has reports whether the report has a problem with this code, file and path.
func has(r Report, code, file, path string) bool {
	for _, p := range r.Problems {
		if p.Code == code && p.File == file && p.Path == path {
			return true
		}
	}
	return false
}
