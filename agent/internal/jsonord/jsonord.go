// Package jsonord writes JSON objects with a fixed key order and reads them while keeping the keys
// the reader does not know. Persisted agent files (ROOT.json, MANIFEST.json, items.json) use it so
// that a file written by a newer agent survives a rewrite by an older one, and so that the bytes
// written for a given value are always the same (operators read these files with jq and diff them).
//
// Writing: Object emits the known keys in the order given, then the unknown ones sorted by name.
// Strings are not HTML-escaped (a note or a URL may contain & or <). Reading: Decode fills the known
// keys through pointers and returns every other key verbatim.
package jsonord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// KV is one known key of an object, in output order.
type KV struct {
	Key string
	Val any
}

// Object returns the compact JSON object with the keys of known in order, then extra sorted by
// name. Callers keep known keys out of extra (Decode does).
func Object(known []KV, extra map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	put := func(key string, val any) error {
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, err := Marshal(key)
		if err != nil {
			return err
		}
		v, err := Marshal(val)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
		return nil
	}
	for _, e := range known {
		if err := put(e.Key, e.Val); err != nil {
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

// Marshal is json.Marshal without HTML escaping.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Indent re-indents compact JSON by two spaces and adds the trailing newline of a file.
func Indent(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Decode parses a JSON object. For every key of known that is present it unmarshals the value into
// the pointer; every other key is returned in extra (nil when there is none), verbatim. Data that
// is not an object is an error. A known key whose value has the wrong type is an error naming it.
func Decode(data []byte, known map[string]any) (extra map[string]json.RawMessage, err error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	for key, dst := range known {
		raw, ok := m[key]
		if !ok {
			continue
		}
		delete(m, key)
		if err := json.Unmarshal(raw, dst); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// Stamp is a time.Time that encodes as RFC 3339 in UTC to the second ("2026-11-02T03:41:10Z"), so
// the bytes written for an instant never depend on the local zone or on sub-second noise. It decodes
// any RFC 3339 string. Use it as a KV value (Stamp(t)) and as a Decode target ((*Stamp)(&t)).
type Stamp time.Time

// MarshalJSON implements json.Marshaler.
func (s Stamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(s).UTC().Truncate(time.Second).Format(time.RFC3339))
}

// UnmarshalJSON implements json.Unmarshaler. null leaves the value alone.
func (s *Stamp) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return err
	}
	*s = Stamp(t.UTC())
	return nil
}
