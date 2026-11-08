package local

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
)

// `aictl docs inventory import <file>` (aictl.md section 4.22a, issue #435): the fix of LAI-SYN-062
// names it instead of asking a person to copy inventory.local.yaml into /etc/localai by hand. The
// file holds the real lab values (security-model.md section 5): no value from it reaches an error,
// the output or the audit log; only the reasons below do.

// maxInventoryBytes bounds the file: a real inventory is a few kilobytes.
const maxInventoryBytes = 1 << 20

// InventoryFile is /etc/localai/inventory.local.yaml (overview.md section 3.2, root:root 0600).
func (p Paths) InventoryFile() string { return filepath.Join(p.EtcDir, "inventory.local.yaml") }

// InventoryError is a file that is not a lab inventory. Reason is fixed text that never quotes the
// file: a YAML parser's message can carry a value from it.
type InventoryError struct {
	Path   string
	Reason string
}

func (e *InventoryError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Reason) }

// Reasons of InventoryError.
const (
	InvEmpty    = "it is empty"
	InvTooLarge = "it is too large to be an inventory (more than 1 MiB)"
	InvNotYAML  = "it is not valid YAML"
	InvSchema   = "it does not follow inventory.example.yaml (schema_version must be 1, serve_pattern A or B)"
)

// ImportInventory validates src as a lab inventory and copies it to InventoryFile atomically,
// root:root 0600. changed is false when the installed file already has the same bytes (nothing is
// written then).
func ImportInventory(e Env, src string) (changed bool, err error) {
	fi, err := os.Stat(src)
	if err != nil {
		return false, fileErr("read", src, err)
	}
	if !fi.Mode().IsRegular() {
		return false, fileErr("read", src, errNotRegular)
	}
	if fi.Size() > maxInventoryBytes {
		return false, &InventoryError{Path: src, Reason: InvTooLarge}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return false, fileErr("read", src, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return false, &InventoryError{Path: src, Reason: InvEmpty}
	}
	var probe map[string]any
	if yaml.Unmarshal(data, &probe) != nil || probe == nil {
		return false, &InventoryError{Path: src, Reason: InvNotYAML}
	}
	if _, perr := inventory.Parse(data); perr != nil {
		return false, &InventoryError{Path: src, Reason: InvSchema}
	}
	dst := e.Paths.InventoryFile()
	if cur, rerr := os.ReadFile(dst); rerr == nil && bytes.Equal(cur, data) && isSecretMode(dst) {
		return false, nil
	} else if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return false, fileErr("read", dst, rerr)
	}
	if err := config.WriteFileAtomicFS(e.fsys(), dst, data, ModeSecret, e.RootUID, e.RootGID); err != nil {
		return false, fileErr("write", dst, err)
	}
	return true, nil
}

// isSecretMode reports whether path is mode 0600 (an installed copy with other modes is rewritten).
func isSecretMode(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm() == ModeSecret
}
