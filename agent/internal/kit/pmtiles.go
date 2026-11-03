package kit

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The pmtiles installer (go-pmtiles, the program that makes and inspects map archives). It follows the
// kiwix-tools and piper installers: the tarball is hashed against the pinned sha256 before a byte is
// extracted, the single program is smoke tested before it replaces anything, `current` is repointed
// atomically and the previous version is kept. Offline: the tarball is a local file.

const (
	// PmtilesBinary is the program of the release.
	PmtilesBinary = "pmtiles"
	// pmtilesMarker holds the sha256 of the tarball a version directory was extracted from.
	pmtilesMarker = ".tarball.sha256"

	pmtilesMaxEntries = 1000
	pmtilesMaxFile    = 256 << 20

	pmtilesSmokeTimeout = 10 * time.Second
)

var pmtilesVersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,63}$`)

func (t *ToolInstaller) pmtilesRoot() string { return filepath.Join(t.optRoot(), "pmtiles") }

// installPmtiles installs the go-pmtiles tarball as <OptRoot>/pmtiles/<version>/pmtiles and makes it
// current. It is a TarballInstaller, wired into Install.
//
//  1. the arguments are checked and the tarball's sha256 must equal expectSHA256;
//  2. when <version>/ already holds this tarball (marker and executable program) it is reused,
//     otherwise only the file named pmtiles is extracted, into <version>.tmp/ (no other entry is ever
//     written; an unsafe name, a second pmtiles or an oversized file refuses the archive);
//  3. `pmtiles version` of the new directory must print the version, BEFORE it is moved into place;
//  4. current is repointed atomically (current.tmp symlink renamed over current). Old versions stay.
func (t *ToolInstaller) installPmtiles(ctx context.Context, tarball, expectSHA256, version string) error {
	if !pmtilesVersionRe.MatchString(version) || strings.HasSuffix(version, ".tmp") || strings.HasSuffix(version, ".bad") {
		return fmt.Errorf("version %q is not a release number such as 1.22.1", clip(version, 40))
	}
	want := strings.ToLower(strings.TrimSpace(expectSHA256))
	if !manifest.IsSHA256(want) {
		return errors.New("the pinned sha256 must be 64 hexadecimal characters; refusing to install an unverified tarball")
	}
	got, err := hashFile(ctx, t.FS, tarball, maxDebBytes)
	if err != nil {
		return fmt.Errorf("cannot read the tarball %s: %w", tarball, err)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errors.New("the tarball does not match the kit's pinned sha256; nothing was installed")
	}

	root := t.pmtilesRoot()
	verDir := filepath.Join(root, version)
	tmpDir := verDir + ".tmp"
	badDir := verDir + ".bad"

	if t.pmtilesMatches(verDir, want) {
		if err := t.pmtilesSmoke(ctx, verDir, version); err != nil {
			return err
		}
	} else {
		if err := t.FS.MkdirAll(root, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", root, err)
		}
		if err := t.FS.RemoveAll(tmpDir); err != nil {
			return fmt.Errorf("cannot remove the leftover %s: %w", tmpDir, err)
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = t.FS.RemoveAll(tmpDir)
			}
		}()
		if err := t.pmtilesExtract(ctx, tarball, tmpDir, want); err != nil {
			return err
		}
		if err := t.pmtilesSmoke(ctx, tmpDir, version); err != nil {
			return err
		}
		if _, err := t.FS.Lstat(verDir); err == nil {
			if err := t.FS.RemoveAll(badDir); err != nil {
				return fmt.Errorf("cannot remove the leftover %s: %w", badDir, err)
			}
			if err := t.FS.Rename(verDir, badDir); err != nil {
				return fmt.Errorf("cannot move the old %s aside: %w", verDir, err)
			}
		}
		if err := t.FS.Rename(tmpDir, verDir); err != nil {
			return fmt.Errorf("cannot move the new version into place: %w", err)
		}
		cleanup = false
		_ = t.FS.RemoveAll(badDir)
	}
	return t.pmtilesRepoint(root, version)
}

// pmtilesMatches reports whether verDir holds the program extracted from the tarball with this hash.
func (t *ToolInstaller) pmtilesMatches(verDir, sha string) bool {
	data, err := t.FS.ReadFile(filepath.Join(verDir, pmtilesMarker))
	if err != nil || strings.TrimSpace(string(data)) != sha {
		return false
	}
	fi, err := t.FS.Lstat(filepath.Join(verDir, PmtilesBinary))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// pmtilesEntry says whether a tar entry name is the program, refusing every unsafe name.
func pmtilesEntry(name string) (isProgram bool, err error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return false, fmt.Errorf("the archive has an unsafe entry name %q", clip(name, 60))
	}
	clean := path.Clean(name)
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if part == ".." {
			return false, fmt.Errorf("the archive has an unsafe entry name %q", clip(name, 60))
		}
	}
	return parts[len(parts)-1] == PmtilesBinary && len(parts) <= 2, nil
}

func (t *ToolInstaller) pmtilesExtract(ctx context.Context, tarball, tmpDir, sha string) error {
	f, err := t.FS.Open(tarball)
	if err != nil {
		return fmt.Errorf("cannot open the tarball %s: %w", tarball, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(ctxReader{ctx, f})
	if err != nil {
		return fmt.Errorf("the tarball is not a gzip archive: %w", err)
	}
	if err := t.FS.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", tmpDir, err)
	}
	tr := tar.NewReader(zr)
	found := false
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the tarball is damaged: %w", err)
		}
		if n >= pmtilesMaxEntries {
			return fmt.Errorf("the archive has more than %d entries", pmtilesMaxEntries)
		}
		isProg, err := pmtilesEntry(h.Name)
		if err != nil {
			return err
		}
		if !isProg {
			continue // LICENSE, README, folders: never written
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s in the archive is not a plain file", PmtilesBinary)
		}
		if found {
			return fmt.Errorf("the archive holds %s twice", PmtilesBinary)
		}
		if h.Size <= 0 || h.Size > pmtilesMaxFile {
			return fmt.Errorf("%s in the archive has a size that is not plausible", PmtilesBinary)
		}
		out, err := t.FS.OpenFile(filepath.Join(tmpDir, PmtilesBinary), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			return fmt.Errorf("cannot write %s: %w", filepath.Join(tmpDir, PmtilesBinary), err)
		}
		copied, cerr := io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr == nil && copied != h.Size {
			cerr = io.ErrUnexpectedEOF
		}
		if cerr == nil {
			cerr = out.Chmod(0o755)
		}
		if cerr == nil {
			cerr = out.Sync()
		}
		if e := out.Close(); cerr == nil {
			cerr = e
		}
		if cerr != nil {
			return fmt.Errorf("cannot write %s: %w", PmtilesBinary, cerr)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("the archive has no %s program", PmtilesBinary)
	}
	return config.WriteFileAtomicFS(t.FS, filepath.Join(tmpDir, pmtilesMarker), []byte(sha+"\n"), 0o644, -1, -1)
}

// pmtilesSmoke runs `pmtiles version` of a version directory and requires the version in its output.
func (t *ToolInstaller) pmtilesSmoke(ctx context.Context, dir, version string) error {
	res, err := t.Runner.Run(ctx, execx.Cmd{Path: filepath.Join(dir, PmtilesBinary), Args: []string{"version"}, Timeout: pmtilesSmokeTimeout, MaxOut: 64 << 10})
	if err != nil {
		return fmt.Errorf("the new pmtiles does not run: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("the new pmtiles exited with status %d", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), version) {
		return fmt.Errorf("the new pmtiles does not report version %s", version)
	}
	return nil
}

// pmtilesRepoint makes <root>/current point to version: a symlink current.tmp created next to it and
// renamed over it, so a reader sees the old or the new one, never none.
func (t *ToolInstaller) pmtilesRepoint(root, version string) error {
	tmp := filepath.Join(root, CurrentName+".tmp")
	if err := t.FS.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot remove the leftover %s: %w", tmp, err)
	}
	if err := t.FS.Symlink(version, tmp); err != nil {
		return fmt.Errorf("cannot create %s: %w", tmp, err)
	}
	if err := t.FS.Rename(tmp, filepath.Join(root, CurrentName)); err != nil {
		_ = t.FS.Remove(tmp)
		return fmt.Errorf("cannot repoint current: %w", err)
	}
	_ = t.FS.SyncDir(root)
	return nil
}
