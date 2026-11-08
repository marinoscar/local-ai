// Package services supervises the Local AI systemd units (agent.md section 4.3): the Manager
// interface with a D-Bus implementation (the default) and a systemctl fallback, the strict unit
// allowlist, state subscription, health beyond systemd, and the journald tail behind GET /logs.
//
// Everything outside the process (the system bus, systemctl, journalctl, the units' HTTP ports) sits
// behind an interface with a fake in internal/services/fake or internal/execx/fake, so the package
// is tested without systemd and never touches a real unit. Only the Manager returned by NewAuto
// talks to the real system bus; only execx runs programs.
package services

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The units the agent knows (overview.md section 3.5). The allowlist of allowlist.go says which verbs
// each one accepts.
const (
	UnitLlama            = "localai-llama.service"
	UnitKiwix            = "localai-kiwix.service"
	UnitWhisper          = "localai-whisper.service"
	UnitSync             = "localai-sync.service"
	UnitSyncTimer        = "localai-sync.timer"
	UnitDrill            = "localai-drill.service"
	UnitDrillTimer       = "localai-drill.timer"
	UnitAgent            = "localai-agent.service"
	UnitAgentRollback    = "localai-agent-rollback.service"
	UnitAgentRollbackTmr = "localai-agent-rollback.timer"
	UnitLlamaQwen        = "llama-qwen.service"
	UnitLlamaCoderNext   = "llama-coder-next.service"
	UnitTailscaled       = "tailscaled.service"
)

// Timeouts of the Manager (agent.md section 4.3.2). A Manager takes them as options so tests need no
// real waiting.
const (
	// JobTimeout bounds start, stop and restart: the job must finish within it.
	JobTimeout = 60 * time.Second
	// CallTimeout bounds every other call.
	CallTimeout = 10 * time.Second
)

// Active states of a unit (systemd ActiveState).
const (
	ActiveActive       = "active"
	ActiveInactive     = "inactive"
	ActiveFailed       = "failed"
	ActiveActivating   = "activating"
	ActiveDeactivating = "deactivating"
	ActiveReloading    = "reloading"
)

// UnitState is the part of a unit's state the agent reads (agent.md section 4.3.1).
type UnitState struct {
	// Unit is the full unit name, "localai-llama.service".
	Unit string
	// ActiveState is systemd's ActiveState: active, inactive, failed, activating, deactivating, ...
	ActiveState string
	// SubState is systemd's SubState: running, dead, exited, failed, auto-restart, ...
	SubState string
	// MainPID is the main process, 0 when none (and always 0 for a timer).
	MainPID int
	// NRestarts counts the automatic restarts (Restart=) since the last manual start; 0 on a systemd
	// that has no such property (older than 235).
	NRestarts int
	// ExecMainStatus is the exit code of the main process (the signal number when it was killed).
	ExecMainStatus int
	// ExecMainExitTimestamp is when the main process last exited; zero when it never did.
	ExecMainExitTimestamp time.Time
	// ActiveEnterTimestamp is when the unit last became active; zero when it never did.
	ActiveEnterTimestamp time.Time
	// UnitFileState is enabled, disabled, masked, static, ... and "" for a unit with no file.
	UnitFileState string
}

// Same reports whether two states are identical (times compared as instants).
func (s UnitState) Same(o UnitState) bool {
	return s.Unit == o.Unit && s.ActiveState == o.ActiveState && s.SubState == o.SubState &&
		s.MainPID == o.MainPID && s.NRestarts == o.NRestarts && s.ExecMainStatus == o.ExecMainStatus &&
		s.UnitFileState == o.UnitFileState &&
		s.ExecMainExitTimestamp.Equal(o.ExecMainExitTimestamp) && s.ActiveEnterTimestamp.Equal(o.ActiveEnterTimestamp)
}

// Active reports whether the unit is active.
func (s UnitState) Active() bool { return s.ActiveState == ActiveActive }

// Enabled reports whether the unit starts at boot (UnitFileState enabled, or enabled-runtime).
func (s UnitState) Enabled() bool {
	return s.UnitFileState == "enabled" || s.UnitFileState == "enabled-runtime"
}

// Since returns when the unit became active, or the zero time when it is not running: a stopped
// unit keeps its old ActiveEnterTimestamp in systemd, which would only mislead.
func (s UnitState) Since() time.Time {
	switch s.ActiveState {
	case ActiveActive, ActiveActivating, ActiveReloading:
		return s.ActiveEnterTimestamp
	}
	return time.Time{}
}

// Manager is the systemd seam (agent.md section 4.3.1). Implementations: the D-Bus one, the systemctl
// one and the failover Manager of NewAuto; the allowlist of NewGuard wraps any of them.
type Manager interface {
	// State reads the unit's state. A unit with no file is inactive/dead, not an error.
	State(ctx context.Context, unit string) (UnitState, error)
	// Start starts the unit (mode "replace") and waits for the job to finish.
	Start(ctx context.Context, unit string) error
	// Stop stops the unit and waits for the job to finish.
	Stop(ctx context.Context, unit string) error
	// Restart restarts the unit and waits for the job to finish.
	Restart(ctx context.Context, unit string) error
	// Enable makes the unit start at boot.
	Enable(ctx context.Context, unit string) error
	// Disable removes the unit from boot.
	Disable(ctx context.Context, unit string) error
	// Mask links the unit to /dev/null so nothing can start it.
	Mask(ctx context.Context, unit string) error
	// Unmask undoes Mask.
	Unmask(ctx context.Context, unit string) error
	// DaemonReload makes systemd re-read the unit files.
	DaemonReload(ctx context.Context) error
	// Subscribe reports the state of the units now and again at every change, until ctx ends (the
	// channel is then closed). The channel is buffered; a reader that stops reading blocks the
	// subscription, not the Manager.
	Subscribe(ctx context.Context, units []string) (<-chan UnitState, error)
}

// Verb names a Manager method for the allowlist, the logs and the errors.
type Verb string

// The verbs.
const (
	VerbState        Verb = "state"
	VerbStart        Verb = "start"
	VerbStop         Verb = "stop"
	VerbRestart      Verb = "restart"
	VerbEnable       Verb = "enable"
	VerbDisable      Verb = "disable"
	VerbMask         Verb = "mask"
	VerbUnmask       Verb = "unmask"
	VerbDaemonReload Verb = "daemon-reload"
	VerbSubscribe    Verb = "subscribe"
	VerbLogs         Verb = "logs"
)

// Errors of the Manager. Callers test them with errors.Is.
var (
	// ErrTimeout: a call or a job did not finish in time, or systemd itself timed the job out.
	ErrTimeout = errors.New("services: timed out")
	// ErrBusLost: the D-Bus connection dropped during a call.
	ErrBusLost = errors.New("services: D-Bus connection lost")
)

// JobError is a systemd job that finished with a result other than "done" (failed, canceled,
// timeout, dependency, skipped).
type JobError struct {
	Unit   string
	Verb   Verb
	Result string
}

func (e *JobError) Error() string {
	return fmt.Sprintf("services: %s %s: job result %q", e.Verb, e.Unit, e.Result)
}

// Is makes a job that systemd timed out match ErrTimeout.
func (e *JobError) Is(target error) bool { return target == ErrTimeout && e.Result == "timeout" }

// CommandError is a systemctl run that exited non-zero.
type CommandError struct {
	Unit     string
	Verb     Verb
	ExitCode int
	// Stderr is the first bytes of systemctl's error text, scrubbed.
	Stderr string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("services: systemctl %s %s: exit %d: %s", e.Verb, e.Unit, e.ExitCode, e.Stderr)
}

// Service is one of the three units the API controls by short name.
type Service struct {
	// Name is the short name of the API: llama, kiwix or whisper.
	Name string
	// Unit is the systemd unit.
	Unit string
}

// Controlled lists the services of GET /services and POST /services/{name}/{action}, in the order of
// the status snapshot.
var Controlled = []Service{
	{Name: "llama", Unit: UnitLlama},
	{Name: "kiwix", Unit: UnitKiwix},
	{Name: "whisper", Unit: UnitWhisper},
}

// ByName returns the controlled service with the short name.
func ByName(name string) (Service, bool) {
	for _, s := range Controlled {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// ByUnit returns the controlled service that runs the unit.
func ByUnit(unit string) (Service, bool) {
	for _, s := range Controlled {
		if s.Unit == unit {
			return s, true
		}
	}
	return Service{}, false
}

// logUnits maps the short names of GET /logs to units (agent.md section 3.8.23).
var logUnits = map[string]string{
	"agent":   UnitAgent,
	"llama":   UnitLlama,
	"kiwix":   UnitKiwix,
	"whisper": UnitWhisper,
	"sync":    UnitSync,
	"drill":   UnitDrill,
}

// LogUnit returns the unit behind a short log name, and whether the name is known.
func LogUnit(short string) (string, bool) {
	u, ok := logUnits[short]
	return u, ok
}
