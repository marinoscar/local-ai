package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

func TestGrantFormatAndBinding(t *testing.T) {
	g := NewGrants(newClock())
	tok, exp := g.Mint("session:abc")
	if !strings.HasPrefix(tok, "pg_") || len(tok) != 3+43 {
		t.Fatalf("grant %q: want pg_ + 43 characters", tok[:6])
	}
	if !exp.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("expires %v", exp)
	}
	if !g.Valid(tok, "session:abc") {
		t.Fatal("the grant is valid for the principal that earned it")
	}
	for _, other := range []string{"session:abd", "bearer:" + "lat_…abcd", "uid:0", "", "session:abc "} {
		if g.Valid(tok, other) {
			t.Errorf("a grant from session:abc must be rejected for %q", other)
		}
	}
}

func TestGrantIsReusableUntilItExpires(t *testing.T) {
	clock := newClock()
	g := NewGrants(clock)
	tok, _ := g.Mint("uid:1000")
	for i := 0; i < 3; i++ {
		clock.Advance(time.Minute)
		if !g.Valid(tok, "uid:1000") {
			t.Fatalf("use #%d: a grant is reusable", i+1)
		}
	}
	clock.Advance(time.Minute + 59*time.Second) // 3m + 1m59s = 4m59s
	if !g.Valid(tok, "uid:1000") {
		t.Fatal("valid just under five minutes (fixed, not sliding)")
	}
	clock.Advance(time.Second)
	if g.Valid(tok, "uid:1000") {
		t.Fatal("expired at five minutes")
	}
	if g.Len() != 0 {
		t.Errorf("Len = %d", g.Len())
	}
}

func TestGrantRejectsJunk(t *testing.T) {
	g := NewGrants(newClock())
	tok, _ := g.Mint("uid:1000")
	for _, in := range []string{"", "pg_", "pg_nope", tok + "x", tok[:len(tok)-1], strings.TrimPrefix(tok, "pg_"), strings.ToUpper(tok)} {
		if g.Valid(in, "uid:1000") {
			t.Errorf("Valid(%q) must be false", in)
		}
	}
	if g.Valid(tok, "") {
		t.Error("an empty binding never matches")
	}
}

func TestGrantRevocation(t *testing.T) {
	g := NewGrants(newClock())
	a, _ := g.Mint("session:a")
	b, _ := g.Mint("session:b")
	c, _ := g.Mint("bearer:x")
	g.DropBinding("session:a")
	if g.Valid(a, "session:a") || !g.Valid(b, "session:b") {
		t.Error("DropBinding revokes that principal's grants only")
	}
	g.DropPrefix("bearer:")
	if g.Valid(c, "bearer:x") || !g.Valid(b, "session:b") {
		t.Error("DropPrefix revokes the bearer grants only")
	}
	g.DropAll()
	if g.Valid(b, "session:b") || g.Len() != 0 {
		t.Error("DropAll revokes everything")
	}
}

func TestGrantStoreIsBounded(t *testing.T) {
	clock := newClock()
	g := NewGrants(clock)
	first, _ := g.Mint("uid:1")
	for i := 0; i < maxGrants+20; i++ {
		clock.Advance(time.Millisecond)
		g.Mint("uid:1")
	}
	if g.Len() > maxGrants {
		t.Fatalf("Len = %d over the cap", g.Len())
	}
	if g.Valid(first, "uid:1") {
		t.Error("the soonest to expire goes first")
	}
}

func TestBinding(t *testing.T) {
	tests := []struct {
		name string
		p    api.Principal
		want string
	}{
		{"session", api.Principal{Kind: api.KindOperator, Credential: api.CredSession, SessionID: "abc"}, "session:abc"},
		{"session without id", api.Principal{Kind: api.KindOperator, Credential: api.CredSession}, ""},
		{"bearer", api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, TokenFingerprint: "lat_…9xQz"}, "bearer:lat_…9xQz"},
		{"bearer without fingerprint", api.Principal{Kind: api.KindOperator, Credential: api.CredBearer}, ""},
		{"socket", api.Principal{Kind: api.KindOperator, Credential: api.CredSocket, UID: 1000}, "uid:1000"},
		{"socket root", api.Principal{Kind: api.KindAdmin, Credential: api.CredSocket, UID: 0}, "uid:0"},
		{"portal", api.Principal{Kind: api.KindPortal, Credential: api.CredPortal, PortalSessionID: "p"}, ""},
		{"anonymous", api.Anonymous(), ""},
		{"lifted by a grant keeps the credential's binding", api.Principal{Kind: api.KindAdmin, Via: "pin", Credential: api.CredSession, SessionID: "abc"}, "session:abc"},
	}
	for _, tt := range tests {
		if got := Binding(tt.p); got != tt.want {
			t.Errorf("%s: Binding = %q, want %q", tt.name, got, tt.want)
		}
	}
}
