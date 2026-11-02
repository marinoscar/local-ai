package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ownPolicy is a policy any unprivileged test user can satisfy.
func ownPolicy(mode os.FileMode) config.SecretPolicy {
	return config.SecretPolicy{Mode: mode, UID: os.Getuid(), GID: -1}
}

func secretFile(t testing.TB, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("example-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSecretWithTheRightModeHasNoIssues(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} { // stricter than the policy is fine
		path := secretFile(t, mode)
		sf, err := config.ReadSecretFile(nil, path, ownPolicy(0o600))
		if err != nil {
			t.Fatal(err)
		}
		if len(sf.Issues) != 0 || string(sf.Data) != "example-secret\n" {
			t.Errorf("mode %04o: %+v", mode, sf)
		}
	}
}

func TestWorldReadableAPIKeyIsFlagged(t *testing.T) {
	path := secretFile(t, 0o644)
	sf, err := config.ReadSecretFile(nil, path, ownPolicy(0o600))
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.Issues) != 1 || sf.Issues[0].Path != path {
		t.Fatalf("a 0644 api-key must be flagged once, got %+v", sf.Issues)
	}
	if string(sf.Data) != "example-secret\n" {
		t.Error("the content is still returned; the caller decides")
	}
}

func TestEveryExtraPermissionBitIsFlagged(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o666, 0o700, 0o601} {
		issues, err := config.CheckSecret(nil, secretFile(t, mode), ownPolicy(0o600))
		if err != nil || len(issues) == 0 {
			t.Errorf("mode %04o must be flagged, got %v %v", mode, issues, err)
		}
	}
}

func TestAgentTokenPolicyAllowsGroupRead(t *testing.T) {
	pol := config.SecretPolicy{Mode: config.PolicyAgentToken.Mode, UID: os.Getuid(), GID: config.PolicyAgentToken.GID}
	if issues, _ := config.CheckSecret(nil, secretFile(t, 0o640), pol); len(issues) != 0 {
		t.Errorf("0640 is the agent-token mode: %v", issues)
	}
	if issues, _ := config.CheckSecret(nil, secretFile(t, 0o644), pol); len(issues) != 1 {
		t.Errorf("0644 is too open for the agent-token: %v", issues)
	}
}

func TestWrongOwnerIsFlagged(t *testing.T) {
	path := secretFile(t, 0o600)
	pol := config.SecretPolicy{Mode: 0o600, UID: os.Getuid() + 1, GID: -1}
	issues, err := config.CheckSecret(nil, path, pol)
	if err != nil || len(issues) != 1 {
		t.Fatalf("a file owned by another uid must be flagged: %v %v", issues, err)
	}
}

func TestSetuidBitIsFlagged(t *testing.T) {
	path := secretFile(t, 0o600)
	if err := os.Chmod(path, 0o600|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if issues, _ := config.CheckSecret(nil, path, ownPolicy(0o600)); len(issues) != 1 {
		t.Errorf("setgid must be flagged: %v", issues)
	}
}

func TestSymlinkedSecretIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "api-key")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := config.ReadSecretFile(nil, link, ownPolicy(0o600)); !errors.Is(err, config.ErrNotRegularFile) {
		t.Errorf("err = %v, want ErrNotRegularFile", err)
	}
	if _, err := config.EnforceSecret(nil, link, ownPolicy(0o600)); !errors.Is(err, config.ErrNotRegularFile) {
		t.Errorf("enforce must not follow a link: %v", err)
	}
}

func TestMissingSecretIsNotExist(t *testing.T) {
	_, err := config.ReadSecretFile(nil, filepath.Join(t.TempDir(), "nope"), ownPolicy(0o600))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

func TestEnforceSecretFixesTheModeAndReportsWhatItFixed(t *testing.T) {
	path := secretFile(t, 0o644)
	fixed, err := config.EnforceSecret(nil, path, ownPolicy(0o600))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed) != 1 {
		t.Fatalf("fixed = %v, want one issue (the cue for the permission-fixed doctor code)", fixed)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after fix = %04o", info.Mode().Perm())
	}
	again, err := config.EnforceSecret(nil, path, ownPolicy(0o600))
	if err != nil || len(again) != 0 {
		t.Errorf("a second pass has nothing to fix: %v %v", again, err)
	}
}

func TestOversizedSecretIsRefused(t *testing.T) {
	path := secretFile(t, 0o600)
	if err := os.WriteFile(path, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadSecretFile(nil, path, ownPolicy(0o600)); err == nil {
		t.Error("a secret file over 1 MiB must be refused")
	}
}
