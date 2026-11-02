package services

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Action is what POST /services/{name}/{action} does to a service.
type Action string

// The three actions of the API.
const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

// Valid reports whether a is one of the three actions.
func (a Action) Valid() bool { return a == ActionStart || a == ActionStop || a == ActionRestart }

// Defaults of the Controller (agent.md section 3.8.22).
const (
	// DefaultWaitFor bounds a whole action: the systemd job and the wait for the final state.
	DefaultWaitFor = 60 * time.Second
	// DefaultStatePoll is how often the final state is read while waiting.
	DefaultStatePoll = 250 * time.Millisecond
)

// Reasons of an ActionError.
const (
	// ReasonJobFailed: systemd or systemctl reported that the job failed.
	ReasonJobFailed = "job_failed"
	// ReasonTimeout: the job or the wait for the state took longer than the limit.
	ReasonTimeout = "timeout"
	// ReasonWrongState: the unit settled in a state other than the one asked for (for example failed).
	ReasonWrongState = "wrong_state"
	// ReasonUnavailable: systemd could not be asked (no bus, no systemctl).
	ReasonUnavailable = "unavailable"
)

// ActionError is an action that did not leave the service in the state asked for.
type ActionError struct {
	Service Service
	Action  Action
	// Reason is one of the Reason* constants.
	Reason string
	// State is the unit's last known state.
	State UnitState
	// Err is the underlying error, if any.
	Err error
}

func (e *ActionError) Error() string {
	msg := fmt.Sprintf("services: %s %s: %s (unit is %s/%s)", e.Action, e.Service.Name, e.Reason, e.State.ActiveState, e.State.SubState)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *ActionError) Unwrap() error { return e.Err }

// Controller runs the three API actions through a Manager and waits for the unit to reach the state
// the action asks for: active after a start or restart, inactive (or failed) after a stop.
type Controller struct {
	Manager Manager
	// WaitFor bounds a whole action; zero means DefaultWaitFor.
	WaitFor time.Duration
	// Poll is how often the state is read while waiting; zero means DefaultStatePoll.
	Poll time.Duration
}

// Do performs the action on the service and returns the unit's final state. The whole call, including
// the systemd job, is bounded by WaitFor. A failure is an *ActionError; a cancelled ctx returns its error.
func (c *Controller) Do(ctx context.Context, svc Service, action Action) (UnitState, error) {
	waitFor, poll := c.WaitFor, c.Poll
	if waitFor <= 0 {
		waitFor = DefaultWaitFor
	}
	if poll <= 0 {
		poll = DefaultStatePoll
	}
	ctx, cancel := context.WithTimeout(ctx, waitFor)
	defer cancel()

	var err error
	switch action {
	case ActionStart:
		err = c.Manager.Start(ctx, svc.Unit)
	case ActionStop:
		err = c.Manager.Stop(ctx, svc.Unit)
	case ActionRestart:
		err = c.Manager.Restart(ctx, svc.Unit)
	default:
		return UnitState{}, fmt.Errorf("services: unknown action %q", action)
	}
	if err != nil {
		return stateOf(c.classify(ctx, svc, action, err))
	}

	for {
		st, serr := c.Manager.State(ctx, svc.Unit)
		if serr == nil {
			if settled, wrong := reached(action, st); settled {
				return st, nil
			} else if wrong {
				return st, &ActionError{Service: svc, Action: action, Reason: ReasonWrongState, State: st}
			}
		} else if ctx.Err() == nil && errors.Is(serr, ErrBusLost) {
			return st, &ActionError{Service: svc, Action: action, Reason: ReasonUnavailable, State: st, Err: serr}
		}
		select {
		case <-ctx.Done():
			return stateOf(c.classify(ctx, svc, action, ctx.Err()))
		case <-time.After(poll):
		}
	}
}

// stateOf returns an ActionError's state along with the error, for the (state, error) result of Do.
func stateOf(err error) (UnitState, error) {
	var ae *ActionError
	if errors.As(err, &ae) {
		return ae.State, err
	}
	return UnitState{}, err
}

// last reads the unit's state for an error report; a failure to read it leaves the zero state.
func (c *Controller) last(svc Service) UnitState {
	ctx, cancel := context.WithTimeout(context.Background(), CallTimeout)
	defer cancel()
	st, _ := c.Manager.State(ctx, svc.Unit)
	return st
}

func (c *Controller) classify(ctx context.Context, svc Service, action Action, err error) error {
	st := c.last(svc)
	e := &ActionError{Service: svc, Action: action, State: st, Err: err}
	var je *JobError
	var ce *CommandError
	switch {
	case errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled:
		return err
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		e.Reason = ReasonTimeout
	case errors.As(err, &je), errors.As(err, &ce):
		e.Reason = ReasonJobFailed
	default:
		e.Reason = ReasonUnavailable
	}
	return e
}

// reached reports whether the unit is in the state the action asked for (settled), or has landed in a
// state it will not leave by itself (wrong). A unit that is still activating, deactivating or waiting
// for an automatic restart is neither: the caller keeps waiting.
func reached(a Action, st UnitState) (settled, wrong bool) {
	switch a {
	case ActionStart, ActionRestart:
		switch st.ActiveState {
		case ActiveActive:
			return true, false
		case ActiveFailed:
			return false, true
		case ActiveInactive:
			// A oneshot that finished, or a stop that raced the start: it will not become active.
			return false, true
		}
	case ActionStop:
		switch st.ActiveState {
		case ActiveInactive, ActiveFailed:
			// failed after a stop means the process exited non-zero on SIGTERM: it is not running.
			return true, false
		}
	}
	return false, false
}
