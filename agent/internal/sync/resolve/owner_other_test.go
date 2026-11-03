//go:build !unix

package resolve

import "io/fs"

func ownerUID(fs.DirEntry) (int, bool) { return 0, false }
