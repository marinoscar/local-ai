package checks

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/setup/harden"
)

// Hardening is the laptop power hardening of the installer (installer.md section 4.3 P4,
// agent/internal/setup/harden) as the drill sees it. The daemon implements it over harden.Hardener,
// so the drill reuses the very checks the doctor runs for LAI-INS-040 to 044.
type Hardening interface {
	// Laptop reports whether this computer is a laptop: the installer's rule (a battery, or a chassis
	// type of 8, 9, 10, 14, 31 or 32).
	Laptop() bool
	// State is what the hardening recorded when it ran (hardening.json).
	State() (harden.State, error)
	// Verify runs the hardening's doctor checks and returns what they found; nothing means every
	// setting is in effect. A pending reboot after an install is LAI-INS-070.
	Verify(ctx context.Context) []doctor.Result
}

// PowerDeps are what check 20 needs.
type PowerDeps struct {
	Hardening Hardening
	// Power reports whether the computer runs on its battery; nil, or a computer that reports no
	// battery, means the charger cannot be judged.
	Power drill.PowerSource
}

// hardeningCodes are the codes of the hardening checks, in the order details lists them.
var hardeningCodes = []string{doctor.INS040, doctor.INS041, doctor.INS042, doctor.INS043, doctor.INS044}

// Values of the per-code details of check 20.
const (
	powerOK       = "ok"
	powerProblem  = "problem"
	powerRebootIs = "reboot_required"
)

// Power is check 20, drl.power: on a laptop, the power hardening is still in effect (LAI-INS-040 to
// 044) and the charger is connected. A computer that is not a laptop skips it.
//
//   - pass: every hardening check is ok and the laptop is on AC (or the charger cannot be read);
//   - warn (LAI-DRL-080): any hardening check reports a problem, the hardening was never applied on
//     this laptop, or the laptop runs on its battery. This check never fails: a laptop with the lid
//     closed that sleeps is a problem to fix, not a broken ark.
//
// While a reboot is pending after an install, the logind check cannot be judged yet: details say
// `LAI-INS-040: reboot_required` (and LAI-INS-070 is reported) instead of a problem.
func Power(d PowerDeps) drill.Check {
	return drill.Check{
		ID: "drl.power", Area: "power", Order: 20,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return d.power(ctx) },
	}
}

func (d PowerDeps) power(ctx context.Context) drill.Outcome {
	if d.Hardening == nil || !d.Hardening.Laptop() {
		return drill.Outcome{Result: drill.ResultSkip, Details: map[string]any{"reason": "not_a_laptop"}}
	}
	details := map[string]any{}
	problem := false

	st, err := d.Hardening.State()
	switch {
	case err != nil:
		details["hardening"] = "unreadable"
		problem = true
	case !st.Applied:
		// The doctor checks say nothing on a laptop the hardening did not touch; for the drill that is
		// the problem: this laptop will sleep when the lid closes.
		details["hardening"] = "not_applied"
		problem = true
	default:
		details["hardening"] = "applied"
		found := map[string]bool{}
		for _, r := range d.Hardening.Verify(ctx) {
			found[r.Code] = true
		}
		for _, code := range hardeningCodes {
			switch {
			case code == doctor.INS040 && found[doctor.INS070]:
				details[code] = powerRebootIs
			case found[code]:
				details[code] = powerProblem
				problem = true
			default:
				details[code] = powerOK
			}
		}
		if found[doctor.INS070] {
			details[doctor.INS070] = "pending"
		}
	}

	if d.Power != nil {
		if b, ok := d.Power.Battery(); ok {
			details["ac_online"] = !b.OnBattery
			if b.OnBattery {
				problem = true
			}
		}
	}
	if problem {
		return drill.Outcome{Result: drill.ResultWarn, Code: doctor.DRL080, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

// hardener is Hardening over the installer's harden.Hardener: its own laptop rule, its recorded state
// and its doctor checks.
type hardener struct{ h *harden.Hardener }

// NewHardening returns the Hardening of a harden.Hardener (harden.New(harden.Paths{Root: root},
// harden.Options{}, nil) on a real node).
func NewHardening(h *harden.Hardener) Hardening { return hardener{h} }

func (a hardener) Laptop() bool                               { return harden.IsLaptop(a.h.Paths) }
func (a hardener) State() (harden.State, error)               { return harden.LoadState(a.h.Paths, a.h.Boot) }
func (a hardener) Verify(ctx context.Context) []doctor.Result { return a.h.Verify(ctx) }
