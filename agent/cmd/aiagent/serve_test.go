package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// `aiagent serve` returns what the daemon returns (exit 1 for a fatal startup error) and passes the
// context through, so SIGTERM reaches the daemon.
func TestServeExitCodeAndContext(t *testing.T) {
	old := serve
	t.Cleanup(func() { serve = old })
	type key struct{}
	for _, want := range []int{0, 1} {
		var got context.Context
		serve = func(ctx context.Context, stderr io.Writer) int {
			got = ctx
			_, _ = io.WriteString(stderr, "aiagent: LAI-AGT-011: not root\n")
			return want
		}
		ctx := context.WithValue(context.Background(), key{}, "marker")
		var out, errb bytes.Buffer
		if code := run(ctx, []string{"aiagent", "serve"}, &out, &errb); code != want {
			t.Errorf("exit = %d, want %d", code, want)
		}
		if got == nil || got.Value(key{}) != "marker" {
			t.Error("the context of run is not the one serve received")
		}
		if !strings.Contains(errb.String(), "LAI-AGT-011") {
			t.Errorf("stderr %q lacks the fatal code", errb.String())
		}
	}
}
