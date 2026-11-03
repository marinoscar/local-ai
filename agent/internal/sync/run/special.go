package run

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The two runs that are not syncs (ark-sync.md section 3.5): a verify run (POST /ark/verify, trigger
// verify) re-hashes what is stored and plans nothing, and an import run (POST /ark/import, trigger
// import) copies from a drive with the network refused. They share the run slot, the journal, the
// events, the report and the end-of-run READY refresh with every other run; this file holds what
// differs.

// ---- verify ---------------------------------------------------------------------------------------

// runContext is what the hooks of a run are told, with the callbacks that put their results into the
// run.
func (e *Engine) runContext(rs *runState) RunContext {
	return RunContext{
		RunID: rs.id, Trigger: rs.trigger, Roots: rs.roots, Run: rs.run,
		Begin:  rs.beginChecks,
		Record: rs.recordCheck,
	}
}

// executeVerify is the body of a verify run: the Verify hook does the work and reports each copy it
// checked. A run with no hook fails (LAI-AGT-099) instead of claiming that everything is fine.
func (e *Engine) executeVerify(rs *runState) {
	if e.d.Hooks.Verify == nil {
		e.d.Logger.Error("a verify run was started but no verifier is wired", "component", "sync", "run_id", rs.id)
		rs.mu.Lock()
		rs.state, rs.code = StateFailed, doctor.AGT099
		rs.warnings = append(rs.warnings, "this computer cannot check its stored content yet")
		rs.mu.Unlock()
		return
	}
	rc := e.runContext(rs)
	req := VerifyRequest{Roots: rs.roots, Full: rs.full, Settings: rs.settings}
	for id := range rs.items {
		req.Items = append(req.Items, id)
	}
	sort.Strings(req.Items)
	e.safe("verify", func() { e.d.Hooks.Verify(rs.ctx, rc, req) })
}

// beginChecks adds the copies a verify run is about to read to its totals.
func (rs *runState) beginChecks(items int, bytes int64) {
	if rs.trigger != TriggerVerify {
		return
	}
	rs.mu.Lock()
	rs.totals.Items += items
	rs.totals.BytesTotal += bytes
	rs.mu.Unlock()
}

// recordCheck settles one checked copy: counters, the item list, sync.item_done, and the root is
// marked touched so READY is regenerated for it at the end. In a run that is not a verify run only a
// copy that turned out damaged is recorded (a nightly sync does not list every file it re-checked).
func (rs *runState) recordCheck(r ItemResult, bytes int64) {
	isVerify := rs.trigger == TriggerVerify
	if !isVerify && (r.Result == "verified" || r.Result == "skipped") {
		if r.Result == "verified" && r.Root != "" {
			// Not listed, but its last_reverified_at moved: READY of that root is regenerated at the end.
			rs.mu.Lock()
			rs.touched[r.Root] = true
			rs.mu.Unlock()
		}
		return
	}
	rs.mu.Lock()
	switch {
	case !isVerify:
		rs.totals.Items++
		rs.totals.Failed++
	case r.Result == "verified":
		rs.totals.Done++
	case r.Result == "skipped":
		if r.Root == "" {
			rs.totals.Items++ // an item that was asked for and is nowhere: not among the copies Begin counted
		}
		rs.totals.Skipped++
	default:
		rs.totals.Failed++
	}
	rs.totals.BytesDone += bytes
	rs.results = append(rs.results, r)
	if r.Root != "" {
		rs.touched[r.Root] = true
	}
	rs.mu.Unlock()
	rs.e.d.Logger.Info("item checked", "component", "sync", "run_id", rs.id, "root", r.Root, "item", r.Item, "version", r.Version, "result", r.Result, "code", r.Code, "reason", r.Reason)
	rs.e.publish(events.TypeSyncItemDone, rs.id, events.SyncItemDone{RunID: rs.id, Root: r.Root, Item: r.Item, Result: r.Result, Version: r.Version, Reason: r.Reason, Code: r.Code})
}

// ---- import ---------------------------------------------------------------------------------------

// resolveTask is the resolving step of a task: an import run copies from its drive and from nowhere
// else; any other run follows resolve.
func (e *Engine) resolveTask(rs *runState, ctx context.Context, t *task) (item.Resolution, error) {
	if rs.trigger == TriggerImport {
		return e.resolveImport(t)
	}
	return e.resolve(ctx, t)
}

// ErrNotFromDrive: an import task whose plan row does not copy from a drive. The importer plans every
// row it makes as a local copy; anything else would need the network, and an import never uses it.
var ErrNotFromDrive = errors.New("run: an import copies from a drive only")

// resolveImport builds the resolution of an import task: the files of the drive's verified copy, each
// read through a local source and hashed against the drive's own MANIFEST (a mismatch is LAI-SYN-070),
// and, where the catalog pins a checksum for a file, against that too. For an item the catalog does not
// pin (computed or tofu trust) the drive's MANIFEST value is the only one there is and is trusted; the
// manifest keeps its trust level (ark-sync.md section 4.14).
func (e *Engine) resolveImport(t *task) (item.Resolution, error) {
	if t.row.Source != plan.SourceLocalCopy || t.row.FromRoot == "" {
		return item.Resolution{}, &item.ResolveError{Code: doctor.SYN033, Permanent: true, Err: ErrNotFromDrive}
	}
	res, err := LocalCopy(e.d.FS, e.d.Copies, t.row)
	if err != nil {
		return res, err
	}
	pin, perr := Pinned(t.cat, t.row, t.catalogVersion)
	if perr != nil {
		return res, nil // the catalog pins nothing for this item
	}
	want := map[string]string{}
	for _, f := range pin.Files {
		want[f.Path] = f.SHA256
	}
	for _, f := range res.Files {
		if w, ok := want[f.Path]; ok && w != f.SHA256 {
			return item.Resolution{}, &item.ResolveError{
				Code: doctor.SYN021, Permanent: true,
				Err: fmt.Errorf("the drive's copy of %s is not the file the catalog pins", f.Path),
			}
		}
	}
	return res, nil
}
