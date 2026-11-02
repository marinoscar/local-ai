//go:build unix

package config

import (
	"os"
	"syscall"
)

// ownerOf returns the numeric owner of a file, or false when the platform does not say.
func ownerOf(info os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return -1, -1, false
	}
	return int(st.Uid), int(st.Gid), true
}
