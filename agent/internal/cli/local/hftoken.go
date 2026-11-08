package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// The Hugging Face token of `aictl ark hf-token set` and `status` (aictl.md section 4.10, issue #435).
// Sync reads the file at request time (ark-sync.md section 4.9.1, sync.hf_token_file, default this
// path), so writing it needs no restart. The token never leaves these functions: it is in no error,
// no return value and no audit field.

// maxHFTokenBytes bounds what HFTokenSet reads: a token file is a few dozen bytes.
const maxHFTokenBytes = 4096

// HFTokenFile is /etc/localai/hf-token (overview.md section 3.2, root:root 0600).
func (p Paths) HFTokenFile() string { return filepath.Join(p.EtcDir, "hf-token") }

// ValidHFToken reports whether token looks like a token: 8 to 256 printable ASCII characters with no
// space. It does not ask Hugging Face (that would need the internet); a wrong token shows as
// LAI-SYN-034 at the next sync.
func ValidHFToken(token string) bool {
	if len(token) < 8 || len(token) > 256 {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

// SetHFToken replaces the token file atomically, root:root 0600, with a trailing newline.
func SetHFToken(e Env, token string) error {
	path := e.Paths.HFTokenFile()
	if err := config.WriteFileAtomicFS(e.fsys(), path, []byte(token+"\n"), ModeSecret, e.RootUID, e.RootGID); err != nil {
		return fileErr("write", path, err)
	}
	return nil
}

// HFTokenSet reports whether a token is set: the file exists, is a regular file and holds something
// other than white space. A file that does not exist is false with no error; a link or a folder
// there, or a file that cannot be read, is a FileError. Only the yes/no leaves this function.
func HFTokenSet(e Env) (bool, error) {
	path := e.Paths.HFTokenFile()
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fileErr("read", path, err)
	}
	if !fi.Mode().IsRegular() {
		return false, &FileError{Op: "read", Path: path, Err: errNotRegular}
	}
	f, err := os.Open(path)
	if err != nil {
		return false, fileErr("read", path, err)
	}
	defer f.Close()
	buf := make([]byte, maxHFTokenBytes)
	n, _ := f.Read(buf)
	set := strings.TrimSpace(string(buf[:n])) != ""
	clear(buf)
	return set, nil
}
