package kiwix

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Units is the slice of the systemd Manager the library service uses (a fake in tests).
type Units interface {
	State(ctx context.Context, unit string) (services.UnitState, error)
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
}

// FDLister lists where the open file descriptors of a process point.
type FDLister interface {
	Links(pid int) ([]string, error)
}

// ProcFD is the real FDLister, over /proc. Root moves it for tests; empty means /proc.
type ProcFD struct{ Root string }

// Links implements FDLister.
func (p ProcFD) Links(pid int) ([]string, error) {
	root := p.Root
	if root == "" {
		root = "/proc"
	}
	dir := filepath.Join(root, strconv.Itoa(pid), "fd")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if t, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// holdsUnder reports whether any link points at or under dir (a " (deleted)" suffix is ignored).
func holdsUnder(links []string, dir string) bool {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return false
	}
	for _, l := range links {
		l = strings.TrimSuffix(l, " (deleted)")
		if l == dir || strings.HasPrefix(l, dir+"/") {
			return true
		}
	}
	return false
}

// removals remembers where each root was mounted, and which roots were unmounted since the last
// check, so the open-file scan knows the paths after the mount is gone.
type removals struct {
	mu      sync.Mutex
	paths   map[string]string
	pending map[string]string
}

func (r *removals) remember(id, path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	if r.paths == nil {
		r.paths = map[string]string{}
	}
	r.paths[id] = path
	r.mu.Unlock()
}

func (r *removals) removed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.paths[id]; ok {
		if r.pending == nil {
			r.pending = map[string]string{}
		}
		r.pending[id] = p
		delete(r.paths, id)
	}
}

func (r *removals) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.pending {
		out = append(out, p)
	}
	r.pending = nil
	return out
}
