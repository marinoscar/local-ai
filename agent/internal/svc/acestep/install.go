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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// The offline build of the music runtime (music.md section 3.1, rebuild-kit.md section 4.5 row engine.acestep).
// From the kit's wheelhouse kit/engines/acestep/<version>/ (filled by ark sync, rebuild-kit.md section 3.2.2) it
// builds /opt/localai/acestep/<version>/ in place:
//
//	python/      the pinned standalone Python, unpacked from the wheelhouse's python/<tarball>
//	venv/        `python/bin/python3 -m venv`, then `pip install --no-index --find-links <wheels>
//	             --require-hashes -r requirements.lock` (the shim's wheel is in the wheelhouse and the lock)
//	BUILD.json   the version, the backend, the Python and the lock's sha256
//
// then runs the shim's self-test and only then moves the current link. Nothing is downloaded. The venv is built
// at its final path because a venv cannot be moved; a marker file (.incomplete) says the folder is not finished
// until the last step, so nothing half-built is ever current. A failure removes what it built and leaves current
// as it was.

// Wheelhouse layout inside a version folder of the kit (written by the pip_wheelhouse producer of ark sync).
const (
	WheelhouseWheels = "wheels"
	WheelhouseLock   = "requirements.lock"
	WheelhousePython = "python"
	// incompleteMarker is in a version folder of the runtime while it is being built.
	incompleteMarker = ".incomplete"
)

// Timeouts of the build.
const (
	unpackTimeout   = 15 * time.Minute
	venvTimeout     = 5 * time.Minute
	pipTimeout      = 60 * time.Minute
	selfTestTimeout = 5 * time.Minute
	maxProgOut      = 1 << 20
)

// Wheelhouse is one version folder of the kit's wheelhouse and the sha256 the kit records for each of its files.
type Wheelhouse struct {
	// Dir is the absolute path of kit/engines/acestep/<version>.
	Dir string
	// Sums maps every file below Dir (slash paths relative to Dir) to its recorded sha256.
	Sums map[string]string
}

// Installer builds the runtime. The programs (tar, the pinned Python, pip) run through Runner; tar is found with
// LookPath. Tests give fakes.
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

// ErrWheelhouseDamaged is a wheelhouse whose files are not what the kit records: nothing is installed.
var ErrWheelhouseDamaged = errors.New("acestep: the kit's wheelhouse does not match its checksums")

func (in Installer) root() string {
	if in.Root == "" {
		return RuntimeRoot
	}
	return in.Root
}

// Current is the build current points to, when it is a finished one.
func (in Installer) Current() (Build, bool) {
	cur := filepath.Join(in.root(), "current")
	if _, err := os.Lstat(filepath.Join(cur, incompleteMarker)); err == nil {
		return Build{}, false
	}
	b, err := ReadBuild(cur)
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
		Env: []string{"PATH=" + execx.FixedPath, "PIP_NO_INDEX=1", "PIP_NO_CACHE_DIR=1", "PIP_DISABLE_PIP_VERSION_CHECK=1",
			"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1"}})
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

// verify re-hashes every file of the wheelhouse right before it is used: every recorded file must be there with
// its digest, nothing unrecorded may be there (pip would see it), and the lock, the wheels and exactly one Python
// tarball must be present. It returns the Python tarball's path.
func (w Wheelhouse) verify(ctx context.Context) (string, error) {
	if w.Dir == "" || len(w.Sums) == 0 {
		return "", ErrWheelhouseDamaged
	}
	seen := map[string]bool{}
	var pythons []string
	err := filepath.WalkDir(w.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(w.Dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		want, ok := w.Sums[rel]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not recorded", ErrWheelhouseDamaged, rel)
		}
		got, err := fileSHA256(p)
		if err != nil || !strings.EqualFold(got, want) {
			return fmt.Errorf("%w: %s", ErrWheelhouseDamaged, rel)
		}
		seen[rel] = true
		if path.Dir(rel) == WheelhousePython && strings.HasSuffix(rel, ".tar.gz") {
			pythons = append(pythons, p)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWheelhouseDamaged) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrWheelhouseDamaged, err)
	}
	for rel := range w.Sums {
		if !seen[rel] {
			return "", fmt.Errorf("%w: %s is missing", ErrWheelhouseDamaged, rel)
		}
	}
	if !seen[WheelhouseLock] || len(pythons) != 1 {
		return "", fmt.Errorf("%w: no lock or not exactly one Python", ErrWheelhouseDamaged)
	}
	if fi, err := os.Stat(filepath.Join(w.Dir, WheelhouseWheels)); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: no wheels", ErrWheelhouseDamaged)
	}
	return pythons[0], nil
}

// Install builds the runtime of version for backend from the wheelhouse. It is idempotent (the same version and
// backend in current is Unchanged), cleans up what an interrupted run left, and never changes current unless
// every step succeeded.
func (in Installer) Install(ctx context.Context, wh Wheelhouse, version, backend string) (InstallOutcome, error) {
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
	// Verify the wheelhouse right before it is used: a flipped byte installs nothing.
	pyTar, err := wh.verify(ctx)
	if err != nil {
		return out, err
	}

	root := in.root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return out, err
	}
	final := filepath.Join(root, version)
	// The version in use is being rebuilt (it is damaged, or not finished): keep it aside until the new one works.
	aside := ""
	if target, err := os.Readlink(filepath.Join(root, "current")); err == nil && filepath.Base(target) == version {
		aside = filepath.Join(root, "."+version+".previous")
		_ = os.RemoveAll(aside)
		if err := os.Rename(final, aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return out, err
		}
	}
	// Anything else at the final path is a leftover of an interrupted run and is never trusted.
	_ = os.RemoveAll(final)
	if err := os.MkdirAll(final, 0o755); err != nil {
		return out, err
	}
	if err := os.WriteFile(filepath.Join(final, incompleteMarker), nil, 0o644); err != nil {
		return out, err
	}
	ok := false
	defer func() {
		if ok {
			return
		}
		_ = os.RemoveAll(final)
		if aside != "" {
			_ = os.Rename(aside, final)
		}
	}()

	if err := in.run(ctx, unpackTimeout, "tar", "-xzf", pyTar, "-C", final, "--no-same-owner"); err != nil {
		return out, err
	}
	py := filepath.Join(final, WheelhousePython, "bin", "python3")
	if fi, err := os.Stat(py); err != nil || fi.IsDir() {
		return out, fmt.Errorf("acestep: the Python tarball has no python/bin/python3")
	}
	venv := filepath.Join(final, "venv")
	if err := in.run(ctx, venvTimeout, py, "-m", "venv", venv); err != nil {
		return out, err
	}
	vpy := filepath.Join(venv, "bin", "python")
	lock := filepath.Join(wh.Dir, WheelhouseLock)
	if err := in.run(ctx, pipTimeout, vpy, "-m", "pip", "install", "--no-index", "--find-links", filepath.Join(wh.Dir, WheelhouseWheels),
		"--require-hashes", "-r", lock); err != nil {
		return out, err
	}
	if err := in.run(ctx, selfTestTimeout, vpy, "-m", ShimModule, "--self-test"); err != nil {
		return out, err
	}
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	b, _ := json.MarshalIndent(Build{Version: version, Backend: backend, Python: filepath.Base(pyTar), LockSHA256: wh.Sums[WheelhouseLock],
		BuiltAt: now().UTC().Format(time.RFC3339)}, "", "  ")
	if err := os.WriteFile(filepath.Join(final, BuildFile), append(b, '\n'), 0o644); err != nil {
		return out, err
	}
	if err := os.Remove(filepath.Join(final, incompleteMarker)); err != nil {
		return out, err
	}
	tmpLink := filepath.Join(root, ".current.new")
	_ = os.Remove(tmpLink)
	if err := os.Symlink(version, tmpLink); err != nil {
		return out, err
	}
	if err := os.Rename(tmpLink, filepath.Join(root, "current")); err != nil {
		return out, err
	}
	ok = true
	if aside != "" {
		_ = os.RemoveAll(aside)
	}
	return out, nil
}

// Versions lists the version folders of a wheelhouse kit item's SHA256SUMS paths ("1.5.0-ab12cd34/wheels/x.whl"),
// newest first. Only folders that hold a lock count.
func Versions(sums map[string]string) []string {
	set := map[string]bool{}
	for p := range sums {
		v, rest, ok := strings.Cut(p, "/")
		if ok && rest == WheelhouseLock && VersionRe.MatchString(v) {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j], out[i]) })
	return out
}

// Sub is the part of a wheelhouse kit item below one version folder.
func Sub(kitItemDir string, sums map[string]string, version string) Wheelhouse {
	w := Wheelhouse{Dir: filepath.Join(kitItemDir, version), Sums: map[string]string{}}
	for p, s := range sums {
		if rest, ok := strings.CutPrefix(p, version+"/"); ok {
			w.Sums[rest] = s
		}
	}
	return w
}

// versionLess orders versions by their dotted numbers, then as text.
func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	pa, pb := strings.FieldsFunc(a, sep), strings.FieldsFunc(b, sep)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil && na != nb {
			return na < nb
		}
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb) || bytes.Compare([]byte(a), []byte(b)) < 0 && len(pa) == len(pb)
}

func sep(r rune) bool { return r == '.' || r == '-' || r == '+' }
