package ensure

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// ActionStep and ActionRun are the audit actions of this package (agent.md section 4.11): one line per
// step that ran, and one per pass.
const (
	ActionStep = "ensure.step"
	ActionRun  = "ensure.run"
)

// Executor runs one allowlisted step. It holds no state: the Service decides which steps run, in which
// order and whether the caller confirmed them.
type Executor struct {
	Installer Installer
	// Kit is asked again right before a step runs: a step runs only from a kit item that verifies now, not
	// from the answer the plan was made with (a sync may have changed the kit in between).
	Kit   Kit
	Audit Auditor
	Clock Clock
	Log   *slog.Logger
	// WhisperWant is the whisper build this computer would use (node.yaml now); nil means any.
	WhisperWant func() WhisperWant
}

// StepResult is what one step did.
type StepResult struct {
	Step Step
	// Err is nil for a step that was done (or that found nothing to do).
	Err error
}

// Run performs step. The step is checked against the allowlist, checked against the kit again, and only
// then run. Whatever happens it is audited. The returned step carries the final status (done or failed),
// the doctor code and the plain sentence of a failure.
func (e *Executor) Run(ctx context.Context, step Step, actor audit.Actor, runID string) StepResult {
	started := e.Clock.Now().UTC()
	step.StartedAt = &started
	step.Status = StatusRunning
	err := e.run(ctx, &step)
	finished := e.Clock.Now().UTC()
	step.FinishedAt = &finished
	switch {
	case err == nil:
		step.Status, step.Code, step.Reason, step.Error = StatusDone, "", "", ""
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		step.Status, step.Error = StatusCancelled, "cancelled"
		err = context.Canceled
	default:
		step.Status = StatusFailed
		step.Code = codeOf(err)
		step.Error = sentence(err)
	}
	e.record(step, actor, runID, err)
	return StepResult{Step: step, Err: err}
}

func (e *Executor) run(ctx context.Context, step *Step) error {
	if err := Allowed(*step); err != nil {
		return err
	}
	switch step.Kind {
	case KindTool:
		src, version, err := e.Kit.Tool(ctx, step.Target)
		if err != nil {
			return err
		}
		step.Source, step.Version = src.KitDir, version
		out, err := e.Installer.InstallTool(ctx, src.KitDir, step.Target)
		if err != nil {
			return err
		}
		step.Version, step.From = out.Version, out.Previous
	case KindPackages:
		src, have, err := e.Kit.Packages(ctx)
		if err != nil {
			return err
		}
		var names []string
		for _, n := range step.Packages {
			if have[n] {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil // the kit no longer carries them: nothing to install is not a failure
		}
		step.Source, step.Packages = src.KitDir, names
		out, err := e.Installer.InstallPackages(ctx, src.KitDir, names)
		if err != nil {
			return err
		}
		step.Packages = out.Installed
	case KindWhisperRestore:
		want := WhisperWant{}
		if e.WhisperWant != nil {
			want = e.WhisperWant()
		}
		file, err := e.Kit.WhisperPrebuilt(ctx, want)
		if err != nil {
			return err
		}
		step.File = file
		return e.Installer.RestoreWhisper(ctx, file)
	case KindWhisperBuild:
		return e.Installer.BuildWhisper(ctx)
	case KindRestart:
		return e.Installer.RestartService(ctx, step.Target)
	}
	return nil
}

func (e *Executor) record(step Step, actor audit.Actor, runID string, err error) {
	if e.Audit == nil {
		return
	}
	result := audit.ResultOK
	if err != nil {
		result = audit.ResultFail
	}
	target := map[string]string{"step": step.ID, "run_id": runID}
	if step.Item != "" {
		target["item"] = step.Item
	}
	if step.Version != "" {
		target["version"] = step.Version
	}
	if step.From != "" {
		target["from"] = step.From
	}
	if len(step.Packages) > 0 {
		target["packages"] = strings.Join(step.Packages, ",")
	}
	ev := audit.Event{Action: ActionStep, Result: result, Code: step.Code, Actor: actor, Target: target}
	if err := e.Audit.Write(ev); err != nil && e.Log != nil {
		e.Log.Warn("audit write failed", "action", ActionStep, "error", err.Error())
	}
}

// codeOf is the doctor code of a failure: the installer's own when it has one (LAI-KIT-001 for a damaged
// file, LAI-KIT-044 for a failed signature, LAI-SVC-003 and LAI-SVC-020 for a tool), else LAI-KIT-047.
func codeOf(err error) string {
	var c interface{ Code() string }
	switch {
	case errors.Is(err, ErrKitDamaged):
		return doctor.KIT001
	case errors.Is(err, ErrKitMissing):
		return doctor.KIT040
	case errors.As(err, &c):
		return c.Code()
	}
	return doctor.KIT047
}

// sentence is the first line of an error, short enough for a card.
func sentence(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
