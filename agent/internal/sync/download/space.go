package download

import (
	"context"
	"fmt"
)

// SpaceMaker frees space on a root by purging what is safe to purge: the eligible trash, oldest
// first (ark-sync.md section 4.10, implemented by SYNC-08). The downloader only asks; it never
// deletes anything itself. Purge returns the bytes freed (0 when nothing was eligible) and may be
// called concurrently.
type SpaceMaker interface {
	Purge(ctx context.Context, rootID string, needBytes int64) (freed int64, err error)
}

// FreeFunc returns the size of the filesystem holding path and the bytes a non-root process may
// still use (Bavail times Bsize).
type FreeFunc func(path string) (total, free int64, err error)

// DriveMinFloor is the smallest min_free_bytes of a drive root: 5 GB.
const DriveMinFloor int64 = 5_000_000_000

// SpaceRecheckEvery is how many downloaded bytes lie between two free-space checks (256 MiB).
const SpaceRecheckEvery int64 = 256 << 20

// DriveMinFree is min_free_bytes of an ark drive root: 2 % of the filesystem size, at least 5 GB
// (ark-sync.md section 4.8). The internal root takes profile.storage.min_free_bytes instead.
func DriveMinFree(fsTotal int64) int64 {
	return max(fsTotal/50, DriveMinFloor)
}

// EnsureSpace is the pre-start hook of ark-sync.md section 4.8: it holds when
// free - needBytes >= minFree on the filesystem of rootPath. When it does not, the eligible trash
// is purged through the SpaceMaker (the amount short, oldest first) and the check repeats. If the
// space is still short it returns an *Error of KindDisk (CodeDisk): the item is "skipped: disk" and
// the scheduler tries the next one. purged is how many bytes the SpaceMaker freed, for the caller
// to report CodePurged (information) when it is not zero.
//
// Fetch runs the same check for the remainder of each file; the engine calls EnsureSpace once for
// the whole item first, so a download that cannot fit does not start.
func (d *Downloader) EnsureSpace(ctx context.Context, rootID, rootPath string, needBytes, minFree int64) (purged int64, err error) {
	return d.ensureSpace(ctx, rootID, rootPath, needBytes, minFree)
}

func (d *Downloader) ensureSpace(ctx context.Context, rootID, rootPath string, need, minFree int64) (purged int64, err error) {
	ok, short, err := d.spaceOK(rootPath, need, minFree)
	if err != nil {
		return 0, err
	}
	if ok {
		return 0, nil
	}
	if d.opt.Space != nil {
		freed, perr := d.opt.Space.Purge(ctx, rootID, short)
		if perr != nil {
			d.opt.Logger.Warn("trash purge failed", "component", "sync", "root", rootID, "err", perr.Error())
		}
		purged = max(freed, 0)
		if ok, short, err = d.spaceOK(rootPath, need, minFree); err != nil {
			return purged, err
		}
		if ok {
			return purged, nil
		}
	}
	return purged, &Error{Kind: KindDisk, Code: CodeDisk, Err: fmt.Errorf("download: %d bytes short of the free space this download needs (minimum free %d)", short, minFree)}
}

// spaceOK measures the filesystem. short is how many bytes are missing when it does not hold. A
// probe that fails with an I/O error is a root fault; any other probe failure means "unknown" and
// the check passes (the write itself will report a full disk).
func (d *Downloader) spaceOK(rootPath string, need, minFree int64) (ok bool, short int64, err error) {
	_, free, ferr := d.opt.Free(rootPath)
	if ferr != nil {
		if isIO(ferr) {
			return false, 0, ferr
		}
		d.opt.Logger.Warn("free space unknown", "component", "sync", "err", ferr.Error())
		return true, 0, nil
	}
	if free-need >= minFree {
		return true, 0, nil
	}
	return false, minFree + need - free, nil
}
