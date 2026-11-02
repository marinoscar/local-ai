package auth

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func newTokenStore(t *testing.T, fsys config.FS) (*TokenStore, string, *cfake.Clock, *syncBuf) {
	t.Helper()
	path, _ := tempPaths(t)
	clock := newClock()
	log, buf := newLogger()
	return NewTokenStore(fsys, path, ownPolicy(0o640), clock, log), path, clock, buf
}

func TestGenerateTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok := GenerateToken()
		if !strings.HasPrefix(tok, "lat_") || len(tok) != 4+43 {
			t.Fatalf("token %q: want lat_ + 43 base64url characters", TokenFingerprint(tok))
		}
		for _, c := range tok[4:] {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", c) {
				t.Fatalf("token has a non-base64url character %q", c)
			}
		}
		if seen[tok] {
			t.Fatal("two generated tokens are equal")
		}
		seen[tok] = true
	}
}

func TestTokenFingerprintHidesTheToken(t *testing.T) {
	tok := GenerateToken()
	fp := TokenFingerprint(tok)
	if !strings.HasPrefix(fp, "lat_…") || !strings.HasSuffix(fp, tok[len(tok)-4:]) || strings.Contains(fp, tok[4:len(tok)-4]) {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestEnsureCreatesAMissingTokenFile(t *testing.T) {
	s, path, _, logs := newTokenStore(t, nil)
	created, err := s.Ensure()
	if err != nil || !created {
		t.Fatalf("Ensure() = %v, %v; want a created token", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSuffix(string(data), "\n")
	if !strings.HasPrefix(tok, "lat_") || len(tok) != 47 || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("the file holds %d bytes; want lat_ + 43 characters and a newline", len(data))
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %04o, want 0640", info.Mode().Perm())
	}
	if !s.Verify(tok) {
		t.Error("the new token does not verify")
	}
	out := logs.String()
	if !strings.Contains(out, "LAI-SEC-011") {
		t.Errorf("no LAI-SEC-011 in the log: %s", out)
	}
	mustNotContain(t, "log", out, tok, tok[4:])
}

func TestEnsureRegeneratesAnEmptyFile(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "newline": "\n", "spaces": "  \r\n"} {
		t.Run(name, func(t *testing.T) {
			s, path, _, _ := newTokenStore(t, nil)
			writeFile(t, path, content, 0o640)
			created, err := s.Ensure()
			if err != nil || !created {
				t.Fatalf("Ensure() = %v, %v", created, err)
			}
			data, _ := os.ReadFile(path)
			if len(strings.TrimSpace(string(data))) != 47 {
				t.Errorf("file not regenerated: %d bytes", len(data))
			}
		})
	}
}

func TestEnsureKeepsAnExistingTokenAndToleratesTheNewline(t *testing.T) {
	tok := GenerateToken()
	for name, content := range map[string]string{"bare": tok, "lf": tok + "\n", "crlf": tok + "\r\n"} {
		t.Run(name, func(t *testing.T) {
			s, path, _, logs := newTokenStore(t, nil)
			writeFile(t, path, content, 0o640)
			created, err := s.Ensure()
			if err != nil || created {
				t.Fatalf("Ensure() = %v, %v; want the existing token kept", created, err)
			}
			if !s.Verify(tok) {
				t.Error("the existing token does not verify")
			}
			if got, _ := os.ReadFile(path); string(got) != content {
				t.Error("the file was rewritten")
			}
			if strings.Contains(logs.String(), "LAI-SEC-011") {
				t.Error("LAI-SEC-011 is for a created token only")
			}
		})
	}
}

func TestEnsureFixesUnsafePermissions(t *testing.T) {
	s, path, _, logs := newTokenStore(t, nil)
	tok := GenerateToken()
	writeFile(t, path, tok+"\n", 0o666)
	if _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %04o, want 0640 after Ensure", info.Mode().Perm())
	}
	if !strings.Contains(logs.String(), "LAI-SEC-009") {
		t.Errorf("no LAI-SEC-009 in the log: %s", logs.String())
	}
	mustNotContain(t, "log", logs.String(), tok)
}

func TestEnsureRefusesALink(t *testing.T) {
	s, path, _, _ := newTokenStore(t, nil)
	target := filepath.Join(filepath.Dir(path), "elsewhere")
	writeFile(t, target, GenerateToken(), 0o640)
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks not available:", err)
	}
	if _, err := s.Ensure(); err == nil {
		t.Fatal("a symlinked token file must be refused")
	}
}

func TestEnsureReportsAFailedCreate(t *testing.T) {
	path, _ := tempPaths(t)
	fsys := cfake.New(config.OSFS{}).FailOn("open", -1)
	log, _ := newLogger()
	s := NewTokenStore(fsys, path, ownPolicy(0o640), newClock(), log)
	if _, err := s.Ensure(); err == nil {
		t.Fatal("Ensure must fail when the file cannot be created")
	}
	if s.Verify(fakeToken()) || s.Verify("") {
		t.Error("a store without a token must match nothing")
	}
}

func TestVerifyTable(t *testing.T) {
	s, path, _, _ := newTokenStore(t, nil)
	tok := GenerateToken()
	writeFile(t, path, tok+"\n", 0o640)
	if _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"right", tok, true},
		{"empty", "", false},
		{"prefix only", "lat_", false},
		{"wrong", fakeToken(), false},
		{"one character off", tok[:len(tok)-1] + flip(tok[len(tok)-1]), false},
		{"longer", tok + "A", false},
		{"shorter", tok[:len(tok)-1], false},
		{"upper-cased", strings.ToUpper(tok), false},
		{"with newline", tok + "\n", false},
		{"without prefix", tok[4:], false},
	}
	for _, tt := range tests {
		if got := s.Verify(tt.in); got != tt.want {
			t.Errorf("%s: Verify = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func TestMtimeChangeIsPickedUpWithinTenSeconds(t *testing.T) {
	s, path, clock, _ := newTokenStore(t, nil)
	oldTok, newTok := GenerateToken(), GenerateToken()
	writeFile(t, path, oldTok+"\n", 0o640)
	if _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, newTok+"\n", 0o640)
	bump(t, path, time.Minute)

	// Inside the 10 s window the cache answers: the old token still passes (documented).
	clock.Advance(5 * time.Second)
	if !s.Verify(oldTok) {
		t.Fatal("within 10 s the cached token is still served")
	}
	// Past 10 s the next Verify sees the new mtime and reloads.
	clock.Advance(6 * time.Second)
	if s.Verify(oldTok) {
		t.Error("the old token must stop working once the mtime change is seen")
	}
	if !s.Verify(newTok) {
		t.Error("the rotated token must work")
	}
}

func TestAFailedCompareRereadsAtOnce(t *testing.T) {
	s, path, _, _ := newTokenStore(t, nil)
	oldTok, newTok := GenerateToken(), GenerateToken()
	writeFile(t, path, oldTok+"\n", 0o640)
	if _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	var dropped int
	s.OnChange(func() { dropped++ })
	writeFile(t, path, newTok+"\n", 0o640)
	bump(t, path, time.Minute)
	// No clock movement: only the failed compare can notice the rotation (aictl token rotate).
	if !s.Verify(newTok) {
		t.Fatal("a new token must verify on the first try")
	}
	if dropped != 1 {
		t.Errorf("OnChange ran %d times, want 1", dropped)
	}
	if s.Verify(oldTok) {
		t.Error("the old token must be dead after the re-read")
	}
	if dropped != 1 {
		t.Errorf("OnChange ran again without a change: %d", dropped)
	}
}

func TestUnreadableOrEmptyFileKeepsTheCachedToken(t *testing.T) {
	tok := GenerateToken()
	for name, damage := range map[string]func(t *testing.T, path string, fsys *cfake.FaultFS){
		"read error": func(t *testing.T, _ string, fsys *cfake.FaultFS) { fsys.FailOn("readfile", -1) },
		"deleted":    func(t *testing.T, path string, _ *cfake.FaultFS) { _ = os.Remove(path) },
		"emptied": func(t *testing.T, path string, _ *cfake.FaultFS) {
			writeFile(t, path, "\n", 0o640)
			bump(t, path, time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := cfake.New(config.OSFS{})
			s, path, clock, logs := newTokenStore(t, fsys)
			writeFile(t, path, tok+"\n", 0o640)
			if _, err := s.Ensure(); err != nil {
				t.Fatal(err)
			}
			damage(t, path, fsys)
			clock.Advance(time.Minute)
			for i := 0; i < 3; i++ {
				if s.Verify(fakeToken()) {
					t.Fatal("a wrong token passed")
				}
			}
			if !s.Verify(tok) {
				t.Error("the cached token must keep working")
			}
			if n := strings.Count(logs.String(), "keeping the cached one"); n != 1 {
				t.Errorf("the problem was logged %d times, want once: %s", n, logs.String())
			}
			mustNotContain(t, "log", logs.String(), tok)
		})
	}
}

func TestVerifyIsSafeForConcurrentUse(t *testing.T) {
	s, path, clock, _ := newTokenStore(t, nil)
	tok := GenerateToken()
	writeFile(t, path, tok+"\n", 0o640)
	if _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if i%2 == 0 {
					clock.Advance(time.Second)
				}
				if !s.Verify(tok) || s.Verify(fakeToken()) {
					t.Error("wrong verdict")
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
