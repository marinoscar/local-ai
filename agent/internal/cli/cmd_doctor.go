package cli

import (
	"flag"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl doctor [--area X]... [--fast] [--all]` (aictl.md section 4.16): runs the agent's checks
// (GET /doctor) and prints the problems, most serious first, then a summary. It is the one read-only
// command whose exit code carries a result: 0 all fine (info allowed), 8 warnings only, 9 any error
// or critical problem, with --json too.

// areaList is a repeatable --area flag; each value may also hold several areas separated by commas.
type areaList []string

func (a *areaList) String() string { return strings.Join(*a, ",") }

func (a *areaList) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if !areaPattern.MatchString(s) {
			return fmt.Errorf("invalid area %q", s)
		}
		*a = append(*a, strings.ToUpper(s))
	}
	return nil
}

// areaPattern: a doctor area is two to five letters (AGT, NET, HW, LLM, ...). The set of areas is
// open (the agent knows them), so the CLI checks only the shape.
var areaPattern = regexp.MustCompile(`^[A-Za-z]{2,5}$`)

func init() {
	Register(&Command{
		Name:    "doctor",
		Summary: "Run the health checks and explain every problem in plain words.",
		Usage:   "[--area AGT|NET|HW|LLM|CAT|SYN|ARK|SVC|CLD|SEC|...]... [--fast] [--all]",
		Description: `Prints the problems found, most serious first, each with how to fix it, then a summary.
--area limits the checks to one area (repeat it for several). --fast runs only the quick checks.
--all also lists the checks that passed. -v adds the check id and details to every problem.
Exit 0: everything fine. Exit 8: warnings only. Exit 9: errors or critical problems (also with --json).
--json prints the agent's report unchanged.`,
		Timeout: 60 * time.Second,
		Flags: func(fs *flag.FlagSet) {
			fs.Var(new(areaList), "area", "only this area, for example HW or LLM (repeatable)")
			fs.Bool("fast", false, "only the quick checks (a few seconds)")
			fs.Bool("all", false, "also list the checks that passed")
		},
		Run: runDoctor,
	})
}

func runDoctor(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	q := url.Values{}
	if al, ok := c.Flags.Lookup("area").Value.(*areaList); ok {
		for _, a := range *al {
			q.Add("area", a)
		}
	}
	if c.Bool("fast") {
		q.Set("fast", "1")
	}
	path := "/doctor"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rep wireDoctor
	raw, err := roGet(c, path, &rep)
	if err != nil {
		return err
	}
	code := doctorExit(&rep)
	if c.Global.JSON {
		if err := roEmit(c, raw); err != nil {
			return err
		}
	} else {
		renderDoctor(c.Out, c.Global.Verbose, c.Bool("all"), &rep)
	}
	if code != ExitOK {
		return ExitWith(code)
	}
	return nil
}

// doctorExit is 9 for any error or critical problem, 8 for warnings only, else 0. It reads the
// listed results as well as the summary counts, so a report with a missing or zero summary still
// exits right.
func doctorExit(r *wireDoctor) int {
	warn, bad := r.Summary.Warning, r.Summary.Error+r.Summary.Critical
	for _, x := range r.Results {
		switch roSeverity(x.Severity) {
		case out.SevWarning:
			warn++
		case out.SevError, out.SevCritical:
			bad++
		}
	}
	switch {
	case bad > 0:
		return ExitErrors
	case warn > 0:
		return ExitWarnings
	}
	return ExitOK
}

// severityOrder lists severities most serious first, the order doctor prints problems in.
var severityOrder = []out.Severity{out.SevCritical, out.SevError, out.SevWarning, out.SevInfo}

func renderDoctor(p *out.Printer, verbose, all bool, r *wireDoctor) {
	n := 0
	for _, sev := range severityOrder {
		for _, x := range r.Results {
			s := roSeverity(x.Severity)
			if s != sev {
				continue
			}
			n++
			p.Finding(out.Problem{Severity: s, Code: x.Code, Message: sentence(x.Title), Fix: x.Fix, Details: x.Details})
			if verbose && x.Check != "" {
				fmt.Fprintln(p.Stdout(), p.Dim("  check: "+x.Check))
			}
		}
	}
	if all && len(r.Passed) > 0 {
		if n > 0 {
			p.Blank()
		}
		for _, id := range r.Passed {
			p.OK(id)
		}
	}
	line := doctorSummary(p, r)
	if n == 0 {
		p.OK(line + ".")
		return
	}
	p.Blank()
	for _, l := range out.Wrap(line, out.DefaultWidth, "", "") {
		p.Result(l)
	}
}

// doctorSummary is "41 checks passed · 1 warning · 1 error": the passed count first, then every
// severity that occurred, least serious first, as the example of aictl.md section 4.16 reads.
func doctorSummary(p *out.Printer, r *wireDoctor) string {
	lang := p.Lang()
	passed := r.Summary.OK
	if passed == 0 {
		passed = len(r.Passed)
	}
	counts := map[out.Severity]int{
		out.SevInfo: r.Summary.Info, out.SevWarning: r.Summary.Warning,
		out.SevError: r.Summary.Error, out.SevCritical: r.Summary.Critical,
	}
	// Trust the listed results when the summary under-counts them.
	listed := map[out.Severity]int{}
	for _, x := range r.Results {
		listed[roSeverity(x.Severity)]++
	}
	for s, n := range listed {
		if n > counts[s] {
			counts[s] = n
		}
	}
	one := func(n int, en1, enN, es1, esN string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, pick(lang, en1, es1))
		}
		return fmt.Sprintf("%d %s", n, pick(lang, enN, esN))
	}
	parts := []string{one(passed, "check passed", "checks passed", "comprobación superada", "comprobaciones superadas")}
	if n := counts[out.SevInfo]; n > 0 {
		parts = append(parts, one(n, "notice", "notices", "aviso", "avisos"))
	}
	if n := counts[out.SevWarning]; n > 0 {
		parts = append(parts, one(n, "warning", "warnings", "advertencia", "advertencias"))
	}
	if n := counts[out.SevError]; n > 0 {
		parts = append(parts, one(n, "error", "errors", "error", "errores"))
	}
	if n := counts[out.SevCritical]; n > 0 {
		parts = append(parts, one(n, "critical problem", "critical problems", "problema crítico", "problemas críticos"))
	}
	return strings.Join(parts, roDot(p))
}
