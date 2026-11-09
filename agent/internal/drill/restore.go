package drill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
)

// LoadInfo is what ModelOps.Load reports.
type LoadInfo struct {
	// LoadMS is how long the router took to load the model; 0 when it was loaded already.
	LoadMS int64
	// AlreadyLoaded: nothing was done, the model was resident.
	AlreadyLoaded bool
}

// ModelError is the error of a model operation, with the doctor code the operation reported (an
// LAI-LLM-... code of the router). The checks put Code in their details so the failure can be told
// apart from a missing model, a busy lock or an out-of-memory load.
type ModelError struct {
	Code    string
	Message string
	Err     error
}

func (e *ModelError) Error() string {
	s := e.Message
	if e.Code != "" {
		s = e.Code + ": " + s
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the underlying error.
func (e *ModelError) Unwrap() error { return e.Err }

// ModelOps is the model layer as the drill sees it (drill.md section 4.3): the router orchestrator
// behind a small interface, so the restore and the model checks are tested with a fake. Every load
// and unload goes through the one path users use (router.Orchestrator, with its model-operations
// lock), never around it.
type ModelOps interface {
	// Resident lists the models loaded now, in the order a restore brings them back: the order they
	// were loaded in as far as the node knows it.
	Resident(ctx context.Context) ([]string, error)
	// Default is the default model the node has recorded; "" when none.
	Default() string
	// Load loads a model and waits until it is ready (the router's load timeout, 300 s by default).
	// With evict the router may unload other models to make room; without it a load that does not fit
	// is refused.
	Load(ctx context.Context, id string, evict bool) (LoadInfo, error)
	// Unload unloads a model and waits until it is gone.
	Unload(ctx context.Context, id string) error
	// SetDefault makes a model the default.
	SetDefault(ctx context.Context, id string) error
}

// WhisperUnit is the speech-to-text service the restore may have to start again.
type WhisperUnit interface {
	// Running reports whether localai-whisper.service is active.
	Running(ctx context.Context) (bool, error)
	// Start starts it and waits for the job to finish.
	Start(ctx context.Context) error
}

// StateRestorer is the Restorer of the drill (DRILL-03, drill.md section 4.3). Capture records the
// models loaded (in order), the default model and whether whisper runs; Restore puts the node back
// exactly there. Restore is idempotent and works from the captured state alone, so it serves the end
// of a run (also an aborted one, or one that panicked) and Recover after a crash: it unloads whatever
// is loaded and was not captured, loads again whatever was captured and is not loaded, resets the
// default and restarts whisper, then reads everything back and says whether the node is as it was.
type StateRestorer struct {
	models  ModelOps
	whisper WhisperUnit
	log     *slog.Logger
}

var _ Restorer = (*StateRestorer)(nil)

// NewStateRestorer returns a StateRestorer. models is required; whisper may be nil on a node without
// the speech service (it is then never captured as running). A nil log uses slog.Default.
func NewStateRestorer(models ModelOps, whisper WhisperUnit, log *slog.Logger) (*StateRestorer, error) {
	if models == nil {
		return nil, errors.New("drill: the state restorer needs the model layer")
	}
	if log == nil {
		log = slog.Default()
	}
	return &StateRestorer{models: models, whisper: whisper, log: log.With("component", "drill.restore")}, nil
}

// Capture implements Restorer. It changes nothing. When the models cannot be read it fails, and the
// engine then changes nothing either and reports restored.ok false.
func (r *StateRestorer) Capture(ctx context.Context) (Captured, error) {
	loaded, err := r.models.Resident(ctx)
	if err != nil {
		return Captured{}, fmt.Errorf("could not read the loaded models: %w", err)
	}
	c := Captured{LoadedModels: slices.Clone(loaded), DefaultModel: r.models.Default()}
	if r.whisper != nil {
		running, err := r.whisper.Running(ctx)
		if err != nil {
			return Captured{}, fmt.Errorf("could not read whether whisper runs: %w", err)
		}
		c.WhisperRunning = running
	}
	return c.normalized(), nil
}

// Restore implements Restorer. Every step runs even when an earlier one failed (a model that will
// not load must not keep whisper from starting); the errors are joined and the result says
// OK: false.
func (r *StateRestorer) Restore(ctx context.Context, c Captured) (Restored, error) {
	c = c.normalized()
	var errs []error
	note := func(step string, err error) {
		r.log.Error("drill restore step failed", "step", step, "error", err.Error())
		errs = append(errs, fmt.Errorf("%s: %w", step, err))
	}

	// 1. Unload the drill-loaded models: whatever is loaded now and was not before.
	if cur, err := r.models.Resident(ctx); err != nil {
		note("read the loaded models", err)
	} else {
		for _, id := range cur {
			if slices.Contains(c.LoadedModels, id) {
				continue
			}
			if err := r.models.Unload(ctx, id); err != nil {
				note("unload "+id, err)
			}
		}
	}

	// 2. Load the previous models again, in their original order. A model still loaded is left alone.
	// Nothing is evicted: the room was made in step 1, and an eviction here would unload a model that
	// belongs to the snapshot.
	cur, _ := r.models.Resident(ctx)
	for _, id := range c.LoadedModels {
		if slices.Contains(cur, id) {
			continue
		}
		if _, err := r.models.Load(ctx, id, false); err != nil {
			note("load "+id, err)
		}
	}

	// 3. Reset the default. The drill never clears a default (no model operation can), so an empty
	// captured default is left as it is.
	if c.DefaultModel != "" && r.models.Default() != c.DefaultModel {
		if err := r.models.SetDefault(ctx, c.DefaultModel); err != nil {
			note("reset the default model", err)
		}
	}

	// 4. Restart whisper if it was running and is not.
	if r.whisper != nil && c.WhisperRunning {
		running, err := r.whisper.Running(ctx)
		switch {
		case err != nil:
			note("read whether whisper runs", err)
		case !running:
			if err := r.whisper.Start(ctx); err != nil {
				note("start whisper", err)
			}
		}
	}

	// Read it all back: the result is what the node looks like now, and OK says it matches.
	out := Restored{LoadedModels: []string{}, DefaultModel: r.models.Default()}
	if now, err := r.models.Resident(ctx); err != nil {
		note("read the loaded models back", err)
	} else {
		out.LoadedModels = slices.Clone(now)
		if !sameSet(now, c.LoadedModels) {
			errs = append(errs, fmt.Errorf("loaded models are %v, expected %v", now, c.LoadedModels))
		}
	}
	if c.DefaultModel != "" && out.DefaultModel != c.DefaultModel {
		errs = append(errs, fmt.Errorf("the default model is %q, expected %q", out.DefaultModel, c.DefaultModel))
	}
	if r.whisper != nil && c.WhisperRunning {
		if running, err := r.whisper.Running(ctx); err == nil && !running {
			errs = append(errs, errors.New("whisper is not running"))
		}
	}
	out.OK = len(errs) == 0
	if !out.OK {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}
