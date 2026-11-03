package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// CUDA option B (rebuild-kit.md sections 4.1.6 and 4.3.2): when nvcc is not installed and the kit has
// the NVIDIA redistributable tarballs of a CUDA version the driver can run, they are unpacked into
// <scratch>/cuda-<ver>/ and the build points cmake at them. It is off unless the optional item
// kit-cuda-redist-<ver>-<arch> is on the root: nothing here runs otherwise.

const (
	// redistMarker is the file of a finished unpack: the digests of the archives it holds, so an
	// unpack that was interrupted, or made from other archives, is redone.
	redistMarker = ".kit-redist"
	// redistXZTimeout bounds the unpacking of one archive.
	redistXZTimeout = 30 * time.Minute
)

// redistLibPrefixes are the runtime libraries copied from the redistributable into the build's lib/.
var redistLibPrefixes = []string{"libcudart", "libcublas"}

// WithCudaRoot returns the plan with the redistributable tree wired in: cmake gets
// -DCUDAToolkit_ROOT=<dir> and -DCMAKE_CUDA_COMPILER=<dir>/bin/nvcc, and the programs get the run
// path $ORIGIN/../lib, where the build's libcudart and libcublas are copied (so they find them with
// no LD_LIBRARY_PATH, from any directory the build is moved to).
func (p Plan) WithCudaRoot(dir string) Plan {
	p.CMakeFlags = append(append([]string{}, p.CMakeFlags...),
		"-DCUDAToolkit_ROOT="+dir,
		"-DCMAKE_CUDA_COMPILER="+filepath.Join(dir, "bin", "nvcc"),
		"-DCMAKE_BUILD_RPATH=$ORIGIN/../lib",
	)
	p.ConfigureArgs = append([]string{"-S", p.SourceDir, "-B", p.BuildDir, "-G", "Ninja"}, p.CMakeFlags...)
	return p
}

// prepareRedist unpacks a redistributable set into <scratch>/cuda-<ver>/ and returns that directory.
// Every archive is checked against the digest the kit manifest recorded before it is read (a
// mismatch is LAI-KIT-001), unpacked with the shared extraction (traversal and link escapes
// refused) without its top-level directory, so the components merge into one tree, and the result
// must hold bin/nvcc. A finished unpack of the same archives is reused.
func (m *Manager) prepareRedist(ctx context.Context, set kit.CudaRedist) (string, *Error) {
	fsys := m.opt.FS
	dest := filepath.Join(m.opt.ScratchDir, "cuda-"+set.Version)
	fail := func(code, msg, cause string, err error) (string, *Error) {
		return "", newErr(code, StageSource, msg, err, "cause", cause, "redist", set.Dir)
	}
	var marker strings.Builder
	for _, f := range set.Files {
		marker.WriteString(f.Name + " " + f.SHA256 + "\n")
	}
	if b, err := fsys.ReadFile(filepath.Join(dest, redistMarker)); err == nil && string(b) == marker.String() {
		if fi, err := fsys.Stat(filepath.Join(dest, "bin", "nvcc")); err == nil && fi.Mode().IsRegular() {
			return dest, nil
		}
	}
	finder := kit.SourceFinder{FS: kitFS{fsys}, Roots: m.kitRoots()}
	tmp := dest + suffixTmp
	if err := fsys.RemoveAll(tmp); err != nil {
		return fail(CodeBuildFailed, "The scratch folder for the CUDA redistributables could not be prepared.", "scratch_dir", err)
	}
	if err := fsys.MkdirAll(tmp, 0o755); err != nil {
		return fail(CodeBuildFailed, "The scratch folder for the CUDA redistributables could not be prepared.", "scratch_dir", err)
	}
	for _, f := range set.Files {
		got, err := finder.HashFile(f.Path)
		if err != nil {
			_ = fsys.RemoveAll(tmp)
			return fail(CodeSourceDamaged, "The CUDA package "+f.Name+" of the kit cannot be read.", "redist_unreadable", err)
		}
		if got != f.SHA256 {
			_ = fsys.RemoveAll(tmp)
			return fail(CodeSourceDamaged, "The CUDA package "+f.Name+" of the kit is damaged: it does not match the sha256 the kit recorded. Run `aictl kit verify --full`.", "redist_damaged", nil)
		}
		if err := m.unpackRedist(ctx, f.Path, tmp); err != nil {
			_ = fsys.RemoveAll(tmp)
			if ctx.Err() != nil {
				return "", &Error{Code: CodeBuildFailed, Stage: StageSource, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
			}
			return fail(CodeBuildFailed, "The CUDA package "+f.Name+" of the kit could not be unpacked: "+err.Error(), "redist_unpack", err)
		}
	}
	if fi, err := fsys.Lstat(filepath.Join(tmp, "bin", "nvcc")); err != nil || !fi.Mode().IsRegular() {
		_ = fsys.RemoveAll(tmp)
		return fail(CodeBuildFailed, "The CUDA packages of the kit hold no bin/nvcc.", "redist_no_nvcc", err)
	}
	if err := fsys.WriteFile(filepath.Join(tmp, redistMarker), []byte(marker.String()), 0o644); err != nil {
		_ = fsys.RemoveAll(tmp)
		return fail(CodeBuildFailed, "The CUDA redistributable tree could not be finished.", "scratch_dir", err)
	}
	if err := fsys.RemoveAll(dest); err != nil {
		_ = fsys.RemoveAll(tmp)
		return fail(CodeBuildFailed, "The CUDA redistributable tree could not be replaced.", "scratch_dir", err)
	}
	if err := fsys.Rename(tmp, dest); err != nil {
		_ = fsys.RemoveAll(tmp)
		return fail(CodeBuildFailed, "The CUDA redistributable tree could not be put in place.", "scratch_dir", err)
	}
	return dest, nil
}

// unpackRedist extracts one archive into dir, dropping its top-level directory. NVIDIA publishes
// .tar.xz, which Go cannot read: `xz --decompress --stdout` streams it (no shell, no temporary tar).
func (m *Manager) unpackRedist(ctx context.Context, archive, dir string) error {
	if !strings.HasSuffix(archive, ".tar.xz") {
		f, err := m.opt.FS.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(m.opt.FS, f, dir, 1)
	}
	xz, err := m.opt.Lookup("xz")
	if err != nil {
		return fmt.Errorf("xz is needed to unpack it (apt package xz-utils): %w", err)
	}
	proc, err := m.opt.Streamer.Start(ctx, execx.StreamCmd{
		Path: xz, Args: []string{"--decompress", "--stdout", "--", archive},
		Env: toolEnv(), Timeout: redistXZTimeout, MaxStderr: 64 << 10,
	})
	if err != nil {
		return err
	}
	xerr := extractTar(m.opt.FS, proc.Stdout(), dir, 1)
	if xerr == nil {
		_, xerr = io.Copy(io.Discard, proc.Stdout()) // the tar end marker is not the end of the stream
	} else {
		proc.Kill()
	}
	res, werr := proc.Wait()
	switch {
	case xerr != nil:
		return xerr
	case werr != nil:
		return werr
	case res.ExitCode != 0:
		return fmt.Errorf("xz exited with status %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// copyRedistLibs copies libcudart* and libcublas* (libcublasLt included) from a redistributable
// tree into dst, keeping links that stay in the same directory. A tree with none is fine: the
// build may link them statically.
func (m *Manager) copyRedistLibs(root, dst string) error {
	fsys := m.opt.FS
	for _, sub := range []string{"lib", "lib64"} {
		dir := filepath.Join(root, sub)
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !hasAnyPrefix(name, redistLibPrefixes) {
				continue
			}
			from, to := filepath.Join(dir, name), filepath.Join(dst, name)
			fi, err := fsys.Lstat(from)
			if err != nil {
				return err
			}
			if err := fsys.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				link, err := fsys.Readlink(from)
				if err != nil {
					return err
				}
				if link == "" || path.Base(link) != link {
					continue // only a link inside the same directory is kept
				}
				_ = fsys.RemoveAll(to)
				if err := fsys.Symlink(link, to); err != nil {
					return err
				}
			case fi.Mode().IsRegular():
				perm := os.FileMode(0o644)
				if fi.Mode().Perm()&0o111 != 0 {
					perm = 0o755
				}
				if _, err := copyFile(fsys, from, to, perm, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
