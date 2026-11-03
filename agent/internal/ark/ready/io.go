package ready

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// maxFileBytes bounds what Read takes in: a report of a few hundred items is a couple of hundred KB.
const maxFileBytes = 4 << 20

// Path is where the report of a root lives: <root>/READY.json.
func Path(rootPath string) string { return filepath.Join(rootPath, FileName) }

// Write stores the report of a root atomically (temp file in the same directory, fsync, rename, fsync
// of the directory), so a reader or a power cut sees the old file or the new one and never half of
// it. It returns the exact bytes written, which are also what the cloud link sends. The file is 0644
// like every file of a root.
//
// A failure leaves the previous file untouched. An error wrapping config.ErrNotDurable means the new
// file is in place and only its survival of a power cut is not guaranteed; IsDurabilityOnly says so.
// Callers do not call Write for a read-only root: its report stays in memory.
func Write(fsys config.FS, rootPath string, r Report) ([]byte, error) {
	data, err := r.MarshalIndented()
	if err != nil {
		return nil, err
	}
	if err := config.WriteFileAtomicFS(fsys, Path(rootPath), data, 0o644, -1, -1); err != nil {
		return data, err
	}
	return data, nil
}

// IsDurabilityOnly reports whether a Write error means the report was replaced and only the final
// directory sync failed.
func IsDurabilityOnly(err error) bool { return errors.Is(err, config.ErrNotDurable) }

// Read loads and parses the report of a root, returning the parsed value and the bytes it came from
// (the cloud link sends the bytes as they are). A missing file is an error satisfying
// errors.Is(err, fs.ErrNotExist); a file that is not a report is ErrCorrupt or ErrSchema, and is
// never repaired or removed here.
func Read(fsys config.FS, rootPath string) (Report, []byte, error) {
	path := Path(rootPath)
	if info, err := fsys.Stat(path); err == nil && info.Size() > maxFileBytes {
		return Report{}, nil, fmt.Errorf("%w: %s is %d bytes", ErrCorrupt, FileName, info.Size())
	}
	data, err := fsys.ReadFile(path)
	if err != nil {
		return Report{}, nil, err
	}
	if len(data) > maxFileBytes {
		return Report{}, nil, fmt.Errorf("%w: %s is %d bytes", ErrCorrupt, FileName, len(data))
	}
	r, err := Parse(data)
	if err != nil {
		return Report{}, nil, err
	}
	return r, data, nil
}
