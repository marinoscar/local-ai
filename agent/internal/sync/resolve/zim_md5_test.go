package resolve

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
)

func zimErr(t *testing.T, err error) *ZIMError {
	t.Helper()
	var ze *ZIMError
	if !errors.As(err, &ze) {
		t.Fatalf("want a *ZIMError, got %T %v", err, err)
	}
	if ze.DoctorCode() != doctor.SYN028 {
		t.Fatalf("code %s, want %s", ze.DoctorCode(), doctor.SYN028)
	}
	return ze
}

func TestVerifyZIMMD5OnTheFixture(t *testing.T) {
	b := readFixture(t, tinyZIMPath)
	if err := VerifyZIMMD5(context.Background(), bytes.NewReader(b), int64(len(b))); err != nil {
		t.Fatalf("the fixture must verify: %v", err)
	}
	if err := VerifyZIMFile(context.Background(), tinyZIMPath); err != nil {
		t.Fatalf("by file name: %v", err)
	}
}

func TestVerifyZIMMD5FlippedByteIsLAISYN028(t *testing.T) {
	good := readFixture(t, tinyZIMPath)
	// One flipped bit in the header, in the middle of the payload, on the last byte of the payload and
	// in the stored MD5 itself: each is a mismatch.
	for _, at := range []int{20, zimHeaderSize + 7, len(good) - zimMD5Size - 1, len(good) - 1} {
		b := append([]byte(nil), good...)
		b[at] ^= 0x01
		ze := zimErr(t, VerifyZIMMD5(context.Background(), bytes.NewReader(b), int64(len(b))))
		if !strings.Contains(ze.Msg, "MD5") {
			t.Errorf("flip at %d: %q", at, ze.Msg)
		}
	}
}

func TestVerifyZIMMD5HeaderChecks(t *testing.T) {
	good := readFixture(t, tinyZIMPath)
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := map[string][]byte{
		"empty":       nil,
		"short":       good[:40],
		"header only": good[:zimHeaderSize],
		"truncated":   good[:len(good)-5],
		"padded":      append(append([]byte(nil), good...), 0),
		"wrong magic": mut(func(b []byte) []byte { b[0] ^= 0xff; return b }),
		"checksum in header": mut(func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[zimChecksumAt:], 12)
			return b
		}),
		"checksum past end": mut(func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[zimChecksumAt:], uint64(len(b)))
			return b
		}),
	}
	for name, b := range cases {
		err := VerifyZIMMD5(context.Background(), bytes.NewReader(b), int64(len(b)))
		if err == nil {
			t.Errorf("%s: must be refused", name)
			continue
		}
		zimErr(t, err)
	}
	// the reader ends before the size it was told
	if err := VerifyZIMMD5(context.Background(), bytes.NewReader(good[:len(good)-30]), int64(len(good))); err == nil {
		t.Error("a reader shorter than the declared size must fail")
	} else {
		zimErr(t, err)
	}
}

func TestVerifyZIMMD5CancelAndReadError(t *testing.T) {
	good := readFixture(t, tinyZIMPath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyZIMMD5(ctx, bytes.NewReader(good), int64(len(good))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	boom := errors.New("disk fell over")
	r := &failingReader{data: good, failAfter: 100, err: boom}
	if err := VerifyZIMMD5(context.Background(), r, int64(len(good))); !errors.Is(err, boom) {
		t.Fatalf("a read error is passed on as it is, not turned into a bad checksum: %v", err)
	}
	if err := VerifyZIMFile(context.Background(), filepath.Join(t.TempDir(), "absent.zim")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

type failingReader struct {
	data      []byte
	failAfter int
	err       error
	n         int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n >= r.failAfter {
		return 0, r.err
	}
	k := copy(p, r.data[r.n:min(len(r.data), r.failAfter)])
	r.n += k
	return k, nil
}

func TestZIMValidatorSetsTheManifestFlagOrQuarantinesWithLAISYN028(t *testing.T) {
	vals := item.NewValidators()
	if err := RegisterZIMValidator(vals); err != nil {
		t.Fatal(err)
	}
	if err := RegisterZIMValidator(vals); err == nil {
		t.Fatal("a second registration must be refused")
	}
	dir := t.TempDir()
	good := readFixture(t, tinyZIMPath)
	if err := os.WriteFile(filepath.Join(dir, "book_2026-03.zim"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	flag := new(bool)
	target := item.Target{
		Meta:      item.Meta{ItemID: "book", Kind: "kiwix", SourceType: "kiwix"},
		StagedDir: dir, Files: []item.File{{Path: "book_2026-03.zim", Kind: "zim", ZimMD5OK: flag}},
	}
	if err := vals.Run(context.Background(), target); err != nil {
		t.Fatalf("good ZIM: %v", err)
	}
	if !*flag {
		t.Fatal("zim_md5_ok was not set")
	}

	bad := append([]byte(nil), good...)
	bad[zimHeaderSize+100] ^= 0x80
	if err := os.WriteFile(filepath.Join(dir, "book_2026-03.zim"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	err := vals.Run(context.Background(), target)
	var ve *item.ValidationError
	if !errors.As(err, &ve) || ve.Code != doctor.SYN028 || ve.Validator != ZIMValidatorName {
		t.Fatalf("want a validation error LAI-SYN-028, got %v", err)
	}
	if *flag {
		t.Fatal("zim_md5_ok must be false after a failure")
	}

	// other items and other file kinds are left alone
	other := target
	other.Meta = item.Meta{ItemID: "map", Kind: "maps", SourceType: "pmtiles_extract"}
	if err := vals.Run(context.Background(), other); err != nil {
		t.Fatalf("a maps item must not be checked: %v", err)
	}
	notZIM := target
	notZIM.Files = []item.File{{Path: "readme.txt", Kind: "file"}}
	if err := vals.Run(context.Background(), notZIM); err != nil {
		t.Fatalf("a file of another kind must not be checked: %v", err)
	}
}
