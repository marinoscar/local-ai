// Package verify is the bit-rot defence of ark sync (docs/specs/ark-sync.md sections 4.13 and 4.1.3):
// the budgeted re-verification that re-hashes what is stored, and the serve-time verification of a copy
// on a root the kernel flagged as unclean.
//
//	oldest last_reverified_at first -> hash every file -> match: stamp MANIFEST.last_reverified_at
//	                                                   -> mismatch: LAI-SYN-025, quarantine, restore
//
// A disk rots silently: a model or a ZIM that was verified a year ago may be corrupt the day it is
// needed. Reverifier spends up to a time budget (sync.reverify_budget_minutes) re-reading the copies
// that are due, oldest first, and stops between files, never in the middle of one, so a file that was
// partly read keeps its old last_reverified_at. A copy that no longer matches its MANIFEST:
//
//   - on a writable root is moved whole into <root>/.staging/quarantine (nothing is deleted), after an
//     older version in .trash that still hashes to its own manifest was put back first (rule R4,
//     retention.Restore). The item is then "missing" or "stale" in READY and the next sync fetches it;
//   - on a root that cannot be written (a read-only mount, an unclean filesystem) is excluded from the
//     best-copy index in memory (ark.Index.Exclude) and nothing is written to the disk;
//   - is reported with LAI-SYN-025, which is red for a tier-1 item.
//
// Reads use posix_fadvise(SEQUENTIAL) and drop what they read (POSIX_FADV_DONTNEED), so checking a drive
// does not push the weights of a loaded model out of the page cache. The package reads through FS and
// moves things through it; it deletes nothing (internal/sync/retention is the only package that does).
package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

// CodeBitRot is LAI-SYN-025: a stored file no longer matches its checksum.
const CodeBitRot = doctor.SYN025

// Results of an Outcome. They are the results of sync.item_done.
const (
	ResultVerified    = "verified"
	ResultQuarantined = "quarantined"
	ResultSkipped     = "skipped"
	ResultFailed      = "failed"
)

// Reasons of an Outcome that is not a plain "verified".
const (
	// ReasonBitRot: the copy was quarantined; the next sync fetches it again.
	ReasonBitRot = "bit_rot"
	// ReasonRestored: the copy was quarantined and the previous version put back from .trash (R4); READY
	// shows the item as stale until the next sync brings the current version.
	ReasonRestored = "restored_from_trash"
	// ReasonExcluded: the copy is damaged on a root that cannot be written; it is excluded from the
	// best-copy index in memory, and nothing was written.
	ReasonExcluded = "excluded_read_only"
	// ReasonQuarantineFailed: the copy is damaged and could not be moved aside (a full disk, a root that
	// turned read-only); it is excluded in memory until the root is mounted again.
	ReasonQuarantineFailed = "quarantine_failed"
	// ReasonRestoreFailed: putting an older version back failed half way; the copy is excluded in memory
	// and `aictl ark verify` run again settles it.
	ReasonRestoreFailed = "restore_failed"
	// ReasonBusy: the journal says a swap of the item is in flight; it is looked at next time.
	ReasonBusy = "busy"
	// ReasonChanged: the copy was replaced while it was being read; it is looked at next time.
	ReasonChanged = "changed"
	// ReasonNotPresent: an item that was asked for has no copy on the roots checked.
	ReasonNotPresent = "not_present"
	// ReasonTimeLimit: the time budget ended before the item was reached.
	ReasonTimeLimit = "time_limit"
)

// Outcome is what became of one copy.
type Outcome struct {
	Root, Item, Version string
	// Result is one of the Result constants.
	Result string
	Reason string
	// Code is the doctor code (LAI-SYN-025 for a damaged copy).
	Code string
	// Bytes is how many bytes were read.
	Bytes int64
	// Restored is the version put back from .trash when Reason is ReasonRestored.
	Restored string
}

// Candidate is a copy that is due, as Run lists it.
type Candidate struct {
	Root, Item, Version string
	Bytes               int64
	LastReverifiedAt    time.Time
}

// Options is one pass.
type Options struct {
	// Roots are root ids; empty means every mounted root that can be read (read-only and unclean ones
	// included: a read-only drive is checked, never written).
	Roots []string
	// Items limits the pass to these item ids. A named item is checked whatever its age.
	Items []string
	// Full ignores DueDays and checks everything.
	Full bool
	// DueDays is sync.reverify_days: a copy checked more recently than this is not due. Zero means every
	// copy is due. Ignored with Full and for a named item.
	DueDays int
	// Budget is the wall-clock time the pass may spend; zero means no limit. It is checked between
	// files, so a pass ends within Budget plus one file.
	Budget time.Duration
	// Begin is told how many copies and bytes the pass is about to read.
	Begin func(items int, bytes int64)
	// Record is called as each copy settles.
	Record func(Outcome)
	// Progress receives the bytes read so far of the copy being checked; it must not block.
	Progress func(root, item string, done, total int64)
}

// Report is what a pass did.
type Report struct {
	// Outcomes are the settled copies in the order they were checked.
	Outcomes []Outcome
	// Left are the due copies the budget ended before.
	Left []Candidate
	// Stopped: the budget ended the pass.
	Stopped bool
}

// Counts of a report.
func (r Report) count(result string) int {
	n := 0
	for _, o := range r.Outcomes {
		if o.Result == result {
			n++
		}
	}
	return n
}

// Verified is how many copies matched their manifests.
func (r Report) Verified() int { return r.count(ResultVerified) }

// Damaged is how many copies did not.
func (r Report) Damaged() int { return r.count(ResultQuarantined) + r.count(ResultFailed) }

// Index is the part of *ark.Index the pass uses.
type Index interface {
	Refresh(ctx context.Context, root ark.Root, relDir string) ([]ark.Problem, error)
	Exclude(rootID, itemID, version string)
	MarkServeVerified(rootID, itemID, version string)
}

// Restorer is the part of *retention.Retention that puts an older version back (rule R4).
type Restorer interface {
	Restore(ctx context.Context, rootID, itemID string) (retention.RestoreResult, error)
}

// Journal is the part of *journal.Items the pass uses.
type Journal interface {
	Get(rootID, itemID string) (journal.Item, bool)
	Update(rootID, itemID string, fn func(*journal.Item)) (journal.Item, error)
}

// Deps are the collaborators of a Reverifier. FS, Roots and Index are required.
type Deps struct {
	FS    FS
	Roots func() []ark.Root
	// Scan lists the copies of a root with their manifests (no hashing); nil reads them with
	// ark.ScanRoot. It lists every copy on the disk, including those on an unclean root, which the
	// best-copy index hides until they are verified.
	Scan  func(ctx context.Context, root ark.Root) ([]ark.ScannedItem, error)
	Index Index
	// Restore puts back an older version from .trash; nil means a damaged copy is only quarantined.
	Restore Restorer
	// Journal records the damage so READY shows quarantined; nil records nothing.
	Journal Journal
	// Now is the clock; time.Now when nil.
	Now    func() time.Time
	Logger *slog.Logger
}

// Reverifier re-hashes stored copies. Safe for concurrent use only through separate passes; the sync
// engine runs one at a time, under the run slot.
type Reverifier struct{ d Deps }

// New checks the dependencies and returns a Reverifier.
func New(d Deps) (*Reverifier, error) {
	var missing []string
	if d.FS == nil {
		missing = append(missing, "FS")
	}
	if d.Roots == nil {
		missing = append(missing, "Roots")
	}
	if d.Index == nil {
		missing = append(missing, "Index")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("verify: needs %v", missing)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Scan == nil {
		fsys := d.FS
		d.Scan = func(ctx context.Context, root ark.Root) ([]ark.ScannedItem, error) {
			items, _, err := ark.ScanRoot(ctx, fsys, root)
			return items, err
		}
	}
	return &Reverifier{d: d}, nil
}

// rootWritable: a pass may write to the root (stamp manifests, quarantine). A read-only mount and an
// unclean filesystem are never written (ark-sync.md sections 4.1.2 and 4.1.3).
func rootWritable(r ark.Root) bool {
	return !r.ReadOnly && r.State != ark.StateReadOnly && r.State != ark.StateUnclean
}

// rootReadable: a root a pass can read from.
func rootReadable(r ark.Root) bool {
	return r.Path != "" && (r.State == "" || r.State == ark.StateReady || r.State == ark.StateReadOnly || r.State == ark.StateUnclean)
}

type cand struct {
	Candidate
	root  ark.Root
	scan  ark.ScannedItem
	order int
}

// Candidates lists the copies a pass with these options would check, in order: the oldest
// last_reverified_at first (never-checked copies before any), then the root order, then the item id.
// It reads manifests only.
func (r *Reverifier) Candidates(ctx context.Context, o Options) ([]Candidate, error) {
	cs, _, err := r.candidates(ctx, o)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(cs))
	for i, c := range cs {
		out[i] = c.Candidate
	}
	return out, nil
}

func (r *Reverifier) candidates(ctx context.Context, o Options) ([]cand, []string, error) {
	want := map[string]bool{}
	for _, id := range o.Roots {
		want[id] = true
	}
	items := map[string]bool{}
	for _, id := range o.Items {
		items[id] = true
	}
	now := r.d.Now()
	var out []cand
	found := map[string]bool{}
	for order, root := range r.d.Roots() {
		if (len(want) > 0 && !want[root.ID]) || !rootReadable(root) {
			continue
		}
		scanned, err := r.d.Scan(ctx, root)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range scanned {
			m := it.Manifest
			if len(items) > 0 {
				if !items[m.ItemID] {
					continue
				}
				found[m.ItemID] = true
			} else if !o.Full && o.DueDays > 0 && !m.LastReverifiedAt.IsZero() &&
				now.Sub(m.LastReverifiedAt) < time.Duration(o.DueDays)*24*time.Hour {
				continue // checked recently enough
			}
			out = append(out, cand{
				Candidate: Candidate{Root: root.ID, Item: m.ItemID, Version: m.Version, Bytes: copyBytes(it), LastReverifiedAt: m.LastReverifiedAt},
				root:      root, scan: it, order: order,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.LastReverifiedAt.Equal(b.LastReverifiedAt) {
			return a.LastReverifiedAt.Before(b.LastReverifiedAt)
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return a.Item < b.Item
	})
	var notFound []string
	for _, id := range o.Items {
		if !found[id] {
			notFound = append(notFound, id)
		}
	}
	return out, notFound, nil
}

// Run checks the copies that are due and settles each one. It returns when they are all done, the
// budget ended, or ctx did (then with its error, and the report of what was settled so far). It never
// leaves a copy half moved: a quarantine is one rename, a restore is retention's crash-safe move.
func (r *Reverifier) Run(ctx context.Context, o Options) (Report, error) {
	start := r.d.Now()
	cs, notFound, err := r.candidates(ctx, o)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	record := func(out Outcome) {
		rep.Outcomes = append(rep.Outcomes, out)
		if o.Record != nil {
			o.Record(out)
		}
	}
	for _, id := range notFound {
		record(Outcome{Item: id, Result: ResultSkipped, Reason: ReasonNotPresent})
	}
	if o.Begin != nil {
		var total int64
		for _, c := range cs {
			total += c.Bytes
		}
		o.Begin(len(cs), total)
	}
	stopped := func() bool { return o.Budget > 0 && r.d.Now().Sub(start) >= o.Budget }
	between := func() error {
		if stopped() {
			return errStopped
		}
		return nil
	}
	for i, c := range cs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if stopped() {
			rep.Stopped = true
			for _, left := range cs[i:] {
				rep.Left = append(rep.Left, left.Candidate)
			}
			return rep, nil
		}
		out, err := r.checkOne(ctx, c, between, o)
		switch {
		case errors.Is(err, errStopped):
			rep.Stopped = true
			for _, left := range cs[i:] {
				rep.Left = append(rep.Left, left.Candidate)
			}
			return rep, nil
		case err != nil:
			return rep, err
		}
		record(out)
	}
	return rep, nil
}

// checkOne reads one copy and settles it. It returns errStopped or the context's error when the read was
// cut short (the copy is untouched), and nothing else.
func (r *Reverifier) checkOne(ctx context.Context, c cand, between func() error, o Options) (Outcome, error) {
	out := Outcome{Root: c.Root, Item: c.Item, Version: c.Version}
	log := r.d.Logger.With("component", "sync", "root", c.Root, "item", c.Item, "version", c.Version)
	if r.d.Journal != nil {
		if rec, ok := r.d.Journal.Get(c.Root, c.Item); ok && (rec.State == journal.StateSwapping || rec.State == journal.StateSwapped) {
			out.Result, out.Reason = ResultSkipped, ReasonBusy
			return out, nil
		}
	}
	var progress func(int64)
	if o.Progress != nil {
		progress = func(n int64) { o.Progress(c.Root, c.Item, n, c.Bytes) }
	}
	chk, err := checkCopy(ctx, r.d.FS, c.scan.Dir, c.scan.Manifest, between, progress)
	if err != nil {
		return out, err
	}
	out.Bytes = chk.Bytes
	if chk.OK {
		out.Result = ResultVerified
		r.markGood(ctx, c, &out, log)
		return out, nil
	}
	log.Error("a stored copy no longer matches its checksum", "code", CodeBitRot, "file", chk.Bad.Path, "problem", chk.Bad.Reason, "err", chk.Bad.Err)
	out.Code = CodeBitRot
	r.markBad(ctx, c, chk.Bad, &out, log)
	return out, nil
}

// markGood stamps last_reverified_at on a copy that matched. A root that cannot be written is not
// touched (the copy is simply fine); a failed stamp is not a failed check.
func (r *Reverifier) markGood(ctx context.Context, c cand, out *Outcome, log *slog.Logger) {
	if !rootWritable(c.root) {
		if c.root.State == ark.StateUnclean {
			r.d.Index.MarkServeVerified(c.root.ID, c.Item, c.Version)
		}
		return
	}
	cur, err := manifest.Read(r.d.FS, manifest.Path(c.scan.Dir))
	if err != nil || cur.Version != c.Version {
		out.Result, out.Reason = ResultSkipped, ReasonChanged
		return
	}
	at := r.d.Now().UTC().Truncate(time.Second)
	if at.Before(cur.VerifiedAt) {
		at = cur.VerifiedAt
	}
	cur.LastReverifiedAt = at
	if err := manifest.Write(r.d.FS, manifest.Path(c.scan.Dir), cur); err != nil && !isNotDurable(err) {
		log.Warn("last_reverified_at was not written; the copy matched", "err", err)
		return
	}
	if rel, err := filepath.Rel(c.root.Path, c.scan.Dir); err == nil {
		if _, err := r.d.Index.Refresh(ctx, c.root, filepath.ToSlash(rel)); err != nil {
			log.Warn("the best-copy index was not refreshed after a re-verification", "err", err)
		}
	}
}

// markBad settles a copy that did not match: restore from .trash and quarantine on a writable root,
// exclusion in memory on one that is not, then the journal and the index.
func (r *Reverifier) markBad(ctx context.Context, c cand, bad BadFile, out *Outcome, log *slog.Logger) {
	exclude := func(result, reason string) {
		r.d.Index.Exclude(c.root.ID, c.Item, c.Version)
		out.Result, out.Reason = result, reason
		r.record(c, log)
	}
	if !rootWritable(c.root) {
		exclude(ResultQuarantined, ReasonExcluded)
		return
	}
	if r.d.Restore != nil && !c.scan.Manifest.IsDir() {
		res, err := r.d.Restore.Restore(ctx, c.root.ID, c.Item)
		switch {
		case err == nil:
			// retention moved the damaged files to quarantine, put the older version back, refreshed the
			// index and recorded the item as verified at the restored version.
			out.Result, out.Reason, out.Restored = ResultQuarantined, ReasonRestored, res.Version
			log.Warn("the damaged copy was set aside and the previous version put back from the trash", "code", CodeBitRot, "restored", res.Version)
			return
		case errors.Is(err, retention.ErrNoRestorable):
			// nothing to put back: quarantine it ourselves below.
		case errors.Is(err, retention.ErrItemBusy):
			out.Result, out.Reason, out.Code = ResultSkipped, ReasonBusy, ""
			return
		default:
			log.Error("restoring the previous version failed", "err", err)
			exclude(ResultFailed, ReasonRestoreFailed)
			return
		}
	}
	dir, err := r.quarantineCopy(c.root, c.scan, bad)
	if err != nil {
		log.Error("the damaged copy could not be moved to quarantine", "err", err)
		exclude(ResultFailed, ReasonQuarantineFailed)
		return
	}
	out.Result, out.Reason = ResultQuarantined, ReasonBitRot
	log.Warn("the damaged copy was set aside", "code", CodeBitRot, "quarantine", dir)
	if rel, err := filepath.Rel(c.root.Path, c.scan.Dir); err == nil {
		if _, err := r.d.Index.Refresh(ctx, c.root, filepath.ToSlash(rel)); err != nil {
			log.Warn("the best-copy index was not refreshed after a quarantine", "err", err)
		}
	}
	r.record(c, log)
}

// record writes the damage into the journal, so READY says quarantined (red for a tier-1 item) until
// the next sync fetches the item again.
func (r *Reverifier) record(c cand, log *slog.Logger) {
	if r.d.Journal == nil {
		return
	}
	_, err := r.d.Journal.Update(c.root.ID, c.Item, func(it *journal.Item) {
		it.State, it.Version, it.Code = journal.StateQuarantined, c.Version, CodeBitRot
		it.LastError = "the stored copy no longer matches its checksum"
		it.Attempts, it.PartialBytes, it.Checkpoints = 0, 0, nil
	})
	if err != nil {
		log.Warn("the journal was not told about the damaged copy", "err", err)
	}
}

func isNotDurable(err error) bool { return errors.Is(err, config.ErrNotDurable) }
