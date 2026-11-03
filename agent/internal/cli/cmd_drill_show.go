package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl drill show [<run-id>]` and `aictl drill record-manual` (aictl.md section 4.17), and the renderer
// of a results document that `aictl drill` uses too (drill.md section 3.4).

// maxManualNotes is the longest note the agent keeps, in characters (drill.md section 3.1).
const maxManualNotes = 2000

// drillDoc is the results document of drill.md section 3.4, as far as aictl reads it. The rest of the
// document is for --json, which prints the agent's body unchanged.
type drillDoc struct {
	SchemaVersion int          `json:"schema_version"`
	RunID         string       `json:"run_id"`
	Trigger       string       `json:"trigger"`
	NodeID        string       `json:"node_id"`
	Quick         bool         `json:"quick"`
	StartedAt     string       `json:"started_at"`
	FinishedAt    *string      `json:"finished_at"`
	State         string       `json:"state"`
	Overall       string       `json:"overall"`
	SurvivalReady bool         `json:"survival_ready"`
	Counts        drillCounts  `json:"counts"`
	Checks        []drillCheck `json:"checks"`
	Restored      *struct {
		OK *bool `json:"ok"`
	} `json:"restored"`
	NetworkFence *struct {
		BlockedDials int `json:"blocked_dials"`
	} `json:"network_fence"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Notes  string `json:"notes"`
}

type drillCounts struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// drillCheck is one line of checks[] (drill.md section 3.4).
type drillCheck struct {
	ID     string `json:"id"`
	Area   string `json:"area"`
	Result string `json:"result"`
	Code   string `json:"code"`
	MS     int64  `json:"ms"`
}

// ended reports whether the run is over: it finished its checks (no state) or was aborted.
func (d drillDoc) ended() bool {
	switch {
	case d.State == "aborted":
		return true
	case d.State != "" || d.FinishedAt == nil:
		return false
	}
	switch d.Overall {
	case "pass", "warn", "fail":
		return true
	}
	return false
}

// counts returns the counts of the document, from checks[] when the document carries none.
func (d drillDoc) counts() drillCounts {
	if d.Counts != (drillCounts{}) || len(d.Checks) == 0 {
		return d.Counts
	}
	var n drillCounts
	for _, ch := range d.Checks {
		switch ch.Result {
		case "pass":
			n.Pass++
		case "warn":
			n.Warn++
		case "fail":
			n.Fail++
		default:
			n.Skip++
		}
	}
	return n
}

// drillExit is the exit code of a finished drill (aictl.md section 3.4): 0 pass, 8 warn, 9 fail or
// aborted. A skipped run, one still running and a manual record that passed are 0.
func drillExit(overall string) error {
	switch overall {
	case "warn":
		return ExitWith(ExitWarnings)
	case "fail", "aborted":
		return ExitWith(ExitErrors)
	}
	return nil
}

func init() {
	attachAll(
		at(&Command{
			Name:    "show",
			Summary: "Show the results of the last drill (or of one run).",
			Usage:   "[<run-id>]",
			Description: `Prints when the drill ran and how it ended, then each check that did not pass with its code and what
to do. Without a run id it shows the last drill. --json prints the results document unchanged.
Exit 0: passed. Exit 8: passed with warnings. Exit 9: failed. Exit 5: no such run, or no drill has run.`,
			Timeout: readOnlyTimeout,
			Run: func(c *Call) error {
				if len(c.Args) > 1 {
					return c.exactArgs(1, "[<run-id>]")
				}
				id := ""
				if len(c.Args) == 1 {
					id = c.Args[0]
				}
				return c.drillShow(id)
			},
		}, "drill"),
		at(&Command{
			Name:    "record-manual",
			Summary: "Record the quarterly \"pull the plug\" drill you did by hand.",
			Usage:   "--result pass|fail [--notes \"<text>\"] [--at <time>]",
			Description: `Stores the result of the manual drill (survival runbook, appendix, Part B) on this node. --notes is
optional text of up to 2000 characters; --at is when you did it (RFC 3339, for example
2026-11-02T15:00:00Z; the default is now). Needs the admin PIN unless you are root.`,
			Marks:   MarkAdmin,
			Timeout: 30 * time.Second,
			Flags: func(fs *flag.FlagSet) {
				fs.String("result", "", "pass or fail (required)")
				fs.String("notes", "", "what you saw (up to 2000 characters; stays on this node)")
				fs.String("at", "", "when you did it, as an RFC 3339 time (default: now)")
			},
			Run: drillRecordManual,
		}, "drill"),
	)
}

// drillShow is `aictl drill show` and `aictl drill --last`.
func (c *Call) drillShow(runID string) error {
	path := "/drill"
	if runID != "" {
		if !drillIDRe.MatchString(runID) {
			return c.usageErr(
				fmt.Sprintf(pick(c.Lang, "%q is not a drill run id.", "%q no es un id de ejecución del simulacro."), runID),
				pick(c.Lang, "A run id looks like drl-20261101T043512Z-9c1e. Run `aictl drill show` for the last one.",
					"Un id de ejecución es como drl-20261101T043512Z-9c1e. Ejecute `aictl drill show` para ver el último."))
		}
		path += "?run_id=" + url.QueryEscape(runID)
	}
	var doc drillDoc
	raw, _, err := c.call(http.MethodGet, path, nil, &doc)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		if err := c.emitRaw(raw); err != nil {
			return err
		}
	} else {
		c.drillReport(doc, drillReportShow)
	}
	return drillExit(doc.Overall)
}

// drillRecordManual is `aictl drill record-manual`: POST /drill/manual (admin).
func drillRecordManual(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	result := c.String("result")
	switch result {
	case "pass", "fail":
	case "":
		return c.usageErr(pick(c.Lang, "Say how the drill went: --result pass or --result fail.", "Indique cómo salió el simulacro: --result pass o --result fail."),
			pick(c.Lang, "For example: `aictl drill record-manual --result pass --notes \"All good\"`.",
				"Por ejemplo: `aictl drill record-manual --result pass --notes \"Todo bien\"`."))
	default:
		return c.usageErr(fmt.Sprintf(pick(c.Lang, "--result is pass or fail, not %q.", "--result es pass o fail, no %q."), result),
			pick(c.Lang, "Use --result pass or --result fail.", "Use --result pass o --result fail."))
	}
	body := map[string]any{"result": result}
	if notes := c.String("notes"); notes != "" {
		if utf8.RuneCountInString(notes) > maxManualNotes {
			return c.usageErr(
				fmt.Sprintf(pick(c.Lang, "The notes are longer than %d characters.", "Las notas tienen más de %d caracteres."), maxManualNotes),
				pick(c.Lang, "Shorten them; the paper log can hold the rest.", "Acórtelas; el registro en papel puede guardar el resto."))
		}
		if !utf8.ValidString(notes) || strings.IndexFunc(notes, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
			return c.usageErr(pick(c.Lang, "The notes hold a character that cannot be stored.", "Las notas tienen un carácter que no se puede guardar."),
				pick(c.Lang, "Use plain text; line breaks and tabs are fine.", "Use texto simple; los saltos de línea y las tabulaciones están permitidos."))
		}
		body["notes"] = notes
	}
	if at := c.String("at"); at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return c.usageErr(fmt.Sprintf(pick(c.Lang, "%q is not an RFC 3339 time.", "%q no es una hora RFC 3339."), at),
				pick(c.Lang, "Write it like 2026-11-02T15:00:00Z.", "Escríbala como 2026-11-02T15:00:00Z."))
		}
		body["at"] = t.UTC().Format(time.RFC3339)
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	var a drillAccepted
	var raw json.RawMessage
	err = c.Admin(func() error {
		var e error
		raw, _, e = c.callWith(cl, c.Ctx, http.MethodPost, "/drill/manual", body, &a)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(fmt.Sprintf(pick(c.Lang,
		"The manual drill was recorded as %s (run %s).",
		"El simulacro manual se registró como %s (ejecución %s)."), resultWord(c.Lang, result), a.RunID))
	return nil
}

func resultWord(l Lang, result string) string {
	if result == "pass" {
		return pick(l, "passed", "aprobado")
	}
	return pick(l, "failed", "fallido")
}

// ---- the report -------------------------------------------------------------------------------------

type drillReportMode int

const (
	// drillReportRun lists every check, then the verdict: the end of `aictl drill`.
	drillReportRun drillReportMode = iota
	// drillReportShow is the verdict first, then the checks that did not pass: `aictl drill show`.
	drillReportShow
)

// drillReport prints a results document in human mode.
func (c *Call) drillReport(doc drillDoc, mode drillReportMode) {
	if mode == drillReportShow {
		c.drillVerdict(doc, true)
	}
	for _, ch := range doc.Checks {
		label := drillCheckLabel(c.Lang, ch.ID)
		switch ch.Result {
		case "pass":
			if mode == drillReportRun {
				line := label
				if ch.MS >= 1000 {
					line += " " + roDot(c.Out) + " " + out.Duration(time.Duration(ch.MS)*time.Millisecond)
				}
				c.Out.OK(line)
			}
		case "skip":
			if mode == drillReportRun {
				c.Out.Info(label + pick(c.Lang, " (skipped)", " (omitido)"))
			}
		default:
			c.Out.Finding(c.drillCheckProblem(ch, label))
		}
	}
	if doc.Restored != nil && doc.Restored.OK != nil && !*doc.Restored.OK {
		c.Out.Finding(c.drillRunProblem(doc, doctor.DRL090, out.SevError))
	}
	if doc.NetworkFence != nil && doc.NetworkFence.BlockedDials > 0 {
		c.Out.Finding(c.drillRunProblem(doc, doctor.DRL091, out.SevWarning))
	}
	if mode == drillReportRun {
		c.drillVerdict(doc, false)
	}
}

// drillCheckProblem is the problem layout of a check that warned or failed: its name, what the code
// says, the code and the fix.
func (c *Call) drillCheckProblem(ch drillCheck, label string) out.Problem {
	p := c.codeProblem(ch.Code, "")
	title := p.Message
	if ch.Code == "" || title == "" {
		title = pick(c.Lang, "did not pass", "no pasó")
		if ch.Result == "warn" {
			title = pick(c.Lang, "passed with a warning", "pasó con un aviso")
		}
	}
	p.Message = sentence(label + ": " + title)
	p.Severity = out.SevError
	if ch.Result == "warn" {
		p.Severity = out.SevWarning
	}
	if title != "" && ch.Code == "" {
		p.Code, p.Fix = "", ""
	}
	return p
}

// drillRunProblem is a problem of the whole run: the restore failed (LAI-DRL-090) or something tried
// to use the internet (LAI-DRL-091).
func (c *Call) drillRunProblem(doc drillDoc, code string, sev out.Severity) out.Problem {
	p := c.codeProblem(code, "")
	p.Severity = sev
	return p
}

// drillVerdict prints how the run ended: one line, then (for a drill that completed) whether survival
// mode is ready. In show mode it names the run ("The drill of 3 days ago (scheduled) ...").
func (c *Call) drillVerdict(doc drillDoc, show bool) {
	sym := c.Out.Symbols()
	l := c.Lang
	subject := pick(l, "Drill", "El simulacro")
	if show {
		when := ""
		for _, s := range []string{deref(doc.FinishedAt), doc.StartedAt} {
			if t, ok := roWhen(s); ok && when == "" {
				when = c.Out.When(t)
			}
		}
		trig := drillTrigger(l, doc.Trigger)
		if doc.Trigger == "manual_plug" {
			c.drillManualVerdict(doc, when)
			return
		}
		switch {
		case when != "" && trig != "":
			subject = fmt.Sprintf(pick(l, "The drill of %s (%s)", "El simulacro de %s (%s)"), when, trig)
		case when != "":
			subject = fmt.Sprintf(pick(l, "The drill of %s", "El simulacro de %s"), when)
		}
	}
	n := doc.counts()
	problems := n.Fail
	if problems == 0 && doc.Overall == "fail" {
		problems = 1 // a run-level failure (the restore) with no failing check
	}
	var symbol, text string
	switch {
	case doc.State == "aborted" || doc.Overall == "aborted":
		symbol = sym.Info
		text = pick(l, subject+" was stopped before it finished.", subject+" se detuvo antes de terminar.")
		if r := drillReason(doc); r != "" {
			text = strings.TrimSuffix(text, ".") + ": " + r + "."
		}
	case doc.State == "skipped" || doc.Overall == "skipped":
		symbol = sym.Info
		text = pick(l, subject+" was skipped.", subject+" se omitió.")
		if r := drillReason(doc); r != "" {
			text = strings.TrimSuffix(text, ".") + ": " + r + "."
		}
	case doc.State == "running" || doc.Overall == "running":
		symbol = sym.Progress
		text = pick(l, subject+" is still running.", subject+" sigue en curso.")
	case doc.Overall == "pass":
		symbol = sym.OK
		text = pick(l, subject+" passed.", subject+" pasó.")
	case doc.Overall == "warn":
		symbol = sym.Warn
		text = fmt.Sprintf(pick(l, "%s passed with %s.", "%s pasó con %s."), subject, countWords(l, n.Warn, "warning", "warnings", "aviso", "avisos"))
	case doc.Overall == "fail":
		symbol = sym.Error
		text = fmt.Sprintf(pick(l, "%s failed: %s did not pass.", "%s falló: %s no pasó."), subject, countWords(l, problems, "check", "checks", "comprobación", "comprobaciones"))
		if l == LangES && problems != 1 {
			text = strings.Replace(text, "no pasó.", "no pasaron.", 1)
		}
	default:
		symbol = sym.Info
		text = pick(l, subject+" ended: "+doc.Overall+".", subject+" terminó: "+doc.Overall+".")
	}
	c.Out.Result(symbol + " " + text)
	if doc.ended() && doc.State == "" && (doc.Overall == "pass" || doc.Overall == "warn") {
		if doc.SurvivalReady {
			c.Out.OK(pick(l, "Survival mode is ready on this node.", "El modo supervivencia está listo en este nodo."))
		} else {
			c.Out.Info(pick(l,
				"This drill did not cover everything survival mode needs (a partial run, or a critical check did not run).",
				"Este simulacro no cubrió todo lo que necesita el modo supervivencia (fue parcial, o no se ejecutó una comprobación crítica)."))
		}
	}
}

// drillManualVerdict is the line of a "pull the plug" record: it has no checks, only a result and the
// notes the person wrote.
func (c *Call) drillManualVerdict(doc drillDoc, when string) {
	l := c.Lang
	text := fmt.Sprintf(pick(l, "The manual \"pull the plug\" drill of %s was recorded as %s.", "El simulacro manual de \"desenchufar\" de %s se registró como %s."),
		orText(when, pick(l, "an unknown time", "una hora desconocida")), resultWord(l, doc.Overall))
	if doc.Overall == "pass" {
		c.Out.OK(text)
	} else {
		c.Out.Result(c.Out.Symbols().Error + " " + text)
	}
	if doc.Notes != "" {
		c.Out.Secondary(pick(l, "Notes: ", "Notas: ") + strings.TrimSpace(doc.Notes))
	}
}

// drillReason is the plain reason of a skipped or aborted run: the doctor title of its code in the
// user's language, else the agent's own sentence.
func drillReason(doc drillDoc) string {
	return strings.TrimSuffix(strings.TrimSpace(doc.Reason), ".")
}

func drillTrigger(l Lang, t string) string {
	switch t {
	case "scheduled":
		return pick(l, "scheduled", "programado")
	case "manual":
		return pick(l, "manual", "manual")
	case "cloud":
		return pick(l, "from the cloud", "desde la nube")
	}
	return ""
}

// countWords is "1 warning" or "3 warnings".
func countWords(l Lang, n int, oneEN, manyEN, oneES, manyES string) string {
	word := pick(l, manyEN, manyES)
	if n == 1 {
		word = pick(l, oneEN, oneES)
	}
	return fmt.Sprintf("%d %s", n, word)
}
