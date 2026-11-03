//go:build !unix

package run

import "os"

// There is no flock off Unix: the in-memory guard of the engine is the only run slot there.
func flockExclusive(*os.File) error { return nil }

func flockRelease(*os.File) error { return nil }

func isWouldBlock(error) bool { return false }
