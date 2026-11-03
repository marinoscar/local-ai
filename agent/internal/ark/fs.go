package ark

import (
	"errors"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// FS is the filesystem seam of this package. It is config.FS (which the atomic writer
// config.WriteFileAtomicFS takes) plus the two calls a root needs that config.FS lacks: Mkdir and
// the filesystem type behind a path. OSFS is the real one; tests wrap it to inject faults and
// fake a network filesystem.
type FS interface {
	config.FS
	// Mkdir creates one directory (the mode is subject to the umask; callers Chmod).
	Mkdir(name string, perm os.FileMode) error
	// FSType returns the statfs f_type magic of the filesystem that holds path. An error means
	// "unknown" (the callers treat it as local).
	FSType(path string) (uint64, error)
}

// OSFS is the real filesystem.
type OSFS struct{ config.OSFS }

// Mkdir implements FS.
func (OSFS) Mkdir(name string, perm os.FileMode) error { return os.Mkdir(name, perm) }

// FSType implements FS (statfs on Linux; ErrFSTypeUnsupported elsewhere).
func (OSFS) FSType(path string) (uint64, error) { return statfsType(path) }

// ErrFSTypeUnsupported is what OSFS.FSType returns on a platform without statfs magic numbers.
var ErrFSTypeUnsupported = errors.New("ark: filesystem type detection is not supported on this platform")

// statfs f_type magic numbers of the network filesystems (linux/magic.h). The issue and the spec
// name NFS, SMB/CIFS and FUSE; FUSE is treated as network as a whole, which covers fuse.sshfs.
const (
	MagicNFS   uint64 = 0x6969
	MagicSMB   uint64 = 0xFF534D42 // CIFS
	MagicSMB2  uint64 = 0xFE534D42 // SMB2/3 ("smb3" mounts report this one)
	MagicFUSE  uint64 = 0x65735546
	magicLimit uint64 = 1 << 32
)

// IsNetworkFS reports whether a statfs f_type magic is a network filesystem (NFS, SMB/CIFS, any
// FUSE). The magic is compared as an unsigned 32-bit value, so a sign-extended f_type (a 32-bit
// field on some architectures) compares equal.
func IsNetworkFS(magic uint64) bool {
	switch magic % magicLimit {
	case MagicNFS, MagicSMB, MagicSMB2, MagicFUSE:
		return true
	}
	return false
}
