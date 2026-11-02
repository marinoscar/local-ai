//go:build !unix

package config

import "os"

func ownerOf(info os.FileInfo) (uid, gid int, ok bool) { return -1, -1, false }
