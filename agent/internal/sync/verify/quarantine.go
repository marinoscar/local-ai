package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
)

// quarantineStamp is the layout of the <UTC stamp> directory name (the same as the item machine's).
const quarantineStamp = "20060102T150405Z"

// quarantineCopy moves a whole copy that failed its check into
// <root>/.staging/quarantine/<item>/<UTC stamp>/new, with a reason.json beside it written first (the
// same file the item machine writes for a download that did not match: ark-sync.md section 4.5). The
// item directory leaves with its MANIFEST in it, so from the instant of the rename the item is "not
// present" for every reader, and the next run fetches it again. Nothing is deleted: quarantine is not
// a copy (rule R5) and retention removes it after quarantine_days, or first when space is needed.
//
// It returns the quarantine directory. An error (a full disk, a root that went read-only) leaves the
// copy where it is, and the caller excludes it in memory instead.
func (r *Reverifier) quarantineCopy(root ark.Root, it ark.ScannedItem, bad BadFile) (string, error) {
	rel, err := filepath.Rel(root.Path, it.Dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("verify: %s is not inside root %s", it.Dir, root.ID)
	}
	fsys := r.d.FS
	now := r.d.Now()
	base := filepath.Join(root.Path, ".staging", item.QuarantineDir, it.Manifest.ItemID)
	ts := now.UTC().Format(quarantineStamp)
	dir := filepath.Join(base, ts)
	for n := 1; ; n++ {
		if _, err := fsys.Lstat(dir); err != nil {
			break
		}
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", ts, n))
	}
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("verify: quarantine: %w", err)
	}
	reason := item.Reason{
		SchemaVersion: 1, Item: it.Manifest.ItemID, Root: root.ID, Version: it.Manifest.Version,
		At: now.UTC().Format(time.RFC3339), Code: doctor.SYN025, Resolver: "reverify",
		Error: "the stored copy no longer matches its checksum (" + bad.Reason + ")",
		Files: []item.ReasonFileEntry{{
			Path: bad.Path, ExpectedSHA256: shaOf(bad.Expected), ActualSHA256: shaOf(bad.Actual),
			ExpectedBytes: bad.ExpectedBytes, Bytes: bad.Bytes,
		}},
	}
	data, err := json.MarshalIndent(reason, "", "  ")
	if err != nil {
		return "", err
	}
	if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, item.ReasonFile), append(data, '\n'), 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return "", fmt.Errorf("verify: quarantine: %w", err)
	}
	if err := fsys.Rename(it.Dir, filepath.Join(dir, "new")); err != nil {
		return "", fmt.Errorf("verify: quarantine: %w", err)
	}
	_ = fsys.SyncDir(filepath.Dir(it.Dir))
	_ = fsys.SyncDir(dir)
	return dir, nil
}

// shaOf keeps a value in reason.json only when it is a sha256 (a size compared as text is not).
func shaOf(s string) string {
	if len(s) == 64 {
		return s
	}
	return ""
}
