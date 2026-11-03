package item

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// ResolveError is what a resolver returns when it cannot say what to fetch. Permanent errors fail the
// item for this run without a retry hint; anything else is transient. Code is the doctor code
// (LAI-SYN-021, -022, -030, -033, -034, -061, ...); empty means LAI-SYN-033.
type ResolveError struct {
	Code      string
	Permanent bool
	Err       error
}

func (e *ResolveError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *ResolveError) Unwrap() error { return e.Err }

// Machine runs items through the state machine. It holds no per-item state: every Run is independent
// and the machine is safe for concurrent use by the scheduler's workers.
type Machine struct {
	d Deps
}

// New checks the dependencies and returns a Machine.
func New(d Deps) (*Machine, error) {
	var missing []string
	if d.FS == nil {
		missing = append(missing, "FS")
	}
	if d.Journal == nil {
		missing = append(missing, "Journal")
	}
	if d.Fetcher == nil {
		missing = append(missing, "Fetcher")
	}
	if d.Index == nil {
		missing = append(missing, "Index")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("item: the machine needs %v", missing)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Machine{d: d}, nil
}

func (m *Machine) now() time.Time { return m.d.Now() }

func (m *Machine) reread() bool { return m.d.VerifyReread == nil || m.d.VerifyReread() }

func (m *Machine) restorer() Restorer {
	if m.d.Restorer != nil {
		return m.d.Restorer
	}
	return TrashRestorer{m: m}
}

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// runner is the state of one Run.
type runner struct {
	m   *Machine
	ctx context.Context
	run *journal.Run
	job Job
	res Result

	rsl     Resolution
	version string
	cp      *download.JournalCheckpointer

	// digests holds the sha256 of each file as the downloader streamed it; resumed and restarted are
	// the per-file facts the re-read rule needs.
	digests   map[string]string
	sizes     map[string]int64
	resumed   map[string]bool
	restarted map[string]bool
	dlStart   time.Time
	// done is the bytes of the files that are complete; the progress of the current file is added.
	done int64
}

// Run takes one item on one root from wherever the journal says it is to a terminal state. It
// resumes: a record in downloading continues from its checkpoints, a staged one swaps, one found
// mid-swap is completed first. It never returns an error: the Result says how it ended, and a
// cancelled run (ctx done) leaves the item in its in-flight state with its .part files and
// checkpoints, which the next run resumes.
func (m *Machine) Run(ctx context.Context, run *journal.Run, job Job) Result {
	r := &runner{
		m: m, ctx: ctx, run: run, job: job,
		res:     Result{Root: job.Root.ID, Item: job.Ref.ID},
		digests: map[string]string{}, sizes: map[string]int64{}, resumed: map[string]bool{}, restarted: map[string]bool{},
	}
	return r.do()
}

func (r *runner) items() *journal.Items { return r.m.d.Journal.Items }

func (r *runner) log(level slog.Level, msg string, attrs ...any) {
	base := []any{"component", "sync", "root", r.job.Root.ID, "item", r.job.Ref.ID, "version", r.version, "run_id", r.run.ID}
	r.m.d.Logger.Log(context.Background(), level, msg, append(base, attrs...)...)
}

func (r *runner) report(state journal.ItemState, done, total int64) {
	if r.job.Progress != nil {
		r.job.Progress(Progress{State: state, Done: done, Total: total})
	}
}

// transition is journal.Journal.Transition for this item; a failure to write the journal ends the run
// of the item (nothing may happen that the journal did not announce).
func (r *runner) transition(to journal.ItemState, version string, fn func(*journal.Item)) error {
	_, err := r.m.d.Journal.Transition(r.run, r.job.Root.ID, r.job.Ref.ID, version, to, fn)
	if err != nil {
		r.log(slog.LevelError, "journal write failed", "to", string(to), "err", err.Error())
	}
	return err
}

func (r *runner) do() Result {
	rec, have := r.items().Get(r.job.Root.ID, r.job.Ref.ID)
	if have {
		r.version = rec.Version
	}

	// A swap an interrupted run left comes first: it is the only state where an item is half moved.
	if have && (rec.State == journal.StateSwapping || rec.State == journal.StateSwapped) {
		if res, ok := r.resumeSwap(rec); ok {
			return res
		}
	}

	if err := r.resolve(rec, have); err != nil {
		return r.res
	}
	rec, have = r.items().Get(r.job.Root.ID, r.job.Ref.ID)

	if !(have && rec.State == journal.StateStaged && rec.Version == r.version && r.stagedComplete()) {
		if !r.download() {
			return r.res
		}
		if !r.verify() {
			return r.res
		}
	}
	return r.swap()
}

// ---- resolving ------------------------------------------------------------------------------------

// resolve is the resolving state: the resolver says which version and files, and the item moves to
// downloading at that version. The returned error is only a signal that r.res is final.
func (r *runner) resolve(rec journal.Item, have bool) error {
	prevVer := ""
	if have {
		prevVer = rec.Version
	}
	enter := prevVer
	if enter == "" {
		enter = "unresolved"
	}
	if err := r.transition(journal.StateResolving, enter, nil); err != nil {
		return r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
	}
	switch {
	case r.job.Resolution != nil:
		r.rsl = *r.job.Resolution
	case r.job.Resolve != nil:
		rsl, err := r.job.Resolve(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				r.res.State, r.res.Cancelled = journal.StateResolving, true
				return err
			}
			var re *ResolveError
			code, permanent := CodeUnresolved, false
			if errors.As(err, &re) {
				if re.Code != "" {
					code = re.Code
				}
				permanent = re.Permanent
			}
			return r.finishAt(enter, journal.StateFailed, code, "unresolved", err, !permanent)
		}
		r.rsl = rsl
	default:
		return r.finishAt(enter, journal.StateFailed, CodeInternal, "internal", errors.New("item: the job has no resolution"), false)
	}
	rsl := r.rsl
	if !manifest.SafeName(rsl.Version) {
		return r.finishAt(enter, journal.StateFailed, CodeInternal, "internal", fmt.Errorf("item: resolver returned the unusable version %q", rsl.Version), false)
	}
	if err := validateResolution(rsl); err != nil {
		return r.finishAt(enter, journal.StateFailed, CodeInternal, "internal", err, false)
	}
	r.version, r.res.Version = rsl.Version, rsl.Version

	if have && Blocked(rec, rsl.Version) {
		r.res.State, r.res.Reason, r.res.Code = journal.StateSkipped, ReasonBlocked, CodeQuarantined
		r.res.Err = fmt.Errorf("version %s was quarantined %d times in a row and stays blocked until the catalog changes", rsl.Version, BlockAfter)
		return r.res.Err
	}

	if have && rec.State == journal.StateStaged && rec.Version == rsl.Version {
		return nil // resumes at the swap: no transition back to downloading
	}
	err := r.transition(journal.StateDownloading, rsl.Version, func(i *journal.Item) {
		if prevVer != rsl.Version {
			i.Attempts, i.PartialBytes, i.Checkpoints = 0, 0, nil
			i.Extras = clearQuarantine(*i)
		}
		i.Attempts++
		i.LastError, i.Code = "", ""
	})
	if err != nil {
		return r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
	}
	return nil
}

func validateResolution(rsl Resolution) error {
	seen := map[string]bool{}
	for _, f := range rsl.Files {
		if !manifest.ValidFilePath(f.Path) {
			return fmt.Errorf("item: resolver returned the unusable file path %q", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("item: resolver returned %q twice", f.Path)
		}
		seen[f.Path] = true
		if f.SHA256 != "" && !manifest.IsSHA256(f.SHA256) {
			return fmt.Errorf("item: the sha256 of %q is not 64 lowercase hex digits", f.Path)
		}
		if f.Source == nil && !rsl.Dir && f.URL == "" {
			return fmt.Errorf("item: file %q has neither a URL nor a source", f.Path)
		}
	}
	if !rsl.Dir && len(rsl.Files) == 0 {
		return errors.New("item: a file item has no files")
	}
	if rsl.Dir && len(rsl.Files) == 0 && rsl.Build == nil {
		return errors.New("item: a directory item has neither files nor a build")
	}
	if rsl.Trust != "" && !rsl.Trust.Valid() {
		return fmt.Errorf("item: trust %q is not valid", rsl.Trust)
	}
	return nil
}

// finish ends the item in a terminal state at the current version (the transition is journaled).
func (r *runner) finish(state journal.ItemState, code, reason string, cause error, retryable bool) error {
	return r.finishAt(r.version, state, code, reason, cause, retryable)
}

func (r *runner) finishAt(version string, state journal.ItemState, code, reason string, cause error, retryable bool) error {
	if version == "" {
		version = "unresolved"
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := r.transition(state, version, func(i *journal.Item) {
		i.Code, i.LastError = code, msg
	}); err != nil {
		r.res.Err = errors.Join(cause, err)
	} else {
		r.res.Err = cause
	}
	r.res.State, r.res.Code, r.res.Reason, r.res.Retryable = state, code, reason, retryable
	if r.res.Version == "" && version != "unresolved" {
		r.res.Version = version
	}
	r.log(slog.LevelWarn, "item "+string(state), "code", code, "reason", reason, "err", msg)
	if cause == nil {
		return errors.New(reason)
	}
	return cause
}

// ---- downloading ----------------------------------------------------------------------------------

// fileName is the file's name in staging and in its checkpoint: for a directory item it lives in tree/.
func (r *runner) fileName(f File) string {
	if r.rsl.Dir {
		return TreeDir + "/" + f.Path
	}
	return f.Path
}

func (r *runner) stagingDir() string {
	return filepath.Join(r.job.Root.Path, ".staging", r.job.Ref.ID, r.version)
}

func (r *runner) finalPath(f File) string {
	return filepath.Join(r.stagingDir(), filepath.FromSlash(r.fileName(f)))
}

func (r *runner) fileJob(f File) download.FileJob {
	name := r.fileName(f)
	j := download.FileJob{
		RootID: r.job.Root.ID, Root: r.job.Root.Path, Item: r.job.Ref.ID, Version: r.version, Name: name,
		URL: f.URL, ExpectedSize: f.Size, ExpectedSHA256: f.SHA256, Auth: f.Auth, MinFreeBytes: r.job.MinFree,
		Source: f.Source,
	}
	if j.Source != nil && j.URL == "" {
		j.URL = "local-copy://" + r.job.Root.ID + "/" + r.job.Ref.ID + "/" + name
	}
	return j
}

// complete reports whether the file is already in staging under its final name with its size: it was
// renamed there after it was verified, so it is not downloaded or verified again.
func (r *runner) complete(f File) bool {
	info, err := r.m.d.FS.Stat(r.finalPath(f))
	return err == nil && info.Mode().IsRegular() && (f.Size == 0 || info.Size() == f.Size)
}

// stagedComplete: every file of a staged item is in staging under its final name (a tree item: the
// tree with its SHA256SUMS).
func (r *runner) stagedComplete() bool {
	if r.rsl.Dir {
		_, err := manifest.ReadSums(r.m.d.FS, filepath.Join(r.stagingDir(), TreeDir))
		return err == nil
	}
	for _, f := range r.rsl.Files {
		if !r.complete(f) {
			return false
		}
	}
	return true
}

func (r *runner) withNetwork(fn func() bool) bool {
	if r.job.Gate == nil || r.rsl.Local {
		return fn()
	}
	release, err := r.job.Gate.Network(r.ctx)
	if err != nil {
		r.cancelled(journal.StateDownloading)
		return false
	}
	defer release()
	return fn()
}

func (r *runner) cancelled(state journal.ItemState) {
	r.res.State, r.res.Cancelled = state, true
	r.res.Err = r.ctx.Err()
}

// download is the downloading state: every file that is not in staging yet is fetched (resuming from
// its checkpoint) into its .part. It returns false when r.res is final.
func (r *runner) download() bool {
	r.cp = &download.JournalCheckpointer{Items: r.items(), Run: r.run, RootID: r.job.Root.ID, ItemID: r.job.Ref.ID, Version: r.version}
	r.dlStart = r.m.now()
	var need int64
	for _, f := range r.rsl.Files {
		if !r.complete(f) {
			need += f.Size
			if info, err := r.m.d.FS.Stat(r.finalPath(f) + ".part"); err == nil && f.Size > 0 {
				need -= min(info.Size(), f.Size)
			}
		}
	}
	if r.rsl.Dir && len(r.rsl.Files) == 0 {
		need = 0
	}
	if need > 0 {
		purged, err := r.m.d.Fetcher.EnsureSpace(r.ctx, r.job.Root.ID, r.job.Root.Path, need, r.job.MinFree)
		if purged > 0 {
			r.res.Notices = append(r.res.Notices, CodePurged)
		}
		if err != nil {
			return r.downloadError(File{}, download.Result{}, err)
		}
	}
	total := r.rsl.TotalBytes()
	return r.withNetwork(func() bool {
		for _, f := range r.rsl.Files {
			if err := r.ctx.Err(); err != nil {
				r.cancelled(journal.StateDownloading)
				return false
			}
			if r.complete(f) {
				r.done += f.Size
				r.report(journal.StateDownloading, r.done, total)
				continue
			}
			if !r.fetch(f, total) {
				return false
			}
			r.done += max(f.Size, r.sizes[f.Path])
		}
		if r.rsl.Build != nil {
			if !r.build() {
				return false
			}
		}
		return true
	})
}

// fetch downloads one file into its .part and records its streamed digest.
func (r *runner) fetch(f File, total int64) bool {
	job := r.fileJob(f)
	base := r.done
	job.OnProgress = func(off int64) { r.report(journal.StateDownloading, base+off, total) }
	res, err := r.m.d.Fetcher.Fetch(r.ctx, job, r.cp)
	if res.PurgedBytes > 0 {
		r.res.Notices = appendNotice(r.res.Notices, CodePurged)
	}
	for _, n := range res.Notices {
		r.res.Notices = appendNotice(r.res.Notices, n)
	}
	if err != nil {
		return r.downloadError(f, res, err)
	}
	r.digests[f.Path] = res.SHA256
	r.sizes[f.Path] = res.Bytes
	r.resumed[f.Path] = res.ResumedFrom > 0
	return true
}

func appendNotice(ns []string, n string) []string {
	for _, x := range ns {
		if x == n {
			return ns
		}
	}
	return append(ns, n)
}

// build runs a directory item's Build into the staged tree, one exclusive resource at a time.
func (r *runner) build() bool {
	tree := filepath.Join(r.stagingDir(), TreeDir)
	if err := r.m.d.FS.MkdirAll(tree, 0o755); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if g := r.job.Gate; g != nil && r.rsl.Exclusive != "" {
		release, err := g.Exclusive(r.ctx, r.rsl.Exclusive)
		if err != nil {
			r.cancelled(journal.StateDownloading)
			return false
		}
		defer release()
	}
	if err := r.rsl.Build(r.ctx, tree); err != nil {
		if r.ctx.Err() != nil {
			r.cancelled(journal.StateDownloading)
			return false
		}
		var re *ResolveError
		code, permanent := CodeUnresolved, false
		if errors.As(err, &re) {
			if re.Code != "" {
				code = re.Code
			}
			permanent = re.Permanent
		}
		_ = r.finish(journal.StateFailed, code, "build", err, !permanent)
		return false
	}
	return true
}

// downloadError maps what a Fetch (or EnsureSpace) returned to the item's outcome. It always returns
// false: the result is final.
func (r *runner) downloadError(f File, res download.Result, err error) bool {
	if r.ctx.Err() != nil && isCtxErr(err) {
		r.cancelled(journal.StateDownloading)
		return false
	}
	var de *download.Error
	if !errors.As(err, &de) {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	switch de.Kind {
	case download.KindMismatch:
		part := r.finalPath(f) + ".part"
		size := res.Bytes
		entry := reasonFile(f, r.fileName(f)+".part", res, size)
		r.quarantineFiles(de.Code, "mismatch", err, []ReasonFileEntry{entry}, []qFile{{Src: part, Rel: r.fileName(f) + ".part"}}, "", "")
	case download.KindPermanent:
		_ = r.finish(journal.StateFailed, de.Code, "permanent", err, false)
		r.cleanStaging()
	case download.KindExhausted:
		_ = r.finish(journal.StateFailed, de.Code, "exhausted", err, true)
	case download.KindDisk:
		_ = r.finish(journal.StateSkipped, de.Code, ReasonDisk, err, true)
	case download.KindRootFault:
		r.res.RootFault = de
		_ = r.finish(journal.StateFailed, de.Code, "root_fault", err, true)
	default:
		_ = r.finish(journal.StateFailed, de.Code, "internal", err, true)
	}
	return false
}

// cleanStaging deletes this version's staging directory (through retention): after a permanent
// failure the .part files are junk that no retry will resume.
func (r *runner) cleanStaging() {
	if r.m.d.Remover == nil {
		return
	}
	if err := r.m.d.Remover.RemoveStaging(r.stagingDir()); err != nil {
		r.log(slog.LevelWarn, "staging not removed", "err", err.Error())
	}
}

// quarantineFiles moves the offending files to quarantine with reason.json and journals the item as
// quarantined, counting the consecutive quarantines of this version.
func (r *runner) quarantineFiles(code, kind string, cause error, entries []ReasonFileEntry, files []qFile, validator, verr string) {
	reason := Reason{
		Item: r.job.Ref.ID, Root: r.job.Root.ID, Version: r.version, Code: code, Resolver: r.rsl.Resolver,
		Files: entries, Validator: validator, Error: verr,
	}
	if reason.Error == "" && cause != nil {
		reason.Error = cause.Error()
	}
	dir, qerr := quarantine(r.m.d.FS, r.job.Root.Path, reason, files, r.m.now())
	if qerr != nil {
		r.log(slog.LevelError, "quarantine failed", "err", qerr.Error())
		// The files stay where they are, but the item must not look good: it fails this run.
		_ = r.finish(journal.StateFailed, code, "quarantine_failed", errors.Join(cause, qerr), true)
		return
	}
	rec, _ := r.items().Get(r.job.Root.ID, r.job.Ref.ID)
	extras := bumpQuarantine(rec, r.version)
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := r.transition(journal.StateQuarantined, r.version, func(i *journal.Item) {
		i.Code, i.LastError, i.Extras = code, msg, extras
		i.Checkpoints, i.PartialBytes = nil, 0
	}); err != nil {
		r.res.Err = errors.Join(cause, err)
	} else {
		r.res.Err = cause
	}
	r.res.State, r.res.Code, r.res.Reason, r.res.Quarantined = journal.StateQuarantined, code, kind, dir
	r.log(slog.LevelWarn, "quarantined", "code", code, "reason", kind, "dir", dir)
}

// ---- verifying ------------------------------------------------------------------------------------

// verify is the verifying state: each file is fsynced, its cache dropped and, when sync.verify_reread
// is on, read back from disk and hashed; a match renames the .part to its final name inside staging.
// Then the content validators run over the staged files and the item is staged. It returns false when
// r.res is final.
func (r *runner) verify() bool {
	if err := r.transition(journal.StateVerifying, r.version, nil); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if r.rsl.Dir {
		// A tree made of downloaded files (a local copy) first commits each .part like a file item;
		// the tree pass then hashes everything once more, so the per-file pass skips its own re-read.
		if len(r.rsl.Files) > 0 && !r.verifyFiles(false) {
			return false
		}
		return r.verifyTree()
	}
	if !r.verifyFiles(true) {
		return false
	}
	if !r.validate() {
		return false
	}
	if err := r.transition(journal.StateStaged, r.version, nil); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	return true
}

// verifyFiles verifies and commits every file of the item in order, then makes the renames durable.
// reread says whether each file is read back from disk here (a tree reads everything back in its own
// pass). It returns false when r.res is final.
func (r *runner) verifyFiles(reread bool) bool {
	total := r.rsl.TotalBytes()
	var done int64
	for _, f := range r.rsl.Files {
		if r.ctx.Err() != nil {
			r.cancelled(journal.StateVerifying)
			return false
		}
		if !r.verifyFile(f, done, total, reread) {
			return false
		}
		done += max(f.Size, r.sizes[f.Path])
	}
	_ = r.m.d.FS.SyncDir(r.stagingDir())
	if r.rsl.Dir {
		_ = r.m.d.FS.SyncDir(filepath.Join(r.stagingDir(), TreeDir))
	}
	return true
}

// verifyFile verifies and commits one file. A re-read that differs from what was streamed after a
// resumed download restarts the file from zero once (ark-sync.md section 4.5); any other mismatch is
// quarantined.
func (r *runner) verifyFile(f File, doneBefore, total int64, reread bool) bool {
	fsys := r.m.d.FS
	final := r.finalPath(f)
	if r.complete(f) {
		if f.SHA256 == "" {
			// A computed file that was committed by an earlier run: its digest is on disk only.
			sum, size, err := hashFile(r.ctx, fsys, final, nil)
			if err != nil {
				return r.verifyIOError(err)
			}
			r.digests[f.Path], r.sizes[f.Path] = sum, size
		} else {
			r.digests[f.Path] = f.SHA256
			r.sizes[f.Path] = max(f.Size, r.sizes[f.Path])
		}
		return true
	}
	for pass := 0; ; pass++ {
		part := final + ".part"
		sum, size, err := verifyFile(r.ctx, fsys, part, reread && r.m.reread(), func(n int64) { r.report(journal.StateVerifying, doneBefore+n, total) })
		if err != nil {
			return r.verifyIOError(err)
		}
		streamed := r.digests[f.Path]
		actual := sum
		if actual == "" {
			actual = streamed
		}
		if size > 0 {
			r.sizes[f.Path] = size
		}
		mismatch := false
		switch {
		case f.SHA256 != "" && actual != f.SHA256:
			mismatch = true
		case sum != "" && streamed != "" && sum != streamed:
			mismatch = true
		}
		if mismatch {
			if r.resumed[f.Path] && !r.restarted[f.Path] && pass == 0 {
				// The bytes the downloader hashed are not the bytes on the disk, and the file was
				// resumed from a checkpoint: restart it from zero once.
				r.restarted[f.Path] = true
				r.res.Notices = appendNotice(r.res.Notices, CodeRestarted)
				r.log(slog.LevelWarn, "re-read differs after a resumed download, restarting the file", "file", f.Path)
				if err := r.cp.Clear(r.fileName(f)); err != nil {
					r.log(slog.LevelWarn, "checkpoint not cleared", "err", err.Error())
				}
				if !r.withNetwork(func() bool { return r.fetch(f, total) }) {
					return false
				}
				r.resumed[f.Path] = false
				continue
			}
			code := download.CodeChecksum
			if f.Source != nil {
				code = download.CodeSourceDamaged
			}
			entry := ReasonFileEntry{
				Path: r.fileName(f) + ".part", ExpectedSHA256: f.SHA256, ActualSHA256: actual, ExpectedBytes: f.Size, Bytes: size,
				URL: download.RedactURL(f.URL),
			}
			r.quarantineFiles(code, "mismatch", fmt.Errorf("the sha256 of %s read back from disk does not match", f.Path),
				[]ReasonFileEntry{entry}, []qFile{{Src: part, Rel: r.fileName(f) + ".part"}}, "", "")
			return false
		}
		r.digests[f.Path] = actual
		if err := fsys.Chmod(part, 0o644); err != nil {
			r.log(slog.LevelWarn, "chmod failed", "file", f.Path, "err", err.Error())
		}
		if _, err := r.m.d.Fetcher.Commit(r.fileJob(f), r.cp); err != nil {
			_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
			return false
		}
		return true
	}
}

// verifyIOError ends the item for a failure of the disk while verifying: cancelled when the context
// ended, a root fault for EIO and a read-only filesystem, a plain failure otherwise.
func (r *runner) verifyIOError(err error) bool {
	switch {
	case r.ctx.Err() != nil && isCtxErr(err):
		r.cancelled(journal.StateVerifying)
	case errors.Is(err, syscall.EIO):
		de := &download.Error{Kind: download.KindRootFault, Code: download.CodeIO, Err: err}
		r.res.RootFault = de
		_ = r.finish(journal.StateFailed, download.CodeIO, "root_fault", err, true)
	case errors.Is(err, syscall.EROFS):
		de := &download.Error{Kind: download.KindRootFault, Code: download.CodeReadOnly, Err: err}
		r.res.RootFault = de
		_ = r.finish(journal.StateFailed, download.CodeReadOnly, "root_fault", err, true)
	default:
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
	}
	return false
}

// verifyTree is verifying for a directory item: every file of the tree is hashed from disk, compared to
// what the resolver expects (per file and for the whole tree), SHA256SUMS is written and the validators
// run. It returns false when r.res is final.
func (r *runner) verifyTree() bool {
	fsys := r.m.d.FS
	tree := filepath.Join(r.stagingDir(), TreeDir)
	if err := fsys.MkdirAll(tree, 0o755); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	files, err := hashTree(r.ctx, fsys, tree, func(n int64) { r.report(journal.StateVerifying, n, r.rsl.TotalBytes()) })
	if err != nil {
		if errors.Is(err, ErrNotRegular) {
			r.quarantineTree(download.CodeChecksum, "not_regular", err, nil)
			return false
		}
		return r.verifyIOError(err)
	}
	if len(files) == 0 {
		r.quarantineTree(download.CodeChecksum, "empty", errors.New("the staged tree has no files"), nil)
		return false
	}
	expected := map[string]File{}
	for _, f := range r.rsl.Files {
		expected[f.Path] = f
	}
	var bad []ReasonFileEntry
	for _, tf := range files {
		if e, ok := expected[tf.Path]; ok && (e.SHA256 != "" && e.SHA256 != tf.SHA256 || e.Size > 0 && e.Size != tf.Size) {
			bad = append(bad, ReasonFileEntry{Path: TreeDir + "/" + tf.Path, ExpectedSHA256: e.SHA256, ActualSHA256: tf.SHA256, ExpectedBytes: e.Size, Bytes: tf.Size, URL: download.RedactURL(e.URL)})
		}
	}
	sums := make([]manifest.Sum, len(files))
	for i, tf := range files {
		sums[i] = manifest.Sum{SHA256: tf.SHA256, Path: tf.Path}
	}
	treeHash, err := manifest.TreeSHA256(sums)
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, false)
		return false
	}
	if len(bad) == 0 && r.rsl.ExpectedTree != "" && r.rsl.ExpectedTree != treeHash {
		bad = append(bad, ReasonFileEntry{Path: TreeDir, ExpectedSHA256: r.rsl.ExpectedTree, ActualSHA256: treeHash})
	}
	if len(bad) > 0 {
		r.quarantineTree(download.CodeChecksum, "mismatch", errors.New("the staged tree does not match what the source says"), bad)
		return false
	}
	if err := r.chmodTree(tree); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if err := manifest.WriteSums(fsys, tree, sums); err != nil && !isNotDurable(err) {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if !r.validate() {
		return false
	}
	if err := r.transition(journal.StateStaged, r.version, nil); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	return true
}

// chmodTree sets the modes a served tree needs: directories 0755 and files 0644 (the services that read
// content run as another user, ark-sync.md section 3.1).
func (r *runner) chmodTree(dir string) error {
	fsys := r.m.d.FS
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return err
	}
	if err := fsys.Chmod(dir, 0o755); err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := r.chmodTree(p); err != nil {
				return err
			}
			continue
		}
		if err := fsys.Chmod(p, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) quarantineTree(code, kind string, cause error, entries []ReasonFileEntry) {
	tree := filepath.Join(r.stagingDir(), TreeDir)
	r.quarantineFiles(code, kind, cause, entries, []qFile{{Src: tree, Rel: TreeDir}}, "", "")
}

// validate runs the content validators over the staged files. A failure quarantines the whole staged
// content.
func (r *runner) validate() bool {
	if r.m.d.Validators == nil {
		return true
	}
	t := Target{
		Meta:      Meta{ItemID: r.job.Ref.ID, Kind: r.job.Ref.Kind, Engine: r.job.Ref.Engine, SourceType: r.rsl.Source.Type},
		StagedDir: r.stagingDir(), Files: r.rsl.Files, Dir: r.rsl.Dir,
	}
	if r.rsl.Dir {
		t.StagedDir = filepath.Join(r.stagingDir(), TreeDir)
	}
	err := r.m.d.Validators.Run(r.ctx, t)
	if err == nil {
		return true
	}
	if r.ctx.Err() != nil && isCtxErr(err) {
		r.cancelled(journal.StateVerifying)
		return false
	}
	var ve *ValidationError
	code, name := CodeQuarantined, ""
	if errors.As(err, &ve) {
		code, name = ve.Code, ve.Validator
	}
	var files []qFile
	if r.rsl.Dir {
		files = []qFile{{Src: filepath.Join(r.stagingDir(), TreeDir), Rel: TreeDir}}
	} else {
		for _, f := range r.rsl.Files {
			files = append(files, qFile{Src: r.finalPath(f), Rel: f.Path})
		}
	}
	r.quarantineFiles(code, "validator", err, nil, files, name, err.Error())
	return false
}

// ---- staged, swapping, swapped, verified ----------------------------------------------------------

// buildManifest is the manifest the swap commits.
func (r *runner) buildManifest(old *manifest.Manifest) (manifest.Manifest, error) {
	now := r.m.now().UTC().Truncate(time.Second)
	rsl := r.rsl
	m := manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, ItemID: r.job.Ref.ID, ItemKind: r.job.Ref.Kind, Engine: r.job.Ref.Engine,
		CatalogVersion: r.job.CatalogVersion, Version: rsl.Version, SortKey: rsl.SortKey, State: manifest.StateVerified,
		Source: rsl.Source, Trust: rsl.Trust,
		DownloadedAt: r.dlStart, VerifiedAt: now, LastReverifiedAt: now,
		SyncedBy: manifest.SyncedBy{NodeID: r.m.d.NodeID, AgentVersion: r.m.d.AgentVersion, RunID: r.run.ID},
	}
	if m.SortKey == "" {
		m.SortKey = now.Format(time.RFC3339)
	}
	if m.Trust == "" {
		m.Trust = manifest.TrustPinned
	}
	if m.Source.Type == "" {
		m.Source.Type = "unknown"
	}
	if m.DownloadedAt.IsZero() || m.DownloadedAt.After(now) {
		m.DownloadedAt = now
	}
	m.DownloadedAt = m.DownloadedAt.UTC().Truncate(time.Second)
	if old != nil {
		m.History = old.SupersededBy(now)
	}
	if rsl.Dir {
		sums, err := manifest.ReadSums(r.m.d.FS, filepath.Join(r.stagingDir(), TreeDir))
		if err != nil {
			return manifest.Manifest{}, err
		}
		tree, err := manifest.TreeSHA256(sums)
		if err != nil {
			return manifest.Manifest{}, err
		}
		m.TreeSHA256, m.FileCount = tree, len(sums)
		for _, f := range rsl.Files {
			m.TotalBytes += f.Size
		}
		if m.TotalBytes == 0 {
			m.TotalBytes = r.treeBytes()
		}
		return m, nil
	}
	for _, f := range rsl.Files {
		sum := r.digests[f.Path]
		if f.SHA256 != "" {
			sum = f.SHA256
		}
		size := f.Size
		if r.sizes[f.Path] > 0 {
			size = r.sizes[f.Path]
		}
		if size == 0 {
			if info, err := r.m.d.FS.Stat(r.finalPath(f)); err == nil {
				size = info.Size()
			}
		}
		src := f.SHA256Source
		if src == "" {
			src = "computed"
		}
		m.Files = append(m.Files, manifest.File{Kind: f.Kind, Path: f.Path, SizeBytes: size, SHA256: sum, SHA256Source: src, URL: download.RedactURL(f.URL), ZimMD5OK: f.ZimMD5OK})
		m.TotalBytes += size
	}
	return m, nil
}

func (r *runner) treeBytes() int64 {
	var n int64
	paths, err := walkTree(r.m.d.FS, filepath.Join(r.stagingDir(), TreeDir))
	if err != nil {
		return 0
	}
	for _, p := range paths {
		if info, err := r.m.d.FS.Stat(filepath.Join(r.stagingDir(), TreeDir, filepath.FromSlash(p))); err == nil {
			n += info.Size()
		}
	}
	return n
}

// swap is staged -> swapping -> swapped -> verified. It returns the final Result.
func (r *runner) swap() Result {
	fsys := r.m.d.FS
	if r.ctx.Err() != nil {
		r.cancelled(journal.StateStaged)
		return r.res
	}
	d, err := r.job.Root.Join(filepath.FromSlash(r.job.RelDir))
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, false)
		return r.res
	}
	var old *manifest.Manifest
	if om, err := manifest.Read(fsys, manifest.Path(d)); err == nil {
		old = &om
	}
	newM, err := r.buildManifest(old)
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return r.res
	}
	// The pending manifest: durable in staging before the swap starts, renamed in as its last step.
	pending := filepath.Join(r.stagingDir(), PendingManifest)
	if err := manifest.Write(fsys, pending, newM); err != nil && !isNotDurable(err) {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return r.res
	}
	if err := r.transition(journal.StateSwapping, r.version, nil); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return r.res
	}
	sp, _, err := r.m.planSwap(swapInput{root: r.job.Root, relDir: r.job.RelDir, itemID: r.job.Ref.ID, version: r.version, newM: newM})
	if err != nil {
		code := CodeSwapFailed
		_ = r.finish(journal.StateFailed, code, "swap", err, false)
		return r.res
	}
	staged := map[string]os.FileInfo{}
	if !newM.IsDir() {
		for _, f := range newM.Files {
			if info, err := fsys.Stat(filepath.Join(sp.S, filepath.FromSlash(f.Path))); err == nil {
				staged[f.Path] = info
			}
		}
	}
	// Write-ahead: the plan is on disk before the first move.
	err = r.run.Append(journal.Event{
		Ev: journal.EvSwapPlan, Root: r.job.Root.ID, Item: r.job.Ref.ID, Version: r.version,
		Renames: sp.Plan.Renames, Trash: sp.Plan.Trash, Exchange: sp.Plan.Exchange,
	})
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return r.res
	}
	r.res.Bytes = newM.TotalBytes
	return r.m.finishSwap(r.ctx, r.run, sp, &newM, staged, r.res, r.job.Root, r.job.Ref.ID)
}

// finishSwap takes a swap whose plan is journaled through to verified: execute (or replay) the plan,
// journal swap_done, check what landed, refresh the best-copy index, clean staging, and journal
// verified. A swap that lost a file is failed (LAI-SYN-026) and the previous version restored (R4).
func (m *Machine) finishSwap(ctx context.Context, run *journal.Run, sp swapPaths, newM *manifest.Manifest, staged map[string]os.FileInfo, res Result, root ark.Root, itemID string) Result {
	res.Root, res.Item, res.Version = root.ID, itemID, sp.Version
	fail := func(cause error, reason string) Result {
		restored, rerr := m.restorer().Restore(ctx, RestoreRequest{Root: root, ItemID: itemID, Version: sp.Version, Plan: sp.Plan, Reread: m.reread()})
		if rerr != nil {
			m.d.Logger.Error("restore failed", "component", "sync", "root", root.ID, "item", itemID, "err", rerr.Error())
			cause = errors.Join(cause, rerr)
		}
		if restored {
			_, _ = m.d.Index.Refresh(context.Background(), root, sp.RelDir)
		}
		res.State, res.Code, res.Reason, res.Err = journal.StateFailed, CodeSwapFailed, reason, cause
		if _, err := m.d.Journal.Transition(run, root.ID, itemID, sp.Version, journal.StateFailed, func(i *journal.Item) {
			i.Code, i.LastError = CodeSwapFailed, cause.Error()
		}); err != nil {
			res.Err = errors.Join(res.Err, err)
		}
		m.d.Logger.Error("swap failed", "component", "sync", "root", root.ID, "item", itemID, "version", sp.Version, "code", CodeSwapFailed, "restored", restored, "err", cause.Error())
		return res
	}
	if err := m.execute(sp); err != nil {
		if errors.Is(err, ErrSwapLost) {
			return fail(err, "swap_lost")
		}
		if errors.Is(err, ErrExchangeUnsupported) {
			return fail(err, "swap_exchange")
		}
		// Anything else (an I/O error mid-swap) leaves the plan in the journal: the next start replays it.
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapping, CodeSwapFailed, "swap", err, true
		if errors.Is(err, syscall.EIO) {
			res.RootFault = &download.Error{Kind: download.KindRootFault, Code: download.CodeIO, Err: err}
		}
		if errors.Is(err, syscall.EROFS) {
			res.RootFault = &download.Error{Kind: download.KindRootFault, Code: download.CodeReadOnly, Err: err}
		}
		return res
	}
	if err := run.Append(journal.Event{Ev: journal.EvSwapDone, Root: root.ID, Item: itemID, Version: sp.Version}); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapping, CodeInternal, "internal", err, true
		return res
	}
	if _, err := m.d.Journal.Transition(run, root.ID, itemID, sp.Version, journal.StateSwapped, nil); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapped, CodeInternal, "internal", err, true
		return res
	}
	committed, err := manifest.Load(m.d.FS, sp.D)
	if err != nil {
		return fail(fmt.Errorf("the committed manifest does not load: %w", err), "swap_check")
	}
	if newM == nil {
		newM = &committed
	}
	if err := m.postCheck(sp, *newM, staged); err != nil {
		return fail(err, "swap_check")
	}
	if probs, err := m.d.Index.Refresh(context.Background(), root, sp.RelDir); err != nil {
		m.d.Logger.Warn("best-copy refresh failed", "component", "sync", "root", root.ID, "item", itemID, "err", err.Error())
	} else {
		for _, p := range probs {
			m.d.Logger.Warn("best-copy problem", "component", "sync", "root", root.ID, "item", itemID, "code", p.Code)
		}
	}
	if m.d.Remover != nil {
		if err := m.d.Remover.RemoveStaging(sp.S); err != nil {
			m.d.Logger.Warn("staging not removed", "component", "sync", "root", root.ID, "item", itemID, "err", err.Error())
		}
	}
	if _, err := m.d.Journal.Transition(run, root.ID, itemID, sp.Version, journal.StateVerified, func(i *journal.Item) {
		i.Attempts, i.LastError, i.Code, i.PartialBytes, i.Checkpoints, i.ConsecutiveFailedRuns = 0, "", "", 0, nil, 0
		i.Extras = clearQuarantine(*i)
	}); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapped, CodeInternal, "internal", err, true
		return res
	}
	res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateVerified, "", "", nil, false
	res.Bytes = newM.TotalBytes
	m.d.Logger.Info("item verified", "component", "sync", "root", root.ID, "item", itemID, "version", sp.Version, "run_id", run.ID, "bytes", res.Bytes)
	return res
}

// resumeSwap finishes a swap the record says was under way. It returns ok false when there is nothing
// to resume (the record says swapping but no move was ever planned): the item then goes through the
// normal flow again, which plans the swap afresh.
func (r *runner) resumeSwap(rec journal.Item) (Result, bool) {
	fsys := r.m.d.FS
	d, err := r.job.Root.Join(filepath.FromSlash(r.job.RelDir))
	if err != nil {
		return Result{}, false
	}
	base := Result{Root: r.job.Root.ID, Item: r.job.Ref.ID, Version: rec.Version}
	if cm, ok := Committed(fsys, d, rec.Version); ok {
		// The commit happened; only the bookkeeping after it is left.
		sp := swapPaths{Root: r.job.Root, RelDir: r.job.RelDir, ItemID: r.job.Ref.ID, Version: rec.Version, D: d,
			S: filepath.Join(r.job.Root.Path, ".staging", r.job.Ref.ID, rec.Version)}
		if r.job.Swap != nil {
			if p, err := pathsFromPlan(r.job.Root, r.job.Ref.ID, rec.Version, *r.job.Swap); err == nil {
				sp = p
			}
		}
		sp.Plan.Renames = nil // nothing left to move: execute must not be called for this path
		return r.m.finishCommitted(r.ctx, r.run, sp, &cm, base), true
	}
	if r.job.Swap != nil {
		sp, err := pathsFromPlan(r.job.Root, r.job.Ref.ID, rec.Version, *r.job.Swap)
		if err != nil {
			return Result{}, false
		}
		return r.m.finishSwap(r.ctx, r.run, sp, nil, nil, base, r.job.Root, r.job.Ref.ID), true
	}
	return Result{}, false
}

// finishCommitted is finishSwap for a swap whose commit is already on disk: no moves, only the
// bookkeeping.
func (m *Machine) finishCommitted(ctx context.Context, run *journal.Run, sp swapPaths, committed *manifest.Manifest, res Result) Result {
	sp.Plan = journal.SwapPlan{}
	res.Version = sp.Version
	if err := run.Append(journal.Event{Ev: journal.EvSwapDone, Root: sp.Root.ID, Item: sp.ItemID, Version: sp.Version}); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapping, CodeInternal, "internal", err, true
		return res
	}
	if _, err := m.d.Journal.Transition(run, sp.Root.ID, sp.ItemID, sp.Version, journal.StateSwapped, nil); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapped, CodeInternal, "internal", err, true
		return res
	}
	if err := m.postCheck(sp, *committed, nil); err != nil {
		res.State, res.Code, res.Reason, res.Err = journal.StateFailed, CodeSwapFailed, "swap_check", err
		_, _ = m.d.Journal.Transition(run, sp.Root.ID, sp.ItemID, sp.Version, journal.StateFailed, func(i *journal.Item) {
			i.Code, i.LastError = CodeSwapFailed, err.Error()
		})
		return res
	}
	_, _ = m.d.Index.Refresh(context.Background(), sp.Root, sp.RelDir)
	if m.d.Remover != nil && sp.S != "" {
		_ = m.d.Remover.RemoveStaging(sp.S)
	}
	if _, err := m.d.Journal.Transition(run, sp.Root.ID, sp.ItemID, sp.Version, journal.StateVerified, func(i *journal.Item) {
		i.Attempts, i.LastError, i.Code, i.PartialBytes, i.Checkpoints, i.ConsecutiveFailedRuns = 0, "", "", 0, nil, 0
		i.Extras = clearQuarantine(*i)
	}); err != nil {
		res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateSwapped, CodeInternal, "internal", err, true
		return res
	}
	res.State, res.Code, res.Reason, res.Err, res.Retryable = journal.StateVerified, "", "", nil, false
	res.Bytes = committed.TotalBytes
	return res
}
