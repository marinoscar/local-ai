package download

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// localJob is a copy job whose source is a file of "another root".
func localJob(e *env, srcPath string, data []byte) FileJob {
	j := e.job("local:ark-0a1b2c3d/"+testItem, data)
	j.Source = NewLocalSource(nil, srcPath)
	return j
}

func writeSource(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "other-root", "models", testItem, testName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalCopyVerifiesAndLeavesTheSourceAlone(t *testing.T) {
	data := genData(testSize, 50)
	src := writeSource(t, data)
	before, _ := os.Stat(src)
	e := newEnv(t, nil)
	res := mustFetch(t, e, localJob(e, src, data))
	if string(e.readPart()) != string(data) || res.SHA256 != sum(data) {
		t.Error("the copy differs")
	}
	if e.bucket.total() != 0 {
		t.Errorf("a local copy drew %d bytes from the bandwidth bucket: only the network is throttled", e.bucket.total())
	}
	after, _ := os.Stat(src)
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("the source file was touched")
	}
	if b, _ := os.ReadFile(src); string(b) != string(data) {
		t.Error("the source content changed")
	}
}

func TestLocalCopyNeverTrustsTheSource(t *testing.T) {
	data := genData(testSize, 51)
	flipped := append([]byte(nil), data...)
	flipped[100] ^= 1
	src := writeSource(t, flipped) // the other root's copy rotted
	e := newEnv(t, nil)
	res, err := e.dl.Fetch(context.Background(), localJob(e, src, data), e.cp)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindMismatch || fe.Code != CodeSourceDamaged {
		t.Fatalf("err = %v, want a damaged-copy mismatch (%s)", err, CodeSourceDamaged)
	}
	if !res.Mismatch || res.Code != CodeSourceDamaged {
		t.Errorf("Result = %+v", res)
	}
	if b, _ := os.ReadFile(src); string(b) != string(flipped) {
		t.Error("the source was modified")
	}
}

func TestLocalCopyUnusableSourceIsPermanent(t *testing.T) {
	data := genData(1000, 52)
	dir := t.TempDir()
	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "nope.bin"),
		"directory": dir,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			res, err := e.dl.Fetch(context.Background(), localJob(e, path, data), e.cp)
			var fe *Error
			if !errors.As(err, &fe) || fe.Kind != KindPermanent || fe.Code != CodeSourceDamaged || !res.Permanent {
				t.Fatalf("err = %v, Result %+v", err, res)
			}
			if res.Attempts != 1 || len(e.clk.delays()) != 0 {
				t.Errorf("attempts %d, waits %v: a missing source is not retried", res.Attempts, e.clk.delays())
			}
		})
	}
}

// cancelSource cancels the context once it has delivered n bytes.
type cancelSource struct {
	*LocalSource
	cancel func()
	after  int
}

func (c *cancelSource) Open(ctx context.Context, offset int64, validator string) (io.ReadCloser, Resp, error) {
	body, r, err := c.LocalSource.Open(ctx, offset, validator)
	if body == nil {
		return nil, r, err
	}
	return &cancelBody{ReadCloser: body, cancel: c.cancel, left: c.after}, r, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel func()
	left   int
}

func (b *cancelBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.left -= n
	if b.left <= 0 {
		b.cancel()
	}
	return n, err
}

func TestLocalCopyResumesFromTheCheckpoint(t *testing.T) {
	data := genData(testSize, 53)
	src := writeSource(t, data)
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	j := localJob(e, src, data)
	j.Source = &cancelSource{LocalSource: NewLocalSource(nil, src), cancel: cancel, after: 5 * testBuf}
	res, err := e.dl.Fetch(ctx, j, e.cp)
	if !errors.Is(err, context.Canceled) || res.Bytes == 0 || res.Bytes >= testSize {
		t.Fatalf("first run: %v, Result %+v", err, res)
	}
	part := res.Bytes
	res2 := mustFetch(t, e, localJob(e, src, data))
	if res2.ResumedFrom != part || res2.Restarted {
		t.Errorf("resume: %+v, want to continue from %d", res2, part)
	}
	if string(e.readPart()) != string(data) {
		t.Error("the resumed copy differs")
	}
}

func TestLocalCopyRestartsWhenTheSourceChanged(t *testing.T) {
	data := genData(testSize, 54)
	src := writeSource(t, data)
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	j := localJob(e, src, data)
	j.Source = &cancelSource{LocalSource: NewLocalSource(nil, src), cancel: cancel, after: 5 * testBuf}
	if _, err := e.dl.Fetch(ctx, j, e.cp); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// The other root replaced its copy with a new version of the same size.
	newer := genData(testSize, 55)
	if err := os.WriteFile(src, newer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(src, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	res := mustFetch(t, e, localJob(e, src, newer))
	if !res.Restarted || string(e.readPart()) != string(newer) {
		t.Errorf("Result %+v: a changed source must restart the copy from zero", res)
	}
}

func TestLocalSourceOpen(t *testing.T) {
	data := genData(10000, 56)
	src := writeSource(t, data)
	s := NewLocalSource(nil, src)
	ctx := context.Background()
	body, r, err := s.Open(ctx, 0, "")
	if err != nil || r.Status != 200 || r.Size != 10000 || r.Offset != 0 || r.ETag == "" {
		t.Fatalf("whole file: %+v, %v", r, err)
	}
	_ = body.Close()
	tag := r.ETag
	body, r, err = s.Open(ctx, 4000, tag)
	if err != nil || r.Status != 206 || r.Offset != 4000 {
		t.Fatalf("resume: %+v, %v", r, err)
	}
	buf := make([]byte, 10)
	if n, _ := body.Read(buf); n != 10 || string(buf) != string(data[4000:4010]) {
		t.Error("resume reads from the wrong offset")
	}
	_ = body.Close()
	if body, r, _ = s.Open(ctx, 4000, "local-1-1"); r.Status != 200 || r.Offset != 0 {
		t.Errorf("a changed source must answer 200 from zero, got %+v", r)
	} else {
		_ = body.Close()
	}
	if body, r, _ = s.Open(ctx, 10000, tag); body != nil || r.Status != 416 || r.Size != 10000 {
		t.Errorf("offset at the end: %+v", r)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.Open(cctx, 0, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
	if !s.Local() || s.MismatchCode() != CodeSourceDamaged {
		t.Error("LocalSource must say it is local and name its mismatch code")
	}
}
