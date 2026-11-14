package ark

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// Modes of the layout (ark-sync.md section 3.1): directories 0755, the staging and trash
// directories 0700, files 0644. The owner of everything is whoever runs the agent (root on a
// node); only the top directory is handed to the localai user, so a numeric uid that differs from
// node to node never matters to the read-only services.
const (
	DirMode        os.FileMode = 0o755
	PrivateDirMode os.FileMode = 0o700
)

// LayoutDirs is the directory layout of a content root, relative to the root, in creation order
// (overview.md section 3.3). READY.json and ROOT.json are files and are written by their owners.
// The mode of each entry is DirMode unless it is in PrivateDirs.
var LayoutDirs = []string{
	"models",
	"speech",
	"speech/whisper",
	"speech/piper",
	"music",
	"kiwix",
	"maps",
	"maps/assets",
	"kit",
	"docs",
	".trash",
	".staging",
}

// PrivateDirs are the layout directories that are 0700 root.
var PrivateDirs = map[string]bool{".trash": true, ".staging": true}

// ErrLayoutConflict: something that is not a directory sits where the layout needs one. Nothing is
// deleted or replaced; the caller (or the operator) decides.
var ErrLayoutConflict = errors.New("ark: a file is in the way of the content layout")

// Owner is a numeric uid and gid.
type Owner struct{ UID, GID int }

// LocalAIUser is the system user that owns the top directory of a root.
const LocalAIUser = "localai"

// LookupLocalAIOwner resolves the localai user's uid and gid with os/user. The app wiring calls it
// once and passes the result as Owner; where it fails (a development machine without the user) the
// wiring passes nil and the top directory stays with the agent's user.
func LookupLocalAIOwner() (*Owner, error) {
	u, err := user.Lookup(LocalAIUser)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("ark: uid %q of %s: %w", u.Uid, LocalAIUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("ark: gid %q of %s: %w", u.Gid, LocalAIUser, err)
	}
	return &Owner{UID: uid, GID: gid}, nil
}

// LayoutOptions tunes CreateLayout.
type LayoutOptions struct {
	// Owner, when set, is given the top directory if CreateLayout creates it. nil leaves the owner
	// alone. Subdirectories are never chowned.
	Owner *Owner
}

// CreateLayout creates the content-root layout under root and is safe to repeat: whatever exists is
// kept, only what is missing is created. It returns created=true when it had to create root itself
// (the only case in which Owner is applied). Missing parents of root are created 0755.
//
// Modes are set with an explicit Chmod, so the process umask does not matter. The 0700 directories
// are re-chmodded even when they already exist, because they hold unverified downloads and the
// previous versions of content. A non-directory in the way is ErrLayoutConflict, never removed.
//
// CreateLayout does not write ROOT.json or READY.json, and nothing calls it for a drive except the
// explicit init action.
func CreateLayout(fsys FS, root string, opts LayoutOptions) (created bool, err error) {
	if err := mkdirAll(fsys, filepath.Dir(root)); err != nil {
		return false, err
	}
	created, err = ensureDir(fsys, root, DirMode)
	if err != nil {
		return false, err
	}
	if created && opts.Owner != nil {
		if err := fsys.Chown(root, opts.Owner.UID, opts.Owner.GID); err != nil {
			return created, fmt.Errorf("chown %s: %w", root, err)
		}
	}
	for _, rel := range LayoutDirs {
		p := filepath.Join(root, filepath.FromSlash(rel))
		mode := DirMode
		if PrivateDirs[rel] {
			mode = PrivateDirMode
		}
		made, err := ensureDir(fsys, p, mode)
		if err != nil {
			return created, err
		}
		if !made && PrivateDirs[rel] {
			if err := fsys.Chmod(p, mode); err != nil {
				return created, fmt.Errorf("chmod %s: %w", p, err)
			}
		}
	}
	return created, nil
}

// ensureDir makes p a directory of the given mode if it does not exist.
func ensureDir(fsys FS, p string, mode os.FileMode) (created bool, err error) {
	info, err := fsys.Stat(p)
	switch {
	case err == nil && info.IsDir():
		return false, nil
	case err == nil:
		return false, fmt.Errorf("%w: %s", ErrLayoutConflict, p)
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("stat %s: %w", p, err)
	}
	if err := fsys.Mkdir(p, mode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// A dangling symlink, or a racing creator.
			if info, serr := fsys.Stat(p); serr == nil && info.IsDir() {
				return false, nil
			}
			return false, fmt.Errorf("%w: %s", ErrLayoutConflict, p)
		}
		return false, fmt.Errorf("mkdir %s: %w", p, err)
	}
	if err := fsys.Chmod(p, mode); err != nil {
		return true, fmt.Errorf("chmod %s: %w", p, err)
	}
	return true, nil
}

// mkdirAll creates p and its missing parents, 0755.
func mkdirAll(fsys FS, p string) error {
	info, err := fsys.Stat(p)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrLayoutConflict, p)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", p, err)
	}
	if parent := filepath.Dir(p); parent != p {
		if err := mkdirAll(fsys, parent); err != nil {
			return err
		}
	}
	_, err = ensureDir(fsys, p, DirMode)
	return err
}
