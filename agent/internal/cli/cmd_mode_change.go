package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// modeChange asks for a switch and, unless --no-wait, waits for it by following the events.
//
// The request is POST /mode/survival (no authentication) for survival, else POST /mode with
// {"target", "wait": !no-wait, "reapply"?}; leaving survival goes through the admin PIN flow, which
// only asks when the agent answers 403 with pin_required (root and socket operators are never asked).
func modeChange(c *Call, target string) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	reapply, noWait := c.Bool("reapply"), c.Bool("no-wait")
	if target == "survival" {
		// The survival route needs no authentication: a person who cannot open the socket or read
		// the token may still use it over TCP.
		cl.AllowAnonymous()
	}

	post := func(ctx context.Context) (modeAnswer, json.RawMessage, error) {
		var a modeAnswer
		var raw json.RawMessage
		send := func() (e error) {
			if target == "survival" {
				body := map[string]any{}
				if reapply {
					body["reapply"] = true
				}
				raw, _, e = c.callWith(cl, ctx, http.MethodPost, "/mode/survival", body, &a)
				return e
			}
			body := map[string]any{"target": target, "wait": !noWait}
			if reapply {
				body["reapply"] = true
			}
			raw, _, e = c.callWith(cl, ctx, http.MethodPost, "/mode", body, &a)
			return e
		}
		if target == "survival" {
			return a, raw, send()
		}
		return a, raw, c.Admin(send)
	}

	if noWait {
		a, raw, err := post(c.Ctx)
		if err != nil {
			return err
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.modeStarted(target, a)
		return nil
	}

	var answered *modeAnswer
	var answeredRaw json.RawMessage
	posted := false
	res, ferr := c.Follow(FollowSpec{
		Progress: c.modeProgress(cl, target),
		Request: func(ctx context.Context) (string, error) {
			posted = true
			a, raw, err := post(ctx)
			if err != nil {
				return "", err
			}
			switch {
			case a.Already, a.Target == "" && a.Mode != "": // already there, or finished (wait: true)
				answered, answeredRaw = &a, raw
				return "", errModeAnswered
			case a.Queued:
				c.Out.Progress(pick(c.Lang,
					"Survival mode will start as soon as the agent is ready.",
					"El modo supervivencia empezará en cuanto el agente esté listo."))
			}
			return a.OpID, nil
		},
	})
	switch {
	case errors.Is(ferr, errModeAnswered):
		if c.Global.JSON {
			return c.emitRaw(answeredRaw)
		}
		if answered.Already {
			c.Out.Info(pick(c.Lang, "Already in "+modePhrase(c.Lang, answered.Mode)+".", "Ya está en "+modePhrase(c.Lang, answered.Mode)+"."))
			return nil
		}
		c.modeDone(target, answered.Warnings)
		return nil
	case ferr != nil:
		if target == "survival" && !posted && eventsUnavailable(ferr) {
			// The event stream is not available to this person (the agent is still starting, or
			// the caller has no token) but the request route is always open: ask anyway, the agent
			// queues it if it must, and tell the person where to look.
			a, raw, err := post(c.Ctx)
			if err != nil {
				return err
			}
			if c.Global.JSON {
				return c.emitRaw(raw)
			}
			if a.Already || a.Queued {
				c.modeStarted(target, a)
				return nil
			}
			c.Out.Info(pick(c.Lang,
				"Survival mode is starting. In a minute, open "+strings.TrimRight(c.Global.URL, "/")+" for the Survival Portal.",
				"El modo supervivencia está arrancando. En un minuto, abra "+strings.TrimRight(c.Global.URL, "/")+" para el Portal de supervivencia."))
			return nil
		}
		return ferr
	}
	changed, _ := res.Value.(*agentevents.ModeChanged)
	if changed == nil {
		changed = &agentevents.ModeChanged{To: target}
	}
	if c.Global.JSON {
		warnings := changed.Warnings
		if warnings == nil {
			warnings = []agentevents.Warning{}
		}
		return c.Out.JSON().Document(map[string]any{"op_id": changed.OpID, "from": changed.From, "mode": changed.To, "warnings": warnings})
	}
	c.modeDone(target, changed.Warnings)
	return nil
}

// eventsUnavailable reports whether following failed before anything was requested for a reason the
// request itself does not share: the agent still starting (unreachable), or a caller who may not
// read events (401, 403).
func eventsUnavailable(err error) bool {
	var ce *CLIError
	var ae *APIError
	switch {
	case errors.As(err, &ce):
		return ce.Code == CodeUnreachable
	case errors.As(err, &ae):
		return ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden
	}
	return false
}

// modeStarted reports a change that was requested and is not being followed.
func (c *Call) modeStarted(target string, a modeAnswer) {
	switch {
	case a.Already:
		c.Out.Info(pick(c.Lang, "Already in "+modePhrase(c.Lang, a.Mode)+".", "Ya está en "+modePhrase(c.Lang, a.Mode)+"."))
	case a.Queued:
		c.Out.Info(pick(c.Lang,
			"Survival mode was requested. It starts as soon as the agent is ready. Check with `aictl mode`.",
			"Se pidió el modo supervivencia. Empieza en cuanto el agente esté listo. Revise con `aictl mode`."))
	default:
		c.Out.Info(pick(c.Lang,
			"The change to "+modePhrase(c.Lang, target)+" has started. Check it with `aictl mode`.",
			"El cambio a "+modePhrase(c.Lang, target)+" ya empezó. Revíselo con `aictl mode`."))
	}
}

// modeDone prints the result of a finished change and the warnings that came with it.
func (c *Call) modeDone(target string, warnings []agentevents.Warning) {
	portal := strings.TrimRight(c.Global.URL, "/")
	switch target {
	case "survival":
		c.Out.OK(pick(c.Lang,
			"Survival mode is on. Open "+portal+" for the Survival Portal.",
			"El modo supervivencia está activo. Abra "+portal+" para el Portal de supervivencia."))
	default:
		c.Out.OK(pick(c.Lang, strings.ToUpper(target[:1])+target[1:]+" mode is on.", "El "+modePhrase(c.Lang, target)+" está activo."))
	}
	for _, w := range warnings {
		c.Out.Problem(c.codeProblem(w.Code, w.Message))
	}
}

// modeProgress says which events a mode change watches and how they are worded.
func (c *Call) modeProgress(cl *Client, target string) progress.Spec {
	inModels := false
	var names map[string]string
	nameOf := func(id string) string {
		if names == nil {
			names = map[string]string{}
			ctx, cancel := context.WithTimeout(c.Ctx, 3*time.Second)
			defer cancel()
			var m struct {
				Models []struct {
					ID          string `json:"id"`
					DisplayName string `json:"display_name"`
				} `json:"models"`
			}
			if _, err := cl.Do(ctx, http.MethodGet, "/models", nil, &m); err == nil {
				for _, x := range m.Models {
					names[x.ID] = x.DisplayName
				}
			}
		}
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	return progress.Spec{
		Types:    []string{agentevents.TypeModeStep, agentevents.TypeModeChanged, agentevents.TypeModelLoading},
		AwaitKey: true,
		Match: func(ev progress.Event, key string) bool {
			switch ev.Type {
			case agentevents.TypeModeChanged:
				var d agentevents.ModeChanged
				return ev.OpID == key || json.Unmarshal(ev.Data, &d) == nil && d.To == target
			case agentevents.TypeModeStep:
				var d agentevents.ModeStep
				return ev.OpID == key || json.Unmarshal(ev.Data, &d) == nil && d.Target == target
			}
			return inModels // a model starting while the survival models step runs
		},
		OnEvent: func(ev progress.Event) progress.Step {
			switch ev.Type {
			case agentevents.TypeModeStep:
				var d agentevents.ModeStep
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				if d.Step == "survival_models" {
					inModels = d.State == "started"
				}
				// The models step is worded by the model that is starting ("Starting Qwen 3.5 9B"),
				// from the model.loading events below; it has no line of its own.
				if label := modeStepLabel(c.Lang, d.Step); label != "" && d.State == "started" && d.Step != "survival_models" {
					return progress.Step{Update: &progress.Update{Label: label, Key: d.Step, Percent: progress.NoBar}}
				}
			case agentevents.TypeModelLoading:
				var d agentevents.ModelLoading
				if json.Unmarshal(ev.Data, &d) != nil || d.Model == "" {
					return progress.Step{}
				}
				pct := float64(progress.NoBar)
				if d.Progress != nil {
					pct = *d.Progress * 100
				}
				return progress.Step{Update: &progress.Update{
					Label: pick(c.Lang, "Starting ", "Iniciando ") + nameOf(d.Model), Key: "model:" + d.Model, Percent: pct}}
			case agentevents.TypeModeChanged:
				var d agentevents.ModeChanged
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				return progress.Step{Done: true, Result: &d}
			}
			return progress.Step{}
		},
		// After a lost stream the real mode decides: a finished change is a mode equal to the target
		// with no transition running.
		Reread: func(ctx context.Context) (progress.Step, error) {
			var m modeInfo
			if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/mode", nil, &m); err != nil {
				return progress.Step{}, err
			}
			switch {
			case m.Transition != nil:
				if label := modeStepLabel(c.Lang, m.Transition.Step); label != "" {
					return progress.Step{Update: &progress.Update{Label: label, Key: m.Transition.Step, Percent: progress.NoBar}}, nil
				}
			case m.Mode == target:
				return progress.Step{Done: true, Result: &agentevents.ModeChanged{To: target}}, nil
			}
			return progress.Step{}, nil
		},
	}
}
