package audit

import (
	"errors"
	"io/fs"
	"strconv"
)

// rotated returns the name of rotated file n (1 is the newest).
func (l *Log) rotated(n int) string { return l.path + "." + strconv.Itoa(n) }

func notExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// rotateLocked shifts the files: it drops .5, renames .4 to .5 ... .1 to .2 and audit.jsonl to
// .1, then reopens a fresh audit.jsonl. The caller holds l.mu, so no record is written while the
// names change.
//
// Exactly Keep rotated files are kept. A step that fails (other than a missing file) stops the
// shift and reports the first error; the file is reopened regardless, so appending continues on
// whatever audit.jsonl is current and no record is lost. If the reopen itself fails the log is
// degraded (LAI-AGT-013) and that error is returned.
func (l *Log) rotateLocked() error {
	l.drop()
	l.needNewline = false

	var first error
	step := func(err error) bool {
		if err != nil && !notExist(err) {
			first = &Error{Code: CodeUnwritable, Path: l.path, Op: "rotate", Err: err}
			return false
		}
		return true
	}
	ok := step(l.fs.Remove(l.rotated(Keep)))
	for i := Keep - 1; ok && i >= 1; i-- {
		ok = step(l.fs.Rename(l.rotated(i), l.rotated(i+1)))
	}
	if ok {
		step(l.fs.Rename(l.path, l.rotated(1)))
	}

	if err := l.openLocked(); err != nil {
		l.fail(err)
		return err
	}
	return first
}
