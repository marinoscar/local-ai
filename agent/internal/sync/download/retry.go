package download

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The doctor codes of the downloader, aliased from the single table (doctor/codes.go). Result.Code
// and Error.Code carry one of these; a code that is only information (CodeRestart, CodePurged)
// appears in Result.Notices instead.
const (
	// CodeChecksum: the streamed digest differs from the expected sha256 (quarantine).
	CodeChecksum = doctor.SYN020
	// CodeSize: the downloaded size differs from the expected size (quarantine).
	CodeSize = doctor.SYN027
	// CodeRefused: the server refused or no longer has the file (permanent).
	CodeRefused = doctor.SYN030
	// CodeRetries: the retries are exhausted, or the server asked for too long a wait.
	CodeRetries = doctor.SYN031
	// CodeRestart: the server does not support resume; the file restarted from zero (information).
	CodeRestart = doctor.SYN032
	// CodeHFLogin: Hugging Face needs a login or is rate-limiting.
	CodeHFLogin = doctor.SYN034
	// CodeDisk: not enough free disk space (the item is skipped, the .part kept).
	CodeDisk = doctor.SYN040
	// CodePurged: old versions were deleted to make room (information, raised by the caller).
	CodePurged = doctor.SYN043
	// CodeSourceDamaged: the copy a local copy reads from is missing, unreadable or does not match
	// its checksum.
	CodeSourceDamaged = doctor.SYN070
	// CodeIO: the drive reported read/write errors.
	CodeIO = doctor.ARK014
	// CodeReadOnly: the drive turned read-only.
	CodeReadOnly = doctor.ARK003
	// CodeNoWritableRoot: the agent may not write to the content root.
	CodeNoWritableRoot = doctor.ARK001
)

// Kind classifies why a Fetch ended without a verified file.
type Kind string

// The kinds.
const (
	// KindDisk: not enough free space; the item is "skipped: disk" and the .part is kept.
	KindDisk Kind = "disk"
	// KindMismatch: the bytes are not the expected ones (size or sha256); the caller quarantines the
	// .part and the next run starts the file from scratch.
	KindMismatch Kind = "mismatch"
	// KindPermanent: retrying will not help this run (404, 403, a refused redirect, a bad
	// certificate); the caller may re-resolve once.
	KindPermanent Kind = "permanent"
	// KindExhausted: transient errors outlasted the retries; the .part and checkpoint are kept and
	// the next run resumes.
	KindExhausted Kind = "exhausted"
	// KindRootFault: the root cannot be written (EIO, EROFS, denied); all writes to it stop.
	KindRootFault Kind = "root_fault"
)

// Error is the error a Fetch returns when it ends without a verified file and was not cancelled.
type Error struct {
	Kind Kind
	// Code is the doctor code of the condition.
	Code string
	// Permanent: retrying in this run is pointless (see KindPermanent).
	Permanent bool
	// Err is the cause, with URLs reduced to scheme, host and path.
	Err error
}

// Sentinels for errors.Is: errors.Is(err, download.ErrDisk) is true for any KindDisk error.
var (
	ErrDisk      = &Error{Kind: KindDisk}
	ErrMismatch  = &Error{Kind: KindMismatch}
	ErrPermanent = &Error{Kind: KindPermanent}
	ErrExhausted = &Error{Kind: KindExhausted}
	ErrRootFault = &Error{Kind: KindRootFault}
)

// Error implements error.
func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Is matches the sentinels by Kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == "" && t.Err == nil && t.Kind == e.Kind
}

func newError(kind Kind, code string, format string, args ...any) *Error {
	return &Error{Kind: kind, Code: code, Permanent: kind == KindPermanent || kind == KindMismatch, Err: fmt.Errorf(format, args...)}
}

// retryable is the internal "this attempt failed, try again" error.
type retryable struct {
	err error
	// retryAfter is the raw Retry-After header of a 429/503, "" when there was none.
	retryAfter string
	// hf429: a Hugging Face rate limit, whose exhausted code is CodeHFLogin.
	hf429 bool
	// restart: the next attempt starts from byte 0 (the server answered inconsistently).
	restart bool
}

func (r *retryable) Error() string { return r.err.Error() }
func (r *retryable) Unwrap() error { return r.err }

// The retry policy (ark-sync.md section 4.8).
const (
	// MaxRetryAfter is the longest Retry-After honoured; a longer one fails the file for this run.
	MaxRetryAfter = 15 * time.Minute
	// ProgressReset is the progress since the previous failure that resets the backoff to 5 s.
	ProgressReset int64 = 256 << 20
	// DefaultJitter is the +/- fraction applied to every backoff.
	DefaultJitter = 0.20
)

// DefaultBackoff is the wait before each retry: 5 s, 15 s, 45 s, 2 min, 5 min, 10 min. Six retries
// make seven attempts.
var DefaultBackoff = []time.Duration{
	5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
}

// Backoff is the capped exponential schedule with jitter.
type Backoff struct {
	// Steps are the waits before retry 1, 2, ...; the number of retries is len(Steps). Default
	// DefaultBackoff.
	Steps []time.Duration
	// Jitter is the +/- fraction (0.2 = plus or minus 20 %); default DefaultJitter. A negative
	// value means none.
	Jitter float64
	// Rand returns a number in [0, 1); default math/rand/v2. Tests pass a fixed one.
	Rand func() float64
}

func (b Backoff) steps() []time.Duration {
	if len(b.Steps) == 0 {
		return DefaultBackoff
	}
	return b.Steps
}

// Retries is how many retries the schedule allows.
func (b Backoff) Retries() int { return len(b.steps()) }

// Delay is the wait before retry number retry (0 for the first), with jitter. It is never negative.
func (b Backoff) Delay(retry int) time.Duration {
	steps := b.steps()
	d := steps[min(max(retry, 0), len(steps)-1)]
	j := b.Jitter
	if j == 0 {
		j = DefaultJitter
	}
	if j < 0 {
		return d
	}
	r := rand.Float64
	if b.Rand != nil {
		r = b.Rand
	}
	f := 1 + j*(2*r()-1)
	return max(time.Duration(float64(d)*f), 0)
}

// ParseRetryAfter reads a Retry-After value: a number of seconds or an HTTP date (relative to now).
// ok is false for anything else. A date in the past is a zero wait.
func ParseRetryAfter(v string, now time.Time) (d time.Duration, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > int64(24*time.Hour/time.Second)*365 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// isCtx reports whether err is the context's own error.
func isCtx(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
