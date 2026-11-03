package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// Ark runs (sync, verify, import) share one lifecycle (ark-sync.md sections 3.5, 3.6 and 4.3): a POST
// answers 202 with a run id, the run reports through sync.* events and ends with sync.done, and GET
// /ark/sync is the real state. This file starts a run, follows it, and turns its end into the
// final line and the exit code of aictl.md section 3.4: 0 all items ok (or a skipped scheduled run),
// 8 some items failed or were skipped, 9 the run failed or was stopped.

type runKind string

const (
	kindSync   runKind = "sync"
	kindVerify runKind = "verify"
	kindImport runKind = "import"
)

type arkRunSpec struct {
	kind      runKind
	path      string
	body      map[string]any
	wait      bool
	scheduled bool
}

// syncStart is the answer of POST /ark/sync|verify|import.
type syncStart struct {
	RunID  string `json:"run_id"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// syncTotals is the totals object of a run (ark-sync.md section 3.5).
type syncTotals struct {
	Items      int   `json:"items"`
	Done       int   `json:"done"`
	Skipped    int   `json:"skipped"`
	Failed     int   `json:"failed"`
	BytesTotal int64 `json:"bytes_total"`
	BytesDone  int64 `json:"bytes_done"`
}

// runDoc is how a run ended: the data of sync.done, or GET /ark/sync, or a skip.
type runDoc struct {
	RunID  string            `json:"run_id,omitempty"`
	State  string            `json:"state"`
	Reason string            `json:"reason,omitempty"`
	Totals json.RawMessage   `json:"totals,omitempty"`
	Ready  map[string]string `json:"ready,omitempty"`
}

func (r runDoc) totals() syncTotals {
	var t syncTotals
	_ = json.Unmarshal(r.Totals, &t)
	return t
}

// itemOutcome is what the events said about one item.
type itemOutcome struct {
	progressed bool
	result     string
	reason     string
	code       string
}

var (
	errArkSkipped = errors.New("ark: the run was skipped")
)

// reasonNotInProfile is the reason of an item an import refused because the profile does not list it.
const reasonNotInProfile = "not_in_profile"

func (k runKind) word(l Lang) string {
	switch k {
	case kindVerify:
		return pick(l, "Check", "Comprobación")
	case kindImport:
		return pick(l, "Import", "Importación")
	}
	return pick(l, "Sync", "Sincronización")
}

func (c *Call) arkRun(rs arkRunSpec) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	post := func(ctx context.Context) (syncStart, json.RawMessage, error) {
		var s syncStart
		raw, _, err := c.callWith(cl, ctx, http.MethodPost, rs.path, rs.body, &s)
		return s, raw, err
	}

	if !rs.wait {
		ctx, cancel := c.withDefaultTimeout(30 * time.Second)
		defer cancel()
		s, raw, err := post(ctx)
		if err != nil {
			return err
		}
		if c.Global.JSON {
			return c.emitRaw(raw)
		}
		c.Out.OK(pick(c.Lang,
			fmt.Sprintf("%s started (run %s). It keeps running in the background; follow it with `aictl ark status`.", rs.kind.word(c.Lang), s.RunID),
			fmt.Sprintf("%s iniciada (ejecución %s). Sigue en segundo plano; véala con `aictl ark status`.", rs.kind.word(c.Lang), s.RunID)))
		return nil
	}

	var runID, skipReason string
	var cancelledByUs bool
	outcomes := map[string]*itemOutcome{}
	outcome := func(item string) *itemOutcome {
		o := outcomes[item]
		if o == nil {
			o = &itemOutcome{}
			outcomes[item] = o
		}
		return o
	}
	spec := progress.Spec{
		Types:    []string{"sync.*"},
		AwaitKey: true,
		Match: func(ev progress.Event, key string) bool {
			var d struct {
				RunID string `json:"run_id"`
			}
			return ev.OpID == key && key != "" || json.Unmarshal(ev.Data, &d) == nil && d.RunID == key
		},
		OnEvent: func(ev progress.Event) progress.Step {
			switch ev.Type {
			case agentevents.TypeSyncProgress:
				var d agentevents.SyncProgress
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				outcome(d.Item).progressed = true
				return progress.Step{Update: c.arkProgressUpdate(rs.kind, d)}
			case agentevents.TypeSyncItemDone:
				var d agentevents.SyncItemDone
				if json.Unmarshal(ev.Data, &d) == nil {
					o := outcome(d.Item)
					o.result, o.reason, o.code = d.Result, d.Reason, d.Code
					if rs.scheduled && !c.Global.JSON { // one journal line per item (ark_sync_scheduled.go)
						c.arkItemLine(d, o.progressed)
					}
				}
			case agentevents.TypeSyncDone:
				var d agentevents.SyncDone
				if json.Unmarshal(ev.Data, &d) != nil {
					return progress.Step{}
				}
				return progress.Step{Done: true, Result: runDoc{RunID: d.RunID, State: d.State, Totals: d.Totals, Ready: d.Ready}}
			}
			return progress.Step{}
		},
		// GET /ark/sync is the truth after a lost stream: the run that ended while we were away.
		Reread: func(ctx context.Context) (progress.Step, error) {
			if runID == "" {
				return progress.Step{}, nil
			}
			var r runDoc
			if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/ark/sync", nil, &r); err != nil {
				return progress.Step{}, err
			}
			switch r.State {
			case "done", "cancelled", "failed", "skipped":
				if r.RunID == runID {
					return progress.Step{Done: true, Result: r}, nil
				}
			}
			return progress.Step{}, nil
		},
	}
	fs := FollowSpec{
		Progress: spec,
		Request: func(ctx context.Context) (string, error) {
			s, _, err := post(ctx)
			if err != nil {
				if reason := c.skippableRefusal(rs, err); reason != "" {
					skipReason = reason
					return "", errArkSkipped
				}
				return "", err
			}
			runID = s.RunID
			if s.State == "skipped" {
				skipReason = orText(s.Reason, "skipped")
				return "", errArkSkipped
			}
			return s.RunID, nil
		},
	}
	switch {
	case rs.kind == kindSync && rs.scheduled:
		// Under systemd: cancel at once, then wait for the stop for at most scheduledStopGrace.
		guard, stopGuard := newStopGuard(scheduledStopGrace)
		defer stopGuard()
		fs.Interrupt = guard.out
		fs.OnInterrupt = func(ctx context.Context) progress.InterruptAction {
			return c.scheduledInterrupt(ctx, cl, guard, &cancelledByUs)
		}
	case rs.kind == kindSync:
		fs.OnInterrupt = func(ctx context.Context) progress.InterruptAction {
			return c.askToCancelSync(ctx, cl, false, &cancelledByUs)
		}
	}
	res, ferr := c.Follow(fs)
	if errors.Is(ferr, errArkSkipped) {
		return c.arkSkipped(rs, cl, runDoc{RunID: runID, State: "skipped", Reason: skipReason})
	}
	if ferr != nil {
		return ferr
	}
	doc, _ := res.Value.(runDoc)
	if doc.State == "skipped" {
		if doc.Reason == "" { // the event does not carry the reason; the run does
			var r runDoc
			if _, _, err := c.callWith(cl, c.Ctx, http.MethodGet, "/ark/sync", nil, &r); err == nil && r.RunID == doc.RunID {
				doc.Reason = r.Reason
			}
		}
		return c.arkSkipped(rs, cl, doc)
	}
	return c.arkFinished(rs, doc, outcomes, cancelledByUs)
}

// skippableRefusal says why a --scheduled run was refused instead of started: the node is in
// survival or offline mode, or another run holds the slot. Those are skips, not failures: the timer
// must not fail its unit for them (aictl.md section 3.4). A manual run gets the agent's error.
func (c *Call) skippableRefusal(rs arkRunSpec, err error) string {
	var ae *APIError
	if !rs.scheduled || !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		return ""
	}
	switch ae.Code {
	case doctor.SYN010, doctor.SYN011:
		return "mode"
	case doctor.SYN005:
		return "busy"
	}
	return ""
}

// skipText is the reason of a skipped run in a sentence.
func (c *Call) skipText(cl *Client, reason string) string {
	l := c.Lang
	switch reason {
	case "mode":
		mode := "offline"
		ctx, cancel := context.WithTimeout(c.Ctx, 3*time.Second)
		defer cancel()
		if h, err := cl.Health(ctx); err == nil && h.Mode != "" {
			mode = h.Mode
		}
		return pick(l, "the node is in "+mode+" mode", "el nodo está en modo "+modeName(l, mode))
	case "offline":
		return pick(l, "offline", "sin conexión")
	case "battery":
		return pick(l, "the computer is running on battery", "el equipo funciona con batería")
	case "disabled":
		return pick(l, "it is turned off (sync.enabled is false)", "está desactivada (sync.enabled es false)")
	case "no_roots":
		return pick(l, "there is no place to store the content", "no hay dónde guardar el contenido")
	case "busy":
		return pick(l, "another sync is already running", "ya hay otra sincronización en curso")
	}
	return reason
}

// arkSkipped reports a run that did not start for a good reason: exit 0, never exit 3.
func (c *Call) arkSkipped(rs arkRunSpec, cl *Client, doc runDoc) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(doc)
	}
	c.Out.Info(pick(c.Lang,
		rs.kind.word(c.Lang)+" skipped: "+c.skipText(cl, doc.Reason)+".",
		rs.kind.word(c.Lang)+" omitida: "+c.skipText(cl, doc.Reason)+"."))
	return nil
}

// askToCancelSync is the interrupt hook of a followed sync: Ctrl-C asks "Cancel the sync too?" and a
// yes sends DELETE /ark/sync. Under systemd (--scheduled, or no terminal) a stop is a SIGTERM and
// there is nobody to ask: the cancel goes out at once. In both cases the wait carries on until the
// run reports that it stopped.
//
// ctx is the hook's: the process context was cancelled by the first Ctrl-C, so the answer is read
// and the request sent with the context Follow hands over.
func (c *Call) askToCancelSync(ctx context.Context, cl *Client, scheduled bool, cancelled *bool) progress.InterruptAction {
	if !scheduled && !c.Global.Yes {
		if c.Global.JSON || !c.Term.Interactive() {
			return progress.Leave
		}
		fmt.Fprint(c.Stderr, pick(c.Lang, "Cancel the sync too?", "¿Cancelar también la sincronización?")+pick(c.Lang, " [y/N] ", " [s/N] "))
		line, err := c.Term.ReadLine(ctx)
		if err != nil {
			fmt.Fprintln(c.Stderr)
			return progress.Leave
		}
		if !isYes(c.Lang, line) {
			return progress.Leave
		}
	}
	if _, err := cl.Do(ctx, http.MethodDelete, "/ark/sync", nil, nil); err != nil {
		return progress.Leave
	}
	*cancelled = true
	return progress.KeepWaiting
}

// arkProgressUpdate words a sync.progress event: "Downloading <item>" with the bar and, after it,
// "4.9/12.0 GB  18 MB/s  6 min left".
func (c *Call) arkProgressUpdate(kind runKind, d agentevents.SyncProgress) *progress.Update {
	verb := pick(c.Lang, "Downloading", "Descargando")
	switch {
	case strings.Contains(d.State, "verif"):
		verb = pick(c.Lang, "Checking", "Comprobando")
	case kind == kindVerify:
		verb = pick(c.Lang, "Checking", "Comprobando")
	case kind == kindImport:
		verb = pick(c.Lang, "Copying", "Copiando")
	}
	u := &progress.Update{Label: verb + " " + d.Item, Key: d.Root + "/" + d.Item + "/" + verb, Percent: progress.NoBar}
	if d.BytesTotal > 0 {
		u.Percent = float64(d.BytesDone) / float64(d.BytesTotal) * 100
		done, total := sizeText(d.BytesDone), sizeText(d.BytesTotal)
		if i := strings.LastIndex(total, " "); i > 0 && strings.HasSuffix(done, total[i:]) {
			done = strings.TrimSuffix(done, total[i:]) // "4.9/12.0 GB", not "4.9 GB/12.0 GB"
		}
		parts := []string{done + "/" + total}
		if d.RateBPS > 0 {
			parts = append(parts, sizeText(d.RateBPS)+"/s")
			if d.BytesDone < d.BytesTotal {
				eta := time.Duration(float64(d.BytesTotal-d.BytesDone)/float64(d.RateBPS)) * time.Second
				parts = append(parts, out.Duration(eta)+pick(c.Lang, " left", " restantes"))
			}
		}
		u.Detail = strings.Join(parts, "  ")
	}
	return u
}

// arkFinished prints how a followed run ended and returns the exit code of aictl.md section 3.4.
func (c *Call) arkFinished(rs arkRunSpec, doc runDoc, outcomes map[string]*itemOutcome, cancelledByUs bool) error {
	t := doc.totals()
	downloaded, upToDate, failed := 0, 0, t.Failed
	for _, o := range outcomes {
		switch o.result {
		case "verified":
			if o.progressed {
				downloaded++
			}
		case "failed", "quarantined":
			if t.Failed == 0 {
				failed++
			}
		}
	}
	if t.Done > downloaded {
		upToDate = t.Done - downloaded
	}
	skipped := t.Skipped
	if c.Global.JSON {
		if err := c.Out.JSON().Document(doc); err != nil {
			return err
		}
	}

	var line string
	switch rs.kind {
	case kindVerify:
		line = pick(c.Lang,
			fmt.Sprintf("Check finished: %d verified, %d with problems.", t.Done, failed),
			fmt.Sprintf("Comprobación terminada: %d verificados, %d con problemas.", t.Done, failed))
	case kindImport:
		line = pick(c.Lang,
			fmt.Sprintf("Import finished: %d copied, %d already here, %d failed.", downloaded, upToDate, failed),
			fmt.Sprintf("Importación terminada: %d copiados, %d ya estaban, %d con error.", downloaded, upToDate, failed))
	default:
		line = pick(c.Lang,
			fmt.Sprintf("Sync finished: %d downloaded, %d already up to date, %d failed.", downloaded, upToDate, failed),
			fmt.Sprintf("Sincronización terminada: %d descargados, %d ya estaban al día, %d con error.", downloaded, upToDate, failed))
	}
	if skipped > 0 {
		line = strings.TrimSuffix(line, ".") + pick(c.Lang, fmt.Sprintf(", %d skipped.", skipped), fmt.Sprintf(", %d omitidos.", skipped))
	}
	ready := c.readyText(doc.Ready)

	switch doc.State {
	case "done":
		if failed == 0 && skipped == 0 {
			if !c.Global.JSON {
				c.Out.OK(strings.TrimSpace(line + " " + ready))
			}
			return nil
		}
		if !c.Global.JSON {
			c.Out.Result(c.Out.Symbols().Warn + " " + strings.TrimSpace(line+" "+ready))
			c.itemProblems(outcomes)
		}
		return ExitWith(ExitWarnings)
	case "cancelled":
		if !c.Global.JSON {
			c.Out.Info(pick(c.Lang,
				rs.kind.word(c.Lang)+" stopped. Partial downloads are kept and continue next time.",
				rs.kind.word(c.Lang)+" detenida. Las descargas parciales se conservan y continúan la próxima vez."))
		}
		if cancelledByUs {
			return ExitWith(ExitInterrupted)
		}
		return ExitWith(ExitErrors)
	}
	if !c.Global.JSON {
		c.itemProblems(outcomes)
		c.Out.Problem(out.Problem{Severity: out.SevError, Message: pick(c.Lang,
			rs.kind.word(c.Lang)+" did not finish. Run `aictl doctor` to see why.",
			rs.kind.word(c.Lang)+" no terminó. Ejecute `aictl doctor` para ver por qué.")})
	}
	return ExitWith(ExitErrors)
}

// itemProblems lists what went wrong per item, with the doctor code and its fix (at most ten).
func (c *Call) itemProblems(outcomes map[string]*itemOutcome) {
	var ids []string
	for id, o := range outcomes {
		if o.result == "failed" || o.result == "quarantined" || o.result == "skipped" && (o.code != "" || o.reason == reasonNotInProfile) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for i, id := range ids {
		if i == 10 {
			c.Out.Warn(pick(c.Lang, fmt.Sprintf("... and %d more. See `aictl ark status`.", len(ids)-10), fmt.Sprintf("... y %d más. Vea `aictl ark status`.", len(ids)-10)))
			break
		}
		o := outcomes[id]
		if o.reason == reasonNotInProfile {
			// An import was asked for an item this computer's profile does not list (ark-sync.md section 4.14).
			c.Out.Problem(out.Problem{Severity: out.SevWarning, Message: id + ": " + pick(c.Lang,
				"It is not in this computer's profile, so it was not copied. Run the import again with --add-to-profile to add it.",
				"No está en el perfil de este equipo, así que no se copió. Ejecute la importación de nuevo con --add-to-profile para añadirlo.")})
			continue
		}
		p := c.codeProblem(o.code, orText(o.reason, o.result))
		if o.code == "" {
			p.Code, p.Fix = "", ""
			p.Message = sentence(orText(o.reason, o.result))
		}
		p.Message = id + ": " + p.Message
		c.Out.Problem(p)
	}
}

// readyText is the one-sentence state of each root after the run.
func (c *Call) readyText(ready map[string]string) string {
	var ids []string
	for id := range ready {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var parts []string
	for _, id := range ids {
		name := pick(c.Lang, "Internal disk", "El disco interno")
		if id != "internal" {
			name = pick(c.Lang, "Ark drive "+id, "La unidad del arca "+id)
		}
		switch ready[id] {
		case "green":
			parts = append(parts, name+pick(c.Lang, " is ready.", " está listo."))
		case "yellow":
			parts = append(parts, name+pick(c.Lang, " is usable but needs attention (yellow).", " se puede usar pero necesita atención (amarillo)."))
		case "red":
			parts = append(parts, name+pick(c.Lang, " is not ready (red): run `aictl ark status`.", " no está listo (rojo): ejecute `aictl ark status`."))
		}
	}
	return strings.Join(parts, " ")
}
