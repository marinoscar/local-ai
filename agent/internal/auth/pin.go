package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// The argon2id parameters of a new PIN hash (agent.md section 3.9.2): m = 64 MiB, t = 3, p = 1,
// a 16-byte salt and a 32-byte key.
const (
	PINMemoryKiB = 64 * 1024
	PINTime      = 3
	PINThreads   = 1
	PINSaltLen   = 16
	PINKeyLen    = 32

	// MinPINDigits and MaxPINDigits bound a valid PIN: 4 to 8 decimal digits.
	MinPINDigits = 4
	MaxPINDigits = 8

	// maxPINInput caps what is hashed for a malformed PIN, so a megabyte body costs one verification.
	maxPINInput = 64

	// Bounds on a stored hash's parameters. A file the agent did not write is not trusted to ask
	// for gigabytes or hours.
	maxPHCMemoryKiB = 1 << 20 // 1 GiB
	maxPHCTime      = 16
	maxPHCThreads   = 16
	minPHCSalt      = 8
	minPHCKey       = 16
	maxPHCKey       = 128
)

// deriveID is argon2.IDKey; a test replaces it to count how often the work is done.
var deriveID = argon2.IDKey

// ErrInvalidPIN is returned by HashPIN for a PIN that is not 4 to 8 digits.
var ErrInvalidPIN = errors.New("auth: a PIN is 4 to 8 digits")

// ValidPIN reports whether pin is 4 to 8 decimal digits.
func ValidPIN(pin string) bool { return isDigits(pin, MinPINDigits, MaxPINDigits) }

// HashPIN returns the PHC string of pin: $argon2id$v=19$m=65536,t=3,p=1$<salt>$<key>, with the
// salt and key in unpadded standard base64. It is shared with `aictl pin set`. It refuses a PIN
// that is not 4 to 8 digits.
func HashPIN(pin string) (string, error) {
	if !ValidPIN(pin) {
		return "", ErrInvalidPIN
	}
	salt := make([]byte, PINSaltLen)
	_, _ = rand.Read(salt)
	key := deriveID([]byte(pin), salt, PINTime, PINMemoryKiB, PINThreads, PINKeyLen)
	return formatPHC(phc{memory: PINMemoryKiB, time: PINTime, threads: PINThreads, salt: salt, key: key}), nil
}

// VerifyPIN reports whether pin matches the PHC string. A malformed PHC is an error; a wrong or
// malformed pin is (false, nil), after the same argon2 work as a right one.
func VerifyPIN(encoded, pin string) (bool, error) {
	h, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	return h.verify(pin), nil
}

type phc struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

func formatPHC(h phc) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, h.memory, h.time, h.threads,
		enc.EncodeToString(h.salt), enc.EncodeToString(h.key))
}

var errBadPHC = errors.New("auth: the PIN file does not hold an argon2id PHC string")

// parsePHC reads $argon2id$v=19$m=..,t=..,p=..$salt$key.
func parsePHC(s string) (phc, error) {
	parts := strings.Split(strings.TrimSpace(s), "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phc{}, errBadPHC
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return phc{}, fmt.Errorf("%w: unsupported version %q", errBadPHC, parts[2])
	}
	var h phc
	params := map[string]uint64{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return phc{}, errBadPHC
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return phc{}, errBadPHC
		}
		if _, dup := params[k]; dup {
			return phc{}, errBadPHC
		}
		params[k] = n
	}
	if len(params) != 3 {
		return phc{}, errBadPHC
	}
	m, okm := params["m"]
	t, okt := params["t"]
	p, okp := params["p"]
	if !okm || !okt || !okp || m < 8 || m > maxPHCMemoryKiB || t < 1 || t > maxPHCTime || p < 1 || p > maxPHCThreads {
		return phc{}, fmt.Errorf("%w: parameters out of range", errBadPHC)
	}
	h.memory, h.time, h.threads = uint32(m), uint32(t), uint8(p)
	var err error
	if h.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(h.salt) < minPHCSalt {
		return phc{}, errBadPHC
	}
	if h.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(h.key) < minPHCKey || len(h.key) > maxPHCKey {
		return phc{}, errBadPHC
	}
	return h, nil
}

// verify runs argon2id for pin whatever its shape, then compares in constant time. A malformed pin
// (wrong length, not digits) is hashed too, truncated, and never matches: no fast path, so the time
// taken does not tell a malformed PIN from a wrong one.
func (h phc) verify(pin string) bool {
	input := pin
	if len(input) > maxPINInput {
		input = input[:maxPINInput]
	}
	got := deriveID([]byte(input), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
	match := subtle.ConstantTimeCompare(got, h.key) == 1
	return match && ValidPIN(pin)
}

// PIN outcomes of PIN.Verify other than success.
var (
	// ErrPINNotSet: /etc/localai/admin-pin is absent, empty, not a PHC string or not a regular file
	// (409 LAI-SEC-005; `sudo aictl pin set` fixes all four).
	ErrPINNotSet = errors.New("auth: no admin PIN is set")
)

// WrongPINError is a failed verification: 401 LAI-SEC-003 with details.remaining_attempts.
type WrongPINError struct{ Remaining int }

func (e *WrongPINError) Error() string { return "auth: wrong PIN" }

// RateLimitedError is a refused verification: 429 LAI-SEC-004 with Retry-After. Locked is true for
// the one-hour lockout, false for the 5-minute window; Started is true on the very request whose
// wrong PIN started the lockout.
type RateLimitedError struct {
	RetryAfter time.Duration
	Locked     bool
	Started    bool
}

func (e *RateLimitedError) Error() string { return "auth: too many wrong PIN attempts" }

// AlertFunc raises an alert (agent.md section 4.13; AGENT-11 provides the manager). severity is
// "critical" for a PIN lockout.
type AlertFunc func(code, severity string, details map[string]any)

// PIN verifies the admin PIN against /etc/localai/admin-pin. The file is read on every verification
// (an `aictl pin set` applies at once and nothing is cached), its permissions are enforced each
// time, and verifications run one at a time: that bounds the memory argon2 takes and makes the
// rate limit exact.
type PIN struct {
	fs     config.FS
	path   string
	policy config.SecretPolicy
	clock  Clock
	log    *slog.Logger
	alert  AlertFunc

	mu      sync.Mutex
	limiter *pinLimiter
}

// NewPIN returns a verifier for path. alert may be nil.
func NewPIN(fsys config.FS, path string, policy config.SecretPolicy, clock Clock, log *slog.Logger, alert AlertFunc) *PIN {
	if fsys == nil {
		fsys = config.OSFS{}
	}
	if clock == nil {
		clock = RealClock{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &PIN{fs: fsys, path: path, policy: policy, clock: clock, log: log, alert: alert, limiter: newPINLimiter()}
}

// Verify checks pin. nil means it is right. Otherwise the error is *RateLimitedError (the limiter
// refused before any work, even a right PIN), ErrPINNotSet, or *WrongPINError.
func (p *PIN) Verify(pin string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	if wait, locked, blocked := p.limiter.check(now); blocked {
		return &RateLimitedError{RetryAfter: wait, Locked: locked}
	}
	h, err := p.load()
	if err != nil {
		return err
	}
	if h.verify(pin) {
		p.limiter.success()
		return nil
	}
	remaining, lockedNow := p.limiter.fail(now)
	if lockedNow {
		p.log.Error("the admin PIN is locked after too many wrong attempts", "code", CodeRateLimited, "severity", "critical", "locked_for_s", int(LockoutFor/time.Second))
		if p.alert != nil {
			p.alert(CodeRateLimited, "critical", map[string]any{"locked_for_s": int(LockoutFor / time.Second)})
		}
		return &RateLimitedError{RetryAfter: LockoutFor, Locked: true, Started: true}
	}
	return &WrongPINError{Remaining: remaining}
}

// load reads and parses the PIN file, fixing its permissions first (LAI-SEC-009 is logged).
func (p *PIN) load() (phc, error) {
	issues, err := config.EnforceSecret(p.fs, p.path, p.policy)
	for _, is := range issues {
		p.log.Warn("secret file had unsafe permissions", "code", CodeSecretPermissions, "path", is.Path, "issue", is.Message)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return phc{}, ErrPINNotSet
		}
		return phc{}, p.notSet("cannot check or fix the PIN file", err)
	}
	sf, err := config.ReadSecretFile(p.fs, p.path, p.policy)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return phc{}, ErrPINNotSet
		}
		return phc{}, p.notSet("cannot read the PIN file", err)
	}
	h, err := parsePHC(string(sf.Data))
	if err != nil {
		return phc{}, p.notSet("the PIN file is not usable", err)
	}
	return h, nil
}

func (p *PIN) notSet(msg string, err error) error {
	p.log.Error(msg, "error", err.Error())
	return ErrPINNotSet
}
