//go:build unix

package local

import (
	"os"
	"syscall"
)

// ownerOf returns the numeric owner of a file, or -1, -1 when the platform does not say.
func ownerOf(fi os.FileInfo) (uid, gid int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}
