package execx

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// violations returns every .go file under root (vendor/ and hidden directories excluded) outside
// the execx package directory that imports os/exec. Imports are parsed, not grepped, so a comment
// or string mentioning "os/exec" never trips the guard.
func violations(root string) ([]string, error) {
	allowed := filepath.Join(root, "internal", "execx")
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || filepath.Dir(path) == allowed {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				rel, _ := filepath.Rel(root, path)
				bad = append(bad, rel)
			}
		}
		return nil
	})
	sort.Strings(bad)
	return bad, err
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func TestOnlyExecxImportsOsExec(t *testing.T) {
	bad, err := violations(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("only internal/execx may import os/exec (agent.md section 3.2); violations: %v", bad)
	}
}

func write(t *testing.T, root, rel, src string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGuardCatchesAFixtureThatImportsOsExec(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/fixture\n\ngo 1.24\n")
	write(t, root, "internal/execx/ok.go", "package execx\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")                             // allowed
	write(t, root, "internal/bad/bad.go", "package bad\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")                                // violation
	write(t, root, "internal/alias/alias.go", "package alias\n\nimport run \"os/exec\"\n\nvar _ = run.Command\n")                       // violation (aliased)
	write(t, root, "internal/badtest/x_test.go", "package badtest\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")                     // violation (tests count)
	write(t, root, "internal/execx/fake/f.go", "package fake\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")                          // violation (subpackage is not execx)
	write(t, root, "vendor/x/y/y.go", "package y\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")                                      // vendor is excluded
	write(t, root, "internal/comment/c.go", "package comment\n\n// uses os/exec in a comment only: \"os/exec\"\nvar s = \"os/exec\"\n") // not an import

	got, err := violations(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/alias/alias.go", "internal/bad/bad.go", "internal/badtest/x_test.go", "internal/execx/fake/f.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("violations = %v, want %v", got, want)
	}
}
