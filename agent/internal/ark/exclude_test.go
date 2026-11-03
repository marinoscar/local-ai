package ark_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Exclude is the in-memory removal of a copy that failed re-verification on a root nothing can be moved
// off (ark-sync.md section 4.13) or a drive copy an import found damaged (section 4.14): the copy stops
// being offered, the other copies are untouched, a rebuild of the same copy keeps the exclusion, and a
// new version or an unmount ends it.
func TestExcludeTakesOneCopyOutOfTheAnswersInMemory(t *testing.T) {
	internal, drive := testRoot(t, ark.InternalID, 0), testRoot(t, "ark-aaaa0001", 10)
	putItem(t, internal, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("qwen-x", "v1", "2026-01"))
	putItem(t, drive, llama("only-here", "v1", "2026-01"))
	ix := ark.NewIndex(config.OSFS{})
	rebuild(t, ix, internal, drive)

	ix.Exclude(internal.ID, "qwen-x", "v1")
	if !ix.Excluded(internal.ID, "qwen-x", "v1") || ix.Excluded(drive.ID, "qwen-x", "v1") {
		t.Fatal("Excluded must report exactly the copy that was excluded")
	}
	if c := best(t, ix, "qwen-x"); c.RootID != drive.ID {
		t.Fatalf("best copy after the exclusion = %s, want the drive's", c.RootID)
	}
	ix.Exclude(drive.ID, "only-here", "v1")
	if _, err := ix.BestCopy("only-here"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("an excluded only copy must be not present: %v", err)
	}
	for _, id := range ix.ItemIDs() {
		if id == "only-here" {
			t.Fatal("ItemIDs lists an item whose only copy is excluded")
		}
	}

	// The same copy read again stays excluded; the files on disk were never touched.
	rebuild(t, ix, internal, drive)
	if _, err := ix.BestCopy("only-here"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("the exclusion must survive a rebuild")
	}
	if !exists(filepath.Join(drive.Path, "models", "only-here", "MANIFEST.json")) {
		t.Fatal("Exclude must not write to the root")
	}

	// A new version is a different copy.
	putItem(t, drive, llama("only-here", "v2", "2026-06"))
	if _, err := ix.Refresh(bg, drive, "models/only-here"); err != nil {
		t.Fatal(err)
	}
	if c := best(t, ix, "only-here"); c.Manifest.Version != "v2" {
		t.Fatalf("a new version must be offered: %s", c.Manifest.Version)
	}

	// Unmounted and back: judged again from scratch.
	rebuild(t, ix, internal)
	rebuild(t, ix, internal, drive)
	if ix.Excluded(drive.ID, "only-here", "v1") {
		t.Fatal("an unmounted root's exclusions must be dropped")
	}
	if c := best(t, ix, "only-here"); c.RootID != drive.ID {
		t.Fatal("after the drive came back the copy is offered")
	}
}
