package auth

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

// cheapPHC is the PHC string of pin with minimal cost (m = 8 KiB, t = 1), so a test can verify
// dozens of times; the cost is read from the string, as in production.
func cheapPHC(pin string) string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(pin), salt, 1, 8, 1, 32)
	return formatPHC(phc{memory: 8, time: 1, threads: 1, salt: salt, key: key})
}

type pinEnv struct {
	pin    *PIN
	path   string
	clock  *cfake.Clock
	logs   *syncBuf
	mu     sync.Mutex
	alerts []string
}

func newPINEnv(t *testing.T) *pinEnv {
	t.Helper()
	_, path := tempPaths(t)
	e := &pinEnv{path: path, clock: newClock()}
	lg, buf := newLogger()
	e.logs = buf
	e.pin = NewPIN(nil, path, ownPolicy(0o600), e.clock, lg, func(code, severity string, _ map[string]any) {
		e.mu.Lock()
		e.alerts = append(e.alerts, code+"/"+severity)
		e.mu.Unlock()
	})
	return e
}

func (e *pinEnv) set(t *testing.T, pin string) {
	t.Helper()
	writeFile(t, e.path, cheapPHC(pin)+"\n", 0o600)
}

// countArgon counts the argon2 computations until the test ends.
func countArgon(t *testing.T) *int {
	t.Helper()
	n := new(int)
	orig := deriveID
	deriveID = func(pw, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
		*n++
		return orig(pw, salt, time, memory, threads, keyLen)
	}
	t.Cleanup(func() { deriveID = orig })
	return n
}

func TestHashPINUsesTheSpecifiedParameters(t *testing.T) {
	enc, err := HashPIN("4821")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("PHC string %q has the wrong header", enc[:strings.LastIndex(enc, "$")])
	}
	h, err := parsePHC(enc)
	if err != nil {
		t.Fatal(err)
	}
	if h.memory != 64*1024 || h.time != 3 || h.threads != 1 || len(h.salt) != 16 || len(h.key) != 32 {
		t.Fatalf("parsed m=%d t=%d p=%d salt=%d key=%d; want 65536/3/1/16/32", h.memory, h.time, h.threads, len(h.salt), len(h.key))
	}
	// Independently derived: the key is argon2id of the PIN with exactly these parameters.
	if want := argon2.IDKey([]byte("4821"), h.salt, 3, 64*1024, 1, 32); string(want) != string(h.key) {
		t.Error("the key is not argon2id(m=64 MiB, t=3, p=1)")
	}
	parts := strings.Split(enc, "$")
	for _, p := range parts[4:] {
		if strings.ContainsAny(p, "=") {
			t.Errorf("salt and key are unpadded base64: %q", p)
		}
		if _, err := base64.RawStdEncoding.DecodeString(p); err != nil {
			t.Errorf("not base64: %v", err)
		}
	}
	if ok, err := VerifyPIN(enc, "4821"); err != nil || !ok {
		t.Errorf("VerifyPIN right = %v, %v", ok, err)
	}
	if ok, err := VerifyPIN(enc, "4822"); err != nil || ok {
		t.Errorf("VerifyPIN wrong = %v, %v", ok, err)
	}
	other, _ := HashPIN("4821")
	if other == enc {
		t.Error("two hashes of one PIN must differ (random salt)")
	}
}

func TestHashPINRefusesAnInvalidPIN(t *testing.T) {
	for _, pin := range []string{"", "1", "123", "123456789", "12a4", "12 34", "１２３４", "-1234", "12.34"} {
		if _, err := HashPIN(pin); !errors.Is(err, ErrInvalidPIN) {
			t.Errorf("HashPIN(%q) err = %v, want ErrInvalidPIN", pin, err)
		}
	}
	for _, pin := range []string{"1234", "00000", "123456", "1234567", "12345678"} {
		if !ValidPIN(pin) {
			t.Errorf("ValidPIN(%q) = false", pin)
		}
	}
}

func TestParsePHCRejections(t *testing.T) {
	good := cheapPHC("1234")
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	tests := map[string]string{
		"empty":            "",
		"garbage":          "not a hash",
		"argon2i":          strings.Replace(good, "argon2id", "argon2i", 1),
		"argon2d":          strings.Replace(good, "argon2id", "argon2d", 1),
		"old version":      strings.Replace(good, "v=19", "v=16", 1),
		"no version":       strings.Replace(good, "$v=19", "", 1),
		"missing t":        "$argon2id$v=19$m=8,p=1$" + salt + "$" + key,
		"missing p":        "$argon2id$v=19$m=8,t=1$" + salt + "$" + key,
		"duplicate m":      "$argon2id$v=19$m=8,m=8,t=1$" + salt + "$" + key,
		"extra param":      "$argon2id$v=19$m=8,t=1,p=1,x=1$" + salt + "$" + key,
		"non-numeric":      "$argon2id$v=19$m=big,t=1,p=1$" + salt + "$" + key,
		"zero time":        "$argon2id$v=19$m=8,t=0,p=1$" + salt + "$" + key,
		"zero threads":     "$argon2id$v=19$m=8,t=1,p=0$" + salt + "$" + key,
		"too many threads": "$argon2id$v=19$m=8,t=1,p=99$" + salt + "$" + key,
		"too much time":    "$argon2id$v=19$m=8,t=999,p=1$" + salt + "$" + key,
		"too much memory":  "$argon2id$v=19$m=4194304,t=1,p=1$" + salt + "$" + key,
		"too little mem":   "$argon2id$v=19$m=1,t=1,p=1$" + salt + "$" + key,
		"bad salt":         "$argon2id$v=19$m=8,t=1,p=1$!!!$" + key,
		"short salt":       "$argon2id$v=19$m=8,t=1,p=1$" + base64.RawStdEncoding.EncodeToString([]byte("abc")) + "$" + key,
		"short key":        "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString([]byte("abc")),
		"long key":         "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 300)),
		"extra field":      good + "$extra",
		"padded base64":    "$argon2id$v=19$m=8,t=1,p=1$" + base64.StdEncoding.EncodeToString([]byte("0123456789abcd")) + "$" + key,
	}
	for name, enc := range tests {
		if _, err := parsePHC(enc); err == nil {
			t.Errorf("%s: parsePHC accepted %q", name, enc)
		}
		if _, err := VerifyPIN(enc, "1234"); err == nil {
			t.Errorf("%s: VerifyPIN accepted it", name)
		}
	}
	// Any order of the three parameters, and a trailing newline, are fine.
	reordered := "$argon2id$v=19$p=1,t=1,m=8$" + salt + "$" + key + "\n"
	if _, err := parsePHC(reordered); err != nil {
		t.Errorf("reordered parameters: %v", err)
	}
}

func TestVerifyRightAndWrongPIN(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatalf("right PIN: %v", err)
	}
	var wrong *WrongPINError
	if err := e.pin.Verify("4822"); !errors.As(err, &wrong) || wrong.Remaining != 4 {
		t.Fatalf("wrong PIN: %v", err)
	}
}

func TestMalformedPINStillCostsTheArgon2Work(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	n := countArgon(t)
	inputs := []string{"", "1", "12", "abcd", "48 21", "4821 ", " 4821", "4821\n", "123456789", strings.Repeat("9", 100000), "４８２１", "\x00\x00\x00\x00"}
	for i, in := range inputs {
		before := *n
		var wrong *WrongPINError
		if err := e.pin.Verify(in); !errors.As(err, &wrong) {
			t.Fatalf("input %d: %v, want a wrong-PIN error", i, err)
		}
		if *n != before+1 {
			t.Errorf("input %d: %d argon2 computations, want exactly 1 (no fast path)", i, *n-before)
		}
		e.clock.Advance(6 * time.Minute) // stay clear of the limiter
	}
	// A right PIN is one computation too.
	before := *n
	if err := e.pin.Verify("4821"); err != nil || *n != before+1 {
		t.Errorf("right PIN: err %v, %d computations", err, *n-before)
	}
}

func TestMalformedPINNeverMatchesEvenIfTheHashDoes(t *testing.T) {
	// A hash of a 2-digit string written by hand into the file must not make "12" a valid PIN.
	_, path := tempPaths(t)
	writeFile(t, path, cheapPHC("12")+"\n", 0o600)
	lg, _ := newLogger()
	p := NewPIN(nil, path, ownPolicy(0o600), newClock(), lg, nil)
	if err := p.Verify("12"); err == nil {
		t.Error("a PIN shorter than 4 digits must never verify")
	}
}

func TestSixthAttemptInTheWindowIs429EvenWithTheRightPIN(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	for i := 0; i < 5; i++ {
		var wrong *WrongPINError
		err := e.pin.Verify("0000")
		if !errors.As(err, &wrong) || wrong.Remaining != 4-i {
			t.Fatalf("attempt %d: %v (remaining %v)", i+1, err, wrong)
		}
		e.clock.Advance(10 * time.Second)
	}
	n := countArgon(t)
	var rl *RateLimitedError
	err := e.pin.Verify("4821")
	if !errors.As(err, &rl) || rl.Locked || rl.Started {
		t.Fatalf("sixth attempt: %v", err)
	}
	if want := 5*time.Minute - 50*time.Second; rl.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v (until the oldest failure ages out)", rl.RetryAfter, want)
	}
	if *n != 0 {
		t.Error("a refused attempt must not do argon2 work")
	}
	e.clock.Advance(rl.RetryAfter)
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatalf("once the window frees, the right PIN works: %v", err)
	}
}

func TestTwentyConsecutiveFailuresLockForAnHourAndAlert(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	var last error
	for round := 0; round < 4; round++ {
		for i := 0; i < 5; i++ {
			last = e.pin.Verify("0000")
		}
		if round < 3 {
			e.clock.Advance(5*time.Minute + time.Second)
		}
	}
	var rl *RateLimitedError
	if !errors.As(last, &rl) || !rl.Locked || !rl.Started || rl.RetryAfter != time.Hour {
		t.Fatalf("20th failure: %v", last)
	}
	if len(e.alerts) != 1 || e.alerts[0] != "LAI-SEC-004/critical" {
		t.Fatalf("alerts = %v, want one LAI-SEC-004/critical", e.alerts)
	}
	if !strings.Contains(e.logs.String(), `"severity":"critical"`) {
		t.Error("the lockout must be logged as critical")
	}
	// Locked: even the right PIN is refused, and Started is false on later refusals.
	e.clock.Advance(59 * time.Minute)
	if err := e.pin.Verify("4821"); !errors.As(err, &rl) || !rl.Locked || rl.Started || rl.RetryAfter != time.Minute {
		t.Fatalf("during the lockout: %v", err)
	}
	e.clock.Advance(time.Minute)
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	if len(e.alerts) != 1 {
		t.Errorf("alerts = %v", e.alerts)
	}
}

func TestSuccessResetsTheConsecutiveCounter(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	fail := func(n int) (last error) {
		for i := 0; i < n; i++ {
			last = e.pin.Verify("0000")
			e.clock.Advance(5*time.Minute + time.Second)
		}
		return last
	}
	if err := fail(19); errors.Is(err, nil) || func() bool { var r *RateLimitedError; return errors.As(err, &r) }() {
		t.Fatalf("19 failures: %v", err)
	}
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatal(err)
	}
	if err := fail(19); func() bool { var r *RateLimitedError; return errors.As(err, &r) }() {
		t.Fatalf("a success must have reset the count: %v", err)
	}
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatal(err)
	}
	if len(e.alerts) != 0 {
		t.Errorf("alerts = %v", e.alerts)
	}
}

func TestNoPINFileIsNotAFailure(t *testing.T) {
	e := newPINEnv(t)
	n := countArgon(t)
	for i := 0; i < 20; i++ {
		if err := e.pin.Verify("4821"); !errors.Is(err, ErrPINNotSet) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if *n != 0 {
		t.Error("no PIN file, no work")
	}
	e.set(t, "4821")
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatalf("a PIN set later works at once, the limiter untouched: %v", err)
	}
}

func TestUnusablePINFilesAreNotSet(t *testing.T) {
	e := newPINEnv(t)
	cases := map[string]func(){
		"empty":   func() { writeFile(t, e.path, "", 0o600) },
		"garbage": func() { writeFile(t, e.path, "hunter2\n", 0o600) },
		"wrong algorithm": func() {
			writeFile(t, e.path, strings.Replace(cheapPHC("1234"), "argon2id", "argon2i", 1), 0o600)
		},
		"symlink": func() {
			_ = os.Remove(e.path)
			target := filepath.Join(filepath.Dir(e.path), "real")
			writeFile(t, target, cheapPHC("1234"), 0o600)
			if err := os.Symlink(target, e.path); err != nil {
				t.Skip("symlinks not available")
			}
		},
		"directory": func() { _ = os.Remove(e.path); _ = os.Mkdir(e.path, 0o700) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			_ = os.RemoveAll(e.path)
			setup()
			if err := e.pin.Verify("1234"); !errors.Is(err, ErrPINNotSet) {
				t.Fatalf("Verify = %v, want ErrPINNotSet (409 LAI-SEC-005: `sudo aictl pin set` fixes it)", err)
			}
		})
	}
}

func TestPINFileIsReadOnEveryVerify(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "1111")
	if err := e.pin.Verify("1111"); err != nil {
		t.Fatal(err)
	}
	e.set(t, "2222") // `aictl pin set`
	var wrong *WrongPINError
	if err := e.pin.Verify("1111"); !errors.As(err, &wrong) {
		t.Fatalf("the old PIN must stop working at once: %v", err)
	}
	e.clock.Advance(6 * time.Minute)
	if err := e.pin.Verify("2222"); err != nil {
		t.Fatalf("the new PIN must work at once: %v", err)
	}
	_ = os.Remove(e.path)
	if err := e.pin.Verify("2222"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("a removed file means no PIN: %v", err)
	}
}

func TestPINFilePermissionsAreEnforced(t *testing.T) {
	e := newPINEnv(t)
	writeFile(t, e.path, cheapPHC("4821")+"\n", 0o644)
	if err := e.pin.Verify("4821"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(e.path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o after Verify, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(e.logs.String(), "LAI-SEC-009") {
		t.Error("the fix must be reported as LAI-SEC-009")
	}
}

func TestPINNeverReachesTheLog(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	_ = e.pin.Verify("7777")
	_ = e.pin.Verify("4821")
	hash, _ := os.ReadFile(e.path)
	mustNotContain(t, "log", e.logs.String(), "7777", "4821", strings.TrimSpace(string(hash)))
}

func TestPINVerificationsRunOneAtATime(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	var running, peak int
	var mu sync.Mutex
	orig := deriveID
	deriveID = func(pw, salt []byte, tm, mem uint32, th uint8, kl uint32) []byte {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		defer func() { mu.Lock(); running--; mu.Unlock() }()
		return orig(pw, salt, tm, mem, th, kl)
	}
	t.Cleanup(func() { deriveID = orig })
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.pin.Verify("4821") }()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("%d verifications overlapped; argon2 memory must be bounded to one at a time", peak)
	}
}

func TestParallelWrongGuessesCannotBeatTheLimit(t *testing.T) {
	e := newPINEnv(t)
	e.set(t, "4821")
	n := countArgon(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.pin.Verify("0000") }()
	}
	wg.Wait()
	if *n != 5 {
		t.Errorf("%d guesses were evaluated, want exactly 5 in the window", *n)
	}
}
