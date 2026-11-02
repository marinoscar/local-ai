//go:build !unix

package router

import "os"

func ownerOf(info os.FileInfo) (uid, gid int, ok bool) { return -1, -1, false }
