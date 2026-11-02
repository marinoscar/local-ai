// Package audit writes the agent's append-only audit log (agent.md section 4.11): one JSON object
// per line in /var/lib/localai/audit.jsonl (root, 0600), fsync'd after every write, mirrored to
// slog with component=audit and rotated at 10 MiB to .1 ... .5.
//
// Writers are the agent and the root-only aictl commands that run without it. Both open the file
// with O_APPEND|O_WRONLY|O_CREATE and write each record with one write(2) of the whole line (at
// most MaxLineBytes, so appends from two processes never interleave). Only the agent rotates
// (the default); a CLI writer is opened with WithoutRotation and never renames anything.
//
// The log never blocks the action it records: if the file cannot be opened or written (disk full,
// read-only filesystem) the record still goes to slog (journald), Write reports the failure, and
// Err keeps the doctor code LAI-AGT-013 until a later write succeeds. Raising the alert is the
// doctor's job (AGENT-11).
package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"
)

const (
	// DefaultPath is the audit file of agent.md section 3.3.
	DefaultPath = "/var/lib/localai/audit.jsonl"
	// DefaultMaxSize is the size at which the agent rotates the file: 10 MiB.
	DefaultMaxSize int64 = 10 << 20
	// Keep is the number of rotated files (.1 ... .5).
	Keep = 5
	// MaxLineBytes bounds one record, newline included. POSIX makes an O_APPEND write of this size
	// or less land in one piece even when two processes append at once.
	MaxLineBytes = 4096
	// FileMode is the mode of the audit file.
	FileMode fs.FileMode = 0o600

	// CodeUnwritable is the doctor code raised when the audit log cannot be written
	// (LAI-AGT-013, doctor-codes.md).
	CodeUnwritable = "LAI-AGT-013"
)

// ErrClosed is returned by Write after Close.
var ErrClosed = errors.New("audit: log is closed")

// Error is a failure to open or write the audit file. Code is always CodeUnwritable.
type Error struct {
	Code string
	Path string
	Op   string // open, write, sync, stat
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: audit log %s: %s: %v", e.Code, e.Op, e.Path, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// File is the part of *os.File the log uses.
type File interface {
	io.Writer
	Sync() error
	Close() error
	Stat() (fs.FileInfo, error)
	Chmod(mode fs.FileMode) error
}

// FS is the part of the filesystem the log uses; internal/audit/fake implements it in memory.
type FS interface {
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
}

type osFS struct{}

func (osFS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}
func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error             { return os.Remove(name) }

// Option configures a Log.
type Option func(*Log)

// WithLogger sets the logger the records are mirrored to (default slog.Default). Pass the
// agent's scrubbing logger from logx.New.
func WithLogger(l *slog.Logger) Option { return func(a *Log) { a.logger = l } }

// WithMaxSize sets the rotation threshold in bytes (default DefaultMaxSize). Tests use a small one.
func WithMaxSize(n int64) Option { return func(a *Log) { a.maxSize = n } }

// WithoutRotation makes the log append-only in the strictest sense: it never renames or removes a
// file. It is for aictl commands that write without the agent; if the agent rotates between a
// CLI writer's open and its write, the line lands in .1, which is acceptable.
func WithoutRotation() Option { return func(a *Log) { a.rotate = false } }

// WithFS replaces the filesystem (tests).
func WithFS(f FS) Option { return func(a *Log) { a.fs = f } }

// WithClock replaces the clock (tests).
func WithClock(now func() time.Time) Option { return func(a *Log) { a.now = now } }

// Log is the audit log. It is safe for concurrent use; writes are serialized.
type Log struct {
	path    string
	fs      FS
	maxSize int64
	rotate  bool
	logger  *slog.Logger
	now     func() time.Time

	mu          sync.Mutex
	f           File
	size        int64
	needNewline bool  // a failed write left a partial line: start the next record on a fresh line
	err         error // the failure of the last attempt, nil while healthy
	closed      bool
	lastWarn    time.Time // throttles the repeated "cannot write" warning
	rotateAfter time.Time // do not retry a failed rotation before this time
}

// warnEvery is the minimum gap between two "cannot write" warnings while the log stays broken.
const warnEvery = time.Minute

// rotateRetry is how long a failed rotation waits before trying again.
const rotateRetry = time.Minute

// Open opens (creating it with mode 0600 if needed) the audit file at path.
//
// The returned *Log is never nil. If the file cannot be opened the error is an *Error with
// Code LAI-AGT-013, and the Log is degraded: every record goes to slog only, and each later Write
// tries to open the file again, so the log recovers by itself once the cause (a full disk) is gone.
func Open(path string, opts ...Option) (*Log, error) {
	l := &Log{path: path, fs: osFS{}, maxSize: DefaultMaxSize, rotate: true, logger: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(l)
	}
	l.logger = l.logger.With("component", "audit")
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.openLocked(); err != nil {
		l.fail(err)
		return l, err
	}
	return l, nil
}

// Err returns the failure of the last open or write, or nil while the log is healthy. A non-nil
// value is an *Error with Code LAI-AGT-013: the doctor reports it as a warning.
func (l *Log) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Write records ev: it is mirrored to slog, then appended to the file as one fsync'd line. A
// zero ev.At is replaced by the current time. The error, if any, is an *Error (or ErrClosed); the
// record has been logged to slog either way, so a caller normally logs nothing more.
func (l *Log) Write(ev Event) error {
	at := ev.At
	if at.IsZero() {
		at = l.now()
	}
	w := wireOf(ev, at)
	l.mirror(w)
	line := encode(w)

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(line)
}

// Close syncs and closes the file. Later writes return ErrClosed (still mirrored to slog).
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	serr := f.Sync()
	if cerr := f.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

func (l *Log) appendLocked(line []byte) error {
	if l.closed {
		return ErrClosed
	}
	if l.f == nil {
		if err := l.openLocked(); err != nil {
			l.fail(err)
			return err
		}
	}
	if l.rotate && l.size > 0 && l.size+int64(len(line)) > l.maxSize && !l.now().Before(l.rotateAfter) {
		if err := l.rotateLocked(); err != nil {
			// Not fatal: keep appending to the current file and try again later.
			l.rotateAfter = l.now().Add(rotateRetry)
			l.logger.Warn("audit log rotation failed; still appending", "code", CodeUnwritable, "error", err.Error())
		}
		if l.f == nil { // the reopen after rotation failed
			err := l.err
			if err == nil {
				err = &Error{Code: CodeUnwritable, Path: l.path, Op: "open", Err: errors.New("not open")}
			}
			return err
		}
	}
	buf := line
	if l.needNewline {
		buf = append([]byte{'\n'}, line...)
	}
	n, err := l.f.Write(buf)
	if err == nil && n < len(buf) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if n > 0 {
			l.size += int64(n)
			l.needNewline = true // a partial line is on disk: the next record starts on a new line
		}
		l.drop()
		e := &Error{Code: CodeUnwritable, Path: l.path, Op: "write", Err: err}
		l.fail(e)
		return e
	}
	l.needNewline = false
	l.size += int64(n)
	if err := l.f.Sync(); err != nil {
		l.drop()
		e := &Error{Code: CodeUnwritable, Path: l.path, Op: "sync", Err: err}
		l.fail(e)
		return e
	}
	l.recovered()
	return nil
}

// openLocked opens the file for appending and learns its size. It fixes a mode looser than 0600.
func (l *Log) openLocked() error {
	f, err := l.fs.OpenFile(l.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, FileMode)
	if err != nil {
		return &Error{Code: CodeUnwritable, Path: l.path, Op: "open", Err: err}
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return &Error{Code: CodeUnwritable, Path: l.path, Op: "stat", Err: err}
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return &Error{Code: CodeUnwritable, Path: l.path, Op: "open", Err: errors.New("not a regular file")}
	}
	if fi.Mode().Perm()&^FileMode != 0 {
		_ = f.Chmod(FileMode) // best effort: the file must not stay readable by others
	}
	l.f = f
	l.size = fi.Size()
	return nil
}

// drop closes a file that failed, so that the next write opens it afresh.
func (l *Log) drop() {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// fail records the failure and warns, at most once a minute.
func (l *Log) fail(err error) {
	l.err = err
	if now := l.now(); l.lastWarn.IsZero() || now.Sub(l.lastWarn) >= warnEvery {
		l.lastWarn = now
		l.logger.Warn("audit log cannot be written; records go to the journal only", "code", CodeUnwritable, "error", err.Error())
	}
}

func (l *Log) recovered() {
	if l.err != nil {
		l.err = nil
		l.lastWarn = time.Time{}
		l.logger.Info("audit log is writable again")
	}
}

// mirror logs the record to slog (journald) with component=audit. It runs outside the file lock.
func (l *Log) mirror(w wireEvent) {
	attrs := []slog.Attr{
		slog.String("action", w.Action),
		slog.String("result", w.Result),
	}
	if w.Code != nil {
		attrs = append(attrs, slog.String("code", *w.Code))
	}
	actor := []any{"kind", w.Actor.Kind}
	if w.Actor.Via != nil {
		actor = append(actor, "via", *w.Actor.Via)
	}
	if w.Actor.UID != nil {
		actor = append(actor, "uid", *w.Actor.UID)
	}
	if w.Actor.JobID != nil {
		actor = append(actor, "job_id", *w.Actor.JobID)
	}
	if w.Actor.Fingerprint != nil {
		actor = append(actor, "fingerprint", *w.Actor.Fingerprint)
	}
	attrs = append(attrs, slog.Group("actor", actor...))
	if len(w.Target) > 0 {
		var target []any
		for _, k := range sortedKeys(w.Target) {
			if v := w.Target[k]; v != nil {
				target = append(target, k, *v)
			}
		}
		if len(target) > 0 {
			attrs = append(attrs, slog.Group("target", target...))
		}
	}
	if w.OpID != nil {
		attrs = append(attrs, slog.String("op_id", *w.OpID))
	}
	if w.ReqID != nil {
		attrs = append(attrs, slog.String("req_id", *w.ReqID))
	}
	level := slog.LevelInfo
	if w.Result != string(ResultOK) {
		level = slog.LevelWarn
	}
	l.logger.LogAttrs(context.Background(), level, "audit", attrs...)
}
