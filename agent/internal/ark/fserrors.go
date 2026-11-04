package ark

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// DefaultSysRoot is the real sysfs mount.
const DefaultSysRoot = "/sys"

// ErrErrorsUnknown: no sysfs entry for the filesystem's error counter was found (an old kernel, a
// device name that does not map). The detector treats it as "no errors recorded" and says so in the
// log; it never blocks a drive.
var ErrErrorsUnknown = errors.New("ark: ext4 errors_count is not readable")

// ErrorCounter reads the number of filesystem errors the kernel recorded for a mounted ext4
// filesystem. The detector talks to this interface so tests serve a fake sysfs.
type ErrorCounter interface {
	// ErrorsCount returns /sys/fs/ext4/<dev>/errors_count for the filesystem mounted from source
	// ("/dev/sdb1") with device numbers devNum ("8:17"). ErrErrorsUnknown means "could not find it".
	ErrorsCount(source, devNum string) (int, error)
}

// SysErrors is the real ErrorCounter: it reads sysfs through an FS, so tests give it a temp
// directory laid out like /sys.
type SysErrors struct {
	FS FS
	// Root is the sysfs mount; default DefaultSysRoot.
	Root string
}

func (s SysErrors) root() string {
	if s.Root == "" {
		return DefaultSysRoot
	}
	return s.Root
}

// ErrorsCount implements ErrorCounter. The ext4 directory under /sys/fs/ext4 is named after the
// kernel's name of the block device ("sdb1", "dm-0"). The lookup tries, in order:
//
//  1. the basename of the mount source ("/dev/sdb1" gives "sdb1");
//  2. the kernel name from /sys/dev/block/<maj:min>/uevent (DEVNAME=dm-0): the answer for a
//     /dev/mapper/<name> source, whose basename is the mapper name, not the kernel name;
//  3. the mapper name from /sys/dev/block/<maj:min>/dm/name (ark-sync.md section 4.1.2).
//
// The first name with a readable errors_count wins.
func (s SysErrors) ErrorsCount(source, devNum string) (int, error) {
	var names []string
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || strings.ContainsAny(n, "/\x00") || n == "." || n == ".." {
			return
		}
		for _, have := range names {
			if have == n {
				return
			}
		}
		names = append(names, n)
	}
	add(path.Base(source))

	if validDevNum(devNum) {
		blk := path.Join(s.root(), "dev", "block", devNum)
		if b, err := s.FS.ReadFile(path.Join(blk, "uevent")); err == nil {
			sc := bufio.NewScanner(bytes.NewReader(b))
			for sc.Scan() {
				if v, ok := strings.CutPrefix(sc.Text(), "DEVNAME="); ok {
					add(path.Base(v))
				}
			}
		}
		if b, err := s.FS.ReadFile(path.Join(blk, "dm", "name")); err == nil {
			add(string(b))
		}
	}

	for _, n := range names {
		b, err := s.FS.ReadFile(path.Join(s.root(), "fs", "ext4", n, "errors_count"))
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || v < 0 {
			return 0, fmt.Errorf("ark: errors_count of %s is %q", n, strings.TrimSpace(string(b)))
		}
		return v, nil
	}
	return 0, ErrErrorsUnknown
}

func validDevNum(s string) bool {
	maj, min, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	_, e1 := strconv.Atoi(maj)
	_, e2 := strconv.Atoi(min)
	return e1 == nil && e2 == nil
}
