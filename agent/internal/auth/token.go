package auth

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// TokenPrefix starts every agent token.
const TokenPrefix = "lat_"

// TokenRecheck is how often Verify looks at the token file's mtime (agent.md section 3.9.2). A
// compare that fails re-reads the file at once, so a rotation is seen by the next wrong guess too.
const TokenRecheck = 10 * time.Second

// GenerateToken returns a new agent token: lat_ plus base64url of 32 random bytes (43 characters).
// It is shared with `aictl token rotate`.
func GenerateToken() string { return TokenPrefix + randomToken(32) }

// TokenFingerprint is the form of a token logs and audit may carry: lat_...last4.
func TokenFingerprint(token string) string { return logx.Redact(token) }

// TokenStore holds the agent token of /etc/localai/agent-token: it creates the file when it is
// absent or empty, caches the token, and re-reads the file when it changes.
type TokenStore struct {
	fs     config.FS
	path   string
	policy config.SecretPolicy
	clock  Clock
	log    *slog.Logger

	mu        sync.Mutex
	token     []byte
	mtime     time.Time
	size      int64
	checkedAt time.Time
	lastErr   string
	onChange  func()
}

// OnChange registers f to run, with the store's lock held, when a re-read finds a different token
// (a rotation). The service uses it to revoke the PIN grants earned with the old one; f must not
// call back into the store.
func (t *TokenStore) OnChange(f func()) {
	t.mu.Lock()
	t.onChange = f
	t.mu.Unlock()
}

// NewTokenStore returns a store for path. Call Ensure before the first Verify.
func NewTokenStore(fsys config.FS, path string, policy config.SecretPolicy, clock Clock, log *slog.Logger) *TokenStore {
	if fsys == nil {
		fsys = config.OSFS{}
	}
	if clock == nil {
		clock = RealClock{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &TokenStore{fs: fsys, path: path, policy: policy, clock: clock, log: log}
}

// Ensure loads the token, creating the file when it is absent or empty (LAI-SEC-011, mode and
// owner from the policy: 0640 root:localai-operators in production). It returns created = true in
// that case, so the caller can raise the warning alert. A file with unsafe permissions is fixed
// (LAI-SEC-009 is logged). A path that is not a regular file is an error: the agent never follows
// a link to a secret.
func (t *TokenStore) Ensure() (created bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sf, err := config.ReadSecretFile(t.fs, t.path, t.policy)
	switch {
	case err == nil && len(trimToken(sf.Data)) > 0:
		if len(sf.Issues) > 0 {
			t.fixPermissions()
		}
		t.token = trimToken(sf.Data)
		t.remember()
		return false, nil
	case err == nil || errors.Is(err, os.ErrNotExist):
		// absent or empty: create it
	default:
		return false, fmt.Errorf("agent token: %w", err)
	}
	token := GenerateToken()
	if err := config.WriteFileAtomicFS(t.fs, t.path, []byte(token+"\n"), t.policy.Mode, t.policy.UID, t.policy.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return false, fmt.Errorf("agent token: create %s: %w", t.path, err)
	}
	t.token = []byte(token)
	t.remember()
	t.log.Warn("the agent token was missing and a new one was created", "code", CodeTokenCreated, "fingerprint", TokenFingerprint(token))
	return true, nil
}

// fixPermissions enforces the policy on the file and logs LAI-SEC-009 for what it found.
func (t *TokenStore) fixPermissions() {
	issues, err := config.EnforceSecret(t.fs, t.path, t.policy)
	for _, is := range issues {
		t.log.Warn("secret file had unsafe permissions", "code", CodeSecretPermissions, "path", is.Path, "issue", is.Message)
	}
	if err != nil {
		t.log.Error("could not fix the permissions of the agent token", "code", CodeSecretPermissions, "path", t.path, "error", err.Error())
	}
}

// remember records the file's mtime and size for the change check. The caller holds mu.
func (t *TokenStore) remember() {
	t.checkedAt = t.clock.Now()
	if info, err := t.fs.Stat(t.path); err == nil {
		t.mtime, t.size = info.ModTime(), info.Size()
	}
}

// Verify reports whether presented is the agent token. The compare is constant time, the file is
// looked at again when it is due (every TokenRecheck) and after a failed compare, and an empty
// presented value or an empty token never matches.
func (t *TokenStore) Verify(presented string) bool {
	if presented == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshIfDue()
	if t.match(presented) {
		return true
	}
	t.reload()
	return t.match(presented)
}

func (t *TokenStore) match(presented string) bool {
	return len(t.token) > 0 && subtle.ConstantTimeCompare(t.token, []byte(presented)) == 1
}

// refreshIfDue re-reads the file when its mtime or size changed, at most every TokenRecheck.
func (t *TokenStore) refreshIfDue() {
	now := t.clock.Now()
	if now.Sub(t.checkedAt) < TokenRecheck {
		return
	}
	t.checkedAt = now
	info, err := t.fs.Stat(t.path)
	if err != nil {
		t.noteErr(err)
		return
	}
	if info.ModTime().Equal(t.mtime) && info.Size() == t.size {
		return
	}
	t.reload()
}

// reload reads the file. A file that cannot be read, or is empty, leaves the cached token alone:
// the agent keeps serving the token it has rather than locking out every caller (an atomic rotation
// never leaves the file empty; a hand edit gone wrong is logged).
func (t *TokenStore) reload() {
	t.checkedAt = t.clock.Now()
	sf, err := config.ReadSecretFile(t.fs, t.path, t.policy)
	if err != nil {
		t.noteErr(err)
		return
	}
	tok := trimToken(sf.Data)
	if len(tok) == 0 {
		t.noteErr(errors.New("the token file is empty"))
		return
	}
	t.lastErr = ""
	changed := len(t.token) > 0 && !bytes.Equal(t.token, tok)
	t.token = tok
	if changed && t.onChange != nil {
		t.onChange()
	}
	if info, err := t.fs.Stat(t.path); err == nil {
		t.mtime, t.size = info.ModTime(), info.Size()
	}
}

// noteErr logs a read problem once, until a read works again, so a flood of wrong guesses cannot
// flood the log.
func (t *TokenStore) noteErr(err error) {
	if t.lastErr == "" {
		t.lastErr = err.Error()
		t.log.Warn("cannot re-read the agent token; keeping the cached one", "error", t.lastErr)
	}
}

func trimToken(b []byte) []byte { return bytes.TrimSpace(b) }
