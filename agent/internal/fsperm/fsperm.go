// Package fsperm creates directories with an exact mode, whatever the process umask is.
//
// localai-agent.service runs with UMask=0027 (the agent writes secrets), but the content root belongs
// to the localai user and every program that reads it (llama-server, kiwix-serve, whisper, piper) runs
// as that user. os.MkdirAll(path, 0o755) under that umask makes 0750 root-owned directories nobody else
// can enter (found on a real node: piper could not open its voice). ark-sync.md section 3.1 says the
// umask must not matter for content, so content directories are created through MkdirAll below.
package fsperm

import (
	"os"
	"path/filepath"
)

// MkdirAll is os.MkdirAll, but every directory it had to create ends with exactly perm (chmod after
// the mkdir, so the umask cannot remove bits). Directories that already existed are left alone.
func MkdirAll(path string, perm os.FileMode) error {
	var missing []string
	for p := filepath.Clean(path); ; {
		if _, err := os.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	// Outermost first, the order they were created in.
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Chmod(missing[i], perm); err != nil {
			return err
		}
	}
	return nil
}
