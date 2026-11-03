package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// `aictl drill` (aictl.md section 4.17, drill.md section 3.1, AICTL-10): start the offline-readiness
// drill and follow it, stop it, show its results and record the manual "pull the plug" drill. The
// engine, the routes and their handlers are the agent's (DRILL-01); this is flags, the events, the
// words and the exit codes: 0 pass, 8 pass with warnings, 9 fail. The files:
//
//	cmd_drill.go        start and follow (the command itself), --cancel and --last
//	cmd_drill_show.go   `drill show`, `drill record-manual`, the results renderer
//	cmd_drill_text.go   the plain-language names of the checks

// drillTimeout is the default deadline of a drill (aictl.md section 4.17).
const drillTimeout = 30 * time.Minute

// drillIDRe is a run id as the agent writes it: drl-<UTC time>-<4 hex>, with -manual for a manual
// record. The CLI refuses anything else before it reaches a URL.
var drillIDRe = regexp.MustCompile(`^drl-[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// checkIDRe is a check id as `--only` takes it (drill.md section 4.2): dots, letters, digits, "_" and "-".
var checkIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var errDrillSkipped = errors.New("drill: the run was skipped")

func init() {
	Register(&Command{
		Name:    "drill",
		Summary: "Run the offline test (the drill), show its results, or record the manual one.",
		Usage:   "[--quick] [--only <check-id>]... [--wait] [--scheduled] | --cancel | --last",
		Description: `Tests that the node works with no internet: it switches to a private test (nothing is published
during the test), checks the brains, the library, the maps, the speech and the manuals, and puts
everything back as it was. Without --wait it returns when the drill has started if no terminal is
attached; on a terminal (or with --wait or --json) it follows the drill to the end.
  --quick       Skip the full file check and the models that are not the default (about 2 minutes).
  --only <id>   Run only this check (and the checks under it); may be repeated.
  --wait        Follow the drill to the end even without a terminal.
  --scheduled   What the monthly timer runs: waits to the end; a drill that is skipped (a sync is
                running, survival mode, low battery) prints "Drill skipped" and exits 0.
  --cancel      Stop the running drill; the agent puts everything back as it was.
  --last        Same as "aictl drill show".
Exit 0: passed. Exit 8: passed with warnings. Exit 9: failed. Exit 6: a manual drill is refused right now
(a sync runs, survival mode). Subcommands: show, record-manual.`,
		Timeout: drillTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("quick", false, "skip the full file check and the models that are not the default")
			fs.Var(&multiFlag{}, "only", "run only this check id (and the checks under it); may be repeated")
			fs.Bool("wait", false, "follow the drill to the end even without a terminal")
			fs.Bool("scheduled", false, "run as the monthly timer does (waits; a skipped drill exits 0)")
			fs.Bool("cancel", false, "stop the running drill")
			fs.Bool("last", false, "show the last results (same as drill show)")
		},
		Run: runDrill,
	})
}

// drillAccepted is the answer of POST /drill and DELETE /drill and POST /drill/manual.
type drillAccepted struct {
	RunID   string `json:"run_id"`
	State   string `json:"state,omitempty"`
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
	RetryAt string `json:"retry_at,omitempty"`
}

// drillDone is what drill.done says about the end of a run.
type drillDone struct {
	RunID         string
	Overall       string
	SurvivalReady bool
}

func runDrill(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	cancel, last := c.Bool("cancel"), c.Bool("last")
	start := c.Bool("quick") || len(c.multi("only")) > 0 || c.Bool("wait") || c.Bool("scheduled")
	switch {
	case cancel && (last || start):
		return c.usageErr(pick(c.Lang, "--cancel stands alone.", "--cancel va solo."),
			pick(c.Lang, "Run `aictl drill --cancel` with no other option.", "Ejecute `aictl drill --cancel` sin otras opciones."))
	case last && start:
		return c.usageErr(pick(c.Lang, "--last stands alone.", "--last va solo."),
			pick(c.Lang, "Run `aictl drill --last` with no other option, or `aictl drill show`.", "Ejecute `aictl drill --last` sin otras opciones, o `aictl drill show`."))
	case cancel:
		return drillCancel(c)
	case last:
		return c.drillShow("")
	}
	only := c.multi("only")
	for _, id := range only {
		if !checkIDRe.MatchString(id) {
			return c.usageErr(
				fmt.Sprintf(pick(c.Lang, "%q is not a check id.", "%q no es un id de comprobación."), id),
				pick(c.Lang, "Check ids look like drl.stt.en. Run `aictl drill show` to see the ones of the last drill.",
					"Los ids son como drl.stt.en. Ejecute `aictl drill show` para ver los del último simulacro."))
		}
	}
	scheduled := c.Bool("scheduled")
	trigger := "manual"
	if scheduled {
		trigger = "scheduled"
	}
	body := map[string]any{"quick": c.Bool("quick"), "only": only, "trigger": trigger}
	wait := c.Bool("wait") || scheduled || c.Global.JSON || c.Term.Interactive()
	return c.drillRun(body, wait, scheduled)
}

// drillCancel is `aictl drill --cancel`: DELETE /drill. Exit 0, or 5 when no drill runs (the agent's
// 404 LAI-AGT-021).
func drillCancel(c *Call) error {
	ctx, cancel := c.withDefaultTimeout(30 * time.Second)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	raw, _, err := c.callWith(cl, ctx, http.MethodDelete, "/drill", nil, nil)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		"The drill is stopping. The agent puts everything back as it was.",
		"El simulacro se está deteniendo. El agente deja todo como estaba."))
	return nil
}

// drillRun starts a drill and, when wait is set, follows it to the end.
func (c *Call) drillRun(body map[string]any, wait, scheduled bool) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	post := func(ctx context.Context) (drillAccepted, json.RawMessage, error) {
		var a drillAccepted
		raw, _, err := c.callWith(cl, ctx, http.MethodPost, "/drill", body, &a)
		return a, raw, err
	}

	if !wait {
		ctx, cancel := c.withDefaultTimeout(30 * time.Second)
		defer cancel()
		a, raw, err := post(ctx)
		if err != nil {
			return err
		}
		if a.State == "skipped" {
			return c.drillSkipped(a, raw)
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Drill started (run %s). It takes a while; see the result with `aictl drill show`.",
			"Simulacro iniciado (ejecución %s). Tarda un rato; vea el resultado con `aictl drill show`."), a.RunID))
		return nil
	}

	var runID string
	var skipped drillAccepted
	var skippedRaw json.RawMessage
	var cancelledByUs bool
	spec := progress.Spec{
		Types:    []string{"drill.*"},
		AwaitKey: true,
		Match: func(ev progress.Event, key string) bool {
			var d struct {
				RunID string `json:"run_id"`
			}
			return ev.OpID == key && key != "" || json.Unmarshal(ev.Data, &d) == nil && d.RunID == key
		},
		OnEvent: func(ev progress.Event) progress.Step {
			switch ev.Type {
			case agentevents.TypeDrillStarted:
				return progress.Step{Update: &progress.Update{
					Label: pick(c.Lang, "Drill: switching to a private test (nothing is published during the test)",
						"Simulacro: pasando a una prueba privada (durante la prueba no se publica nada)"),
					Key: "start", Percent: progress.NoBar,
				}}
			case agentevents.TypeDrillCheckDone:
				var d agentevents.DrillCheckDone
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				return progress.Step{Update: &progress.Update{
					Label: pick(c.Lang, "Drill: running the checks", "Simulacro: ejecutando las comprobaciones"),
					Key:   "check", Percent: progress.NoBar,
					Detail: pick(c.Lang, "just checked: ", "recién comprobado: ") + drillCheckLabel(c.Lang, d.ID),
				}}
			case agentevents.TypeDrillDone:
				var d agentevents.DrillDone
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				return progress.Step{Done: true, Result: drillDone{RunID: d.RunID, Overall: d.Overall, SurvivalReady: d.SurvivalReady}}
			}
			return progress.Step{}
		},
		// GET /drill?run_id= is the truth after a lost stream: a run that ended while we were away.
		Reread: func(ctx context.Context) (progress.Step, error) {
			if runID == "" {
				return progress.Step{}, nil
			}
			var doc drillDoc
			if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/drill?run_id="+url.QueryEscape(runID), nil, &doc); err != nil {
				var ae *APIError
				if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
					return progress.Step{}, nil
				}
				return progress.Step{}, err
			}
			if doc.RunID == runID && doc.ended() {
				return progress.Step{Done: true, Result: drillDone{RunID: doc.RunID, Overall: doc.Overall, SurvivalReady: doc.SurvivalReady}}, nil
			}
			return progress.Step{}, nil
		},
	}
	fs := FollowSpec{
		Progress: spec,
		Request: func(ctx context.Context) (string, error) {
			a, raw, err := post(ctx)
			if err != nil {
				if scheduled && c.drillSkippableRefusal(err) {
					var ae *APIError
					_ = errors.As(err, &ae)
					skipped = drillAccepted{State: "skipped", Code: ae.Code}
					return "", errDrillSkipped
				}
				return "", err
			}
			runID = a.RunID
			if a.State == "skipped" {
				skipped, skippedRaw = a, raw
				return "", errDrillSkipped
			}
			return a.RunID, nil
		},
		OnInterrupt: func(ctx context.Context) progress.InterruptAction {
			return c.askToCancelDrill(ctx, cl, scheduled, &cancelledByUs)
		},
	}
	res, ferr := c.Follow(fs)
	if errors.Is(ferr, errDrillSkipped) {
		return c.drillSkipped(skipped, skippedRaw)
	}
	if ferr != nil {
		return ferr
	}
	done, _ := res.Value.(drillDone)

	// The full results document: the lines of the report and the --json document.
	var doc drillDoc
	raw, _, gerr := c.callWith(cl, c.Ctx, http.MethodGet, "/drill?run_id="+url.QueryEscape(done.RunID), nil, &doc)
	if gerr != nil {
		if c.Global.JSON {
			return gerr
		}
		doc = drillDoc{RunID: done.RunID, Overall: done.Overall, SurvivalReady: done.SurvivalReady}
	}
	if c.Global.JSON {
		if err := c.emitRaw(raw); err != nil {
			return err
		}
	} else {
		c.drillReport(doc, drillReportRun)
	}
	if doc.State == "aborted" || done.Overall == "aborted" {
		if cancelledByUs {
			return ExitWith(ExitInterrupted)
		}
		return ExitWith(ExitErrors)
	}
	return drillExit(orText(doc.Overall, done.Overall))
}

// drillSkippableRefusal says whether a --scheduled start was refused for a reason that is a skip, not a
// failure: a sync, verify or import run, or another drill (LAI-DRL-002), survival mode (003), llama in
// use (004) or a low battery (005). The agent answers 202 with state skipped for these; the refusal is
// the same decision from an agent that answers 409. The timer must not fail its unit for either.
func (c *Call) drillSkippableRefusal(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		return false
	}
	switch ae.Code {
	case doctor.DRL002, doctor.DRL003, doctor.DRL004, doctor.DRL005:
		return true
	}
	return false
}

// drillSkipped reports a drill that did not start for a good reason: exit 0, never exit 3 (aictl.md
// section 3.4). raw is the agent's answer, printed unchanged with --json.
func (c *Call) drillSkipped(a drillAccepted, raw json.RawMessage) error {
	if c.Global.JSON {
		if raw != nil {
			return c.emitRaw(raw)
		}
		return c.Out.JSON().Document(a)
	}
	reason := strings.TrimSuffix(strings.TrimSpace(a.Reason), ".")
	if title, _, ok := doctor.Text(a.Code, string(c.Lang)); ok && (c.Lang == LangES || reason == "") {
		reason = strings.TrimSuffix(title, ".")
	}
	if reason == "" {
		reason = pick(c.Lang, "it could not run now", "no se pudo ejecutar ahora")
	}
	c.Out.Info(fmt.Sprintf(pick(c.Lang, "Drill skipped: %s.", "Simulacro omitido: %s."), reason))
	return nil
}

// askToCancelDrill is the interrupt hook of a followed drill: Ctrl-C on a terminal asks "Cancel the
// drill too?" and a yes sends DELETE /drill. Under systemd (--scheduled), with --yes or with no terminal
// a stop is a SIGTERM and there is nobody to ask: the call goes out at once, as drill.md section 3.1
// says. In both cases the wait carries on until the run reports that it stopped, so the agent has put
// everything back before aictl exits.
func (c *Call) askToCancelDrill(ctx context.Context, cl *Client, scheduled bool, cancelled *bool) progress.InterruptAction {
	if !scheduled && !c.Global.Yes {
		if c.Global.JSON || !c.Term.Interactive() {
			// No terminal: a signal is the unit stopping (or a script giving up), and nobody can be asked.
		} else {
			fmt.Fprint(c.Stderr, pick(c.Lang, "Cancel the drill too?", "¿Cancelar también el simulacro?")+pick(c.Lang, " [y/N] ", " [s/N] "))
			line, err := c.Term.ReadLine(ctx)
			if err != nil {
				fmt.Fprintln(c.Stderr)
				return progress.Leave
			}
			if !isYes(c.Lang, line) {
				return progress.Leave
			}
		}
	}
	if _, err := cl.Do(ctx, http.MethodDelete, "/drill", nil, nil); err != nil {
		return progress.Leave
	}
	*cancelled = true
	return progress.KeepWaiting
}
