package ark

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// DriveOptions configures OpenDriveRoot.
type DriveOptions struct {
	// DefaultProfile is node.yaml sync.ark_drive.default_profile; default DefaultDriveProfile. It
	// is used when ROOT.json is missing or has an empty profile.
	DefaultProfile string
	// NodeID and AgentVersion are recorded in a ROOT.json that has to be recreated.
	NodeID, AgentVersion string
	// Now stamps created_at of a recreated ROOT.json; default time.Now.
	Now func() time.Time
	// Logger receives notices; default slog.Default.
	Logger *slog.Logger
}

// OpenDriveRoot builds the Root of a mounted, already classified drive from its ROOT.json
// (ark-sync.md section 3.2 and 4.1.2). It never creates a root: only the explicit init action does.
//
//   - <mount>/localai absent, or present but empty: the drive is uninitialized (State
//     uninitialized, LAI-ARK-010 info) and nothing is written.
//   - <mount>/localai has content but no ROOT.json: the defaults are written (profile
//     DefaultProfile, created by this node, the drive's label) and LAI-ARK-010 is raised as info.
//     On a read-only drive (d.ReadOnly) nothing can be written: the Root carries the defaults in
//     memory, State read_only, and the file is recreated the next time the drive is writable.
//   - ROOT.json present: Profile comes from it (empty means DefaultProfile). The root id always
//     comes from the filesystem UUID, which is the truth: a stale root_id in the file (the UUID was
//     changed with tune2fs) is logged and left alone.
//   - ROOT.json damaged, or of a newer schema, or unreadable: an error (ErrRootJSONCorrupt,
//     ErrRootJSONSchema, or the I/O error), State unusable, and the file is not touched.
//
// The returned Root is always a usable value. A UUID that gives no id is an error with a zero Root.
func OpenDriveRoot(fsys FS, d DriveInfo, opts DriveOptions) (Root, []Problem, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	root, err := NewDriveRoot(d, opts.DefaultProfile)
	if err != nil {
		return Root{}, nil, err
	}
	newDrive := func() []Problem {
		return []Problem{newProblem(CodeNewDrive, map[string]any{"root_id": root.ID, "mount": d.Mount})}
	}
	uninitialized := func() (Root, []Problem, error) {
		root.State = StateUninitialized
		return root, newDrive(), nil
	}

	info, err := fsys.Stat(root.Path)
	switch {
	case err != nil && isNotExist(err):
		return uninitialized()
	case err != nil:
		return unusable(root), nil, fmt.Errorf("stat %s: %w", root.Path, err)
	case !info.IsDir():
		return unusable(root), nil, fmt.Errorf("%w: %s", ErrLayoutConflict, root.Path)
	}

	p := filepath.Join(root.Path, RootJSONName)
	rj, err := ReadRootJSONFS(fsys, p)
	switch {
	case err == nil:
		if rj.Profile != "" {
			root.Profile = rj.Profile
		}
		if rj.RootID != root.ID {
			opts.Logger.Warn("ROOT.json root_id differs from the filesystem UUID; the UUID wins",
				"path", p, "file", rj.RootID, "uuid", root.ID)
		}
		return root, nil, nil
	case !isNotExist(err):
		return unusable(root), nil, err
	}

	// No ROOT.json. An empty localai directory is an uninitialized drive, not a damaged one.
	entries, err := fsys.ReadDir(root.Path)
	if err != nil {
		return unusable(root), nil, fmt.Errorf("read %s: %w", root.Path, err)
	}
	if len(entries) == 0 {
		return uninitialized()
	}
	problems := newDrive()
	if d.ReadOnly {
		return root, problems, nil
	}
	config.SweepTemps(fsys, p)
	by := CreatedBy{NodeID: opts.NodeID, AgentVersion: opts.AgentVersion}
	if err := WriteRootJSONFS(fsys, p, NewRootJSON(root, by, d.Label, opts.Now())); err != nil {
		// The write failed (full disk, a read-only remount, EIO): serve the drive read-only.
		root.ReadOnly = true
		root.State = StateReadOnly
		return root, problems, err
	}
	return root, problems, nil
}

func unusable(r Root) Root {
	r.State = StateUnusable
	r.ReadOnly = true
	return r
}
