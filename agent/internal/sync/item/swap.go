package item

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The swap (ark-sync.md section 4.5.1). For an item with item directory D and staging directory S
// (<root>/.staging/<item>/<version>):
//
//  1. the plan is journaled (swap_plan) before the first move;
//  2. mkdir -p D and the trash directory .trash/<item>/<old version>;
//  3. the old MANIFEST.json moves to the trash FIRST: from this moment the item is "not present" on
//     the root, so a half-swapped directory never looks verified;
//  4. every old file moves to the trash (never deleted: it is the previous version, kept until
//     retention may purge it, and what rule R4 restores on failure);
//  5. every new file moves from S to D; a directory item is swapped as a whole with
//     renameat2(RENAME_EXCHANGE) and the displaced old tree moves to the trash;
//  6. all touched directories are fsynced, then S/MANIFEST.json (written and fsynced into staging
//     before step 1) is renamed to D/MANIFEST.json and D is fsynced. That rename is the commit point,
//     and the last write of the swap;
//  7. swap_done is journaled.
//
// Every step is idempotent, so replay is the same code: it redoes the plan and skips what is done.

// ErrSwapLost: a planned move has neither its source nor its destination any more. The swap cannot
// complete (LAI-SYN-026) and the previous version is restored from the trash (rule R4).
var ErrSwapLost = errors.New("item: a file of the swap is gone from both its source and its destination")

// ErrKindChanged: the item on disk is a file item and the new version a directory item, or the other
// way round. Sync does not convert one into the other.
var ErrKindChanged = errors.New("item: the new version is not the same kind of item as the one on disk")

// swapPaths is everything a swap, its replay and its restore need to know about one item, derived from
// the journaled plan alone when a crashed run is resumed.
type swapPaths struct {
	Root            ark.Root
	RelDir          string
	ItemID, Version string
	// D is the item directory, S the staging directory, T the trash directory ("" when nothing was
	// superseded).
	D, S, T string
	Plan    journal.SwapPlan
}

// manifestPair is the last rename of the plan: the commit point.
func (p swapPaths) manifestPair() (src, dst string) {
	if n := len(p.Plan.Renames); n > 0 {
		pair := p.Plan.Renames[n-1]
		return pair[0], pair[1]
	}
	return "", ""
}

// pathsFromPlan rebuilds the swapPaths of a journaled plan (a resumed run has nothing else).
func pathsFromPlan(root ark.Root, itemID, version string, plan journal.SwapPlan) (swapPaths, error) {
	p := swapPaths{Root: root, ItemID: itemID, Version: version, Plan: plan}
	src, dst := p.manifestPair()
	if src == "" || filepath.Base(dst) != manifest.FileName || filepath.Base(src) != PendingManifest {
		return swapPaths{}, fmt.Errorf("item: swap plan of %s/%s does not end with the manifest rename", root.ID, itemID)
	}
	p.S, p.D = filepath.Dir(src), filepath.Dir(dst)
	rel, err := filepath.Rel(root.Path, p.D)
	if err != nil || strings.HasPrefix(rel, "..") {
		return swapPaths{}, fmt.Errorf("item: swap plan of %s/%s leaves the root", root.ID, itemID)
	}
	p.RelDir = filepath.ToSlash(rel)
	for _, pair := range plan.Trash {
		if filepath.Base(pair[1]) == manifest.FileName {
			p.T = filepath.Dir(pair[1]) // the old manifest moves first: its directory is the trash directory
			break
		}
	}
	if p.T == "" {
		for _, pair := range plan.Renames {
			if filepath.Base(pair[0]) == TreeDir && filepath.Base(pair[1]) == TreeDir {
				p.T = filepath.Dir(pair[1]) // the displaced old tree
			}
		}
	}
	return p, nil
}

// swapInput is what planning a swap needs.
type swapInput struct {
	root    ark.Root
	relDir  string
	itemID  string
	version string
	// newM is the manifest the swap commits.
	newM manifest.Manifest
}

// planSwap builds the swap plan for newM against what is on disk now. It only reads.
func (m *Machine) planSwap(in swapInput) (swapPaths, *manifest.Manifest, error) {
	fsys := m.d.FS
	d, err := in.root.Join(filepath.FromSlash(in.relDir))
	if err != nil {
		return swapPaths{}, nil, err
	}
	p := swapPaths{
		Root: in.root, RelDir: in.relDir, ItemID: in.itemID, Version: in.version,
		D: d, S: filepath.Join(in.root.Path, ".staging", in.itemID, in.version),
	}
	var oldM *manifest.Manifest
	manifestExists := false
	if mm, err := manifest.Read(fsys, manifest.Path(d)); err == nil {
		oldM, manifestExists = &mm, true
	} else if manifest.IsInvalid(err) {
		manifestExists = true // damaged: still moved aside, as evidence
	} else if !isNotExist(err) {
		return swapPaths{}, nil, err
	}
	if oldM != nil && oldM.IsDir() != in.newM.IsDir() {
		return swapPaths{}, nil, ErrKindChanged
	}
	dirExists := false
	if in.newM.IsDir() {
		if info, err := fsys.Lstat(d); err == nil {
			if !info.IsDir() {
				return swapPaths{}, nil, fmt.Errorf("item: %s exists and is not a directory", d)
			}
			dirExists = true
		}
	}
	needTrash := manifestExists || dirExists
	if needTrash {
		label := "unversioned"
		if oldM != nil {
			label = oldM.Version
		}
		p.T = freeTrashDir(m.d.FS, in.root.Path, in.itemID, label)
	}

	src := func(rel string) string { return filepath.Join(p.S, filepath.FromSlash(rel)) }
	dst := func(rel string) string { return filepath.Join(d, filepath.FromSlash(rel)) }
	trash := func(rel string) string { return filepath.Join(p.T, filepath.FromSlash(rel)) }

	if manifestExists {
		p.Plan.Trash = append(p.Plan.Trash, [2]string{manifest.Path(d), trash(manifest.FileName)})
	}
	if oldM != nil && !oldM.IsDir() {
		for _, f := range oldM.Files {
			if exists(fsys, dst(f.Path)) {
				p.Plan.Trash = append(p.Plan.Trash, [2]string{dst(f.Path), trash(f.Path)})
			}
		}
	}
	if in.newM.IsDir() {
		tree := filepath.Join(p.S, TreeDir)
		if dirExists {
			p.Plan.Exchange = append(p.Plan.Exchange, [2]string{tree, d})
			p.Plan.Renames = append(p.Plan.Renames, [2]string{tree, trash(TreeDir)})
		} else {
			p.Plan.Renames = append(p.Plan.Renames, [2]string{tree, d})
		}
	} else {
		for _, f := range in.newM.Files {
			p.Plan.Renames = append(p.Plan.Renames, [2]string{src(f.Path), dst(f.Path)})
		}
	}
	p.Plan.Renames = append(p.Plan.Renames, [2]string{src(PendingManifest), manifest.Path(d)})
	return p, oldM, nil
}

// freeTrashDir picks .trash/<item>/<label>, or <label>.<n> when that exists (a version superseded
// twice, or "unversioned" again).
func freeTrashDir(fsys FS, rootPath, itemID, label string) string {
	base := filepath.Join(rootPath, ".trash", itemID)
	dir := filepath.Join(base, label)
	for n := 1; exists(fsys, dir); n++ {
		dir = filepath.Join(base, fmt.Sprintf("%s.%d", label, n))
	}
	return dir
}

// execute performs the plan, in the order of the spec: trash moves, directory exchanges, new files,
// directory syncs, and the manifest rename last. Every step skips what an earlier attempt already did,
// so a replay is the same call. The context is deliberately not consulted: once a swap has begun it is
// finished, a cancel never leaves it half done. It returns ErrSwapLost when a move has neither end.
func (m *Machine) execute(p swapPaths) error {
	fsys := m.d.FS
	if p.T != "" {
		if err := fsys.MkdirAll(p.T, 0o700); err != nil {
			return err
		}
	}
	mk := p.D
	if m.treeDir(p) {
		mk = filepath.Dir(p.D) // D itself is the destination of the tree rename
	}
	if err := fsys.MkdirAll(mk, 0o755); err != nil {
		return err
	}
	for _, pair := range p.Plan.Trash {
		if err := m.applyTrash(pair); err != nil {
			return err
		}
	}
	for _, pair := range p.Plan.Exchange {
		if err := m.applyExchange(p, pair); err != nil {
			return err
		}
	}
	n := len(p.Plan.Renames)
	if n == 0 {
		return fmt.Errorf("item: empty swap plan for %s/%s", p.Root.ID, p.ItemID)
	}
	for _, pair := range p.Plan.Renames[:n-1] {
		if err := m.applyRename(pair); err != nil {
			return err
		}
	}
	// Everything but the manifest is in place: make the directories durable before the commit.
	for _, dir := range syncDirs(p) {
		if err := fsys.SyncDir(dir); err != nil && !isNotExist(err) && !isUnsupportedSync(err) {
			return err
		}
	}
	if err := m.applyRename(p.Plan.Renames[n-1]); err != nil {
		return err
	}
	if err := fsys.SyncDir(p.D); err != nil && !isUnsupportedSync(err) {
		return err
	}
	return nil
}

// treeDir reports whether the plan swaps a directory item (D is the tree itself: it may not exist
// before the rename, and MkdirAll(D) must not create the destination of a rename).
func (m *Machine) treeDir(p swapPaths) bool {
	for _, pair := range p.Plan.Renames {
		if pair[1] == p.D && filepath.Base(pair[0]) == TreeDir {
			return true
		}
	}
	return len(p.Plan.Exchange) > 0
}

// syncDirs lists the directories whose entries the swap changed, parents of every destination.
func syncDirs(p swapPaths) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(p.S)
	add(p.T)
	for _, pair := range p.Plan.Trash {
		add(filepath.Dir(pair[0]))
		add(filepath.Dir(pair[1]))
	}
	for _, pair := range p.Plan.Renames {
		add(filepath.Dir(pair[0]))
		add(filepath.Dir(pair[1]))
	}
	for _, pair := range p.Plan.Exchange {
		add(filepath.Dir(pair[1]))
	}
	return out
}

// applyTrash moves an old file to the trash. A destination that exists means it was moved already:
// the source may then be the NEW file of a later step, and must not be touched.
func (m *Machine) applyTrash(pair [2]string) error {
	fsys := m.d.FS
	srcPath, dstPath := pair[0], pair[1]
	if exists(fsys, dstPath) || !exists(fsys, srcPath) {
		return nil
	}
	if err := fsys.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	return fsys.Rename(srcPath, dstPath)
}

// applyRename moves a new file into place. A missing source with an existing destination is a move
// that already happened; both missing is ErrSwapLost.
func (m *Machine) applyRename(pair [2]string) error {
	fsys := m.d.FS
	srcPath, dstPath := pair[0], pair[1]
	if !exists(fsys, srcPath) {
		if exists(fsys, dstPath) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrSwapLost, filepath.Base(dstPath))
	}
	if err := fsys.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	return fsys.Rename(srcPath, dstPath)
}

// applyExchange swaps the staged tree with the item directory. After a crash both exist either way, so
// "already exchanged" is decided by content: the directory holds the new tree when the hash of its
// SHA256SUMS is the tree hash of the pending manifest. A pending manifest that is gone means the
// commit already happened, which comes after the exchange.
func (m *Machine) applyExchange(p swapPaths, pair [2]string) error {
	fsys := m.d.FS
	staged, d := pair[0], pair[1]
	if !exists(fsys, staged) {
		if exists(fsys, d) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrSwapLost, filepath.Base(d))
	}
	if !exists(fsys, d) {
		if err := fsys.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			return err
		}
		return fsys.Rename(staged, d)
	}
	pending, err := manifest.Read(fsys, filepath.Join(p.S, PendingManifest))
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	if sums, err := manifest.ReadSums(fsys, d); err == nil {
		if tree, err := manifest.TreeSHA256(sums); err == nil && tree == pending.TreeSHA256 {
			return nil // D already holds the new tree
		}
	}
	return fsys.Exchange(staged, d)
}

// isUnsupportedSync: some filesystems refuse fsync on a directory; the rename is as durable as the
// filesystem makes it.
func isUnsupportedSync(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}

// postCheck is the swapped state's check (ark-sync.md section 4.5): every new file is in the item
// directory with the recorded size and, when the staged file was seen in this process, the same inode.
// A directory item needs its SHA256SUMS.
func (m *Machine) postCheck(p swapPaths, newM manifest.Manifest, staged map[string]os.FileInfo) error {
	fsys := m.d.FS
	if newM.IsDir() {
		info, err := fsys.Stat(filepath.Join(p.D, manifest.SumsName))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s is missing after the swap", manifest.SumsName)
		}
		return nil
	}
	for _, f := range newM.Files {
		info, err := fsys.Stat(filepath.Join(p.D, filepath.FromSlash(f.Path)))
		switch {
		case err != nil:
			return fmt.Errorf("%s is missing after the swap: %w", f.Path, err)
		case !info.Mode().IsRegular() || info.Size() != f.SizeBytes:
			return fmt.Errorf("%s has %d bytes after the swap, expected %d", f.Path, info.Size(), f.SizeBytes)
		}
		if before, ok := staged[f.Path]; ok && !os.SameFile(before, info) {
			return fmt.Errorf("%s is not the file that was staged (another inode)", f.Path)
		}
	}
	return nil
}

// Committed reports whether the item directory already holds the committed manifest of version: the
// swap's last rename happened. It is the test that tells a swap resumed after swap_done from one that
// never started.
func Committed(fsys FS, d, version string) (manifest.Manifest, bool) {
	m, err := manifest.Load(fsys, d)
	if err != nil || m.Version != version {
		return manifest.Manifest{}, false
	}
	return m, true
}

// ---- restore (rule R4) ----------------------------------------------------------------------------

// RestoreRequest asks a Restorer to put the previous version back after a failed swap.
type RestoreRequest struct {
	Root   ark.Root
	ItemID string
	// Version is the version whose swap failed.
	Version string
	// Plan is the journaled plan of that swap.
	Plan journal.SwapPlan
	// Reread re-hashes the restored files against the old manifest (rule R4).
	Reread bool
}

// Restorer restores the previous version from .trash. It reports whether there was a previous version
// to restore and an error when the restore itself did not work.
type Restorer interface {
	Restore(ctx context.Context, req RestoreRequest) (restored bool, err error)
}

// TrashRestorer is the machine's own Restorer: the reverse of the swap. The half-installed new files
// go to quarantine (they are not a copy), the old files and manifest come back from the trash, the old
// manifest last, and the old files are hashed again. Retention (SYNC-08) may replace it.
type TrashRestorer struct {
	m *Machine
}

// Restore implements Restorer.
func (r TrashRestorer) Restore(ctx context.Context, req RestoreRequest) (bool, error) {
	return r.m.restore(ctx, req)
}

func (m *Machine) restore(ctx context.Context, req RestoreRequest) (bool, error) {
	fsys := m.d.FS
	p, err := pathsFromPlan(req.Root, req.ItemID, req.Version, req.Plan)
	if err != nil {
		return false, err
	}
	q := quarantineDir(fsys, req.Root.Path, req.ItemID, m.now())
	moveNew := func(src, rel string) error {
		dst := filepath.Join(q, "new", filepath.FromSlash(rel))
		if err := fsys.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return fsys.Rename(src, dst)
	}

	// 1. De-commit: a manifest of the new version in D goes to quarantine.
	if cur, err := manifest.Read(fsys, manifest.Path(p.D)); err == nil && cur.Version == req.Version {
		if err := fsys.MkdirAll(q, 0o700); err != nil {
			return false, err
		}
		if err := fsys.Rename(manifest.Path(p.D), filepath.Join(q, manifest.FileName)); err != nil {
			return false, err
		}
	}
	// 2. New files that moved in go to quarantine; old files come back from the trash.
	for _, pair := range req.Plan.Renames[:len(req.Plan.Renames)-1] {
		srcP, dstP := pair[0], pair[1]
		if filepath.Base(srcP) == TreeDir || dstP == p.D {
			continue
		}
		if strings.HasPrefix(dstP, p.D+string(filepath.Separator)) && !exists(fsys, srcP) && exists(fsys, dstP) {
			rel, _ := filepath.Rel(p.D, dstP)
			if err := moveNew(dstP, filepath.ToSlash(rel)); err != nil {
				return false, err
			}
		}
	}
	for _, pair := range req.Plan.Trash {
		if filepath.Base(pair[1]) == manifest.FileName {
			continue
		}
		if exists(fsys, pair[1]) && !exists(fsys, pair[0]) {
			if err := fsys.MkdirAll(filepath.Dir(pair[0]), 0o755); err != nil {
				return false, err
			}
			if err := fsys.Rename(pair[1], pair[0]); err != nil {
				return false, err
			}
		}
	}
	// 3. A directory item: put the old tree back.
	if len(req.Plan.Exchange) > 0 {
		if _, err := m.restoreTree(p, q); err != nil {
			return false, err
		}
	}
	// 4. The old manifest last: from this rename the old version counts as present again.
	oldManifest := ""
	if len(req.Plan.Trash) > 0 && filepath.Base(req.Plan.Trash[0][1]) == manifest.FileName {
		oldManifest = req.Plan.Trash[0][1]
	}
	if oldManifest == "" || !exists(fsys, oldManifest) {
		if cur, err := manifest.Load(fsys, p.D); err == nil && cur.Version != req.Version {
			return true, nil // the old manifest never left: nothing was touched
		}
		return false, nil
	}
	if err := fsys.Rename(oldManifest, manifest.Path(p.D)); err != nil {
		return false, err
	}
	_ = fsys.SyncDir(p.D)
	old, err := manifest.Load(fsys, p.D)
	if err != nil {
		return false, fmt.Errorf("item: the restored version is not complete: %w", err)
	}
	if req.Reread && !old.IsDir() {
		for _, f := range old.Files {
			sum, _, err := hashFile(ctx, fsys, filepath.Join(p.D, filepath.FromSlash(f.Path)), nil)
			if err != nil {
				return false, err
			}
			if sum != f.SHA256 {
				return false, fmt.Errorf("item: the restored file %s does not match its checksum", f.Path)
			}
		}
	}
	return true, nil
}

// restoreTree undoes a directory exchange: when D holds the new tree, the old one (in the trash, or
// still in staging when the crash came before the move) is exchanged back and the new tree goes to
// quarantine.
func (m *Machine) restoreTree(p swapPaths, q string) (bool, error) {
	fsys := m.d.FS
	var pending manifest.Manifest
	havePending := false
	if mm, err := manifest.Read(fsys, filepath.Join(p.S, PendingManifest)); err == nil {
		pending, havePending = mm, true
	}
	dHoldsNew := false
	if havePending {
		if sums, err := manifest.ReadSums(fsys, p.D); err == nil {
			if tree, err := manifest.TreeSHA256(sums); err == nil && tree == pending.TreeSHA256 {
				dHoldsNew = true
			}
		}
	}
	trashTree := filepath.Join(p.T, TreeDir)
	stagedTree := filepath.Join(p.S, TreeDir)
	switch {
	case exists(fsys, trashTree) && (dHoldsNew || !exists(fsys, p.D)):
		if exists(fsys, p.D) {
			if err := fsys.Exchange(trashTree, p.D); err != nil {
				return false, err
			}
			dst := filepath.Join(q, "new", TreeDir)
			if err := fsys.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return false, err
			}
			return true, fsys.Rename(trashTree, dst)
		}
		return true, fsys.Rename(trashTree, p.D)
	case dHoldsNew && exists(fsys, stagedTree):
		// the crash came after the exchange and before the old tree moved to the trash
		return true, fsys.Exchange(stagedTree, p.D)
	}
	return false, nil
}
