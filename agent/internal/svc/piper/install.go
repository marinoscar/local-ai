package piper

// The installer of the voice program: the rhasspy/piper C++ release (piper, its three shared
// libraries and espeak-ng-data) that the rebuild kit carries, extracted offline under
// /opt/localai/piper/<version>/ with the "current" symlink repointed atomically (companion-services.md
// section 4.3.1). It follows the install pattern of the kiwix-tools installer (internal/svc/kiwix):
// the tarball is a local file whose sha256 is checked against the kit MANIFEST pin before a single
// byte is extracted, every archive entry is validated, the new directory is smoke tested before it
// replaces anything, and the disk (FS) and the process runner (execx.Runner) are interfaces with
// fakes. Nothing here touches the network.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// MarkerName is written last into a version directory: the sha256 of the tarball it was extracted
// from. Its presence with the pinned hash is what makes a directory "matching" on a reinstall.
const MarkerName = ".tarball.sha256"

// Limits of the extractor. The real release is 26 MB packed, 52 MB and about 400 entries unpacked;
// these only stop a hostile or corrupt archive from filling the disk or the inode table.
const (
	maxEntries   = 5000
	maxFileBytes = 256 << 20
	maxTotalSize = 512 << 20
)

// Limits of the smoke tests (execx requires both).
const (
	smokeTimeout    = 20 * time.Second
	smokeMaxOut     = 1 << 20
	smokeMinAudio   = 10000 // bytes of raw PCM "test" must produce (section 4.3.1)
	smokeUsageToken = "--output_raw"
)

// Failure kinds. Every error Install and Check return is an *InstallError whose Kind is one of
// these (errors.Is works), and every one carries the doctor code LAI-SVC-020: whatever went wrong,
// the voice program is not (properly) installed, and the fix is to run the installer's repair.
var (
	// ErrBadInput: version or sha256 is not usable, or the tarball is not for this machine.
	ErrBadInput = errors.New("invalid input")
	// ErrChecksumMismatch: the tarball does not hash to the pinned sha256.
	ErrChecksumMismatch = errors.New("checksum mismatch")
	// ErrUnsafeArchive: an entry would write outside the target, or is not a plain file, directory
	// or sibling symlink, or the archive is over a limit.
	ErrUnsafeArchive = errors.New("unsafe or malformed archive")
	// ErrIncomplete: the archive lacks the program, its libraries or the phoneme data.
	ErrIncomplete = errors.New("release incomplete")
	// ErrSmokeTest: the program does not run, or a test voice does not make audio.
	ErrSmokeTest = errors.New("smoke test failed")
	// ErrIO: the disk refused (full, read-only, ...) or the tarball could not be read.
	ErrIO = errors.New("file system error")
)

// InstallError is the error type of Install and Check.
type InstallError struct {
	Kind   error  // one of the Err* values above
	Detail string // plain-language detail, never a secret
	Err    error  // the underlying cause, if any
}

// Error implements error.
func (e *InstallError) Error() string {
	s := "piper: " + e.Kind.Error()
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap lets errors.Is match both the kind and the cause.
func (e *InstallError) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Err}
}

// Code returns the doctor code of the failure: LAI-SVC-020.
func (e *InstallError) Code() string { return doctor.SVC020 }

func fail(kind error, detail string, cause error) error {
	return &InstallError{Kind: kind, Detail: detail, Err: cause}
}

// Options configures NewInstaller. Every field is optional.
type Options struct {
	// Root is the directory holding the versions and "current"; default DefaultRoot.
	Root string
	// FS is the disk; default OSFS.
	FS FS
	// Runner runs the smoke tests; default execx.Exec{}.
	Runner execx.Runner
	// Arch is runtime.GOARCH of the machine ("amd64" or "arm64"); default runtime.GOARCH.
	Arch string
	// Logger receives the install steps; default slog.Default().
	Logger *slog.Logger
	// Voice, when set, names an installed voice (its .onnx and .onnx.json) for the smoke test of
	// section 4.3.1: `echo test | piper --model <voice> --output_raw | wc -c` must exceed 10 000.
	// Without a voice (a fresh node: the voices come with the ark) the smoke test only starts the
	// program with --help, which loads every shared library, and looks for its usage text.
	Voice func() (model, config string, ok bool)
}

// Installer installs and checks the voice program under one root. It is safe for concurrent use;
// installs are serialised.
type Installer struct {
	root   string
	fs     FS
	runner execx.Runner
	arch   string
	log    *slog.Logger
	voice  func() (string, string, bool)
	mu     sync.Mutex
}

// NewInstaller returns an Installer.
func NewInstaller(o Options) *Installer {
	i := &Installer{root: o.Root, fs: o.FS, runner: o.Runner, arch: o.Arch, log: o.Logger, voice: o.Voice}
	if i.root == "" {
		i.root = DefaultRoot
	}
	i.root = filepath.Clean(i.root)
	if i.fs == nil {
		i.fs = OSFS{}
	}
	if i.runner == nil {
		i.runner = execx.Exec{}
	}
	if i.arch == "" {
		i.arch = runtime.GOARCH
	}
	if i.log == nil {
		i.log = slog.Default()
	}
	i.log = i.log.With("component", "piper")
	return i
}

// Install installs piper with the tarball, the real disk and the real process runner. See
// Installer.Install. The aictl kit dispatcher (KIT-06) and the installer (INST-04) call it.
func Install(ctx context.Context, tarball, expectSHA256, version string) error {
	return NewInstaller(Options{}).Install(ctx, tarball, expectSHA256, version)
}

// Check runs the smoke test of Installer.Check on the real machine.
func Check(ctx context.Context) error { return NewInstaller(Options{}).Check(ctx) }

// ArchSuffix maps runtime.GOARCH to the architecture word of the release file names.
func ArchSuffix(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	}
	return "", fail(ErrBadInput, fmt.Sprintf("architecture %q has no piper release (amd64 and arm64 do)", goarch), nil)
}

// TarballName is the file name of the release asset for an architecture: piper_linux_x86_64.tar.gz
// or piper_linux_aarch64.tar.gz. The release number is not in the name; it is the version the
// caller passes to Install and the pin of the kit MANIFEST.
func TarballName(goarch string) (string, error) {
	a, err := ArchSuffix(goarch)
	if err != nil {
		return "", err
	}
	return "piper_linux_" + a + ".tar.gz", nil
}

// KitPath is where the rebuild kit keeps the tarball under a content root:
// <root>/kit/tools/piper/piper_linux_<arch>.tar.gz (rebuild-kit.md section 3.1, kit item kit-piper).
func KitPath(contentRoot, goarch string) (string, error) {
	n, err := TarballName(goarch)
	if err != nil {
		return "", err
	}
	return filepath.Join(contentRoot, "kit", "tools", "piper", n), nil
}

// BinaryPath is the program the agent runs: <root>/current/piper.
func (i *Installer) BinaryPath() string { return filepath.Join(i.root, CurrentName, BinPiper) }

// Installed reports whether <root>/current/piper exists as a file (LAI-SVC-020 when not).
func (i *Installer) Installed() bool {
	info, err := i.fs.Lstat(i.BinaryPath())
	return err == nil && !info.IsDir()
}

var (
	installVersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,63}$`)
	sha256Re         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// archFileRe picks the architecture out of a release file name, to refuse the wrong one.
	archFileRe = regexp.MustCompile(`^piper_linux_(x86_64|aarch64)\.tar\.gz$`)
)

func checkVersion(v string) error {
	if !installVersionRe.MatchString(v) || strings.HasSuffix(v, ".tmp") || strings.HasSuffix(v, ".bad") {
		return fail(ErrBadInput, fmt.Sprintf("version %q is not a release number such as %s", clip(v, 40), DefaultVersion), nil)
	}
	return nil
}

// Install installs the release tarball as <root>/<version>/ and makes it current, offline:
//
//  1. check the arguments, and that the tarball is for this machine's architecture;
//  2. verify the sha256 of the tarball against expectSHA256 (the kit MANIFEST pin). A mismatch
//     returns ErrChecksumMismatch and nothing on disk changes;
//  3. when <root>/<version>/ already holds this very tarball (its marker has the same hash and the
//     release is complete) it is reused; otherwise the archive is extracted, with every entry
//     checked (no absolute or ".." path, no link that could leave the target, no device, no
//     duplicate; limits on size and count), into <root>/<version>.tmp/, the program gets 0755, and
//     the marker is written;
//  4. a new directory is smoke tested (see Options.Voice) BEFORE it is moved to <root>/<version>/,
//     so a binary that cannot run never replaces a working one;
//  5. <root>/current is repointed atomically (a symlink created as current.tmp, then renamed over
//     current). The previous version directory is kept.
//
// On any failure the temporary directory is removed and current is untouched; a leftover
// <version>.tmp from a killed run is removed first. Installs through one Installer are serialised;
// run one install at a time per node.
func (i *Installer) Install(ctx context.Context, tarball, expectSHA256, version string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := checkVersion(version); err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(expectSHA256))
	if !sha256Re.MatchString(want) {
		return fail(ErrBadInput, "the pinned sha256 must be 64 hexadecimal characters; refusing to install an unverified tarball", nil)
	}
	if tarball == "" {
		return fail(ErrBadInput, "no tarball given", nil)
	}
	if err := i.checkTarballArch(tarball); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f, err := i.fs.Open(tarball)
	if err != nil {
		return fail(ErrIO, "cannot open the tarball "+tarball, err)
	}
	defer f.Close()

	// Step 2: verify before anything is extracted or created.
	got, err := hashReader(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail(ErrIO, "cannot read the tarball "+tarball, err)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		i.log.Error("piper tarball does not match its pinned checksum", "tarball", tarball, "version", version)
		return fail(ErrChecksumMismatch, "the tarball does not match the kit's pinned sha256; nothing was installed", nil)
	}

	verDir := filepath.Join(i.root, version)
	tmpDir := verDir + ".tmp"
	badDir := verDir + ".bad"

	// Step 3: reuse a matching directory, else extract.
	if i.matches(verDir, want) {
		if err := i.smoke(ctx, verDir); err != nil {
			return err
		}
		i.log.Info("piper version already installed; reusing", "version", version)
	} else {
		if err := i.fs.MkdirAll(i.root, 0o755); err != nil {
			return fail(ErrIO, "cannot create "+i.root, err)
		}
		// Debris of a killed run (or a previous failure we could not clean).
		if err := i.fs.RemoveAll(tmpDir); err != nil {
			return fail(ErrIO, "cannot remove the leftover "+tmpDir, err)
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = i.fs.RemoveAll(tmpDir)
			}
		}()
		if err := i.extract(ctx, f, tmpDir, want); err != nil {
			return err
		}
		if err := i.smoke(ctx, tmpDir); err != nil {
			return err
		}
		if err := i.swapIn(tmpDir, verDir, badDir); err != nil {
			return err
		}
		cleanup = false
		i.log.Info("piper extracted", "version", version, "dir", verDir)
	}

	// Step 5: repoint current.
	if err := i.repoint(version); err != nil {
		return err
	}
	i.log.Info("piper is current", "version", version)
	return nil
}

// checkTarballArch refuses a release file of the other architecture. A file name that does not
// follow the release naming (a renamed copy) is not judged here: the smoke test catches a binary
// that cannot run.
func (i *Installer) checkTarballArch(tarball string) error {
	want, err := ArchSuffix(i.arch)
	if err != nil {
		return err
	}
	if m := archFileRe.FindStringSubmatch(filepath.Base(tarball)); m != nil && m[1] != want {
		return fail(ErrBadInput, fmt.Sprintf("%s is for %s but this machine is %s", filepath.Base(tarball), m[1], want), nil)
	}
	return nil
}

// Check checks <root>/current: the release is complete and the smoke test passes. A missing or
// broken program is an error (LAI-SVC-020).
func (i *Installer) Check(ctx context.Context) error {
	cur := filepath.Join(i.root, CurrentName)
	if err := i.checkLayout(cur); err != nil {
		return err
	}
	return i.smoke(ctx, cur)
}

// smoke runs the program of dir. With an installed voice it is the test of the spec: "test" on
// stdin must make more than 10 000 bytes of raw audio. Without one the program is started with
// --help, which links every shared library and prints its usage, so a missing library or a binary
// of the wrong architecture fails here.
func (i *Installer) smoke(ctx context.Context, dir string) error {
	bin := filepath.Join(dir, BinPiper)
	env := []string{"LD_LIBRARY_PATH=" + dir}
	if i.voice != nil {
		if model, cfg, ok := i.voice(); ok {
			res, err := i.runner.Run(ctx, execx.Cmd{
				Path: bin, Timeout: smokeTimeout, MaxOut: smokeMaxOut, Env: env,
				Args:  []string{"--model", model, "--config", cfg, "--espeak_data", filepath.Join(dir, EspeakData), "--output_raw"},
				Stdin: strings.NewReader("test\n"),
			})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fail(ErrSmokeTest, "cannot run "+bin, err)
			}
			if res.ExitCode != 0 {
				return fail(ErrSmokeTest, fmt.Sprintf("%s exited with status %d", bin, res.ExitCode), nil)
			}
			if len(res.Stdout) <= smokeMinAudio {
				return fail(ErrSmokeTest, fmt.Sprintf("a test sentence made %d bytes of audio, expected more than %d", len(res.Stdout), smokeMinAudio), nil)
			}
			return nil
		}
	}
	res, err := i.runner.Run(ctx, execx.Cmd{Path: bin, Args: []string{"--help"}, Timeout: smokeTimeout, MaxOut: smokeMaxOut, Env: env})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail(ErrSmokeTest, "cannot run "+bin, err)
	}
	if res.ExitCode != 0 {
		return fail(ErrSmokeTest, fmt.Sprintf("%s --help exited with status %d", bin, res.ExitCode), nil)
	}
	if !strings.Contains(string(res.Stdout)+string(res.Stderr), smokeUsageToken) {
		return fail(ErrSmokeTest, bin+" --help did not print the usage text of piper", nil)
	}
	return nil
}

// hashReader returns the lowercase hex sha256 of everything r yields, and leaves a seekable file
// at its end (the caller seeks back).
func hashReader(ctx context.Context, r ReadFile) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: r}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader stops a long read when the context is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// matches reports whether verDir is a complete extraction of the tarball with this hash.
func (i *Installer) matches(verDir, sha string) bool {
	info, err := i.fs.Lstat(verDir)
	if err != nil || !info.IsDir() {
		return false
	}
	b, err := i.fs.ReadFile(filepath.Join(verDir, MarkerName))
	if err != nil || strings.TrimSpace(string(b)) != sha {
		return false
	}
	return i.checkLayout(verDir) == nil
}

// checkLayout verifies a version directory holds what piper needs: the program as an executable
// regular file, the phoneme data directory, and the three shared libraries (espeak-ng, piper
// phonemize, onnxruntime) under their sonames. The libraries are symlinks to the versioned file in
// the release; Lstat finds the name either way, and the smoke test proves they load.
func (i *Installer) checkLayout(dir string) error {
	fi, err := i.fs.Lstat(filepath.Join(dir, BinPiper))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fail(ErrIncomplete, BinPiper+" is not in the tarball (as an executable file)", err)
	}
	if di, err := i.fs.Lstat(filepath.Join(dir, EspeakData)); err != nil || !di.IsDir() {
		return fail(ErrIncomplete, EspeakData+" is not in the tarball (as a directory)", err)
	}
	for _, lib := range []string{"libespeak-ng.so.1", "libpiper_phonemize.so.1"} {
		if _, err := i.fs.Lstat(filepath.Join(dir, lib)); err != nil {
			return fail(ErrIncomplete, lib+" is not in the tarball", err)
		}
	}
	entries, err := i.fs.ReadDir(dir)
	if err != nil {
		return fail(ErrIO, "cannot list "+dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "libonnxruntime.so.") {
			return nil
		}
	}
	return fail(ErrIncomplete, "libonnxruntime.so.* is not in the tarball", nil)
}

// swapIn moves the finished tmpDir to verDir. A directory that is already there (a corrupt or
// different extraction of the same version) is moved aside first and removed after the swap; if the
// swap fails it is put back.
func (i *Installer) swapIn(tmpDir, verDir, badDir string) error {
	moved := false
	if _, err := i.fs.Lstat(verDir); err == nil {
		if err := i.fs.RemoveAll(badDir); err != nil {
			return fail(ErrIO, "cannot remove the leftover "+badDir, err)
		}
		if err := i.fs.Rename(verDir, badDir); err != nil {
			return fail(ErrIO, "cannot move the old "+verDir+" aside", err)
		}
		moved = true
	}
	if err := i.fs.Rename(tmpDir, verDir); err != nil {
		if moved {
			_ = i.fs.Rename(badDir, verDir)
		}
		return fail(ErrIO, "cannot move "+tmpDir+" into place", err)
	}
	i.syncDir(i.root)
	if moved {
		_ = i.fs.RemoveAll(badDir)
	}
	return nil
}

// repoint makes current -> version, atomically: current.tmp is created as a symlink and renamed over
// current (ln -sfn <ver> current.tmp && mv -T current.tmp current). A "current" that is not a
// symlink is never replaced.
func (i *Installer) repoint(version string) error {
	cur := filepath.Join(i.root, CurrentName)
	tmp := cur + ".tmp"
	if info, err := i.fs.Lstat(cur); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fail(ErrIO, cur+" exists and is not a symlink; refusing to replace it", nil)
		}
		if t, err := i.fs.Readlink(cur); err == nil && t == version {
			return nil // already there
		}
	}
	if err := i.fs.RemoveAll(tmp); err != nil {
		return fail(ErrIO, "cannot remove the leftover "+tmp, err)
	}
	if err := i.fs.Symlink(version, tmp); err != nil {
		return fail(ErrIO, "cannot create "+tmp, err)
	}
	if err := i.fs.Rename(tmp, cur); err != nil {
		_ = i.fs.RemoveAll(tmp)
		return fail(ErrIO, "cannot repoint "+cur, err)
	}
	i.syncDir(i.root)
	return nil
}

// syncDir makes a rename durable; a failure is logged, not fatal (the rename already happened).
func (i *Installer) syncDir(dir string) {
	if err := i.fs.SyncDir(dir); err != nil {
		i.log.Warn("could not sync a directory", "dir", dir, "error", err)
	}
}

// ---------------------------------------------------------------- extraction

// entryName cleans a tar entry name into its components. It refuses what could leave the target:
// an absolute name, a ".." component, a NUL or a backslash. "." and empty components are dropped.
func entryName(name string) ([]string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return nil, fmt.Errorf("entry name %q is not allowed", clip(name, 60))
	}
	if strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("entry %q is an absolute path", clip(name, 60))
	}
	var comps []string
	for _, c := range strings.Split(name, "/") {
		switch c {
		case "", ".":
		case "..":
			return nil, fmt.Errorf("entry %q has a .. component", clip(name, 60))
		default:
			comps = append(comps, c)
		}
	}
	return comps, nil
}

// linkTarget allows a symlink only when it names a sibling in its own directory (libx.so ->
// libx.so.1). A target with a slash, or "." or "..", could be combined with other links to leave the
// target directory, so it is refused outright.
func linkTarget(t string) error {
	if t == "" || t == "." || t == ".." || strings.ContainsAny(t, "/\\\x00") {
		return fmt.Errorf("symlink target %q is not a plain sibling name", clip(t, 60))
	}
	return nil
}

// tarEntry is one validated archive entry.
type tarEntry struct {
	hdr   *tar.Header
	comps []string // cleaned, before the common top directory is stripped
}

// walk reads the gzip+tar stream and calls fn for every real entry (pax global headers are skipped)
// after validating its type, name, link target and sizes. body is only valid during fn.
func walk(r io.Reader, fn func(e tarEntry, body io.Reader) error) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fail(ErrUnsafeArchive, "the file is not a gzip archive", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var n int
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fail(ErrUnsafeArchive, "the archive is damaged", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if n++; n > maxEntries {
			return fail(ErrUnsafeArchive, fmt.Sprintf("more than %d entries", maxEntries), nil)
		}
		comps, err := entryName(hdr.Name)
		if err != nil {
			return fail(ErrUnsafeArchive, err.Error(), nil)
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxFileBytes {
				return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q is too large", clip(hdr.Name, 60)), nil)
			}
			if total += hdr.Size; total > maxTotalSize {
				return fail(ErrUnsafeArchive, "the archive expands to more than the allowed size", nil)
			}
			if len(comps) == 0 {
				return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q has no name", clip(hdr.Name, 60)), nil)
			}
		case tar.TypeDir:
		case tar.TypeSymlink:
			if len(comps) == 0 {
				return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q has no name", clip(hdr.Name, 60)), nil)
			}
			if err := linkTarget(hdr.Linkname); err != nil {
				return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q: %v", clip(hdr.Name, 60), err), nil)
			}
		default:
			return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q is a hard link, device or other special file", clip(hdr.Name, 60)), nil)
		}
		if err := fn(tarEntry{hdr: hdr, comps: comps}, tr); err != nil {
			return err
		}
	}
}

// stripPrefix is the common top directory to drop: every entry is under one directory (the release
// unpacks into piper/). An archive that has its programs at the top level
// has no prefix to strip.
func stripPrefix(entries [][]string, topIsDir map[string]bool) int {
	if len(entries) == 0 {
		return 0
	}
	first := entries[0][0]
	for _, c := range entries {
		if c[0] != first {
			return 0
		}
		if len(c) == 1 && !topIsDir[first] {
			return 0
		}
	}
	return 1
}

// extract unpacks f into tmpDir (created here, mode 0755) and verifies its content. The archive is
// read twice: once to validate every entry and find the common top directory, then to write; the
// second read re-hashes the stream so a file that changed in between is caught.
func (i *Installer) extract(ctx context.Context, f ReadFile, tmpDir, wantSHA string) error {
	// Pass 1: validate and plan.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(ErrIO, "cannot re-read the tarball", err)
	}
	var all [][]string
	topIsDir := map[string]bool{}
	err := walk(&ctxReader{ctx: ctx, r: f}, func(e tarEntry, body io.Reader) error {
		if len(e.comps) == 0 {
			return nil // "./"
		}
		all = append(all, e.comps)
		if len(e.comps) == 1 && e.hdr.Typeflag == tar.TypeDir {
			topIsDir[e.comps[0]] = true
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	strip := stripPrefix(all, topIsDir)

	// Pass 2: write.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(ErrIO, "cannot re-read the tarball", err)
	}
	if err := i.fs.MkdirAll(tmpDir, 0o755); err != nil {
		return fail(ErrIO, "cannot create "+tmpDir, err)
	}
	h := sha256.New()
	tee := io.TeeReader(&ctxReader{ctx: ctx, r: f}, h)
	links := map[string]bool{} // relative paths of the symlinks created
	err = walk(tee, func(e tarEntry, body io.Reader) error {
		if len(e.comps) <= strip {
			return nil // the top directory itself, or "./"
		}
		return i.writeEntry(tmpDir, e, e.comps[strip:], body, links)
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	// The gzip reader may stop before the end of the file (padding); the hash must cover it all.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return fail(ErrIO, "cannot read the tarball", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return fail(ErrChecksumMismatch, "the tarball changed while it was being installed; nothing was installed", nil)
	}

	if err := i.checkLayout(tmpDir); err != nil {
		return err
	}
	// The marker goes in last: a directory without it is never reused.
	if err := i.writeFile(filepath.Join(tmpDir, MarkerName), strings.NewReader(wantSHA+"\n"), 0o644); err != nil {
		return err
	}
	i.syncDir(tmpDir)
	return nil
}

// writeEntry creates one entry under tmpDir. rel is its path inside the version directory.
func (i *Installer) writeEntry(tmpDir string, e tarEntry, rel []string, body io.Reader, links map[string]bool) error {
	// Never write through a symlink created by this archive.
	for n := 1; n < len(rel); n++ {
		if links[path.Join(rel[:n]...)] {
			return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q goes through a symlink", clip(e.hdr.Name, 60)), nil)
		}
	}
	target := filepath.Join(append([]string{tmpDir}, rel...)...)
	switch e.hdr.Typeflag {
	case tar.TypeDir:
		if err := i.fs.MkdirAll(target, 0o755); err != nil {
			return fail(ErrIO, "cannot create "+target, err)
		}
	case tar.TypeSymlink:
		if err := i.fs.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fail(ErrIO, "cannot create "+filepath.Dir(target), err)
		}
		if err := i.fs.Symlink(e.hdr.Linkname, target); err != nil {
			return fail(ErrUnsafeArchive, fmt.Sprintf("cannot create the symlink %q (duplicate entry?)", clip(e.hdr.Name, 60)), err)
		}
		links[path.Join(rel...)] = true
	case tar.TypeReg:
		if len(rel) == 1 && rel[0] == MarkerName {
			return fail(ErrUnsafeArchive, "the archive carries the installer's marker file name", nil)
		}
		if err := i.fs.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fail(ErrIO, "cannot create "+filepath.Dir(target), err)
		}
		mode := os.FileMode(0o644)
		if isBinary(rel) || e.hdr.Mode&0o111 != 0 {
			mode = 0o755
		}
		if err := i.writeFile(target, body, mode); err != nil {
			var ie *InstallError
			if errors.As(err, &ie) && ie.Kind == ErrIO && errors.Is(ie.Err, os.ErrExist) {
				return fail(ErrUnsafeArchive, fmt.Sprintf("entry %q appears twice", clip(e.hdr.Name, 60)), nil)
			}
			return err
		}
	}
	return nil
}

// isBinary reports whether rel names the program at the top of the version directory.
func isBinary(rel []string) bool { return len(rel) == 1 && rel[0] == BinPiper }

// writeFile creates name exclusively, copies r into it, sets the mode (so the umask never matters)
// and syncs it. A write error (a full disk) fails the install.
func (i *Installer) writeFile(name string, r io.Reader, mode os.FileMode) error {
	f, err := i.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(ErrIO, "cannot create "+name, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fail(ErrIO, "cannot write "+name, err)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fail(ErrIO, "cannot set the mode of "+name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fail(ErrIO, "cannot sync "+name, err)
	}
	if err := f.Close(); err != nil {
		return fail(ErrIO, "cannot close "+name, err)
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
