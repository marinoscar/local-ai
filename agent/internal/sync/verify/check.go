package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// hashBufSize is the read buffer: 1 MiB, the downloader's block size, so a cancel lands at the same
// granularity everywhere in ark sync.
const hashBufSize = 1 << 20

// errStopped is returned by a Between callback that wants the check to stop before the next file (the
// time budget is spent). The item then keeps its old last_reverified_at.
var errStopped = errors.New("verify: stopped between files")

// Bad reasons, the reason of a BadFile.
const (
	// BadMissing: the file is not there any more.
	BadMissing = "missing"
	// BadSize: the file has another size than the manifest says.
	BadSize = "size"
	// BadChecksum: the file reads fine and hashes to something else (bit rot).
	BadChecksum = "checksum"
	// BadUnreadable: the file cannot be read to the end (an I/O error of the disk).
	BadUnreadable = "unreadable"
	// BadSums: a directory item's SHA256SUMS is missing, damaged or does not match tree_sha256.
	BadSums = "sums"
)

// BadFile is the first file of a copy that did not match its manifest.
type BadFile struct {
	// Path is relative to the item directory, slash separated.
	Path   string
	Reason string
	// Expected and Actual are the sha256 (or the sizes, for BadSize) that were compared.
	Expected, Actual string
	// ExpectedBytes and Bytes are the expected and the read sizes.
	ExpectedBytes, Bytes int64
	// Err is the cause of a BadUnreadable (never shown to a user).
	Err error
}

// Check is the outcome of reading one copy against its manifest.
type Check struct {
	// OK: every file matched.
	OK bool
	// Bad is set when OK is false: the first file that did not match.
	Bad BadFile
	// Bytes is how many bytes were read.
	Bytes int64
}

// checkCopy hashes every file of a copy and compares it with its manifest: for a file item the size and
// sha256 of each file; for a directory item the SHA256SUMS against tree_sha256, then every file it
// lists. It stops at the first file that does not match (the item is judged as a unit) and returns the
// context's error, or whatever between returned, when the check is cut short; the caller then leaves
// the copy exactly as it was. between is called before each file.
func checkCopy(ctx context.Context, fsys FS, dir string, m manifest.Manifest, between func() error, progress func(n int64)) (Check, error) {
	var res Check
	type target struct {
		rel, sha string
		size     int64 // -1 for a tree file: the size is whatever SHA256SUMS' hash covers
	}
	var files []target
	if m.IsDir() {
		sums, err := manifest.ReadSums(fsys, dir)
		if err != nil {
			return Check{Bad: BadFile{Path: manifest.SumsName, Reason: BadSums, Err: err}}, nil
		}
		tree, err := manifest.TreeSHA256(sums)
		if err != nil || tree != m.TreeSHA256 {
			return Check{Bad: BadFile{Path: manifest.SumsName, Reason: BadSums, Expected: m.TreeSHA256, Actual: tree, Err: err}}, nil
		}
		for _, s := range sums {
			files = append(files, target{rel: s.Path, sha: s.SHA256, size: -1})
		}
	} else {
		for _, f := range m.Files {
			files = append(files, target{rel: f.Path, sha: f.SHA256, size: f.SizeBytes})
		}
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if between != nil {
			if err := between(); err != nil {
				return res, err
			}
		}
		base := res.Bytes
		sum, size, err := hashFile(ctx, fsys, filepath.Join(dir, filepath.FromSlash(f.rel)), func(n int64) {
			if progress != nil {
				progress(base + n)
			}
		})
		res.Bytes += size
		bad := BadFile{Path: f.rel, Expected: f.sha, Actual: sum, ExpectedBytes: f.size, Bytes: size}
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return res, ctx.Err()
		case errors.Is(err, fs.ErrNotExist):
			bad.Reason = BadMissing
			res.Bad = bad
			return res, nil
		default:
			bad.Reason, bad.Err = BadUnreadable, err
			res.Bad = bad
			return res, nil
		}
		switch {
		case f.size >= 0 && size != f.size:
			bad.Reason = BadSize
			bad.Expected, bad.Actual = fmt.Sprint(f.size), fmt.Sprint(size)
			res.Bad = bad
			return res, nil
		case sum != f.sha:
			bad.Reason = BadChecksum
			res.Bad = bad
			return res, nil
		}
	}
	res.OK = true
	return res, nil
}

// hashFile reads a whole file from disk and returns its sha256 and size. progress receives the bytes
// read so far after every block. A cancelled context stops it at the next block.
func hashFile(ctx context.Context, fsys FS, name string, progress func(n int64)) (string, int64, error) {
	r, err := fsys.OpenRead(name)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	buf := make([]byte, hashBufSize)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += int64(n)
			if progress != nil {
				progress(size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", size, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// copyBytes is the number of bytes a check of the copy reads.
func copyBytes(it ark.ScannedItem) int64 { return it.Manifest.TotalBytes }
