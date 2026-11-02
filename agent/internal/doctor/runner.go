package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// RunOptions selects and bounds a run.
type RunOptions struct {
	// Areas keeps only the checks whose Area is listed (the repeatable `?area=`). Empty means all.
	// Case-insensitive; an area that is not one of Areas fails the run with ErrInvalidArea.
	Areas []string
	// FastOnly keeps only the checks marked Fast.
	FastOnly bool
	// Budget bounds the whole run when greater than zero (`?fast=1` uses FastBudget). A check still
	// running when it ends is abandoned like one that timed out.
	Budget time.Duration
	// Lang is the language of the code texts, "en" (default) or "es".
	Lang string
	// Now is the clock of RanAt and DurationMS; default time.Now.
	Now func() time.Time
}

// Summary counts a run (agent.md section 3.8.27). OK is the number of checks that passed (they are
// listed in Report.Passed); the others count the problem results by severity.
type Summary struct {
	OK       int `json:"ok"`
	Info     int `json:"info"`
	Warning  int `json:"warning"`
	Error    int `json:"error"`
	Critical int `json:"critical"`
}

// Report is the outcome of a run and the body of GET /doctor.
type Report struct {
	RanAt      time.Time `json:"ran_at"`
	DurationMS int64     `json:"duration_ms"`
	Summary    Summary   `json:"summary"`
	// Results holds only the problems, worst first (severity, then check id, then code).
	Results []Result `json:"results"`
	// Passed lists the checks that found nothing, sorted by id.
	Passed []string `json:"passed"`
}

// Run runs the selected checks, at most MaxConcurrent at a time, each under its own timeout, and
// returns the report. A check that times out, panics or returns a code outside the table yields one
// LAI-AGT-099 result with details.check and details.reason; the other checks still run, and the run
// never fails as a whole (only a bad area is an error).
func (r *Registry) Run(ctx context.Context, opts RunOptions) (Report, error) {
	areas := map[string]bool{}
	for _, a := range opts.Areas {
		a = strings.ToUpper(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if !ValidArea(a) {
			return Report{}, fmt.Errorf("%w: %q", ErrInvalidArea, a)
		}
		areas[a] = true
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	start := now()

	var selected []Check
	for _, c := range r.Checks() {
		if len(areas) > 0 && !areas[c.Area] {
			continue
		}
		if opts.FastOnly && !c.Fast {
			continue
		}
		selected = append(selected, c)
	}

	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if opts.Budget > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Budget)
	}
	defer cancel()

	raw := make([][]Result, len(selected))
	sem := make(chan struct{}, MaxConcurrent)
	var wg sync.WaitGroup
	for i, c := range selected {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				raw[i] = []Result{problem(c.ID, "timeout")}
				return
			}
			defer func() { <-sem }()
			raw[i] = r.runOne(runCtx, c)
		}(i, c)
	}
	wg.Wait()

	rep := Report{Results: []Result{}, Passed: []string{}}
	for i, c := range selected {
		problems := 0
		for _, res := range normalize(c.ID, raw[i], opts.Lang) {
			problems++
			rep.Results = append(rep.Results, res)
			switch res.Severity {
			case Info:
				rep.Summary.Info++
			case Warning:
				rep.Summary.Warning++
			case Error:
				rep.Summary.Error++
			case Critical:
				rep.Summary.Critical++
			}
		}
		if problems == 0 {
			rep.Passed = append(rep.Passed, c.ID)
		}
	}
	rep.Summary.OK = len(rep.Passed)
	sort.Strings(rep.Passed)
	sort.SliceStable(rep.Results, func(i, j int) bool {
		a, b := rep.Results[i], rep.Results[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Code < b.Code
	})
	end := now()
	rep.RanAt = start.UTC().Truncate(time.Second)
	rep.DurationMS = end.Sub(start).Milliseconds()
	return rep, nil
}

// runOne runs one check in its own goroutine under its timeout. The goroutine is abandoned when
// the timeout passes; the registry remembers it as running, so a check that ignores its context is
// never started a second time while the first run is stuck (it keeps reporting LAI-AGT-099).
func (r *Registry) runOne(ctx context.Context, c Check) []Result {
	r.mu.Lock()
	if r.running[c.ID] {
		r.mu.Unlock()
		return []Result{problem(c.ID, "timeout")}
	}
	r.running[c.ID] = true
	r.mu.Unlock()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan []Result, 1)
	go func() {
		res := safeRun(cctx, c)
		// Unmark the check BEFORE the result is delivered. The caller may start the next run the
		// moment it has the result; if the mark were still set it would take a finished check for a
		// stuck one and report LAI-AGT-099 (#207). An abandoned run unmarks itself here when it
		// finally returns.
		r.mu.Lock()
		delete(r.running, c.ID)
		r.mu.Unlock()
		done <- res
	}()

	select {
	case res := <-done:
		return res
	case <-cctx.Done():
		reason := "timeout"
		if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
			reason = "canceled"
		}
		return []Result{problem(c.ID, reason)}
	}
}

// safeRun runs c and turns a panic into the LAI-AGT-099 result of a panicking check.
func safeRun(ctx context.Context, c Check) (res []Result) {
	defer func() {
		if p := recover(); p != nil {
			res = []Result{problem(c.ID, "panic: "+firstLine(fmt.Sprint(p)))}
		}
	}()
	return c.Run(ctx)
}

// problem is the LAI-AGT-099 result of a check that did not finish properly.
func problem(check, reason string) Result {
	return Result{Check: check, Code: AGT099, Details: map[string]any{"check": check, "reason": reason}}
}

// firstLine returns the first line of s, cut to 200 characters.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > 200 {
		s = string([]rune(s)[:200])
	}
	return s
}

// normalize turns what a check returned into the problem results of the report: ok results are
// dropped; the check id is set; a code outside the table (or retired) becomes LAI-AGT-099; the
// severity defaults to the code's; title and fix come from the table in lang unless the check set
// its own; the details map is copied.
func normalize(check string, in []Result, lang string) []Result {
	var out []Result
	for _, r := range in {
		if r.IsOK() {
			continue
		}
		r.Check = check
		if !Emittable(r.Code) {
			r = Result{Check: check, Code: AGT099, Details: map[string]any{"check": check, "reason": "unknown code " + firstLine(r.Code)}}
		}
		c, _ := Lookup(r.Code)
		if !r.Severity.Valid() {
			r.Severity = c.Severity
		}
		if r.Title == "" {
			r.Title = c.Title.In(lang)
		}
		if r.Fix == "" {
			r.Fix = c.Fix.In(lang)
		}
		if len(r.Details) > 0 {
			d := make(map[string]any, len(r.Details))
			for k, v := range r.Details {
				d[k] = v
			}
			r.Details = d
		} else {
			r.Details = nil
		}
		out = append(out, r)
	}
	return out
}
