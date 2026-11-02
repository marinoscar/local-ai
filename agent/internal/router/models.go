package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Status is the value of a model's status in GET /models (llama.cpp tools/server, the router's
// server_model_status): the seven values below. A newer llama-server may add one; it is kept as is
// (Known reports false) rather than refused, so the watcher never goes blind on an upgrade.
type Status string

// The status values of the router (llama-router.md section 3.6).
const (
	StatusUnloaded    Status = "unloaded"
	StatusLoading     Status = "loading"
	StatusLoaded      Status = "loaded"
	StatusSleeping    Status = "sleeping"
	StatusDownloading Status = "downloading"
	StatusDownloaded  Status = "downloaded"
	StatusIncomplete  Status = "incomplete"
)

// Known reports whether s is one of the seven values this client was written against.
func (s Status) Known() bool {
	switch s {
	case StatusUnloaded, StatusLoading, StatusLoaded, StatusSleeping,
		StatusDownloading, StatusDownloaded, StatusIncomplete:
		return true
	}
	return false
}

// Running reports whether the router has a child process for the model (the router's is_running:
// loaded, loading or sleeping).
func (s Status) Running() bool {
	return s == StatusLoaded || s == StatusLoading || s == StatusSleeping
}

// Resident reports whether the model's weights are in (or on their way into) memory and it was
// loaded at some point: loaded or sleeping. A model going from a resident status to a non-resident
// one outside an agent operation is what the watcher calls a crash.
func (s Status) Resident() bool { return s == StatusLoaded || s == StatusSleeping }

// Router model sources (server_model_source). Only "preset" belongs in the agent's models.ini
// world; "cache" is a LLAMA_CACHE model (LAI-LLM-024).
const (
	SourcePreset    = "preset"
	SourceModelsDir = "models_dir"
	SourceCache     = "cache"
)

// Download is the progress of one file of a downloading model (status.progress, a map keyed by
// URL). The agent never starts a download through the router; the field is parsed so that a
// cache download that someone else started is visible.
type Download struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// DeviceMemory is one entry of a memory estimate's devices, in bytes. Kind is "device" (a GPU) or
// "host".
type DeviceMemory struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Total   int64  `json:"total"`
	Free    int64  `json:"free"`
	Model   int64  `json:"model"`
	Context int64  `json:"context"`
	Compute int64  `json:"compute"`
}

// MemoryEstimate is memory.min, memory.max or memory.effective of a model (sizes in bytes).
type MemoryEstimate struct {
	NCtx    int64          `json:"n_ctx"`
	Devices []DeviceMemory `json:"devices"`
}

// DeviceMiB is the graphics memory the estimate needs: model + context + compute of every entry of
// kind "device" (the host entry is RAM, which the planner does not budget as VRAM), in MiB rounded up.
func (e *MemoryEstimate) DeviceMiB() int64 {
	if e == nil {
		return 0
	}
	var b int64
	for _, d := range e.Devices {
		if d.Kind == "device" {
			b += d.Model + d.Context + d.Compute
		}
	}
	return (b + (1 << 20) - 1) >> 20
}

// Memory is the memory object of a model; it is present only when llama-server runs with
// --models-memory-margin (memory.status is unmeasured, measuring, measured or failed; effective is
// null unless the model is loaded).
type Memory struct {
	Status    string          `json:"status"`
	Min       *MemoryEstimate `json:"min"`
	Max       *MemoryEstimate `json:"max"`
	Effective *MemoryEstimate `json:"effective"`
}

// ModelStatus is one entry of GET /models: a preset (or cache model) and its state.
type ModelStatus struct {
	// ID is the catalog model id (the preset section name).
	ID      string
	Aliases []string
	Tags    []string
	// Source is preset, models_dir or cache; empty when the router did not say.
	Source string
	// ContextLength is the model's training context (context_length), 0 when absent.
	ContextLength int64
	// Status is status.value.
	Status Status
	// Failed is status.failed: the child exited with a non-zero code and the model is not running.
	// ExitCode is status.exit_code (0 unless Failed).
	Failed   bool
	ExitCode int
	// Args are the child's command line as the router reports it (status.args). They carry no key
	// (the router removes it), but they are never logged anyway.
	Args []string
	// Preset is the INI section text (status.preset), empty when absent.
	Preset string
	// Downloads is status.progress of a downloading model, keyed by URL; nil otherwise.
	Downloads map[string]Download
	// Memory is nil unless the router reports memory.
	Memory *Memory
	// InputModalities and OutputModalities come from the architecture object.
	InputModalities, OutputModalities []string
}

// Loaded reports whether the model is routable: status loaded.
func (m ModelStatus) Loaded() bool { return m.Status == StatusLoaded }

// EffectiveMiB returns the graphics memory the router measured for the loaded model
// (memory.effective devices of kind "device"), for Loaded.EffectiveMiB of the planner. It is nil
// when the router reports no memory, or no positive value.
func (m ModelStatus) EffectiveMiB() *int64 {
	if m.Memory == nil || m.Memory.Effective == nil {
		return nil
	}
	v := m.Memory.Effective.DeviceMiB()
	if v <= 0 {
		return nil
	}
	return &v
}

// ErrMalformed is returned when the router answers with a body this client cannot use: not JSON,
// the wrong shape, a missing id or status, a duplicate id, or a body over the size cap. Unknown
// fields are not malformed: llama-server adds fields between releases.
var ErrMalformed = errors.New("router: malformed response")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// maxIDLen bounds a model id (the router's ids are preset names or HF "repo:tag" strings).
const maxIDLen = 256

// validID reports whether id is usable as a model id on the wire: not empty, valid UTF-8, within
// maxIDLen, no control characters.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLen || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// rawModel is the strict wire shape of one data[] entry. Types are checked by encoding/json (a
// string where a list is expected fails); unknown fields are ignored on purpose.
type rawModel struct {
	ID            *string   `json:"id"`
	Aliases       []string  `json:"aliases"`
	Tags          []string  `json:"tags"`
	Source        string    `json:"source"`
	ContextLength int64     `json:"context_length"`
	Memory        *Memory   `json:"memory"`
	Status        *rawState `json:"status"`
	Architecture  *struct {
		In  []string `json:"input_modalities"`
		Out []string `json:"output_modalities"`
	} `json:"architecture"`
}

type rawState struct {
	Value    *string         `json:"value"`
	Args     []string        `json:"args"`
	Preset   string          `json:"preset"`
	Failed   bool            `json:"failed"`
	ExitCode int             `json:"exit_code"`
	Progress json.RawMessage `json:"progress"`
}

// ParseModels decodes the body of GET /models (and /v1/models, the same handler in router mode).
//
// It is strict about shape and tolerant about content: the body must be an object with a data
// array; every entry needs a non-empty id and a status.value; ids must be unique. Unknown fields,
// unknown status values and extra statuses are accepted. status.progress is read as a download map
// when it is one, and ignored otherwise (a loading progress object).
func ParseModels(body []byte) ([]ModelStatus, error) {
	var top struct {
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&top); err != nil {
		return nil, malformed("models body is not a JSON object: %v", err)
	}
	if err := trailing(dec); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(top.Data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, malformed("models body has no data array")
	}
	var raws []rawModel
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil, malformed("models data: %v", err)
	}
	out := make([]ModelStatus, 0, len(raws))
	seen := make(map[string]struct{}, len(raws))
	for i, r := range raws {
		if r.ID == nil || !validID(*r.ID) {
			return nil, malformed("models data[%d] has no usable id", i)
		}
		if r.Status == nil || r.Status.Value == nil || *r.Status.Value == "" {
			return nil, malformed("model %q has no status.value", *r.ID)
		}
		if _, dup := seen[*r.ID]; dup {
			return nil, malformed("model %q listed twice", *r.ID)
		}
		seen[*r.ID] = struct{}{}
		if err := checkMemory(r.Memory); err != nil {
			return nil, malformed("model %q: %v", *r.ID, err)
		}
		m := ModelStatus{
			ID: *r.ID, Aliases: r.Aliases, Tags: r.Tags, Source: r.Source,
			ContextLength: r.ContextLength, Memory: r.Memory,
			Status: Status(strings.TrimSpace(*r.Status.Value)),
			Failed: r.Status.Failed, ExitCode: r.Status.ExitCode,
			Args: r.Status.Args, Preset: r.Status.Preset,
			Downloads: parseDownloads(r.Status.Progress),
		}
		if r.Architecture != nil {
			m.InputModalities, m.OutputModalities = r.Architecture.In, r.Architecture.Out
		}
		out = append(out, m)
	}
	return out, nil
}

func trailing(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		return malformed("trailing data after the JSON body")
	}
	return nil
}

// checkMemory refuses negative sizes, which no router produces and which would corrupt the planner.
func checkMemory(m *Memory) error {
	if m == nil {
		return nil
	}
	for _, e := range []*MemoryEstimate{m.Min, m.Max, m.Effective} {
		if e == nil {
			continue
		}
		for _, d := range e.Devices {
			if d.Total < 0 || d.Free < 0 || d.Model < 0 || d.Context < 0 || d.Compute < 0 {
				return errors.New("negative memory size")
			}
		}
	}
	return nil
}

// parseDownloads reads status.progress when it is a map of {done,total}; any other shape gives nil.
func parseDownloads(raw json.RawMessage) map[string]Download {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]Download
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return nil
	}
	return m
}

// Props is the part of GET /props?model=<id> the agent uses (doctor checks): the context size and
// the build of a loaded model.
type Props struct {
	// NCtx is default_generation_settings.n_ctx, 0 when absent.
	NCtx int
	// TotalSlots is total_slots.
	TotalSlots int
	// BuildInfo is build_info ("b<number>-<commit>").
	BuildInfo string
	// ModelPath is model_path.
	ModelPath string
	// Sleeping is is_sleeping.
	Sleeping bool
}

// ParseProps decodes the body of GET /props. The body must be a JSON object; every field is optional.
func ParseProps(body []byte) (Props, error) {
	var raw struct {
		Settings *struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots int    `json:"total_slots"`
		BuildInfo  string `json:"build_info"`
		ModelPath  string `json:"model_path"`
		Sleeping   bool   `json:"is_sleeping"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil {
		return Props{}, malformed("props body: %v", err)
	}
	if err := trailing(dec); err != nil {
		return Props{}, err
	}
	p := Props{TotalSlots: raw.TotalSlots, BuildInfo: raw.BuildInfo, ModelPath: raw.ModelPath, Sleeping: raw.Sleeping}
	if raw.Settings != nil {
		p.NCtx = raw.Settings.NCtx
	}
	return p, nil
}
