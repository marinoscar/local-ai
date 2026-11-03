package manifest

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// SumsName is the file in a directory item that lists the sha256 of every file, in the format of
// sha256sum: "<64 hex>  <relative path>" per line.
const SumsName = "SHA256SUMS"

// Sum is one line of a SHA256SUMS file.
type Sum struct {
	SHA256 string
	// Path is relative to the item directory, slash separated.
	Path string
}

func (s Sum) line() string { return s.SHA256 + "  " + s.Path + "\n" }

// FormatSums renders sums as a SHA256SUMS file, sorted by path. It validates every entry.
func FormatSums(sums []Sum) ([]byte, error) {
	sorted, err := checkSums(sums)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for _, s := range sorted {
		b.WriteString(s.line())
	}
	return b.Bytes(), nil
}

// ParseSums reads a SHA256SUMS file. Both separators of sha256sum are accepted ("  " for text and
// " *" for binary mode); a line starting with a backslash (an escaped name) is refused, as are an
// invalid hash or path and a path listed twice. The result is sorted by path. An empty file is
// an error: a directory item has files.
func ParseSums(data []byte) ([]Sum, error) {
	var sums []Sum
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 || (line[64:66] != "  " && line[64:66] != " *") {
			return nil, fmt.Errorf("%w: %s line %d is not \"<sha256>  <path>\"", ErrCorrupt, SumsName, n)
		}
		sums = append(sums, Sum{SHA256: line[:64], Path: line[66:]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, SumsName, err)
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("%w: %s lists no files", ErrCorrupt, SumsName)
	}
	return checkSums(sums)
}

func checkSums(sums []Sum) ([]Sum, error) {
	out := append([]Sum(nil), sums...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	for i, s := range out {
		switch {
		case !IsSHA256(s.SHA256):
			return nil, fmt.Errorf("%w: %s: %q is not 64 lowercase hex digits", ErrCorrupt, SumsName, s.SHA256)
		case !ValidFilePath(s.Path) || strings.ContainsRune(s.Path, '\n'):
			return nil, fmt.Errorf("%w: %s: %q is not a relative path inside the item", ErrCorrupt, SumsName, s.Path)
		case i > 0 && out[i-1].Path == s.Path:
			return nil, fmt.Errorf("%w: %s: %q is listed twice", ErrCorrupt, SumsName, s.Path)
		}
	}
	return out, nil
}

// TreeSHA256 is the tree hash of a directory item: the sha256 of the "<sha256>  <path>\n" lines of
// every file, the lines sorted bytewise. It does not depend on the order of sums. The entries are
// validated like FormatSums does.
func TreeSHA256(sums []Sum) (string, error) {
	if _, err := checkSums(sums); err != nil {
		return "", err
	}
	lines := make([]string, len(sums))
	for i, s := range sums {
		lines[i] = s.line()
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteSums writes dir/SHA256SUMS atomically (mode 0644). Write it before the manifest: the
// manifest's commit point must find it.
func WriteSums(fsys config.FS, dir string, sums []Sum) error {
	data, err := FormatSums(sums)
	if err != nil {
		return err
	}
	return config.WriteFileAtomicFS(fsys, filepath.Join(dir, SumsName), data, 0o644, -1, -1)
}

// ReadSums reads and parses dir/SHA256SUMS. A missing file satisfies errors.Is(err, fs.ErrNotExist).
func ReadSums(fsys config.FS, dir string) ([]Sum, error) {
	data, err := fsys.ReadFile(filepath.Join(dir, SumsName))
	if err != nil {
		return nil, err
	}
	return ParseSums(data)
}
