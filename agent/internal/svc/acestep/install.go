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
// engine.acestep). From the kit's wheel bundle
// kit/engines/acestep/<version>/acestep-<version>-<backend>.tar.zst it builds
// /opt/localai/acestep/<version>/venv with `pip install --no-index --require-hashes`, writes BUILD.json,
// runs the shim's self-test and only then moves the current link. Nothing is downloaded; a failure at
// any step leaves current as it was and removes what it built.

// Bundle layout (built by scripts/release/build-acestep-bundle.sh): wheels/ with every wheel (the shim's
// own included) and requirements.lock with a hash per wheel.
const (
	BundleWheels = "wheels"
	BundleLock   = "requirements.lock"
)

// Timeouts of the build.
const (
	unpackTimeout   = 30 * time.Minute
	venvTimeout     = 5 * time.Minute
	pipTimeout      = 60 * time.Minute
	selfTestTimeout = 5 * time.Minute
	maxProgOut      = 1 << 20
)

// BundleName is the file name of a bundle in the kit.
func BundleName(version, backend string) string {
	return "acestep-" + version + "-" + backend + ".tar.zst"
}

// Installer builds the runtime. The programs (tar with zstd, python3) are found with LookPath and run
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

// ErrBundleDamaged is a bundle whose sha256 is not the kit's record: nothing is installed.
var ErrBundleDamaged = errors.New("acestep: the wheel bundle does not match the kit's checksum")

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

// Install builds the runtime of version for backend from bundle, whose sha256 must be want. It is
// idempotent (the same version and backend in current is Unchanged), cleans up what an interrupted run
// left, and never changes current unless every step succeeded.
func (in Installer) Install(ctx context.Context, bundle, want, version, backend string) (InstallOutcome, error) {
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
	// Verify the bundle right before it is used: a flipped byte installs nothing.
	got, err := fileSHA256(bundle)
	if err != nil {
		return out, fmt.Errorf("acestep: read the bundle: %w", err)
	}
	if !strings.EqualFold(got, want) {
		return out, ErrBundleDamaged
	}

	root := in.root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return out, err
	}
	final := filepath.Join(root, version)
	work := filepath.Join(root, "."+version+".partial")
	// A leftover of an interrupted run is never trusted.
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(filepath.Join(work, "unpacked"), 0o755); err != nil {
		return out, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(work)
		}
	}()
	unpacked := filepath.Join(work, "unpacked")
	if err := in.run(ctx, unpackTimeout, "tar", "--zstd", "-xf", bundle, "-C", unpacked, "--no-same-owner"); err != nil {
		return out, err
	}
	for _, need := range []string{BundleWheels, BundleLock} {
		if _, err := os.Stat(filepath.Join(unpacked, need)); err != nil {
			return out, fmt.Errorf("acestep: the bundle has no %s", need)
		}
	}
	venv := filepath.Join(work, "venv")
	if err := in.run(ctx, venvTimeout, "python3", "-m", "venv", venv); err != nil {
		return out, err
	}
	py := filepath.Join(venv, "bin", "python")
	if err := in.run(ctx, pipTimeout, py, "-m", "pip", "install", "--no-index", "--find-links", filepath.Join(unpacked, BundleWheels),
		"--require-hashes", "-r", filepath.Join(unpacked, BundleLock)); err != nil {
		return out, err
	}
	if err := in.run(ctx, selfTestTimeout, py, "-m", ShimModule, "--self-test"); err != nil {
		return out, err
	}
	lockSum, _ := fileSHA256(filepath.Join(unpacked, BundleLock))
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	b, _ := json.MarshalIndent(Build{Version: version, Backend: backend, Python: "python3", LockSHA256: lockSum,
		BuiltAt: now().UTC().Format(time.RFC3339)}, "", "  ")
	if err := os.WriteFile(filepath.Join(work, BuildFile), append(b, '\n'), 0o644); err != nil {
		return out, err
	}
	_ = os.RemoveAll(unpacked)
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

// FindBundle looks for the newest bundle for backend under kitDir/engines/acestep/<version>/ and
// returns its path and version ("" when the kit has none).
func FindBundle(kitDir, backend string) (path, version string) {
	dir := filepath.Join(kitDir, "engines", "acestep")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		v := e.Name()
		if !e.IsDir() || !VersionRe.MatchString(v) {
			continue
		}
		p := filepath.Join(dir, v, BundleName(v, backend))
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && versionLess(version, v) {
			path, version = p, v
		}
	}
	return path, version
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
