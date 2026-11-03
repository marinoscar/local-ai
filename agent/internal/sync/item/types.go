// Package item is the per-item state machine of ark sync (docs/specs/ark-sync.md section 4.5): one
// instance per (root, item, target version) that moves a download through verify, staging and an
// atomic swap committed by MANIFEST.json, quarantines whatever fails its checksum, and survives a
// crash at any step.
//
//	planned -> resolving -> downloading -> verifying -> staged -> swapping -> swapped -> verified
//	                              \-> quarantined | failed | skipped
//
// The promises, each with its own test:
//
//   - Write-ahead: every transition is appended to the run journal (fsync) before the side effect it
//     announces, then written to items.json (journal.Journal.Transition).
//   - Verification re-reads the whole file from disk (fsync, posix_fadvise DONTNEED, hash again); the
//     downloader's digest is not trusted. A file that fails is quarantined with a reason file, never
//     deleted. A mismatch after a resumed download restarts the file from zero once.
//   - A swap is crash safe at every step. The plan is journaled first, the old version is moved to
//     .trash (never deleted) before the new one moves in, and MANIFEST.json is renamed in LAST: until
//     then the item counts as not present, so a half-swapped directory never looks verified. Replay
//     redoes the plan, skipping the renames whose source is gone and whose destination is there; a
//     destination that is gone with its source is a failed swap (LAI-SYN-026) and the old version is
//     restored from .trash (rule R4).
//   - Cancellation leaves the .part files and their checkpoints, so the next run resumes exactly there.
//
// What it does not do: plan (internal/sync/plan), schedule (internal/sync/run), resolve a source
// (SYNC-09 to SYNC-11 provide the Resolution) or delete (internal/sync/retention).
package item

import (
	"context"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The doctor codes of this package. They alias the doctor table.
const (
	// CodeSwapFailed: a swap could not complete (LAI-SYN-026).
	CodeSwapFailed = doctor.SYN026
	// CodeQuarantined: a download did not match its checksum (LAI-SYN-020).
	CodeQuarantined = doctor.SYN020
	// CodeRepeatFailure: the same item failed in three runs in a row (LAI-SYN-035).
	CodeRepeatFailure = doctor.SYN035
	// CodeUnresolved: the resolver could not tell what to fetch (LAI-SYN-033).
	CodeUnresolved = doctor.SYN033
	// CodeDiskFull: not enough free space (LAI-SYN-040).
	CodeDiskFull = doctor.SYN040
	// CodePurged: old versions were deleted to make room (LAI-SYN-043, information).
	CodePurged = doctor.SYN043
	// CodeRestarted: the file restarted from zero (LAI-SYN-032, information).
	CodeRestarted = doctor.SYN032
	// CodeInternal: a bug or an unexpected failure (LAI-AGT-099).
	CodeInternal = doctor.AGT099
)

// Reasons of a skipped item (sync.item_done reason). The planner's reasons (budget, disk, unresolved,
// time_limit, read_only) are shared; blocked is the machine's.
const (
	ReasonDisk       = "disk"
	ReasonUnresolved = "unresolved"
	// ReasonBlocked: the version was quarantined twice in a row and stays blocked until the catalog
	// changes its version (LAI-SYN-020).
	ReasonBlocked = "blocked"
)

// TreeDir is the directory of a directory item's content inside staging: .staging/<item>/<version>/tree,
// swapped into the item directory with renameat2(RENAME_EXCHANGE).
const TreeDir = "tree"

// PendingManifest is the name of the manifest the swap commits: written (atomically, fsynced) into
// the staging directory before the swap starts, renamed into the item directory as the last step.
const PendingManifest = manifest.FileName

// File is one file of an item version, as the resolver describes it.
type File struct {
	// Path is relative to the item directory, a clean slash path (for a directory item: relative to
	// the tree).
	Path string
	// Kind is the manifest's file kind (weights, mmproj, zim, ...).
	Kind string
	// URL is the https source; "" for a local copy (the Source is used).
	URL string
	// Size is the expected size in bytes, 0 when unknown (a computed checksum).
	Size int64
	// SHA256 is the expected hex digest, "" when unknown (computed: the digest the download reports
	// is recorded).
	SHA256 string
	// SHA256Source says how the digest was established (catalog+hf_lfs, kiwix_sidecar, computed, ...).
	SHA256Source string
	// Auth sends the Hugging Face token (only ever to its own host).
	Auth bool
	// ZimMD5OK is the result of the ZIM internal checksum; nil for anything that is not a ZIM.
	ZimMD5OK *bool
	// Source replaces HTTP for this file (a local copy of another root's file).
	Source download.Source
}

// Resolution is what a resolver (or the catalog's own pins) says about the target version of an item:
// the version, where it comes from and which files make it up. It is the engine-facing counterpart of
// plan.Resolved (version and size only).
type Resolution struct {
	// Version and SortKey identify the version; an empty SortKey becomes the sync time.
	Version, SortKey string
	// Source and Trust go into the manifest.
	Source manifest.Source
	Trust  manifest.Trust
	// Files are the files to fetch; for a directory item they are optional (the tree may come from
	// Build instead).
	Files []File
	// Dir marks a directory item: its content is staged under tree/ and swapped as a whole.
	Dir bool
	// Build produces a directory item's tree (git, rendered docs, apt closure) into treeDir. It must
	// tolerate a partly built tree (a resumed run) and never touch anything outside treeDir.
	Build func(ctx context.Context, treeDir string) error
	// ExpectedTree is the tree hash a directory item must have; "" means computed.
	ExpectedTree string
	// Resolver names the resolver in a quarantine reason ("huggingface", "local_copy", ...).
	Resolver string
	// Local marks a copy from another root: it takes no network slot and bypasses the bandwidth bucket.
	Local bool
	// Exclusive names a resource of which only one build may run at a time ("pmtiles"); "" for none.
	Exclusive string
	// Produce makes a directory item in one call (a git mirror with its tarballs, the rendered docs, the
	// kit catalog, an apt closure) instead of Files or Build: it runs while the item is downloading,
	// under the network slot, and says what it made (Produced). The machine keeps that answer in the
	// staging directory (PRODUCED.json), so a run that finds the item staged swaps it without calling
	// Produce again. Requires Dir.
	Produce func(ctx context.Context, env ProduceEnv) (Produced, error)
}

// TotalBytes is the sum of the files' expected sizes.
func (r Resolution) TotalBytes() int64 {
	var n int64
	for _, f := range r.Files {
		n += f.Size
	}
	return n
}

// Gate limits what runs at once. The engine's implementation holds sync.concurrency network slots and
// the one-at-a-time resources; a nil Gate limits nothing.
type Gate interface {
	// Network takes a network download slot; call release when the download phase ends.
	Network(ctx context.Context) (release func(), err error)
	// Exclusive takes the named single-slot resource.
	Exclusive(ctx context.Context, key string) (release func(), err error)
}

// Progress is a progress report of one item.
type Progress struct {
	State journal.ItemState
	// Done and Total are bytes (of the download, or of the verification re-read).
	Done, Total int64
}

// Job is one item on one root, as the engine hands it to the machine.
type Job struct {
	// Root is the content root the item is written to.
	Root ark.Root
	// Ref names the item for ark.ItemDir; RelDir is that directory relative to the root (slash).
	Ref    ark.ItemRef
	RelDir string
	// CatalogVersion goes into the manifest.
	CatalogVersion string
	// Resolve returns the resolution. It is called in state resolving; a nil Resolve means Resolution
	// is used.
	Resolve    func(ctx context.Context) (Resolution, error)
	Resolution *Resolution
	// MinFree is the root's min_free_bytes.
	MinFree int64
	// Gate limits network slots and exclusive resources; may be nil.
	Gate Gate
	// Progress receives progress reports; may be nil. It must not block.
	Progress func(Progress)
	// Swap is the swap plan an interrupted run left (journal.Intent.Swap); the machine finishes it.
	Swap *journal.SwapPlan
}

// Result is how a Run ended.
type Result struct {
	Root, Item, Version string
	// State is the state the item ended in: verified, failed, quarantined or skipped; for a cancelled
	// run the in-flight state (downloading, verifying, ...), which the next run resumes.
	State journal.ItemState
	// Cancelled: the context ended; the item keeps its .part files and checkpoints.
	Cancelled bool
	// Reason is the skip reason (disk, unresolved, blocked) or the failure kind (permanent, exhausted,
	// swap, internal); empty for verified.
	Reason string
	// Code is the doctor code of the failure or skip.
	Code string
	// Err is the cause, for the log; it is never shown to a user.
	Err error
	// Retryable: the next run may succeed without any change (transient network failure, disk).
	Retryable bool
	// RootFault is set when the root turned out unwritable (EIO, EROFS): the engine stops writing to it.
	RootFault *download.Error
	// Notices are information codes for the run log (CodePurged, CodeRestarted).
	Notices []string
	// Bytes is the size of the verified version (verified) or the bytes on disk (otherwise).
	Bytes int64
	// Quarantined is the directory the files were moved to, when State is quarantined.
	Quarantined string
}

// Fetcher is the downloader as the machine uses it: *download.Downloader implements it.
type Fetcher interface {
	Fetch(ctx context.Context, j download.FileJob, cp download.Checkpointer) (download.Result, error)
	Commit(j download.FileJob, cp download.Checkpointer) (string, error)
	EnsureSpace(ctx context.Context, rootID, rootPath string, needBytes, minFree int64) (purged int64, err error)
}

// Indexer is the best-copy index: *ark.Index implements it.
type Indexer interface {
	Refresh(ctx context.Context, root ark.Root, relDir string) ([]ark.Problem, error)
}

// Remover deletes staging (internal/sync/retention); the machine itself never deletes.
type Remover interface {
	RemoveStaging(path string) error
}

// Deps are the machine's collaborators. FS, Journal, Fetcher and Index are required.
type Deps struct {
	FS      FS
	Journal *journal.Journal
	Fetcher Fetcher
	Index   Indexer
	// Remover cleans staging after a swap and after a permanent failure; nil leaves staging to the
	// retention pass at the end of the run.
	Remover Remover
	// Roots resolves a root id (for Recover); may be nil when Recover is not used.
	Roots func() []ark.Root
	// Validators are the content validators; nil means none.
	Validators *Validators
	// Restorer restores the previous version after a failed swap; nil uses the machine's own
	// (TrashRestorer).
	Restorer Restorer
	// VerifyReread re-reads every file after the download (sync.verify_reread); the zero value is
	// false, NewDeps-style callers set it from the settings.
	VerifyReread func() bool
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// NodeID and AgentVersion go into the manifest's synced_by.
	NodeID, AgentVersion string
	Logger               *slog.Logger
}
