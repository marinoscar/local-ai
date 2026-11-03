package retention

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The static check of ark-sync.md section 9.1: nothing in internal/sync or internal/ark outside this
// package deletes. Deletion is the one thing retention exists to own, so a new delete anywhere else is
// a build failure here, not a review comment.
//
// What counts as a delete:
//   - a call of os.Remove or os.RemoveAll, of unix.Unlink, unix.Unlinkat or unix.Rmdir, or of
//     syscall.Unlink, syscall.Unlinkat or syscall.Rmdir (the package is found through the file's
//     imports, so an alias does not hide it);
//   - a call of a method named Remove or RemoveAll on anything else: that is the FS seam
//     (config.FS.Remove, retention.FS.RemoveAll) that the production code deletes through.
//
// The scan is of non-test files: a test may clean up after itself.

// bannedPackageCalls maps an import path to the function names that delete.
var bannedPackageCalls = map[string]map[string]bool{
	"os":                    {"Remove": true, "RemoveAll": true},
	"golang.org/x/sys/unix": {"Unlink": true, "Unlinkat": true, "Rmdir": true},
	"syscall":               {"Unlink": true, "Unlinkat": true, "Rmdir": true},
}

// bannedMethods are method names that delete through a seam.
var bannedMethods = map[string]bool{"Remove": true, "RemoveAll": true}

// deleteAllowlist names the files outside this package that may call a banned method, with the reason.
// A file is added here only if what it deletes is not content: the sync journal's own housekeeping.
var deleteAllowlist = map[string]string{
	"sync/journal/runs.go": "old run logs of the journal itself (runs/<id>.jsonl), never an item's files",
	"ark/detect.go":        "the drive detector's write probe: it removes the empty .probe file it created itself, never content",
}

// scanDeletes returns "path:line: call" for every delete found in the non-test Go files below the
// roots, skipping directories named retention, testdata and vendor, and the files of allow (keys are
// slash paths relative to base).
func scanDeletes(t *testing.T, base string, roots []string, allow map[string]string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(base, root), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "retention", "testdata", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			if _, ok := allow[filepath.ToSlash(rel)]; ok {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			// local name -> import path
			imports := map[string]string{}
			for _, im := range f.Imports {
				ipath, _ := strconv.Unquote(im.Path.Value)
				name := filepath.Base(ipath)
				if im.Name != nil {
					name = im.Name.Name
				}
				imports[name] = ipath
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pos := fset.Position(call.Pos())
				where := filepath.ToSlash(rel) + ":" + strconv.Itoa(pos.Line)
				if id, ok := sel.X.(*ast.Ident); ok {
					if ipath, isPkg := imports[id.Name]; isPkg {
						if bannedPackageCalls[ipath][sel.Sel.Name] {
							found = append(found, where+": "+id.Name+"."+sel.Sel.Name)
						}
						return true
					}
				}
				if bannedMethods[sel.Sel.Name] {
					found = append(found, where+": ."+sel.Sel.Name+" on a seam")
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(found)
	return found
}

// TestOnlyRetentionDeletes is the check on the real tree.
func TestOnlyRetentionDeletes(t *testing.T) {
	// the test runs in internal/sync/retention; the roots are internal/sync and internal/ark
	base := filepath.Join("..", "..")
	for _, f := range scanDeletes(t, base, []string{"sync", "ark"}, deleteAllowlist) {
		t.Errorf("%s: only internal/sync/retention may delete (ark-sync.md section 4.10)", f)
	}
	for file := range deleteAllowlist {
		if _, err := os.Stat(filepath.Join(base, filepath.FromSlash(file))); err != nil {
			t.Errorf("the allowlist names %s, which is not there: %v", file, err)
		}
	}
}

// TestTheDeleteCheckFindsAStrayRemove proves the check is not vacuous: a tree with a stray delete in
// every spelling it knows fails, and a clean one passes.
func TestTheDeleteCheckFindsAStrayRemove(t *testing.T) {
	write := func(base, rel, src string) {
		t.Helper()
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a clean tree passes", func(t *testing.T) {
		base := t.TempDir()
		write(base, "sync/a/a.go", "package a\nimport \"os\"\nfunc F() { _ = os.Rename(\"a\", \"b\") }\n")
		if got := scanDeletes(t, base, []string{"sync"}, nil); len(got) != 0 {
			t.Fatalf("found %v", got)
		}
	})
	t.Run("every spelling is found", func(t *testing.T) {
		base := t.TempDir()
		write(base, "sync/a/direct.go", "package a\nimport \"os\"\nfunc F() { _ = os.Remove(\"x\") }\n")
		write(base, "sync/a/all.go", "package a\nimport \"os\"\nfunc F() { _ = os.RemoveAll(\"x\") }\n")
		write(base, "sync/a/alias.go", "package a\nimport o \"os\"\nfunc F() { _ = o.Remove(\"x\") }\n")
		write(base, "sync/b/unix.go", "package b\nimport \"golang.org/x/sys/unix\"\nfunc F() { _ = unix.Unlink(\"x\") }\n")
		write(base, "sync/b/sys.go", "package b\nimport \"syscall\"\nfunc F() { _ = syscall.Rmdir(\"x\") }\n")
		write(base, "ark/seam.go", "package ark\ntype fsys interface{ Remove(string) error }\nfunc F(f fsys) { _ = f.Remove(\"x\") }\n")
		write(base, "ark/seamall.go", "package ark\ntype fsys interface{ RemoveAll(string) error }\nfunc F(f fsys) { _ = f.RemoveAll(\"x\") }\n")
		got := scanDeletes(t, base, []string{"sync", "ark"}, nil)
		if len(got) != 7 {
			t.Fatalf("found %d, want 7: %v", len(got), got)
		}
	})
	t.Run("retention itself, tests and testdata are not scanned", func(t *testing.T) {
		base := t.TempDir()
		write(base, "sync/retention/r.go", "package retention\nimport \"os\"\nfunc F() { _ = os.RemoveAll(\"x\") }\n")
		write(base, "sync/a/a_test.go", "package a\nimport \"os\"\nfunc F() { _ = os.Remove(\"x\") }\n")
		write(base, "sync/a/testdata/t.go", "package a\nimport \"os\"\nfunc F() { _ = os.Remove(\"x\") }\n")
		if got := scanDeletes(t, base, []string{"sync"}, nil); len(got) != 0 {
			t.Fatalf("found %v", got)
		}
	})
	t.Run("the allowlist names a file and nothing more", func(t *testing.T) {
		base := t.TempDir()
		write(base, "sync/journal/runs.go", "package journal\ntype f interface{ Remove(string) error }\nfunc F(x f) { _ = x.Remove(\"x\") }\n")
		write(base, "sync/other.go", "package sync\ntype f interface{ Remove(string) error }\nfunc F(x f) { _ = x.Remove(\"x\") }\n")
		got := scanDeletes(t, base, []string{"sync"}, map[string]string{"sync/journal/runs.go": "test"})
		if len(got) != 1 || !strings.HasPrefix(got[0], "sync/other.go:") {
			t.Fatalf("found %v", got)
		}
	})
}
