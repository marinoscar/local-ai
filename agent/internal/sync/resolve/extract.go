package resolve

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Archive extraction of the web resolvers (ark-sync.md sections 4.9.4, 4.9.5 and 6): a verified
// archive of a `github_release` or `url` item with `extract: true` is unpacked into a staged
// directory that the engine swaps in as a directory item.
//
// The rules, each with its own test:
//
//   - the whole archive is checked as it is read: an absolute name, a `..` element, a backslash, a
//     control character, a symbolic link (wherever it points: a staged tree holds regular files only,
//     see item.ErrNotRegular), a hard link, a device, a fifo or any other special entry refuses the
//     whole archive, wanted or not;
//   - nothing is written except regular files and the directories above them, always below the
//     destination, never through a link (writeExtractedMode checks every directory it passes);
//   - modes are normalised: 0755 for a file with an executable bit, 0644 otherwise, never setuid;
//   - entry count, one file's size, the total size and the raw bytes read are bounded (a bomb stops at
//     the limit, not at the end of the disk);
//   - `.tar.gz` and `.zip` are read in-process; `.tar.xz` goes through the `xz` program, started by
//     execx with a fixed argv and its output read as a stream (the module has no xz decoder and the
//     agent's third-party modules are a closed list).

// ExtractLimits bound one extraction. The zero value of a field means its default.
type ExtractLimits struct {
	// MaxEntries is the number of entries of the archive (directories included).
	MaxEntries int
	// MaxFileBytes is the size of one file; MaxTotalBytes of all of them.
	MaxFileBytes, MaxTotalBytes int64
}

// Default bounds: far above map fonts, sprites and tool bundles, far below a disk.
const (
	defaultExtractEntries = 200_000
	defaultExtractFile    = 8 << 30
	defaultExtractTotal   = 32 << 30
)

func (l ExtractLimits) withDefaults() ExtractLimits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = defaultExtractEntries
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = defaultExtractFile
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = defaultExtractTotal
	}
	return l
}

// ExtractStats says what an extraction wrote.
type ExtractStats struct {
	// Files and Dirs are the entries written; Bytes the size of the files.
	Files, Dirs int
	Bytes       int64
}

// The archive formats, found by their first bytes and never by their name.
const (
	fmtTarGz  = "tar.gz"
	fmtTarXz  = "tar.xz"
	fmtTar    = "tar"
	fmtZip    = "zip"
	xzTimeout = 2 * time.Hour
)

// xzStderrMax is the part of xz's stderr kept for an error message.
const xzStderrMax = 4 << 10

// ErrNoXZ is wrapped when a .tar.xz must be unpacked and the xz program is not installed (CodeTool).
var ErrNoXZ = errors.New("resolve: the xz program is not installed")

// archiveFormat reads the first bytes of the file.
func archiveFormat(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return fmtTarGz, nil
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return fmtTarXz, nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return fmtZip, nil
	case len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar")):
		return fmtTar, nil
	}
	return "", fmt.Errorf("%w: not a .tar.gz, .tar.xz or .zip archive", ErrUnsafeArchive)
}

// ExtractArchive unpacks the archive at src into the existing directory dest, under the rules above.
// It does not delete anything: dest should be a fresh directory (a leftover from an earlier attempt
// would be overwritten file by file, which is why the caller names a new one for every attempt).
func (w *Web) ExtractArchive(ctx context.Context, src, dest string) (ExtractStats, error) {
	lim := w.Limits.withDefaults()
	format, err := archiveFormat(src)
	if err != nil {
		return ExtractStats{}, err
	}
	switch format {
	case fmtZip:
		return extractZip(ctx, src, dest, lim)
	case fmtTar:
		f, err := os.Open(src)
		if err != nil {
			return ExtractStats{}, err
		}
		defer f.Close()
		return extractTarStream(ctx, f, dest, lim)
	case fmtTarGz:
		f, err := os.Open(src)
		if err != nil {
			return ExtractStats{}, err
		}
		defer f.Close()
		zr, err := gzip.NewReader(bufio.NewReader(f))
		if err != nil {
			return ExtractStats{}, fmt.Errorf("%w: not a readable gzip stream", ErrUnsafeArchive)
		}
		defer zr.Close()
		return extractTarStream(ctx, zr, dest, lim)
	default: // fmtTarXz
		return w.extractTarXz(ctx, src, dest, lim)
	}
}

// extractTarXz streams `xz -dc` into the tar reader.
func (w *Web) extractTarXz(ctx context.Context, src, dest string, lim ExtractLimits) (ExtractStats, error) {
	xz, err := w.Env.tool("xz")
	if err != nil {
		return ExtractStats{}, fail(CodeTool, ErrNoXZ, "xz is not installed, so a .tar.xz cannot be unpacked")
	}
	st := w.Streamer
	if st == nil {
		st = execx.Exec{}
	}
	proc, err := st.Start(ctx, execx.StreamCmd{
		Path: xz, Args: []string{"-dc", "--memlimit-decompress=2GiB", "--", src},
		Env:     []string{"PATH=" + execx.FixedPath, "LC_ALL=C"},
		Timeout: xzTimeout, MaxStderr: xzStderrMax,
	})
	if err != nil {
		return ExtractStats{}, fail(CodeTool, err, "xz could not be started")
	}
	stats, terr := extractTarStream(ctx, proc.Stdout(), dest, lim)
	if terr != nil {
		proc.Kill()
	} else {
		// Drain what the tar reader left (padding, the end-of-archive blocks) so xz can finish.
		_, _ = io.Copy(io.Discard, io.LimitReader(proc.Stdout(), 1<<20))
	}
	res, werr := proc.Wait()
	switch {
	case terr != nil:
		return stats, terr
	case werr != nil:
		return stats, werr
	case res.ExitCode != 0:
		return stats, fmt.Errorf("%w: xz could not read the archive (exit %d): %s", ErrUnsafeArchive, res.ExitCode, Clip(res.Stderr, 200))
	}
	return stats, nil
}

// extractTarStream unpacks a tar stream (already decompressed).
func extractTarStream(ctx context.Context, r io.Reader, dest string, lim ExtractLimits) (ExtractStats, error) {
	// Every byte read is a header, padding or file content; content is counted separately, so the raw
	// cap only has to stop a stream that is mostly headers.
	tr := tar.NewReader(io.LimitReader(r, lim.MaxTotalBytes*2+int64(lim.MaxEntries)*1024))
	var (
		st      ExtractStats
		entries int
	)
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			return st, fmt.Errorf("%w: %v", ErrUnsafeArchive, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if entries++; entries > lim.MaxEntries {
			return st, fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, lim.MaxEntries)
		}
		rel, err := archiveEntryPath(h.Name)
		if err != nil {
			return st, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if rel == "" {
				continue
			}
			if err := mkdirBelow(dest, rel); err != nil {
				return st, err
			}
			st.Dirs++
		case tar.TypeReg:
			if rel == "" || strings.HasSuffix(h.Name, "/") {
				return st, fmt.Errorf("%w: %q is a file with a directory name", ErrUnsafeArchive, Clip([]byte(h.Name), 80))
			}
			if err := checkSize(&st, rel, h.Size, lim); err != nil {
				return st, err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := writeExtractedMode(dest, rel, tr, h.Size, mode); err != nil {
				return st, err
			}
			st.Files++
			st.Bytes += h.Size
		case tar.TypeSymlink:
			return st, fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeArchive, rel)
		case tar.TypeLink:
			return st, fmt.Errorf("%w: %s is a hard link", ErrUnsafeArchive, rel)
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return st, fmt.Errorf("%w: %s is a device or a fifo", ErrUnsafeArchive, rel)
		default:
			return st, fmt.Errorf("%w: %s has an unsupported entry type", ErrUnsafeArchive, rel)
		}
	}
}

// extractZip unpacks a zip file.
func extractZip(ctx context.Context, src, dest string, lim ExtractLimits) (ExtractStats, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return ExtractStats{}, fmt.Errorf("%w: not a readable zip file: %v", ErrUnsafeArchive, err)
	}
	defer zr.Close()
	var st ExtractStats
	if len(zr.File) > lim.MaxEntries {
		return st, fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, lim.MaxEntries)
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		rel, err := archiveEntryPath(f.Name)
		if err != nil {
			return st, err
		}
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return st, fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeArchive, rel)
		case mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0:
			return st, fmt.Errorf("%w: %s is a device, a fifo or a socket", ErrUnsafeArchive, rel)
		case f.FileInfo().IsDir():
			if rel == "" {
				continue
			}
			if err := mkdirBelow(dest, rel); err != nil {
				return st, err
			}
			st.Dirs++
			continue
		case rel == "":
			return st, fmt.Errorf("%w: %q is a file with a directory name", ErrUnsafeArchive, Clip([]byte(f.Name), 80))
		}
		if f.UncompressedSize64 > uint64(lim.MaxFileBytes) {
			return st, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsafeArchive, rel, lim.MaxFileBytes)
		}
		size := int64(f.UncompressedSize64)
		if err := checkSize(&st, rel, size, lim); err != nil {
			return st, err
		}
		rc, err := f.Open()
		if err != nil {
			return st, fmt.Errorf("%w: cannot read %s: %v", ErrUnsafeArchive, rel, err)
		}
		perm := os.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		err = writeExtractedMode(dest, rel, rc, size, perm)
		_ = rc.Close()
		if err != nil {
			return st, err
		}
		st.Files++
		st.Bytes += size
	}
	return st, nil
}

// checkSize applies the per-file and total bounds before a file is written.
func checkSize(st *ExtractStats, rel string, size int64, lim ExtractLimits) error {
	if size < 0 || size > lim.MaxFileBytes {
		return fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsafeArchive, rel, lim.MaxFileBytes)
	}
	if st.Bytes+size > lim.MaxTotalBytes {
		return fmt.Errorf("%w: more than %d bytes to extract", ErrUnsafeArchive, lim.MaxTotalBytes)
	}
	return nil
}

// archiveEntryPath validates the name of one entry and returns it as a clean slash path below the
// destination ("" for the archive's own root, "./"). Every entry of the archive goes through it,
// wanted or not.
func archiveEntryPath(name string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %q %s", ErrUnsafeArchive, Clip([]byte(name), 80), why)
	}
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return bad("is not a usable name")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return bad("has a control character")
		}
	}
	if strings.HasPrefix(name, "/") {
		return bad("is an absolute path")
	}
	for _, el := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if el == ".." {
			return bad("climbs out of the archive")
		}
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	if !inside(clean) {
		return bad("is not a usable name")
	}
	return clean, nil
}

// mkdirBelow creates the directory rel below root, refusing to pass through a link.
func mkdirBelow(root, rel string) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(full, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %s: %w", ErrUnsafeArchive, rel, err)
	}
	for p := full; len(p) > len(root); p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%w: %s sits behind a link or a file", ErrUnsafeArchive, rel)
		}
	}
	return nil
}
