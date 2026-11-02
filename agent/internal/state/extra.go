package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// knownKeys caches, per struct type, the lower-cased JSON keys its fields use. encoding/json matches
// keys case-insensitively, so "Mode" fills the Mode field and must not also be kept as an unknown key.
var knownKeys sync.Map // reflect.Type -> map[string]bool

func keysOf(t reflect.Type) map[string]bool {
	if v, ok := knownKeys.Load(t); ok {
		return v.(map[string]bool)
	}
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			n, _, _ := strings.Cut(tag, ",")
			if n == "-" {
				continue
			}
			if n != "" {
				name = n
			}
		}
		keys[strings.ToLower(name)] = true
	}
	knownKeys.Store(t, keys)
	return keys
}

// decodeKeepExtra decodes the JSON object b into plain (a pointer to a struct without JSON methods)
// and stores every key plain has no field for in *extra, byte for byte.
func decodeKeepExtra(b []byte, plain any, extra *map[string]json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(b, plain); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	known := keysOf(reflect.TypeOf(plain).Elem())
	var rest map[string]json.RawMessage
	for k, v := range all {
		if known[strings.ToLower(k)] {
			continue
		}
		if rest == nil {
			rest = map[string]json.RawMessage{}
		}
		rest[k] = append(json.RawMessage(nil), v...)
	}
	*extra = rest
	return nil
}

// encodeKeepExtra encodes plain (a struct without JSON methods, so no recursion) and appends the
// extra keys, sorted, after the known ones. An extra key that collides with a known field is dropped:
// the typed value wins.
func encodeKeepExtra(plain any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(plain)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return b, nil
	}
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return nil, fmt.Errorf("state: %T did not encode to an object", plain)
	}
	known := keysOf(reflect.TypeOf(plain))
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if !known[strings.ToLower(k)] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.Write(b[:len(b)-1])
	first := len(b) == 2 // "{}"
	for _, k := range keys {
		if !first {
			out.WriteByte(',')
		}
		first = false
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out.Write(kb)
		out.WriteByte(':')
		v := extra[k]
		if len(v) == 0 {
			v = json.RawMessage("null")
		}
		out.Write(v)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
