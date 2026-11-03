package update

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// File names of a signed release (agent.md section 4.10).
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.minisig"

	// maxSumsBytes bounds SHA256SUMS: a real one has five short lines.
	maxSumsBytes = 1 << 20
)

// ErrNoSums is a release directory without SHA256SUMS or SHA256SUMS.minisig (`aictl verify-release` exit 2).
var ErrNoSums = errors.New("update: SHA256SUMS or SHA256SUMS.minisig is missing")

// SumEntry is one line of SHA256SUMS.
type SumEntry struct {
	// Hash is the lower-case hex sha256.
	Hash string
	// Name is a plain file name, never a path.
	Name string
}

// Sums is a parsed SHA256SUMS, in file order.
type Sums []SumEntry

// ParseSums parses "<sha256>  <name>" lines (text mode, two spaces; binary mode " *" is accepted too). It is
// strict because the file is signed but the names are used to open files: no empty line, no path separator,
// no "." or "..", no control character, no duplicate name, no hash that is not 64 hex digits.
func ParseSums(data []byte) (Sums, error) {
	if len(data) > maxSumsBytes {
		return nil, fmt.Errorf("%w: SHA256SUMS is too large", ErrMalformed)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: SHA256SUMS must be text lines ending in a newline", ErrMalformed)
	}
	var out Sums
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) < 64+2+1 || (line[64:66] != "  " && line[64:66] != " *") {
			return nil, fmt.Errorf("%w: SHA256SUMS line %d", ErrMalformed, i+1)
		}
		hash := strings.ToLower(line[:64])
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("%w: SHA256SUMS line %d has no sha256", ErrMalformed, i+1)
		}
		name := line[66:]
		if !plainName(name) || seen[name] {
			return nil, fmt.Errorf("%w: SHA256SUMS line %d has a bad or repeated file name", ErrMalformed, i+1)
		}
		seen[name] = true
		out = append(out, SumEntry{Hash: hash, Name: name})
	}
	return out, nil
}

// Lookup returns the entry for name.
func (s Sums) Lookup(name string) (SumEntry, bool) {
	for _, e := range s {
		if e.Name == name {
			return e, true
		}
	}
	return SumEntry{}, false
}

func plainName(n string) bool {
	if n == "" || len(n) > 255 || n == "." || n == ".." || strings.ContainsAny(n, "/\\") {
		return false
	}
	for _, c := range n {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// hashEqual compares two lower-case hex digests in constant time.
func hashEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sha256File returns the hex sha256 of a file and its size, reading at most limit bytes (a larger file is an
// error).
func sha256File(fsys FS, path string, limit int64) (string, int64, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil {
		return "", n, err
	}
	if n > limit {
		return "", n, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), limit)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// readLimited reads a small file, refusing one larger than limit.
func readLimited(fsys FS, path string, limit int64) ([]byte, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), limit)
	}
	return data, nil
}

// FileCheck is the verdict on one file of SHA256SUMS.
type FileCheck struct {
	Name string
	// Status is "ok", "mismatch" or "missing".
	Status string
}

// DirReport is what VerifyDir found.
type DirReport struct {
	// KeyID is the id of the key that signed SHA256SUMS (KeyIDString form).
	KeyID string
	// TrustedComment is the signed comment of the signature.
	TrustedComment string
	// Files lists every line of SHA256SUMS, in file order.
	Files []FileCheck
}

// Checked counts the files that matched.
func (r DirReport) Checked() int {
	n := 0
	for _, f := range r.Files {
		if f.Status == "ok" {
			n++
		}
	}
	return n
}

// DirOptions tunes VerifyDir.
type DirOptions struct {
	// IgnoreMissing accepts a file listed in SHA256SUMS that is not in the directory (like
	// `sha256sum -c --ignore-missing`): a downloaded subset of a release.
	IgnoreMissing bool
	// MaxFileBytes bounds the size of each file that is hashed; 0 means 8 GiB.
	MaxFileBytes int64
}

// VerifyDir verifies a downloaded or kit release directory offline: SHA256SUMS.minisig against keys, then
// every file listed in SHA256SUMS (agent.md section 4.10, aictl.md section 4.27a). It is the entry point of
// `aictl verify-release` and of the kit verification. It needs no agent, no network and no root.
//
// The returned report is filled as far as the check got. The error is nil only when the signature verified
// and every listed file matched (a missing file only when IgnoreMissing). A directory without SHA256SUMS or
// its signature is ErrNoSums; a bad or unknown signature is ErrBadSignature, ErrUnknownKey, ErrNoKeys or
// ErrMalformed; a file whose hash differs is *MismatchError. Callers map every error but ErrNoSums to
// LAI-AGT-010 (LAI-KIT-044 for a kit directory).
func VerifyDir(fsys FS, dir string, keys []PublicKey, opt DirOptions) (DirReport, error) {
	var rep DirReport
	sums, err := readLimited(fsys, filepath.Join(dir, SumsName), maxSumsBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rep, fmt.Errorf("%w: %v", ErrNoSums, err)
		}
		return rep, err
	}
	sigData, err := readLimited(fsys, filepath.Join(dir, SigName), maxSignatureFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rep, fmt.Errorf("%w: %v", ErrNoSums, err)
		}
		return rep, err
	}
	sig, err := Verify(keys, sigData, sums)
	if err != nil {
		return rep, err
	}
	rep.KeyID = KeyIDString(sig.KeyID)
	rep.TrustedComment = sig.TrustedComment

	entries, err := ParseSums(sums)
	if err != nil {
		return rep, err
	}
	limit := opt.MaxFileBytes
	if limit <= 0 {
		limit = 8 << 30
	}
	var bad []string
	for _, e := range entries {
		sum, _, herr := sha256File(fsys, filepath.Join(dir, e.Name), limit)
		switch {
		case herr == nil && hashEqual(sum, e.Hash):
			rep.Files = append(rep.Files, FileCheck{Name: e.Name, Status: "ok"})
		case herr != nil && errors.Is(herr, os.ErrNotExist):
			rep.Files = append(rep.Files, FileCheck{Name: e.Name, Status: "missing"})
			if !opt.IgnoreMissing {
				bad = append(bad, e.Name+" (missing)")
			}
		default:
			rep.Files = append(rep.Files, FileCheck{Name: e.Name, Status: "mismatch"})
			bad = append(bad, e.Name)
		}
	}
	if len(bad) > 0 {
		return rep, &MismatchError{Files: bad}
	}
	return rep, nil
}

// MismatchError lists the files of a release directory that did not match SHA256SUMS or were missing.
type MismatchError struct{ Files []string }

// Error implements error.
func (e *MismatchError) Error() string {
	return "update: files that do not match SHA256SUMS: " + strings.Join(e.Files, ", ")
}
