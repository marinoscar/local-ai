package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Restore is rule R4 for a live copy that rotted (the other half of R4, a swap that failed, is the
// item machine's TrashRestorer): when .trash holds an older version of the item that still hashes to
// its own manifest, that version is moved back into the item directory and the bad live files go to
// quarantine. Nothing is deleted; the live files are moved aside, never removed.
//
// The order makes a crash harmless: the live manifest moves out first (from then on the item is "not
// present" for every reader and the old version is still whole in .trash), the old files come back
// next, and the manifest of the old version is written last, after the restored files were hashed
// once more. A run of Restore that was cut short is simply run again: it finds the old version in
// .trash, or, when its files had already moved back, hashes them where they are and finishes.
//
// Only file items are restorable. A directory item (a tree with SHA256SUMS) is fetched again by the
// next run; the item stays missing until then.

// Errors of Restore.
var (
	// ErrNoRestorable: .trash holds no older version of the item that is complete and hashes to its
	// manifest. The caller re-fetches in the next run.
	ErrNoRestorable = errors.New("retention: no verified older version of the item is in .trash")
	// ErrItemBusy: the journal says the item is being swapped; restoring now would race the swap.
	ErrItemBusy = errors.New("retention: the item is being swapped")
)

// RestoreResult says what Restore did.
type RestoreResult struct {
	// Version is the version that was put back.
	Version string
	// Dir is the item directory it was put back into.
	Dir string
	// Quarantine is the directory the displaced live files went to ("" when nothing was displaced).
	Quarantine string
}

// restoreCandidate is a version in .trash that could come back.
type restoreCandidate struct {
	dir  string
	m    manifest.Manifest
	mod  time.Time
	name string
	// moved: the files already left .trash for the item directory (an earlier Restore was cut short);
	// only the manifest is still in .trash.
	moved bool
}

// Restore puts the newest verified older version of an item back on a root (R4). It returns
// ErrNoRestorable when there is none, *RefusedError (ReasonReadOnly) on a read-only root, and
// ErrItemBusy during a swap. After it, the item has a verified copy at the restored version (older
// than the catalog's, so READY shows it as stale), its journal record says verified, and the best-copy
// index knows it.
func (r *Retention) Restore(ctx context.Context, rootID, itemID string) (RestoreResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.root(rootID)
	if !ok {
		return RestoreResult{}, fmt.Errorf("retention: %s is not a mounted root", rootID)
	}
	if rootReadOnly(root) {
		return RestoreResult{}, &RefusedError{Path: root.Path, Reason: ReasonReadOnly}
	}
	if rec, ok := r.d.Journal.Get(rootID, itemID); ok {
		switch rec.State {
		case journal.StateSwapping, journal.StateSwapped:
			return RestoreResult{}, ErrItemBusy
		}
	}
	if !manifest.SafeName(itemID) {
		return RestoreResult{}, fmt.Errorf("retention: %q is not an item id", itemID)
	}

	cands := r.restoreCandidates(root, itemID)
	var pick *restoreCandidate
	var d string
	for i := range cands {
		if err := ctx.Err(); err != nil {
			return RestoreResult{}, err
		}
		dir, err := r.itemDir(root, itemID, cands[i].m)
		if err != nil {
			return RestoreResult{}, err
		}
		from := cands[i].dir
		if cands[i].moved {
			from = dir
		}
		ok, err := r.hashesToManifest(ctx, from, cands[i].m)
		if err != nil {
			return RestoreResult{}, err
		}
		if ok {
			pick, d = &cands[i], dir
			break
		}
	}
	if pick == nil {
		return RestoreResult{}, ErrNoRestorable
	}
	res := RestoreResult{Version: pick.m.Version, Dir: d}
	fsys := r.d.FS

	// 1. The files that must make way: those the old manifest names and, when the live manifest reads,
	// those it names. The live manifest itself moves first.
	displace := map[string]bool{}
	if !pick.moved {
		for _, f := range pick.m.Files {
			displace[f.Path] = true
		}
	}
	if live, err := manifest.Read(fsys, manifest.Path(d)); err == nil {
		for _, f := range live.Files {
			if !pick.moved || !containsFile(pick.m, f.Path) {
				displace[f.Path] = true
			}
		}
	}
	q := ""
	quarantine := func() (string, error) {
		if q != "" {
			return q, nil
		}
		dir := r.freeQuarantineDir(root.Path, itemID)
		if err := fsys.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		reason, _ := json.Marshal(map[string]any{
			"schema_version": 1, "item": itemID, "root": root.ID, "version": pick.m.Version,
			"at": r.d.Now().UTC().Format(time.RFC3339), "code": doctor.SYN025,
			"error": "the live copy failed re-verification; the older version " + pick.m.Version + " was restored from .trash",
		})
		if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, "reason.json"), reason, 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
			return "", err
		}
		q = dir
		return q, nil
	}
	if _, err := fsys.Lstat(manifest.Path(d)); err == nil {
		dir, err := quarantine()
		if err != nil {
			return RestoreResult{}, err
		}
		if err := fsys.Rename(manifest.Path(d), filepath.Join(dir, manifest.FileName)); err != nil {
			return RestoreResult{}, err
		}
		_ = fsys.SyncDir(d)
	}
	rels := make([]string, 0, len(displace))
	for rel := range displace {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if !manifest.ValidFilePath(rel) {
			continue
		}
		p := filepath.Join(d, filepath.FromSlash(rel))
		if _, err := fsys.Lstat(p); err != nil {
			continue
		}
		dir, err := quarantine()
		if err != nil {
			return RestoreResult{}, err
		}
		dst := filepath.Join(dir, "new", filepath.FromSlash(rel))
		if err := fsys.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return RestoreResult{}, err
		}
		if err := fsys.Rename(p, dst); err != nil {
			return RestoreResult{}, err
		}
	}
	res.Quarantine = q

	// 2. The old files come back.
	if err := fsys.MkdirAll(d, 0o755); err != nil {
		return RestoreResult{}, err
	}
	for _, f := range pick.m.Files {
		if pick.moved {
			break
		}
		src := filepath.Join(pick.dir, filepath.FromSlash(f.Path))
		dst := filepath.Join(d, filepath.FromSlash(f.Path))
		if err := fsys.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return RestoreResult{}, err
		}
		if err := fsys.Rename(src, dst); err != nil {
			return RestoreResult{}, err
		}
	}
	_ = fsys.SyncDir(d)

	// 3. Hash them again where they are now, and only then write the manifest (the commit point).
	ok, err := r.hashesToManifest(ctx, d, pick.m)
	if err != nil {
		return RestoreResult{}, err
	}
	if !ok {
		return RestoreResult{}, fmt.Errorf("retention: the restored files of %s %s do not match their checksums", itemID, pick.m.Version)
	}
	restored := pick.m
	restored.LastReverifiedAt = r.d.Now().UTC().Truncate(time.Second)
	if restored.LastReverifiedAt.Before(restored.VerifiedAt) {
		restored.LastReverifiedAt = restored.VerifiedAt
	}
	if err := manifest.Write(fsys, manifest.Path(d), restored); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return RestoreResult{}, err
	}

	// 4. The bookkeeping: the trash entry has given its files up (its manifest is the stale rest and
	// goes through the normal rules), the index learns the copy, the journal says verified.
	if rel, err := filepath.Rel(root.Path, d); err == nil && r.d.Index != nil {
		_, _ = r.d.Index.Refresh(ctx, root, filepath.ToSlash(rel))
	}
	_ = r.removeLocked(pick.dir)
	r.tidyTrashItem(filepath.Dir(pick.dir))
	if u, ok := r.d.Items.(interface {
		Update(rootID, itemID string, fn func(*journal.Item)) (journal.Item, error)
	}); ok {
		_, err := u.Update(rootID, itemID, func(it *journal.Item) {
			it.State, it.Version = journal.StateVerified, restored.Version
			it.Attempts, it.LastError, it.Code, it.PartialBytes = 0, "", "", 0
			it.Checkpoints = nil
		})
		if err != nil {
			r.d.Logger.Warn("retention: the journal record of a restored copy was not updated", "component", "sync", "root", rootID, "item", itemID, "err", err)
		}
	}
	r.d.Logger.Info("retention: older version restored from the trash", "component", "sync", "root", rootID, "item", itemID, "version", restored.Version, "quarantine", res.Quarantine)
	return res, nil
}

// restoreCandidates lists the complete file-item versions in .trash/<item>, best first: the highest
// sort_key, then the most recently written.
func (r *Retention) restoreCandidates(root ark.Root, itemID string) []restoreCandidate {
	base := filepath.Join(root.Path, ".trash", itemID)
	vers, err := r.d.FS.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []restoreCandidate
	for _, v := range vers {
		if !v.IsDir() || !manifest.SafeName(v.Name()) {
			continue
		}
		dir := filepath.Join(base, v.Name())
		m, err := manifest.Read(r.d.FS, manifest.Path(dir))
		if err != nil || m.IsDir() || m.ItemID != itemID {
			continue
		}
		c := restoreCandidate{dir: dir, m: m, name: v.Name(), moved: manifest.CheckFiles(r.d.FS, dir, m) != nil}
		if info, err := v.Info(); err == nil {
			c.mod = info.ModTime()
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].m.SortKey != out[j].m.SortKey {
			return out[i].m.SortKey > out[j].m.SortKey
		}
		if !out[i].mod.Equal(out[j].mod) {
			return out[i].mod.After(out[j].mod)
		}
		return out[i].name > out[j].name
	})
	return out
}

func containsFile(m manifest.Manifest, rel string) bool {
	for _, f := range m.Files {
		if f.Path == rel {
			return true
		}
	}
	return false
}

// itemDir is the directory the item lives in on a root: the one its live copy is in when the index
// knows it, else the one its kind and id name.
func (r *Retention) itemDir(root ark.Root, itemID string, m manifest.Manifest) (string, error) {
	for _, cp := range r.d.Copies.Copies(itemID) {
		if cp.RootID == root.ID {
			return cp.Dir, nil
		}
	}
	rel, err := ark.ItemDir(ark.ItemRef{Kind: m.ItemKind, Engine: m.Engine, ID: itemID})
	if err != nil {
		return "", fmt.Errorf("retention: where %s lives is not known: %w", itemID, err)
	}
	return root.Join(filepath.FromSlash(rel))
}

// freeQuarantineDir picks <root>/.staging/quarantine/<item>/<UTC stamp>, with a numeric suffix when
// that exists (the item machine's naming).
func (r *Retention) freeQuarantineDir(rootPath, itemID string) string {
	base := filepath.Join(rootPath, ".staging", "quarantine", itemID)
	ts := r.d.Now().UTC().Format(quarantineStampLayout)
	dir := filepath.Join(base, ts)
	for n := 1; ; n++ {
		if _, err := r.d.FS.Lstat(dir); err != nil {
			return dir
		}
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", ts, n))
	}
}

// hashesToManifest reads every file of a file item from disk and compares its size and sha256 with
// the manifest. A file that is missing, short or different is false, not an error; an error is the
// disk failing or the context ending.
func (r *Retention) hashesToManifest(ctx context.Context, dir string, m manifest.Manifest) (bool, error) {
	for _, f := range m.Files {
		sum, size, err := r.hashFile(ctx, filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		case ctx.Err() != nil:
			return false, ctx.Err()
		default:
			return false, nil // an unreadable file is not a verified one
		}
		if size != f.SizeBytes || sum != f.SHA256 {
			return false, nil
		}
	}
	return true, nil
}

func (r *Retention) hashFile(ctx context.Context, name string) (string, int64, error) {
	rd, err := r.d.FS.OpenRead(name)
	if err != nil {
		return "", 0, err
	}
	defer rd.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, rerr := rd.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", size, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
