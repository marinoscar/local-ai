package piper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// ent is one entry of a fixture tarball.
type ent struct {
	name string
	typ  byte // tar.TypeReg (default), TypeDir, TypeSymlink, TypeLink, TypeChar
	body string
	link string
	mode int64
}

// buildTar builds a gzip+tar archive in memory.
func buildTar(t testing.TB, ents []ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// release is the shape of the real piper_linux_x86_64.tar.gz of 2023.11.14-2: one top directory
// with the program, three versioned libraries and their soname symlinks, the phoneme data tree and
// a few extra files. The libraries are 0644 in the real archive; piper alone is executable.
func release(tag string) []ent {
	return []ent{
		{name: "piper/", typ: tar.TypeDir, mode: 0o755},
		{name: "piper/piper", body: "piper " + tag, mode: 0o644}, // the installer, not the archive, makes it 0755
		{name: "piper/piper_phonemize", body: "phonemize", mode: 0o755},
		{name: "piper/libespeak-ng.so.1.52.0.1", body: "espeak"},
		{name: "piper/libespeak-ng.so.1", typ: tar.TypeSymlink, link: "libespeak-ng.so.1.52.0.1"},
		{name: "piper/libespeak-ng.so", typ: tar.TypeSymlink, link: "libespeak-ng.so.1"},
		{name: "piper/libpiper_phonemize.so.1.2.0", body: "phonemize lib"},
		{name: "piper/libpiper_phonemize.so.1", typ: tar.TypeSymlink, link: "libpiper_phonemize.so.1.2.0"},
		{name: "piper/libonnxruntime.so.1.14.1", body: "onnx"},
		{name: "piper/libonnxruntime.so", typ: tar.TypeSymlink, link: "libonnxruntime.so.1.14.1"},
		{name: "piper/libtashkeel_model.ort", body: "model"},
		{name: "piper/espeak-ng-data/", typ: tar.TypeDir, mode: 0o755},
		{name: "piper/espeak-ng-data/en_dict", body: "dict"},
		{name: "piper/espeak-ng-data/voices/", typ: tar.TypeDir, mode: 0o755},
		{name: "piper/espeak-ng-data/voices/en", body: "voice"},
		{name: "piper/pkgconfig/", typ: tar.TypeDir, mode: 0o755},
	}
}

// without returns ents minus the entries whose name has the prefix.
func without(ents []ent, prefix string) []ent {
	var out []ent
	for _, e := range ents {
		if !strings.HasPrefix(e.name, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const usage = "usage: piper [options]\n  --model  FILE\n  --output_raw  stream raw audio to stdout\n"

// installEnv is one test installation: a root under a temp dir, a fake runner, an Installer.
type installEnv struct {
	t       *testing.T
	base    string
	root    string
	run     *fake.Runner
	inst    *Installer
	tarDir  string
	outside string
}

func newInstallEnv(t *testing.T, fsys FS, voice func() (string, string, bool)) *installEnv {
	t.Helper()
	base := t.TempDir()
	e := &installEnv{t: t, base: base, root: filepath.Join(base, "opt", "piper"), run: fake.New(), tarDir: filepath.Join(base, "kit")}
	if err := os.MkdirAll(e.tarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.outside = filepath.Join(base, "opt", "outside.txt")
	if err := os.MkdirAll(filepath.Dir(e.outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.inst = NewInstaller(Options{Root: e.root, FS: fsys, Runner: e.run, Arch: "amd64", Voice: voice,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return e
}

// scriptHelp scripts the smoke test of directory dir (a version dir, its .tmp, or "current").
func (e *installEnv) scriptHelp(dir string, res execx.Result) {
	e.run.Expect(fake.Key(filepath.Join(e.root, dir, BinPiper), "--help"), res, nil)
}

func (e *installEnv) helpOK(dir string) { e.scriptHelp(dir, execx.Result{Stderr: []byte(usage)}) }

func (e *installEnv) tarball(name string, ents []ent) (string, string) {
	data := buildTar(e.t, ents)
	p := filepath.Join(e.tarDir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p, shaHex(data)
}

func (e *installEnv) target() string {
	e.t.Helper()
	l, err := os.Readlink(filepath.Join(e.root, CurrentName))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *installEnv) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.root, rel))
	return err == nil
}

// assertClean checks that nothing was left behind or written outside the root.
func (e *installEnv) assertClean(version string) {
	e.t.Helper()
	for _, n := range []string{version + ".tmp", version + ".bad", CurrentName + ".tmp"} {
		if e.exists(n) {
			e.t.Errorf("%s was left behind", n)
		}
	}
	if b, err := os.ReadFile(e.outside); err != nil || string(b) != "untouched" {
		e.t.Errorf("a file outside the root changed: %q %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(e.root))
	for _, en := range entries {
		if en.Name() != "piper" && en.Name() != "outside.txt" {
			e.t.Errorf("unexpected entry next to the root: %s", en.Name())
		}
	}
}

func TestInstallExtractsSmokeTestsAndSwitchesCurrent(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("2023.11.14-2.tmp")

	if err := e.inst.Install(context.Background(), p, sum, "2023.11.14-2"); err != nil {
		t.Fatal(err)
	}
	if got := e.target(); got != "2023.11.14-2" {
		t.Errorf("current -> %q", got)
	}
	ver := filepath.Join(e.root, "2023.11.14-2")
	// The top directory of the archive is stripped; the program is executable whatever the archive said.
	fi, err := os.Stat(filepath.Join(ver, BinPiper))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("piper: %v %v, want mode 0755", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(ver, "libonnxruntime.so.1.14.1")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("a library keeps 0644: %v %v", fi, err)
	}
	for link, target := range map[string]string{
		"libespeak-ng.so.1": "libespeak-ng.so.1.52.0.1", "libpiper_phonemize.so.1": "libpiper_phonemize.so.1.2.0", "libonnxruntime.so": "libonnxruntime.so.1.14.1",
	} {
		if got, err := os.Readlink(filepath.Join(ver, link)); err != nil || got != target {
			t.Errorf("%s -> %q, %v, want %q", link, got, err, target)
		}
	}
	if b, err := os.ReadFile(filepath.Join(ver, "espeak-ng-data", "voices", "en")); err != nil || string(b) != "voice" {
		t.Errorf("espeak-ng-data tree: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(ver, MarkerName)); err != nil || strings.TrimSpace(string(b)) != sum {
		t.Errorf("marker %q, %v", b, err)
	}
	if !e.inst.Installed() {
		t.Error("Installed() is false after an install")
	}
	// The smoke test ran in the temporary directory, with the libraries found there and nothing else
	// in the environment, before the directory was moved into place.
	calls := e.run.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d smoke runs, want 1", len(calls))
	}
	if want := filepath.Join(e.root, "2023.11.14-2.tmp"); calls[0].Env[0] != "LD_LIBRARY_PATH="+want || len(calls[0].Env) != 1 {
		t.Errorf("smoke env %q, want LD_LIBRARY_PATH=%s only", calls[0].Env, want)
	}
	e.assertClean("2023.11.14-2")
}

func TestInstallRefusesWhatItCannotTrust(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	other := strings.Repeat("0", 64)
	cases := []struct {
		name    string
		tarball string
		sha     string
		version string
		kind    error
	}{
		{"checksum mismatch", p, other, "2023.11.14-2", ErrChecksumMismatch},
		{"upper case pin is compared case-insensitively but must still match", p, strings.ToUpper(other), "2023.11.14-2", ErrChecksumMismatch},
		{"no pin", p, "", "2023.11.14-2", ErrBadInput},
		{"short pin", p, "abc", "2023.11.14-2", ErrBadInput},
		{"non-hex pin", p, strings.Repeat("g", 64), "2023.11.14-2", ErrBadInput},
		{"path in the version", p, sum, "../x", ErrBadInput},
		{"empty version", p, sum, "", ErrBadInput},
		{"version ending in .tmp", p, sum, "1.0.tmp", ErrBadInput},
		{"no tarball", "", sum, "1.0", ErrBadInput},
		{"missing tarball", filepath.Join(e.tarDir, "nope.tar.gz"), sum, "1.0", ErrIO},
		{"tarball for the other architecture", filepath.Join(e.tarDir, "piper_linux_aarch64.tar.gz"), sum, "1.0", ErrBadInput},
	}
	for _, c := range cases {
		err := e.inst.Install(context.Background(), c.tarball, c.sha, c.version)
		var ie *InstallError
		if !errors.Is(err, c.kind) || !errors.As(err, &ie) || ie.Code() != doctor.SVC020 {
			t.Errorf("%s: err = %v, want %v with LAI-SVC-020", c.name, err, c.kind)
		}
	}
	if e.exists("") && len(mustReadDir(t, e.root)) != 0 {
		t.Errorf("a refused install left %v in the root", mustReadDir(t, e.root))
	}
	if n := len(e.run.Calls()); n != 0 {
		t.Errorf("%d programs were run for refused installs", n)
	}
	// An unsupported architecture.
	e386 := NewInstaller(Options{Root: e.root, Arch: "386", Runner: e.run})
	if err := e386.Install(context.Background(), p, sum, "1.0"); !errors.Is(err, ErrBadInput) {
		t.Errorf("386: %v", err)
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestInstallRefusesUnsafeArchives(t *testing.T) {
	bigList := release("a")
	for i := 0; i < maxEntries; i++ {
		bigList = append(bigList, ent{name: fmt.Sprintf("piper/pkgconfig/f%d", i)})
	}
	cases := []struct {
		name string
		ents []ent
	}{
		{"absolute path", append(release("a"), ent{name: "/etc/cron.d/evil", body: "x"})},
		{"dot dot", append(release("a"), ent{name: "piper/../../evil", body: "x"})},
		{"dot dot at the start", append(release("a"), ent{name: "../evil", body: "x"})},
		{"backslash", append(release("a"), ent{name: `piper\..\evil`, body: "x"})},
		{"hard link", append(release("a"), ent{name: "piper/hard", typ: tar.TypeLink, link: "piper/piper"})},
		{"device", append(release("a"), ent{name: "piper/dev", typ: tar.TypeChar})},
		{"symlink to a path", append(release("a"), ent{name: "piper/escape", typ: tar.TypeSymlink, link: "../../etc"})},
		{"symlink to an absolute path", append(release("a"), ent{name: "piper/escape", typ: tar.TypeSymlink, link: "/etc/passwd"})},
		{"symlink to dot dot", append(release("a"), ent{name: "piper/up", typ: tar.TypeSymlink, link: ".."})},
		{"a write through a symlink of the archive", append(release("a"),
			ent{name: "piper/lnk", typ: tar.TypeSymlink, link: "pkgconfig"}, ent{name: "piper/lnk/evil", body: "x"})},
		{"a duplicate entry", append(release("a"), ent{name: "piper/piper", body: "second"})},
		{"the marker's own name", append(release("a"), ent{name: "piper/" + MarkerName, body: "x"})},
		{"too many entries", bigList},
	}
	for _, c := range cases {
		e := newInstallEnv(t, nil, nil)
		p, sum := e.tarball("piper_linux_x86_64.tar.gz", c.ents)
		err := e.inst.Install(context.Background(), p, sum, "1.0")
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%s: err = %v, want ErrUnsafeArchive", c.name, err)
		}
		if e.target() != "" || e.exists("1.0") {
			t.Errorf("%s: something was installed", c.name)
		}
		e.assertClean("1.0")
		if n := len(e.run.Calls()); n != 0 {
			t.Errorf("%s: the smoke test ran on an unsafe archive", c.name)
		}
	}
}

func TestInstallRefusesAnIncompleteRelease(t *testing.T) {
	cases := []struct {
		name string
		ents []ent
	}{
		{"no program", without(release("a"), "piper/piper")},
		{"no phoneme data", without(release("a"), "piper/espeak-ng-data")},
		{"no espeak-ng library", without(release("a"), "piper/libespeak-ng")},
		{"no phonemize library", without(release("a"), "piper/libpiper_phonemize")},
		{"no onnxruntime library", without(release("a"), "piper/libonnxruntime")},
		{"the program is a link", append(without(release("a"), "piper/piper"), ent{name: "piper/piper", typ: tar.TypeSymlink, link: "piper_phonemize"})},
	}
	for _, c := range cases {
		e := newInstallEnv(t, nil, nil)
		p, sum := e.tarball("piper_linux_x86_64.tar.gz", c.ents)
		err := e.inst.Install(context.Background(), p, sum, "1.0")
		if !errors.Is(err, ErrIncomplete) {
			t.Errorf("%s: err = %v, want ErrIncomplete", c.name, err)
		}
		if e.target() != "" {
			t.Errorf("%s: current was set", c.name)
		}
		e.assertClean("1.0")
	}
}

func TestSmokeFailureLeavesTheOldVersionCurrent(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p1, sum1 := e.tarball("one.tar.gz", release("one"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p1, sum1, "1.0"); err != nil {
		t.Fatal(err)
	}
	p2, sum2 := e.tarball("two.tar.gz", release("two"))

	cases := []struct {
		name string
		res  execx.Result
		err  error
	}{
		{"non-zero exit (a missing library)", execx.Result{ExitCode: 127, Stderr: []byte("error while loading shared libraries")}, nil},
		{"does not print the usage", execx.Result{Stdout: []byte("hello")}, nil},
		{"cannot be run", execx.Result{}, errors.New("exec format error")},
	}
	for _, c := range cases {
		e.run.Expect(fake.Key(filepath.Join(e.root, "2.0.tmp", BinPiper), "--help"), c.res, c.err)
		err := e.inst.Install(context.Background(), p2, sum2, "2.0")
		if !errors.Is(err, ErrSmokeTest) {
			t.Errorf("%s: err = %v, want ErrSmokeTest", c.name, err)
		}
		if e.target() != "1.0" || e.exists("2.0") {
			t.Errorf("%s: current -> %q, 2.0 exists %v: a binary that cannot run must never replace a working one", c.name, e.target(), e.exists("2.0"))
		}
		e.assertClean("2.0")
	}
}

func TestReinstallOfTheSameTarballIsReused(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(e.root, "1.0", "stray")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.helpOK("1.0") // the smoke test of a reused directory runs in place
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("the directory was extracted again; a matching one must be reused")
	}
	if e.target() != "1.0" {
		t.Errorf("current -> %q", e.target())
	}
	e.assertClean("1.0")
}

func TestDamagedVersionDirectoryIsReplaced(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	// The marker is there but the phoneme data was deleted: not a match.
	if err := os.RemoveAll(filepath.Join(e.root, "1.0", EspeakData)); err != nil {
		t.Fatal(err)
	}
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	if !e.exists("1.0/espeak-ng-data/en_dict") {
		t.Error("the damaged directory was not replaced")
	}
	e.assertClean("1.0")
}

func TestNewVersionKeepsTheOldDirectory(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p1, s1 := e.tarball("one.tar.gz", release("one"))
	p2, s2 := e.tarball("two.tar.gz", release("two"))
	e.helpOK("1.0.tmp")
	e.helpOK("2.0.tmp")
	for _, x := range []struct{ p, s, v string }{{p1, s1, "1.0"}, {p2, s2, "2.0"}} {
		if err := e.inst.Install(context.Background(), x.p, x.s, x.v); err != nil {
			t.Fatal(err)
		}
	}
	if e.target() != "2.0" || !e.exists("1.0/piper") {
		t.Errorf("current -> %q, the previous version directory must stay", e.target())
	}
	e.assertClean("2.0")
}

func TestLeftoverTmpDirOfAKilledRunIsRemoved(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(e.root, "1.0.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, "1.0.tmp", "debris"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	if e.exists("1.0/debris") {
		t.Error("debris of a killed run survived")
	}
	e.assertClean("1.0")
}

func TestCurrentThatIsNotASymlinkIsNeverReplaced(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(e.root, CurrentName), 0o755); err != nil {
		t.Fatal(err)
	}
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	err := e.inst.Install(context.Background(), p, sum, "1.0")
	if !errors.Is(err, ErrIO) || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("err = %v", err)
	}
	if fi, _ := os.Lstat(filepath.Join(e.root, CurrentName)); fi == nil || !fi.IsDir() {
		t.Error("current was replaced")
	}
}

func TestInstallStopsWithItsContext(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.inst.Install(ctx, p, sum, "1.0"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if e.exists("") && len(mustReadDir(t, e.root)) != 0 {
		t.Errorf("a cancelled install left %v", mustReadDir(t, e.root))
	}
}

// ---------------------------------------------------------------- disk faults

// faultFS fails the operations a test names: a full disk while writing a file, a refused rename.
type faultFS struct {
	FS
	failWriteAfter int64 // bytes written before a write fails; 0 = never
	renameTo       string
}

func (f faultFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	file, err := f.FS.OpenFile(name, flag, perm)
	if err != nil || f.failWriteAfter == 0 {
		return file, err
	}
	return &fullFile{File: file, left: f.failWriteAfter}, nil
}

func (f faultFS) Rename(oldpath, newpath string) error {
	if f.renameTo != "" && strings.HasSuffix(newpath, f.renameTo) {
		return errors.New("injected rename failure")
	}
	return f.FS.Rename(oldpath, newpath)
}

type fullFile struct {
	File
	left int64
}

func (f *fullFile) Write(p []byte) (int, error) {
	if int64(len(p)) > f.left {
		return 0, errors.New("no space left on device")
	}
	f.left -= int64(len(p))
	return f.File.Write(p)
}

func TestDiskFaultsLeaveNothingBehind(t *testing.T) {
	for _, c := range []struct {
		name string
		fs   faultFS
	}{
		{"disk full while extracting", faultFS{FS: OSFS{}, failWriteAfter: 20}},
		{"rename into place refused", faultFS{FS: OSFS{}, renameTo: "1.0"}},
		{"repoint refused", faultFS{FS: OSFS{}, renameTo: CurrentName}},
	} {
		e := newInstallEnv(t, c.fs, nil)
		p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
		e.helpOK("1.0.tmp")
		err := e.inst.Install(context.Background(), p, sum, "1.0")
		if !errors.Is(err, ErrIO) {
			t.Errorf("%s: err = %v, want ErrIO", c.name, err)
		}
		if e.target() != "" {
			t.Errorf("%s: current -> %q", c.name, e.target())
		}
		for _, n := range []string{"1.0.tmp", CurrentName + ".tmp"} {
			if e.exists(n) {
				t.Errorf("%s: %s was left behind", c.name, n)
			}
		}
	}
}

// ---------------------------------------------------------------- the smoke test with a voice

func TestSmokeTestWithAVoiceFollowsTheSpec(t *testing.T) {
	voice := func() (string, string, bool) { return "/ark/v/m.onnx", "/ark/v/m.onnx.json", true }
	for _, c := range []struct {
		name  string
		audio int
		exit  int
		ok    bool
	}{
		{"more than 10 000 bytes", 10001, 0, true},
		{"exactly 10 000 bytes is not enough", 10000, 0, false},
		{"almost no audio", 100, 0, false},
		{"non-zero exit", 20000, 1, false},
	} {
		e := newInstallEnv(t, nil, voice)
		p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
		tmp := filepath.Join(e.root, "1.0.tmp")
		e.run.Expect(fake.Key(filepath.Join(tmp, BinPiper), "--model", "/ark/v/m.onnx", "--config", "/ark/v/m.onnx.json",
			"--espeak_data", filepath.Join(tmp, EspeakData), "--output_raw"),
			execx.Result{Stdout: make([]byte, c.audio), ExitCode: c.exit}, nil)
		err := e.inst.Install(context.Background(), p, sum, "1.0")
		if c.ok != (err == nil) || (!c.ok && !errors.Is(err, ErrSmokeTest)) {
			t.Errorf("%s: err = %v", c.name, err)
		}
		calls := e.run.Calls()
		if len(calls) != 1 {
			t.Fatalf("%s: %d runs", c.name, len(calls))
		}
		b, _ := io.ReadAll(calls[0].Stdin)
		if string(b) != "test\n" {
			t.Errorf("%s: stdin %q, want test", c.name, b)
		}
		if c.ok && e.target() != "1.0" {
			t.Errorf("%s: not current", c.name)
		}
	}
	// A voice function that has no voice yet falls back to --help.
	e := newInstallEnv(t, nil, func() (string, string, bool) { return "", "", false })
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Errorf("no voice yet: %v", err)
	}
}

func TestCheckVerifiesTheCurrentInstall(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	if err := e.inst.Check(context.Background()); !errors.Is(err, ErrIncomplete) {
		t.Errorf("nothing installed: %v", err)
	}
	p, sum := e.tarball("piper_linux_x86_64.tar.gz", release("a"))
	e.helpOK("1.0.tmp")
	if err := e.inst.Install(context.Background(), p, sum, "1.0"); err != nil {
		t.Fatal(err)
	}
	e.helpOK("current")
	if err := e.inst.Check(context.Background()); err != nil {
		t.Errorf("installed: %v", err)
	}
	e.scriptHelp("current", execx.Result{ExitCode: 127})
	if err := e.inst.Check(context.Background()); !errors.Is(err, ErrSmokeTest) {
		t.Errorf("broken: %v", err)
	}
	if err := os.Remove(filepath.Join(e.root, "1.0", "libonnxruntime.so.1.14.1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.root, "1.0", "libonnxruntime.so")); err != nil {
		t.Fatal(err)
	}
	if err := e.inst.Check(context.Background()); !errors.Is(err, ErrIncomplete) {
		t.Errorf("library gone: %v", err)
	}
}

func TestNamesAndKitPaths(t *testing.T) {
	for goarch, want := range map[string]string{"amd64": "piper_linux_x86_64.tar.gz", "arm64": "piper_linux_aarch64.tar.gz"} {
		got, err := TarballName(goarch)
		if err != nil || got != want {
			t.Errorf("TarballName(%s) = %q, %v", goarch, got, err)
		}
		kp, err := KitPath("/media/ark", goarch)
		if err != nil || kp != "/media/ark/kit/tools/piper/"+want {
			t.Errorf("KitPath(%s) = %q, %v", goarch, kp, err)
		}
	}
	if _, err := TarballName("riscv64"); !errors.Is(err, ErrBadInput) {
		t.Errorf("riscv64: %v", err)
	}
	if _, err := KitPath("/x", "386"); !errors.Is(err, ErrBadInput) {
		t.Errorf("386: %v", err)
	}
}

func TestInstalledReflectsTheProgram(t *testing.T) {
	e := newInstallEnv(t, nil, nil)
	if e.inst.Installed() {
		t.Error("installed before anything")
	}
	if got := e.inst.BinaryPath(); got != filepath.Join(e.root, "current", "piper") {
		t.Errorf("BinaryPath = %q", got)
	}
}

// TestInstallTheRealReleaseLayout extracts the real rhasspy/piper tarball with a fake runner: it
// proves the extractor accepts the real archive (about 400 entries, five sibling symlinks, 0644
// libraries) and the layout check finds what piper needs, without running anything from it. It is
// skipped unless LOCALAI_PIPER_TARBALL names the file (the rebuild kit carries it; CI downloads
// release 2023.11.14-2 for it).
func TestInstallTheRealReleaseLayout(t *testing.T) {
	tarball := os.Getenv("LOCALAI_PIPER_TARBALL")
	if tarball == "" {
		t.Skip("LOCALAI_PIPER_TARBALL is not set")
	}
	data, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatal(err)
	}
	// Compare with the pin at run time: no digest is written in the source.
	sum := shaHex(data)
	root := filepath.Join(t.TempDir(), "piper")
	run := fake.New()
	run.Expect(fake.Key(filepath.Join(root, DefaultVersion+".tmp", BinPiper), "--help"), execx.Result{Stderr: []byte(usage)}, nil)
	inst := NewInstaller(Options{Root: root, Runner: run, Arch: "amd64", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := inst.Install(context.Background(), tarball, sum, DefaultVersion); err != nil {
		t.Fatal(err)
	}
	ver := filepath.Join(root, DefaultVersion)
	for _, name := range []string{"piper", "libespeak-ng.so.1", "libpiper_phonemize.so.1", "espeak-ng-data/en_dict"} {
		if _, err := os.Stat(filepath.Join(ver, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(ver, "piper")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("piper mode: %v %v", fi, err)
	}
}

// TestInstallAndRunTheRealRelease installs the real tarball and STARTS the real program, so it also
// needs LOCALAI_PIPER_RUN=1 (a person or CI saying "run this binary"). LOCALAI_PIPER_VOICE, a
// directory holding one voice's .onnx and .onnx.json, adds the smoke test with a voice and a
// synthesis through the exec contract.
func TestInstallAndRunTheRealRelease(t *testing.T) {
	tarball := os.Getenv("LOCALAI_PIPER_TARBALL")
	if tarball == "" || os.Getenv("LOCALAI_PIPER_RUN") != "1" {
		t.Skip("LOCALAI_PIPER_TARBALL and LOCALAI_PIPER_RUN=1 are not set")
	}
	data, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatal(err)
	}
	sum := shaHex(data)
	root := filepath.Join(t.TempDir(), "piper")
	var model, cfg string
	if dir := os.Getenv("LOCALAI_PIPER_VOICE"); dir != "" {
		ms, _ := filepath.Glob(filepath.Join(dir, "*.onnx"))
		if len(ms) != 1 {
			t.Fatalf("want one .onnx in %s, found %v", dir, ms)
		}
		model, cfg = ms[0], ms[0]+".json"
	}
	opts := Options{Root: root, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if model != "" {
		opts.Voice = func() (string, string, bool) { return model, cfg, true }
	}
	inst := NewInstaller(opts)
	if err := inst.Install(context.Background(), tarball, sum, DefaultVersion); err != nil {
		t.Fatal(err)
	}
	if err := inst.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if model == "" {
		return
	}
	// A real synthesis through the same Runtime the routes use, with the real streamer.
	v := Voice{CatalogVoice: CatalogVoice{ID: "piper-test", Language: "en"}, Model: model, Config: cfg}
	rt := Runtime{Root: root, WorkDir: t.TempDir()}
	pcm, rate, err := rt.Synthesize(context.Background(), v, "Press firmly on the wound with a clean cloth.", DefaultConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	secs, rms := PCMStats(pcm, rate)
	if secs < 0.5 || rms < -50 {
		t.Errorf("synthesis: %.2f s at %.1f dBFS", secs, rms)
	}
}
