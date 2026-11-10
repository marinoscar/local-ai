package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
)

// `aictl ensure` (aictl.md section 4.23d, rebuild-kit.md section 4.5, ADR-0010): install what this computer
// is missing, from the rebuild kit. The agent does it by itself after every sync run, at start and when the
// node goes online; this command is the same thing on demand (the Portal, the desktop app and the cloud have
// a button for it). It shows the state first, then what will be done, follows the progress, and ends with
// the result. It talks to the agent (GET, POST and DELETE /ensure) and needs no root: the agent installs.
//
// Exit codes: 0 complete (or a dry run), 8 it did what it could and something still needs you (a
// confirmation, a kit item that is missing), 9 a step failed, 130 you stopped it with Ctrl-C.

// ensurePoll is how often the progress is read.
var ensurePoll = time.Second

// ensureTimeout is the default deadline of a whole run: a CUDA or Vulkan build that was confirmed can take
// most of an hour.
const ensureTimeout = 3 * time.Hour

func init() {
	Register(&Command{
		Name:    "ensure",
		Summary: "Install what this computer is missing, from the rebuild kit.",
		Usage:   "[--dry-run] [--yes]",
		Description: `Installs the tools and packages the profile wants and this computer lacks (kiwix-tools, pmtiles, piper,
the offline packages, the speech-to-text program), from the files of the rebuild kit that check out. The
agent does this by itself after every sync (ensure.auto in node.yaml: on, ask or off); this command does it
now. It shows what is missing, what will be done and the progress, one step at a time. Nothing is ever
deleted, and the agent itself is never replaced (that stays "aictl update").
  --dry-run  Show what would be installed; change nothing.
  --yes      Also confirm the steps that always ask first: building the speech-to-text program from
             source (it takes a long time) and restarting the library service. At a terminal you are asked.
Ctrl-C stops it; what was installed stays. Exit 0: complete. Exit 8: something still needs you. Exit 9:
a step failed. --json prints the final picture (the body of GET /ensure) as one document.
Specification: docs/specs/aictl.md section 4.23d`,
		Timeout: ensureTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("dry-run", false, "show what would be installed and change nothing")
			declareYes(fs)
		},
		Run: runEnsure,
	})
}

func runEnsure(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	applyYes(c)
	cl, err := c.Client()
	if err != nil {
		return err
	}
	get := func(ctx context.Context) (ensure.View, json.RawMessage, error) {
		var v ensure.View
		raw, _, err := c.callWith(cl, ctx, http.MethodGet, "/ensure", nil, &v)
		return v, raw, err
	}
	post := func(ctx context.Context, body map[string]any) (ensure.View, json.RawMessage, error) {
		var v ensure.View
		raw, _, err := c.callWith(cl, ctx, http.MethodPost, "/ensure", body, &v)
		return v, raw, err
	}

	if c.Bool("dry-run") {
		v, raw, err := post(c.Ctx, map[string]any{"dry_run": true})
		if err != nil {
			return err
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.ensureState(v, false)
		c.ensureSteps(v, false)
		c.Out.Info(pick(c.Lang, "Dry run: nothing was changed.", "Prueba: no se cambió nada."))
		return nil
	}

	v, raw, err := get(c.Ctx)
	if err != nil {
		return err
	}
	if v.State == ensure.StateComplete {
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.ensureState(v, false)
		return nil
	}
	first := v.Message
	if !c.Global.JSON {
		if n := countAutomatic(v); n > 0 && !hasStatus(v, ensure.StatusFailed) {
			first = fmt.Sprintf(pick(c.Lang, "This computer is missing %d thing(s) that the kit has. Installing them now.",
				"A esta computadora le faltan %d cosa(s) que el kit tiene. Instalándolas ahora."), n)
			c.Out.Info(first)
		} else {
			c.ensureState(v, false)
		}
		c.ensureSteps(v, true)
	}

	declined := map[string]bool{}
	for round := 0; round < 3; round++ {
		confirm, err := c.ensureConfirmations(v, declined)
		if err != nil {
			return err
		}
		if round > 0 && len(confirm) == 0 {
			break
		}
		if round == 0 && !hasAutomaticWork(v) && len(confirm) == 0 {
			break // nothing this command may start: the state and the next action were shown
		}
		body := map[string]any{}
		if len(confirm) > 0 {
			body["confirm"] = confirm
		}
		started, rawStart, err := post(c.Ctx, body)
		if err != nil {
			return err
		}
		v, raw = started, rawStart
		if v.State == ensure.StateInstalling {
			if v, raw, err = c.ensureFollow(cl, get, v); err != nil {
				return err
			}
		}
		if !c.Global.JSON {
			c.ensureResults(v)
		}
		if v.State == ensure.StateComplete {
			break
		}
		if !c.Term.Interactive() && !c.Global.Yes {
			break
		}
	}
	return c.ensureEnd(v, raw, first)
}

func countAutomatic(v ensure.View) int {
	n := 0
	for _, s := range v.Steps {
		if s.Status == ensure.StatusPending || s.Status == ensure.StatusWaiting {
			n++
		}
	}
	return n
}

func hasStatus(v ensure.View, st ensure.Status) bool {
	for _, s := range v.Steps {
		if s.Status == st {
			return true
		}
	}
	return false
}

// hasAutomaticWork reports whether a pass has a step it may start on its own.
func hasAutomaticWork(v ensure.View) bool {
	for _, s := range v.Steps {
		if s.Status == ensure.StatusPending || s.Status == ensure.StatusWaiting {
			return true
		}
	}
	return false
}

// ensureConfirmations decides which steps that need a confirmation are confirmed: all of them with --yes,
// the ones the person says yes to at a terminal, none otherwise (a script never compiles by accident).
func (c *Call) ensureConfirmations(v ensure.View, declined map[string]bool) ([]string, error) {
	var ids []string
	for _, s := range v.Steps {
		if s.Status != ensure.StatusNeedsConfirm || declined[s.ID] {
			continue
		}
		if !c.Global.Yes && (c.Global.JSON || !c.Term.Interactive()) {
			continue
		}
		ok, err := c.Confirm(ensureConfirmPrompt(c.Lang, s.ID))
		if err != nil {
			return nil, err
		}
		if !ok {
			declined[s.ID] = true
			c.Out.Info(pick(c.Lang, "Skipped: "+s.Title+".", "Omitido: "+s.Title+"."))
			continue
		}
		ids = append(ids, s.ID)
	}
	return ids, nil
}

func ensureConfirmPrompt(l Lang, id string) string {
	switch id {
	case ensure.IDWhisperBuild:
		return pick(l, "Build the speech-to-text program from source? It takes a long time (up to an hour) and uses the processor fully.",
			"¿Compilar el programa de voz a texto desde el código? Tarda mucho (hasta una hora) y usa el procesador a fondo.")
	case ensure.IDRestartKiwix:
		return pick(l, "Restart the library service now? It is unavailable for a few seconds.",
			"¿Reiniciar ahora el servicio de biblioteca? No estará disponible unos segundos.")
	}
	return pick(l, "Do this step now?", "¿Hacer este paso ahora?")
}

// ensureFollow reads the picture every second until the pass ends, telling each new step; Ctrl-C asks the
// agent to stop it (what was installed stays).
func (c *Call) ensureFollow(cl *Client, get func(context.Context) (ensure.View, json.RawMessage, error), v ensure.View) (ensure.View, json.RawMessage, error) {
	last := ""
	tell := func(v ensure.View) {
		if c.Global.JSON || v.State != ensure.StateInstalling {
			return
		}
		line := v.Message
		for _, s := range v.Steps {
			if s.ID == v.Progress.Current {
				line += ": " + s.Title
			}
		}
		if line != last {
			c.Out.Progress(line)
			last = line
		}
	}
	tell(v)
	sig, stopSig := Interrupts()
	defer stopSig()
	tick := time.NewTicker(ensurePoll)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			return c.ensureStop(cl, v)
		case <-c.Ctx.Done():
			return c.ensureStop(cl, v)
		case <-tick.C:
			nv, raw, err := get(c.Ctx)
			if err != nil {
				if c.Ctx.Err() != nil {
					continue
				}
				return v, nil, err
			}
			v = nv
			if v.State != ensure.StateInstalling {
				return v, raw, nil
			}
			tell(v)
		}
	}
}

// ensureStop asks the agent to stop the pass; what was installed stays.
func (c *Call) ensureStop(cl *Client, v ensure.View) (ensure.View, json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, derr := c.callWith(cl, ctx, http.MethodDelete, "/ensure", nil, nil)
	if !c.Global.JSON {
		c.Out.Blank()
		if derr != nil {
			c.Out.Warn(pick(c.Lang, "Ctrl-C: the agent could not be told to stop; run `aictl ensure` to see where it is.",
				"Ctrl-C: no se pudo avisar al agente que se detenga; ejecute `aictl ensure` para ver cómo va."))
		} else {
			c.Out.Info(pick(c.Lang, "Stopped. What was already installed stays; run `aictl ensure` to continue.",
				"Detenido. Lo que ya se instaló se queda; ejecute `aictl ensure` para continuar."))
		}
	}
	return v, nil, ExitWith(ExitInterrupted)
}

// ensureState is the first thing every run shows: is it OK.
func (c *Call) ensureState(v ensure.View, _ bool) {
	switch v.State {
	case ensure.StateComplete:
		c.Out.OK(v.Message)
	case ensure.StateInstalling:
		c.Out.Info(v.Message)
	default:
		c.Out.Warn(v.Message)
	}
}

// ensureNext is the one thing left to do. A step that waits for a confirmation is confirmed here with
// --yes, so the generic "confirm it when you are ready" is made concrete.
func (c *Call) ensureNext(v ensure.View) {
	switch v.NextAction.Kind {
	case ensure.NextConfirm:
		if !c.Global.Yes {
			c.Out.Info(pick(c.Lang, "Run `aictl ensure --yes` to confirm "+stepWord(v, ensure.StatusNeedsConfirm, c.Lang)+".",
				"Ejecute `aictl ensure --yes` para confirmar "+stepWord(v, ensure.StatusNeedsConfirm, c.Lang)+"."))
			return
		}
	case ensure.NextPressButton:
		c.Out.Info(pick(c.Lang, "Run `aictl ensure` to install them.", "Ejecute `aictl ensure` para instalarlos."))
		return
	case ensure.NextFix:
		// The finding above carries the fix of the code; the way to try again is the same command.
		c.Out.Info(pick(c.Lang, "Run `aictl ensure` to try again.", "Ejecute `aictl ensure` para intentar otra vez."))
		return
	case ensure.NextNone, ensure.NextWait:
		return
	}
	if v.NextAction.Text != "" {
		c.Out.Info(v.NextAction.Text)
	}
}

// stepWord names the first step with the status, for a sentence.
func stepWord(v ensure.View, st ensure.Status, l Lang) string {
	for _, s := range v.Steps {
		if s.Status == st {
			return "\"" + s.Title + "\""
		}
	}
	return pick(l, "it", "el paso")
}

// ensureSteps lists what is not in place yet. plain is the heading of a real run; a dry run says what
// the pass would do.
func (c *Call) ensureSteps(v ensure.View, run bool) {
	var todo []ensure.ViewStep
	for _, s := range v.Steps {
		if s.Status != ensure.StatusOK {
			todo = append(todo, s)
		}
	}
	if len(todo) == 0 {
		return
	}
	if run {
		c.Out.Result(pick(c.Lang, "What is missing:", "Lo que falta:"))
	} else {
		c.Out.Result(pick(c.Lang, "What would be installed:", "Lo que se instalaría:"))
	}
	for _, s := range todo {
		c.Out.Result("  " + c.ensureMark(s.Status) + " " + s.Title + ": " + s.Detail)
	}
}

func (c *Call) ensureMark(st ensure.Status) string {
	sym := c.Out.Symbols()
	switch st {
	case ensure.StatusDone, ensure.StatusOK:
		return sym.OK
	case ensure.StatusFailed, ensure.StatusBlocked:
		return sym.Error
	case ensure.StatusNeedsConfirm:
		return sym.Warn
	}
	return sym.Arrow
}

// ensureResults tells what each step of the pass ended as.
func (c *Call) ensureResults(v ensure.View) {
	for _, s := range v.Steps {
		switch s.Status {
		case ensure.StatusDone:
			c.Out.OK(s.Title + ": " + s.Detail)
		case ensure.StatusFailed:
			fix := ""
			if _, f, ok := doctor.Text(s.Code, string(c.Lang)); ok {
				fix = f
			}
			c.Out.Finding(out.Problem{Severity: sevError, Code: s.Code, Message: sentence(s.Title + ": " + s.Error), Fix: fix})
		case ensure.StatusCancelled:
			c.Out.Info(s.Title + ": " + s.Detail)
		}
	}
}

// ensureEnd prints the closing line and returns the exit code.
func (c *Call) ensureEnd(v ensure.View, raw json.RawMessage, first string) error {
	failed := false
	for _, s := range v.Steps {
		if s.Status == ensure.StatusFailed {
			failed = true
		}
	}
	if c.Global.JSON {
		if len(raw) > 0 {
			if err := c.emitRaw(raw); err != nil {
				return err
			}
		} else if err := c.Out.JSON().Document(v); err != nil {
			return err
		}
	} else {
		if v.Message != first {
			c.ensureState(v, false)
		}
		if v.State != ensure.StateComplete {
			c.ensureNext(v)
		}
	}
	switch {
	case failed:
		return ExitWith(ExitErrors)
	case v.State == ensure.StateComplete:
		return nil
	}
	return ExitWith(ExitWarnings)
}
