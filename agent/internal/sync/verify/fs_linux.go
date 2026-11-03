//go:build linux

package verify

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// dropEvery is how much is read before the pages behind the reader are dropped from the cache.
const dropEvery = 16 << 20

// OpenRead implements FS: posix_fadvise(SEQUENTIAL) at open, posix_fadvise(DONTNEED) on the range just
// read every 16 MiB and on the whole file at the end. Pages a running process has mapped (a loaded
// model) are never evicted by DONTNEED, so this only gives back the cache the check itself filled.
func (OSFS) OpenRead(name string) (io.ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_SEQUENTIAL) // best effort
	return &seqReader{f: f}, nil
}

type seqReader struct {
	f             *os.File
	read, dropped int64
}

func (r *seqReader) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	r.read += int64(n)
	if r.read-r.dropped >= dropEvery {
		_ = unix.Fadvise(int(r.f.Fd()), r.dropped, r.read-r.dropped, unix.FADV_DONTNEED)
		r.dropped = r.read
	}
	return n, err
}

func (r *seqReader) Close() error {
	_ = unix.Fadvise(int(r.f.Fd()), 0, 0, unix.FADV_DONTNEED)
	return r.f.Close()
}
