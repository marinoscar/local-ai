package ark_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// sha is a stand-in sha256 (the hash of a name): no hash literal is ever written in the repo.
func sha(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// itemSpec describes a verified item to put on a root.
type itemSpec struct {
	ref     ark.ItemRef
	version string
	sortKey string
	files   map[string]string // relative path -> content
	dirItem bool              // a directory item: tree hash and SHA256SUMS instead of a file list
}

func (s itemSpec) manifest() manifest.Manifest {
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: s.ref.ID, ItemKind: s.ref.Kind, Engine: s.ref.Engine, CatalogVersion: "2026.11.02-1",
		Version: s.version, SortKey: s.sortKey, State: manifest.StateVerified,
		Source:       manifest.Source{Type: "url"},
		DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
	var sums []manifest.Sum
	for rel, c := range s.files {
		m.TotalBytes += int64(len(c))
		if s.dirItem {
			sums = append(sums, manifest.Sum{SHA256: sha(rel), Path: rel})
			continue
		}
		m.Files = append(m.Files, manifest.File{Kind: "weights", Path: rel, SizeBytes: int64(len(c)), SHA256: sha(rel), SHA256Source: "catalog"})
	}
	if s.dirItem {
		m.TreeSHA256, _ = manifest.TreeSHA256(sums)
		m.FileCount = len(sums)
	} else {
		// Manifest order is the order the caller will see: sort by path for a stable test.
		sortFiles(m.Files)
	}
	return m
}

func sortFiles(fs []manifest.File) {
	for i := range fs {
		for j := i + 1; j < len(fs); j++ {
			if fs[j].Path < fs[i].Path {
				fs[i], fs[j] = fs[j], fs[i]
			}
		}
	}
}

// putItem writes the item's files and a valid MANIFEST under root and returns the item directory.
func putItem(t testing.TB, root ark.Root, s itemSpec) string {
	t.Helper()
	rel, err := ark.ItemDir(s.ref)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root.Path, filepath.FromSlash(rel))
	for p, c := range s.files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), c)
	}
	if s.dirItem {
		var sums []manifest.Sum
		for p := range s.files {
			sums = append(sums, manifest.Sum{SHA256: sha(p), Path: p})
		}
		if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), s.manifest()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func model(id, engine string) ark.ItemRef {
	return ark.ItemRef{Kind: manifest.KindModel, Engine: engine, ID: id}
}

func llama(id, version, sortKey string) itemSpec {
	return itemSpec{ref: model(id, ark.EngineLlama), version: version, sortKey: sortKey, files: map[string]string{id + ".gguf": "weights-of-" + id + "-" + version}}
}

func testRoot(t testing.TB, id string, mountID int) ark.Root {
	t.Helper()
	if id == ark.InternalID {
		r := ark.NewInternalRoot(filepath.Join(t.TempDir(), "srv"), "ark-full")
		return r
	}
	r, err := ark.NewDriveRoot(ark.DriveInfo{Mount: filepath.Join(t.TempDir(), "mnt"), FSUUID: id[len("ark-"):] + "-0000-0000-0000-000000000000", MountID: mountID}, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
