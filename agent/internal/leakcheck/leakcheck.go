// Package leakcheck answers "did this secret reach the log?" for tests. A plain strings.Contains on
// the raw JSON log is wrong for a short secret such as a 4-digit PIN: the log carries generated
// numbers (nanosecond timestamps, durations, ports, request ids) that contain those digits by
// chance, and the test flakes. Contains parses every JSON line and looks only at the values the
// program put there, skipping the generated fields.
//
// It imports nothing from the agent, so any package's tests may use it.
package leakcheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// generated are the keys whose scalar value is produced by the runtime, not by the program's data:
// clocks, durations, sizes, counters, ids. Their values are never searched.
var generated = map[string]bool{
	"time": true, "ts": true, "timestamp": true, "at": true, "since": true,
	"expires_at": true, "created_at": true, "updated_at": true, "last_used_at": true,
	"request_id": true, "op_id": true, "duration": true, "duration_ms": true, "elapsed": true,
	"elapsed_ms": true, "latency": true, "latency_ms": true, "pid": true, "uid": true, "gid": true,
	"port": true, "seq": true, "size": true, "bytes": true, "status": true, "http_status": true,
}

// Contains reports whether secret appears in a value of the log. Every line is parsed as JSON and
// each string and number in it is searched, except values under generated keys and any string that
// is an RFC 3339 time. A line that is not JSON is searched whole.
func Contains(log, secret string) bool {
	if secret == "" {
		return false
	}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader([]byte(line)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			if strings.Contains(line, secret) {
				return true
			}
			continue
		}
		if walk(v, "", secret) {
			return true
		}
	}
	return false
}

func walk(v any, key, secret string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if strings.Contains(k, secret) || walk(e, strings.ToLower(k), secret) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if walk(e, key, secret) {
				return true
			}
		}
	case string:
		if generated[key] {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return false
		}
		return strings.Contains(x, secret)
	case json.Number:
		return !generated[key] && strings.Contains(x.String(), secret)
	}
	return false
}
