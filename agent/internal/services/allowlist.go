package services

import (
	"context"
	"fmt"
)

// CodeViolation is the doctor code of an allowlist violation: a bug in the agent, never a user error
// (agent.md section 4.3.1).
const CodeViolation = "LAI-AGT-099"

// allowed lists the verbs each unit accepts (agent.md section 4.3.1). Anything else is a programming
// error. DaemonReload names no unit and is always allowed.
var allowed = map[string][]Verb{
	UnitLlama:   {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},
	UnitKiwix:   {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},
	UnitWhisper: {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},

	UnitSync:       {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},
	UnitSyncTimer:  {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},
	UnitDrill:      {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},
	UnitDrillTimer: {VerbState, VerbSubscribe, VerbStart, VerbStop, VerbRestart, VerbEnable, VerbDisable},

	// The agent restarts itself after a self-update and never does anything else to its own unit.
	UnitAgent: {VerbRestart},
	// The transient rollback pair is only ever stopped (the update succeeded).
	UnitAgentRollbackTmr: {VerbStop},
	UnitAgentRollback:    {VerbStop},
	// The old hand-made llama unit: the migration and its rollback (llama-router.md section 4.9).
	UnitLlamaQwen: {VerbState, VerbStart, VerbStop, VerbEnable, VerbDisable, VerbMask, VerbUnmask},
	// Not ours: read only.
	UnitTailscaled: {VerbState},
}

// Allowed reports whether verb is permitted on unit.
func Allowed(unit string, verb Verb) bool {
	if verb == VerbDaemonReload {
		return true
	}
	for _, v := range allowed[unit] {
		if v == verb {
			return true
		}
	}
	return false
}

// ViolationError is an allowlist violation: a unit the agent does not manage, or a verb the unit does
// not accept. It carries LAI-AGT-099; the API answers it as an internal error.
type ViolationError struct {
	Unit string
	Verb Verb
}

// Code returns the doctor code of the violation.
func (e *ViolationError) Code() string { return CodeViolation }

func (e *ViolationError) Error() string {
	if _, known := allowed[e.Unit]; !known {
		return fmt.Sprintf("%s: unit %q is not on the service allowlist (verb %s)", CodeViolation, e.Unit, e.Verb)
	}
	return fmt.Sprintf("%s: verb %s is not allowed on unit %q", CodeViolation, e.Verb, e.Unit)
}

// GuardOption configures NewGuard.
type GuardOption func(*guard)

// PanicOnViolation makes the guard panic on a violation instead of returning a *ViolationError. Tests
// use it so that a bug cannot hide behind an error that something swallows; production does not.
func PanicOnViolation() GuardOption { return func(g *guard) { g.panics = true } }

// NewGuard wraps inner so that it only ever sees allowed (unit, verb) pairs (agent.md section 4.3.1).
// It is the one enforcement point for every implementation.
func NewGuard(inner Manager, opts ...GuardOption) Manager {
	g := &guard{inner: inner}
	for _, o := range opts {
		o(g)
	}
	return g
}

type guard struct {
	inner  Manager
	panics bool
}

func (g *guard) check(unit string, verb Verb) error {
	if Allowed(unit, verb) {
		return nil
	}
	err := &ViolationError{Unit: unit, Verb: verb}
	if g.panics {
		panic(err.Error())
	}
	return err
}

func (g *guard) State(ctx context.Context, unit string) (UnitState, error) {
	if err := g.check(unit, VerbState); err != nil {
		return UnitState{}, err
	}
	return g.inner.State(ctx, unit)
}

func (g *guard) Start(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbStart); err != nil {
		return err
	}
	return g.inner.Start(ctx, unit)
}

func (g *guard) Stop(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbStop); err != nil {
		return err
	}
	return g.inner.Stop(ctx, unit)
}

func (g *guard) Restart(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbRestart); err != nil {
		return err
	}
	return g.inner.Restart(ctx, unit)
}

func (g *guard) Enable(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbEnable); err != nil {
		return err
	}
	return g.inner.Enable(ctx, unit)
}

func (g *guard) Disable(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbDisable); err != nil {
		return err
	}
	return g.inner.Disable(ctx, unit)
}

func (g *guard) Mask(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbMask); err != nil {
		return err
	}
	return g.inner.Mask(ctx, unit)
}

func (g *guard) Unmask(ctx context.Context, unit string) error {
	if err := g.check(unit, VerbUnmask); err != nil {
		return err
	}
	return g.inner.Unmask(ctx, unit)
}

func (g *guard) DaemonReload(ctx context.Context) error { return g.inner.DaemonReload(ctx) }

func (g *guard) Subscribe(ctx context.Context, units []string) (<-chan UnitState, error) {
	for _, u := range units {
		if err := g.check(u, VerbSubscribe); err != nil {
			return nil, err
		}
	}
	return g.inner.Subscribe(ctx, units)
}
