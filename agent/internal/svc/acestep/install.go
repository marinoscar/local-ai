package acestep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// The offline build of the music runtime (music.md section 3.1, rebuild-kit.md section 4.5 row
// engine.acestep). From the kit item kit-engine-acestep-<backend>, synced file by file into
// kit/engines/acestep/<backend>/<version>/, it builds /opt/localai/acestep/<version>/venv with
// `pip install --no-index --require-hashes`, writes BUILD.json, runs the shim's self-test and only then
// moves the current link. Nothing is downloaded; a failure at any step leaves current as it was and
// removes what it built.

// Kit layout of one runtime version (written by ark sync from catalog/ark.yaml, the list made by
// scripts/release/build-acestep-kit.sh): requirements.lock with a hash per wheel, wheels/ with every
// wheel (the shim's own included) and, optionally, python/ with one python-build-standalone
// "install_only" tarball: the runtime's own CPython, for nodes whose python3 is a version ACE-Step does
// not support (#511).
const (
	KitWheels = "wheels"
	KitLock   = "requirements.lock"
	KitPython = "python"
)

// Timeouts of the build.
const (
	unpackTimeout   = 10 * time.Minute
	venvTimeout     = 5 * time.Minute
	pipTimeout      = 60 * time.Minute
	selfTestTimeout = 5 * time.Minute
	maxProgOut      = 1 << 20
)

// KitFile is one file of a runtime version in the kit and its sha256 from the kit's record.
type KitFile struct {
	Path, SHA256 string
}

// Installer builds the runtime. The programs (tar, python3) are found with LookPath and run
// through Runner; tests give fakes.
type Installer struct {
	// Root is /opt/localai/acestep; tests use a temporary directory.
	Root     string
	Runner   execx.Runner
	LookPath func(name string) (string, error)
	Now      func() time.Time
}

// InstallOutcome is what one Install did.
type InstallOutcome struct {
	Version, Previous string
	// Unchanged is true when current already was this version for this backend.
	Unchanged bool
}

// ErrKitDamaged is a runtime version in the kit that is incomplete or has a file whose sha256 is not
// the kit's record: nothing is installed.
var ErrKitDamaged = errors.New("acestep: the music runtime in the kit is incomplete or does not match its checksums")

func (in Installer) root() string {
	if in.Root == "" {
		return RuntimeRoot
	}
	return in.Root
}

// Current is the version current points to, "" when none.
func (in Installer) Current() (Build, bool) {
	b, err := ReadBuild(filepath.Join(in.root(), "current"))
	return b, err == nil
}

func (in Installer) run(ctx context.Context, timeout time.Duration, prog string, args ...string) error {
	path := prog
	if !filepath.IsAbs(prog) {
		p, err := in.LookPath(prog)
		if err != nil {
			return fmt.Errorf("acestep: %s is not installed: %w", prog, err)
		}
		path = p
	}
	res, err := in.Runner.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: timeout, MaxOut: maxProgOut,
		Env: []string{"PATH=" + execx.FixedPath, "PIP_NO_INDEX=1", "PIP_DISABLE_PIP_VERSION_CHECK=1", "HF_HUB_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1"}})
	if err != nil {
		return fmt.Errorf("acestep: %s: %w", filepath.Base(path), err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("acestep: %s exited with %d: %s", filepath.Base(path), res.ExitCode, clip(msg, 200))
	}
	return nil
}

// fileSHA256 is the hex sha256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Install builds the runtime of version for backend from the kit's version directory dir, whose files
// must each match their sha256 in files (every file of the version, as the kit's record lists them).
// It is idempotent (the same version and backend in current is Unchanged), cleans up what an
// interrupted run left, and never changes current unless every step succeeded.
func (in Installer) Install(ctx context.Context, dir string, files []KitFile, version, backend string) (InstallOutcome, error) {
	out := InstallOutcome{Version: version}
	if !VersionRe.MatchString(version) || (backend != "cuda" && backend != "vulkan" && backend != "cpu") {
		return out, fmt.Errorf("acestep: version %q or backend %q is not usable", version, backend)
	}
	if cur, ok := in.Current(); ok {
		out.Previous = cur.Version
		if cur.Version == version && cur.Backend == backend {
			out.Unchanged = true
			return out, nil
		}
	}
	// Verify every file right before it is used: a flipped byte installs nothing.
	lock, tarball := filepath.Join(dir, KitLock), ""
	hasLock := false
	for _, f := range files {
		got, err := fileSHA256(f.Path)
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrKitDamaged, err)
		}
		if !strings.EqualFold(got, f.SHA256) {
			return out, ErrKitDamaged
		}
		switch rel, _ := filepath.Rel(dir, f.Path); {
		case rel == KitLock:
			hasLock = true
		case filepath.Dir(rel) == KitPython && strings.HasSuffix(rel, ".tar.gz"):
			if tarball != "" {
				return out, fmt.Errorf("%w: more than one python in %s", ErrKitDamaged, KitPython)
			}
			tarball = f.Path
		}
	}
	if !hasLock {
		return out, fmt.Errorf("%w: no %s", ErrKitDamaged, KitLock)
	}

	root := in.root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return out, err
	}
	final := filepath.Join(root, version)
	work := filepath.Join(root, "."+version+".partial")
	// A leftover of an interrupted run is never trusted.
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return out, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(work)
		}
	}()
	venv := filepath.Join(work, "venv")
	python, py := "python3", filepath.Join(venv, "bin", "python")
	if tarball != "" {
		// The kit's own interpreter is the runtime. python-build-standalone is relocatable, so it is
		// unpacked as venv/ and the wheels go into it (a venv made from it would name the scratch path).
		unpacked := filepath.Join(work, "unpacked")
		if err := os.MkdirAll(unpacked, 0o755); err != nil {
			return out, err
		}
		if err := in.run(ctx, unpackTimeout, "tar", "-xzf", tarball, "-C", unpacked, "--no-same-owner"); err != nil {
			return out, err
		}
		if err := os.Rename(filepath.Join(unpacked, "python"), venv); err != nil {
			return out, fmt.Errorf("%w: the python tarball has no python/ folder", ErrKitDamaged)
		}
		_ = os.RemoveAll(unpacked)
		// The unit and the runtime check run venv/bin/python.
		if _, err := os.Lstat(filepath.Join(venv, "bin", "python")); err != nil {
			if err := os.Symlink("python3", filepath.Join(venv, "bin", "python")); err != nil {
				return out, err
			}
		}
		python, py = "bundled "+filepath.Base(tarball), filepath.Join(venv, "bin", "python3")
	} else if err := in.run(ctx, venvTimeout, "python3", "-m", "venv", venv); err != nil {
		return out, err
	}
	if err := in.run(ctx, pipTimeout, py, "-m", "pip", "install", "--no-index", "--find-links", filepath.Join(dir, KitWheels),
		"--require-hashes", "-r", lock); err != nil {
		return out, err
	}
	if err := in.run(ctx, selfTestTimeout, py, "-m", ShimModule, "--self-test"); err != nil {
		return out, err
	}
	lockSum, _ := fileSHA256(lock)
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	b, _ := json.MarshalIndent(Build{Version: version, Backend: backend, Python: python, LockSHA256: lockSum,
		BuiltAt: now().UTC().Format(time.RFC3339)}, "", "  ")
	if err := os.WriteFile(filepath.Join(work, BuildFile), append(b, '\n'), 0o644); err != nil {
		return out, err
	}
	// The version directory replaces any older build of the same version, then current moves.
	_ = os.RemoveAll(final)
	if err := os.Rename(work, final); err != nil {
		return out, err
	}
	ok = true
	tmpLink := filepath.Join(root, ".current.new")
	_ = os.Remove(tmpLink)
	if err := os.Symlink(version, tmpLink); err != nil {
		return out, err
	}
	if err := os.Rename(tmpLink, filepath.Join(root, "current")); err != nil {
		return out, err
	}
	return out, nil
}

// KitDir is the directory of backend's runtime versions in a kit: kit/engines/acestep/<backend>, the
// item directory of kit-engine-acestep-<backend> (it holds the item's MANIFEST.json).
func KitDir(kitDir, backend string) string {
	return filepath.Join(kitDir, "engines", "acestep", backend)
}

// FindVersion looks for the newest runtime version for backend in the kit (a directory
// kit/engines/acestep/<backend>/<version>/ with a requirements.lock) and returns its directory and
// version ("" when the kit has none).
func FindVersion(kitDir, backend string) (dir, version string) {
	base := KitDir(kitDir, backend)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		v := e.Name()
		if !e.IsDir() || !VersionRe.MatchString(v) {
			continue
		}
		d := filepath.Join(base, v)
		if fi, err := os.Stat(filepath.Join(d, KitLock)); err == nil && fi.Mode().IsRegular() && versionLess(version, v) {
			dir, version = d, v
		}
	}
	return dir, version
}

// versionLess orders versions by their dotted numbers, then as text.
func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if len(pa[i]) != len(pb[i]) {
			return len(pa[i]) < len(pb[i])
		}
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb) || bytes.Compare([]byte(a), []byte(b)) < 0 && len(pa) == len(pb)
}
