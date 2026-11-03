package router

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// DefaultKeyPath is the node's llama key file (agent.md section 3.3).
const DefaultKeyPath = "/etc/localai/api-key"

// Errors of the key source. Their text never contains key material.
var (
	// ErrKeyUnavailable: the key file cannot be read (missing, not a regular file, I/O error).
	ErrKeyUnavailable = errors.New("router: the llama key is not available")
	// ErrBadKey: the key file holds something that cannot be sent as a bearer credential (empty,
	// over 512 bytes, or a character outside printable ASCII).
	ErrBadKey = errors.New("router: the llama key file holds an unusable key")
)

// maxKeyLen bounds a key; the node's key is 64 hex digits.
const maxKeyLen = 512

// KeySource supplies the key the client sends as a bearer credential. Key is called before every
// request, so an implementation must be cheap when nothing changed.
type KeySource interface {
	Key() (string, error)
}

// KeyRefresher is an optional extension of a KeySource: Refresh forgets what was cached and reads
// the key again, reporting whether it differs from the one in use. The client calls it after a 401
// and retries once when the key changed (the key was rotated between two reads).
type KeyRefresher interface {
	Refresh() (changed bool, err error)
}

// KeyInvalidator is an optional extension of a KeySource: Invalidate makes the next Key call read
// the file regardless of its modification time. The watcher calls it on key.rotated.
type KeyInvalidator interface {
	Invalidate()
}

// StaticKey is a fixed key, for tests and for callers that already hold the key.
type StaticKey string

// Key implements KeySource.
func (k StaticKey) Key() (string, error) {
	if err := checkKey(string(k)); err != nil {
		return "", err
	}
	return string(k), nil
}

// Fingerprint is the only form of the key that may appear in a log line, an error or an event: the
// last four characters behind an ellipsis.
func Fingerprint(key string) string { return logx.Redact(key) }

// KeyFingerprint is how the key is shown to people: the first four and the last four characters
// around an ellipsis ("ab12…9f3c", security-model.md section 2). It is the form of state.key, of
// POST /key/rotate and of the key.rotated event. A value shorter than 16 characters is not a key
// this node makes, and is shown as "…" alone rather than half-revealed.
func KeyFingerprint(key string) string {
	r := []rune(key)
	if len(r) < 16 {
		return "…"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// KeyBytes is the size of a llama key before hex encoding: 32 random bytes, 64 hex digits.
const KeyBytes = 32

// GenerateKey returns a new llama key: KeyBytes random bytes read from src (nil: crypto/rand) as 64
// lower-case hex digits.
func GenerateKey(src io.Reader) (string, error) {
	if src == nil {
		src = rand.Reader
	}
	var b [KeyBytes]byte
	if _, err := io.ReadFull(src, b[:]); err != nil {
		return "", fmt.Errorf("router: no randomness for a new key: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// WriteKey replaces the key file at path (empty: DefaultKeyPath) with key, atomically (temp file,
// fsync, rename, directory fsync), mode 0600, owner root:root (config.PolicyAPIKey). The old file
// stays whole until the rename, so a failure leaves the old key in place. The error never contains
// the key.
func WriteKey(fsys config.FS, path, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if fsys == nil {
		fsys = config.OSFS{}
	}
	if path == "" {
		path = DefaultKeyPath
	}
	p := config.PolicyAPIKey
	if err := config.WriteFileAtomicFS(fsys, path, []byte(key+"\n"), p.Mode, p.UID, p.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return fmt.Errorf("router: could not write the key file: %w", err)
	}
	return nil
}

func checkKey(k string) error {
	if k == "" || len(k) > maxKeyLen {
		return ErrBadKey
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return ErrBadKey
		}
	}
	return nil
}

// FileKey reads the key from a file and re-reads it when the file's modification time or size
// changes (a rotation writes a new file and renames it into place), or after Invalidate or Refresh.
// It never returns a stale key for a file that has gone: a missing file is ErrKeyUnavailable.
type FileKey struct {
	fs   config.FS
	path string

	mu     sync.Mutex
	loaded bool
	key    string
	mtime  time.Time
	size   int64
}

var (
	_ KeySource      = (*FileKey)(nil)
	_ KeyRefresher   = (*FileKey)(nil)
	_ KeyInvalidator = (*FileKey)(nil)
)

// NewFileKey returns a FileKey over fsys (nil means the real filesystem) at path ("" means
// DefaultKeyPath).
func NewFileKey(fsys config.FS, path string) *FileKey {
	if fsys == nil {
		fsys = config.OSFS{}
	}
	if path == "" {
		path = DefaultKeyPath
	}
	return &FileKey{fs: fsys, path: path}
}

// Key implements KeySource.
func (f *FileKey) Key() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := f.fs.Lstat(f.path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	if f.loaded && info.ModTime().Equal(f.mtime) && info.Size() == f.size {
		return f.key, nil
	}
	return f.readLocked(info.ModTime(), info.Size())
}

// Refresh implements KeyRefresher.
func (f *FileKey) Refresh() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, had := f.key, f.loaded
	info, err := f.fs.Lstat(f.path)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	k, err := f.readLocked(info.ModTime(), info.Size())
	if err != nil {
		return false, err
	}
	return !had || k != old, nil
}

// Invalidate implements KeyInvalidator.
func (f *FileKey) Invalidate() {
	f.mu.Lock()
	f.loaded = false
	f.mu.Unlock()
}

// readLocked reads and validates the file. Permission problems are reported by the config package's
// secret checks elsewhere (LAI-SEC), so the issues of ReadSecretFile are not this client's concern.
func (f *FileKey) readLocked(mtime time.Time, size int64) (string, error) {
	sf, err := config.ReadSecretFile(f.fs, f.path, config.PolicyAPIKey)
	if err != nil {
		f.loaded = false
		return "", fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	k := strings.TrimSpace(string(sf.Data))
	if err := checkKey(k); err != nil {
		f.loaded = false
		return "", err
	}
	// mtime and size come from the Lstat taken before the read: if the file changes in between, the
	// next call sees a different mtime and reads again, never the other way round.
	f.key, f.mtime, f.size, f.loaded = k, mtime, size, true
	return k, nil
}
