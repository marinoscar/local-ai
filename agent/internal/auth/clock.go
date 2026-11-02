package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// Clock is the time seam of the package. internal/config/fake.Clock satisfies it.
type Clock interface{ Now() time.Time }

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// randomToken returns base64url (no padding) of n random bytes: 43 characters for 32 bytes.
// crypto/rand.Read never fails on a supported platform (Go 1.24 aborts the process instead).
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// secretKey hashes a bearer-style secret (a session cookie, a grant) into the key the in-memory
// stores use, so a lookup never compares the secret itself and the stores hold no usable secret.
func secretKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
