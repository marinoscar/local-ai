package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// APIKeyPath is /etc/localai/api-key.
const APIKeyPath = "/etc/localai/api-key"

// KeyMaterial is the llama key as the push needs it. Key is secret: it goes into the body of one
// request over https and nowhere else. Fingerprint is the first four and the last four hex digits
// around an ellipsis, the only form that is ever logged.
type KeyMaterial struct {
	Key         string
	Fingerprint string
	RotatedAt   time.Time
}

// KeySource reads the current llama key. FileKeySource reads /etc/localai/api-key; the router's key
// code may provide its own.
type KeySource interface {
	Key() (KeyMaterial, error)
}

var keyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Fingerprint returns the display form of a llama key: "ab12" + ellipsis + "9f3c".
func Fingerprint(key string) string {
	if len(key) < 8 {
		return ""
	}
	return key[:4] + "…" + key[len(key)-4:]
}

// FileKeySource reads the key from the api-key file (root 0600). The rotation time comes from
// state.key.rotated_at, else from the file's modification time.
type FileKeySource struct {
	Path  string
	FS    config.FS
	State interface{ Snapshot() state.State }
}

// Key implements KeySource. The error never contains the key.
func (s FileKeySource) Key() (KeyMaterial, error) {
	path := s.Path
	if path == "" {
		path = APIKeyPath
	}
	fsys := s.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	f, err := config.ReadSecretFile(fsys, path, config.PolicyAPIKey)
	if err != nil {
		return KeyMaterial{}, fmt.Errorf("the llama key cannot be read: %w", pathError(err))
	}
	key := strings.ToLower(strings.TrimSpace(string(f.Data)))
	if !keyRE.MatchString(key) {
		return KeyMaterial{}, errors.New("the llama key file does not hold a 64 digit hex key")
	}
	m := KeyMaterial{Key: key, Fingerprint: Fingerprint(key)}
	if s.State != nil {
		if t := s.State.Snapshot().Key.RotatedAt; t != nil {
			m.RotatedAt = *t
		}
	}
	if m.RotatedAt.IsZero() {
		if fi, err := fsys.Stat(path); err == nil {
			m.RotatedAt = fi.ModTime()
		}
	}
	return m, nil
}

// pathError keeps only the operation and the cause of a file error, which carry no secret.
func pathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

type keyBody struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	RotatedAt   string `json:"rotated_at"`
}

// pushKey sends the current key to POST /api/nodes/{id}/localai/key (agent.md section 4.8.5) and, on
// success, records state.key.cloud_synced = true. It refuses to send over plain http. Only the
// fingerprint and the status of the answer are logged, never the key and never the answer's body.
func (l *Link) pushKey(ctx context.Context) (fingerprint string, err error) {
	if l.d.Keys == nil {
		return "", errors.New("cloud: no key source is configured")
	}
	km, err := l.d.Keys.Key()
	if err != nil {
		return "", err
	}
	fingerprint = km.Fingerprint
	if !keyRE.MatchString(km.Key) {
		return fingerprint, errors.New("the llama key is not a 64 digit hex key")
	}
	if !strings.HasPrefix(l.currentURL(), "https://") {
		return fingerprint, ErrKeyPushInsecure
	}
	if !l.hasFeature(FeatureKey) {
		return fingerprint, ErrNoKeyPush
	}
	path, ok := l.serverPath("/localai/key")
	if !ok {
		return fingerprint, ErrNotLinked
	}
	rotated := km.RotatedAt
	if rotated.IsZero() {
		rotated = l.clk.Now()
	}
	_, err = l.client().Call(ctx, "POST", path, keyBody{Key: km.Key, Fingerprint: km.Fingerprint, RotatedAt: rfc3339(rotated)}, callTimeout)
	if err != nil {
		// Strip the cloud's message: the body of the key route is never logged.
		if ae := asAPIError(err); ae != nil {
			l.noteAuth(err)
			if ae.Status == 404 || ae.Status == 405 {
				l.recheckHandshake(ctx)
			}
			return fingerprint, &APIError{Status: ae.Status, Code: ae.Code, Reason: ae.Reason, RetryAfter: ae.RetryAfter}
		}
		return fingerprint, err
	}
	l.mu.Lock()
	l.st.keyUnsyncedSince = time.Time{}
	l.mu.Unlock()
	l.clear(doctor.CLD060)
	l.log.Info("the llama key was sent to the cloud", "fingerprint", fingerprint)
	// cloud_synced is the last thing to change: whoever sees it true sees everything above done.
	l.setSynced(true)
	return fingerprint, nil
}

func (l *Link) setSynced(v bool) {
	_ = l.d.State.Mutate(func(s *state.State) error { s.Key.CloudSynced = v; return nil })
}

func (l *Link) keySynced() bool { return l.d.State.Snapshot().Key.CloudSynced }

// keyLoop pushes the key whenever state.key.cloud_synced is false: at link start, after a rotation, when
// the handshake says keyReported is false. A failed push is retried every 5 minutes, and after 30
// minutes unsynced LAI-CLD-060 is raised (agent.md section 4.8.5).
func (l *Link) keyLoop(ctx context.Context) {
	for {
		if l.keySynced() {
			// Synced: sleep until something clears the flag.
			select {
			case <-ctx.Done():
				return
			case <-l.keyWake:
			}
			continue
		}
		if l.online() {
			if _, err := l.pushKey(ctx); err == nil {
				if l.keySynced() {
					continue
				}
			} else {
				l.keyFailed(err)
			}
		}
		if !l.wait(ctx, keyRetryEvery, l.keyWake) {
			return
		}
	}
}

func (l *Link) keyFailed(err error) {
	now := l.clk.Now()
	l.mu.Lock()
	if l.st.keyUnsyncedSince.IsZero() {
		l.st.keyUnsyncedSince = now
	}
	since := l.st.keyUnsyncedSince
	l.mu.Unlock()
	l.log.Warn("the llama key could not be sent to the cloud", "error", errText(err))
	if now.Sub(since) >= keyWarnAfter {
		l.raise(doctor.CLD060, nil)
	}
}

// Push sends the current llama key to the cloud now. It is the key pusher the key routes call after
// a rotation, before the rotate result is reported: nil means the cloud has the key
// (state.key.cloud_synced is true), an error means it does not and cloud_synced is false; the link
// retries every 5 minutes by itself. ErrNotLinked is the answer of a node that is not enrolled,
// stopped or revoked.
func (l *Link) Push(ctx context.Context) error {
	if !l.callable() || !l.online() {
		l.setSynced(false)
		return ErrNotLinked
	}
	if _, err := l.pushKey(ctx); err != nil {
		l.setSynced(false)
		l.mu.Lock()
		if l.st.keyUnsyncedSince.IsZero() {
			l.st.keyUnsyncedSince = l.clk.Now()
		}
		l.mu.Unlock()
		l.kick(l.keyWake)
		return err
	}
	return nil
}

// ResendKey implements api.CloudLink (POST /cloud/resend-key): it pushes the key now and answers
// key_synced with the fingerprint. A node that is not linked answers 409 LAI-AGT-023; a push the cloud
// did not take answers 200 with key_synced false (the link keeps retrying).
func (l *Link) ResendKey(ctx context.Context) (api.ResendKeyResult, error) {
	if !l.callable() || !l.online() {
		return api.ResendKeyResult{}, api.NewError(api.CodeNotInMode, 409, map[string]any{"reason": "not_linked", "state": string(l.State())})
	}
	if l.d.Keys == nil {
		return api.ResendKeyResult{}, api.NewError(api.CodeInternal, 500, nil)
	}
	fp, err := l.pushKey(ctx)
	if err != nil {
		l.setSynced(false)
		l.kick(l.keyWake)
		if errors.Is(err, ErrKeyPushInsecure) || errors.Is(err, ErrNoKeyPush) || fp == "" {
			l.log.Warn("the llama key was not sent", "error", errText(err))
		}
		return api.ResendKeyResult{KeySynced: false, Fingerprint: fp}, nil
	}
	return api.ResendKeyResult{KeySynced: true, Fingerprint: fp}, nil
}
