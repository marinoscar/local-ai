// Package dirs is `aiagent setup dirs` (installer.md section 4.3 P5): the localai user and the
// localai-operators group, every canonical directory with its exact owner and mode (overview.md section
// 3.2), the /srv/localai content root with its ROOT.json, and the polkit rule that lets ai-desktop start
// the agent.
//
// Everything that touches the machine goes through System, so the tests assert the commands and the
// directory table without root. A run is idempotent: what is already right is left alone, a wrong owner
// or mode is corrected and the old value is reported. With DryRun nothing is changed; the report says what
// would be.
package dirs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/setup/templates"
)

// Names of the accounts (overview.md section 3.1).
const (
	User          = "localai"
	OperatorGroup = "localai-operators"
	// Home is the home directory of the localai user.
	Home = "/var/lib/localai"
	// ContentRoot is the internal ark content root.
	ContentRoot = ark.DefaultInternalPath
	// PolkitDir is where the polkit rule goes.
	PolkitDir = "/etc/polkit-1/rules.d"
)

// ServiceGroups are the groups the localai user needs for the GPU (installer.md section 4.3 P5). A group
// that does not exist on this Ubuntu is skipped with a note.
var ServiceGroups = []string{"video", "render"}

// Spec is one directory of the table: who owns it and its mode.
type Spec struct {
	Path  string
	Owner string // "root" or "localai"
	Group string // "root" or "localai"
	Mode  os.FileMode
}

func (s Spec) want() string { return fmt.Sprintf("%s:%s %04o", s.Owner, s.Group, uint32(s.Mode)) }

// Table is the directory table of overview.md section 3.2 and installer.md section 4.3 P5, parents first.
// The content-root layout below /srv/localai is added by Run from ark.LayoutDirs.
func Table() []Spec {
	var t []Spec
	root := func(p string, m os.FileMode) { t = append(t, Spec{p, "root", "root", m}) }
	for _, p := range []string{
		"/opt/localai", "/opt/localai/bin", "/opt/localai/installers",
		"/opt/localai/llama.cpp", "/opt/localai/llama.cpp/builds", "/opt/localai/llama.cpp/src",
		"/opt/localai/whisper.cpp", "/opt/localai/whisper.cpp/builds",
		"/opt/localai/piper", "/opt/localai/kiwix-tools", "/opt/localai/pmtiles",
	} {
		root(p, 0o755)
	}
	t = append(t,
		Spec{"/etc/localai", "root", "localai", 0o750},
		Spec{"/etc/localai/catalog", "root", "localai", 0o750},
		Spec{Home, "localai", "localai", 0o750},
	)
	for _, n := range []string{"sync", "drill", "cloud", "kiwix", "llama-cache"} {
		t = append(t, Spec{Home + "/" + n, "localai", "localai", 0o750})
	}
	for _, n := range []string{"install", "build", "update", "migration"} {
		root(Home+"/"+n, 0o700)
	}
	root(Home+"/bench", 0o755)
	t = append(t, Spec{ContentRoot, "localai", "localai", 0o755})
	return t
}

// Info is what System.Stat says about a path.
type Info struct {
	Exists   bool
	IsDir    bool
	UID, GID int
	Mode     os.FileMode // permission bits only
}

// System is the machine. The real one is NewOSSystem; tests use a fake.
type System interface {
	LookupUser(name string) (uid, gid int, ok bool, err error)
	LookupGroup(name string) (gid int, ok bool, err error)
	InGroup(user, group string) (bool, error)
	AddGroup(ctx context.Context, name string) error
	AddUser(ctx context.Context, name, home string) error
	AddToGroups(ctx context.Context, user string, groups []string) error

	// Stat follows symlinks; a missing path is Info{Exists: false} and no error.
	Stat(path string) (Info, error)
	Mkdir(path string, mode os.FileMode) error
	Chown(path string, uid, gid int) error
	// Names renders uid:gid as "user:group" where the names are known, else the numbers.
	Names(uid, gid int) string
	Chmod(path string, mode os.FileMode) error
	ReadFile(path string) ([]byte, error)
	// WriteFile replaces path atomically (temp file + rename) with the mode and owner.
	WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error
	Rename(oldPath, newPath string) error
}

// Options configures Run.
type Options struct {
	// DesktopUser joins localai-operators; "" skips that step with a note.
	DesktopUser string
	// NoPolkit skips the polkit rule (--no-polkit).
	NoPolkit bool
	// DryRun changes nothing.
	DryRun bool
	// NodeID, AgentVersion and Profile go into a new ROOT.json (informational for the internal root).
	NodeID, AgentVersion, Profile string
	// Now stamps created_at; default time.Now.
	Now func() time.Time
}

// Change is one thing done, or in a dry run one thing that would be done.
type Change struct {
	// Kind is group-add, user-add, user-groups, mkdir, chown, chmod, write or move.
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Old and New are the values before and after, when there is something to compare.
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}

// String is the plain-language line shown to the operator.
func (c Change) String() string {
	switch c.Kind {
	case "mkdir":
		return fmt.Sprintf("create folder %s (%s)", c.Target, c.New)
	case "chown":
		return fmt.Sprintf("fix owner of %s: %s -> %s", c.Target, c.Old, c.New)
	case "chmod":
		return fmt.Sprintf("fix mode of %s: %s -> %s", c.Target, c.Old, c.New)
	case "write":
		if c.Old != "" {
			return fmt.Sprintf("replace file %s (%s)", c.Target, c.New)
		}
		return fmt.Sprintf("write file %s (%s)", c.Target, c.New)
	case "move":
		return fmt.Sprintf("move aside %s to %s", c.Target, c.New)
	case "group-add":
		return "create group " + c.Target
	case "user-add":
		return "create system user " + c.Target
	case "user-groups":
		return fmt.Sprintf("add %s to group(s) %s", c.Target, c.New)
	}
	return c.Kind + " " + c.Target
}

// Report is the outcome of Run.
type Report struct {
	DryRun  bool     `json:"dry_run"`
	Changes []Change `json:"changes"`
	// Notes are warnings and reminders (a group that does not exist, the log-out-and-in reminder).
	Notes []string `json:"notes"`
	// LogoutNeeded is true when the desktop user was added to localai-operators in this run.
	LogoutNeeded bool `json:"logout_needed"`
}

func (r *Report) add(c Change)  { r.Changes = append(r.Changes, c) }
func (r *Report) note(s string) { r.Notes = append(r.Notes, s) }

// Pending reports whether anything was (or, in a dry run, would be) changed.
func (r *Report) Pending() bool { return len(r.Changes) > 0 }

// ErrDesktopUser: the desktop user named with --desktop-user does not exist.
var ErrDesktopUser = errors.New("the desktop user does not exist")

// ErrNotADirectory: a file sits where a directory belongs. Nothing is deleted.
var ErrNotADirectory = errors.New("a file is in the way of a folder")

type ids struct {
	localaiUID, localaiGID int
	localaiPlanned         bool
}

// Run does the work. The returned Report lists what was done up to a failure, so a failed run still tells
// the operator what changed.
func Run(ctx context.Context, sys System, o Options) (*Report, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	rep := &Report{DryRun: o.DryRun}
	if err := ensureAccounts(ctx, sys, o, rep); err != nil {
		return rep, err
	}
	id, err := resolve(sys, o.DryRun)
	if err != nil {
		return rep, err
	}
	specs := Table()
	if !o.NoPolkit {
		specs = append(specs, Spec{"/etc/polkit-1", "root", "root", 0o755}, Spec{PolkitDir, "root", "root", 0o755})
	}
	for _, s := range specs {
		if err := ensureDir(ctx, sys, id, s, o.DryRun, rep); err != nil {
			return rep, err
		}
	}
	if err := ensureLayout(sys, id, o, rep); err != nil {
		return rep, err
	}
	if err := ensureRootJSON(sys, o, rep); err != nil {
		return rep, err
	}
	if o.NoPolkit {
		rep.note("The polkit rule was skipped (--no-polkit): ai-desktop will ask for the admin password to start the agent.")
	} else if err := ensurePolkit(sys, o.DryRun, rep); err != nil {
		return rep, err
	}
	if rep.LogoutNeeded {
		rep.note("Log out and log back in as " + o.DesktopUser + " so the new group " + OperatorGroup + " applies.")
	}
	return rep, nil
}

func ensureAccounts(ctx context.Context, sys System, o Options, rep *Report) error {
	if _, ok, err := sys.LookupGroup(OperatorGroup); err != nil {
		return err
	} else if !ok {
		rep.add(Change{Kind: "group-add", Target: OperatorGroup})
		if !o.DryRun {
			if err := sys.AddGroup(ctx, OperatorGroup); err != nil {
				return fmt.Errorf("create group %s: %w", OperatorGroup, err)
			}
		}
	}
	_, _, userOK, err := sys.LookupUser(User)
	if err != nil {
		return err
	}
	if !userOK {
		rep.add(Change{Kind: "user-add", Target: User})
		if !o.DryRun {
			if err := sys.AddUser(ctx, User, Home); err != nil {
				return fmt.Errorf("create user %s: %w", User, err)
			}
		}
	}
	// GPU groups for the service user.
	var missing []string
	for _, g := range ServiceGroups {
		if _, ok, err := sys.LookupGroup(g); err != nil {
			return err
		} else if !ok {
			rep.note("Group " + g + " does not exist on this computer; skipped.")
			continue
		}
		in := false
		if userOK {
			if in, err = sys.InGroup(User, g); err != nil {
				return err
			}
		}
		if !in {
			missing = append(missing, g)
		}
	}
	if len(missing) > 0 {
		rep.add(Change{Kind: "user-groups", Target: User, New: join(missing)})
		if !o.DryRun {
			if err := sys.AddToGroups(ctx, User, missing); err != nil {
				return fmt.Errorf("add %s to %s: %w", User, join(missing), err)
			}
		}
	}
	// The desktop user joins the operators.
	if o.DesktopUser == "" {
		rep.note("No desktop user was given, so nobody was added to " + OperatorGroup + ". Run: sudo usermod -aG " + OperatorGroup + " <user>")
		return nil
	}
	if _, _, ok, err := sys.LookupUser(o.DesktopUser); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %q", ErrDesktopUser, o.DesktopUser)
	}
	in, err := sys.InGroup(o.DesktopUser, OperatorGroup)
	if err != nil {
		return err
	}
	if !in {
		rep.add(Change{Kind: "user-groups", Target: o.DesktopUser, New: OperatorGroup})
		rep.LogoutNeeded = true
		if !o.DryRun {
			if err := sys.AddToGroups(ctx, o.DesktopUser, []string{OperatorGroup}); err != nil {
				return fmt.Errorf("add %s to %s: %w", o.DesktopUser, OperatorGroup, err)
			}
		}
	}
	return nil
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

// resolve finds the numeric ids. In a dry run on a machine without the localai user, its id is unknown
// ("planned") and every directory that should belong to it counts as needing a change.
func resolve(sys System, dry bool) (ids, error) {
	var id ids
	uid, gid, ok, err := sys.LookupUser(User)
	if err != nil {
		return id, err
	}
	if !ok {
		if !dry {
			return id, fmt.Errorf("user %s does not exist after creating it", User)
		}
		id.localaiPlanned = true
		return id, nil
	}
	id.localaiUID, id.localaiGID = uid, gid
	return id, nil
}

func (i ids) of(name string) (int, bool) {
	if name == "root" {
		return 0, true
	}
	return i.localaiUID, !i.localaiPlanned
}

func (i ids) gidOf(name string) (int, bool) {
	if name == "root" {
		return 0, true
	}
	return i.localaiGID, !i.localaiPlanned
}

func ensureDir(_ context.Context, sys System, id ids, s Spec, dry bool, rep *Report) error {
	uid, uidKnown := id.of(s.Owner)
	gid, gidKnown := id.gidOf(s.Group)
	info, err := sys.Stat(s.Path)
	if err != nil {
		return fmt.Errorf("look at %s: %w", s.Path, err)
	}
	if !info.Exists {
		rep.add(Change{Kind: "mkdir", Target: s.Path, New: s.want()})
		if dry {
			return nil
		}
		if err := sys.Mkdir(s.Path, s.Mode); err != nil {
			return fmt.Errorf("create %s: %w", s.Path, err)
		}
		if err := sys.Chmod(s.Path, s.Mode); err != nil {
			return fmt.Errorf("set mode of %s: %w", s.Path, err)
		}
		if err := sys.Chown(s.Path, uid, gid); err != nil {
			return fmt.Errorf("set owner of %s: %w", s.Path, err)
		}
		return nil
	}
	if !info.IsDir {
		return fmt.Errorf("%w: %s", ErrNotADirectory, s.Path)
	}
	if !uidKnown || !gidKnown || info.UID != uid || info.GID != gid {
		rep.add(Change{Kind: "chown", Target: s.Path, Old: sys.Names(info.UID, info.GID), New: s.Owner + ":" + s.Group})
		if !dry {
			if err := sys.Chown(s.Path, uid, gid); err != nil {
				return fmt.Errorf("set owner of %s: %w", s.Path, err)
			}
		}
	}
	if info.Mode.Perm() != s.Mode {
		rep.add(Change{Kind: "chmod", Target: s.Path, Old: fmt.Sprintf("%04o", uint32(info.Mode.Perm())), New: fmt.Sprintf("%04o", uint32(s.Mode))})
		if !dry {
			if err := sys.Chmod(s.Path, s.Mode); err != nil {
				return fmt.Errorf("set mode of %s: %w", s.Path, err)
			}
		}
	}
	return nil
}

// ensureLayout creates the content-root directories of overview.md section 3.3 the way ark.CreateLayout
// does: missing ones are created (root-owned, 0755), the private ones (.trash, .staging) are 0700 and are
// corrected when they exist; other existing ones are left alone.
func ensureLayout(sys System, id ids, o Options, rep *Report) error {
	for _, rel := range ark.LayoutDirs {
		p := filepath.Join(ContentRoot, filepath.FromSlash(rel))
		mode := ark.DirMode
		if ark.PrivateDirs[rel] {
			mode = ark.PrivateDirMode
		}
		info, err := sys.Stat(p)
		if err != nil {
			return fmt.Errorf("look at %s: %w", p, err)
		}
		switch {
		case !info.Exists:
			rep.add(Change{Kind: "mkdir", Target: p, New: fmt.Sprintf("root:root %04o", uint32(mode))})
			if o.DryRun {
				continue
			}
			if err := sys.Mkdir(p, mode); err != nil {
				return fmt.Errorf("create %s: %w", p, err)
			}
			if err := sys.Chmod(p, mode); err != nil {
				return fmt.Errorf("set mode of %s: %w", p, err)
			}
			if err := sys.Chown(p, 0, 0); err != nil {
				return fmt.Errorf("set owner of %s: %w", p, err)
			}
		case !info.IsDir:
			return fmt.Errorf("%w: %s", ErrNotADirectory, p)
		case ark.PrivateDirs[rel] && info.Mode.Perm() != mode:
			rep.add(Change{Kind: "chmod", Target: p, Old: fmt.Sprintf("%04o", uint32(info.Mode.Perm())), New: fmt.Sprintf("%04o", uint32(mode))})
			if !o.DryRun {
				if err := sys.Chmod(p, mode); err != nil {
					return fmt.Errorf("set mode of %s: %w", p, err)
				}
			}
		}
	}
	return nil
}

// ensureRootJSON makes sure /srv/localai/ROOT.json is an internal root's. An existing valid one (or one from
// a newer schema) is kept; a damaged one is moved aside, never deleted.
func ensureRootJSON(sys System, o Options, rep *Report) error {
	p := filepath.Join(ContentRoot, ark.RootJSONName)
	info, err := sys.Stat(p)
	if err != nil {
		return fmt.Errorf("look at %s: %w", p, err)
	}
	if info.Exists {
		data, err := sys.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rj, perr := ark.ParseRootJSON(data)
		switch {
		case perr == nil && rj.Kind == ark.KindInternal && rj.RootID == ark.InternalID:
			return nil
		case errors.Is(perr, ark.ErrRootJSONSchema):
			rep.note(p + " is from a newer version; left alone.")
			return nil
		}
		aside := p + ".damaged-" + o.Now().UTC().Format("20060102T150405Z")
		rep.add(Change{Kind: "move", Target: p, New: aside})
		if !o.DryRun {
			if err := sys.Rename(p, aside); err != nil {
				return fmt.Errorf("move %s aside: %w", p, err)
			}
		}
	}
	root := ark.NewInternalRoot(ContentRoot, o.Profile)
	rj := ark.NewRootJSON(root, ark.CreatedBy{NodeID: o.NodeID, AgentVersion: o.AgentVersion}, "", o.Now())
	data, err := rj.Encode()
	if err != nil {
		return fmt.Errorf("build ROOT.json: %w", err)
	}
	rep.add(Change{Kind: "write", Target: p, New: "root:root 0644, kind internal"})
	if o.DryRun {
		return nil
	}
	if err := sys.WriteFile(p, data, 0o644, 0, 0); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

func ensurePolkit(sys System, dry bool, rep *Report) error {
	p := filepath.Join(PolkitDir, templates.PolkitRuleName)
	want := templates.PolkitRule()
	info, err := sys.Stat(p)
	if err != nil {
		return fmt.Errorf("look at %s: %w", p, err)
	}
	old := ""
	if info.Exists {
		have, err := sys.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if string(have) == string(want) && info.Mode.Perm() == 0o644 && info.UID == 0 && info.GID == 0 {
			return nil
		}
		old = "different"
	}
	rep.add(Change{Kind: "write", Target: p, Old: old, New: "root:root 0644"})
	if dry {
		return nil
	}
	if err := sys.WriteFile(p, want, 0o644, 0, 0); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}
