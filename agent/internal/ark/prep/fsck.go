package prep

import (
	"strconv"
	"strings"
)

// CheckProgress is one completion line of `fsck.ext4 -C 1`: "<pass> <done> <total> <device>".
type CheckProgress struct {
	// Pass is 1 to 5 (inodes, directories, connectivity, reference counts, group summaries).
	Pass  int
	Done  int64
	Total int64
}

// Percent is how far the current pass is, 0 to 100.
func (p CheckProgress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	n := int(p.Done * 100 / p.Total)
	if n > 100 {
		n = 100
	}
	if n < 0 {
		n = 0
	}
	return n
}

// ParseCheckProgress reads one completion line. fsck prints its ordinary messages on the same output,
// so anything that is not exactly four fields with three numbers and a /dev path is not progress.
func ParseCheckProgress(line string) (CheckProgress, bool) {
	f := strings.Fields(line)
	if len(f) != 4 || !strings.HasPrefix(f[3], "/dev/") {
		return CheckProgress{}, false
	}
	pass, err1 := strconv.Atoi(f[0])
	done, err2 := strconv.ParseInt(f[1], 10, 64)
	total, err3 := strconv.ParseInt(f[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || pass < 1 || pass > 5 || done < 0 || total < 0 {
		return CheckProgress{}, false
	}
	return CheckProgress{Pass: pass, Done: done, Total: total}, true
}

// CheckOutcome is what a check found, in plain terms.
type CheckOutcome string

// The outcomes of CheckResult.
const (
	// CheckClean: no errors were found (fsck exit 0).
	CheckClean CheckOutcome = "clean"
	// CheckFixed: errors were found and all of them fixed (exit 1 or 2).
	CheckFixed CheckOutcome = "fixed"
	// CheckErrorsLeft: some errors could not be fixed (exit 4).
	CheckErrorsLeft CheckOutcome = "errors_left"
	// CheckFailed: the check itself could not run to the end (exit 8 or more: an operational error,
	// a usage error, cancelled, a library error).
	CheckFailed CheckOutcome = "failed"
)

// CheckResult reads the exit code of fsck (a bit mask, e2fsck(8)). Errors left uncorrected win over
// errors fixed; an operational error wins over both.
func CheckResult(code int) CheckOutcome {
	switch {
	case code < 0 || code >= 8:
		return CheckFailed
	case code&4 != 0:
		return CheckErrorsLeft
	case code&3 != 0:
		return CheckFixed
	}
	return CheckClean
}
