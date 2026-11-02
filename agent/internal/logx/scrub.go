package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// tokenPrefixes are the credential families of the token rule (security-model.md section 2).
var tokenPrefixes = [...]string{"lat_", "nod_", "lai_", "pat_", "pg_"}

// secretWords are the whole snake_case words that mark an attribute key as secret.
var secretWords = map[string]struct{}{
	"token": {}, "key": {}, "apikey": {}, "secret": {}, "pin": {}, "password": {}, "passwd": {},
	"credential": {}, "authorization": {}, "cookie": {}, "grant": {},
}

// currentKey is the live llama key (lower-case hex), used by the sha256 exemption rule.
var currentKey atomic.Pointer[string]

// SetCurrentKey tells the scrubber the live llama key (64 hex digits). A value logged under a key
// ending in sha256 is normally exempt from the 64-hex rule (it is a digest), but never when it
// contains this key. An empty string clears it. The key is held in memory only and never logged.
func SetCurrentKey(hex string) {
	hex = strings.ToLower(strings.TrimSpace(hex))
	if hex == "" {
		currentKey.Store(nil)
		return
	}
	currentKey.Store(&hex)
}

// Redact renders a secret as a fingerprint that is safe to log: values of 8 characters or fewer
// become "…"; longer ones keep the first characters of a known credential prefix (lat_, nod_,
// lai_, pat_, pg_), if any, then "…" and the last 4 characters (for example lat_…9xQz).
func Redact(v string) string {
	if utf8.RuneCountInString(v) <= 8 {
		return "…"
	}
	prefix := ""
	for _, p := range tokenPrefixes {
		if strings.HasPrefix(v, p) {
			prefix = p
			break
		}
	}
	return prefix + "…" + lastRunes(v, 4)
}

func lastRunes(s string, n int) string {
	i := len(s)
	for ; n > 0 && i > 0; n-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:]
}

// normKey lower-cases a key and replaces '-' and '.' by '_' (agent.md section 4.11).
func normKey(key string) string {
	clean := true
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c == '-' || c == '.' || (c >= 'A' && c <= 'Z') {
			clean = false
			break
		}
	}
	if clean {
		return key
	}
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case c == '-' || c == '.':
			b.WriteByte('_')
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// SensitiveKey reports whether an attribute key names a secret: it matches
//
//	(?i)(^|_)(token|key|apikey|secret|pin|password|passwd|credential|authorization|cookie|grant)(_|$)
//
// on the key lower-cased with '-' and '.' replaced by '_', so only whole snake_case words count
// (pinned, spinner, monkey and keyboard do not). Keys ending in _synced, containing fingerprint
// or ending in _sha256 are exempt.
func SensitiveKey(key string) bool {
	k := normKey(key)
	if strings.HasSuffix(k, "_synced") || strings.Contains(k, "fingerprint") || strings.HasSuffix(k, "_sha256") {
		return false
	}
	for {
		word, rest, more := strings.Cut(k, "_")
		if _, ok := secretWords[word]; ok {
			return true
		}
		if !more {
			return false
		}
		k = rest
	}
}

// Scrub wraps next so that every record's message and every attribute, including those inside
// nested groups and those added with WithAttrs, is scrubbed before next sees it:
//
//   - an attribute whose key is a SensitiveKey has its value replaced by Redact(value), whatever
//     the value's kind;
//   - string values, error values, byte slices and the strings inside structured values go
//     through ScrubString; a value under a key ending in sha256 is exempt from the 64-hex rule
//     unless it contains the key set with SetCurrentKey.
func Scrub(next slog.Handler) slog.Handler { return scrubHandler{next: next} }

type scrubHandler struct{ next slog.Handler }

func (h scrubHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h scrubHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, ScrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(scrubAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h scrubHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = scrubAttr(a)
	}
	return scrubHandler{next: h.next.WithAttrs(out)}
}

func (h scrubHandler) WithGroup(name string) slog.Handler {
	return scrubHandler{next: h.next.WithGroup(name)}
}

func scrubAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i := range g {
			out[i] = scrubAttr(g[i])
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, Redact(v.String()))
	}
	hexExempt := strings.HasSuffix(normKey(a.Key), "sha256")
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, scrub(v.String(), hexExempt))
	case slog.KindAny:
		return slog.Attr{Key: a.Key, Value: scrubAny(v.Any(), hexExempt)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// scrubAny scrubs a value of kind Any. Errors and byte slices become scrubbed strings (the JSON
// handler would print them as the error text and as base64). Anything else is serialized as
// JSON, so that the rules (including key-name redaction) reach nested fields; the value is
// returned untouched when nothing had to change.
func scrubAny(x any, hexExempt bool) slog.Value {
	switch t := x.(type) {
	case nil:
		return slog.AnyValue(nil)
	case []byte:
		return slog.StringValue(scrub(string(t), hexExempt))
	case json.Marshaler:
		// handled below with every other structured value
	case error:
		return slog.StringValue(scrub(t.Error(), hexExempt))
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return slog.AnyValue(x) // the handler reports its own marshal error; nothing to scrub
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return slog.StringValue(scrub(string(raw), hexExempt))
	}
	changed := false
	tree = scrubTree(tree, hexExempt, &changed)
	if !changed {
		return slog.AnyValue(x)
	}
	out, err := json.Marshal(tree)
	if err != nil {
		return slog.StringValue(scrub(string(raw), hexExempt))
	}
	return slog.AnyValue(json.RawMessage(out))
}

// scrubTree walks a decoded JSON tree applying the attribute rules to every object member.
func scrubTree(v any, hexExempt bool, changed *bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if SensitiveKey(k) {
				t[k] = Redact(jsonText(child))
				*changed = true
				continue
			}
			t[k] = scrubTree(child, strings.HasSuffix(normKey(k), "sha256"), changed)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = scrubTree(child, hexExempt, changed)
		}
		return t
	case string:
		if out := scrub(t, hexExempt); out != t {
			*changed = true
			return out
		}
	}
	return v
}

func jsonText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
