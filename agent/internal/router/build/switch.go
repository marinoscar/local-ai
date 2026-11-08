package build

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Model statuses of GET /models (agent.md section 3.8.7) the post-switch check cares about.
const (
	StatusLoaded  = "loaded"
	StatusLoading = "loading"
	StatusFailed  = "failed"
)

var (
	// ErrAgentUnavailable is what Agent.RestartLlama returns (wrapped is fine) when the agent stopped
	// answering: the switch then restarts llama with systemctl.
	ErrAgentUnavailable = errors.New("build: the agent is not answering")
	// ErrNoAgent is what Probe.LoadModel returns when there is no agent to load a model through
	// (the agent is down): the router loads its default model by itself at startup, so the check
	// keeps waiting for it.
	ErrNoAgent = errors.New("build: there is no agent to load a model through")
)

// BusyError says that the agent is running another model operation (409 LAI-AGT-022); OpID is the
// running operation's id when the agent said it. Agent.RestartLlama returns it for that answer.
type BusyError struct{ OpID string }

// Error implements error.
func (e *BusyError) Error() string {
	if e.OpID == "" {
		return "build: the agent is busy with another model operation"
	}
	return "build: the agent is busy with another model operation (" + e.OpID + ")"
}

// Agent is the part of the agent API the switch uses. AICTL-09 implements it over the agent's
// HTTP API (the unix socket, as root).
type Agent interface {
	// Up reports whether the agent answers.
	Up(ctx context.Context) bool
	// RestartLlama is POST /api/v1/services/llama/restart: the agent takes modelOpsMu, restarts
	// localai-llama, waits for /health (60 s) and restores the models. A busy agent is a *BusyError;
	// an agent that stopped answering is ErrAgentUnavailable.
	RestartLlama(ctx context.Context) error
}

// ServiceRestarter is the part of the agent API a recipe other than llama's uses to restart its
// service (POST /services/<name>/restart): whisper.cpp restarts the speech service through it.
// AICTL-09 and SVC-05 implement it over the agent's HTTP API.
type ServiceRestarter interface {
	// Up reports whether the agent answers.
	Up(ctx context.Context) bool
	// RestartService restarts the named service ("whisper") and waits for the unit. A busy agent
	// is a *BusyError; one that stopped answering is ErrAgentUnavailable.
	RestartService(ctx context.Context, service string) error
}

// ErrServiceNotConfigured is what Options.ServiceCheck returns when the restarted service has no
// configuration to start from yet (whisper without /etc/localai/whisper.env): there is nothing to
// check, which is a warning and never a reason to undo the switch.
var ErrServiceNotConfigured = errors.New("build: the service is not configured yet")

// Probe is what the post-switch check asks of the running router (llama-router.md section 4.10.3
// step 6). AICTL-09 implements it over the router client and the agent API; every call must be
// bounded by its context.
type Probe interface {
	// Health is the router's GET /health: nil when it is up and ready.
	Health(ctx context.Context) error
	// DefaultModel is the effective default model id; "" when there is none.
	DefaultModel(ctx context.Context) (string, error)
	// ModelStatus is the model's status in GET /models ("loaded", "loading", "unloaded", "failed", ...).
	ModelStatus(ctx context.Context, id string) (string, error)
	// LoadModel loads the model through the agent's orchestrator (POST /models/{id}/load); it
	// returns ErrNoAgent when there is no agent.
	LoadModel(ctx context.Context, id string) error
	// Complete asks the loaded model for a completion of at most maxTokens tokens and returns nil
	// when it succeeded.
	Complete(ctx context.Context, id string, maxTokens int) error
}

// StateRecorder records the installed build in state.json after a restart that did not go through
// the agent (the agent records it itself on its own restart path, and is the only writer of the
// file while it runs). StoreRecorder is the implementation over a state store.
type StateRecorder interface {
	RecordBuild(buildID string) error
}

// StateMutator is the part of *state.Store StoreRecorder uses.
type StateMutator interface {
	Mutate(fn func(*state.State) error) error
}

// StoreRecorder records state.llama.build and previous_build the way the router's restart does: the
// build it replaces becomes previous_build, and the same id changes nothing.
type StoreRecorder struct{ Store StateMutator }

// RecordBuild implements StateRecorder.
func (r StoreRecorder) RecordBuild(id string) error {
	return r.Store.Mutate(func(s *state.State) error {
		if s.Llama.Build == id {
			return nil
		}
		if s.Llama.Build != "" {
			prev := s.Llama.Build
			s.Llama.PreviousBuild = &prev
		}
		s.Llama.Build = id
		return nil
	})
}

// activation is what the switch needs from the build that was just placed.
type activation struct {
	req       Request
	plan      Plan
	curTarget string // what current pointed at before ("" when there was no link)
	oldDir    string // the move-aside name of a build with the same id ("" when none)
	info      BuildInfo
}

type activationResult struct {
	previous     string
	defaultModel string
	warnings     []string
}

// activate is llama-router.md section 4.10.3 step 6: point current at the new build, restart llama,
// check that the new build serves, and put current back when anything of that fails.
func (m *Manager) activate(ctx context.Context, a activation, progress func(Stage, string)) (activationResult, *Error) {
	id := a.plan.BuildID
	newTarget := path.Join(buildsDir, id)
	prevTarget := a.curTarget
	if a.oldDir != "" && buildName(a.curTarget) == id {
		// The build that current points at was moved aside to make room for the new one.
		prevTarget = path.Join(buildsDir, filepath.Base(a.oldDir))
	}
	prevName := buildName(prevTarget)
	target := map[string]string{"build": id, "previous": prevName, "backend": a.plan.Backend}
	res := activationResult{previous: prevName}

	fail := func(code string, stage Stage, cause string, err error, rolledBack bool, kv ...any) *Error {
		msg := "The new llama.cpp build did not work; the previous build is still in use."
		if prevTarget == "" {
			msg = "The new llama.cpp build did not work, and there is no previous build to go back to."
		} else if !rolledBack {
			msg = "The new llama.cpp build did not work, and the previous build could not be put back."
		}
		e := newErr(code, stage, msg, err, append([]any{
			"cause", cause, "build_id", id, "commit", a.plan.Commit, "backend", a.plan.Backend,
			"previous_build", prevName, "rolled_back", rolledBack,
			"log_path", filepath.Join(a.plan.FinalDir, buildLog),
		}, kv...)...)
		if tail := tailLines(m.opt.FS, filepath.Join(a.plan.FinalDir, buildLog), logTailLines); tail != "" {
			e.Details["log_tail"] = tail
		}
		target["rolled_back"] = fmt.Sprint(rolledBack)
		target["cause"] = cause
		m.auditSwitch(a.req, audit.ResultFail, e.Code, target)
		m.log.Error("the new "+m.opt.Recipe.Name+" build was rejected", "build_id", id, "cause", cause, "rolled_back", rolledBack, "error", err)
		return e
	}

	if err := m.setCurrent(newTarget); err != nil {
		return res, fail(CodeBuildUnusable, StageSwitch, "symlink", err, prevTarget != "")
	}

	start := m.opt.Clock()
	viaAgent, err := m.restartLlama(ctx)
	var def string
	var warns []string
	var check *Error
	if err == nil {
		progress(StageCheck, "Checking that the new build serves")
		def, warns, check = m.postCheck(ctx, start)
	}
	if err == nil && check == nil {
		res.defaultModel, res.warnings = def, warns
		if !viaAgent && m.opt.Recorder != nil {
			if rerr := m.opt.Recorder.RecordBuild(id); rerr != nil {
				res.warnings = append(res.warnings, "The installed build could not be recorded in the state file: "+rerr.Error())
			}
		}
		m.auditSwitch(a.req, audit.ResultOK, "", target)
		m.log.Info("switched to the new "+m.opt.Recipe.Name+" build", "build_id", id, "previous", prevName)
		return res, nil
	}

	// Something failed: put current back, even when the caller gave up meanwhile.
	cause, cerr := "restart_failed", err
	if check != nil {
		cause, cerr = check.Details["cause"].(string), check.Err
	}
	var busy *BusyError
	if errors.As(err, &busy) {
		// The unit was never restarted, so the old build is still the one running: undo the link only.
		rolled := prevTarget != "" && m.setCurrent(prevTarget) == nil
		e := fail(CodeBusy, StageSwitch, "agent_busy", err, rolled)
		e.Message = "The agent is busy with another model operation; the new build was not activated."
		if busy.OpID != "" {
			e.Details["op_id"] = busy.OpID
		}
		return res, e
	}
	if ctx.Err() != nil && cerr == nil {
		cerr = ctx.Err()
	}
	rctx := context.WithoutCancel(ctx)
	rolled := false
	if prevTarget != "" {
		if serr := m.setCurrent(prevTarget); serr != nil {
			m.log.Error("could not put the previous "+m.opt.Recipe.Name+" build back", "error", serr)
		} else {
			rolled = true
			if _, rerr := m.restartLlama(rctx); rerr != nil {
				m.log.Error(m.opt.Recipe.Service+" did not restart on the previous build", "error", rerr)
				if err != nil && m.opt.Recipe.Service == LlamaRecipe.Service {
					// llama started on neither build: the build is not the cause.
					return res, m.serviceDown(a, fail, switchStage(check), err, rerr)
				}
				rolled = false
			}
		}
	}
	e := fail(CodeBuildUnusable, switchStage(check), cause, cerr, rolled, "restart_error", errString(err))
	if ctx.Err() != nil {
		return res, &Error{Code: e.Code, Stage: e.Stage, Message: e.Message, Err: ctx.Err(), Details: e.Details}
	}
	return res, e
}

// switchStage is where a failed switch stopped: the check when the service restarted, else the switch.
func switchStage(check *Error) Stage {
	if check != nil {
		return StageCheck
	}
	return StageSwitch
}

// serviceDown is the result of a switch whose service started on neither the new build nor the
// previous one (after current was put back): something outside the build stops it, most often another
// program on its port, such as an old llama service that still runs. The new build is not blamed and
// current points at the previous build again.
func (m *Manager) serviceDown(a activation, fail func(string, Stage, string, error, bool, ...any) *Error, st Stage, err, rerr error) *Error {
	code, cause := doctor.LLM001, "service_not_starting"
	busy := m.opt.PortInUse != nil && m.opt.PortInUse()
	if busy {
		code, cause = doctor.LLM003, "port_in_use"
	}
	e := fail(code, st, cause, err, true, "restart_error", errString(err), "previous_restart_error", errString(rerr), "port_in_use", busy)
	svc := m.opt.Recipe.Service
	e.Message = fmt.Sprintf("The new %s build %s was not switched on: %s did not start on it, nor on the previous build, so the build is not the cause. The previous build is selected again.",
		m.opt.Recipe.Name, a.plan.BuildID, svc)
	if busy {
		e.Message += " Another program is using the port of " + svc + "."
	}
	return e
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// setCurrent points current at target atomically: a new symlink under another name, renamed over
// the old one (ln -sfn target current.tmp && mv -T current.tmp current).
func (m *Manager) setCurrent(target string) error {
	fsys := m.opt.FS
	tmp := filepath.Join(m.root, currentTmp)
	if err := fsys.RemoveAll(tmp); err != nil {
		return err
	}
	if err := fsys.Symlink(target, tmp); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(m.root, currentLink)); err != nil {
		_ = fsys.RemoveAll(tmp)
		return err
	}
	return nil
}

// agentRestart returns what asks the agent to restart the recipe's service: the Services seam for
// any recipe, else llama's Agent for llama. up is nil when there is no agent to ask.
func (m *Manager) agentRestart() (up func(context.Context) bool, restart func(context.Context) error) {
	if s := m.opt.Services; s != nil {
		name := m.opt.Recipe.Service
		return s.Up, func(ctx context.Context) error { return s.RestartService(ctx, name) }
	}
	if a := m.opt.Agent; a != nil && m.opt.Recipe.Service == LlamaRecipe.Service {
		return a.Up, a.RestartLlama
	}
	return nil, nil
}

// restartLlama restarts the recipe's service (localai-llama, localai-whisper) so that it runs what
// current points at. The agent does it (and, for llama, restores the models) when it is up,
// retrying while it is busy; otherwise systemctl does. It reports which one ran.
func (m *Manager) restartLlama(ctx context.Context) (viaAgent bool, err error) {
	if up, restart := m.agentRestart(); up != nil && up(ctx) {
		err := m.restartViaAgent(ctx, restart)
		if err == nil || !errors.Is(err, ErrAgentUnavailable) {
			return true, err
		}
		m.log.Warn("the agent stopped answering; restarting "+m.opt.Recipe.Service+" with systemctl", "error", err)
	}
	return false, m.systemctlRestart(ctx)
}

func (m *Manager) restartViaAgent(ctx context.Context, restart func(context.Context) error) error {
	t := m.opt.Timing
	first := m.opt.Clock()
	for {
		err := restart(ctx)
		var busy *BusyError
		if !errors.As(err, &busy) {
			return err
		}
		if m.opt.Clock().Sub(first) >= t.BusyWait || ctx.Err() != nil {
			return busy
		}
		m.log.Info("the agent is busy; trying again", "op_id", busy.OpID)
		m.opt.Sleep(ctx, t.BusyEvery)
	}
}

func (m *Manager) systemctlRestart(ctx context.Context) error {
	p, err := m.opt.Lookup("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl: %w", err)
	}
	res, err := m.opt.Exec.Run(ctx, execx.Cmd{
		Path: p, Args: []string{"restart", m.opt.Recipe.Unit}, Timeout: m.opt.Timing.Systemctl, MaxOut: 64 << 10,
	})
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w", m.opt.Recipe.Unit, err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("systemctl restart %s exited with status %d: %s", m.opt.Recipe.Unit, res.ExitCode, msg)
	}
	return nil
}

// postCheck is the post-switch check: /health within 60 s of the restart, then the default model is
// loaded (loaded through the agent when it is not) and a 16-token completion against it succeeds,
// all within 180 s of the restart. With no default model only the health check runs, with a warning.
// It returns the default model it checked.
func (m *Manager) postCheck(ctx context.Context, start time.Time) (string, []string, *Error) {
	if m.opt.Recipe.Service != LlamaRecipe.Service {
		return m.serviceCheck(ctx, start)
	}
	probe := m.opt.Probe
	if probe == nil {
		return "", []string{"The new build was not checked: nothing was available to ask the router."}, nil
	}
	t := m.opt.Timing
	healthBy, overall := start.Add(t.HealthWait), start.Add(t.CheckWait)
	bad := func(cause string, err error) (string, []string, *Error) {
		return "", nil, newErr(CodeBuildUnusable, StageCheck, "The new llama.cpp build did not work.", err, "cause", cause)
	}
	// stop reports whether the caller gave up; the check then fails so that the switch is undone.
	stop := func() bool { return ctx.Err() != nil }

	for {
		err := probe.Health(ctx)
		if err == nil {
			break
		}
		if stop() {
			return bad("cancelled", ctx.Err())
		}
		if !m.opt.Clock().Before(healthBy) {
			return bad("health_timeout", err)
		}
		m.opt.Sleep(ctx, t.Poll)
	}

	def, err := probe.DefaultModel(ctx)
	if err != nil {
		return bad("default_lookup", err)
	}
	if def == "" {
		return "", []string{"No default model is set, so the new build was only checked for answering /health."}, nil
	}

	loadTried := false
	var lastErr error
	for loaded := false; !loaded; {
		st, err := probe.ModelStatus(ctx, def)
		switch {
		case err != nil:
			lastErr = err
		case st == StatusLoaded:
			loaded = true
		case st == StatusFailed:
			return bad("model_failed", fmt.Errorf("the default model %s failed to start", def))
		case st != StatusLoading && !loadTried:
			loadTried = true
			if lerr := probe.LoadModel(ctx, def); lerr != nil && !errors.Is(lerr, ErrNoAgent) {
				return bad("load_failed", lerr)
			}
		}
		if loaded {
			break
		}
		if stop() {
			return bad("cancelled", ctx.Err())
		}
		if !m.opt.Clock().Before(overall) {
			return bad("load_timeout", lastErr)
		}
		m.opt.Sleep(ctx, t.Poll)
	}

	remaining := overall.Sub(m.opt.Clock())
	if remaining <= 0 {
		return bad("completion_timeout", nil)
	}
	cctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := probe.Complete(cctx, def, SmokeTokens); err != nil {
		return bad("completion_failed", err)
	}
	return def, nil, nil
}
