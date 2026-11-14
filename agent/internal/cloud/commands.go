package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// The command plane's types (agent.md section 4.8.6). A cloud command is a job: the claim loop
// (Claimer) takes it from the cloud, checks it against cloud.allow and the closed set of known types,
// decodes its params strictly, and runs it through a Handler. The handlers themselves (one per type,
// each calling the same internal method as the REST route) live in internal/cloud/jobs, which imports
// this package; nothing here knows what a model or a service is.

// TypePrefix is the namespace of the Local AI job types on the cloud: "localai.model.load".
const TypePrefix = "localai."

// The 17 job names, without the prefix: the closed set the agent accepts (config.CloudJobNames).
const (
	TypeStatusRefresh   = "status.refresh"
	TypeModelLoad       = "model.load"
	TypeModelUnload     = "model.unload"
	TypeModelSetDefault = "model.set_default"
	TypeModelDownload   = "model.download"
	TypeModelDelete     = "model.delete"
	TypeArkSync         = "ark.sync"
	TypeArkVerify       = "ark.verify"
	TypeModeOffline     = "mode.offline"
	TypeKeyRotate       = "key.rotate"
	TypeServiceRestart  = "service.restart"
	TypeLogsTail        = "logs.tail"
	TypeCatalogRefresh  = "catalog.refresh"
	TypeAgentUpdate     = "agent.update"
	// TypeEnsureRun is "Install what's missing" (ADR-0010): POST /ensure with the safe default.
	TypeEnsureRun = "ensure.run"
	// TypeWorkloadActivate switches the node to a named workload (E11, #513): POST
	// /workloads/{name}/activate with wait: true.
	TypeWorkloadActivate = "workload.activate"
	// TypeMusicGenerate makes songs for the cloud (E11, #513): the sample comes and the songs go by
	// signed URL.
	TypeMusicGenerate = "music.generate"
)

// Limits are the per-type timings of cloud-app.md section 3.7.
type Limits struct {
	// Lease is the lease the cloud grants a claim of this type; it is used for the expiry recorded in
	// inflight.json when the claim answer carries no leaseExpiresAt.
	Lease time.Duration
	// HardCap is the longest the command may run, counted from the claim: the deadline of the
	// handler's context. The cloud refuses to renew past it.
	HardCap time.Duration
}

// TypeLimits returns the limits of a job name (without the prefix) and false for a name that is not
// one of the 17. The lease column of cloud-app.md section 3.7: 30 s, 60 s or 300 s; the hard cap is
// the lease times ten, except model.load (600 s) and agent.update (900 s).
func TypeLimits(name string) (Limits, bool) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	switch name {
	case TypeStatusRefresh, TypeModelUnload, TypeModelSetDefault, TypeModelDownload, TypeArkSync, TypeArkVerify, TypeModeOffline, TypeLogsTail, TypeEnsureRun:
		return Limits{Lease: ms(30000), HardCap: ms(300000)}, true
	case TypeModelLoad:
		return Limits{Lease: ms(60000), HardCap: ms(600000)}, true
	case TypeModelDelete, TypeKeyRotate, TypeServiceRestart, TypeCatalogRefresh:
		return Limits{Lease: ms(60000), HardCap: ms(600000)}, true
	case TypeAgentUpdate:
		return Limits{Lease: ms(300000), HardCap: ms(900000)}, true
	case TypeWorkloadActivate: // cloud-app.md section 3.7: lease 60 s, hard cap 900 s
		return Limits{Lease: ms(60000), HardCap: ms(900000)}, true
	case TypeMusicGenerate: // cloud-app.md section 3.7: lease 60 s, hard cap 3600 s
		return Limits{Lease: ms(60000), HardCap: ms(3600000)}, true
	}
	return Limits{}, false
}

// Job is what a handler learns about the command it runs.
type Job struct {
	// ID is the cloud's id of the command.
	ID string
	// Type is the full type, "localai.model.load".
	Type     string
	Attempts int
	// Plane is localai or eab.
	Plane string
	// Actor is the principal of the run: audit.Cloud(ID). Every audit line a handler's operation
	// writes carries it (actor: {kind: cloud, job_id}).
	Actor audit.Actor
	// Logger carries component=cloud and the job id.
	Logger *slog.Logger
}

// Outcome is the successful end of a handler.
type Outcome struct {
	// Result is the result member of the settle body; it must marshal to the type's result schema
	// of cloud-app.md section 3.7.
	Result any
	// After runs once the result has been settled (sent, or dropped for good), before the slot is
	// freed. mode.offline uses it: the result goes out first, then the node goes offline. The
	// context is not cancelled by the claim loop stopping.
	After func(ctx context.Context)
	// Defer says that the process is about to replace itself (agent.update): the command is neither
	// settled nor renewed from here on and its inflight entry stays; the new process settles it
	// (AGENT-19).
	Defer bool
}

// Handler is one job type: its strict decoder and its executor.
type Handler interface {
	// Type is the job name without the prefix, for example "model.load".
	Type() string
	// Cancellable says whether cancelling the context of Run stops the operation (a sync start, a
	// log read, a status push). A model load or unload is not: it carries on locally when the node
	// goes offline (agent.md section 4.8.8).
	Cancellable() bool
	// Decode validates params strictly and returns the typed value Run receives. An invalid params
	// object is a *DecodeError.
	Decode(raw json.RawMessage) (any, error)
	// Run executes the command. A returned error becomes failure "<code>: <message>": a *JobError
	// chooses both, an *api.Error (or anything with a doctor code, see CodeOf) gives its code.
	Run(ctx context.Context, job Job, params any) (Outcome, error)
}

// DecodeError is a params object that does not match the type's schema. Path is the field
// ("model_id", "roots[2]"), "" for the object as a whole.
type DecodeError struct {
	Path    string
	Problem string
}

// Error implements error.
func (e *DecodeError) Error() string {
	if e.Path == "" {
		return "params: " + e.Problem
	}
	return e.Path + ": " + e.Problem
}

// JobError is a failure a handler reports with its own doctor code and plain-language message.
type JobError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *JobError) Error() string { return e.Code + ": " + e.Message }

// MaxParamsBytes bounds the params object of a command; the largest legitimate one (ark.sync with
// 200 content ids) is about 14 KiB.
const MaxParamsBytes = 64 << 10

// DecodeParams decodes raw into dst (a pointer to a struct with json tags) the way the Zod schemas
// of cloud-app.md section 3.7 do with .strict(): the value must be an object (null or nothing counts
// as {}), a key that is not a field of dst (compared exactly, not case-insensitively as encoding/json
// would) is "unknown field", and a value of the wrong type is rejected. Range and pattern checks are
// the handler's; it returns a *DecodeError for them too. The returned error is always a *DecodeError.
func DecodeParams(raw json.RawMessage, dst any) error {
	if len(raw) > MaxParamsBytes {
		return &DecodeError{Problem: "larger than 64 KiB"}
	}
	rt := reflect.TypeOf(dst)
	if rt == nil || rt.Kind() != reflect.Pointer || rt.Elem().Kind() != reflect.Struct {
		return &DecodeError{Problem: "internal: the target is not a struct"}
	}
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || string(body) == "null" {
		body = []byte("{}")
	}
	if body[0] != '{' {
		return &DecodeError{Problem: "must be an object"}
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		return &DecodeError{Problem: "is not valid JSON"}
	}
	allowed := fieldNames(rt.Elem())
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !allowed[k] {
			return &DecodeError{Path: cutPath(k), Problem: "unknown field"}
		}
		if string(bytes.TrimSpace(keys[k])) == "null" {
			return &DecodeError{Path: k, Problem: "must not be null"}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var ute *json.UnmarshalTypeError
		switch {
		case errors.As(err, &ute):
			return &DecodeError{Path: cutPath(ute.Field), Problem: "must be " + typeWords(ute.Type)}
		default:
			return &DecodeError{Problem: "is not valid"}
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &DecodeError{Problem: "has data after the object"}
	}
	return nil
}

func fieldNames(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// typeWords describes a Go type in the words of a problem report.
func typeWords(t reflect.Type) string {
	if t == nil {
		return "a different type"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Pointer:
		return typeWords(t.Elem())
	}
	return "a different type"
}

// cutPath keeps a field path from the cloud short and printable before it goes into a message.
func cutPath(s string) string { return printable(s, 64) }

// printable keeps the printable ASCII of s up to n characters: text that comes from the cloud is
// never echoed back verbatim.
func printable(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= n {
			break
		}
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// Registry is the set of handlers the claim loop can run. Types missing from it are refused with
// LAI-CLD-057 like a type missing from cloud.allow.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns a Registry over hs. A handler whose type is not one of the 14 job names, or a
// duplicate, is an error.
func NewRegistry(hs ...Handler) (*Registry, error) {
	known := map[string]bool{}
	for _, n := range config.CloudJobNames() {
		known[n] = true
	}
	r := &Registry{handlers: make(map[string]Handler, len(hs))}
	for _, h := range hs {
		name := h.Type()
		if !known[name] {
			return nil, fmt.Errorf("cloud: %q is not a known job type", name)
		}
		if _, dup := r.handlers[name]; dup {
			return nil, fmt.Errorf("cloud: the job type %q is registered twice", name)
		}
		r.handlers[name] = h
	}
	return r, nil
}

// Lookup returns the handler of a job name without the prefix.
func (r *Registry) Lookup(name string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	h, ok := r.handlers[name]
	return h, ok
}

// Types returns the registered job names, sorted.
func (r *Registry) Types() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
