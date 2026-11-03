package ark_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The inventory tests only ever use invented text: the real inventory is secret and never appears in
// the repository.

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type invRig struct {
	t     *testing.T
	fs    *tfs
	node  string // the node's inventory
	mount string
}

func newInvRig(t *testing.T) *invRig {
	dir := t.TempDir()
	r := &invRig{t: t, fs: newFS(), node: filepath.Join(dir, "etc", "inventory.local.yaml"), mount: filepath.Join(dir, "mount")}
	if err := os.MkdirAll(r.mount, 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *invRig) drive() string { return filepath.Join(r.mount, ark.InventoryFile) }

// put writes a file with the given content and modification time.
func (r *invRig) put(path, content string, age time.Duration) {
	r.t.Helper()
	writeFile(r.t, path, content)
	at := epoch.Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		r.t.Fatal(err)
	}
}

func (r *invRig) sync(last string) (ark.InventoryResult, error) {
	return ark.SyncInventory(ark.InventoryOptions{FS: r.fs, NodeFile: r.node, Mount: r.mount, NodeID: "ai-n3", Last: last, Now: fixedNow})
}

func (r *invRig) conflicts() []string {
	var out []string
	for _, n := range names(r.t, r.mount) {
		if strings.HasSuffix(n, ".conflict") {
			out = append(out, n)
		}
	}
	return out
}

func TestInventoryNoNodeFileCopiesNothing(t *testing.T) {
	r := newInvRig(t)
	res, err := r.sync("")
	if err != nil || res.Action != ark.InventoryNoNodeFile || res.SHA != "" || res.DrivePresent {
		t.Fatalf("%+v %v", res, err)
	}
	if exists(r.drive()) {
		t.Error("an inventory appeared on the drive")
	}
	// A drive file with no node file is left alone and reported as present.
	r.put(r.drive(), "drive: only\n", time.Hour)
	res, err = r.sync("")
	if err != nil || res.Action != ark.InventoryNoNodeFile || !res.DrivePresent || readFile(t, r.drive()) != "drive: only\n" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestInventoryEqualFilesAreLeftAlone(t *testing.T) {
	r := newInvRig(t)
	r.put(r.node, "lab: alpha\n", 2*time.Hour)
	r.put(r.drive(), "lab: alpha\n", time.Hour)
	before, _ := os.Stat(r.drive())
	res, err := r.sync("")
	if err != nil || res.Action != ark.InventoryUnchanged || res.SHA != sum("lab: alpha\n") || !res.DrivePresent || res.Code() != "" {
		t.Fatalf("%+v %v", res, err)
	}
	after, _ := os.Stat(r.drive())
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the drive file was rewritten although it is the same")
	}
	if len(r.conflicts()) != 0 {
		t.Errorf("conflicts %v", r.conflicts())
	}
}

func TestInventoryMissingOnTheDriveIsCopiedWithMode0600(t *testing.T) {
	r := newInvRig(t)
	r.put(r.node, "lab: alpha\n", time.Hour)
	res, err := r.sync("")
	if err != nil || res.Action != ark.InventoryCopied || res.SHA != sum("lab: alpha\n") || !res.DrivePresent {
		t.Fatalf("%+v %v", res, err)
	}
	if got := readFile(t, r.drive()); got != "lab: alpha\n" {
		t.Errorf("drive file %q", got)
	}
	if m := mode(t, r.drive()); m != 0o600 {
		t.Errorf("mode %o, want 600", m)
	}
	for _, n := range names(t, r.mount) {
		if strings.Contains(n, ".tmp") {
			t.Errorf("a temp file was left: %s", n)
		}
	}
}

func TestInventoryOneSideChanged(t *testing.T) {
	last := sum("lab: alpha\n")
	t.Run("the node changed and is newer: the drive is updated", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: beta\n", time.Minute)
		r.put(r.drive(), "lab: alpha\n", time.Hour)
		res, err := r.sync(last)
		if err != nil || res.Action != ark.InventoryUpdatedDrive || res.SHA != sum("lab: beta\n") {
			t.Fatalf("%+v %v", res, err)
		}
		if got := readFile(t, r.drive()); got != "lab: beta\n" || mode(t, r.drive()) != 0o600 {
			t.Errorf("drive file %q %o", got, mode(t, r.drive()))
		}
		if len(r.conflicts()) != 0 {
			t.Errorf("conflicts %v", r.conflicts())
		}
	})
	t.Run("the drive changed and is newer: the node is updated", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: alpha\n", time.Hour)
		r.put(r.drive(), "lab: gamma\n", time.Minute)
		res, err := r.sync(last)
		if err != nil || res.Action != ark.InventoryUpdatedNode || res.SHA != sum("lab: gamma\n") {
			t.Fatalf("%+v %v", res, err)
		}
		if got := readFile(t, r.node); got != "lab: gamma\n" || mode(t, r.node) != 0o600 {
			t.Errorf("node file %q %o", got, mode(t, r.node))
		}
	})
	t.Run("the changed file is the older one: nothing says which to keep, so it is a conflict", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: beta\n", 2*time.Hour)
		r.put(r.drive(), "lab: alpha\n", time.Hour)
		res, err := r.sync(last)
		if err != nil || !res.Conflict() {
			t.Fatalf("%+v %v", res, err)
		}
		if readFile(t, r.drive()) != "lab: alpha\n" {
			t.Error("the drive file was overwritten")
		}
	})
}

func TestInventoryBothChangedKeepsBothAndRaisesTheCode(t *testing.T) {
	r := newInvRig(t)
	r.put(r.node, "lab: beta\n", time.Minute)
	r.put(r.drive(), "lab: gamma\n", time.Hour)
	res, err := r.sync(sum("lab: alpha\n"))
	if err != nil || !res.Conflict() || res.Code() != doctor.ARK012 || res.SHA != "" {
		t.Fatalf("%+v %v", res, err)
	}
	want := "inventory.local.yaml.ai-n3.20261102T180411Z.conflict"
	if res.ConflictFile != want || len(r.conflicts()) != 1 || r.conflicts()[0] != want {
		t.Fatalf("conflict file %q, files %v", res.ConflictFile, r.conflicts())
	}
	p := filepath.Join(r.mount, want)
	if readFile(t, p) != "lab: beta\n" || mode(t, p) != 0o600 {
		t.Errorf("conflict file %q %o", readFile(t, p), mode(t, p))
	}
	if readFile(t, r.drive()) != "lab: gamma\n" || readFile(t, r.node) != "lab: beta\n" {
		t.Error("one of the two files was touched")
	}

	t.Run("the same conflict seen again writes nothing new", func(t *testing.T) {
		later := ark.InventoryOptions{FS: r.fs, NodeFile: r.node, Mount: r.mount, NodeID: "ai-n3", Last: sum("lab: alpha\n"),
			Now: func() time.Time { return epoch.Add(24 * time.Hour) }}
		res2, err := ark.SyncInventory(later)
		if err != nil || !res2.Conflict() || res2.ConflictFile != want || len(r.conflicts()) != 1 {
			t.Fatalf("%+v %v files %v", res2, err, r.conflicts())
		}
	})
	t.Run("a new change of the node's file is a new conflict file", func(t *testing.T) {
		r.put(r.node, "lab: delta\n", time.Second)
		later := ark.InventoryOptions{FS: r.fs, NodeFile: r.node, Mount: r.mount, NodeID: "ai-n3", Last: sum("lab: alpha\n"),
			Now: func() time.Time { return epoch.Add(48 * time.Hour) }}
		res3, err := ark.SyncInventory(later)
		if err != nil || !res3.Conflict() || len(r.conflicts()) != 2 || res3.ConflictFile == want {
			t.Fatalf("%+v %v files %v", res3, err, r.conflicts())
		}
	})
	t.Run("once a person makes the two files equal the conflict is over", func(t *testing.T) {
		r.put(r.drive(), "lab: delta\n", 0)
		res4, err := r.sync(sum("lab: alpha\n"))
		if err != nil || res4.Action != ark.InventoryUnchanged || res4.Code() != "" || res4.SHA != sum("lab: delta\n") {
			t.Fatalf("%+v %v", res4, err)
		}
	})
}

func TestInventoryWithNoRecordOfALastCopyIsAConflict(t *testing.T) {
	r := newInvRig(t)
	r.put(r.node, "lab: beta\n", time.Minute)
	r.put(r.drive(), "lab: gamma\n", time.Hour)
	res, err := r.sync("")
	if err != nil || !res.Conflict() || readFile(t, r.drive()) != "lab: gamma\n" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestInventoryNodeIDCannotBeAPath(t *testing.T) {
	r := newInvRig(t)
	r.put(r.node, "lab: beta\n", time.Minute)
	r.put(r.drive(), "lab: gamma\n", time.Hour)
	res, err := ark.SyncInventory(ark.InventoryOptions{FS: r.fs, NodeFile: r.node, Mount: r.mount, NodeID: "../../Evil Node", Last: "", Now: fixedNow})
	if err != nil || !res.Conflict() {
		t.Fatalf("%+v %v", res, err)
	}
	if got := res.ConflictFile; got != "inventory.local.yaml.evil-node.20261102T180411Z.conflict" {
		t.Errorf("conflict name %q", got)
	}
	if exists(filepath.Join(filepath.Dir(r.mount), "Evil Node")) {
		t.Error("a file escaped the mount")
	}
}

func TestInventoryFailures(t *testing.T) {
	t.Run("a drive that is read-only: the copy fails and nothing is left behind", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: alpha\n", time.Hour)
		r.fs.FailOn("rename", -1)
		if _, err := r.sync(""); err == nil {
			t.Fatal("no error")
		}
		if exists(r.drive()) {
			t.Error("a half-copied inventory is on the drive")
		}
		for _, n := range names(t, r.mount) {
			if strings.HasPrefix(n, ".") || strings.Contains(n, "tmp") {
				t.Errorf("temp file %s left", n)
			}
		}
	})
	t.Run("a conflict that cannot be saved still reports the conflict", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: beta\n", time.Minute)
		r.put(r.drive(), "lab: gamma\n", time.Hour)
		r.fs.FailOn("rename", -1)
		res, err := r.sync("")
		if err == nil || !res.Conflict() || res.Code() != doctor.ARK012 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("a drive inventory that is a symbolic link is never followed", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "lab: alpha\n", time.Hour)
		target := filepath.Join(t.TempDir(), "elsewhere")
		writeFile(t, target, "x: y\n")
		if err := os.Symlink(target, r.drive()); err != nil {
			t.Skip(err)
		}
		if _, err := r.sync(""); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("error %v", err)
		}
		if readFile(t, target) != "x: y\n" {
			t.Error("the link target was written")
		}
	})
	t.Run("a huge file is not an inventory", func(t *testing.T) {
		r := newInvRig(t)
		writeFile(t, r.node, strings.Repeat("a", 2<<20))
		if _, err := r.sync(""); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("the error never contains the content", func(t *testing.T) {
		r := newInvRig(t)
		r.put(r.node, "secret_address: 203.0.113.9\n", time.Hour)
		r.fs.FailOn("rename", -1)
		_, err := r.sync("")
		if err == nil || strings.Contains(err.Error(), "203.0.113.9") || strings.Contains(err.Error(), "secret_address") {
			t.Fatalf("error %v", err)
		}
	})
}
