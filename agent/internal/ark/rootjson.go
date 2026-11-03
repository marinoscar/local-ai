package ark

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// RootJSONName is the file name of the root identity file, directly under the root.
const RootJSONName = "ROOT.json"

// RootSchemaVersion is the only schema_version this code reads and writes.
const RootSchemaVersion = 1

// maxRootJSONBytes bounds what ReadRootJSON accepts; a real file is a few hundred bytes.
const maxRootJSONBytes = 64 << 10

// Errors of ReadRootJSON. A missing file is an error that satisfies errors.Is(err, fs.ErrNotExist).
var (
	// ErrRootJSONCorrupt: the file is not valid JSON, or a field is missing, has the wrong type or
	// an impossible value. The file is never rewritten for this; callers decide (the internal root
	// moves it aside, a drive is reported unusable).
	ErrRootJSONCorrupt = errors.New("ark: ROOT.json is damaged")
	// ErrRootJSONSchema: schema_version is newer than this code understands. Reading and rewriting
	// would drop fields, so the file is left alone.
	ErrRootJSONSchema = errors.New("ark: ROOT.json has an unsupported schema_version")
)

// CreatedBy records which agent made the root.
type CreatedBy struct {
	NodeID       string
	AgentVersion string
	// Extra holds unknown keys of created_by, kept verbatim on rewrite.
	Extra map[string]json.RawMessage
}

// RootJSON is ROOT.json, schema v1 (ark-sync.md section 3.2). Keys this code does not know are kept
// in Extra (and CreatedBy.Extra) and written back, so a newer agent's additions survive an older
// agent's rewrite. The key order on disk is fixed: the known keys in the order of the spec, then
// the unknown ones sorted by name.
type RootJSON struct {
	SchemaVersion int
	RootID        string
	Kind          RootKind
	// Profile is the profile the root is planned with. The internal root's value is informational.
	Profile   string
	CreatedAt time.Time
	CreatedBy CreatedBy
	Label     string
	Notes     string
	// Extra holds the unknown top-level keys.
	Extra map[string]json.RawMessage
}

// NewRootJSON returns the defaults for a root: schema 1, the root's id, kind and profile, created
// now (UTC, whole seconds), created by by, with an optional label.
func NewRootJSON(r Root, by CreatedBy, label string, now time.Time) RootJSON {
	return RootJSON{
		SchemaVersion: RootSchemaVersion,
		RootID:        r.ID,
		Kind:          r.Kind,
		Profile:       r.Profile,
		CreatedAt:     now.UTC().Truncate(time.Second),
		CreatedBy:     by,
		Label:         label,
	}
}

// Validate checks the invariants of a v1 file: the schema version, a known kind, a well-formed root
// id that matches the kind, and a creation time.
func (r RootJSON) Validate() error {
	switch {
	case r.SchemaVersion > RootSchemaVersion:
		return fmt.Errorf("%w: %d", ErrRootJSONSchema, r.SchemaVersion)
	case r.SchemaVersion != RootSchemaVersion:
		return fmt.Errorf("%w: schema_version is %d, want %d", ErrRootJSONCorrupt, r.SchemaVersion, RootSchemaVersion)
	case !r.Kind.Valid():
		return fmt.Errorf("%w: kind %q is not internal or ark_drive", ErrRootJSONCorrupt, r.Kind)
	case !ValidRootID(r.RootID):
		return fmt.Errorf("%w: root_id %q is not internal or ark-<8 hex>", ErrRootJSONCorrupt, r.RootID)
	case (r.Kind == KindInternal) != (r.RootID == InternalID):
		return fmt.Errorf("%w: kind %q does not fit root_id %q", ErrRootJSONCorrupt, r.Kind, r.RootID)
	case r.CreatedAt.IsZero():
		return fmt.Errorf("%w: created_at is missing", ErrRootJSONCorrupt)
	}
	return nil
}

// MarshalJSON writes the fixed key order (see RootJSON), compact. Encode is the file form.
func (r RootJSON) MarshalJSON() ([]byte, error) {
	by, err := marshalOrdered([]kv{
		{"node_id", r.CreatedBy.NodeID},
		{"agent_version", r.CreatedBy.AgentVersion},
	}, r.CreatedBy.Extra)
	if err != nil {
		return nil, err
	}
	raw, err := marshalOrdered([]kv{
		{"schema_version", r.SchemaVersion},
		{"root_id", r.RootID},
		{"kind", r.Kind},
		{"profile", r.Profile},
		{"created_at", r.CreatedAt.UTC().Format(time.RFC3339)},
		{"created_by", json.RawMessage(by)},
		{"label", r.Label},
		{"notes", r.Notes},
	}, r.Extra)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Encode returns the bytes of the file: the MarshalJSON key order, indented by two spaces, with a
// trailing newline. The output for a given value is always the same.
func (r RootJSON) Encode() ([]byte, error) {
	raw, err := r.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// UnmarshalJSON reads the known keys and keeps the others in Extra. Missing known keys stay zero;
// Validate (called by ReadRootJSON) decides what is required.
func (r *RootJSON) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	var out RootJSON
	var createdAt string
	var by map[string]json.RawMessage
	known := map[string]any{
		"schema_version": &out.SchemaVersion,
		"root_id":        &out.RootID,
		"kind":           &out.Kind,
		"profile":        &out.Profile,
		"created_at":     &createdAt,
		"created_by":     &by,
		"label":          &out.Label,
		"notes":          &out.Notes,
	}
	for key, dst := range known {
		raw, ok := m[key]
		if !ok {
			continue
		}
		delete(m, key)
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if createdAt != "" {
		t, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return fmt.Errorf("created_at: %w", err)
		}
		out.CreatedAt = t
	}
	byKnown := map[string]any{"node_id": &out.CreatedBy.NodeID, "agent_version": &out.CreatedBy.AgentVersion}
	for key, dst := range byKnown {
		raw, ok := by[key]
		if !ok {
			continue
		}
		delete(by, key)
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("created_by.%s: %w", key, err)
		}
	}
	if len(by) > 0 {
		out.CreatedBy.Extra = by
	}
	if len(m) > 0 {
		out.Extra = m
	}
	*r = out
	return nil
}

type kv struct {
	key string
	val any
}

// marshalOrdered writes an object with the keys of known in order, then extra sorted by name.
// Strings are not HTML-escaped (a note may contain & or <).
func marshalOrdered(known []kv, extra map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	put := func(key string, val any) error {
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, err := marshalNoEscape(key)
		if err != nil {
			return err
		}
		v, err := marshalNoEscape(val)
		if err != nil {
			return err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
		return nil
	}
	for _, e := range known {
		if err := put(e.key, e.val); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := put(name, extra[name]); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// ParseRootJSON parses and validates the content of a ROOT.json. Errors satisfy ErrRootJSONCorrupt
// or ErrRootJSONSchema.
func ParseRootJSON(data []byte) (RootJSON, error) {
	if len(data) > maxRootJSONBytes {
		return RootJSON{}, fmt.Errorf("%w: %d bytes is too large", ErrRootJSONCorrupt, len(data))
	}
	var r RootJSON
	if err := json.Unmarshal(data, &r); err != nil {
		return RootJSON{}, fmt.Errorf("%w: %v", ErrRootJSONCorrupt, err)
	}
	if err := r.Validate(); err != nil {
		return RootJSON{}, err
	}
	return r, nil
}

// ReadRootJSON reads and validates the ROOT.json at path on the real filesystem.
func ReadRootJSON(path string) (RootJSON, error) { return ReadRootJSONFS(OSFS{}, path) }

// ReadRootJSONFS is ReadRootJSON on an injected FS. A missing file satisfies
// errors.Is(err, fs.ErrNotExist); a damaged one ErrRootJSONCorrupt; a newer schema ErrRootJSONSchema.
func ReadRootJSONFS(fsys FS, path string) (RootJSON, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return RootJSON{}, err
	}
	r, err := ParseRootJSON(data)
	if err != nil {
		return RootJSON{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// WriteRootJSON validates r and writes it to path atomically (temp file, fsync, rename, fsync of the
// directory), mode 0644, on the real filesystem. Unknown keys are written back from r.Extra, so the
// caller keeps them by editing the value ReadRootJSON returned (UpdateRootJSON does exactly that).
func WriteRootJSON(path string, r RootJSON) error { return WriteRootJSONFS(OSFS{}, path, r) }

// WriteRootJSONFS is WriteRootJSON on an injected FS. A failure leaves path untouched and no temp
// file behind (config.WriteFileAtomicFS). A non-nil error wrapping config.ErrNotDurable means the
// file was replaced but the directory sync failed.
func WriteRootJSONFS(fsys FS, path string, r RootJSON) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := r.Encode()
	if err != nil {
		return err
	}
	return config.WriteFileAtomicFS(fsys, path, data, 0o644, -1, -1)
}

// UpdateRootJSON reads path, applies edit and writes the result back, keeping every unknown key
// (for example `aictl ark drives set-profile`). A missing or damaged file is an error: use
// WriteRootJSON to create one.
func UpdateRootJSON(fsys FS, path string, edit func(*RootJSON)) (RootJSON, error) {
	r, err := ReadRootJSONFS(fsys, path)
	if err != nil {
		return RootJSON{}, err
	}
	edit(&r)
	if err := WriteRootJSONFS(fsys, path, r); err != nil {
		return RootJSON{}, err
	}
	return r, nil
}

// isNotExist is errors.Is(err, fs.ErrNotExist).
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
