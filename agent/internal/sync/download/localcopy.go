package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
)

// LocalSource reads a verified file of another content root instead of the network: the source of
// the planner's source = local_copy and of import (ark-sync.md sections 4.5 and 4.14). It plugs
// into Fetch as FileJob.Source, so the copy goes through the same .part, checkpoint, resume and
// streaming sha256 code as a download, and the result is verified against the expected sha256
// before it is trusted: a source is never taken on its word. The source file is only ever read.
//
// It differs from the network in three ways: the bucket is not applied (copy speed is limited by
// the disks only), a failure to read the source is permanent (retrying a damaged drive does not
// help; the planner falls back to the network), and its codes are CodeSourceDamaged.
type LocalSource struct {
	// FS reads the source; default OSFS.
	FS FS
	// Path is the absolute path of the source file.
	Path string
}

// NewLocalSource returns a LocalSource of path on fsys (nil means the real filesystem).
func NewLocalSource(fsys FS, path string) *LocalSource {
	if fsys == nil {
		fsys = OSFS{}
	}
	return &LocalSource{FS: fsys, Path: path}
}

// Local marks the source as not network traffic: Fetch skips the bandwidth bucket for it.
func (*LocalSource) Local() bool { return true }

// MismatchCode is the code of a digest that differs from the expected one: the copy is damaged.
func (*LocalSource) MismatchCode() string { return CodeSourceDamaged }

func (s *LocalSource) fail(format string, args ...any) *Error {
	return &Error{Kind: KindPermanent, Code: CodeSourceDamaged, Permanent: true, Err: fmt.Errorf(format, args...)}
}

// Open implements Source. The validator is "size-mtime" of the source file: when the file changed
// since the checkpoint the answer is 200 from byte 0, exactly as a server whose If-Range failed.
func (s *LocalSource) Open(ctx context.Context, offset int64, validator string) (io.ReadCloser, Resp, error) {
	if err := ctx.Err(); err != nil {
		return nil, Resp{}, err
	}
	st, err := s.FS.Stat(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Resp{}, s.fail("download: the source copy is missing")
		}
		return nil, Resp{}, s.fail("download: the source copy cannot be read: %v", scrubPathErr(err))
	}
	if !st.Mode().IsRegular() {
		return nil, Resp{}, s.fail("download: the source copy is not a regular file")
	}
	size := st.Size()
	tag := "local-" + strconv.FormatInt(size, 10) + "-" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	r := Resp{Status: 200, Size: size, ETag: tag}
	if offset > 0 && validator != "" && validator != tag {
		offset = 0 // the source changed: start over, like a 200 to a failed If-Range
	}
	if offset > 0 {
		if offset >= size {
			r.Status = 416
			return nil, r, nil
		}
		r.Status, r.Offset = 206, offset
	}
	f, err := s.FS.OpenRead(s.Path)
	if err != nil {
		return nil, Resp{}, s.fail("download: the source copy cannot be opened: %v", scrubPathErr(err))
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, Resp{}, s.fail("download: the source copy cannot be read: %v", scrubPathErr(err))
		}
	}
	return &localBody{ctx: ctx, f: f, src: s}, r, nil
}

// scrubPathErr keeps the errno and drops the path.
func scrubPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

type localBody struct {
	ctx context.Context
	f   ReadFile
	src *LocalSource
}

func (b *localBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := b.f.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, b.src.fail("download: reading the source copy failed: %v", scrubPathErr(err))
	}
	return n, err
}

func (b *localBody) Close() error { return b.f.Close() }
