package auth

import (
	"strings"
	"testing"
	"time"
)

func TestUICodeFormat(t *testing.T) {
	u := NewUICodes(newClock())
	leadingZero := false
	digits := map[byte]int{}
	for i := 0; i < 3000; i++ {
		code, exp := u.Mint()
		if len(code) != 6 || !isDigits(code, 6, 6) {
			t.Fatalf("code %q is not six digits", code)
		}
		if !exp.Equal(t0.Add(5 * time.Minute)) {
			t.Fatalf("expires %v, want 5 minutes after the mint", exp)
		}
		leadingZero = leadingZero || code[0] == '0'
		for j := 0; j < len(code); j++ {
			digits[code[j]]++
		}
		u.Consume(code) // keep the store small
	}
	if !leadingZero {
		t.Error("a code may start with 0: 3000 draws never did")
	}
	// 18000 digits, 1800 expected each; a modulo bias or a missing digit would show.
	for d := byte('0'); d <= '9'; d++ {
		if n := digits[d]; n < 1500 || n > 2100 {
			t.Errorf("digit %c appeared %d times of 18000", d, n)
		}
	}
}

func TestUICodeIsSingleUse(t *testing.T) {
	u := NewUICodes(newClock())
	code, _ := u.Mint()
	if !u.Consume(code) {
		t.Fatal("a fresh code must redeem")
	}
	if u.Consume(code) {
		t.Fatal("a code redeems once")
	}
	if u.Len() != 0 {
		t.Errorf("Len = %d", u.Len())
	}
}

func TestUICodeExpiry(t *testing.T) {
	tests := []struct {
		after time.Duration
		want  bool
	}{
		{0, true},
		{4*time.Minute + 59*time.Second, true},
		{5 * time.Minute, false},
		{time.Hour, false},
	}
	for _, tt := range tests {
		clock := newClock()
		u := NewUICodes(clock)
		code, _ := u.Mint()
		clock.Advance(tt.after)
		if got := u.Consume(code); got != tt.want {
			t.Errorf("after %v: Consume = %v, want %v", tt.after, got, tt.want)
		}
	}
}

func TestFourthCodeEvictsTheOldest(t *testing.T) {
	clock := newClock()
	u := NewUICodes(clock)
	var codes []string
	for i := 0; i < 4; i++ {
		c, _ := u.Mint()
		codes = append(codes, c)
		clock.Advance(time.Second)
	}
	if u.Len() != 3 {
		t.Fatalf("Len = %d, want 3 alive", u.Len())
	}
	if u.Consume(codes[0]) {
		t.Error("the oldest code must have been invalidated")
	}
	for i := 1; i < 4; i++ {
		if !u.Consume(codes[i]) {
			t.Errorf("code %d must still work", i)
		}
	}
}

func TestExpiredCodesDoNotCountTowardTheCap(t *testing.T) {
	clock := newClock()
	u := NewUICodes(clock)
	for i := 0; i < 3; i++ {
		u.Mint()
	}
	clock.Advance(6 * time.Minute)
	fresh, _ := u.Mint()
	if u.Len() != 1 || !u.Consume(fresh) {
		t.Fatalf("Len = %d; the three expired codes must be gone", u.Len())
	}
}

func TestWrongOrMalformedCodeNeverRedeems(t *testing.T) {
	u := NewUICodes(newClock())
	code, _ := u.Mint()
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for _, in := range []string{wrong, "", "12345", code + "0", " " + code, strings.Repeat("9", 1000)} {
		if u.Consume(in) {
			t.Errorf("Consume(%q) must be false", in)
		}
	}
	if !u.Consume(code) {
		t.Error("failed guesses must not burn the real code")
	}
}

func TestValidCodeFormat(t *testing.T) {
	for in, want := range map[string]bool{"482913": true, "000000": true, "": false, "48291": false, "4829133": false, "48291a": false, "48 913": false, "４８２９１３": false} {
		if got := validCodeFormat(in); got != want {
			t.Errorf("validCodeFormat(%q) = %v, want %v", in, got, want)
		}
	}
}
