package auth

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionCreateAndLookup(t *testing.T) {
	s := NewSessions(newClock())
	cookie, id := s.Create()
	if len(cookie) != 43 || strings.ContainsAny(cookie, "+/=") {
		t.Fatalf("cookie must be 43 base64url characters, got %d", len(cookie))
	}
	if id == cookie || strings.Contains(id, cookie) {
		t.Fatal("the internal id must not be the cookie")
	}
	got, state := s.Lookup(cookie)
	if state != SessionValid || got != id {
		t.Fatalf("Lookup = %q, %v", got, state)
	}
	other, _ := s.Create()
	if other == cookie {
		t.Error("two sessions share a cookie")
	}
	for _, bad := range []string{"", "nope", cookie + "x", cookie[:42]} {
		if _, st := s.Lookup(bad); st != SessionUnknown {
			t.Errorf("Lookup(%q) = %v, want unknown", bad, st)
		}
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d", s.Len())
	}
}

// every returns n equal steps.
func every(d time.Duration, n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d
	}
	return out
}

func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	const h = time.Hour
	tests := []struct {
		name  string
		steps []time.Duration // advance, then Lookup; the last step's state is checked
		want  SessionState
	}{
		{"idle just under 12h", []time.Duration{12*h - time.Second}, SessionValid},
		{"idle exactly 12h", []time.Duration{12 * h}, SessionExpired},
		{"idle over 12h", []time.Duration{13 * h}, SessionExpired},
		{"use restarts the idle timer", []time.Duration{11 * h, 11 * h, 11 * h}, SessionValid},
		{"idle after use", []time.Duration{11 * h, 12 * h}, SessionExpired},
		{"absolute: active every 6h until 6d18h", every(6*h, 27), SessionValid},
		{"absolute: just under 7d", append(every(6*h, 27), 6*h-time.Second), SessionValid},
		{"absolute: exactly 7d though never idle", every(6*h, 28), SessionExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newClock()
			s := NewSessions(clock)
			cookie, _ := s.Create()
			var state SessionState
			for _, d := range tt.steps {
				clock.Advance(d)
				_, state = s.Lookup(cookie)
				if state != SessionValid && state != tt.want {
					t.Fatalf("expired early at +%v", d)
				}
			}
			if state != tt.want {
				t.Fatalf("state = %v, want %v", state, tt.want)
			}
		})
	}
}

func TestExpiredSessionStaysKnownThenIsForgotten(t *testing.T) {
	clock := newClock()
	s := NewSessions(clock)
	cookie, id := s.Create()
	clock.Advance(13 * time.Hour)
	for i := 0; i < 3; i++ {
		if got, st := s.Lookup(cookie); st != SessionExpired || got != id {
			t.Fatalf("Lookup #%d = %q, %v; want expired (so the answer is LAI-SEC-012)", i, got, st)
		}
	}
	if s.Len() != 0 {
		t.Errorf("an expired session is not live: Len = %d", s.Len())
	}
	clock.Advance(25 * time.Hour)
	if _, st := s.Lookup(cookie); st != SessionUnknown {
		t.Errorf("after the tombstone time the session is unknown, got %v", st)
	}
}

func TestSessionDeleteAndDropAll(t *testing.T) {
	s := NewSessions(newClock())
	a, aid := s.Create()
	b, _ := s.Create()
	if !s.Delete(aid) || s.Delete(aid) {
		t.Error("Delete reports live once")
	}
	if _, st := s.Lookup(a); st != SessionUnknown {
		t.Error("a deleted session must be unknown")
	}
	if _, st := s.Lookup(b); st != SessionValid {
		t.Error("the other session must survive")
	}
	s.DropAll()
	if _, st := s.Lookup(b); st != SessionUnknown || s.Len() != 0 {
		t.Error("DropAll must end everything")
	}
}

func TestSessionStoreIsBounded(t *testing.T) {
	clock := newClock()
	s := NewSessions(clock)
	first, _ := s.Create()
	for i := 0; i < maxSessions+10; i++ {
		clock.Advance(time.Second)
		s.Create()
	}
	if s.Len() > maxSessions {
		t.Fatalf("Len = %d, over the cap %d", s.Len(), maxSessions)
	}
	if _, st := s.Lookup(first); st != SessionUnknown {
		t.Error("the least recently used session is evicted first")
	}
}

func TestSessionCookieHeaders(t *testing.T) {
	if got, want := SessionCookieHeader("abc"), "localai_session=abc; Path=/; HttpOnly; SameSite=Strict; Max-Age=604800"; got != want {
		t.Errorf("create header %q, want %q", got, want)
	}
	if got, want := ClearSessionCookieHeader(), "localai_session=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0"; got != want {
		t.Errorf("clear header %q, want %q", got, want)
	}
}

func TestSessionsConcurrentUse(t *testing.T) {
	clock := newClock()
	s := NewSessions(clock)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c, id := s.Create()
				if _, st := s.Lookup(c); st != SessionValid {
					t.Error("lost a session")
					return
				}
				s.Delete(id)
				clock.Advance(time.Second)
			}
		}()
	}
	wg.Wait()
}
