package local

import (
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// SetPIN hashes pin (argon2id, 64 MiB, t=3, p=1, 16-byte salt, 32-byte key, PHC string; auth.HashPIN
// refuses anything but 4 to 8 digits) and replaces /etc/localai/admin-pin atomically, root:root 0600.
// The agent reads the file on every verification, so no restart is needed. pin is not kept and
// appears in no error.
func SetPIN(e Env, pin string) error {
	phc, err := auth.HashPIN(pin)
	if err != nil {
		return err
	}
	path := e.Paths.PINFile()
	if err := config.WriteFileAtomicFS(e.fsys(), path, []byte(phc+"\n"), ModeSecret, e.RootUID, e.RootGID); err != nil {
		return fileErr("write", path, err)
	}
	return nil
}

// RotateToken writes a new agent token (auth.GenerateToken: lat_ plus 43 base64url characters) to
// /etc/localai/agent-token atomically, root:localai-operators 0640, with a trailing newline. The
// group is resolved first, so a machine without it fails before any file is touched. The agent
// re-reads the file within 10 s and sessions stay valid; the token is never returned or printed.
func RotateToken(e Env) error {
	gid, err := e.LookupGroup(OperatorsGroup)
	if err != nil {
		return &GroupError{Name: OperatorsGroup, Err: err}
	}
	path := e.Paths.TokenFile()
	if err := config.WriteFileAtomicFS(e.fsys(), path, []byte(auth.GenerateToken()+"\n"), ModeToken, e.RootUID, gid); err != nil {
		return fileErr("write", path, err)
	}
	return nil
}
