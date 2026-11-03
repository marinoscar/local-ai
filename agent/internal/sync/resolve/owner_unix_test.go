//go:build unix

package resolve

import (
	"io/fs"
	"syscall"
)

// ownerUID is the owning uid of a directory entry.
func ownerUID(d fs.DirEntry) (int, bool) {
	info, err := d.Info()
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
