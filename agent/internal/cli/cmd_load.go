package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// `aictl load <model-id> [--keep-others] [--no-wait]` (aictl.md section 4.3): POST /models/{id}/load
// and, unless --no-wait, wait for the model by following the model.* events of that operation.
//
// The request always carries "wait": false, also when aictl waits. A load that blocks the HTTP
// request would be cancelled by the agent when the client goes away (the handler runs under the
// request's context), so Ctrl-C could stop the load itself; with wait false the agent answers 202 at
// once and the orchestrator keeps working whatever the client does. aictl then follows /events (and
// re-reads GET /models/{id} after a lost stream), so Ctrl-C only stops the waiting (exit 130) and
// nothing ever calls unload.

// loadTimeout is the command deadline: the default load timeout (300 s) times three, plus a minute
// (aictl.md section 4.3).
const loadTimeout = 3*5*time.Minute + time.Minute

// modelLoadAnswer is the 200 and the 202 body of POST /models/{id}/load (agent.md section 3.8.8).
type modelLoadAnswer struct {
	ID            string      `json:"id"`
	Status        string      `json:"status"`
	AlreadyLoaded bool        `json:"already_loaded"`
	LoadMS        int64       `json:"load_ms"`
	Unloaded      []string    `json:"unloaded"`
	OpID          string      `json:"op_id"`
	Warning       *wireNotice `json:"warning,omitempty"`
}

// wireNotice is a doctor code with a sentence, as the agent attaches it to an answer.
type wireNotice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errLoadAnswered ends Follow when the first answer already says how the load ended (200).
var errLoadAnswered = errors.New("load: answered at once")

func init() {
	Register(&Command{
		Name:    "load",
		Summary: "Load a model so it can answer (other models are unloaded to make room).",
		Usage:   "<model-id> [--keep-others] [--no-wait]",
		Description: `Starts the model and waits until it is ready, showing the progress. If it cannot start, the
previous model is started again. Ctrl-C only stops the waiting: the model keeps loading.
--keep-others does not unload the other models (the load is refused when there is no room).
--no-wait returns as soon as the load has started. --json prints the agent body (with the wait, the final
answer: id, status, already_loaded, load_ms, unloaded, op_id).`,
		Timeout: loadTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("keep-others", false, "do not unload the other models to make room")
			fs.Bool("no-wait", false, "return as soon as the load has started")
		},
		Run: runLoad,
	})
}

func runLoad(c *Call) error {
	id, err := c.modelIDArg()
	if err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	noWait := c.Bool("no-wait")
	names := c.modelNames(cl)
	name := names.of(id)
	body := map[string]any{"wait": false, "unload_others": !c.Bool("keep-others")}

	post := func(ctx context.Context) (modelLoadAnswer, json.RawMessage, error) {
		var a modelLoadAnswer
		raw, _, err := c.callWith(cl, ctx, http.MethodPost, modelPath(id, "/load"), body, &a)
		return a, raw, err
	}

	if noWait {
		a, raw, err := post(c.Ctx)
		if err != nil {
			return err
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.loadWarning(a)
		if a.Status == "loaded" {
			c.loadReady(id, name, a)
			return nil
		}
		c.Out.Info(pick(c.Lang,
			"Loading "+name+" has started. Check it with `aictl models`.",
			"Ya empezó la carga de "+name+". Revísela con `aictl models`."))
		return nil
	}

	var first modelLoadAnswer
	var firstRaw json.RawMessage
	posted := false
	st := &loadWatch{c: c, id: id, name: name, names: names}
	res, ferr := c.Follow(FollowSpec{
		Progress: c.loadProgress(cl, st, &posted),
		Request: func(ctx context.Context) (string, error) {
			a, raw, err := post(ctx)
			if err != nil {
				return "", err
			}
			posted = true
			first, firstRaw = a, raw
			if a.Status == "loaded" { // already loaded, or finished before the answer
				return "", errLoadAnswered
			}
			return a.OpID, nil
		},
	})
	switch {
	case errors.Is(ferr, errLoadAnswered):
		if c.Global.JSON {
			return c.emitRaw(firstRaw)
		}
		c.loadWarning(first)
		c.loadReady(id, name, first)
		return nil
	case ferr != nil:
		return ferr
	}
	done, _ := res.Value.(*loadDone)
	if done == nil {
		done = &loadDone{}
	}
	answer := modelLoadAnswer{ID: id, Status: "loaded", LoadMS: done.loadMS, Unloaded: st.unloaded(), OpID: first.OpID, Warning: first.Warning}
	if c.Global.JSON {
		return c.Out.JSON().Document(answer)
	}
	c.loadWarning(first)
	c.loadReady(id, name, answer)
	return nil
}

// loadDone is how a followed load ended well.
type loadDone struct{ loadMS int64 }

// loadWatch is what the events of one load have said so far.
type loadWatch struct {
	c     *Call
	id    string
	name  string
	names modelNameMap
	// evicting are the models still being unloaded to make room; evicted those that were.
	evicting map[string]bool
	evicted  []string
	// announced is true once "Unloading X to make room" was shown.
	announced bool
}

func (w *loadWatch) unloaded() []string {
	if w.evicted == nil {
		return []string{}
	}
	return w.evicted
}

// loadReady prints the result of a load that worked.
func (c *Call) loadReady(id, name string, a modelLoadAnswer) {
	switch {
	case a.AlreadyLoaded:
		c.Out.OK(pick(c.Lang, name+" is already loaded.", name+" ya está cargado."))
	case a.LoadMS > 0:
		took := out.Duration(time.Duration(a.LoadMS) * time.Millisecond)
		c.Out.OK(pick(c.Lang, name+" is ready. ("+took+")", name+" está listo. ("+took+")"))
	default:
		c.Out.OK(pick(c.Lang, name+" is ready.", name+" está listo."))
	}
	c.Out.Secondary(pick(c.Lang,
		"  Use it at "+llamaLocalBase+" with model "+quote(id)+".",
		"  Úselo en "+llamaLocalBase+" con el modelo "+quote(id)+"."))
}

// loadWarning shows the memory warning (LAI-HW-005, LAI-HW-006) the agent attached to the answer.
func (c *Call) loadWarning(a modelLoadAnswer) {
	if a.Warning != nil && a.Warning.Code != "" {
		c.Out.Problem(c.codeProblem(a.Warning.Code, a.Warning.Message))
	}
}

// loadProgress says which events a load watches and how they are worded.
func (c *Call) loadProgress(cl *Client, w *loadWatch, posted *bool) progress.Spec {
	loading := func(percent float64) *progress.Update {
		return &progress.Update{Label: pick(c.Lang, "Loading ", "Cargando ") + w.name, Key: "load", Percent: percent}
	}
	return progress.Spec{
		Types:    []string{agentevents.TypeModelLoading, agentevents.TypeModelLoaded, agentevents.TypeModelUnloaded, agentevents.TypeModelFailed},
		AwaitKey: true,
		Match:    func(ev progress.Event, key string) bool { return key != "" && ev.OpID == key },
		OnEvent: func(ev progress.Event) progress.Step {
			switch ev.Type {
			case agentevents.TypeModelLoading:
				var d agentevents.ModelLoading
				if json.Unmarshal(ev.Data, &d) != nil || d.Model != w.id {
					return progress.Step{}
				}
				if d.Progress != nil {
					return progress.Step{Update: loading(*d.Progress * 100)}
				}
				if len(d.Unloading) > 0 && !w.announced {
					w.announced = true
					w.evicting = map[string]bool{}
					for _, id := range d.Unloading {
						w.evicting[id] = true
					}
					return progress.Step{Update: &progress.Update{
						Label: pick(c.Lang, "Unloading "+w.names.list(c.Lang, d.Unloading)+" to make room",
							"Descargando "+w.names.list(c.Lang, d.Unloading)+" para hacer lugar"),
						Key: "unload", Percent: progress.NoBar}}
				}
				return progress.Step{Update: loading(progress.NoBar)}
			case agentevents.TypeModelUnloaded:
				var d agentevents.ModelUnloaded
				if json.Unmarshal(ev.Data, &d) != nil || d.Reason != agentevents.UnloadEvicted {
					return progress.Step{}
				}
				w.evicted = append(w.evicted, d.Model)
				if w.evicting[d.Model] {
					delete(w.evicting, d.Model)
					if len(w.evicting) == 0 { // the room is made: the load itself starts
						return progress.Step{Update: loading(progress.NoBar)}
					}
				}
			case agentevents.TypeModelLoaded:
				var d agentevents.ModelLoaded
				// A rollback loads the previous models again with the same operation id: only the
				// requested model ends the wait.
				if json.Unmarshal(ev.Data, &d) != nil || d.Model != w.id {
					return progress.Step{}
				}
				return progress.Step{Done: true, Result: &loadDone{loadMS: d.LoadMS}}
			case agentevents.TypeModelFailed:
				var d agentevents.ModelFailed
				if json.Unmarshal(ev.Data, &d) != nil || d.Model != w.id {
					return progress.Step{}
				}
				return progress.Step{Err: c.loadFailure(w.name, ev.OpID, d)}
			}
			return progress.Step{}
		},
		// After a lost stream the model's real state decides: loaded is done, failed is a failure,
		// loading shows the line again; anything else (the load has not started, or was rolled back
		// and the event was missed) keeps waiting until the deadline.
		Reread: func(ctx context.Context) (progress.Step, error) {
			if !*posted {
				return progress.Step{}, nil
			}
			var m wireModel
			if _, _, err := c.callWith(cl, ctx, http.MethodGet, modelPath(w.id, ""), nil, &m); err != nil {
				return progress.Step{}, err
			}
			switch m.Status {
			case "loaded":
				var ms int64
				if m.LastLoadMS != nil {
					ms = *m.LastLoadMS
				}
				return progress.Step{Done: true, Result: &loadDone{loadMS: ms}}, nil
			case "loading":
				return progress.Step{Update: loading(progress.NoBar)}, nil
			case "failed":
				return progress.Step{Err: c.loadFailure(w.name, "", agentevents.ModelFailed{Model: w.id, Code: doctor.LLM009})}, nil
			}
			return progress.Step{}, nil
		},
	}
}

// loadFailure is the error of a load that did not work: the agent's code with a sentence in the
// user's language, whether the previous model was started again, and the exit code (7 for a model that
// took too long, 1 for the rest).
func (c *Call) loadFailure(name, opID string, d agentevents.ModelFailed) error {
	code := d.Code
	if code == "" {
		code = doctor.LLM009
	}
	rolled := pick(c.Lang, " The previous model was started again.", " Se volvió a iniciar el modelo anterior.")
	if !d.RolledBack {
		rolled = ""
	}
	var msg string
	exit := ExitFailed
	switch code {
	case doctor.LLM009:
		msg = pick(c.Lang, "The model "+name+" failed to start.", "El modelo "+name+" no pudo iniciar.") + rolled
	case doctor.LLM010:
		msg = pick(c.Lang, "The model "+name+" took too long to start.", "El modelo "+name+" tardó demasiado en iniciar.") + rolled
		exit = ExitTimeout
	case doctor.LLM012:
		msg = pick(c.Lang, "No model could be started, not even the previous one.", "No se pudo iniciar ningún modelo, ni siquiera el anterior.")
	default:
		msg = sentence(d.Message)
	}
	e := c.codeError(code, exit, "")
	if msg != "" {
		e.Message = msg
	}
	restored := d.Restored
	if restored == nil {
		restored = []string{}
	}
	e.WithDetails("model", d.Model, "rolled_back", d.RolledBack, "restored", restored)
	if opID != "" {
		e.WithDetails("op_id", opID)
	}
	return e
}
