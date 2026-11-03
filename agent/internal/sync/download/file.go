package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// DefaultBufSize is the streaming buffer: 1 MiB (ark-sync.md section 4.6). It is also the
// granularity of a cancel: the download stops at the next block boundary.
const DefaultBufSize = 1 << 20

// maxRestarts bounds the restarts from zero that one Fetch makes without waiting (a server that
// keeps answering inconsistently is then treated as a transient failure).
const maxRestarts = 3

// Bucket is the bandwidth bucket; throttle.Bucket satisfies it. Bytes are taken after they were
// written.
type Bucket interface {
	Wait(ctx context.Context, n int) error
}

// Timer is the part of *time.Timer the stall watchdog needs.
type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// Clock is the time seam: backoff waits, Retry-After dates and the stall watchdog.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

// RealClock is the wall clock.
func RealClock() Clock { return realClock{} }

// Options configures a Downloader. The zero value is production: the real filesystem and clock, the
// client rules of section 4.6, no bandwidth limit, no trash purge.
type Options struct {
	// FS is the filesystem; default OSFS.
	FS FS
	// HTTP makes the sources of network jobs; default NewHTTPClient(ClientConfig{}).
	HTTP *HTTPClient
	// Bucket is the shared bandwidth bucket (nil = unlimited). Local copies bypass it.
	Bucket Bucket
	// Clock is the time source; default the wall clock.
	Clock Clock
	// Space purges eligible trash when a download needs room; nil = never purge.
	Space SpaceMaker
	// Free measures free space; default statfs.
	Free FreeFunc
	// Logger receives progress notices; default slog.Default. Nothing secret is ever logged.
	Logger *slog.Logger
	// Backoff is the retry schedule; default DefaultBackoff with 20 % jitter.
	Backoff Backoff
	// Stall is the no-body-bytes timeout; default StallTimeout (60 s).
	Stall time.Duration
	// CheckpointEvery is the offset step between checkpoints; default journal.CheckpointEvery (64 MiB).
	CheckpointEvery int64
	// SpaceRecheckEvery is the offset step between free-space checks; default 256 MiB.
	SpaceRecheckEvery int64
	// BufSize is the streaming buffer; default DefaultBufSize (1 MiB).
	BufSize int
	// ProgressReset is the progress since the previous failure that resets the backoff; default
	// ProgressReset (256 MiB).
	ProgressReset int64
	// OnRootFault is called once when a root turns out unwritable (EIO, EROFS): the caller stops
	// planning writes to it and, for readOnly (EROFS), re-runs the drive checks. The Downloader has
	// already stopped writing to that root.
	OnRootFault func(rootID string, err *Error, readOnly bool)
}

// Downloader downloads files. It is safe for concurrent use: the engine runs up to sync.concurrency
// Fetches at once over one Downloader, sharing its bucket, and a root fault stops them all.
type Downloader struct {
	opt  Options
	bufs sync.Pool

	mu      sync.Mutex
	faulted map[string]*Error
}

// New returns a Downloader.
func New(opt Options) *Downloader {
	if opt.FS == nil {
		opt.FS = OSFS{}
	}
	if opt.HTTP == nil {
		opt.HTTP = NewHTTPClient(ClientConfig{})
	}
	if opt.Clock == nil {
		opt.Clock = RealClock()
	}
	if opt.Free == nil {
		opt.Free = statfsFree
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Stall <= 0 {
		opt.Stall = StallTimeout
	}
	if opt.CheckpointEvery <= 0 {
		opt.CheckpointEvery = journal.CheckpointEvery
	}
	if opt.SpaceRecheckEvery <= 0 {
		opt.SpaceRecheckEvery = SpaceRecheckEvery
	}
	if opt.BufSize <= 0 {
		opt.BufSize = DefaultBufSize
	}
	if opt.ProgressReset <= 0 {
		opt.ProgressReset = ProgressReset
	}
	d := &Downloader{opt: opt, faulted: map[string]*Error{}}
	d.bufs.New = func() any { b := make([]byte, opt.BufSize); return &b }
	return d
}

// FileJob is one file of an item.
type FileJob struct {
	// RootID is the content root's id; Root is its absolute path.
	RootID, Root string
	// Item and Version place the staging directory: <Root>/.staging/<Item>/<Version>/.
	Item, Version string
	// Name is the file's path inside the item (a clean relative slash path); it is also the key of
	// its checkpoint.
	Name string
	// URL is the https source (credentials, query and fragment are never logged or stored). For a
	// job with a Source it is only a label that must stay the same between attempts.
	URL string
	// ExpectedSize is the size in bytes; 0 means unknown (a computed checksum).
	ExpectedSize int64
	// ExpectedSHA256 is the hex sha256 the file must have; "" means unknown (computed: the digest is
	// returned for the caller to record).
	ExpectedSHA256 string
	// Auth sends the Hugging Face token, but only to the auth host over https.
	Auth bool
	// MinFreeBytes is the root's min_free_bytes (DriveMinFree for a drive).
	MinFreeBytes int64
	// Commit renames the verified .part to its final name (Downloader.Commit) before Fetch returns.
	// Leave it false when the engine re-reads the file first (verify_reread).
	Commit bool
	// Source replaces the HTTP source (a LocalSource for local_copy and import).
	Source Source
}

// StagingDir is <Root>/.staging/<Item>/<Version>.
func (j FileJob) StagingDir() string {
	return filepath.Join(j.Root, ".staging", j.Item, j.Version)
}

// PartPath is the .part file of the job.
func (j FileJob) PartPath() string {
	return filepath.Join(j.StagingDir(), filepath.FromSlash(j.Name)+".part")
}

// FinalPath is where the verified file goes after Commit (still inside staging; the swap moves it
// into the item directory).
func (j FileJob) FinalPath() string {
	return filepath.Join(j.StagingDir(), filepath.FromSlash(j.Name))
}

// ErrInvalidJob: the FileJob is malformed (a caller bug, not a user-visible condition).
var ErrInvalidJob = errors.New("download: invalid file job")

func (j FileJob) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidJob, fmt.Sprintf(format, args...))
	}
	switch {
	case j.Root == "" || j.RootID == "":
		return bad("root and root id are required")
	case !manifest.SafeName(j.Item) || !manifest.SafeName(j.Version):
		return bad("item %q or version %q is not a plain name", j.Item, j.Version)
	case !manifest.ValidFilePath(j.Name):
		return bad("file name %q is not a clean relative path", j.Name)
	case j.ExpectedSize < 0:
		return bad("negative expected size")
	case j.MinFreeBytes < 0:
		return bad("negative minimum free bytes")
	}
	if j.ExpectedSHA256 != "" {
		if _, err := hex.DecodeString(j.ExpectedSHA256); err != nil || len(j.ExpectedSHA256) != sha256.Size*2 {
			return bad("expected sha256 is not 64 hex characters")
		}
	}
	if j.Source == nil {
		u, err := url.Parse(j.URL)
		switch {
		case err != nil || u.Host == "":
			return bad("the URL is not valid")
		case u.Scheme != "https" && u.Scheme != "http":
			return bad("the URL scheme must be https")
		case u.User != nil:
			return bad("the URL carries credentials")
		}
	}
	return nil
}

// Result is the outcome of Fetch. It is filled on every return, with the bytes and notices known at
// that point.
type Result struct {
	// SHA256 is the lower-case hex digest of the bytes streamed: set on success and on a digest
	// mismatch (the caller records expected and actual in the quarantine reason).
	SHA256 string
	// Bytes is the size of the .part: the verified size on success, the resumable offset otherwise.
	Bytes int64
	// Code is the doctor code of the failure ("" on success).
	Code string
	// Permanent: the failure will not go away by retrying in this run.
	Permanent bool
	// Mismatch: the bytes are not the expected ones; quarantine the .part.
	Mismatch bool
	// Skipped is "disk" when the file was skipped for lack of space; the .part and its checkpoint
	// are kept.
	Skipped string
	// Restarted: the file was restarted from zero because the server could not resume it.
	Restarted bool
	// Notices are informational codes to put in the run log: CodeRestart (32) after a server that
	// ignored Range.
	Notices []string
	// PurgedBytes is what the SpaceMaker freed for this file; the caller raises CodePurged (43) when
	// it is not zero.
	PurgedBytes int64
	// ResumedFrom is the offset the download started from (0 for a fresh file).
	ResumedFrom int64
	// Attempts is how many requests were made.
	Attempts int
	// Part is the .part path; Final is the final path after a Commit ("" until then).
	Part, Final string
}

// Fetch downloads one file into its .part and verifies it: resume from the checkpoint, stream
// through a 1 MiB buffer into the .part and the sha256, checkpoint every CheckpointEvery bytes,
// retry transient failures (section 4.8), and at EOF compare the size and the digest. On success
// the .part holds exactly the verified bytes (renamed to its final name when j.Commit is set).
//
// Errors: nil on success; ctx.Err() when cancelled (the download stopped at a block boundary and
// checkpointed, so it resumes exactly there); otherwise an *Error whose Kind says what to do
// (KindMismatch: quarantine; KindPermanent: failed this run; KindExhausted: failed, resumable next
// run; KindDisk: skipped, resumable; KindRootFault: stop using the root). In every case but a
// mismatch the .part and its checkpoint are kept.
//
// cp may be nil (progress then only survives retries inside this call).
func (d *Downloader) Fetch(ctx context.Context, j FileJob, cp Checkpointer) (Result, error) {
	if err := j.validate(); err != nil {
		return Result{}, err
	}
	if cp == nil {
		cp = &MemoryCheckpointer{}
	}
	f := &fetch{d: d, j: j, cp: cp, url: labelURL(j.URL)}
	f.res.Part = j.PartPath()
	err := f.do(ctx)
	f.closePart()
	f.res.Bytes = f.offset
	var e *Error
	if errors.As(err, &e) {
		f.res.Code, f.res.Permanent = e.Code, e.Permanent
		f.res.Mismatch = e.Kind == KindMismatch
		if e.Kind == KindDisk {
			f.res.Skipped = "disk"
		}
	}
	return f.res, err
}

// Commit renames the verified .part of j to its final name inside staging and makes the rename
// durable. Call it only after Fetch succeeded (and after the engine's re-read, when it makes one):
// the .part is the unverified-until-now file, the final name is the verified one. The checkpoint is
// cleared (cp may be nil), since the .part it described is gone.
func (d *Downloader) Commit(j FileJob, cp Checkpointer) (string, error) {
	if err := j.validate(); err != nil {
		return "", err
	}
	part, final := j.PartPath(), j.FinalPath()
	if err := d.opt.FS.Rename(part, final); err != nil {
		return "", fmt.Errorf("download: commit %s: %w", j.Name, err)
	}
	if err := d.opt.FS.SyncDir(filepath.Dir(final)); err != nil {
		return final, fmt.Errorf("download: commit %s: the rename is not durable: %w", j.Name, err)
	}
	if cp != nil {
		if err := cp.Clear(j.Name); err != nil {
			d.opt.Logger.Warn("checkpoint not cleared after commit", "component", "sync", "item", j.Item, "file", j.Name, "err", err.Error())
		}
	}
	return final, nil
}

// ClearRootFault lets Fetch write to a root again after the caller dealt with its fault (the drive
// was remounted read-write, or replaced).
func (d *Downloader) ClearRootFault(rootID string) {
	d.mu.Lock()
	delete(d.faulted, rootID)
	d.mu.Unlock()
}

func (d *Downloader) checkFault(rootID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.faulted[rootID]; ok {
		return e
	}
	return nil
}

func (d *Downloader) markFault(rootID string, e *Error, readOnly bool) {
	d.mu.Lock()
	_, already := d.faulted[rootID]
	if !already {
		d.faulted[rootID] = e
	}
	d.mu.Unlock()
	if !already && d.opt.OnRootFault != nil {
		d.opt.OnRootFault(rootID, e, readOnly)
	}
}

// labelURL is the URL as it is stored in a checkpoint and logged: scheme, host and path only.
func labelURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return RedactURL(raw)
	}
	return raw
}

// ShortHash is a hash as it is logged: first4...last2 (ark-sync.md section 7).
func ShortHash(hexSum string) string {
	if len(hexSum) < 8 {
		return hexSum
	}
	return hexSum[:4] + "…" + hexSum[len(hexSum)-2:]
}

var errStall = errors.New("download: no data received for the stall timeout")

// fetch is the state of one Fetch call.
type fetch struct {
	d   *Downloader
	j   FileJob
	cp  Checkpointer
	url string
	res Result

	part PartFile
	h    hash.Hash

	offset    int64 // bytes in the .part, hashed and written
	lastCkpt  int64 // offset of the last checkpoint
	lastSpace int64 // offset of the last free-space check

	etag, lastMod string
	restarts      int
}

func (f *fetch) log(level slog.Level, msg string, attrs ...any) {
	base := []any{"component", "sync", "root", f.j.RootID, "item", f.j.Item, "file", f.j.Name, "offset", f.offset}
	f.d.opt.Logger.Log(context.Background(), level, msg, append(base, attrs...)...)
}

func (f *fetch) closePart() {
	if f.part != nil {
		_ = f.part.Close()
		f.part = nil
	}
}

func (f *fetch) do(ctx context.Context) error {
	d, j := f.d, f.j
	if err := d.checkFault(j.RootID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.opt.FS.MkdirAll(j.StagingDir(), 0o700); err != nil {
		return f.fsFail(err)
	}
	if dir := filepath.Dir(f.res.Part); dir != j.StagingDir() {
		if err := d.opt.FS.MkdirAll(dir, 0o700); err != nil {
			return f.fsFail(err)
		}
	}
	part, err := d.opt.FS.OpenPart(f.res.Part)
	if err != nil {
		return f.fsFail(err)
	}
	f.part = part
	if err := f.resume(); err != nil {
		return err
	}
	f.res.ResumedFrom = f.offset
	f.lastSpace = f.offset

	src := j.Source
	if src == nil {
		src = d.opt.HTTP.Source(j.URL, j.Auth)
	}
	retries, failOffset := 0, f.offset
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.res.Attempts++
		err := f.attempt(ctx, src)
		if err == nil {
			return f.complete()
		}
		var terr *Error
		if errors.As(err, &terr) {
			return f.terminal(terr)
		}
		if ctx.Err() != nil && isCtx(err) {
			f.checkpointNow()
			return ctx.Err()
		}
		rt, ok := err.(*retryable)
		if !ok {
			rt = &retryable{err: err}
		}
		if rt.restart && f.restarts < maxRestarts {
			f.restarts++
			f.log(slog.LevelWarn, "restarting the file from zero", "reason", rt.err.Error())
			if err := f.restart(); err != nil {
				return err
			}
			continue
		}
		// A transient failure: keep what is on disk resumable, then wait and try again.
		if cerr := f.checkpointNowErr(); cerr != nil {
			return cerr
		}
		if f.offset-failOffset >= d.opt.ProgressReset {
			retries = 0
		}
		failOffset = f.offset
		if retries >= d.opt.Backoff.Retries() {
			code := CodeRetries
			if rt.hf429 {
				code = CodeHFLogin
			}
			e := &Error{Kind: KindExhausted, Code: code, Err: fmt.Errorf("download failed after %d attempts: %w", f.res.Attempts, rt.err)}
			f.log(slog.LevelWarn, "download failed after all retries", "code", code, "attempts", f.res.Attempts, "err", rt.err.Error())
			return e
		}
		delay := d.opt.Backoff.Delay(retries)
		retries++
		if rt.retryAfter != "" {
			if ra, ok := ParseRetryAfter(rt.retryAfter, d.opt.Clock.Now()); ok {
				if ra > MaxRetryAfter {
					e := &Error{Kind: KindExhausted, Code: CodeRetries, Err: fmt.Errorf("the server asked to wait %s, longer than the %s this run honours: %w", ra.Round(time.Second), MaxRetryAfter, rt.err)}
					f.log(slog.LevelWarn, "retry-after too long", "code", CodeRetries, "retry_after", ra.String())
					return e
				}
				delay = ra
			}
		}
		f.log(slog.LevelInfo, "transient failure, will retry", "attempt", f.res.Attempts, "delay", delay.String(), "err", rt.err.Error())
		select {
		case <-d.opt.Clock.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// resume decides where this download starts: from the checkpoint when it is trustworthy, else
// from zero.
func (f *fetch) resume() error {
	j := f.j
	size, err := f.part.Seek(0, io.SeekEnd)
	if err != nil {
		return f.fsFail(err)
	}
	f.h = sha256.New()
	ck, had := f.cp.Load(j.Name)
	if had {
		switch {
		case ck.URL != f.url:
			f.log(slog.LevelInfo, "checkpoint is for another URL, restarting", "checkpoint_offset", ck.Offset)
		case size < ck.Offset:
			f.log(slog.LevelWarn, "the .part is shorter than its checkpoint, restarting", "checkpoint_offset", ck.Offset, "part_size", size)
		case j.ExpectedSize > 0 && ck.Offset > j.ExpectedSize:
			f.log(slog.LevelWarn, "checkpoint is beyond the expected size, restarting", "checkpoint_offset", ck.Offset)
		default:
			h, err := decodeSHA(ck.ShaState, ck.Offset)
			if err != nil {
				f.log(slog.LevelWarn, "checkpoint hash state unusable, restarting", "err", err.Error())
				break
			}
			if size > ck.Offset {
				if err := f.part.Truncate(ck.Offset); err != nil {
					return f.fsFail(err)
				}
			}
			if _, err := f.part.Seek(ck.Offset, io.SeekStart); err != nil {
				return f.fsFail(err)
			}
			f.h, f.offset, f.lastCkpt = h, ck.Offset, ck.Offset
			f.etag, f.lastMod = ck.ETag, ck.LastModified
			f.log(slog.LevelInfo, "resume", "part_size", size)
			return nil
		}
	}
	if size > 0 {
		if err := f.part.Truncate(0); err != nil {
			return f.fsFail(err)
		}
	}
	if _, err := f.part.Seek(0, io.SeekStart); err != nil {
		return f.fsFail(err)
	}
	if had {
		f.clearCheckpoint()
	}
	return nil
}

func (f *fetch) clearCheckpoint() {
	if err := f.cp.Clear(f.j.Name); err != nil {
		f.log(slog.LevelWarn, "checkpoint not cleared", "err", err.Error())
	}
}

// restart truncates the .part, resets the hash and forgets the checkpoint and validators.
func (f *fetch) restart() error {
	if err := f.part.Truncate(0); err != nil {
		return f.fsFail(err)
	}
	if _, err := f.part.Seek(0, io.SeekStart); err != nil {
		return f.fsFail(err)
	}
	f.h = sha256.New()
	f.offset, f.lastCkpt, f.lastSpace = 0, 0, 0
	f.etag, f.lastMod = "", ""
	f.clearCheckpoint()
	return nil
}

// validator is what the next resume sends in If-Range: a strong ETag, else Last-Modified.
func (f *fetch) validator() string {
	if f.etag != "" && !strings.HasPrefix(f.etag, "W/") {
		return f.etag
	}
	return f.lastMod
}

// attempt makes one request and streams its body. nil means the file is complete (the caller
// verifies it); *Error is terminal; *retryable and any other error are transient; the context's own
// error means cancelled.
func (f *fetch) attempt(ctx context.Context, src Source) error {
	d, j := f.d, f.j
	if j.ExpectedSize > 0 && f.offset == j.ExpectedSize {
		return nil // everything is already on disk and hashed: nothing to request
	}
	need := int64(0)
	if j.ExpectedSize > 0 {
		need = j.ExpectedSize - f.offset
	}
	purged, err := d.ensureSpace(ctx, j.RootID, j.Root, need, j.MinFreeBytes)
	f.res.PurgedBytes += purged
	if err != nil {
		return f.spaceErr(err)
	}

	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	body, resp, err := src.Open(actx, f.offset, f.validator())
	if body != nil {
		defer body.Close()
	}
	if err != nil {
		var terr *Error
		var rt *retryable
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.As(err, &terr), errors.As(err, &rt):
			return err
		}
		return &retryable{err: err}
	}
	local := isLocal(src)

	switch {
	case resp.Status == 200:
		if f.offset > 0 {
			f.log(slog.LevelInfo, "the server restarted the file from zero", "code", CodeRestart)
			f.res.Restarted = true
			f.res.Notices = appendOnce(f.res.Notices, CodeRestart)
			if err := f.restart(); err != nil {
				return err
			}
		}
		if err := f.checkSize(resp.Size); err != nil {
			return err
		}
		f.etag, f.lastMod = resp.ETag, resp.LastModified
	case resp.Status == 206:
		if resp.Offset != f.offset {
			return &retryable{err: fmt.Errorf("the server resumed at byte %d, not %d", resp.Offset, f.offset), restart: true}
		}
		if err := f.checkSize(resp.Size); err != nil {
			return err
		}
		if f.etag != "" && resp.ETag != "" && resp.ETag != f.etag {
			return &retryable{err: errors.New("the file changed on the server since the checkpoint"), restart: true}
		}
		if f.etag == "" && f.lastMod == "" {
			f.etag, f.lastMod = resp.ETag, resp.LastModified
		}
	case resp.Status == 416:
		if f.offset > 0 {
			if (j.ExpectedSize > 0 && f.offset == j.ExpectedSize) || (resp.Size >= 0 && f.offset == resp.Size) {
				return nil // the file was already complete
			}
			return &retryable{err: errors.New("the server refused the resume offset (416)"), restart: true}
		}
		return &retryable{err: errors.New("the server answered 416 to a request for the whole file")}
	default:
		return f.statusErr(resp)
	}
	return f.stream(ctx, actx, cancel, body, local)
}

// statusErr classifies a response with no usable body.
func (f *fetch) statusErr(resp Resp) error {
	st := resp.Status
	cause := fmt.Errorf("%s answered %d", f.url, st)
	switch {
	case st == 401 || st == 403:
		code := CodeRefused
		if resp.HuggingFace {
			code = CodeHFLogin
		}
		return &Error{Kind: KindPermanent, Code: code, Permanent: true, Err: cause}
	case st == 408:
		return &retryable{err: cause}
	case st == 429:
		return &retryable{err: cause, retryAfter: resp.RetryAfter, hf429: resp.HuggingFace}
	case st >= 500 && st < 600:
		return &retryable{err: cause, retryAfter: resp.RetryAfter}
	case st >= 400 && st < 500:
		return &Error{Kind: KindPermanent, Code: CodeRefused, Permanent: true, Err: cause}
	}
	return &retryable{err: cause}
}

// checkSize refuses a server that says the file has another size than expected, before any byte
// is downloaded.
func (f *fetch) checkSize(total int64) error {
	if total >= 0 && f.j.ExpectedSize > 0 && total != f.j.ExpectedSize {
		return newError(KindMismatch, CodeSize, "the server says the file is %d bytes, expected %d", total, f.j.ExpectedSize)
	}
	return nil
}

func isLocal(src Source) bool {
	l, ok := src.(interface{ Local() bool })
	return ok && l.Local()
}

func appendOnce(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// readBlock fills buf (or reads to the end), arming the stall watchdog around every Read only: the
// time spent writing, checkpointing or waiting for the bucket never counts as a stall.
func (f *fetch) readBlock(actx context.Context, body io.Reader, buf []byte, watch Timer) (int, error) {
	n := 0
	for n < len(buf) {
		if err := actx.Err(); err != nil {
			return n, err
		}
		watch.Reset(f.d.opt.Stall)
		m, err := body.Read(buf[n:])
		watch.Stop()
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// stream copies the body into the .part and the hash until EOF or an error.
func (f *fetch) stream(ctx, actx context.Context, cancel context.CancelCauseFunc, body io.Reader, local bool) error {
	d := f.d
	bp := d.bufs.Get().(*[]byte)
	defer d.bufs.Put(bp)
	buf := *bp
	watch := d.opt.Clock.AfterFunc(d.opt.Stall, func() { cancel(errStall) })
	watch.Stop()
	defer watch.Stop()
	for {
		n, rerr := f.readBlock(actx, body, buf, watch)
		if n > 0 {
			if err := f.consume(ctx, buf[:n], local); err != nil {
				return err
			}
		}
		switch {
		case rerr == nil:
			continue
		case errors.Is(rerr, io.EOF):
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(context.Cause(actx), errStall):
			return &retryable{err: fmt.Errorf("no data received for %s from %s", d.opt.Stall, f.url)}
		}
		var terr *Error
		if errors.As(rerr, &terr) {
			return rerr
		}
		return &retryable{err: scrub(rerr)}
	}
}

// consume writes one block, hashes it, and does the periodic work: checkpoint, free-space check,
// bucket.
func (f *fetch) consume(ctx context.Context, block []byte, local bool) error {
	d, j := f.d, f.j
	over := false
	if j.ExpectedSize > 0 {
		if room := j.ExpectedSize - f.offset; int64(len(block)) > room {
			block, over = block[:room], true
		}
	}
	if len(block) > 0 {
		if err := f.write(ctx, block); err != nil {
			return err
		}
	}
	if over {
		return newError(KindMismatch, CodeSize, "the server sent more than the expected %d bytes", j.ExpectedSize)
	}
	if f.offset-f.lastCkpt >= d.opt.CheckpointEvery {
		if err := f.checkpoint(); err != nil {
			return err
		}
	}
	if f.offset-f.lastSpace >= d.opt.SpaceRecheckEvery {
		f.lastSpace = f.offset
		if err := f.recheckSpace(ctx); err != nil {
			return err
		}
	}
	if !local && d.opt.Bucket != nil {
		if err := d.opt.Bucket.Wait(ctx, len(block)); err != nil {
			return err
		}
	}
	return nil
}

// recheckSpace is the in-flight check of section 4.8: free space must still be at least
// min_free_bytes. When it is not: checkpoint, purge, check again, else skip the file.
func (f *fetch) recheckSpace(ctx context.Context) error {
	d, j := f.d, f.j
	ok, _, err := d.spaceOK(j.Root, 0, j.MinFreeBytes)
	if err != nil {
		return f.fsFail(err)
	}
	if ok {
		return nil
	}
	f.checkpointNow()
	purged, err := d.ensureSpace(ctx, j.RootID, j.Root, 0, j.MinFreeBytes)
	f.res.PurgedBytes += purged
	if err != nil {
		return f.spaceErr(err)
	}
	return nil
}

// write appends a block to the .part and the hash. On ENOSPC or EDQUOT the partial block is
// truncated away, so the .part ends at the last good offset; the file is checkpointed there, the
// trash purged and the write tried once more; if there is still no room the file is skipped.
func (f *fetch) write(ctx context.Context, block []byte) error {
	d, j := f.d, f.j
	if err := d.checkFault(j.RootID); err != nil {
		return err
	}
	retried := false
	for {
		n, err := f.part.Write(block)
		if err == nil && n < len(block) {
			err = io.ErrShortWrite
		}
		if err == nil {
			_, _ = f.h.Write(block)
			f.offset += int64(len(block))
			return nil
		}
		if !isNoSpace(err) && !errors.Is(err, io.ErrShortWrite) {
			return f.fsFail(err)
		}
		if terr := f.part.Truncate(f.offset); terr != nil {
			return f.fsFail(terr)
		}
		if _, serr := f.part.Seek(f.offset, io.SeekStart); serr != nil {
			return f.fsFail(serr)
		}
		f.checkpointNow()
		purged, perr := d.ensureSpace(ctx, j.RootID, j.Root, int64(len(block)), j.MinFreeBytes)
		f.res.PurgedBytes += purged
		if perr == nil && !retried {
			retried = true
			continue
		}
		if perr == nil {
			perr = &Error{Kind: KindDisk, Code: CodeDisk, Err: errors.New("download: the disk is full")}
		}
		return f.spaceErr(perr)
	}
}

// spaceErr turns an ensureSpace failure into the error Fetch returns, after checkpointing the
// .part so it resumes where it stopped.
func (f *fetch) spaceErr(err error) error {
	var e *Error
	if errors.As(err, &e) {
		f.checkpointNow()
		f.log(slog.LevelWarn, "skipped: not enough free disk space", "code", e.Code)
		return e
	}
	return f.fsFail(err)
}

// fsFail turns a filesystem error into the *Error of a root fault: EIO stops all writes to the
// root and raises the drive-I/O code, EROFS stops them and asks for a drive re-check, a permission
// error reports an unwritable root; a full disk is the disk skip. Paths are not secret, the error
// keeps them.
func (f *fetch) fsFail(err error) error {
	switch {
	case isNoSpace(err):
		e := &Error{Kind: KindDisk, Code: CodeDisk, Err: fmt.Errorf("download: no space left on the root: %w", err)}
		return e
	case isReadOnlyFS(err):
		e := &Error{Kind: KindRootFault, Code: CodeReadOnly, Err: fmt.Errorf("download: the root turned read-only: %w", err)}
		f.log(slog.LevelError, "root is read-only", "code", e.Code)
		f.d.markFault(f.j.RootID, e, true)
		return e
	case isDenied(err):
		e := &Error{Kind: KindRootFault, Code: CodeNoWritableRoot, Err: fmt.Errorf("download: the root is not writable: %w", err)}
		f.log(slog.LevelError, "root is not writable", "code", e.Code)
		return e
	}
	e := &Error{Kind: KindRootFault, Code: CodeIO, Err: fmt.Errorf("download: the root reported an I/O error: %w", err)}
	f.log(slog.LevelError, "root I/O error", "code", e.Code)
	if isIO(err) {
		f.d.markFault(f.j.RootID, e, false)
	}
	return e
}

// checkpoint makes the .part durable and records the resume point. A failure of the journal is
// logged and ignored (the previous point stays valid); a root fault is returned.
func (f *fetch) checkpoint() error {
	if err := f.part.Sync(); err != nil {
		if isIO(err) || isReadOnlyFS(err) {
			return f.fsFail(err)
		}
		f.log(slog.LevelWarn, "fdatasync failed, no checkpoint", "err", err.Error())
		return nil
	}
	state, err := encodeSHA(f.h)
	if err != nil {
		f.log(slog.LevelWarn, "hash state not marshalled, no checkpoint", "err", err.Error())
		return nil
	}
	cp := Checkpoint{Offset: f.offset, ShaState: state, ETag: f.etag, LastModified: f.lastMod, URL: f.url}
	if err := f.cp.Save(f.j.Name, cp); err != nil {
		f.log(slog.LevelWarn, "checkpoint not saved", "err", err.Error())
	}
	f.lastCkpt = f.offset
	return nil
}

// checkpointNow checkpoints when there is progress that no checkpoint covers; best effort.
func (f *fetch) checkpointNow() { _ = f.checkpointNowErr() }

func (f *fetch) checkpointNowErr() error {
	if f.part == nil || f.offset == 0 || f.offset == f.lastCkpt {
		return nil
	}
	return f.checkpoint()
}

// terminal finishes a Fetch with an *Error: the file's state decides what is kept.
func (f *fetch) terminal(e *Error) error {
	switch e.Kind {
	case KindMismatch:
		f.clearCheckpoint() // the bytes are rejected; the .part is the caller's to quarantine
	case KindRootFault:
	default:
		f.checkpointNow()
	}
	if e.Kind == KindPermanent {
		f.log(slog.LevelWarn, "permanent failure", "code", e.Code, "err", e.Err.Error())
	}
	return e
}

// complete verifies a fully downloaded file: size, then digest.
func (f *fetch) complete() error {
	j := f.j
	f.res.Bytes = f.offset
	if j.ExpectedSize > 0 && f.offset != j.ExpectedSize {
		f.res.SHA256 = hex.EncodeToString(f.h.Sum(nil))
		return f.terminal(newError(KindMismatch, CodeSize, "downloaded %d bytes, expected %d", f.offset, j.ExpectedSize))
	}
	if err := f.part.Sync(); err != nil {
		return f.terminal(f.syncErr(err))
	}
	sum := hex.EncodeToString(f.h.Sum(nil))
	f.res.SHA256 = sum
	if j.ExpectedSHA256 != "" && !strings.EqualFold(sum, j.ExpectedSHA256) {
		code := CodeChecksum
		if m, ok := j.Source.(interface{ MismatchCode() string }); ok {
			code = m.MismatchCode()
		}
		f.log(slog.LevelWarn, "checksum mismatch", "code", code, "expected", ShortHash(strings.ToLower(j.ExpectedSHA256)), "actual", ShortHash(sum))
		return f.terminal(newError(KindMismatch, code, "the sha256 of the %d downloaded bytes is %s, expected %s", f.offset, ShortHash(sum), ShortHash(strings.ToLower(j.ExpectedSHA256))))
	}
	if f.offset != f.lastCkpt {
		if err := f.checkpoint(); err != nil {
			return f.terminal(err.(*Error))
		}
	}
	if j.Commit {
		f.closePart()
		final, err := f.d.Commit(j, f.cp)
		if err != nil {
			return f.fsFail(err)
		}
		f.res.Final = final
	}
	f.log(slog.LevelInfo, "file verified", "bytes", f.offset, "sha256", ShortHash(sum))
	return nil
}

// syncErr classifies a failed final fsync: on a full disk the data may not have reached it.
func (f *fetch) syncErr(err error) *Error {
	if isNoSpace(err) {
		return &Error{Kind: KindDisk, Code: CodeDisk, Err: fmt.Errorf("download: the final fsync found no space: %w", err)}
	}
	return f.fsFail(err).(*Error)
}
