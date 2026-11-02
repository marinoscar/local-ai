package state_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

const (
	testDir  = "/var/lib/localai"
	testPath = testDir + "/state.json"
)

// failOn returns a Hook that fails every operation of one kind.
func failOn(kind string, err error) func(fake.Op) error {
	return func(op fake.Op) error {
		if op.Kind == kind {
			return err
		}
		return nil
	}
}

func tempNames(m *fake.MemFS) []string {
	var out []string
	for _, n := range m.Names(testDir) {
		if strings.Contains(n, ".tmp-") {
			out = append(out, n)
		}
	}
	return out
}

func TestWriteFileAtomicSuccessOrder(t *testing.T) {
	m := fake.New()
	m.Seed(testPath, []byte("old"))
	m.ResetOps()
	if err := state.WriteFileAtomic(m, testPath, []byte("new"), 0o640, 0, 1234); err != nil {
		t.Fatal(err)
	}
	// chown and chmod come before the fsync so the metadata is durable too; the directory is fsynced last.
	want := []string{fake.KindOpen, fake.KindWrite, fake.KindChown, fake.KindChmod, fake.KindSync, fake.KindClose, fake.KindRename, fake.KindSyncDir}
	var got []string
	for _, o := range m.Ops() {
		got = append(got, o.Kind)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	data, _ := m.Contents(testPath)
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	mode, uid, gid, _ := m.Mode(testPath)
	if mode != 0o640 || uid != 0 || gid != 1234 {
		t.Fatalf("mode/owner = %v %d:%d", mode, uid, gid)
	}
	if n := tempNames(m); len(n) != 0 {
		t.Fatalf("temp files left: %v", n)
	}
}

func TestWriteFileAtomicNoChownWhenOwnerUnset(t *testing.T) {
	m := fake.New()
	if err := state.WriteFileAtomic(m, testPath, []byte("x"), 0o600, -1, -1); err != nil {
		t.Fatal(err)
	}
	for _, o := range m.Ops() {
		if o.Kind == fake.KindChown {
			t.Fatal("chown called with uid and gid -1")
		}
	}
}

func TestWriteFileAtomicFailures(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		err      error
		wantNew  bool // the new content is in place (failure after the rename)
		wantTemp bool
	}{
		{"open ENOSPC", fake.KindOpen, syscall.ENOSPC, false, false},
		{"open EROFS", fake.KindOpen, syscall.EROFS, false, false},
		{"open EACCES", fake.KindOpen, syscall.EACCES, false, false},
		{"write ENOSPC", fake.KindWrite, syscall.ENOSPC, false, false},
		{"chown EPERM", fake.KindChown, syscall.EPERM, false, false},
		{"chmod EPERM", fake.KindChmod, syscall.EPERM, false, false},
		{"fsync EIO", fake.KindSync, syscall.EIO, false, false},
		{"close EIO", fake.KindClose, syscall.EIO, false, false},
		{"rename EACCES", fake.KindRename, syscall.EACCES, false, false},
		{"directory fsync EIO", fake.KindSyncDir, syscall.EIO, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fake.New()
			m.Seed(testPath, []byte("old"))
			m.Hook = failOn(tc.kind, tc.err)
			err := state.WriteFileAtomic(m, testPath, []byte("new"), 0o640, 0, 0)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.err)
			}
			data, _ := m.Contents(testPath)
			want := "old"
			if tc.wantNew {
				want = "new"
			}
			if string(data) != want {
				t.Fatalf("content = %q, want %q", data, want)
			}
			if n := tempNames(m); len(n) != 0 {
				t.Fatalf("temp files left: %v", n)
			}
		})
	}
}

func TestWriteFileAtomicShortWrite(t *testing.T) {
	m := fake.New()
	m.Seed(testPath, []byte("old"))
	m.Hook = failOn(fake.KindWrite, errors.New("boom"))
	if err := state.WriteFileAtomic(m, testPath, []byte("new"), 0o640, -1, -1); err == nil {
		t.Fatal("want an error")
	}
	if data, _ := m.Contents(testPath); string(data) != "old" {
		t.Fatalf("content = %q", data)
	}
}

// A kill at every possible point leaves the old or the new file, complete, and Open can sweep the
// temp file. Unfsynced bytes are dropped by Reboot, so this also proves the fsync-before-rename order.
func TestWriteFileAtomicCrashAtEveryStep(t *testing.T) {
	probe := fake.New()
	probe.Seed(testPath, []byte("old-content"))
	if err := state.WriteFileAtomic(probe, testPath, []byte("new-content"), 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	steps := probe.MutatingOps()
	for n := 0; n <= steps; n++ {
		m := fake.New()
		m.Seed(testPath, []byte("old-content"))
		m.CrashAfter(n)
		err := state.WriteFileAtomic(m, testPath, []byte("new-content"), 0o640, -1, -1)
		if n < steps && !errors.Is(err, fake.ErrCrashed) {
			t.Fatalf("crash after %d ops: err = %v", n, err)
		}
		m.Reboot()
		data, ok := m.Contents(testPath)
		if !ok || (string(data) != "old-content" && string(data) != "new-content") {
			t.Fatalf("crash after %d ops: state file = %q (exists %v), want old or new complete content", n, data, ok)
		}
	}
}

func TestOSFSRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f"
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFileAtomic(state.OSFS{}, path, []byte("new"), 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("read = %q, %v", data, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the file", len(entries))
	}
}
