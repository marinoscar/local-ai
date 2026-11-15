package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// The real catalog's pip_wheelhouse items agree with the files of this repository (rebuild-kit.md section 3.2.2):
// the lock's sha256 is the one ark.yaml pins, the lock parses, and the tree hash it records for each repo_path
// build is the hash of that folder as it is now. A shim edited without `scripts/release/lock-acestep.sh
// --shim-only`, or a lock regenerated without updating ark.yaml, fails here instead of on a node.
func TestRealCatalogWheelhousesMatchTheRepository(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "..")
	cat, err := catalog.Load(filepath.Join(repo, "catalog"))
	if err != nil {
		t.Fatalf("the real catalog does not load: %v", err)
	}
	n := 0
	for i := range cat.Content {
		it := cat.Content[i]
		if it.Source.Type != "pip_wheelhouse" {
			continue
		}
		n++
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(it.Source.Lock)))
		if err != nil {
			t.Fatalf("%s: %v", it.ID, err)
		}
		if got := sha256Hex(data); got != it.Source.SHA256 {
			t.Errorf("%s: %s has sha256 %s, ark.yaml pins %s (put the sha256 lock-acestep.sh printed into ark.yaml)", it.ID, it.Source.Lock, got, it.Source.SHA256)
		}
		lock, err := ParsePipLock(data)
		if err != nil {
			t.Fatalf("%s: %s: %v", it.ID, it.Source.Lock, err)
		}
		if len(lock.Unresolved) > 0 {
			// Not a failure here: the generator could not reach every index (see the lock's header). Ark sync
			// refuses the item until the lock is complete (LAI-SYN-060).
			t.Logf("%s: %d packages are still unresolved in %s: %v", it.ID, len(lock.Unresolved), it.Source.Lock, lock.Unresolved)
		}
		for _, b := range it.Source.Builds {
			if b.RepoPath == "" {
				continue
			}
			got, err := RepoTreeSHA256(filepath.Join(repo, filepath.FromSlash(b.RepoPath)))
			if err != nil {
				t.Fatalf("%s: %s: %v", it.ID, b.RepoPath, err)
			}
			if want := lock.RepoTrees[b.RepoPath]; got != want {
				t.Errorf("%s: the tree hash of %s is %s, the lock records %q (run scripts/release/lock-acestep.sh --shim-only)", it.ID, b.RepoPath, got, want)
			}
		}
		if v := catalog.WheelhouseVersion(it.ID, it.Source); v == "" {
			t.Errorf("%s has no version", it.ID)
		}
	}
	if n == 0 {
		t.Fatal("the real catalog has no pip_wheelhouse item (kit-engine-acestep-cuda)")
	}
}
