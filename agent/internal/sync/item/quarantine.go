package item

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// Quarantine layout (ark-sync.md sections 3.1 and 4.5): the files that failed verification move to
// <root>/.staging/quarantine/<item>/<UTC-timestamp>/ with a reason.json next to them. Quarantine is
// not a copy (rule R5): retention deletes it after quarantine_days, or first when space is needed.
const (
	// QuarantineDir is the directory below .staging that holds quarantined files.
	QuarantineDir = "quarantine"
	// ReasonFile is the explanation written next to the quarantined files.
	ReasonFile = "reason.json"
	// quarantineStamp is the layout of the <UTC-timestamp> directory name.
	quarantineStamp = "20060102T150405Z"
	// ExtraQuarantine is the key of the item's extras that counts consecutive quarantines.
	ExtraQuarantine = "quarantine"
	// BlockAfter is how many quarantines in a row block a version until the catalog changes it.
	BlockAfter = 2
)

// ReasonFileEntry is one quarantined file in reason.json.
type ReasonFileEntry struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	ActualSHA256   string `json:"actual_sha256,omitempty"`
	ExpectedBytes  int64  `json:"expected_bytes,omitempty"`
	Bytes          int64  `json:"bytes"`
	// URL is the source without query or fragment (credentials never are stored).
	URL string `json:"url,omitempty"`
}

// Reason is reason.json: what failed, with the expected and the actual checksum, the URL, the size and
// the resolver (ark-sync.md section 4.5), or the validator and its error.
type Reason struct {
	SchemaVersion int               `json:"schema_version"`
	Item          string            `json:"item"`
	Root          string            `json:"root"`
	Version       string            `json:"version"`
	At            string            `json:"at"`
	Code          string            `json:"code"`
	Resolver      string            `json:"resolver,omitempty"`
	Files         []ReasonFileEntry `json:"files,omitempty"`
	Validator     string            `json:"validator,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// qFile is one file to quarantine: where it is now and its path relative to the quarantine directory.
type qFile struct {
	Src string
	Rel string
}

// quarantineDir picks <root>/.staging/quarantine/<item>/<ts> (a free name: a second quarantine in the
// same second gets a numeric suffix).
func quarantineDir(fsys FS, rootPath, itemID string, now time.Time) string {
	base := filepath.Join(rootPath, ".staging", QuarantineDir, itemID)
	ts := now.UTC().Format(quarantineStamp)
	dir := filepath.Join(base, ts)
	for n := 1; exists(fsys, dir); n++ {
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", ts, n))
	}
	return dir
}

// quarantine moves files into a fresh quarantine directory and writes reason.json there first, so the
// explanation exists before anything is moved. It never deletes: a file that cannot be moved stays
// where it is and the error is returned.
func quarantine(fsys FS, rootPath string, reason Reason, files []qFile, now time.Time) (string, error) {
	dir := quarantineDir(fsys, rootPath, reason.Item, now)
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("quarantine: %w", err)
	}
	reason.SchemaVersion = 1
	reason.At = now.UTC().Format(time.RFC3339)
	data, err := marshalReason(reason)
	if err != nil {
		return "", err
	}
	if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, ReasonFile), data, 0o600, -1, -1); err != nil && !isNotDurable(err) {
		return "", fmt.Errorf("quarantine: %w", err)
	}
	for _, f := range files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Rel))
		if err := fsys.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return dir, fmt.Errorf("quarantine: %w", err)
		}
		if err := fsys.Rename(f.Src, dst); err != nil {
			if isNotExist(err) {
				continue // already moved by an earlier attempt
			}
			return dir, fmt.Errorf("quarantine: move %s: %w", f.Rel, err)
		}
	}
	_ = fsys.SyncDir(dir)
	_ = fsys.SyncDir(filepath.Dir(dir))
	return dir, nil
}

func marshalReason(r Reason) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func isNotDurable(err error) bool { return errors.Is(err, config.ErrNotDurable) }

// quarantineCount is the record of consecutive quarantines of one version.
type quarantineCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// readQuarantine reads the consecutive-quarantine counter of a record.
func readQuarantine(rec journal.Item) quarantineCount {
	var q quarantineCount
	if raw, ok := rec.Extras[ExtraQuarantine]; ok {
		_ = json.Unmarshal(raw, &q)
	}
	return q
}

// Blocked reports whether the record says version was quarantined BlockAfter times in a row: it stays
// blocked until the catalog changes the version (LAI-SYN-020, red).
func Blocked(rec journal.Item, version string) bool {
	q := readQuarantine(rec)
	return q.Version == version && q.Count >= BlockAfter
}

// bumpQuarantine returns the Extras of rec after one more quarantine of version.
func bumpQuarantine(rec journal.Item, version string) map[string]json.RawMessage {
	q := readQuarantine(rec)
	if q.Version != version {
		q = quarantineCount{Version: version}
	}
	q.Count++
	return setExtra(rec.Extras, ExtraQuarantine, q)
}

// clearQuarantine returns the Extras of rec without the counter.
func clearQuarantine(rec journal.Item) map[string]json.RawMessage {
	if _, ok := rec.Extras[ExtraQuarantine]; !ok {
		return rec.Extras
	}
	return setExtra(rec.Extras, ExtraQuarantine, nil)
}

func setExtra(in map[string]json.RawMessage, key string, v any) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in)+1)
	for k, raw := range in {
		out[k] = raw
	}
	if v == nil {
		delete(out, key)
	} else if raw, err := json.Marshal(v); err == nil {
		out[key] = raw
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// reasonFile builds the reason.json entry of a download that did not match.
func reasonFile(f File, rel string, res download.Result, size int64) ReasonFileEntry {
	return ReasonFileEntry{
		Path: rel, ExpectedSHA256: f.SHA256, ActualSHA256: res.SHA256, ExpectedBytes: f.Size, Bytes: size,
		URL: download.RedactURL(f.URL),
	}
}
