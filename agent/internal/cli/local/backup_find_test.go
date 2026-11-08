package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindBackupsNewestFirstAcrossRoots(t *testing.T) {
	internal := Root{Kind: KindInternal, Path: t.TempDir()}
	drive := Root{Kind: KindDrive, Path: t.TempDir(), Mount: "/media/op/AI-ARK"}
	put := func(r Root, node, name string) string {
		t.Helper()
		p := filepath.Join(r.KitConfig(), node, name)
		write(t, p, "x", 0o600)
		return p
	}
	newest := put(drive, "ai-n3", "localai-config-ai-n3-20261102T150405Z.tar.gz")
	put(drive, "ai-n3", "localai-config-ai-n3-20261001T000000Z.tar.gz")
	other := put(internal, "ai-n9", "localai-config-ai-n9-20261105T000000Z.tar.gz")
	put(internal, "ai-n3", "notes.txt")
	put(internal, "ai-n3", "localai-config-ai-n9-20261201T000000Z.tar.gz") // in another node's folder
	put(internal, "ai-n3", "localai-config-ai-n3-2026.tar.gz")
	if err := os.MkdirAll(filepath.Join(internal.KitConfig(), "ai-n3", "localai-config-ai-n3-20261231T000000Z.tar.gz"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newest, filepath.Join(internal.KitConfig(), "ai-n3", "localai-config-ai-n3-20261230T000000Z.tar.gz")); err != nil {
		t.Fatal(err)
	}

	got := FindBackups([]Root{internal, drive, {Path: filepath.Join(t.TempDir(), "missing")}})
	if len(got) != 3 {
		t.Fatalf("found %d backups, want 3: %+v", len(got), got)
	}
	if got[0].Path != other || got[1].Path != newest || got[2].Time != "20261001T000000Z" {
		t.Errorf("order = %s, %s, %s", got[0].Path, got[1].Path, got[2].Path)
	}
	if got[1].NodeID != "ai-n3" || got[1].Root.Mount != drive.Mount {
		t.Errorf("record = %+v", got[1])
	}
	if b, ok := LatestBackup(got, "ai-n3"); !ok || b.Path != newest {
		t.Errorf("latest of ai-n3 = %+v %v", b, ok)
	}
	if _, ok := LatestBackup(got, "ai-n4"); ok {
		t.Error("ai-n4 has no backup")
	}
	if len(FindBackups(nil)) != 0 {
		t.Error("no roots, no backups")
	}
}
