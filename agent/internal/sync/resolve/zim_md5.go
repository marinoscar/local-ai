package resolve

import (
	"context"
	"crypto/md5" //nolint:gosec // the ZIM format's own integrity field is an MD5, not a security hash
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
)

// The ZIM header (openzim "ZIM file format", all integers little endian), 80 bytes:
//
//	 0 magic        uint32  0x44D495A
//	 4 majorVersion uint16
//	 6 minorVersion uint16
//	 8 uuid         16 bytes
//	24 entryCount   uint32
//	28 clusterCount uint32
//	32 urlPtrPos    uint64
//	40 titlePtrPos  uint64
//	48 clusterPtrPos uint64
//	56 mimeListPos  uint64
//	64 mainPage     uint32
//	68 layoutPage   uint32
//	72 checksumPos  uint64
//
// The last 16 bytes of the file, at checksumPos, are the MD5 of every byte before them (the bytes
// [0, checksumPos)). That is the format's built-in integrity check (ark-sync.md section 4.9.2).
const (
	zimMagic      = 0x44D495A
	zimHeaderSize = 80
	zimChecksumAt = 72
	zimMD5Size    = 16
)

// CodeZIMMD5 is the doctor code of a ZIM whose internal checksum failed (LAI-SYN-028).
const CodeZIMMD5 = doctor.SYN028

// ZIMError is a ZIM that failed its header or internal checksum check. It carries the doctor code
// (item.Validators reads DoctorCode, so the item is quarantined with LAI-SYN-028).
type ZIMError struct {
	Msg string
}

// Error implements error.
func (e *ZIMError) Error() string { return CodeZIMMD5 + ": " + e.Msg }

// DoctorCode returns LAI-SYN-028.
func (*ZIMError) DoctorCode() string { return CodeZIMMD5 }

// zimHeader is the part of the header the check needs.
type zimHeader struct {
	checksumPos uint64
}

// parseZIMHeader validates the first 80 bytes against the file size. A file shorter than a header, a
// wrong magic, a checksum position inside the header, or one that is not exactly 16 bytes before the end
// of the file (a truncated or padded download) is refused.
func parseZIMHeader(b []byte, size int64) (zimHeader, error) {
	if len(b) < zimHeaderSize || size < zimHeaderSize+zimMD5Size {
		return zimHeader{}, &ZIMError{Msg: "the file is too short to be a ZIM"}
	}
	if binary.LittleEndian.Uint32(b[0:4]) != zimMagic {
		return zimHeader{}, &ZIMError{Msg: "the file does not start with the ZIM magic number"}
	}
	pos := binary.LittleEndian.Uint64(b[zimChecksumAt : zimChecksumAt+8])
	switch {
	case pos < zimHeaderSize:
		return zimHeader{}, &ZIMError{Msg: "the checksum position points into the header"}
	case pos > uint64(size) || uint64(size)-pos != zimMD5Size:
		return zimHeader{}, &ZIMError{Msg: fmt.Sprintf("the checksum position %d does not match the file size %d", pos, size)}
	}
	return zimHeader{checksumPos: pos}, nil
}

// zimMD5Block is the read size of the check.
const zimMD5Block = 1 << 20

// VerifyZIMMD5 reads a ZIM once, front to back, and checks the MD5 stored at checksumPos against the
// MD5 of the bytes before it. It returns a *ZIMError (LAI-SYN-028) for a bad header or a mismatch, the
// context's error when ctx ends, and any other error for a read failure. The check does not allocate
// more than one block.
func VerifyZIMMD5(ctx context.Context, r io.Reader, size int64) error {
	head := make([]byte, zimHeaderSize)
	if _, err := io.ReadFull(r, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &ZIMError{Msg: "the file is too short to be a ZIM"}
		}
		return err
	}
	h, err := parseZIMHeader(head, size)
	if err != nil {
		return err
	}
	sum := md5.New() //nolint:gosec // see the import
	sum.Write(head)
	remaining := int64(h.checksumPos) - zimHeaderSize
	buf := make([]byte, zimMD5Block)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		got, err := io.ReadFull(r, buf[:n])
		sum.Write(buf[:got])
		remaining -= int64(got)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return &ZIMError{Msg: "the file ends before its checksum"}
			}
			return err
		}
	}
	var stored [zimMD5Size]byte
	if _, err := io.ReadFull(r, stored[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &ZIMError{Msg: "the file ends before its checksum"}
		}
		return err
	}
	if string(sum.Sum(nil)) != string(stored[:]) {
		return &ZIMError{Msg: "the MD5 inside the file does not match its content"}
	}
	return nil
}

// VerifyZIMFile is VerifyZIMMD5 over a file on disk.
func VerifyZIMFile(ctx context.Context, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return &ZIMError{Msg: "the ZIM is not a regular file"}
	}
	return VerifyZIMMD5(ctx, f, st.Size())
}

// ZIMValidatorName is the name item.Validators shows in reason.json for the ZIM check.
const ZIMValidatorName = "zim_md5"

// RegisterZIMValidator registers the ZIM internal checksum on the content validators of the item
// machine (ark-sync.md section 4.9.2): it runs in "verifying" for every kiwix item, over each staged
// file of kind zim, after the sha256 matched. A mismatch quarantines the item with LAI-SYN-028; a
// match sets the file's ZimMD5OK (the manifest's zim_md5_ok) when the resolver gave it a flag.
func RegisterZIMValidator(v *item.Validators) error {
	return v.Register(ZIMValidatorName,
		func(m item.Meta) bool { return m.Kind == "kiwix" },
		func(ctx context.Context, t item.Target) error {
			for _, f := range t.Files {
				if f.Kind != "zim" {
					continue
				}
				if !strings.HasSuffix(strings.ToLower(f.Path), ".zim") {
					return &ZIMError{Msg: "a file of kind zim is not named .zim"}
				}
				if err := VerifyZIMFile(ctx, filepath.Join(t.StagedDir, filepath.FromSlash(f.Path))); err != nil {
					if f.ZimMD5OK != nil {
						*f.ZimMD5OK = false
					}
					return err
				}
				if f.ZimMD5OK != nil {
					*f.ZimMD5OK = true
				}
			}
			return nil
		})
}
