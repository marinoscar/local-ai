package migrate

import (
	"context"
	"os"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Ids of the doctor checks of the migration.
const (
	CheckLegacyService = "llm.legacy_service"
	CheckInterrupted   = "llm.migration"
	CheckLegacyKeyLink = "sec.legacy_key_path"
)

// CheckSources are what the checks read. The agent passes its Manager and its state store; the paths are
// the migration's (DefaultPaths of the agent's root).
type CheckSources struct {
	Systemd interface {
		State(ctx context.Context, unit string) (services.UnitState, error)
	}
	State interface{ Snapshot() state.State }
	Paths Paths
	// Lstat and Readlink default to the os functions; tests replace them.
	Lstat    func(string) (os.FileInfo, error)
	Readlink func(string) (string, error)
}

// Checks returns the three checks of the migration (llama-router.md section 4.9, 8):
//
//	llm.legacy_service   LAI-LLM-016: llama-qwen.service is still enabled or running (also true on a node
//	                     that was rolled back: the old service runs there on purpose, and the doctor says so)
//	llm.migration        LAI-LLM-029: the migration or its rollback was interrupted (the agent is stopped
//	                     while a migration runs, so a recorded migration that is not finished is one that
//	                     was cut short)
//	sec.legacy_key_path  LAI-SEC-015: after a finished migration the old key path is not a link to the
//	                     current key
func Checks(src CheckSources) []doctor.Check {
	lstat, readlink := src.Lstat, src.Readlink
	if lstat == nil {
		lstat = os.Lstat
	}
	if readlink == nil {
		readlink = os.Readlink
	}
	var out []doctor.Check
	if src.Systemd != nil {
		out = append(out, doctor.Check{ID: CheckLegacyService, Area: "LLM", Fast: true, Run: func(ctx context.Context) []doctor.Result {
			var res []doctor.Result
			for _, unit := range LegacyUnitNames {
				st, err := src.Systemd.State(ctx, unit)
				if err != nil {
					continue // a unit the manager cannot read is not a finding of this check
				}
				active := st.ActiveState == services.ActiveActive || st.ActiveState == services.ActiveActivating || st.ActiveState == services.ActiveReloading
				if !active && !st.Enabled() {
					continue
				}
				res = append(res, doctor.Result{Code: CodeOldRunning, Details: map[string]any{
					"unit": unit, "active": st.ActiveState, "enabled": st.Enabled(),
				}})
			}
			return res
		}})
	}
	if src.State != nil {
		out = append(out,
			doctor.Check{ID: CheckInterrupted, Area: "LLM", Fast: true, Run: func(context.Context) []doctor.Result {
				e, ok := src.State.Snapshot().Migration[StateKey]
				if !ok || (e.State != StateInProgress && e.State != StateRollingBack) {
					return nil
				}
				return []doctor.Result{{Code: CodeInterrupted, Details: map[string]any{
					"state": e.State, "step": e.Step, "at": e.At.UTC().Format("2006-01-02T15:04:05Z"),
				}}}
			}},
			doctor.Check{ID: CheckLegacyKeyLink, Area: "SEC", Fast: true, Run: func(context.Context) []doctor.Result {
				e, ok := src.State.Snapshot().Migration[StateKey]
				if !ok || e.State != StateDone {
					return nil
				}
				fi, err := lstat(src.Paths.LegacyKey)
				if err != nil {
					return nil // the old path is gone: nothing reads it
				}
				if fi.Mode()&os.ModeSymlink != 0 {
					if t, err := readlink(src.Paths.LegacyKey); err == nil && strings.TrimSpace(t) == src.Paths.NewKey {
						return nil
					}
				}
				return []doctor.Result{{Code: CodeOldCredPath, Details: map[string]any{"path": src.Paths.LegacyKey}}}
			}},
		)
	}
	return out
}

// RegisterChecks adds Checks(src) to reg (doctor.DefaultRegistry when nil).
func RegisterChecks(reg *doctor.Registry, src CheckSources) error {
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	for _, c := range Checks(src) {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}
