package router

import (
	"errors"
	"os"
	"testing"
)

const (
	laiUID = 990
	laiGID = 991
)

// own sets the owner and mode of an entry of the fake disk.
func (m *memFS) own(p string, uid, gid int, mode os.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[p]
	e.uid, e.gid, e.mode = uid, gid, mode
}

func accessReason(err error) string {
	var ap *AccessProblem
	if errors.As(err, &ap) {
		return ap.Reason
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestCheckReadable(t *testing.T) {
	const file = "/srv/localai/models/m/w.gguf"
	id := Identity{UID: laiUID, GID: laiGID}
	cases := []struct {
		name string
		id   Identity
		prep func(*memFS)
		want string
	}{
		{"world readable everywhere", id, func(fs *memFS) {}, ""},
		{"no identity, world readable", Identity{-1, -1}, func(fs *memFS) {}, ""},
		{"directory without o+x", id, func(fs *memFS) { fs.own("/srv/localai/models", 0, 0, 0o750) }, AccessNoTraverse},
		{"directory without o+x, owned by the user", id, func(fs *memFS) { fs.own("/srv/localai/models", laiUID, 0, 0o700) }, ""},
		{"directory without o+x, grouped to the user", id, func(fs *memFS) { fs.own("/srv/localai/models", 0, laiGID, 0o750) }, ""},
		{"directory group bit set but another group", id, func(fs *memFS) { fs.own("/srv/localai/models", 0, 5, 0o750) }, AccessNoTraverse},
		{"directory owned by the user without the owner x bit", id, func(fs *memFS) { fs.own("/srv/localai/models", laiUID, 0, 0o600) }, AccessNoTraverse},
		{"the root directory is closed", id, func(fs *memFS) { fs.own("/", 0, 0, 0o700) }, AccessNoTraverse},
		{"file without o+r", id, func(fs *memFS) { fs.own(file, 0, 0, 0o600) }, AccessNoRead},
		{"file without o+r, owned by the user", id, func(fs *memFS) { fs.own(file, laiUID, 0, 0o600) }, ""},
		{"file without o+r, grouped to the user", id, func(fs *memFS) { fs.own(file, 0, laiGID, 0o640) }, ""},
		{"file group readable but another group", id, func(fs *memFS) { fs.own(file, 0, 5, 0o640) }, AccessNoRead},
		{"file owned by the user, write only", id, func(fs *memFS) { fs.own(file, laiUID, 0, 0o200) }, AccessNoRead},
		{"unknown identity never matches an owner", Identity{-1, -1}, func(fs *memFS) { fs.own(file, 0, 0, 0o600) }, AccessNoRead},
		{"file missing", id, func(fs *memFS) { _ = fs.Remove(file) }, AccessMissing},
		{"directory missing", id, func(fs *memFS) {
			_ = fs.Remove(file)
			_ = fs.Remove("/srv/localai/models/m")
		}, AccessMissing},
		{"the file is a directory", id, func(fs *memFS) {
			_ = fs.Remove(file)
			fs.mkdirs(file + "/x")
			fs.mu.Lock()
			fs.entries[file] = &memEntry{dir: true, mode: 0o755}
			fs.mu.Unlock()
		}, AccessNotFile},
		{"a path component is a file", id, func(fs *memFS) {
			_ = fs.Remove("/srv/localai/models/m")
			fs.put("/srv/localai/models/m", []byte("x"), 0o644)
		}, AccessNotFile},
		{"stat fails", id, func(fs *memFS) { fs.failPath["/srv/localai/models/m"] = errors.New("EIO") }, AccessError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMemFS()
			fs.put(file, []byte("gguf"), 0o644)
			tc.prep(fs)
			if got := accessReason(CheckReadable(fs, tc.id, file)); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
	if got := accessReason(CheckReadable(newMemFS(), id, "relative/w.gguf")); got != AccessMissing {
		t.Fatalf("relative path: %q", got)
	}
	if (&AccessProblem{Path: "/x", Reason: AccessNoRead}).Error() != "file_not_readable: /x" {
		t.Fatal("AccessProblem.Error")
	}
	if id.String() != "uid=990 gid=991" {
		t.Fatalf("Identity.String = %q", id.String())
	}
}
