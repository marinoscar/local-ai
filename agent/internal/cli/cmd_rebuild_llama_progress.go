package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// The compile progress of `aictl rebuild-llama` (issue #433, aictl.md section 4.23): the engine passes
// the output of `cmake --build` on as it is written (build.Request.Output), ninja writes one
// "[n/N] Building CXX object …" line per step, and this file turns those lines into the step's name,
// the percentage done and an estimate of the time left. On a terminal that is one updating line on
// stderr; elsewhere a plain line at every tenth of the build; with --json or --quiet nothing.

var (
	// ninjaLineRe is ninja's status line "[n/N] description".
	ninjaLineRe = regexp.MustCompile(`^\[(\d+)/(\d+)\]\s+(.*)$`)
	// ninjaBuildRe and ninjaLinkRe pick the file out of the two descriptions cmake writes.
	ninjaBuildRe = regexp.MustCompile(`^Building \S+ object (\S+)`)
	ninjaLinkRe  = regexp.MustCompile(`^Linking \S+ (?:static library|shared library|shared module|executable) (\S+)`)
)

// The estimate of the time left needs a few finished steps and some time behind it: the first steps
// of a build (the small C files) say little about the CUDA kernels that follow.
const (
	etaMinSteps = 3
	etaMinTime  = 20 * time.Second
	// ninjaMaxLine bounds a line held while waiting for its end.
	ninjaMaxLine = 4096
)

// compileStatus is what one ninja line says.
type compileStatus struct {
	Done, Total int
	// Step is the short name of the step: the source file ("fattn.cu") or the program being linked
	// ("llama-server"); Verb is "compile", "link" or "" (another kind of step, Step is then the
	// whole description).
	Step, Verb string
	Percent    float64
	// Left is the estimated time left; HasLeft is false while there is too little to go on.
	Left    time.Duration
	HasLeft bool
}

// ninjaMeter reads ninja lines and estimates the time left from the pace since the first line it saw
// (a build that carries on where it stopped starts at a high n, and its pace counts from there).
type ninjaMeter struct {
	started   bool
	n0, total int
	t0        time.Time
	last      int
}

// observe parses one line; ok is false for a line that is not a ninja status line.
func (m *ninjaMeter) observe(line string, now time.Time) (compileStatus, bool) {
	g := ninjaLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if g == nil {
		return compileStatus{}, false
	}
	done, err1 := strconv.Atoi(g[1])
	total, err2 := strconv.Atoi(g[2])
	if err1 != nil || err2 != nil || total <= 0 || done < 0 || done > total {
		return compileStatus{}, false
	}
	// A new count (cmake regenerated the build files, or a second ninja run) starts the pace again.
	if !m.started || total != m.total || done < m.last {
		m.started, m.n0, m.total, m.t0 = true, done, total, now
	}
	m.last = done
	st := compileStatus{Done: done, Total: total, Percent: 100 * float64(done) / float64(total)}
	st.Step, st.Verb = ninjaStep(g[3])
	if steps, el := done-m.n0, now.Sub(m.t0); steps >= etaMinSteps && el >= etaMinTime {
		st.Left = time.Duration(float64(el) / float64(steps) * float64(total-done))
		st.HasLeft = true
	}
	return st, true
}

// ninjaStep is the short name of a step and its kind.
func ninjaStep(desc string) (step, verb string) {
	if g := ninjaBuildRe.FindStringSubmatch(desc); g != nil {
		base := filepath.Base(g[1])
		for _, ext := range []string{".o", ".obj"} {
			base = strings.TrimSuffix(base, ext)
		}
		return base, "compile"
	}
	if g := ninjaLinkRe.FindStringSubmatch(desc); g != nil {
		return filepath.Base(g[1]), "link"
	}
	return strings.TrimSpace(desc), ""
}

// leftText is "about 40 min left"; under a minute "less than a minute left".
func leftText(l Lang, d time.Duration) string {
	if d < time.Minute {
		return pick(l, "less than a minute left", "queda menos de un minuto")
	}
	return fmt.Sprintf(pick(l, "about %s left", "quedan unos %s"), out.Duration(d.Round(time.Minute)))
}

// compileLabel is the live line's text: "Compiling fattn.cu".
func compileLabel(l Lang, st compileStatus) string {
	switch st.Verb {
	case "compile":
		return pick(l, "Compiling ", "Compilando ") + st.Step
	case "link":
		return pick(l, "Linking ", "Enlazando ") + st.Step
	}
	return st.Step
}

// compileDetail is the live line's detail: "123/456  about 40 min left".
func compileDetail(l Lang, st compileStatus) string {
	d := fmt.Sprintf("%d/%d", st.Done, st.Total)
	if st.HasLeft {
		d += "  " + leftText(l, st.Left)
	}
	return d
}

// compilePlain is the line printed off a terminal: "Compiling: 30% done (123 of 456), about 40 min
// left".
func compilePlain(l Lang, st compileStatus) string {
	s := fmt.Sprintf(pick(l, "Compiling: %d%% done (%d of %d)", "Compilando: %d %% hecho (%d de %d)"), int(st.Percent), st.Done, st.Total)
	if st.HasLeft {
		s += ", " + leftText(l, st.Left)
	}
	return s
}

// llamaStderrTTY says whether the compile progress may redraw one line; tests force it.
var llamaStderrTTY = func(c *Call) bool { return c.stderrIsTTY() }

// llamaRenderClock is the renderer's clock: llamaClock, so tests can move it.
type llamaRenderClock struct{}

func (llamaRenderClock) Now() time.Time { return llamaClock() }
func (llamaRenderClock) NewTicker(d time.Duration) config.Ticker {
	return config.RealClock{}.NewTicker(d)
}

// lineSplitter is an io.Writer that calls fn once per complete line. A line longer than
// ninjaMaxLine is passed on in pieces.
type lineSplitter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (s *lineSplitter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' || len(s.buf) >= ninjaMaxLine {
			if len(s.buf) > 0 {
				s.fn(string(s.buf))
			}
			s.buf = s.buf[:0]
			if b == '\n' || b == '\r' {
				continue
			}
		}
		s.buf = append(s.buf, b)
	}
	return len(p), nil
}

// compileOutput is build.Request.Output: the ninja lines of the compile step. nil with --json or
// --quiet, which show no progress.
func (p *llamaPrinter) compileOutput() io.Writer {
	if p.c.Global.JSON || p.c.Global.Quiet {
		return nil
	}
	return &lineSplitter{fn: p.compileLine}
}

// compileLine shows one ninja line: the live line on a terminal, else a plain line each time the
// build passes another tenth.
func (p *llamaPrinter) compileLine(s string) {
	now := llamaClock()
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.meter.observe(s, now)
	if !ok {
		return
	}
	p.lastCompile = now
	if p.paused {
		return
	}
	if p.tty {
		if p.rend == nil {
			p.rend = progress.NewRenderer(progress.RenderOptions{
				Out: p.c.Stderr, TTY: true, Color: !p.c.Global.NoColor, UTF8: out.UTF8Enabled(p.c.env.Getenv),
				Lang: string(p.c.Lang), Clock: llamaRenderClock{},
			})
		}
		p.rend.Update(progress.Update{Label: compileLabel(p.c.Lang, st), Key: "compile", Percent: st.Percent, Detail: compileDetail(p.c.Lang, st)})
		return
	}
	if tenth := int(st.Percent / 10); tenth > p.tenth {
		p.tenth = tenth
		p.c.Out.Progress(compilePlain(p.c.Lang, st))
	}
}

// clearLive removes the live compile line before anything else is printed (the caller holds mu).
func (p *llamaPrinter) clearLive() {
	if p.rend != nil {
		p.rend.Clear()
	}
}

// endCompile removes the live line for good and forgets the count: a later build (the next option of
// the fallback, or the retry with fewer jobs) starts its own.
func (p *llamaPrinter) endCompile() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearLive()
	p.rend, p.meter, p.tenth = nil, ninjaMeter{}, 0
}

// compiledRecently reports whether a ninja line was shown within d: the heartbeat then has nothing
// to add.
func (p *llamaPrinter) compiledRecently(d time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.lastCompile.IsZero() && llamaClock().Sub(p.lastCompile) < d
}
