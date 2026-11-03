package drill

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/marinoscar/local-ai/agent/internal/modes"
)

// MinBatteryPercent is the charge below which a node on battery does not run the drill
// (drill.md section 4.1, LAI-DRL-005).
const MinBatteryPercent = 50

// ModeView is the mode engine's picture; *modes.Engine implements it.
type ModeView interface {
	Info() modes.Info
}

// ArkActivity says whether an ark sync, verify or import run is active; the sync engine implements
// it (ark-sync.md).
type ArkActivity interface {
	RunActive() bool
}

// LlamaActivity says whether llama is busy: any slot processing (the router's /slots of each loaded
// model) or a chat request in the last 10 minutes. The wiring (AGENT-20) builds it from the router
// client and the proxy's last chat time.
type LlamaActivity interface {
	Busy(ctx context.Context) (bool, error)
}

// BatteryStatus is the charge of a node with a battery.
type BatteryStatus struct {
	Percent   int
	OnBattery bool
}

// PowerSource reports the battery; ok is false on a node without one (a desktop), which the
// battery rule then never refuses.
type PowerSource interface {
	Battery() (st BatteryStatus, ok bool)
}

// Refusal is a precondition that holds a run back (drill.md section 4.1).
type Refusal struct {
	// Code is LAI-DRL-002, 003, 004 or 005.
	Code string
	// Reason is a plain English phrase for "Drill skipped: <reason>.".
	Reason string
	// Retry is true when the agent tries a scheduled run again later: a sync that is running, or the
	// AI being in use. A survival node, a low battery and another running drill are not retried.
	Retry   bool
	Details map[string]any
}

// RefusedError is what Start returns for a manual run a precondition refuses: 409 with the code.
type RefusedError struct{ Refusal }

func (e *RefusedError) Error() string { return e.Code + ": drill refused: " + e.Reason }

// Preconditions evaluates the table of drill.md section 4.1. A nil probe never refuses.
type Preconditions struct {
	Mode  ModeView
	Ark   ArkActivity
	Llama LlamaActivity
	Power PowerSource
	Log   *slog.Logger
}

// preOpts adjusts one evaluation.
type preOpts struct {
	// skipBusy ignores the llama-busy rule: the 48 hour deferral ran out and the drill runs anyway as
	// --quick.
	skipBusy bool
}

// Evaluate returns the first precondition that fails, in the order of the table (survival, sync,
// llama busy, battery), or nil. The effective mode counts: a node heading to survival is in survival.
func (p Preconditions) Evaluate(ctx context.Context, o preOpts) *Refusal {
	if p.Mode != nil {
		info := p.Mode.Info()
		if info.Mode == modes.Survival || info.Effective == modes.Survival {
			return &Refusal{Code: CodeSurvival, Reason: "the node is in survival mode",
				Details: map[string]any{"reason": "survival_mode"}}
		}
	}
	if p.Ark != nil && p.Ark.RunActive() {
		return &Refusal{Code: CodeSyncActive, Reason: "an ark sync is running", Retry: true,
			Details: map[string]any{"reason": "ark_run_active"}}
	}
	if p.Llama != nil && !o.skipBusy {
		busy, err := p.Llama.Busy(ctx)
		switch {
		case err != nil:
			// A router that cannot answer is not in use; the model checks report it themselves.
			p.log().Warn("llama busy probe failed", "error", err.Error())
		case busy:
			return &Refusal{Code: CodeBusy, Reason: "the AI is in use", Retry: true,
				Details: map[string]any{"reason": "llama_busy"}}
		}
	}
	if p.Power != nil {
		if st, ok := p.Power.Battery(); ok && st.OnBattery && st.Percent < MinBatteryPercent {
			return &Refusal{Code: CodeBattery, Reason: fmt.Sprintf("the battery is below %d %%", MinBatteryPercent),
				Details: map[string]any{"reason": "on_battery", "battery_percent": st.Percent}}
		}
	}
	return nil
}

func (p Preconditions) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default().With("component", "drill")
}

// drillRunning is the refusal of a start while a drill is already running. It is not retried: the
// drill that runs is the drill.
func drillRunning(runID string) *Refusal {
	return &Refusal{Code: CodeSyncActive, Reason: "another drill is running",
		Details: map[string]any{"reason": "drill_running", "run_id": runID}}
}
