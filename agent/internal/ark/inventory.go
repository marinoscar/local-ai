package ark

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The copy of the lab inventory that travels on every ark drive (ark-sync.md section 4.15, SYNC-14).
//
// The inventory (`/etc/localai/inventory.local.yaml`) holds the real addresses of the lab, so it is
// kept secret everywhere: mode 0600 on the node and on the drive, never logged, never sent to the
// cloud (only the fact that it is present), and nothing here prints a byte of it. The drive carries a
// copy so a node rebuilt from the drive alone can still render its manuals with the real values.

// InventoryFile is the name of the inventory, on the node and at the top level of a drive.
const InventoryFile = "inventory.local.yaml"

// ExtrasItemID is the item id of the journal record (items.json, key `<root>/drive-extras`) that holds
// the facts of the drive extras in its `extras` bag. The record is never a catalog item: the planner and
// the readiness report look records up by catalog id, so it is ignored by both.
const ExtrasItemID = "drive-extras"

// ExtraInventory is the key under which the record stores the sha256 of the inventory as it was when it
// was last copied (items.json `extras.inventory`).
const ExtraInventory = "inventory"

// maxInventoryBytes bounds what is read: an inventory is a few kilobytes, anything bigger is not one.
const maxInventoryBytes = 1 << 20

// InventoryAction says what one inventory pass did.
type InventoryAction string

const (
	// InventoryNoNodeFile: the node has no inventory, so there is nothing to copy.
	InventoryNoNodeFile InventoryAction = "no_node_file"
	// InventoryUnchanged: both files are the same bytes.
	InventoryUnchanged InventoryAction = "unchanged"
	// InventoryCopied: the drive had none; the node's file was copied to it.
	InventoryCopied InventoryAction = "copied"
	// InventoryUpdatedDrive: the node's file changed since the last copy and replaced the drive's.
	InventoryUpdatedDrive InventoryAction = "updated_drive"
	// InventoryUpdatedNode: the drive's file changed since the last copy and replaced the node's.
	InventoryUpdatedNode InventoryAction = "updated_node"
	// InventoryConflict: both files changed since the last copy (or nothing says which is newer).
	// Nothing was overwritten; the node's file was saved on the drive as a `.conflict` file.
	InventoryConflict InventoryAction = "conflict"
)

// InventoryOptions is one pass of the inventory copy.
type InventoryOptions struct {
	FS FS
	// NodeFile is the node's inventory, normally /etc/localai/inventory.local.yaml.
	NodeFile string
	// Mount is the drive's mount point (the top level, not the localai/ folder).
	Mount string
	// NodeID names the node in a conflict file.
	NodeID string
	// Last is the sha256 stored by the previous pass for this drive ("" when there is none).
	Last string
	// Now stamps a conflict file.
	Now func() time.Time
}

// InventoryResult is what a pass did. It carries no content of either file.
type InventoryResult struct {
	Action InventoryAction
	// SHA is the sha256 the caller should store as the last copied one ("" leaves the stored one alone:
	// a conflict, or no node file).
	SHA string
	// DrivePresent is whether the drive holds an inventory after the pass.
	DrivePresent bool
	// ConflictFile is the base name of the `.conflict` file that holds the node's version ("" unless
	// Action is InventoryConflict).
	ConflictFile string
}

// Conflict says the pass found the case that raises LAI-ARK-012.
func (r InventoryResult) Conflict() bool { return r.Action == InventoryConflict }

// Code is the doctor code of the result ("" when there is nothing to tell).
func (r InventoryResult) Code() string {
	if r.Conflict() {
		return doctor.ARK012
	}
	return ""
}

type invFile struct {
	data  []byte
	sha   string
	mtime time.Time
	ok    bool
}

// SyncInventory applies the copy rule of ark-sync.md section 4.15:
//
//   - the node has no inventory: nothing to do;
//   - the same bytes: nothing to do (the caller remembers the sha);
//   - the drive has none: copy the node's file (temp file, fsync, rename, mode 0600);
//   - they differ: the file with the newer modification time wins, but only when the other file is
//     unchanged since the last copy (Last). When both changed since then, when nothing records a last
//     copy, or when the changed file is the older one, it is a conflict: the drive's file is left as it
//     is and the node's is saved beside it as inventory.local.yaml.<node-id>.<UTC time>.conflict.
//
// A conflict repeated with the same node file does not write a second conflict file.
func SyncInventory(o InventoryOptions) (InventoryResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	node, err := readInventory(o.FS, o.NodeFile)
	if err != nil {
		return InventoryResult{}, fmt.Errorf("the node's inventory: %w", err)
	}
	drivePath := filepath.Join(o.Mount, InventoryFile)
	drive, err := readInventory(o.FS, drivePath)
	if err != nil {
		return InventoryResult{}, fmt.Errorf("the drive's inventory: %w", err)
	}

	switch {
	case !node.ok:
		return InventoryResult{Action: InventoryNoNodeFile, DrivePresent: drive.ok}, nil
	case !drive.ok:
		if err := writeInventory(o.FS, drivePath, node.data); err != nil {
			return InventoryResult{}, fmt.Errorf("copy the inventory to the drive: %w", err)
		}
		return InventoryResult{Action: InventoryCopied, SHA: node.sha, DrivePresent: true}, nil
	case node.sha == drive.sha:
		return InventoryResult{Action: InventoryUnchanged, SHA: node.sha, DrivePresent: true}, nil
	}

	nodeChanged, driveChanged := node.sha != o.Last, drive.sha != o.Last
	switch {
	case o.Last != "" && nodeChanged && !driveChanged && node.mtime.After(drive.mtime):
		if err := writeInventory(o.FS, drivePath, node.data); err != nil {
			return InventoryResult{}, fmt.Errorf("copy the inventory to the drive: %w", err)
		}
		return InventoryResult{Action: InventoryUpdatedDrive, SHA: node.sha, DrivePresent: true}, nil
	case o.Last != "" && driveChanged && !nodeChanged && drive.mtime.After(node.mtime):
		if err := writeInventory(o.FS, o.NodeFile, drive.data); err != nil {
			return InventoryResult{}, fmt.Errorf("copy the inventory from the drive: %w", err)
		}
		return InventoryResult{Action: InventoryUpdatedNode, SHA: drive.sha, DrivePresent: true}, nil
	}

	name, err := saveConflict(o, node)
	if err != nil {
		return InventoryResult{Action: InventoryConflict, DrivePresent: true}, fmt.Errorf("save the conflicting inventory: %w", err)
	}
	return InventoryResult{Action: InventoryConflict, DrivePresent: true, ConflictFile: name}, nil
}

// readInventory reads one inventory. A file that does not exist is ok=false with no error; anything
// that is not a regular file of a sane size is an error (a symbolic link is never followed: the drive
// is not trusted).
func readInventory(fsys FS, path string) (invFile, error) {
	fi, err := fsys.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return invFile{}, nil
	}
	if err != nil {
		return invFile{}, err
	}
	if !fi.Mode().IsRegular() {
		return invFile{}, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if fi.Size() > maxInventoryBytes {
		return invFile{}, fmt.Errorf("%s is too large to be an inventory (%d bytes)", filepath.Base(path), fi.Size())
	}
	data, err := fsys.ReadFile(path)
	if err != nil {
		return invFile{}, err
	}
	sum := sha256.Sum256(data)
	return invFile{data: data, sha: hex.EncodeToString(sum[:]), mtime: fi.ModTime(), ok: true}, nil
}

func writeInventory(fsys FS, path string, data []byte) error {
	return config.WriteFileAtomicFS(fsys, path, data, 0o600, -1, -1)
}

var unsafeNodeChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// conflictName is inventory.local.yaml.<node-id>.<UTC time>.conflict; the node id is cut down to
// lower-case letters, digits, "_" and "-" so it can never be a path.
func conflictName(nodeID string, at time.Time) string {
	return fmt.Sprintf("%s%s.conflict", conflictPrefix(nodeID), at.UTC().Format("20060102T150405Z"))
}

// conflictPrefix is what every conflict file of one node starts with: inventory.local.yaml.<node-id>.
func conflictPrefix(nodeID string) string {
	id := unsafeNodeChars.ReplaceAllString(strings.ToLower(nodeID), "-")
	id = strings.Trim(id, "-")
	if id == "" {
		id = "node"
	}
	return InventoryFile + "." + id + "."
}

// saveConflict writes the node's inventory beside the drive's, unless this node already left a conflict
// file with the same bytes (the same conflict seen again on the next run).
func saveConflict(o InventoryOptions, node invFile) (string, error) {
	prefix := conflictPrefix(o.NodeID)
	if ents, err := o.FS.ReadDir(o.Mount); err == nil {
		for _, e := range ents {
			n := e.Name()
			if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".conflict") || e.IsDir() {
				continue
			}
			if old, err := readInventory(o.FS, filepath.Join(o.Mount, n)); err == nil && old.ok && old.sha == node.sha {
				return n, nil
			}
		}
	}
	name := conflictName(o.NodeID, o.Now())
	path := filepath.Join(o.Mount, name)
	if _, err := o.FS.Lstat(path); err == nil {
		return "", fmt.Errorf("%s exists already", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return name, writeInventory(o.FS, path, node.data)
}
