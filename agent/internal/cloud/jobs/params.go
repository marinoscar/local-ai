package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"unicode/utf16"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
)

// The strict decoders of the 14 job types: the params schemas of cloud-app.md section 3.7 (Zod,
// .strict()) written as Go. Each decoder rejects an unknown key (compared exactly), a null, a value
// of the wrong type, a value out of range or off its pattern, and fills the defaults. A rejection is
// a *cloud.DecodeError, which the claim loop turns into failure "LAI-CLD-058: <path>: <problem>".
//
// Nothing in a params object can name a command, a path or a unit file: ids and roots are matched
// against the patterns below, and the only free text (mode.offline's reason) is bounded and never
// leaves the log.

// The patterns of cloud-app.md section 3.7 (state.schema.ts).
var (
	// ModelID is ModelId and ContentId.
	modelIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)
	// rootIDRE is RootId: the internal root or a drive.
	rootIDRE   = regexp.MustCompile(`^(internal|ark-[0-9a-f]{8})$`)
	semverRE   = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	maxRoots   = 9
	maxItems   = 200
	maxReason  = 200
	maxVersion = 64
)

// Defaults and limits of logs.tail.
const (
	defaultLogLines = 200
	maxLogLines     = 500
)

var logUnits = []string{"agent", "llama", "kiwix", "whisper", "sync", "drill"}
var serviceNames = []string{"llama", "kiwix", "whisper"}

func bad(path, format string, a ...any) error {
	return &cloud.DecodeError{Path: path, Problem: fmt.Sprintf(format, a...)}
}

func checkModelID(path, v string) error {
	if !modelIDRE.MatchString(v) {
		return bad(path, "must be a model id (lower case letters, digits, . and -; 3 to 64 characters)")
	}
	return nil
}

// checkRoots validates a list of root ids: 1 to 9 entries, each a RootId, without duplicates.
func checkRoots(path string, v []string) error {
	return checkIDs(path, v, rootIDRE, maxRoots, "a root id (internal or ark-xxxxxxxx)")
}

func checkContent(path string, v []string) error {
	return checkIDs(path, v, modelIDRE, maxItems, "a content id")
}

func checkIDs(path string, v []string, re *regexp.Regexp, max int, what string) error {
	if len(v) < 1 {
		return bad(path, "must have at least 1 entry")
	}
	if len(v) > max {
		return bad(path, "must have at most %d entries", max)
	}
	seen := map[string]bool{}
	for i, s := range v {
		if !re.MatchString(s) {
			return bad(fmt.Sprintf("%s[%d]", path, i), "must be %s", what)
		}
		if seen[s] {
			return bad(fmt.Sprintf("%s[%d]", path, i), "is listed twice")
		}
		seen[s] = true
	}
	return nil
}

func required(path string, present bool) error {
	if !present {
		return bad(path, "is required")
	}
	return nil
}

func oneOf(path, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return bad(path, "must be one of %v", allowed)
}

// ---------------------------------------------------------------------------------------------------
// status.refresh, catalog.refresh, key.rotate: {}

type noParams struct{}

func decodeNone(raw json.RawMessage) (noParams, error) {
	var p struct{}
	if err := cloud.DecodeParams(raw, &p); err != nil {
		return noParams{}, err
	}
	return noParams{}, nil
}

// ---------------------------------------------------------------------------------------------------
// model.*

// ModelLoadParams is {model_id, wait: true, unload_others = true, set_default = false}.
type ModelLoadParams struct {
	ModelID      string
	UnloadOthers bool
	SetDefault   bool
}

func decodeModelLoad(raw json.RawMessage) (ModelLoadParams, error) {
	var w struct {
		ModelID      *string `json:"model_id"`
		Wait         *bool   `json:"wait"`
		UnloadOthers *bool   `json:"unload_others"`
		SetDefault   *bool   `json:"set_default"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ModelLoadParams{}, err
	}
	if err := required("model_id", w.ModelID != nil); err != nil {
		return ModelLoadParams{}, err
	}
	if err := checkModelID("model_id", *w.ModelID); err != nil {
		return ModelLoadParams{}, err
	}
	if w.Wait != nil && !*w.Wait {
		return ModelLoadParams{}, bad("wait", "must be true")
	}
	p := ModelLoadParams{ModelID: *w.ModelID, UnloadOthers: true}
	if w.UnloadOthers != nil {
		p.UnloadOthers = *w.UnloadOthers
	}
	if w.SetDefault != nil {
		p.SetDefault = *w.SetDefault
	}
	return p, nil
}

// ModelIDParams is {model_id} (model.unload, model.set_default).
type ModelIDParams struct{ ModelID string }

func decodeModelID(raw json.RawMessage) (ModelIDParams, error) {
	var w struct {
		ModelID *string `json:"model_id"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ModelIDParams{}, err
	}
	if err := required("model_id", w.ModelID != nil); err != nil {
		return ModelIDParams{}, err
	}
	if err := checkModelID("model_id", *w.ModelID); err != nil {
		return ModelIDParams{}, err
	}
	return ModelIDParams{ModelID: *w.ModelID}, nil
}

// ModelDownloadParams is {model_id, roots?: [RootId] (1..9)}.
type ModelDownloadParams struct {
	ModelID string
	Roots   []string
}

func decodeModelDownload(raw json.RawMessage) (ModelDownloadParams, error) {
	var w struct {
		ModelID *string  `json:"model_id"`
		Roots   []string `json:"roots"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ModelDownloadParams{}, err
	}
	if err := required("model_id", w.ModelID != nil); err != nil {
		return ModelDownloadParams{}, err
	}
	if err := checkModelID("model_id", *w.ModelID); err != nil {
		return ModelDownloadParams{}, err
	}
	if w.Roots != nil {
		if err := checkRoots("roots", w.Roots); err != nil {
			return ModelDownloadParams{}, err
		}
	}
	return ModelDownloadParams{ModelID: *w.ModelID, Roots: w.Roots}, nil
}

// ModelDeleteParams is {model_id, roots: [RootId] (1..9), confirm}; the agent re-checks that confirm
// equals model_id (a typed confirmation in the cloud UI, checked on both sides).
type ModelDeleteParams struct {
	ModelID string
	Roots   []string
}

func decodeModelDelete(raw json.RawMessage) (ModelDeleteParams, error) {
	var w struct {
		ModelID *string  `json:"model_id"`
		Roots   []string `json:"roots"`
		Confirm *string  `json:"confirm"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ModelDeleteParams{}, err
	}
	if err := required("model_id", w.ModelID != nil); err != nil {
		return ModelDeleteParams{}, err
	}
	if err := checkModelID("model_id", *w.ModelID); err != nil {
		return ModelDeleteParams{}, err
	}
	if err := required("roots", w.Roots != nil); err != nil {
		return ModelDeleteParams{}, err
	}
	if err := checkRoots("roots", w.Roots); err != nil {
		return ModelDeleteParams{}, err
	}
	if err := required("confirm", w.Confirm != nil); err != nil {
		return ModelDeleteParams{}, err
	}
	if *w.Confirm != *w.ModelID {
		return ModelDeleteParams{}, bad("confirm", "must equal model_id")
	}
	return ModelDeleteParams{ModelID: *w.ModelID, Roots: w.Roots}, nil
}

// ---------------------------------------------------------------------------------------------------
// ark.*

// ArkSyncParams is {roots?: [RootId], items?: [ContentId] (1..200), dry_run = false}.
type ArkSyncParams struct {
	Roots  []string
	Items  []string
	DryRun bool
}

func decodeArkSync(raw json.RawMessage) (ArkSyncParams, error) {
	var w struct {
		Roots  []string `json:"roots"`
		Items  []string `json:"items"`
		DryRun *bool    `json:"dry_run"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ArkSyncParams{}, err
	}
	if w.Roots != nil {
		if err := checkRoots("roots", w.Roots); err != nil {
			return ArkSyncParams{}, err
		}
	}
	if w.Items != nil {
		if err := checkContent("items", w.Items); err != nil {
			return ArkSyncParams{}, err
		}
	}
	p := ArkSyncParams{Roots: w.Roots, Items: w.Items}
	if w.DryRun != nil {
		p.DryRun = *w.DryRun
	}
	return p, nil
}

// ArkVerifyParams is {roots?, items?}.
type ArkVerifyParams struct {
	Roots []string
	Items []string
}

func decodeArkVerify(raw json.RawMessage) (ArkVerifyParams, error) {
	var w struct {
		Roots []string `json:"roots"`
		Items []string `json:"items"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ArkVerifyParams{}, err
	}
	if w.Roots != nil {
		if err := checkRoots("roots", w.Roots); err != nil {
			return ArkVerifyParams{}, err
		}
	}
	if w.Items != nil {
		if err := checkContent("items", w.Items); err != nil {
			return ArkVerifyParams{}, err
		}
	}
	return ArkVerifyParams{Roots: w.Roots, Items: w.Items}, nil
}

// ---------------------------------------------------------------------------------------------------
// mode.offline, service.restart, logs.tail, agent.update

// ModeOfflineParams is {reason?: string <= 200}.
type ModeOfflineParams struct{ Reason string }

func decodeModeOffline(raw json.RawMessage) (ModeOfflineParams, error) {
	var w struct {
		Reason *string `json:"reason"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ModeOfflineParams{}, err
	}
	if w.Reason == nil {
		return ModeOfflineParams{}, nil
	}
	// Zod counts UTF-16 code units.
	if len(utf16.Encode([]rune(*w.Reason))) > maxReason {
		return ModeOfflineParams{}, bad("reason", "must be at most %d characters", maxReason)
	}
	return ModeOfflineParams{Reason: *w.Reason}, nil
}

// ServiceRestartParams is {service: llama | kiwix | whisper}.
type ServiceRestartParams struct{ Service string }

func decodeServiceRestart(raw json.RawMessage) (ServiceRestartParams, error) {
	var w struct {
		Service *string `json:"service"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return ServiceRestartParams{}, err
	}
	if err := required("service", w.Service != nil); err != nil {
		return ServiceRestartParams{}, err
	}
	if err := oneOf("service", *w.Service, serviceNames); err != nil {
		return ServiceRestartParams{}, err
	}
	return ServiceRestartParams{Service: *w.Service}, nil
}

// LogsTailParams is {unit: agent|llama|kiwix|whisper|sync|drill, lines: 1..500 = 200}.
type LogsTailParams struct {
	Unit  string
	Lines int
}

func decodeLogsTail(raw json.RawMessage) (LogsTailParams, error) {
	var w struct {
		Unit  *string `json:"unit"`
		Lines *int    `json:"lines"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return LogsTailParams{}, err
	}
	if err := required("unit", w.Unit != nil); err != nil {
		return LogsTailParams{}, err
	}
	if err := oneOf("unit", *w.Unit, logUnits); err != nil {
		return LogsTailParams{}, err
	}
	p := LogsTailParams{Unit: *w.Unit, Lines: defaultLogLines}
	if w.Lines != nil {
		if *w.Lines < 1 || *w.Lines > maxLogLines {
			return LogsTailParams{}, bad("lines", "must be between 1 and %d", maxLogLines)
		}
		p.Lines = *w.Lines
	}
	return p, nil
}

// AgentUpdateParams is {version: semver}. The decoder belongs to this package; the executor is
// AGENT-19's (AgentUpdater).
type AgentUpdateParams struct{ Version string }

func decodeAgentUpdate(raw json.RawMessage) (AgentUpdateParams, error) {
	var w struct {
		Version *string `json:"version"`
	}
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return AgentUpdateParams{}, err
	}
	if err := required("version", w.Version != nil); err != nil {
		return AgentUpdateParams{}, err
	}
	if len(*w.Version) > maxVersion || !semverRE.MatchString(*w.Version) {
		return AgentUpdateParams{}, bad("version", "must be a version such as 1.2.3")
	}
	return AgentUpdateParams{Version: *w.Version}, nil
}
