package router

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestKeyFingerprint(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"a 64 hex key", "abcd" + strings.Repeat("0", 56) + "wxyz", "abcd…wxyz"},
		{"sixteen characters", "0123456789abcdef", "0123…cdef"},
		{"too short to show", "0123456789abcde", "…"},
		{"empty", "", "…"},
	}
	for _, c := range cases {
		if got := KeyFingerprint(c.key); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	k := strings.Repeat("a", 64)
	if f := KeyFingerprint(k); strings.Contains(f, k) || len(f) >= len(k) {
		t.Errorf("fingerprint %q reveals too much", f)
	}
}

func TestGenerateKey(t *testing.T) {
	k, err := GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0xA5}, KeyBytes)))
	if err != nil || !hex64Re.MatchString(k) || k != strings.Repeat("a5", KeyBytes) {
		t.Fatalf("key from a fixed source: %q, %v", k, err)
	}
	// The real source gives 64 hex digits, and a different key each time.
	a, errA := GenerateKey(nil)
	b, errB := GenerateKey(nil)
	if errA != nil || errB != nil || !hex64Re.MatchString(a) || !hex64Re.MatchString(b) || a == b {
		t.Errorf("crypto/rand keys: %v %v", errA, errB)
	}
	// A source that runs dry is an error that names no key.
	_, err = GenerateKey(bytes.NewReader(make([]byte, KeyBytes-1)))
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: %v", err)
	}
	if _, err := GenerateKey(errReader{}); err == nil {
		t.Error("a failing source must fail")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// ownerFS is the real filesystem whose files only record the owner asked for: the tests run as an
// ordinary user and still prove the key is written root:root.
type ownerFS struct {
	config.OSFS
	mu     sync.Mutex
	owners [][2]int
}

type ownerFile struct {
	config.File
	fs *ownerFS
}

func (f ownerFile) Chown(uid, gid int) error {
	f.fs.mu.Lock()
	f.fs.owners = append(f.fs.owners, [2]int{uid, gid})
	f.fs.mu.Unlock()
	return nil
}

func (o *ownerFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	f, err := o.OSFS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return ownerFile{File: f, fs: o}, nil
}

type renameFailsFS struct{ *ownerFS }

func (renameFailsFS) Rename(string, string) error { return errors.New("no space left on device") }

func TestWriteKeyReplacesTheFileAtomicallyWithMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	old, fresh := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := os.WriteFile(path, []byte(old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &ownerFS{}
	if err := WriteKey(fs, path, fresh); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != fresh+"\n" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if len(fs.owners) != 1 || fs.owners[0] != [2]int{0, 0} {
		t.Errorf("owner asked for = %v", fs.owners)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
	// FileKey reads back what was written.
	if k, err := NewFileKey(nil, path).Key(); err != nil || k != fresh {
		t.Errorf("FileKey = %q, %v", k, err)
	}
}

func TestWriteKeyFailureKeepsTheOldKeyAndNamesNoKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	old, fresh := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := os.WriteFile(path, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteKey(renameFailsFS{&ownerFS{}}, path, fresh)
	if err == nil {
		t.Fatal("a failed rename must fail the write")
	}
	if strings.Contains(err.Error(), fresh) || strings.Contains(err.Error(), old) {
		t.Errorf("the error holds a key: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != old+"\n" {
		t.Errorf("the old key was lost: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the temp file was not removed: %v", entries)
	}
}

func TestWriteKeyRefusesAnUnusableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	for _, k := range []string{"", "has space in it", strings.Repeat("a", maxKeyLen+1), "tab\tbed"} {
		if err := WriteKey(&ownerFS{}, path, k); !errors.Is(err, ErrBadKey) {
			t.Errorf("%q: %v", k, err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a file was created for a bad key")
	}
}
