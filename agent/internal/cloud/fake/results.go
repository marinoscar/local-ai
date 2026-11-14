package fake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The result schemas of cloud-app.md section 3.7 (COMMANDS, the Zod objects with .strict()), as the
// cloud checks a result with them: the fake refuses a result that does not match with a 400, so a
// test of the agent proves the shapes it sends. The patterns are those of the spec.

var (
	reModelID  = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)
	reRootID   = regexp.MustCompile(`^(internal|ark-[0-9a-f]{8})$`)
	reKeyHint  = regexp.MustCompile(`^[0-9a-f]{4}…[0-9a-f]{4}$`)
	reSemver   = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	reCatalog  = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}-\d+$`)
	reCode     = regexp.MustCompile(`^LAI-[A-Z]{2,3}-\d{3}$`)
	reISO      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	reAnything = regexp.MustCompile(`.*`)
	reSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reWorkload = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
)

type spec struct {
	kind     string // object, string, int, number, bool, array, enum, literal-true
	fields   map[string]spec
	re       *regexp.Regexp
	maxLen   int
	min      int64
	enum     []string
	elem     *spec
	maxItems int
	nullable bool
}

func obj(fields map[string]spec) spec { return spec{kind: "object", fields: fields} }
func str(re *regexp.Regexp, maxLen int) spec {
	return spec{kind: "string", re: re, maxLen: maxLen}
}
func integer(min int64) spec   { return spec{kind: "int", min: min} }
func number(min int64) spec    { return spec{kind: "number", min: min} }
func boolean() spec            { return spec{kind: "bool"} }
func enum(v ...string) spec    { return spec{kind: "enum", enum: v} }
func arr(e spec, max int) spec { return spec{kind: "array", elem: &e, maxItems: max} }
func null(s spec) spec         { s.nullable = true; return s }

var resultSchemas = map[string]spec{
	"localai.status.refresh": obj(map[string]spec{"seq": integer(0), "reported_at": str(reISO, 0)}),
	"localai.model.load": obj(map[string]spec{
		"model_id": str(reModelID, 0), "status": enum("loaded"), "load_ms": integer(0),
		"unloaded": arr(str(reModelID, 0), 16), "vram_used_mib": null(integer(0)), "is_default": boolean(),
	}),
	"localai.model.unload":      obj(map[string]spec{"model_id": str(reModelID, 0), "status": enum("unloaded"), "was_loaded": boolean()}),
	"localai.model.set_default": obj(map[string]spec{"model_id": str(reModelID, 0), "previous": null(str(reModelID, 0))}),
	"localai.model.download": obj(map[string]spec{
		"model_id": str(reModelID, 0), "already_present": boolean(), "sync_run_id": null(str(reAnything, 64)), "bytes_planned": integer(0),
	}),
	"localai.model.delete": obj(map[string]spec{
		"model_id": str(reModelID, 0), "deleted_roots": arr(str(reRootID, 0), 9), "freed_bytes": integer(0),
		"kept_roots": arr(obj(map[string]spec{"root": str(reRootID, 0), "reason": enum("last_verified_copy", "loaded", "not_present")}), 9),
	}),
	"localai.ark.sync": obj(map[string]spec{
		"sync_run_id": str(reAnything, 64), "started": boolean(),
		"plan": obj(map[string]spec{"items_to_download": integer(0), "bytes_to_download": integer(0), "items_skipped_budget": integer(0)}),
	}),
	"localai.ark.verify": obj(map[string]spec{"verify_run_id": str(reAnything, 64), "started": boolean(), "items_planned": integer(0)}),
	"localai.mode.offline": obj(map[string]spec{
		"previous_mode": enum("online", "offline", "survival"), "mode": enum("offline"),
	}),
	"localai.key.rotate": obj(map[string]spec{"fingerprint": str(reKeyHint, 0), "rotated_at": str(reISO, 0), "cloud_synced": boolean()}),
	"localai.service.restart": obj(map[string]spec{
		"service": enum("llama", "kiwix", "whisper"), "active_state": enum("active", "activating", "failed"), "restarted_at": str(reISO, 0),
	}),
	"localai.logs.tail": obj(map[string]spec{
		"unit": str(reAnything, 64), "lines": arr(str(reAnything, 2000), 500), "truncated": boolean(),
	}),
	"localai.catalog.refresh": obj(map[string]spec{
		"catalog_version": str(reCatalog, 0), "changed": boolean(), "accepted": boolean(), "rejected_code": null(str(reCode, 0)),
	}),
	"localai.ensure.run": obj(map[string]spec{"started": boolean(), "state": enum("complete", "installing", "needs_you", "off"), "pending_steps": integer(0)}),
	// E11: workloads and music (the cloud's contracts, apps/cloud .../contracts/commands.ts).
	"localai.workload.activate": obj(map[string]spec{"active": str(reWorkload, 0)}),
	"localai.music.generate": obj(map[string]spec{
		"songs": arr(obj(map[string]spec{
			"index": integer(0), "sha256": str(reSHA256, 0), "size_bytes": integer(1),
			"mime": str(reAnything, 64), "duration_s": number(0),
		}), 4),
	}),
	"localai.agent.update": obj(map[string]spec{
		"from_version": str(reSemver, 0), "to_version": str(reSemver, 0), "status": enum("updated", "already_current", "rolled_back"),
	}),
}

// ValidateResult checks a result body against the schema of its type; the problems come back as
// "path: problem" lines, empty when the result is valid. A type with no schema is a problem.
func ValidateResult(typ string, raw json.RawMessage) []string {
	sp, ok := resultSchemas[typ]
	if !ok {
		return []string{"type: unknown command type"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []string{"result: not JSON"}
	}
	var probs []string
	check(sp, v, "result", &probs)
	return probs
}

func check(sp spec, v any, path string, probs *[]string) {
	bad := func(format string, a ...any) { *probs = append(*probs, path+": "+fmt.Sprintf(format, a...)) }
	if v == nil {
		if !sp.nullable {
			bad("null is not allowed")
		}
		return
	}
	switch sp.kind {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			bad("expected an object")
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, known := sp.fields[k]; !known {
				bad("unrecognized key %q", k)
			}
		}
		names := make([]string, 0, len(sp.fields))
		for k := range sp.fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			child, present := m[k]
			if !present {
				bad("missing %q", k)
				continue
			}
			check(sp.fields[k], child, path+"."+k, probs)
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			bad("expected a string")
			return
		}
		if sp.maxLen > 0 && len([]rune(s)) > sp.maxLen {
			bad("longer than %d", sp.maxLen)
		}
		if sp.re != nil && !sp.re.MatchString(s) {
			bad("does not match %s", sp.re)
		}
	case "int":
		n, ok := v.(json.Number)
		if !ok {
			bad("expected an integer")
			return
		}
		i, err := n.Int64()
		if err != nil || strings.ContainsAny(n.String(), ".eE") {
			bad("expected an integer")
			return
		}
		if i < sp.min {
			bad("below %d", sp.min)
		}
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			bad("expected a number")
			return
		}
		f, err := n.Float64()
		if err != nil {
			bad("expected a number")
			return
		}
		if f < float64(sp.min) {
			bad("below %d", sp.min)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			bad("expected true or false")
		}
	case "enum":
		s, ok := v.(string)
		if !ok {
			bad("expected a string")
			return
		}
		found := false
		for _, e := range sp.enum {
			found = found || e == s
		}
		if !found {
			bad("%q is not one of %v", s, sp.enum)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			bad("expected an array")
			return
		}
		if sp.maxItems > 0 && len(a) > sp.maxItems {
			bad("more than %d items", sp.maxItems)
		}
		for i, e := range a {
			check(*sp.elem, e, fmt.Sprintf("%s[%d]", path, i), probs)
		}
	}
}
