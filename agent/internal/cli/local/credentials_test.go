package local

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestSetPINWritesAnArgon2idHashTheAgentAccepts(t *testing.T) {
	e, _ := testEnv(t)
	if err := SetPIN(e, "4821"); err != nil {
		t.Fatal(err)
	}
	path := e.Paths.PINFile()
	got := read(t, path)
	if !regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}\n$`).MatchString(got) {
		t.Fatalf("not the PHC format with the exact parameters: %q", got)
	}
	if strings.Contains(got, "4821") {
		t.Error("the PIN itself was written")
	}
	if m := modeOf(t, path); m != 0o600 {
		t.Errorf("mode %o, want 0600", m)
	}
	if uid, gid := ownerOf(mustStat(t, path)); uid != e.RootUID || gid != e.RootGID {
		t.Errorf("owner %d:%d, want %d:%d", uid, gid, e.RootUID, e.RootGID)
	}
	// The round trip: the agent's own verifier (AGENT-06) accepts the right PIN and refuses a wrong one.
	v := auth.NewPIN(nil, path, config.SecretPolicy{Mode: 0o600, UID: -1, GID: -1}, nil, nil, nil)
	if err := v.Verify("4821"); err != nil {
		t.Errorf("the agent refused the new PIN: %v", err)
	}
	var wrong *auth.WrongPINError
	if err := v.Verify("4822"); !errors.As(err, &wrong) {
		t.Errorf("a wrong PIN gave %v", err)
	}
}

func TestSetPINRefusesABadPINBeforeWritingAnything(t *testing.T) {
	for _, pin := range []string{"", "123", "123456789", "12ab", "12 34", "١٢٣٤"} {
		e, _ := testEnv(t)
		if err := SetPIN(e, pin); !errors.Is(err, auth.ErrInvalidPIN) {
			t.Errorf("%q: %v, want ErrInvalidPIN", pin, err)
		}
		if exists(e.Paths.PINFile()) {
			t.Errorf("%q: a file was written", pin)
		}
	}
}

func TestSetPINReplacesTheOldHash(t *testing.T) {
	e, _ := testEnv(t)
	if err := SetPIN(e, "1111"); err != nil {
		t.Fatal(err)
	}
	first := read(t, e.Paths.PINFile())
	if err := SetPIN(e, "2222"); err != nil {
		t.Fatal(err)
	}
	if read(t, e.Paths.PINFile()) == first {
		t.Fatal("the hash did not change")
	}
}

func TestSetPINWriteFailuresLeaveTheOldFileAndNoTemp(t *testing.T) {
	for _, op := range []string{"rename", "open"} {
		t.Run(op, func(t *testing.T) {
			e, _ := testEnv(t)
			if err := SetPIN(e, "1111"); err != nil {
				t.Fatal(err)
			}
			before := read(t, e.Paths.PINFile())
			e.FS = cfake.New(config.OSFS{}).FailOn(op, 1)
			err := SetPIN(e, "2222")
			var fe *FileError
			if !errors.As(err, &fe) || fe.Path != e.Paths.PINFile() {
				t.Fatalf("got %v, want a FileError naming the PIN file", err)
			}
			if read(t, e.Paths.PINFile()) != before {
				t.Error("the old PIN file changed")
			}
			for _, f := range listFiles(t, e.Paths.EtcDir) {
				if strings.Contains(f, ".tmp-") {
					t.Errorf("temp file left behind: %s", f)
				}
			}
		})
	}
}

func TestSetPINUnwritableFolder(t *testing.T) {
	e, _ := testEnv(t)
	e.Paths.EtcDir = e.Paths.EtcDir + "/missing/deeper"
	err := SetPIN(e, "1111")
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("got %v", err)
	}
	if fe.Reason() != "it does not exist" {
		t.Errorf("reason %q", fe.Reason())
	}
}

func TestRotateTokenFormatModeAndGroup(t *testing.T) {
	e, _ := testEnv(t)
	var asked string
	e.LookupGroup = func(name string) (int, error) { asked = name; return os.Getgid(), nil }
	if err := RotateToken(e); err != nil {
		t.Fatal(err)
	}
	if asked != "localai-operators" {
		t.Errorf("looked up group %q", asked)
	}
	path := e.Paths.TokenFile()
	got := read(t, path)
	if !regexp.MustCompile(`^` + auth.TokenPrefix + `[A-Za-z0-9_-]{43}\n$`).MatchString(got) {
		t.Fatalf("token format wrong: length %d", len(got))
	}
	if m := modeOf(t, path); m != 0o640 {
		t.Errorf("mode %o, want 0640", m)
	}
	if _, gid := ownerOf(mustStat(t, path)); gid != os.Getgid() {
		t.Errorf("group %d", gid)
	}
	if err := RotateToken(e); err != nil {
		t.Fatal(err)
	}
	if read(t, path) == got {
		t.Error("a second rotation gave the same token")
	}
}

func TestRotateTokenWithoutTheOperatorsGroupTouchesNothing(t *testing.T) {
	e, _ := testEnv(t)
	write(t, e.Paths.TokenFile(), "old\n", 0o640)
	e.LookupGroup = func(string) (int, error) { return -1, errors.New("group not found") }
	err := RotateToken(e)
	var ge *GroupError
	if !errors.As(err, &ge) || ge.Name != "localai-operators" {
		t.Fatalf("got %v, want a GroupError", err)
	}
	if read(t, e.Paths.TokenFile()) != "old\n" {
		t.Error("the token file changed")
	}
}

func TestRotateTokenInterruptedWriteKeepsTheOldToken(t *testing.T) {
	e, _ := testEnv(t)
	write(t, e.Paths.TokenFile(), "old\n", 0o640)
	e.FS = cfake.New(config.OSFS{}).FailOn("rename", 1)
	if err := RotateToken(e); err == nil {
		t.Fatal("no error")
	}
	if read(t, e.Paths.TokenFile()) != "old\n" {
		t.Error("the token file changed")
	}
}

func TestReasonInPlainWords(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{syscall.ENOSPC, "the disk is full"},
		{syscall.EROFS, "the disk is read-only"},
		{syscall.EACCES, "permission denied"},
		{syscall.ENOENT, "it does not exist"},
		{syscall.EIO, "input/output error"},
		{errNotRegular, "it is not a regular file"},
	} {
		if got := Reason(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
