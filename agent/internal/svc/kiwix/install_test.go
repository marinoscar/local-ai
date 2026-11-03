package kiwix

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- fault injection

// faultFS is OSFS with failures on demand.
type faultFS struct {
	OSFS
	// diskBytes is how many bytes may be written before every write fails with ENOSPC; -1 = no limit.
	diskBytes int64
	written   int64
	// renameErr fails a rename when it returns non-nil.
	renameErr func(oldpath, newpath string) error
	// open replaces the tarball reader.
	open func(name string) (ReadFile, error)
	// openCalls counts OpenFile (file creations) calls.
	openCalls int
}

func newFault() *faultFS { return &faultFS{diskBytes: -1} }

func (f *faultFS) Open(name string) (ReadFile, error) {
	if f.open != nil {
		return f.open(name)
	}
	return f.OSFS.Open(name)
}

func (f *faultFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f.openCalls++
	fl, err := f.OSFS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &limitFile{File: fl, fs: f}, nil
}

func (f *faultFS) Rename(o, n string) error {
	if f.renameErr != nil {
		if err := f.renameErr(o, n); err != nil {
			return err
		}
	}
	return f.OSFS.Rename(o, n)
}

type limitFile struct {
	File
	fs *faultFS
}

func (l *limitFile) Write(p []byte) (int, error) {
	if l.fs.diskBytes >= 0 && l.fs.written+int64(len(p)) > l.fs.diskBytes {
		room := l.fs.diskBytes - l.fs.written
		if room > 0 {
			n, _ := l.File.Write(p[:room])
			l.fs.written += int64(n)
			return n, syscall.ENOSPC
		}
		return 0, syscall.ENOSPC
	}
	n, err := l.File.Write(p)
	l.fs.written += int64(n)
	return n, err
}

// ---- helpers

func mustInstall(t *testing.T, e *env, tarball, sha, version, tmpPrinted string) {
	t.Helper()
	e.scriptVersion(version+".tmp", tmpPrinted)
	if err := e.inst.Install(context.Background(), tarball, sha, version); err != nil {
		t.Fatalf("install %s: %v", version, err)
	}
}

func wantKind(t *testing.T, err error, kind error) *InstallError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error of kind %q, got nil", kind)
	}
	if !errors.Is(err, kind) {
		t.Fatalf("error %q is not of kind %q", err, kind)
	}
	var ie *InstallError
	if !errors.As(err, &ie) {
		t.Fatalf("error %T is not an *InstallError", err)
	}
	if ie.Code() != doctor.SVC003 {
		t.Errorf("code = %s, want %s", ie.Code(), doctor.SVC003)
	}
	return ie
}

// ---- tests

func TestInstallLayouts(t *testing.T) {
	cases := []struct {
		name string
		top  string
	}{
		{"release layout with one top directory", "kiwix-tools_linux-x86_64-3.7.0-2"},
		{"programs at the top level", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			ents := release(tc.top, "v1")
			ents = append(ents, ent{name: joinTop(tc.top, "lib"), typ: tar.TypeDir, mode: 0o755},
				ent{name: joinTop(tc.top, "lib/libx.so.1"), body: "x"},
				ent{name: joinTop(tc.top, "lib/libx.so"), typ: tar.TypeSymlink, link: "libx.so.1"})
			tb, sha := e.tarball("kiwix-tools_linux-x86_64-3.7.0-2.tar.gz", ents)
			mustInstall(t, e, tb, sha, "3.7.0-2", "3.7.0")

			for _, b := range Binaries {
				fi, err := os.Stat(filepath.Join(e.root, "3.7.0-2", b))
				if err != nil {
					t.Fatalf("%s: %v", b, err)
				}
				if fi.Mode().Perm() != 0o755 {
					t.Errorf("%s mode = %v, want 0755", b, fi.Mode().Perm())
				}
			}
			if l, err := os.Readlink(filepath.Join(e.root, "3.7.0-2", "lib", "libx.so")); err != nil || l != "libx.so.1" {
				t.Errorf("sibling symlink = %q, %v", l, err)
			}
			if fi, _ := os.Stat(filepath.Join(e.root, "3.7.0-2", "lib", "libx.so.1")); fi == nil || fi.Mode().Perm() != 0o644 {
				t.Errorf("plain file mode = %v", fi)
			}
			if b, _ := os.ReadFile(filepath.Join(e.root, "3.7.0-2", MarkerName)); strings.TrimSpace(string(b)) != sha {
				t.Errorf("marker = %q", b)
			}
			if got := e.target(); got != "3.7.0-2" {
				t.Errorf("current -> %q", got)
			}
			e.assertClean("3.7.0-2")
		})
	}
}

func joinTop(top, rest string) string {
	if top == "" {
		return rest
	}
	return top + "/" + rest
}

func TestInstallRefusesBadInput(t *testing.T) {
	e := newEnv(t, nil)
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "v1"))
	arm, armSHA := e.tarball("kiwix-tools_linux-aarch64-3.7.0.tar.gz", release("d", "v1"))
	cases := []struct {
		name, tarball, sha, version string
	}{
		{"empty version", tb, sha, ""},
		{"version with a slash", tb, sha, "3.7/../x"},
		{"version that is current", tb, sha, "current"},
		{"version ending in .tmp", tb, sha, "3.7.0.tmp"},
		{"version ending in .bad", tb, sha, "3.7.0.bad"},
		{"version not starting with a digit", tb, sha, "v3.7.0"},
		{"no sha", tb, "", "3.7.0"},
		{"short sha", tb, sha[:40], "3.7.0"},
		{"non-hex sha", tb, strings.Repeat("g", 64), "3.7.0"},
		{"no tarball", "", sha, "3.7.0"},
		{"tarball for the other architecture", arm, armSHA, "3.7.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := e.inst.Install(context.Background(), tc.tarball, tc.sha, tc.version)
			wantKind(t, err, ErrBadInput)
			if e.target() != "" || e.exists("3.7.0") {
				t.Error("something was installed")
			}
		})
	}
	t.Run("missing tarball file", func(t *testing.T) {
		wantKind(t, e.inst.Install(context.Background(), filepath.Join(e.tarDir, "nope.tar.gz"), sha, "3.7.0"), ErrIO)
	})
	t.Run("architecture other than amd64 and arm64", func(t *testing.T) {
		i := NewInstaller(Options{Root: e.root, Runner: e.run, Arch: "riscv64"})
		wantKind(t, i.Install(context.Background(), tb, sha, "3.7.0"), ErrBadInput)
	})
	if e.run.Calls() != nil && len(e.run.Calls()) != 0 {
		t.Errorf("no program may run for refused input: %v", e.run.Calls())
	}
}

func TestInstallChecksumMismatchLeavesCurrentUntouched(t *testing.T) {
	e := newEnv(t, nil)
	v1, sha1 := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
	mustInstall(t, e, v1, sha1, "1.0.0", "1.0.0")

	v2, sha2 := e.tarball("kiwix-tools_linux-x86_64-2.0.0.tar.gz", release("d", "v2"))
	wrong := []string{sha1, strings.Repeat("0", 64), strings.ToUpper(sha1)}
	for _, w := range wrong {
		before := len(e.run.Calls())
		err := e.inst.Install(context.Background(), v2, w, "2.0.0")
		wantKind(t, err, ErrChecksumMismatch)
		if got := e.target(); got != "1.0.0" {
			t.Fatalf("current -> %q after a refused install", got)
		}
		if e.exists("2.0.0") {
			t.Fatal("the refused version was installed")
		}
		if len(e.run.Calls()) != before {
			t.Error("a program ran for a refused tarball")
		}
	}
	e.assertClean("2.0.0")
	// A correct, upper-case pin is accepted (the MANIFEST may spell it either way).
	e.scriptVersion("2.0.0.tmp", "2.0.0")
	if err := e.inst.Install(context.Background(), v2, strings.ToUpper(sha2), "2.0.0"); err != nil {
		t.Fatalf("upper-case sha: %v", err)
	}
}

func TestInstallTwiceKeepsPreviousVersion(t *testing.T) {
	e := newEnv(t, nil)
	v1, sha1 := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
	v2, sha2 := e.tarball("kiwix-tools_linux-x86_64-2.0.0.tar.gz", release("d", "v2"))
	mustInstall(t, e, v1, sha1, "1.0.0", "1.0.0")
	if e.target() != "1.0.0" {
		t.Fatalf("current -> %q", e.target())
	}
	mustInstall(t, e, v2, sha2, "2.0.0", "2.0.0")
	if got := e.target(); got != "2.0.0" {
		t.Errorf("current -> %q, want 2.0.0", got)
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		for _, b := range Binaries {
			if !e.exists(filepath.Join(v, b)) {
				t.Errorf("%s/%s is missing", v, b)
			}
		}
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "1.0.0", BinServe)); string(b) != "kiwix-serve v1" {
		t.Errorf("the previous version was changed: %q", b)
	}
	e.assertClean("2.0.0")
	// current resolves through the symlink: the unit's ExecStart path works.
	if b, err := os.ReadFile(filepath.Join(e.root, CurrentName, BinServe)); err != nil || string(b) != "kiwix-serve v2" {
		t.Errorf("current/kiwix-serve = %q, %v", b, err)
	}
}

func TestReinstallSameVersionReusesTheDirectory(t *testing.T) {
	ffs := newFault()
	e := newEnv(t, ffs)
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "v1"))
	mustInstall(t, e, tb, sha, "3.7.0", "3.7.0")
	creates := ffs.openCalls
	e.scriptVersion("3.7.0", "3.7.0") // a reuse runs the program in place

	if err := e.inst.Install(context.Background(), tb, sha, "3.7.0"); err != nil {
		t.Fatal(err)
	}
	if ffs.openCalls != creates {
		t.Errorf("a reinstall of the same tarball created %d files; it must reuse the directory", ffs.openCalls-creates)
	}
	if e.target() != "3.7.0" {
		t.Errorf("current -> %q", e.target())
	}
	e.assertClean("3.7.0")

	damage := map[string]func(t *testing.T){
		"marker removed": func(t *testing.T) { _ = os.Remove(filepath.Join(e.root, "3.7.0", MarkerName)) },
		"program no longer executable": func(t *testing.T) {
			if err := os.Chmod(filepath.Join(e.root, "3.7.0", BinManage), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"program removed": func(t *testing.T) { _ = os.Remove(filepath.Join(e.root, "3.7.0", BinSearch)) },
		"marker of another tarball": func(t *testing.T) {
			_ = os.WriteFile(filepath.Join(e.root, "3.7.0", MarkerName), []byte(strings.Repeat("a", 64)+"\n"), 0o644)
		},
	}
	for name, d := range damage {
		t.Run("a damaged directory is re-extracted: "+name, func(t *testing.T) {
			d(t)
			before := ffs.openCalls
			e.scriptVersion("3.7.0.tmp", "3.7.0")
			if err := e.inst.Install(context.Background(), tb, sha, "3.7.0"); err != nil {
				t.Fatal(err)
			}
			if ffs.openCalls == before {
				t.Error("the damaged directory was reused")
			}
			for _, b := range Binaries {
				fi, err := os.Stat(filepath.Join(e.root, "3.7.0", b))
				if err != nil || fi.Mode().Perm() != 0o755 {
					t.Errorf("%s: %v %v", b, fi, err)
				}
			}
			e.assertClean("3.7.0")
		})
	}
}

func TestSameVersionDifferentTarballReplacesTheDirectory(t *testing.T) {
	e := newEnv(t, nil)
	a, shaA := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "old"))
	mustInstall(t, e, a, shaA, "3.7.0", "3.7.0")
	b, shaB := e.tarball("kiwix-tools_linux-x86_64-3.7.0-rebuilt.tar.gz", release("d", "new"))
	mustInstall(t, e, b, shaB, "3.7.0", "3.7.0")
	if got, _ := os.ReadFile(filepath.Join(e.root, "3.7.0", BinServe)); string(got) != "kiwix-serve new" {
		t.Errorf("content = %q", got)
	}
	if e.target() != "3.7.0" {
		t.Errorf("current -> %q", e.target())
	}
	e.assertClean("3.7.0")
}

func TestInstallRefusesUnsafeArchives(t *testing.T) {
	good := release("d", "v1")
	cases := []struct {
		name string
		ents []ent
		kind error
	}{
		{"absolute path", append(append([]ent{}, good...), ent{name: "/etc/localai-pwned", body: "x"}), ErrUnsafeArchive},
		{"parent traversal", append(append([]ent{}, good...), ent{name: "d/../../outside.txt", body: "pwned"}), ErrUnsafeArchive},
		{"parent traversal first", append([]ent{{name: "../outside.txt", body: "pwned"}}, good...), ErrUnsafeArchive},
		{"deep traversal", append(append([]ent{}, good...), ent{name: "d/a/../../../outside.txt", body: "pwned"}), ErrUnsafeArchive},
		{"backslash name", append(append([]ent{}, good...), ent{name: `d\..\outside.txt`, body: "x"}), ErrUnsafeArchive},
		{"symlink to an absolute path", append(append([]ent{}, good...), ent{name: "d/etc", typ: tar.TypeSymlink, link: "/etc"}), ErrUnsafeArchive},
		{"symlink to the parent", append(append([]ent{}, good...), ent{name: "d/up", typ: tar.TypeSymlink, link: ".."}), ErrUnsafeArchive},
		{"symlink with a relative escape", append(append([]ent{}, good...), ent{name: "d/up", typ: tar.TypeSymlink, link: "../../outside.txt"}), ErrUnsafeArchive},
		{"symlink into a subdirectory", append(append([]ent{}, good...), ent{name: "d/s", typ: tar.TypeSymlink, link: "sub/x"}), ErrUnsafeArchive},
		{"empty symlink target", append(append([]ent{}, good...), ent{name: "d/s", typ: tar.TypeSymlink, link: ""}), ErrUnsafeArchive},
		{"write through a symlink", append(append([]ent{}, good...),
			ent{name: "d/sub", typ: tar.TypeDir, mode: 0o755},
			ent{name: "d/link", typ: tar.TypeSymlink, link: "sub"},
			ent{name: "d/link/file", body: "x"}), ErrUnsafeArchive},
		{"hard link", append(append([]ent{}, good...), ent{name: "d/hl", typ: tar.TypeLink, link: "d/kiwix-serve"}), ErrUnsafeArchive},
		{"device node", append(append([]ent{}, good...), ent{name: "d/dev", typ: tar.TypeChar}), ErrUnsafeArchive},
		{"duplicate file", append(append([]ent{}, good...), ent{name: "d/kiwix-serve", body: "again"}), ErrUnsafeArchive},
		{"the installer's marker name", append(append([]ent{}, good...), ent{name: "d/" + MarkerName, body: "x"}), ErrUnsafeArchive},
		{"a file that is also a symlink name", append(append([]ent{}, good...),
			ent{name: "d/a", typ: tar.TypeSymlink, link: "kiwix-serve"}, ent{name: "d/a", body: "x"}), ErrUnsafeArchive},
		{"program missing", []ent{{name: "d/kiwix-serve", body: "x"}, {name: "d/kiwix-manage", body: "x"}}, ErrMissingBinary},
		{"program is a symlink, not a file", []ent{{name: "d/kiwix-serve", typ: tar.TypeSymlink, link: "kiwix-manage"},
			{name: "d/kiwix-manage", body: "x"}, {name: "d/kiwix-search", body: "x"}}, ErrMissingBinary},
		{"programs nested too deep", []ent{{name: "d/bin/kiwix-serve", body: "x"}, {name: "d/bin/kiwix-manage", body: "x"}, {name: "d/bin/kiwix-search", body: "x"}}, ErrMissingBinary},
		{"empty archive", nil, ErrMissingBinary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			v0, sha0 := e.tarball("kiwix-tools_linux-x86_64-0.9.0.tar.gz", release("d", "v0"))
			mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")

			tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", tc.ents)
			err := e.inst.Install(context.Background(), tb, sha, "1.0.0")
			wantKind(t, err, tc.kind)
			if got := e.target(); got != "0.9.0" {
				t.Errorf("current -> %q after a refused archive", got)
			}
			if e.exists("1.0.0") {
				t.Error("the refused version directory exists")
			}
			e.assertClean("1.0.0")
		})
	}
}

func TestInstallRefusesNonGzipAndDamagedArchives(t *testing.T) {
	e := newEnv(t, nil)
	good := buildTar(t, release("d", "v1"))
	cases := map[string][]byte{
		"plain text":     []byte("this is not an archive"),
		"empty file":     {},
		"truncated gzip": good[:len(good)/2],
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			tb, sha := writeTarball(t, e.tarDir, "kiwix-tools_linux-x86_64-1.0.0.tar.gz", data)
			err := e.inst.Install(context.Background(), tb, sha, "1.0.0")
			if err == nil {
				t.Fatal("a damaged archive was installed")
			}
			if !errors.Is(err, ErrUnsafeArchive) && !errors.Is(err, ErrMissingBinary) && !errors.Is(err, ErrIO) {
				t.Errorf("unexpected kind: %v", err)
			}
			if e.target() != "" || e.exists("1.0.0") {
				t.Error("something was installed")
			}
			e.assertClean("1.0.0")
		})
	}
}

func TestInstallTooManyEntries(t *testing.T) {
	e := newEnv(t, nil)
	ents := release("d", "v1")
	for i := 0; i < maxEntries+1; i++ {
		ents = append(ents, ent{name: fmt.Sprintf("d/f%04d", i), body: "x"})
	}
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", ents)
	wantKind(t, e.inst.Install(context.Background(), tb, sha, "1.0.0"), ErrUnsafeArchive)
	e.assertClean("1.0.0")
}

func TestInstallDiskFull(t *testing.T) {
	// Sizes of the fixture: the three programs are about 20 bytes each; every limit below cuts the
	// extraction at a different file (and one cuts the marker).
	for _, limit := range []int64{0, 5, 25, 45, 70} {
		t.Run(fmt.Sprintf("disk full after %d bytes", limit), func(t *testing.T) {
			ffs := newFault()
			e := newEnv(t, ffs)
			v0, sha0 := e.tarball("kiwix-tools_linux-x86_64-0.9.0.tar.gz", release("d", "v0"))
			mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")

			tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
			ffs.diskBytes, ffs.written = limit, 0
			err := e.inst.Install(context.Background(), tb, sha, "1.0.0")
			wantKind(t, err, ErrIO)
			if !errors.Is(err, syscall.ENOSPC) {
				t.Errorf("the cause is not ENOSPC: %v", err)
			}
			if got := e.target(); got != "0.9.0" {
				t.Errorf("current -> %q after a full disk", got)
			}
			if e.exists("1.0.0") {
				t.Error("a half-extracted version directory exists")
			}
			e.assertClean("1.0.0")
		})
	}
}

func TestInstallAfterAKilledRun(t *testing.T) {
	e := newEnv(t, nil)
	tmp := filepath.Join(e.root, "1.0.0.tmp")
	if err := os.MkdirAll(filepath.Join(tmp, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A previous process died after writing one program: the same name is there, so an exclusive
	// create would fail if the debris were not cleared.
	if err := os.WriteFile(filepath.Join(tmp, BinServe), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a current.tmp from a killed repoint.
	if err := os.Symlink("9.9.9", filepath.Join(e.root, CurrentName+".tmp")); err != nil {
		t.Fatal(err)
	}
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
	mustInstall(t, e, tb, sha, "1.0.0", "1.0.0")
	if b, _ := os.ReadFile(filepath.Join(e.root, "1.0.0", BinServe)); string(b) != "kiwix-serve v1" {
		t.Errorf("content = %q", b)
	}
	if e.exists("1.0.0/stale") {
		t.Error("debris of the killed run survived")
	}
	if e.target() != "1.0.0" {
		t.Errorf("current -> %q", e.target())
	}
	e.assertClean("1.0.0")
}

func TestInstallSmokeTestFailures(t *testing.T) {
	cases := []struct {
		name string
		res  execx.Result
		err  error
	}{
		{"another version", versionOut("2.9.9"), nil},
		{"does not run", execx.Result{}, errors.New("exec format error")},
		{"exits non-zero", execx.Result{ExitCode: 127, Stderr: []byte("not found")}, nil},
		{"prints no version line", execx.Result{Stdout: []byte("hello\n")}, nil},
		{"a prefix of the version is not the version", versionOut("3.7"), nil},
		{"timed out", execx.Result{}, execx.ErrTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			v0, sha0 := e.tarball("kiwix-tools_linux-x86_64-0.9.0.tar.gz", release("d", "v0"))
			mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")

			tb, sha := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "v1"))
			e.run.Expect(fakeKey(filepath.Join(e.root, "3.7.0.tmp", BinServe)), tc.res, tc.err)
			err := e.inst.Install(context.Background(), tb, sha, "3.7.0")
			wantKind(t, err, ErrVersionMismatch)
			if e.target() != "0.9.0" {
				t.Errorf("current -> %q: a program that cannot run became current", e.target())
			}
			if e.exists("3.7.0") {
				t.Error("the version directory was moved into place")
			}
			e.assertClean("3.7.0")
		})
	}
}

func fakeKey(bin string) string { return bin + " --version" }

func TestSmokeFailureOnReuseKeepsCurrent(t *testing.T) {
	e := newEnv(t, nil)
	v0, sha0 := e.tarball("kiwix-tools_linux-x86_64-0.9.0.tar.gz", release("d", "v0"))
	mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "v1"))
	mustInstall(t, e, tb, sha, "3.7.0", "3.7.0")
	// Back to the old one (reused in place), then reinstall 3.7.0 whose in-place program now fails.
	e.scriptVersion("0.9.0", "0.9.0")
	mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")
	e.run.Expect(fakeKey(filepath.Join(e.root, "3.7.0", BinServe)), execx.Result{ExitCode: 1}, nil)
	wantKind(t, e.inst.Install(context.Background(), tb, sha, "3.7.0"), ErrVersionMismatch)
	if e.target() != "0.9.0" {
		t.Errorf("current -> %q", e.target())
	}
}

func TestInstallRepointFailures(t *testing.T) {
	t.Run("current is a real directory", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := os.MkdirAll(filepath.Join(e.root, CurrentName, "keep"), 0o755); err != nil {
			t.Fatal(err)
		}
		tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
		e.scriptVersion("1.0.0.tmp", "1.0.0")
		wantKind(t, e.inst.Install(context.Background(), tb, sha, "1.0.0"), ErrIO)
		if !e.exists("current/keep") {
			t.Error("a real directory named current was deleted")
		}
	})
	t.Run("the rename over current fails", func(t *testing.T) {
		ffs := newFault()
		e := newEnv(t, ffs)
		v0, sha0 := e.tarball("kiwix-tools_linux-x86_64-0.9.0.tar.gz", release("d", "v0"))
		mustInstall(t, e, v0, sha0, "0.9.0", "0.9.0")
		ffs.renameErr = func(o, n string) error {
			if filepath.Base(n) == CurrentName {
				return syscall.EROFS
			}
			return nil
		}
		tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
		e.scriptVersion("1.0.0.tmp", "1.0.0")
		wantKind(t, e.inst.Install(context.Background(), tb, sha, "1.0.0"), ErrIO)
		if e.target() != "0.9.0" {
			t.Errorf("current -> %q", e.target())
		}
		if e.exists("current.tmp") {
			t.Error("current.tmp was left behind")
		}
	})
	t.Run("moving the directory into place fails", func(t *testing.T) {
		ffs := newFault()
		e := newEnv(t, ffs)
		ffs.renameErr = func(o, n string) error {
			if strings.HasSuffix(o, ".tmp") && filepath.Base(n) == "1.0.0" {
				return syscall.EIO
			}
			return nil
		}
		tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
		e.scriptVersion("1.0.0.tmp", "1.0.0")
		wantKind(t, e.inst.Install(context.Background(), tb, sha, "1.0.0"), ErrIO)
		e.assertClean("1.0.0")
	})
	t.Run("a failed swap puts the old directory back", func(t *testing.T) {
		ffs := newFault()
		e := newEnv(t, ffs)
		a, shaA := e.tarball("kiwix-tools_linux-x86_64-3.7.0.tar.gz", release("d", "old"))
		mustInstall(t, e, a, shaA, "3.7.0", "3.7.0")
		ffs.renameErr = func(o, n string) error {
			if strings.HasSuffix(o, ".tmp") && filepath.Base(n) == "3.7.0" {
				return syscall.EIO
			}
			return nil
		}
		b, shaB := e.tarball("kiwix-tools_linux-x86_64-3.7.0-b.tar.gz", release("d", "new"))
		e.scriptVersion("3.7.0.tmp", "3.7.0")
		wantKind(t, e.inst.Install(context.Background(), b, shaB, "3.7.0"), ErrIO)
		if got, _ := os.ReadFile(filepath.Join(e.root, "3.7.0", BinServe)); string(got) != "kiwix-serve old" {
			t.Errorf("the old directory was not restored: %q", got)
		}
		e.assertClean("3.7.0")
	})
}

// memFile is a tarball reader whose content can change while the install runs.
type memFile struct {
	r     *bytes.Reader
	swap  func(seeks int) []byte // returns new content at a Seek(0), or nil to keep
	seeks int
}

func (m *memFile) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m *memFile) Close() error               { return nil }
func (m *memFile) Seek(off int64, whence int) (int64, error) {
	if off == 0 && whence == io.SeekStart {
		m.seeks++
		if b := m.swap(m.seeks); b != nil {
			m.r = bytes.NewReader(b)
		}
	}
	return m.r.Seek(off, whence)
}

func TestInstallDetectsATarballThatChangesDuringTheInstall(t *testing.T) {
	good := buildTar(t, release("d", "v1"))
	evil := buildTar(t, release("d", "EVIL")) // a valid archive with other content
	sha := shaHex(good)

	ffs := newFault()
	ffs.open = func(string) (ReadFile, error) {
		return &memFile{r: bytes.NewReader(good), swap: func(seeks int) []byte {
			// seek 1 = the hash pass, seek 2 = the validation pass, seek 3 = the write pass.
			if seeks == 3 {
				return evil
			}
			return nil
		}}, nil
	}
	e := newEnv(t, ffs)
	err := e.inst.Install(context.Background(), "kiwix-tools_linux-x86_64-1.0.0.tar.gz", sha, "1.0.0")
	wantKind(t, err, ErrChecksumMismatch)
	if e.target() != "" || e.exists("1.0.0") {
		t.Error("tampered content was installed")
	}
	e.assertClean("1.0.0")
}

func TestInstallHonoursACancelledContext(t *testing.T) {
	e := newEnv(t, nil)
	tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.inst.Install(ctx, tb, sha, "1.0.0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if e.exists("1.0.0") || e.exists("1.0.0.tmp") {
		t.Error("a cancelled install left files behind")
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		version string
		res     execx.Result
		err     error
		want    error // nil = ok
	}{
		{"exact", "3.7.0", versionOut("3.7.0"), nil, nil},
		{"packaging revision in the file name only", "3.7.0-2", versionOut("3.7.0"), nil, nil},
		{"the binary prints the revision too", "3.7.0-2", versionOut("3.7.0-2"), nil, nil},
		{"on stderr", "3.7.0", execx.Result{Stderr: []byte("kiwix-tools 3.7.0\n")}, nil, nil},
		{"a kiwix-serve line is accepted too", "3.7.0", execx.Result{Stdout: []byte("kiwix-serve 3.7.0\n")}, nil, nil},
		{"the library versions are not the tools version", "3.7.0", execx.Result{Stdout: []byte("libkiwix 3.7.0\n")}, nil, ErrVersionMismatch},
		{"different version", "3.8.0", versionOut("3.7.0"), nil, ErrVersionMismatch},
		{"different revision", "3.7.0-2", versionOut("3.7.0-1"), nil, ErrVersionMismatch},
		{"revision expected, other minor printed", "3.8.0-1", versionOut("3.7.0"), nil, ErrVersionMismatch},
		{"missing binary", "3.7.0", execx.Result{}, errors.New("no such file or directory"), ErrVersionMismatch},
		{"non-zero exit", "3.7.0", execx.Result{ExitCode: 2}, nil, ErrVersionMismatch},
		{"no version line", "3.7.0", execx.Result{Stdout: []byte("usage: kiwix-serve\n")}, nil, ErrVersionMismatch},
		{"bad version argument", "../3.7.0", versionOut("3.7.0"), nil, ErrBadInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			e.run.Expect(fakeKey(filepath.Join(e.root, CurrentName, BinServe)), tc.res, tc.err)
			err := e.inst.Check(context.Background(), tc.version)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantKind(t, err, tc.want)
		})
	}
	t.Run("runs current/kiwix-serve with a fixed argv and a timeout", func(t *testing.T) {
		e := newEnv(t, nil)
		e.run.Expect(fakeKey(filepath.Join(e.root, CurrentName, BinServe)), versionOut("3.7.0"), nil)
		if err := e.inst.Check(context.Background(), "3.7.0"); err != nil {
			t.Fatal(err)
		}
		c := e.run.Calls()
		if len(c) != 1 || c[0].Timeout <= 0 || c[0].MaxOut <= 0 || len(c[0].Args) != 1 || c[0].Args[0] != "--version" {
			t.Errorf("calls = %+v", c)
		}
	})
	t.Run("a cancelled context is returned as is", func(t *testing.T) {
		e := newEnv(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e.run.Expect(fakeKey(filepath.Join(e.root, CurrentName, BinServe)), execx.Result{}, context.Canceled)
		if err := e.inst.Check(ctx, "3.7.0"); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestArchMapping(t *testing.T) {
	cases := []struct {
		goarch, suffix string
		ok             bool
	}{
		{"amd64", "x86_64", true},
		{"arm64", "aarch64", true},
		{"386", "", false},
		{"riscv64", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, err := ArchSuffix(tc.goarch)
		if (err == nil) != tc.ok || got != tc.suffix {
			t.Errorf("ArchSuffix(%q) = %q, %v", tc.goarch, got, err)
		}
		if err != nil {
			wantKind(t, err, ErrBadInput)
		}
	}
	if n, err := TarballName("3.7.0-2", "amd64"); err != nil || n != "kiwix-tools_linux-x86_64-3.7.0-2.tar.gz" {
		t.Errorf("TarballName amd64 = %q, %v", n, err)
	}
	if n, err := TarballName("3.7.0-2", "arm64"); err != nil || n != "kiwix-tools_linux-aarch64-3.7.0-2.tar.gz" {
		t.Errorf("TarballName arm64 = %q, %v", n, err)
	}
	if _, err := TarballName("3.7.0", "mips"); err == nil {
		t.Error("TarballName accepted mips")
	}
	p, err := KitPath("/srv/localai", "3.7.0-2", "arm64")
	if err != nil || p != "/srv/localai/kit/tools/kiwix-tools/kiwix-tools_linux-aarch64-3.7.0-2.tar.gz" {
		t.Errorf("KitPath = %q, %v", p, err)
	}
	if _, err := KitPath("/srv/localai", "3.7.0", "mips"); err == nil {
		t.Error("KitPath accepted mips")
	}
}

func TestDefaultsAreTheCanonicalPaths(t *testing.T) {
	i := NewInstaller(Options{})
	if i.ServePath() != "/opt/localai/kiwix-tools/current/kiwix-serve" {
		t.Errorf("ServePath = %s", i.ServePath())
	}
}

func TestDoctorCheck(t *testing.T) {
	t.Run("missing program raises LAI-SVC-003", func(t *testing.T) {
		e := newEnv(t, nil)
		c := e.inst.DoctorCheck(nil)
		if c.ID != CheckInstalled || c.Area != "SVC" || !c.Fast || c.Run == nil {
			t.Fatalf("check = %+v", c)
		}
		res := c.Run(context.Background())
		if len(res) != 1 || res[0].Code != doctor.SVC003 || res[0].Details["reason"] != "missing" {
			t.Fatalf("results = %+v", res)
		}
		if res[0].Details["path"] != filepath.Join(e.root, "current", "kiwix-serve") {
			t.Errorf("path = %v", res[0].Details["path"])
		}
		if !doctor.Emittable(res[0].Code) {
			t.Error("the code is not emittable")
		}
		if e.inst.Installed() {
			t.Error("Installed() is true")
		}
	})
	t.Run("installed program is ok", func(t *testing.T) {
		e := newEnv(t, nil)
		tb, sha := e.tarball("kiwix-tools_linux-x86_64-1.0.0.tar.gz", release("d", "v1"))
		mustInstall(t, e, tb, sha, "1.0.0", "1.0.0")
		if res := e.inst.DoctorCheck(nil).Run(context.Background()); len(res) != 0 {
			t.Errorf("results = %+v", res)
		}
		if !e.inst.Installed() {
			t.Error("Installed() is false")
		}
	})
	t.Run("dangling current raises it", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := os.MkdirAll(e.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("9.9.9", filepath.Join(e.root, CurrentName)); err != nil {
			t.Fatal(err)
		}
		res := e.inst.DoctorCheck(nil).Run(context.Background())
		if len(res) != 1 || res[0].Code != doctor.SVC003 {
			t.Errorf("results = %+v", res)
		}
	})
	t.Run("a directory in place of the program raises it", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := os.MkdirAll(filepath.Join(e.root, CurrentName, BinServe), 0o755); err != nil {
			t.Fatal(err)
		}
		res := e.inst.DoctorCheck(nil).Run(context.Background())
		if len(res) != 1 || res[0].Details["reason"] != "not_a_file" {
			t.Errorf("results = %+v", res)
		}
	})
	t.Run("kiwix.enabled false reports nothing", func(t *testing.T) {
		e := newEnv(t, nil)
		if res := e.inst.DoctorCheck(func() bool { return false }).Run(context.Background()); len(res) != 0 {
			t.Errorf("results = %+v", res)
		}
		if res := e.inst.DoctorCheck(func() bool { return true }).Run(context.Background()); len(res) != 1 {
			t.Errorf("results = %+v", res)
		}
	})
}

func TestInstallErrorShape(t *testing.T) {
	cause := errors.New("boom")
	e := fail(ErrIO, "cannot write x", cause)
	if !errors.Is(e, ErrIO) || !errors.Is(e, cause) {
		t.Error("the error does not match its kind and cause")
	}
	if !strings.Contains(e.Error(), "cannot write x") || !strings.Contains(e.Error(), "boom") {
		t.Errorf("message = %q", e.Error())
	}
	if _, ok := doctor.Lookup(e.(*InstallError).Code()); !ok {
		t.Error("the code is not in the doctor table")
	}
}
