package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// secret returns a 64-hex-digit string that differs per n, built at run time.
func secret(n int) string { return strings.Repeat(string(rune('a'+n%6)), 63) + string(rune('0'+n%10)) }

// writeKey writes the key file and sets its mtime, so a test controls what the mtime check sees.
func writeKey(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestFileKeyReloadsOnMtimeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeKey(t, path, secret(1)+"\n", m0)
	fk := NewFileKey(nil, path)

	got, err := fk.Key()
	if err != nil || got != secret(1) {
		t.Fatalf("first read: %q, %v", got, err)
	}
	// Same mtime and size: the cached key is served even though the content differs (that is the
	// point of the mtime check: no read per request).
	writeKey(t, path, secret(2)+"\n", m0)
	if got, _ = fk.Key(); got != secret(1) {
		t.Errorf("unchanged mtime and size must serve the cache, got a new key")
	}
	// A new mtime is a rotation.
	writeKey(t, path, secret(2)+"\n", m0.Add(time.Second))
	if got, err = fk.Key(); err != nil || got != secret(2) {
		t.Errorf("after mtime change: %q, %v", got, err)
	}
	// A size change alone is also noticed.
	writeKey(t, path, secret(3)+"xx\n", m0.Add(time.Second))
	if got, _ = fk.Key(); got != secret(3)+"xx" {
		t.Errorf("after size change: %q", got)
	}
}

func TestFileKeyInvalidateAndRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeKey(t, path, secret(1), m0)
	fk := NewFileKey(nil, path)
	if _, err := fk.Key(); err != nil {
		t.Fatal(err)
	}

	writeKey(t, path, secret(2), m0) // same mtime and size
	fk.Invalidate()
	if got, _ := fk.Key(); got != secret(2) {
		t.Errorf("Invalidate must force a read, got %q", got)
	}

	changed, err := fk.Refresh()
	if err != nil || changed {
		t.Errorf("Refresh of an unchanged file = %v, %v; want false, nil", changed, err)
	}
	writeKey(t, path, secret(4), m0)
	if changed, err = fk.Refresh(); err != nil || !changed {
		t.Errorf("Refresh after a rotation = %v, %v; want true, nil", changed, err)
	}
	if got, _ := fk.Key(); got != secret(4) {
		t.Errorf("Key after Refresh = %q", got)
	}
}

func TestFileKeyFailures(t *testing.T) {
	dir := t.TempDir()
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		setup func(path string)
		want  error
	}{
		{"missing", func(string) {}, ErrKeyUnavailable},
		{"directory", func(p string) { _ = os.Mkdir(p, 0o700) }, ErrKeyUnavailable},
		{"symlink", func(p string) {
			target := p + ".real"
			writeKey(t, target, secret(1), m0)
			_ = os.Symlink(target, p)
		}, ErrKeyUnavailable},
		{"empty", func(p string) { writeKey(t, p, "\n", m0) }, ErrBadKey},
		{"inner space", func(p string) { writeKey(t, p, "abc def", m0) }, ErrBadKey},
		{"control character", func(p string) { writeKey(t, p, "abc\x01def", m0) }, ErrBadKey},
		{"non ascii", func(p string) { writeKey(t, p, "clavé", m0) }, ErrBadKey},
		{"too long", func(p string) { writeKey(t, p, strings.Repeat("a", maxKeyLen+1), m0) }, ErrBadKey},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "k"+string(rune('a'+i)))
			c.setup(path)
			got, err := NewFileKey(nil, path).Key()
			if !errors.Is(err, c.want) || got != "" {
				t.Errorf("got %q, %v; want %v", got, err, c.want)
			}
		})
	}
}

func TestFileKeyFileGoneAfterLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	writeKey(t, path, secret(1), time.Now())
	fk := NewFileKey(nil, path)
	if _, err := fk.Key(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A key whose file is gone is not served from memory.
	if _, err := fk.Key(); !errors.Is(err, ErrKeyUnavailable) {
		t.Errorf("got %v, want ErrKeyUnavailable", err)
	}
}

func TestStaticKeyAndFingerprint(t *testing.T) {
	if k, err := StaticKey(secret(1)).Key(); err != nil || k != secret(1) {
		t.Errorf("StaticKey: %q %v", k, err)
	}
	if _, err := StaticKey("").Key(); !errors.Is(err, ErrBadKey) {
		t.Errorf("empty StaticKey: %v", err)
	}
	fp := Fingerprint(secret(1))
	if strings.Contains(fp, secret(1)) || !strings.HasSuffix(fp, secret(1)[60:]) || len(fp) > 12 {
		t.Errorf("fingerprint %q leaks or is too long", fp)
	}
}

func TestNewFileKeyDefaults(t *testing.T) {
	if fk := NewFileKey(nil, ""); fk.path != DefaultKeyPath || fk.fs == nil {
		t.Errorf("defaults not applied: %+v", fk)
	}
}
