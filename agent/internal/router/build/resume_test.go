package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Issue #433: the compile step's output reaches Request.Output as it is written, and a build that was
// stopped is carried on by the next run of the same build (rebuild-kit.md section 4.3.1).

// lockedBuf is an io.Writer the test can read while the engine writes.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestCompileOutputGoesToRequestOutput(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	const ninja = "[1/2] Building CXX object ggml/src/CMakeFiles/ggml-base.dir/ggml.c.o\n[2/2] Linking CXX executable bin/llama-server\n"
	var tees []string
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "cmake" && c.Tee != nil {
			tees = append(tees, c.Args[0])
			_, _ = c.Tee.Write([]byte(ninja))
		}
		return execx.Result{}, nil, false // the world answers
	})
	var got lockedBuf
	h.req.Output = &got
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if got.String() != ninja {
		t.Errorf("Output got %q", got.String())
	}
	if len(tees) != 1 || tees[0] != "--build" {
		t.Errorf("only the compile step is teed, got %v", tees)
	}
}

func TestNoOutputMeansNoTee(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.exec.all() {
		if c.Tee != nil {
			t.Errorf("%s was given a tee", line(c))
		}
	}
}

func TestStoppedBuildIsCarriedOnByTheNextRun(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	tmp := filepath.Join(h.root, "builds", testID+".tmp")
	ctx, cancel := context.WithCancel(context.Background())
	first := true
	var sawObject bool
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) != "cmake" || c.Args[0] != "--build" {
			return execx.Result{}, nil, false
		}
		if first {
			// The compile got half way: cmake's cache and one object are there, then Ctrl-C.
			first = false
			h.write(filepath.Join(c.Args[1], "CMakeCache.txt"), "CMAKE_HOME_DIRECTORY=x\n")
			h.write(filepath.Join(c.Args[1], "ggml.c.o"), "object")
			cancel()
			return execx.Result{}, ctx.Err(), true
		}
		_, err := os.Stat(filepath.Join(c.Args[1], "ggml.c.o"))
		sawObject = err == nil
		return execx.Result{}, nil, false
	})
	_, err := h.manager().Build(ctx, h.req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !h.exists("builds/"+testID+".tmp/build/CMakeCache.txt") || !h.exists("builds/"+testID+".tmp/build/ggml.c.o") {
		t.Fatal("the build tree of a stopped build must be kept for the next run")
	}
	if h.exists("builds/"+testID+".tmp.failed") || h.exists("builds/"+testID) {
		t.Error("a stopped build is neither failed nor finished")
	}
	if h.link() != "builds/"+oldID {
		t.Error("a stopped build must leave current alone")
	}
	// Something else in <id>.tmp is not kept.
	h.write(filepath.Join(tmp, "stale", "file"), "x")

	var msgs []string
	h.req.Progress = func(p Progress) { msgs = append(msgs, p.Message) }
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if !sawObject {
		t.Error("the second run did not carry on in the kept build tree")
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "Carrying on with the build that was stopped before") {
		t.Errorf("the resume is not said: %q", msgs)
	}
	if h.exists("builds/"+testID+"/stale") || h.exists("builds/"+testID+".tmp") {
		t.Error("the leftovers of the stopped build ended up in the new build, or .tmp stayed")
	}
}

func TestStoppedBuildWithoutACMakeCacheLeavesNothing(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "cmake" && c.Args[0] == "-S" {
			h.write(filepath.Join(c.Args[3], "half"), "x") // configure stopped before its cache
			cancel()
			return execx.Result{}, ctx.Err(), true
		}
		return execx.Result{}, nil, false
	})
	if _, err := h.manager().Build(ctx, h.req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if h.exists("builds/" + testID + ".tmp") {
		t.Error("a build tree that cannot be carried on must be removed")
	}
}

func TestLinkedBuildTreeIsNotResumed(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	elsewhere := t.TempDir()
	h.write(filepath.Join(elsewhere, "CMakeCache.txt"), "x")
	if err := os.MkdirAll(filepath.Join(h.root, "builds", testID+".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(h.root, "builds", testID+".tmp", "build")); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	h.req.Progress = func(p Progress) { msgs = append(msgs, p.Message) }
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(msgs, "\n"), "Carrying on") {
		t.Error("a build tree that is a link must not be resumed")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "CMakeCache.txt")); err != nil {
		t.Error("removing the leftover followed the link")
	}
}
