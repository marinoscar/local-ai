//go:build !unix

package local

import "os"

func ownerOf(os.FileInfo) (uid, gid int) { return -1, -1 }
