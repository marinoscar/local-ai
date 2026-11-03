// Package manifest is MANIFEST.json, the commit point of ark sync (docs/specs/ark-sync.md section
// 4.11): the file that says "this version of this item is complete and verified". It is written last
// by a swap, read by the best-copy index and by the journal rebuild, and never written in any other
// state than "verified".
//
// What the package does: the schema v1 types, strict parsing and validation (a file that fails is
// invalid and the item counts as missing, LAI-SYN-052), the deterministic encoding (fixed key order,
// unknown keys kept, history capped at 10), the atomic write, the cheap on-load check that every
// listed file is there with its recorded size (no hashing: re-verify is a later layer's job), and
// the SHA256SUMS and tree hash of directory items.
//
// What it never does: delete or move anything, hash a content file, or decide where an item lives
// (internal/ark's ItemDir does). Every filesystem call goes through config.FS so tests inject faults.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/jsonord"
)

// Fixed names and limits.
const (
	// FileName is the file name of a manifest, directly in the item directory.
	FileName = "MANIFEST.json"
	// SchemaVersion is the only schema_version this code reads and writes.
	SchemaVersion = 1
	// StateVerified is the only state a manifest on disk may have (the other states live in items.json).
	StateVerified = "verified"
	// MaxHistory is how many superseded versions a manifest remembers.
	MaxHistory = 10

	maxBytes = 4 << 20
)

// Code is the doctor code of every way a manifest can be unusable: missing or damaged, a state other
// than verified, a newer schema, or a file that is missing or has another size (LAI-SYN-052).
const Code = doctor.SYN052

// Item kinds (the item_kind field). They are the catalog's: "model" for models.yaml entries, the
// kind of the entry for ark.yaml entries.
const (
	KindModel = "model"
	KindKiwix = "kiwix"
	KindMaps  = "maps"
	KindKit   = "kit"
	KindDocs  = "docs"
)

// Trust says how the sha256 of a file was established.
type Trust string

// The trust levels (ark-sync.md section 4.11).
const (
	TrustPinned      Trust = "pinned"       // the catalog's sha256 matched the source
	TrustSidecar     Trust = "sidecar"      // a kiwix sidecar
	TrustSignedIndex Trust = "signed_index" // apt
	TrustComputed    Trust = "computed"     // pmtiles, git, docs
	TrustTOFU        Trust = "tofu"         // trust on first use (a generic url)
)

// Valid reports whether t is one of the five levels.
func (t Trust) Valid() bool {
	switch t {
	case TrustPinned, TrustSidecar, TrustSignedIndex, TrustComputed, TrustTOFU:
		return true
	}
	return false
}

// Errors of Read, Parse and Validate. All of them are LAI-SYN-052 (Code). Test with errors.Is.
var (
	// ErrCorrupt: not JSON, a field of the wrong type, a missing or impossible value.
	ErrCorrupt = errors.New("manifest: MANIFEST.json is damaged")
	// ErrSchema: schema_version is not 1. A newer file is left alone: reading and rewriting it
	// would drop what this code does not know.
	ErrSchema = errors.New("manifest: unsupported schema_version")
	// ErrNotVerified: state is not "verified". Only the journal knows other states.
	ErrNotVerified = errors.New("manifest: state is not verified")
	// ErrIncomplete: the manifest is fine but a file it lists is missing, is not a regular file or
	// has another size than recorded (CheckFiles, Load).
	ErrIncomplete = errors.New("manifest: a listed file is missing or has the wrong size")
)

// Source records where the item came from. Type is the catalog source type; every other key
// (repo, revision, book, base_url, version_policy, url, ...) is kept verbatim in Fields, so the
// resolvers own their shape and the manifest never has to know it. Keys are written sorted after
// "type".
type Source struct {
	Type   string
	Fields map[string]json.RawMessage
}

// String returns the string value of a field, or "" when it is absent or not a string.
func (s Source) String(key string) string {
	var v string
	if raw, ok := s.Fields[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return ""
}

// With returns a copy of s with the field set to the JSON encoding of v.
func (s Source) With(key string, v any) (Source, error) {
	raw, err := jsonord.Marshal(v)
	if err != nil {
		return s, err
	}
	out := Source{Type: s.Type, Fields: make(map[string]json.RawMessage, len(s.Fields)+1)}
	for k, f := range s.Fields {
		out.Fields[k] = f
	}
	out.Fields[key] = raw
	return out, nil
}

// MarshalJSON implements json.Marshaler.
func (s Source) MarshalJSON() ([]byte, error) {
	return jsonord.Object([]jsonord.KV{{Key: "type", Val: s.Type}}, s.Fields)
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Source) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{"type": &s.Type})
	if err != nil {
		return err
	}
	s.Fields = extra
	return nil
}

// File is one file of a file item. Path is relative to the item directory, slash separated.
type File struct {
	Kind         string
	Path         string
	SizeBytes    int64
	SHA256       string
	SHA256Source string
	URL          string
	// ZimMD5OK is the result of the ZIM internal checksum; nil for anything that is not a ZIM.
	ZimMD5OK *bool
	// Extra holds unknown keys, kept verbatim.
	Extra map[string]json.RawMessage
}

// MarshalJSON implements json.Marshaler.
func (f File) MarshalJSON() ([]byte, error) {
	kv := []jsonord.KV{{Key: "kind", Val: f.Kind}, {Key: "path", Val: f.Path}, {Key: "size_bytes", Val: f.SizeBytes}, {Key: "sha256", Val: f.SHA256}, {Key: "sha256_source", Val: f.SHA256Source}}
	if f.URL != "" {
		kv = append(kv, jsonord.KV{Key: "url", Val: f.URL})
	}
	if f.ZimMD5OK != nil {
		kv = append(kv, jsonord.KV{Key: "zim_md5_ok", Val: *f.ZimMD5OK})
	}
	return jsonord.Object(kv, f.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *File) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{
		"kind": &f.Kind, "path": &f.Path, "size_bytes": &f.SizeBytes, "sha256": &f.SHA256,
		"sha256_source": &f.SHA256Source, "url": &f.URL, "zim_md5_ok": &f.ZimMD5OK,
	})
	f.Extra = extra
	return err
}

// SyncedBy records which agent run wrote the manifest.
type SyncedBy struct {
	NodeID       string
	AgentVersion string
	RunID        string
	Extra        map[string]json.RawMessage
}

// MarshalJSON implements json.Marshaler.
func (s SyncedBy) MarshalJSON() ([]byte, error) {
	return jsonord.Object([]jsonord.KV{{Key: "node_id", Val: s.NodeID}, {Key: "agent_version", Val: s.AgentVersion}, {Key: "run_id", Val: s.RunID}}, s.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *SyncedBy) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{"node_id": &s.NodeID, "agent_version": &s.AgentVersion, "run_id": &s.RunID})
	s.Extra = extra
	return err
}

// HistoryEntry is the metadata of one superseded version.
type HistoryEntry struct {
	Version      string
	VerifiedAt   time.Time
	SupersededAt time.Time
	Extra        map[string]json.RawMessage
}

// MarshalJSON implements json.Marshaler.
func (h HistoryEntry) MarshalJSON() ([]byte, error) {
	return jsonord.Object([]jsonord.KV{
		{Key: "version", Val: h.Version}, {Key: "verified_at", Val: jsonord.Stamp(h.VerifiedAt)}, {Key: "superseded_at", Val: jsonord.Stamp(h.SupersededAt)},
	}, h.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (h *HistoryEntry) UnmarshalJSON(data []byte) error {
	extra, err := jsonord.Decode(data, map[string]any{
		"version": &h.Version, "verified_at": (*jsonord.Stamp)(&h.VerifiedAt), "superseded_at": (*jsonord.Stamp)(&h.SupersededAt),
	})
	h.Extra = extra
	return err
}

// Manifest is MANIFEST.json, schema v1. It describes either a file item (Files) or a directory item
// (TreeSHA256 and FileCount, the per-file list being the item directory's SHA256SUMS), never both.
type Manifest struct {
	SchemaVersion int
	ItemID        string
	ItemKind      string
	// Engine is the model engine (llama, whisper, piper); empty for content items.
	Engine         string
	CatalogVersion string
	// Version identifies the content: the revision prefix of a model, "2026-10" for a ZIM.
	Version string
	// SortKey orders versions of one item by plain string comparison (dates sort lexically).
	SortKey string
	// State is always StateVerified on disk.
	State  string
	Source Source
	// Files lists the files of a file item. Directory items leave it empty.
	Files []File
	// TreeSHA256 and FileCount describe a directory item; see TreeSHA256.
	TreeSHA256 string
	FileCount  int
	TotalBytes int64

	DownloadedAt     time.Time
	VerifiedAt       time.Time
	LastReverifiedAt time.Time

	Trust    Trust
	SyncedBy SyncedBy
	// History is newest first and holds at most MaxHistory entries.
	History []HistoryEntry
	// Extra holds the unknown top-level keys, kept verbatim and written back sorted by name.
	Extra map[string]json.RawMessage
}

// IsDir reports whether this is a directory item (it has a tree hash instead of a file list).
func (m Manifest) IsDir() bool { return m.TreeSHA256 != "" }

// Validate checks the manifest as a reader and a writer both need it. It returns ErrSchema,
// ErrNotVerified or ErrCorrupt (wrapped with the reason).
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version is %d, this agent reads %d", ErrSchema, m.SchemaVersion, SchemaVersion)
	}
	if m.State != StateVerified {
		return fmt.Errorf("%w: state is %q", ErrNotVerified, m.State)
	}
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
	}
	if !SafeName(m.ItemID) {
		return bad("item_id %q is not a plain name", m.ItemID)
	}
	if m.ItemKind == "" {
		return bad("item_kind is empty")
	}
	if m.ItemKind == KindModel && m.Engine == "" {
		return bad("a model needs an engine")
	}
	if !SafeName(m.Version) {
		return bad("version %q is not a plain name", m.Version)
	}
	if m.SortKey == "" {
		return bad("sort_key is empty")
	}
	if m.Source.Type == "" {
		return bad("source.type is empty")
	}
	if !m.Trust.Valid() {
		return bad("trust %q is not one of pinned, sidecar, signed_index, computed, tofu", m.Trust)
	}
	if m.TotalBytes < 0 {
		return bad("total_bytes is negative")
	}
	for _, st := range []struct {
		name string
		at   time.Time
	}{{"downloaded_at", m.DownloadedAt}, {"verified_at", m.VerifiedAt}, {"last_reverified_at", m.LastReverifiedAt}} {
		if st.at.IsZero() {
			return bad("%s is missing", st.name)
		}
	}
	if m.IsDir() {
		if !IsSHA256(m.TreeSHA256) {
			return bad("tree_sha256 is not 64 lowercase hex digits")
		}
		if len(m.Files) != 0 {
			return bad("a directory item lists no files (they are in SHA256SUMS)")
		}
		if m.FileCount < 1 {
			return bad("file_count must be at least 1")
		}
		return nil
	}
	if len(m.Files) == 0 {
		return bad("no files and no tree_sha256")
	}
	if m.FileCount != 0 {
		return bad("file_count belongs to directory items")
	}
	seen := make(map[string]bool, len(m.Files))
	for i, f := range m.Files {
		switch {
		case f.Kind == "":
			return bad("files[%d].kind is empty", i)
		case !ValidFilePath(f.Path):
			return bad("files[%d].path %q is not a relative path inside the item", i, f.Path)
		case seen[f.Path]:
			return bad("files[%d].path %q is listed twice", i, f.Path)
		case f.SizeBytes < 0:
			return bad("files[%d].size_bytes is negative", i)
		case !IsSHA256(f.SHA256):
			return bad("files[%d].sha256 is not 64 lowercase hex digits", i)
		}
		seen[f.Path] = true
	}
	return nil
}

// SafeName reports whether s can be one path element: not empty, not "." or "..", no separator, no
// control character, no leading or trailing space.
func SafeName(s string) bool {
	if s == "" || s == "." || s == ".." || s != strings.TrimSpace(s) || strings.ContainsAny(s, `/\`) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidFilePath reports whether p is a clean relative slash path that stays inside the item
// directory and is not the manifest or SHA256SUMS itself.
func ValidFilePath(p string) bool {
	if !fs.ValidPath(p) || p == "." || p == FileName || p == SumsName || strings.ContainsRune(p, '\\') {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return path.Clean(p) == p
}

// IsSHA256 reports whether s is exactly 64 lowercase hex digits.
func IsSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Trimmed returns a copy of m whose history is cut to MaxHistory (the newest entries stay).
func (m Manifest) Trimmed() Manifest {
	if len(m.History) > MaxHistory {
		m.History = append([]HistoryEntry(nil), m.History[:MaxHistory]...)
	}
	return m
}

// SupersededBy builds the history of the manifest that replaces m: m's own entry first (version,
// verified_at, and the instant it was superseded), then m's history, cut to MaxHistory. The swap
// stores the result in the new manifest's History.
func (m Manifest) SupersededBy(at time.Time) []HistoryEntry {
	h := append([]HistoryEntry{{Version: m.Version, VerifiedAt: m.VerifiedAt, SupersededAt: at}}, m.History...)
	if len(h) > MaxHistory {
		h = h[:MaxHistory]
	}
	return h
}

// MarshalJSON implements json.Marshaler: the key order of the spec, then unknown keys by name. The
// value is not validated here (Encode does that).
func (m Manifest) MarshalJSON() ([]byte, error) {
	kv := []jsonord.KV{
		{Key: "schema_version", Val: m.SchemaVersion}, {Key: "item_id", Val: m.ItemID}, {Key: "item_kind", Val: m.ItemKind},
	}
	if m.Engine != "" {
		kv = append(kv, jsonord.KV{Key: "engine", Val: m.Engine})
	}
	kv = append(kv,
		jsonord.KV{Key: "catalog_version", Val: m.CatalogVersion}, jsonord.KV{Key: "version", Val: m.Version}, jsonord.KV{Key: "sort_key", Val: m.SortKey},
		jsonord.KV{Key: "state", Val: m.State}, jsonord.KV{Key: "source", Val: m.Source},
	)
	if m.IsDir() {
		kv = append(kv, jsonord.KV{Key: "tree_sha256", Val: m.TreeSHA256}, jsonord.KV{Key: "file_count", Val: m.FileCount})
	} else {
		files := m.Files
		if files == nil {
			files = []File{}
		}
		kv = append(kv, jsonord.KV{Key: "files", Val: files})
	}
	history := m.History
	if history == nil {
		history = []HistoryEntry{}
	}
	kv = append(kv,
		jsonord.KV{Key: "total_bytes", Val: m.TotalBytes},
		jsonord.KV{Key: "downloaded_at", Val: jsonord.Stamp(m.DownloadedAt)}, jsonord.KV{Key: "verified_at", Val: jsonord.Stamp(m.VerifiedAt)},
		jsonord.KV{Key: "last_reverified_at", Val: jsonord.Stamp(m.LastReverifiedAt)},
		jsonord.KV{Key: "trust", Val: m.Trust}, jsonord.KV{Key: "synced_by", Val: m.SyncedBy}, jsonord.KV{Key: "history", Val: history},
	)
	return jsonord.Object(kv, m.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. It fills the fields and validates nothing.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var trust string
	extra, err := jsonord.Decode(data, map[string]any{
		"schema_version": &m.SchemaVersion, "item_id": &m.ItemID, "item_kind": &m.ItemKind, "engine": &m.Engine,
		"catalog_version": &m.CatalogVersion, "version": &m.Version, "sort_key": &m.SortKey, "state": &m.State,
		"source": &m.Source, "files": &m.Files, "tree_sha256": &m.TreeSHA256, "file_count": &m.FileCount,
		"total_bytes": &m.TotalBytes, "downloaded_at": (*jsonord.Stamp)(&m.DownloadedAt),
		"verified_at": (*jsonord.Stamp)(&m.VerifiedAt), "last_reverified_at": (*jsonord.Stamp)(&m.LastReverifiedAt),
		"trust": &trust, "synced_by": &m.SyncedBy, "history": &m.History,
	})
	m.Trust = Trust(trust)
	m.Extra = extra
	return err
}

// Parse parses and validates the content of a MANIFEST.json. Errors satisfy ErrCorrupt, ErrSchema
// or ErrNotVerified. The schema is checked before the fields, so a newer file whose fields changed
// shape is ErrSchema, not ErrCorrupt.
func Parse(data []byte) (Manifest, error) {
	if len(data) > maxBytes {
		return Manifest{}, fmt.Errorf("%w: %d bytes is too large", ErrCorrupt, len(data))
	}
	var head struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	switch {
	case head.SchemaVersion == nil:
		return Manifest{}, fmt.Errorf("%w: schema_version is missing", ErrCorrupt)
	case *head.SchemaVersion > SchemaVersion:
		return Manifest{}, fmt.Errorf("%w: schema_version %d is newer than %d", ErrSchema, *head.SchemaVersion, SchemaVersion)
	case *head.SchemaVersion < SchemaVersion:
		return Manifest{}, fmt.Errorf("%w: schema_version %d", ErrCorrupt, *head.SchemaVersion)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Encode validates m and returns the bytes of the file: indented by two spaces, trailing newline,
// history cut to MaxHistory. The same value always gives the same bytes.
func (m Manifest) Encode() ([]byte, error) {
	m = m.Trimmed()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, err := m.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return jsonord.Indent(raw)
}

// Path is the manifest's path in an item directory.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Read reads and validates the manifest at filePath. A missing file satisfies
// errors.Is(err, fs.ErrNotExist) and is not one of the invalid errors: it is the normal "this
// item is not here".
func Read(fsys config.FS, filePath string) (Manifest, error) {
	data, err := fsys.ReadFile(filePath)
	if err != nil {
		return Manifest{}, err
	}
	m, err := Parse(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", filePath, err)
	}
	return m, nil
}

// Write validates m and replaces the manifest at filePath atomically (temp file, fsync, rename,
// fsync of the directory), mode 0644, so a crash leaves the old file or the new one. This is the
// commit point of a swap: call it last. A failure leaves filePath untouched and no temp file; an
// error wrapping config.ErrNotDurable means the file was replaced but the directory sync failed.
func Write(fsys config.FS, filePath string, m Manifest) error {
	data, err := m.Encode()
	if err != nil {
		return err
	}
	return config.WriteFileAtomicFS(fsys, filePath, data, 0o644, -1, -1)
}

// CheckFiles is the cheap "is it all there" test of a load (no hashing): every listed file exists
// in dir as a regular file with the recorded size; a directory item needs its SHA256SUMS. A
// failure wraps ErrIncomplete and names the file.
func CheckFiles(fsys config.FS, dir string, m Manifest) error {
	if m.IsDir() {
		info, err := fsys.Stat(filepath.Join(dir, SumsName))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is missing", ErrIncomplete, SumsName)
		}
		return nil
	}
	for _, f := range m.Files {
		info, err := fsys.Stat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case err != nil:
			return fmt.Errorf("%w: %s: %v", ErrIncomplete, f.Path, err)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%w: %s is not a regular file", ErrIncomplete, f.Path)
		case info.Size() != f.SizeBytes:
			return fmt.Errorf("%w: %s is %d bytes, the manifest says %d", ErrIncomplete, f.Path, info.Size(), f.SizeBytes)
		}
	}
	return nil
}

// Load is Read of dir/MANIFEST.json followed by CheckFiles: what the best-copy index and the
// rebuild call. Errors: fs.ErrNotExist (no manifest: the item is not here), or one of ErrCorrupt,
// ErrSchema, ErrNotVerified, ErrIncomplete (LAI-SYN-052).
func Load(fsys config.FS, dir string) (Manifest, error) {
	m, err := Read(fsys, Path(dir))
	if err != nil {
		return Manifest{}, err
	}
	if err := CheckFiles(fsys, dir, m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", Path(dir), err)
	}
	return m, nil
}

// IsInvalid reports whether err is one of the LAI-SYN-052 errors (not a missing file, not an I/O
// error of the drive).
func IsInvalid(err error) bool {
	return errors.Is(err, ErrCorrupt) || errors.Is(err, ErrSchema) || errors.Is(err, ErrNotVerified) || errors.Is(err, ErrIncomplete)
}
