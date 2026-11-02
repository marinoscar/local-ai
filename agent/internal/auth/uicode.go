package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"
)

// UI-code rules (agent.md sections 3.8.2 and 3.9.2).
const (
	// UICodeDigits is the length of a login code.
	UICodeDigits = 6
	// UICodeTTL is how long a code is valid.
	UICodeTTL = 5 * time.Minute
	// MaxUICodes is how many unexpired codes may exist; minting one more evicts the oldest.
	MaxUICodes = 3
)

// UICodes holds the live login codes. They are six decimal digits drawn uniformly from crypto/rand,
// single use, memory only. Redeeming goes through the failure limiter (Service), not here.
type UICodes struct {
	clock Clock

	mu    sync.Mutex
	codes []uiCode // oldest first
}

type uiCode struct {
	code    string
	minted  time.Time
	expires time.Time
}

// NewUICodes returns an empty store.
func NewUICodes(clock Clock) *UICodes {
	if clock == nil {
		clock = RealClock{}
	}
	return &UICodes{clock: clock}
}

// Mint creates a code. When MaxUICodes unexpired codes already exist, the oldest is invalidated.
func (u *UICodes) Mint() (code string, expires time.Time) {
	now := u.clock.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.purgeLocked(now)
	for len(u.codes) >= MaxUICodes {
		u.codes = u.codes[1:]
	}
	for {
		code = randomDigits(UICodeDigits)
		if !u.hasLocked(code) { // two live codes never collide, so a code names one mint
			break
		}
	}
	expires = now.Add(UICodeTTL)
	u.codes = append(u.codes, uiCode{code: code, minted: now, expires: expires})
	return code, expires
}

// Consume redeems a code: true once, for an unexpired code, and the code is gone. Every live code
// is compared in constant time, with no early exit, so the time taken does not say which matched.
func (u *UICodes) Consume(code string) bool {
	now := u.clock.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.purgeLocked(now)
	hit := -1
	for i, c := range u.codes {
		if subtle.ConstantTimeCompare([]byte(c.code), []byte(code)) == 1 && hit < 0 {
			hit = i
		}
	}
	if hit < 0 {
		return false
	}
	u.codes = append(u.codes[:hit], u.codes[hit+1:]...)
	return true
}

// Len returns the number of unexpired codes.
func (u *UICodes) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.purgeLocked(u.clock.Now())
	return len(u.codes)
}

func (u *UICodes) purgeLocked(now time.Time) {
	kept := u.codes[:0]
	for _, c := range u.codes {
		if now.Before(c.expires) {
			kept = append(kept, c)
		}
	}
	u.codes = kept
	sort.SliceStable(u.codes, func(i, j int) bool { return u.codes[i].minted.Before(u.codes[j].minted) })
}

func (u *UICodes) hasLocked(code string) bool {
	for _, c := range u.codes {
		if c.code == code {
			return true
		}
	}
	return false
}

// randomDigits returns n decimal digits, each drawn uniformly (rand.Int has no modulo bias), with
// leading zeros kept: "004821" is a valid code.
func randomDigits(n int) string {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil { // cannot happen: crypto/rand does not fail on a supported platform
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%0*d", n, v)
}

// validCodeFormat reports whether s is exactly UICodeDigits decimal digits.
func validCodeFormat(s string) bool { return isDigits(s, UICodeDigits, UICodeDigits) }

func isDigits(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
