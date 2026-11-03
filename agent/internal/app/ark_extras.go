package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// The drive extras (ark-sync.md section 4.15, SYNC-14): after every run that touches an ark drive, and at
// `aictl ark init`, the agent writes START-HERE.html at the drive's top level and keeps the copy of the
// lab inventory there. The run engine calls driveExtras.Run (run.Hooks.Extras); the drive manager
// (SYNC-03) calls driveExtras.Write for a drive it has just initialised.
//
// Nothing here may fail a run. A drive that cannot be written (read-only, full, pulled) is logged and
// left as it is; the next run tries again.

// inventoryNodePath is the node's own copy of the lab inventory.
const inventoryNodePath = "/etc/localai/inventory.local.yaml"

// extrasJournal is the part of the sync journal the extras use: the per-root record that remembers the
// sha256 of the inventory as it was last copied (items.json extras.inventory).
type extrasJournal interface {
	Get(rootID, itemID string) (journal.Item, bool)
	Update(rootID, itemID string, fn func(*journal.Item)) (journal.Item, error)
}

type driveExtras struct {
	FS ark.FS
	// Roots lists the mounted roots.
	Roots func() []ark.Root
	// Report is the START-HERE input of a root, from its readiness report; false while it has none.
	Report   func(rootID string) (ark.StartHereInput, bool)
	Settings func() run.Settings
	Journal  extrasJournal
	NodeFile string
	NodeID   func() string
	Now      func() time.Time
	// Raise and Clear hold and withdraw LAI-ARK-012.
	Raise func(code string, details map[string]any)
	Clear func(code string)
	Log   *slog.Logger

	mu sync.Mutex
	// conflicts holds, per root, the conflict file of an inventory conflict that is still open.
	conflicts map[string]string
}

// newDriveExtras binds the extras to the daemon.
func (s *arkStack) newDriveExtras() *driveExtras {
	d := s.d
	return &driveExtras{
		FS: ark.OSFS{}, Roots: s.roots.List, Settings: d.syncSettings, Journal: s.jr.Items,
		NodeFile: d.c.Paths.Join(inventoryNodePath), NodeID: func() string { return d.c.Config.Get().Node.ID },
		Now:   d.env.Clock.Now,
		Raise: d.raise, Clear: d.clearAlert,
		Log: d.log.With("component", "sync"),
		Report: func(rootID string) (ark.StartHereInput, bool) {
			rep, ok := s.ready.Report(rootID)
			if !ok {
				return ark.StartHereInput{}, false
			}
			cat := d.c.Catalog.Get().Catalog
			return rep.StartHere(func(id string) string { return displayName(cat, id) }), true
		},
	}
}

// displayName is the catalog's display name of a model or content item ("" when it has none).
func displayName(cat *catalog.Catalog, id string) string {
	if cat == nil {
		return ""
	}
	if m, ok := cat.Model(id); ok {
		return m.DisplayName
	}
	if c, ok := cat.ContentItem(id); ok {
		return c.DisplayName
	}
	return ""
}

// Run is run.Hooks.Extras: it writes the extras of every ark drive the run was about.
func (x *driveExtras) Run(ctx context.Context, rc run.RunContext) {
	mounted := map[string]bool{}
	for _, r := range x.Roots() {
		mounted[r.ID] = true
		if r.Kind == ark.KindArkDrive && slices.Contains(rc.Roots, r.ID) {
			_ = x.Write(ctx, r)
		}
	}
	x.prune(mounted)
}

// Write writes START-HERE.html and the inventory copy of one drive. A root that is not a writable ark
// drive, or sync.ark_drive.enabled being false, is no work. The error is for the caller that wants to
// say so (the init command); Run ignores it, as everything is already logged.
func (x *driveExtras) Write(ctx context.Context, r ark.Root) error {
	if r.Kind != ark.KindArkDrive || r.Mount == "" {
		return nil
	}
	set := x.Settings()
	if !set.ArkDrive.Enabled || r.ReadOnly || r.State != ark.StateReady {
		return nil
	}
	var first error
	note := func(what string, err error) {
		if err == nil {
			return
		}
		x.Log.Warn("a drive extra could not be written", "root", r.ID, "extra", what, "error", err.Error())
		if first == nil {
			first = err
		}
	}

	in, _ := x.Report(r.ID)
	note("start_here", ark.WriteStartHere(x.FS, r.Mount, in))
	if set.ArkDrive.CopyInventory {
		note("inventory", x.inventory(r))
	}
	return first
}

// inventory keeps the drive's copy of the inventory (ark.SyncInventory), remembers the sha256 of what
// was last copied, and holds LAI-ARK-012 for as long as a conflict is open on any drive.
func (x *driveExtras) inventory(r ark.Root) error {
	last := x.lastCopied(r.ID)
	res, err := ark.SyncInventory(ark.InventoryOptions{
		FS: x.FS, NodeFile: x.NodeFile, Mount: r.Mount, NodeID: x.NodeID(), Last: last, Now: x.Now,
	})
	if res.SHA != "" && res.SHA != last {
		if rerr := x.remember(r.ID, res.SHA); rerr != nil && err == nil {
			err = rerr
		}
	}
	if res.Conflict() {
		x.mu.Lock()
		if x.conflicts == nil {
			x.conflicts = map[string]string{}
		}
		x.conflicts[r.ID] = res.ConflictFile
		x.mu.Unlock()
		x.Raise(doctor.ARK012, map[string]any{"root_id": r.ID, "conflict_file": res.ConflictFile})
		return err
	}
	if err == nil {
		x.resolved(r.ID)
	}
	return err
}

// resolved withdraws LAI-ARK-012 once no drive has an open conflict.
func (x *driveExtras) resolved(rootID string) {
	x.mu.Lock()
	_, had := x.conflicts[rootID]
	delete(x.conflicts, rootID)
	none := len(x.conflicts) == 0
	x.mu.Unlock()
	if had && none {
		x.Clear(doctor.ARK012)
	}
}

// prune forgets the conflicts of drives that are no longer mounted: the alert is about a drive in the
// room, and the next sync of that drive raises it again if it is still true.
func (x *driveExtras) prune(mounted map[string]bool) {
	x.mu.Lock()
	had := len(x.conflicts) > 0
	for id := range x.conflicts {
		if !mounted[id] {
			delete(x.conflicts, id)
		}
	}
	none := len(x.conflicts) == 0
	x.mu.Unlock()
	if had && none {
		x.Clear(doctor.ARK012)
	}
}

// lastCopied is the sha256 stored by the previous pass for a drive ("" for none).
func (x *driveExtras) lastCopied(rootID string) string {
	it, ok := x.Journal.Get(rootID, ark.ExtrasItemID)
	if !ok {
		return ""
	}
	var sha string
	if json.Unmarshal(it.Extras[ark.ExtraInventory], &sha) != nil {
		return ""
	}
	return sha
}

func (x *driveExtras) remember(rootID, sha string) error {
	raw, _ := json.Marshal(sha)
	_, err := x.Journal.Update(rootID, ark.ExtrasItemID, func(i *journal.Item) {
		if i.State == "" {
			i.State = journal.StateSkipped // a record that is not an item; no planner state applies
		}
		extras := make(map[string]json.RawMessage, len(i.Extras)+1)
		for k, v := range i.Extras {
			extras[k] = v
		}
		extras[ark.ExtraInventory] = raw
		i.Extras = extras
	})
	return err
}
