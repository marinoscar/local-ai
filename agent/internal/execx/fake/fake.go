// Package fake is an execx.Runner for tests: it runs nothing, records every call and answers
// from scripted expectations. An unexpected command is an error, so tests fail loudly.
package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Runner implements execx.Runner.
type Runner struct {
	mu     sync.Mutex
	calls  []execx.Cmd
	expect map[string]reply
}

type reply struct {
	res execx.Result
	err error
}

// New returns an empty Runner.
func New() *Runner { return &Runner{expect: map[string]reply{}} }

// Key is the lookup key for a command: the path and arguments joined by single spaces.
func Key(path string, args ...string) string {
	return strings.Join(append([]string{path}, args...), " ")
}

// Expect scripts the answer for one exact command line.
func (r *Runner) Expect(key string, res execx.Result, err error) *Runner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expect[key] = reply{res, err}
	return r
}

// Calls returns the commands run so far, in order.
func (r *Runner) Calls() []execx.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execx.Cmd(nil), r.calls...)
}

// Run records the call and returns the scripted answer.
func (r *Runner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	if a, ok := r.expect[Key(c.Path, c.Args...)]; ok {
		return a.res, a.err
	}
	return execx.Result{}, fmt.Errorf("fake: unexpected command: %s", Key(c.Path, c.Args...))
}

var _ execx.Runner = (*Runner)(nil)
