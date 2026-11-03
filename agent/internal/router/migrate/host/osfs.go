// Package host wires the migration (internal/router/migrate) to the real machine: the real filesystem,
// systemd through the allowlisted Manager, the router's HTTP API, the build manager, node.yaml,
// state.json and the preset and unit generators. It is what `aiagent migrate` runs; every piece is a
// seam of migrate with a fake in the tests of that package, and the tests here use a temporary directory
// and an httptest server, never the machine.
package host

import (
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// OSFS is migrate.FS on the real filesystem.
type OSFS struct{ config.OSFS }

// MkdirAll implements migrate.FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// Symlink implements migrate.FS.
func (OSFS) Symlink(target, link string) error { return os.Symlink(target, link) }

// Readlink implements migrate.FS.
func (OSFS) Readlink(path string) (string, error) { return os.Readlink(path) }

// OpenRead implements migrate.FS.
func (OSFS) OpenRead(name string) (io.ReadCloser, error) { return os.Open(name) }

// Dev implements migrate.FS: st_dev of the file itself.
func (OSFS) Dev(name string) (uint64, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return 0, err
	}
	return devOf(fi)
}

// FreeBytes implements migrate.FS: the space a process that is not root can use.
func (OSFS) FreeBytes(path string) (uint64, error) { return freeBytes(path) }
