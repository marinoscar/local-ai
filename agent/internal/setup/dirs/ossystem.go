package dirs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// OSSystem is the real machine: os/user for lookups, groupadd/useradd/usermod through execx for accounts.
type OSSystem struct {
	Runner   execx.Runner
	LookPath func(name string) (string, error)
}

// NewOSSystem returns the System of the real machine.
func NewOSSystem() *OSSystem {
	return &OSSystem{Runner: execx.Exec{}, LookPath: execx.Lookup}
}

var _ System = (*OSSystem)(nil)

func (OSSystem) LookupUser(name string) (int, int, bool, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unk user.UnknownUserError
		if errors.As(err, &unk) {
			return 0, 0, false, nil
		}
		return 0, 0, false, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return 0, 0, false, fmt.Errorf("user %s has a non-numeric id", name)
	}
	return uid, gid, true, nil
}

func (OSSystem) LookupGroup(name string) (int, bool, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		var unk user.UnknownGroupError
		if errors.As(err, &unk) {
			return 0, false, nil
		}
		return 0, false, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, false, fmt.Errorf("group %s has a non-numeric id", name)
	}
	return gid, true, nil
}

func (s OSSystem) InGroup(userName, group string) (bool, error) {
	u, err := user.Lookup(userName)
	if err != nil {
		return false, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return false, err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == g.Gid {
			return true, nil
		}
	}
	return false, nil
}

func (s OSSystem) run(ctx context.Context, bin string, args ...string) error {
	path, err := s.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s is not installed: %w", bin, err)
	}
	res, err := s.Runner.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: 30 * time.Second, MaxOut: 64 << 10})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s %s exited %d: %s", bin, strings.Join(args, " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func (s OSSystem) AddGroup(ctx context.Context, name string) error {
	return s.run(ctx, "groupadd", "--system", name)
}

func (s OSSystem) AddUser(ctx context.Context, name, home string) error {
	return s.run(ctx, "useradd", "--system", "--user-group", "--home-dir", home, "--no-create-home", "--shell", "/usr/sbin/nologin", name)
}

func (s OSSystem) AddToGroups(ctx context.Context, userName string, groups []string) error {
	return s.run(ctx, "usermod", "-aG", strings.Join(groups, ","), userName)
}

func (OSSystem) Stat(path string) (Info, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	in := Info{Exists: true, IsDir: fi.IsDir(), Mode: fi.Mode().Perm()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		in.UID, in.GID = int(st.Uid), int(st.Gid)
	}
	return in, nil
}

func (OSSystem) Mkdir(path string, mode os.FileMode) error { return os.Mkdir(path, mode) }
func (OSSystem) Chown(path string, uid, gid int) error     { return os.Chown(path, uid, gid) }
func (OSSystem) Chmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
func (OSSystem) ReadFile(path string) ([]byte, error)      { return os.ReadFile(path) }
func (OSSystem) Rename(oldPath, newPath string) error      { return os.Rename(oldPath, newPath) }

func (OSSystem) Names(uid, gid int) string {
	un, gn := strconv.Itoa(uid), strconv.Itoa(gid)
	if u, err := user.LookupId(un); err == nil {
		un = u.Username
	}
	if g, err := user.LookupGroupId(gn); err == nil {
		gn = g.Name
	}
	return un + ":" + gn
}

func (OSSystem) WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".lai-setup.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func(e error) error { _ = f.Close(); _ = os.Remove(tmp); return e }
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chown(tmp, uid, gid); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
