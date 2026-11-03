package ark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// CheckInterval is how often the internal root is re-checked after the check at startup
// (ark-sync.md section 4.1.1 and the issue: startup and every 6 h).
const CheckInterval = 6 * time.Hour

// InternalOptions configures EnsureInternal and the Monitor.
type InternalOptions struct {
	// Path of the internal root; default DefaultInternalPath.
	Path string
	// Profile is node.yaml's profile; it becomes Root.Profile and the informational profile of a
	// ROOT.json that has to be written.
	Profile string
	// NodeID and AgentVersion are recorded in a ROOT.json that has to be written.
	NodeID, AgentVersion string
	// Owner, when set, receives the top directory if it is recreated (the localai user).
	Owner *Owner
	// Now stamps created_at and the names of files moved aside; default time.Now.
	Now func() time.Time
	// Logger receives notices (a ROOT.json moved aside, a newer schema left alone); default slog.Default.
	Logger *slog.Logger
}

func (o InternalOptions) withDefaults() InternalOptions {
	if o.Path == "" {
		o.Path = DefaultInternalPath
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// EnsureInternal checks the internal root and heals it (ark-sync.md section 4.1.1). In order:
//
//  1. On a network filesystem (statfs reports NFS, SMB/CIFS or FUSE) the root is read-only and
//     nothing is written: LAI-ARK-021. The statfs is taken on the root, or on the nearest existing
//     parent when the root is missing. A statfs that fails counts as local.
//  2. Missing, or not a directory (a file, a dangling symlink): whatever is there is renamed to
//     <path>.replaced-<UTC time> (never deleted), the root is recreated empty with the full layout
//     and LAI-ARK-020 is raised. A symlink to a directory is a directory and is left alone.
//  3. Otherwise any missing layout directory is created, the 0700 modes are enforced, and ROOT.json
//     is made sure of: absent gets the defaults, a damaged one (or one that is not an internal
//     root's) is renamed to ROOT.json.damaged-<UTC time> and replaced by the defaults, a newer
//     schema is left alone. The internal root's ROOT.json is informational, so none of this raises
//     a code.
//
// The returned Root is always usable as a value. On a hard failure (a read-only mount, no space, a
// permission error, a file that cannot be moved) the error says why, the Root is ReadOnly and its
// State is read_only when the directory exists, unusable when it does not. LAI-ARK-020 is raised
// only by the call that recreated the root; the Monitor keeps it latched.
func EnsureInternal(fsys FS, opts InternalOptions) (Root, []Problem, error) {
	opts = opts.withDefaults()
	root := NewInternalRoot(opts.Path, opts.Profile)
	var problems []Problem

	present, err := isDir(fsys, root.Path)
	if err != nil {
		return failedRoot(fsys, root), nil, err
	}

	probe := root.Path
	if !present {
		probe = nearestExistingDir(fsys, filepath.Dir(root.Path))
	}
	if magic, merr := fsys.FSType(probe); merr == nil && IsNetworkFS(magic) {
		root.ReadOnly = true
		root.State = StateReadOnly
		problems = append(problems, newProblem(CodeNetworkFS, map[string]any{
			"root_id": root.ID, "path": root.Path, "fs_type": fmt.Sprintf("0x%X", magic),
		}))
		return root, problems, nil
	}

	recreated := !present
	if recreated {
		if err := moveAside(fsys, root.Path, ".replaced-", opts.Now()); err != nil {
			return failedRoot(fsys, root), nil, err
		}
	}
	if _, err := CreateLayout(fsys, root.Path, LayoutOptions{Owner: opts.Owner}); err != nil {
		failed := failedRoot(fsys, root)
		if recreated && failed.State == StateReadOnly {
			// The directory itself was recreated before the layout failed.
			problems = append(problems, newProblem(CodeInternalRecreated, map[string]any{"root_id": root.ID, "path": root.Path}))
		}
		return failed, problems, err
	}
	if recreated {
		problems = append(problems, newProblem(CodeInternalRecreated, map[string]any{
			"root_id": root.ID, "path": root.Path,
		}))
	}
	if err := ensureInternalRootJSON(fsys, root, opts); err != nil {
		return failedRoot(fsys, root), problems, err
	}
	return root, problems, nil
}

// failedRoot marks root read-only: read_only when its directory still exists, unusable when not.
func failedRoot(fsys FS, root Root) Root {
	root.ReadOnly = true
	root.State = StateUnusable
	if ok, _ := isDir(fsys, root.Path); ok {
		root.State = StateReadOnly
	}
	return root
}

// ensureInternalRootJSON makes sure the internal root has a ROOT.json of its own kind.
func ensureInternalRootJSON(fsys FS, root Root, opts InternalOptions) error {
	p := filepath.Join(root.Path, RootJSONName)
	config.SweepTemps(fsys, p)
	rj, err := ReadRootJSONFS(fsys, p)
	switch {
	case err == nil && rj.Kind == KindInternal && rj.RootID == InternalID:
		return nil
	case err == nil:
		opts.Logger.Warn("ROOT.json under the internal root is not an internal root's; replacing it",
			"path", p, "kind", string(rj.Kind), "root_id", rj.RootID)
	case errors.Is(err, ErrRootJSONSchema):
		opts.Logger.Warn("ROOT.json has a newer schema; leaving it alone", "path", p, "err", err)
		return nil
	case errors.Is(err, ErrRootJSONCorrupt):
		opts.Logger.Warn("ROOT.json is damaged; replacing it with the defaults", "path", p, "err", err)
	case isNotExist(err):
		// Fall through to write the defaults.
	default:
		return fmt.Errorf("read %s: %w", p, err)
	}
	if err == nil || errors.Is(err, ErrRootJSONCorrupt) {
		if aerr := moveAside(fsys, p, ".damaged-", opts.Now()); aerr != nil {
			return aerr
		}
	}
	by := CreatedBy{NodeID: opts.NodeID, AgentVersion: opts.AgentVersion}
	return WriteRootJSONFS(fsys, p, NewRootJSON(root, by, "", opts.Now()))
}

// isDir reports whether p exists and is a directory (following symlinks). A missing p, or a
// non-directory, is (false, nil); any other stat error is returned.
func isDir(fsys FS, p string) (bool, error) {
	info, err := fsys.Stat(p)
	if err == nil {
		return info.IsDir(), nil
	}
	if isNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", p, err)
}

// nearestExistingDir walks up from p to the first existing directory (or the top).
func nearestExistingDir(fsys FS, p string) string {
	for {
		if ok, _ := isDir(fsys, p); ok {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// moveAside renames p, if anything exists there (a symlink counts, dangling or not), to
// p+marker+<UTC time>, with a numeric suffix when that name is taken. It never deletes.
func moveAside(fsys FS, p, marker string, now time.Time) error {
	if _, err := fsys.Lstat(p); err != nil {
		if isNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", p, err)
	}
	base := p + marker + now.UTC().Format("20060102T150405Z")
	target := base
	for n := 1; n < 100; n++ {
		if _, err := fsys.Lstat(target); isNotExist(err) {
			break
		}
		target = fmt.Sprintf("%s-%d", base, n)
	}
	if err := fsys.Rename(p, target); err != nil {
		return fmt.Errorf("move %s aside to %s: %w", p, target, err)
	}
	return nil
}

// Monitor runs the internal-root check at startup and every CheckInterval and keeps the latest
// result. It is the piece the app wiring starts (go m.Run(ctx)); doctor checks and READY read Root
// and Problems.
type Monitor struct {
	fsys   FS
	opts   InternalOptions
	clock  config.Clock
	notify func(Root, []Problem, error)

	mu       sync.Mutex
	root     Root
	problems []Problem
	err      error
	latched  *Problem
}

// NewMonitor returns a Monitor over fsys. clock drives the 6-hour ticker (and Now, unless opts has
// one); notify, if not nil, is called after every check with the result, outside the Monitor's lock.
func NewMonitor(fsys FS, opts InternalOptions, clock config.Clock, notify func(Root, []Problem, error)) *Monitor {
	if opts.Now == nil && clock != nil {
		opts.Now = clock.Now
	}
	opts = opts.withDefaults()
	return &Monitor{fsys: fsys, opts: opts, clock: clock, notify: notify, root: NewInternalRoot(opts.Path, opts.Profile)}
}

// Check runs one pass of EnsureInternal, stores the result and returns it. LAI-ARK-020 from a pass
// that recreated the root stays in Problems until ClearRecreated, so a later pass that finds the
// (now existing) empty root does not make the alert disappear on its own.
func (m *Monitor) Check() (Root, []Problem, error) {
	root, problems, err := EnsureInternal(m.fsys, m.opts)

	m.mu.Lock()
	for i := range problems {
		if problems[i].Code == CodeInternalRecreated {
			p := problems[i]
			m.latched = &p
		}
	}
	if m.latched != nil && !hasCode(problems, CodeInternalRecreated) {
		problems = append(problems, *m.latched)
	}
	m.root, m.problems, m.err = root, problems, err
	m.mu.Unlock()

	if m.notify != nil {
		m.notify(root, problems, err)
	}
	return root, problems, err
}

// Root returns the root as of the latest check (the unchecked defaults before the first one).
func (m *Monitor) Root() Root {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root
}

// Problems returns a copy of the problems of the latest check, plus the latched LAI-ARK-020.
func (m *Monitor) Problems() []Problem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Problem(nil), m.problems...)
}

// Err returns the error of the latest check, nil when it was healthy.
func (m *Monitor) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// ClearRecreated drops the latched LAI-ARK-020. The sync engine calls it once the recreated root
// has been refilled (a sync or an import finished), which is what the fix text asks the operator to do.
func (m *Monitor) ClearRecreated() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latched = nil
	kept := m.problems[:0:0]
	for _, p := range m.problems {
		if p.Code != CodeInternalRecreated {
			kept = append(kept, p)
		}
	}
	m.problems = kept
}

// Run checks at once, then every CheckInterval, until ctx is done. It needs a clock.
func (m *Monitor) Run(ctx context.Context) {
	// The ticker exists before the first check, so a test that waits for that check can advance a
	// manual clock without racing the ticker's creation.
	t := m.clock.NewTicker(CheckInterval)
	defer t.Stop()
	m.Check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			m.Check()
		}
	}
}

func hasCode(ps []Problem, code string) bool {
	for _, p := range ps {
		if p.Code == code {
			return true
		}
	}
	return false
}
