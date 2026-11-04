//go:build !linux

package ark

import (
	"context"
	"os"
)

// DefaultMountInfoPath is the real mount table (Linux only; elsewhere nothing is detected).
const DefaultMountInfoPath = "/proc/self/mountinfo"

// ProcMounts is the real MountSource on Linux. Elsewhere it reads the file if it exists and never
// signals a change, so the detector runs on its timer alone.
type ProcMounts struct{ Path string }

// Read implements MountSource.
func (p ProcMounts) Read() ([]MountEntry, error) {
	path := p.Path
	if path == "" {
		path = DefaultMountInfoPath
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMountInfo(f)
}

// Changes implements MountSource: the channel never fires and is closed with ctx.
func (ProcMounts) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{})
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
