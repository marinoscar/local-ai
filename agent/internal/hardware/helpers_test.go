package hardware

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// fixture returns a filesystem rooted at testdata/<name>: its proc/, sys/, dev/ and etc/ folders
// stand for the real ones.
func fixture(t testing.TB, name string) fs.FS {
	t.Helper()
	dir := filepath.Join("testdata", filepath.FromSlash(name))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return os.DirFS(dir)
}

// testdata reads a file under testdata.
func testdata(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// installed is a LookPath where every program is in /usr/bin.
func installed(name string) (string, error) { return "/usr/bin/" + name, nil }

// only is a LookPath where just the named programs are installed.
func only(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", execx.ErrNotFound
	}
}

func testEnv(fsys fs.FS, run execx.Runner) *env {
	if fsys == nil {
		fsys = fstest.MapFS{}
	}
	if run == nil {
		run = execfake.New()
	}
	return &env{fs: fsys, run: run, look: installed, execTimeout: time.Second}
}

func ok(stdout string) execx.Result { return execx.Result{Stdout: []byte(stdout)} }

func exit(code int, stderr string) execx.Result {
	return execx.Result{ExitCode: code, Stderr: []byte(stderr)}
}

// blockingRunner never answers until its context ends: a hung program.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ execx.Cmd) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{}, ctx.Err()
}

// file is a MapFS entry.
func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
