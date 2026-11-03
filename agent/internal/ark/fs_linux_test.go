//go:build linux

package ark_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

func TestOSFSOnTheRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	var fsys ark.FS = ark.OSFS{}

	magic, err := fsys.FSType(dir)
	if err != nil || magic == 0 {
		t.Fatalf("FSType(%s) = 0x%X, %v", dir, magic, err)
	}
	if ark.IsNetworkFS(magic) {
		t.Skipf("the temp directory is on a network filesystem (0x%X)", magic)
	}
	if _, err := fsys.FSType(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("FSType of a missing path: %v", err)
	}
	if err := fsys.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir(filepath.Join(dir, "a"), 0o755); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Mkdir: %v", err)
	}
}

func TestEnsureInternalOnTheRealFilesystem(t *testing.T) {
	// The whole path with the real FS, no fakes: the production wiring does exactly this.
	root := filepath.Join(t.TempDir(), "srv", "localai")
	got, problems, err := ark.EnsureInternal(ark.OSFS{}, ark.InternalOptions{Path: root, Profile: "small-8gb"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadOnly || got.State != ark.StateReady || len(problems) != 1 || problems[0].Code != ark.CodeInternalRecreated {
		t.Fatalf("got %+v, %v", got, codes(problems))
	}
	assertLayout(t, root)
	if _, err := ark.ReadRootJSON(filepath.Join(root, ark.RootJSONName)); err != nil {
		t.Fatal(err)
	}
}
