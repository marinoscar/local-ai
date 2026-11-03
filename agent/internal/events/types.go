package events

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Event types (agent.md section 3.10). Subsystems publish with these constants and with the
// payload struct registered for each of them below; Publish refuses any other combination, so a
// publisher cannot invent a shape.
const (
	TypeModeChanged         = "mode.changed"
	TypeModeStep            = "mode.step"
	TypeModelLoading        = "model.loading"
	TypeModelLoaded         = "model.loaded"
	TypeModelUnloaded       = "model.unloaded"
	TypeModelFailed         = "model.failed"
	TypeModelDefaultChanged = "model.default_changed"
	TypeSyncProgress        = "sync.progress"
	TypeSyncItemDone        = "sync.item_done"
	TypeSyncDone            = "sync.done"
	TypeServiceState        = "service.state"
	TypeDriveMounted        = "drive.mounted"
	TypeDriveUnmounted      = "drive.unmounted"
	TypeAlertRaised         = "alert.raised"
	TypeAlertCleared        = "alert.cleared"
	TypeCloudState          = "cloud.state"
	TypeHardwarePower       = "hardware.power"
	TypeKeyRotated          = "key.rotated"
	TypeUpdateState         = "update.state"
	TypeDrillStarted        = "drill.started"
	TypeDrillCheckDone      = "drill.check_done"
	TypeDrillDone           = "drill.done"

	// TypeResync tells a client its picture may be stale: re-read /status. The bus owns it (Resync
	// and the replay rules of Subscribe); Publish refuses it.
	TypeResync = "resync"
	// TypeShutdown is the last frame of a stream when the server stops. It is written by the SSE
	// handler, never stored in the ring; Publish refuses it.
	TypeShutdown = "shutdown"
)

// Reasons of a resync event.
const (
	// ResyncGap: the client's Last-Event-ID is older than the ring.
	ResyncGap = "gap"
	// ResyncRestart: the agent restarted (ids start again at 1).
	ResyncRestart = "restart"
)

// Reasons of model.unloaded.
const (
	UnloadRequest       = "request"
	UnloadEvicted       = "evicted"
	UnloadRollback      = "rollback"
	UnloadCrash         = "crash"
	UnloadPresetChanged = "preset_changed"
	UnloadDriveRemoved  = "drive_removed"
)

// Warning is one entry of mode.changed.warnings.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ModeChanged is the data of mode.changed.
type ModeChanged struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	OpID     string    `json:"op_id"`
	Warnings []Warning `json:"warnings"`
}

// ModeStep is the data of mode.step. State is started, done or failed; Code is set on failed.
type ModeStep struct {
	Target string `json:"target"`
	Step   string `json:"step"`
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
}

// ModelLoading is the data of model.loading. Progress is the router's progress value, present only
// on the progress updates sent while the load runs (at most one per second).
type ModelLoading struct {
	Model     string   `json:"model"`
	Unloading []string `json:"unloading"`
	OpID      string   `json:"op_id"`
	Progress  *float64 `json:"progress,omitempty"`
}

// ModelLoaded is the data of model.loaded.
type ModelLoaded struct {
	Model  string `json:"model"`
	LoadMS int64  `json:"load_ms"`
}

// ModelUnloaded is the data of model.unloaded. Reason is one of the Unload* constants.
type ModelUnloaded struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// ModelFailed is the data of model.failed.
type ModelFailed struct {
	Model      string   `json:"model"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	RolledBack bool     `json:"rolled_back"`
	Restored   []string `json:"restored"`
}

// ModelDefaultChanged is the data of model.default_changed.
type ModelDefaultChanged struct {
	Model    string `json:"model"`
	Previous string `json:"previous"`
}

// SyncProgress is the data of sync.progress (ark-sync.md section 3.6), at most one per item per second.
type SyncProgress struct {
	RunID      string `json:"run_id"`
	Root       string `json:"root"`
	Item       string `json:"item"`
	State      string `json:"state"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	RateBPS    int64  `json:"rate_bps"`
}

// SyncItemDone is the data of sync.item_done. Result is verified, skipped, failed or quarantined.
type SyncItemDone struct {
	RunID   string `json:"run_id"`
	Root    string `json:"root"`
	Item    string `json:"item"`
	Result  string `json:"result"`
	Version string `json:"version"`
	Reason  string `json:"reason,omitempty"`
	Code    string `json:"code,omitempty"`
}

// SyncDone is the data of sync.done. Totals has the shape ark-sync.md section 3.6 owns; Ready maps a
// root id to its overall READY state.
type SyncDone struct {
	RunID  string            `json:"run_id"`
	State  string            `json:"state"`
	Totals json.RawMessage   `json:"totals"`
	Ready  map[string]string `json:"ready"`
}

// ServiceState is the data of service.state. From and To are the previous and new health states
// (companion-services.md section 4.6); Code is set when unhealthy. Unit is empty for a service
// without a systemd unit (piper, maps) and is sent as null.
type ServiceState struct {
	Name    string `json:"name"`
	Unit    string `json:"unit"`
	From    string `json:"from"`
	To      string `json:"to"`
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Healthy bool   `json:"healthy"`
	Code    string `json:"code,omitempty"`
}

// MarshalJSON writes an empty Unit as null (agent.md section 3.10: the unit of piper and maps is
// null); every other field is encoded as the struct tags say.
func (s ServiceState) MarshalJSON() ([]byte, error) {
	type plain ServiceState
	if s.Unit != "" {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		Unit *string `json:"unit"`
	}{plain: plain(s)})
}

// DriveMounted is the data of drive.mounted.
type DriveMounted struct {
	RootID   string `json:"root_id"`
	Mount    string `json:"mount"`
	State    string `json:"state"`
	ReadOnly bool   `json:"read_only"`
}

// DriveUnmounted is the data of drive.unmounted. SafeToUnplug false means a surprise removal.
type DriveUnmounted struct {
	RootID       string `json:"root_id"`
	SafeToUnplug bool   `json:"safe_to_unplug"`
}

// Alert is the data of alert.raised and alert.cleared.
type Alert struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Fix      string `json:"fix"`
}

// CloudState is the data of cloud.state.
type CloudState struct {
	State     string `json:"state"`
	Previous  string `json:"previous"`
	LastError string `json:"last_error"`
}

// HardwarePower is the data of hardware.power. BatteryPercent is null on a machine without a battery.
type HardwarePower struct {
	Source         string `json:"source"`
	BatteryPercent *int   `json:"battery_percent"`
	Charging       bool   `json:"charging"`
}

// KeyRotated is the data of key.rotated: the fingerprint only, never key material.
type KeyRotated struct {
	Fingerprint string `json:"fingerprint"`
}

// UpdateState is the data of update.state.
type UpdateState struct {
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// DrillStarted is the data of drill.started.
type DrillStarted struct {
	RunID   string `json:"run_id"`
	Trigger string `json:"trigger"`
	Quick   bool   `json:"quick"`
}

// DrillCheckDone is the data of drill.check_done. Result is pass, warn, fail or skip.
type DrillCheckDone struct {
	RunID  string `json:"run_id"`
	ID     string `json:"id"`
	Result string `json:"result"`
	Code   string `json:"code,omitempty"`
}

// DrillDone is the data of drill.done. Trigger is the trigger of the run (absent for a run that
// recovery closed). Notice is the desktop notification text of a finished scheduled run in both
// languages (drill.md section 4.5) and is absent for every other run, so a listener that shows a
// notification only when it is present never shows one for a manual run or an aborted one.
type DrillDone struct {
	RunID         string       `json:"run_id"`
	Overall       string       `json:"overall"`
	SurvivalReady bool         `json:"survival_ready"`
	Trigger       string       `json:"trigger,omitempty"`
	Notice        *DrillNotice `json:"notice,omitempty"`
}

// DrillNotice is the text of the notification the desktop shows after a scheduled drill, in English
// and Spanish. The desktop picks the one of its own language.
type DrillNotice struct {
	EN string `json:"en"`
	ES string `json:"es"`
}

// ResyncData is the data of resync. Reason is ResyncGap or ResyncRestart.
type ResyncData struct {
	Reason string `json:"reason"`
}

// payloads is the event table: the one struct type each publishable event type carries.
var payloads = map[string]reflect.Type{
	TypeModeChanged:         reflect.TypeOf(ModeChanged{}),
	TypeModeStep:            reflect.TypeOf(ModeStep{}),
	TypeModelLoading:        reflect.TypeOf(ModelLoading{}),
	TypeModelLoaded:         reflect.TypeOf(ModelLoaded{}),
	TypeModelUnloaded:       reflect.TypeOf(ModelUnloaded{}),
	TypeModelFailed:         reflect.TypeOf(ModelFailed{}),
	TypeModelDefaultChanged: reflect.TypeOf(ModelDefaultChanged{}),
	TypeSyncProgress:        reflect.TypeOf(SyncProgress{}),
	TypeSyncItemDone:        reflect.TypeOf(SyncItemDone{}),
	TypeSyncDone:            reflect.TypeOf(SyncDone{}),
	TypeServiceState:        reflect.TypeOf(ServiceState{}),
	TypeDriveMounted:        reflect.TypeOf(DriveMounted{}),
	TypeDriveUnmounted:      reflect.TypeOf(DriveUnmounted{}),
	TypeAlertRaised:         reflect.TypeOf(Alert{}),
	TypeAlertCleared:        reflect.TypeOf(Alert{}),
	TypeCloudState:          reflect.TypeOf(CloudState{}),
	TypeHardwarePower:       reflect.TypeOf(HardwarePower{}),
	TypeKeyRotated:          reflect.TypeOf(KeyRotated{}),
	TypeUpdateState:         reflect.TypeOf(UpdateState{}),
	TypeDrillStarted:        reflect.TypeOf(DrillStarted{}),
	TypeDrillCheckDone:      reflect.TypeOf(DrillCheckDone{}),
	TypeDrillDone:           reflect.TypeOf(DrillDone{}),
}

// Types returns every publishable event type, sorted. resync and shutdown are not in the list: the
// bus owns them.
func Types() []string {
	out := make([]string, 0, len(payloads))
	for t := range payloads {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// PayloadType returns the Go type that carries the data of typ, and false for a type that is not
// publishable.
func PayloadType(typ string) (reflect.Type, bool) {
	t, ok := payloads[typ]
	return t, ok
}

// checkPayload reports whether data is the registered struct for typ, as a value or a non-nil pointer.
func checkPayload(typ string, data any) error {
	want, ok := payloads[typ]
	if !ok {
		if typ == TypeResync || typ == TypeShutdown {
			return fmt.Errorf("%w: %q", ErrReservedType, typ)
		}
		return fmt.Errorf("%w: %q", ErrUnknownType, typ)
	}
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if !v.IsValid() || v.Type() != want {
		return fmt.Errorf("%w: %s wants %s, got %T", ErrPayloadType, typ, want, data)
	}
	return nil
}
