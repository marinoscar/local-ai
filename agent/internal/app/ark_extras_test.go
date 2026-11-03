package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// memJournal is extrasJournal over a map.
type memJournal struct {
	items   map[string]journal.Item
	failErr error
}

func (m *memJournal) Get(rootID, itemID string) (journal.Item, bool) {
	it, ok := m.items[journal.Key(rootID, itemID)]
	return it, ok
}

func (m *memJournal) Update(rootID, itemID string, fn func(*journal.Item)) (journal.Item, error) {
	if m.failErr != nil {
		return journal.Item{}, m.failErr
	}
	it := m.items[journal.Key(rootID, itemID)]
	fn(&it)
	m.items[journal.Key(rootID, itemID)] = it
	return it, nil
}

type extrasRig struct {
	t        *testing.T
	x        *driveExtras
	j        *memJournal
	tmp      string
	roots    []ark.Root
	settings run.Settings
	raised   []string
	cleared  int
	details  map[string]any
}

const (
	driveA = "ark-3f9c0a12"
	driveB = "ark-77aa00bb"
)

func newExtrasRig(t *testing.T) *extrasRig {
	tmp := t.TempDir()
	r := &extrasRig{t: t, tmp: tmp, j: &memJournal{items: map[string]journal.Item{}}, settings: run.DefaultSettings()}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(tmp, "srv"), State: ark.StateReady},
		r.addDrive(driveA),
	}
	r.x = &driveExtras{
		FS: ark.OSFS{}, Roots: func() []ark.Root { return r.roots }, Settings: func() run.Settings { return r.settings },
		Journal: r.j, NodeFile: filepath.Join(tmp, "etc", "inventory.local.yaml"), NodeID: func() string { return "ai-n3" },
		Now: func() time.Time { return time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC) },
		Report: func(id string) (ark.StartHereInput, bool) {
			return ark.StartHereInput{Overall: "green", SurvivalCapable: true, WrittenBy: "ai-n3", Items: []ark.StartHereItem{{ID: "docs-bundle", Version: "1", Status: "verified"}}}, true
		},
		Raise: func(code string, d map[string]any) { r.raised = append(r.raised, code); r.details = d },
		Clear: func(code string) {
			if code == doctor.ARK012 {
				r.cleared++
			}
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return r
}

func (r *extrasRig) addDrive(id string) ark.Root {
	mount := filepath.Join(r.tmp, "media", id)
	if err := os.MkdirAll(filepath.Join(mount, "localai"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return ark.Root{ID: id, Kind: ark.KindArkDrive, Path: filepath.Join(mount, "localai"), Mount: mount, State: ark.StateReady}
}

func (r *extrasRig) writeNode(content string, age time.Duration) {
	r.t.Helper()
	write(r.t, r.x.NodeFile, content, age)
}

func write(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func (r *extrasRig) read(path string) string {
	r.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *extrasRig) has(path string) bool { _, err := os.Lstat(path); return err == nil }

func (r *extrasRig) startHere(id string) string {
	return filepath.Join(r.tmp, "media", id, "START-HERE.html")
}
func (r *extrasRig) inv(id string) string {
	return filepath.Join(r.tmp, "media", id, "inventory.local.yaml")
}

func TestDriveExtrasWritesTheStartPageOfTheDrivesOfTheRun(t *testing.T) {
	r := newExtrasRig(t)
	r.roots = append(r.roots, r.addDrive(driveB))
	r.x.Run(context.Background(), run.RunContext{RunID: "run-1", Roots: []string{ark.InternalID, driveA}})
	if !r.has(r.startHere(driveA)) {
		t.Fatal("the drive of the run has no START-HERE.html")
	}
	if r.has(r.startHere(driveB)) {
		t.Error("a drive the run was not about got a page")
	}
	if r.has(filepath.Join(r.tmp, "srv", "START-HERE.html")) {
		t.Error("the internal root got a page")
	}
	page := r.read(r.startHere(driveA))
	if !strings.Contains(page, "Disco AI-ARK. Empiece aquí.") || !strings.Contains(page, "docs-bundle") || !strings.Contains(page, "READY") {
		t.Errorf("page: %.200s", page)
	}
}

func TestDriveExtrasSkipsWhatCannotBeWritten(t *testing.T) {
	for name, mut := range map[string]func(*extrasRig){
		"a read-only drive":     func(r *extrasRig) { r.roots[1].ReadOnly = true },
		"an unclean drive":      func(r *extrasRig) { r.roots[1].State = ark.StateUnclean },
		"a drive being ejected": func(r *extrasRig) { r.roots[1].State = ark.StateEjecting },
		"ark_drive disabled":    func(r *extrasRig) { r.settings.ArkDrive.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := newExtrasRig(t)
			r.writeNode("lab: alpha\n", time.Hour)
			mut(r)
			if err := r.x.Write(context.Background(), r.roots[1]); err != nil {
				t.Fatal(err)
			}
			if r.has(r.startHere(driveA)) || r.has(r.inv(driveA)) {
				t.Error("something was written")
			}
		})
	}
}

func TestDriveExtrasCopiesTheInventoryAndRemembersIt(t *testing.T) {
	r := newExtrasRig(t)
	r.writeNode("lab: alpha\n", time.Hour)
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	if got := r.read(r.inv(driveA)); got != "lab: alpha\n" {
		t.Fatalf("drive inventory %q", got)
	}
	it, ok := r.j.Get(driveA, ark.ExtrasItemID)
	if !ok || it.State != journal.StateSkipped || string(it.Extras[ark.ExtraInventory]) != `"`+shaOf("lab: alpha\n")+`"` {
		t.Fatalf("journal record %+v", it)
	}
	if len(r.raised) != 0 {
		t.Errorf("raised %v", r.raised)
	}

	t.Run("a change on the node is carried over on the next run", func(t *testing.T) {
		r.writeNode("lab: beta\n", 0)
		r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
		if got := r.read(r.inv(driveA)); got != "lab: beta\n" {
			t.Errorf("drive inventory %q", got)
		}
		if got := r.j.items[journal.Key(driveA, ark.ExtrasItemID)]; string(got.Extras[ark.ExtraInventory]) != `"`+shaOf("lab: beta\n")+`"` {
			t.Errorf("remembered %s", got.Extras[ark.ExtraInventory])
		}
	})
}

func TestDriveExtrasConflictRaisesTheCodeAndClearsItWhenResolved(t *testing.T) {
	r := newExtrasRig(t)
	r.writeNode("lab: alpha\n", time.Hour)
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}}) // the first copy
	// Both sides are edited after the last copy.
	r.writeNode("lab: beta\n", time.Minute)
	write(t, r.inv(driveA), "lab: gamma\n", 2*time.Hour)

	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	if len(r.raised) != 1 || r.raised[0] != doctor.ARK012 {
		t.Fatalf("raised %v", r.raised)
	}
	if r.details["root_id"] != driveA || !strings.HasPrefix(r.details["conflict_file"].(string), "inventory.local.yaml.ai-n3.") {
		t.Errorf("details %v", r.details)
	}
	for k, v := range r.details {
		if s, ok := v.(string); ok && (strings.Contains(s, "beta") || strings.Contains(s, r.tmp)) {
			t.Errorf("detail %s leaks %q", k, s)
		}
	}
	if r.read(r.inv(driveA)) != "lab: gamma\n" || r.read(r.x.NodeFile) != "lab: beta\n" {
		t.Error("a file was overwritten")
	}
	if r.cleared != 0 {
		t.Error("cleared too early")
	}

	// The person keeps the node's version on both sides.
	write(t, r.inv(driveA), "lab: beta\n", 0)
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	if r.cleared != 1 {
		t.Errorf("cleared %d times, want 1", r.cleared)
	}
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	if r.cleared != 1 {
		t.Errorf("a settled drive cleared again: %d", r.cleared)
	}
}

func TestDriveExtrasUnpluggingTheConflictedDriveWithdrawsTheAlert(t *testing.T) {
	r := newExtrasRig(t)
	r.writeNode("lab: alpha\n", time.Hour)
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	r.writeNode("lab: beta\n", time.Minute)
	write(t, r.inv(driveA), "lab: gamma\n", 2*time.Hour)
	r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
	if len(r.raised) != 1 {
		t.Fatalf("raised %v", r.raised)
	}
	r.roots = r.roots[:1] // the drive is gone
	r.x.Run(context.Background(), run.RunContext{Roots: []string{ark.InternalID}})
	if r.cleared != 1 {
		t.Errorf("cleared %d", r.cleared)
	}
}

func TestDriveExtrasTwoDrivesOneConflict(t *testing.T) {
	r := newExtrasRig(t)
	r.roots = append(r.roots, r.addDrive(driveB))
	r.writeNode("lab: alpha\n", time.Hour)
	run1 := run.RunContext{Roots: []string{driveA, driveB}}
	r.x.Run(context.Background(), run1)
	r.writeNode("lab: beta\n", 0)
	write(t, r.inv(driveA), "lab: gamma\n", 2*time.Hour)
	r.x.Run(context.Background(), run1)
	if r.cleared != 0 || len(r.raised) != 1 {
		t.Fatalf("raised %v cleared %d", r.raised, r.cleared)
	}
	if r.read(r.inv(driveB)) != "lab: beta\n" {
		t.Error("the quiet drive was not updated")
	}
}

func TestDriveExtrasInventorySwitchAndFailures(t *testing.T) {
	t.Run("copy_inventory false copies nothing but still writes the page", func(t *testing.T) {
		r := newExtrasRig(t)
		r.settings.ArkDrive.CopyInventory = false
		r.writeNode("lab: alpha\n", time.Hour)
		r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
		if r.has(r.inv(driveA)) || !r.has(r.startHere(driveA)) {
			t.Error("wrong files on the drive")
		}
	})
	t.Run("no inventory on the node: nothing is copied and nothing is raised", func(t *testing.T) {
		r := newExtrasRig(t)
		r.x.Run(context.Background(), run.RunContext{Roots: []string{driveA}})
		if r.has(r.inv(driveA)) || len(r.raised) != 0 {
			t.Error("something happened")
		}
	})
	t.Run("a failing page does not stop the inventory", func(t *testing.T) {
		r := newExtrasRig(t)
		r.writeNode("lab: alpha\n", time.Hour)
		if err := os.MkdirAll(filepath.Join(r.tmp, "media", driveA, "START-HERE.html", "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := r.x.Write(context.Background(), r.roots[1])
		if err == nil || !r.has(r.inv(driveA)) {
			t.Fatalf("err %v, inventory copied %v", err, r.has(r.inv(driveA)))
		}
	})
	t.Run("a journal that cannot be written is reported, the copy stands", func(t *testing.T) {
		r := newExtrasRig(t)
		r.j.failErr = os.ErrPermission
		r.writeNode("lab: alpha\n", time.Hour)
		if err := r.x.Write(context.Background(), r.roots[1]); err == nil {
			t.Fatal("no error")
		}
		if r.read(r.inv(driveA)) != "lab: alpha\n" {
			t.Error("the copy was not made")
		}
	})
	t.Run("a drive that vanished is an error and not a panic", func(t *testing.T) {
		r := newExtrasRig(t)
		r.writeNode("lab: alpha\n", time.Hour)
		if err := os.RemoveAll(filepath.Join(r.tmp, "media")); err != nil {
			t.Fatal(err)
		}
		if err := r.x.Write(context.Background(), r.roots[1]); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("the internal root is not a drive", func(t *testing.T) {
		r := newExtrasRig(t)
		if err := r.x.Write(context.Background(), r.roots[0]); err != nil {
			t.Fatal(err)
		}
	})
}

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
