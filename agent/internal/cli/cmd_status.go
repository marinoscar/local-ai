package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl status [--watch]` (aictl.md section 4.1): the summary an operator reads first. It renders
// GET /status in plain words, with the alerts in the problem layout after it. It works in every
// mode and exits 0 even with alerts (they are information, not a failure of the command).

// statusWatchInterval is how often --watch redraws on its own; statusWatchMinGap is the least time
// between a redraw and the next one an event asks for (a burst of events is one redraw); and
// statusWatchRetry is how long the event subscription waits before it reconnects. Tests shorten them.
var (
	statusWatchInterval = 2 * time.Second
	statusWatchMinGap   = 500 * time.Millisecond
	statusWatchRetry    = 5 * time.Second
)

func init() {
	Register(&Command{
		Name:    "status",
		Summary: "Show the state of this node: mode, model, services, ark, cloud.",
		Usage:   "[--watch]",
		Description: `Prints a summary of this node and then its alerts. Exit 0 even with alerts.
--watch redraws every 2 s and whenever the agent reports an event, until Ctrl-C (a terminal only;
with --json it is refused).
--json prints the agent's status body unchanged.
The default timeout is 10 s per request; --watch has no overall deadline unless --timeout is given.`,
		// No registry timeout: --watch must run for as long as the user wants, so every request gets
		// its own 10 s deadline (roRequestContext) unless --timeout was given.
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("watch", false, "redraw every 2 s until Ctrl-C (terminal only)")
		},
		Run: runStatus,
	})
}

// roRequestContext gives one request its default deadline when the user set none (--timeout, which
// already bounds c.Ctx, wins).
func roRequestContext(c *Call) (context.Context, context.CancelFunc) {
	if c.Global.TimeoutSet {
		return c.Ctx, func() {}
	}
	return context.WithTimeout(c.Ctx, readOnlyTimeout)
}

func runStatus(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	if c.Bool("watch") {
		return statusWatch(c)
	}
	ctx, cancel := roRequestContext(c)
	defer cancel()
	var st wireStatus
	raw, err := roGetCtx(c, ctx, "/status", &st)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderStatus(c.Out, &st, statusBrainNames(c, ctx, &st))
	return nil
}

// statusBrainNames fetches GET /models to turn the model ids of the Brain line into display names
// ("Qwen 3.5 9B (qwen3.5-9b-q4km)"). It is best effort with a short deadline: any failure leaves the
// ids alone, so a hung router never delays or fails `aictl status`.
func statusBrainNames(c *Call, ctx context.Context, st *wireStatus) map[string]string {
	if st.Models == nil || (st.Models.Default == "" && len(st.Models.Loaded) == 0 && len(st.Models.Loading) == 0) {
		return nil
	}
	sub, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var ms wireModels
	if _, err := roGetCtx(c, sub, "/models", &ms); err != nil {
		return nil
	}
	names := map[string]string{}
	for _, m := range ms.Models {
		if m.DisplayName != "" {
			names[m.ID] = m.DisplayName
		}
	}
	return names
}

// renderStatus prints the summary block and the alerts. It is a pure function of the body, the
// printer (language, symbols, clock) and the display names, so golden tests need no agent. A section
// the agent left out is skipped, never printed as zeros.
func renderStatus(p *out.Printer, st *wireStatus, names map[string]string) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	dot := roDot(p)
	label := func(id string) string {
		if n := names[id]; n != "" && n != id {
			return n + " (" + id + ")"
		}
		return id
	}

	// Header.
	head := []string{}
	if st.Node.ID != "" {
		head = append(head, tr("Node ", "Nodo ")+st.Node.ID)
	}
	if n := st.Node.DisplayName; n != "" && n != st.Node.ID {
		head = append(head, n)
	}
	if st.Node.Profile != "" {
		head = append(head, tr("profile ", "perfil ")+st.Node.Profile)
	}
	if len(head) > 0 {
		p.Result(strings.Join(head, dot))
	}

	var rows []out.Pair
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, out.Pair{Label: k, Value: v})
		}
	}
	// col pads a value to 12 columns, with at least one space, so a note after it lines up.
	col := func(s string) string {
		if out.DisplayWidth(s) >= 12 {
			return s + " "
		}
		return roPad(s, 12)
	}

	// Mode.
	mode := st.Mode.Value
	if mode != "" {
		v := modeWord(p, mode)
		if t := st.Mode.Transition; t != nil && t.Target != "" {
			v += tr(" (switching to "+modeWord(p, t.Target)+")", " (cambiando a "+modeWord(p, t.Target)+")")
		} else if ago := roAgo(p, st.Mode.Since); ago != "" {
			v = col(v) + "(" + tr("since ", "desde ") + ago + ")"
		}
		add(tr("Mode", "Modo"), v)
	}

	// Internet: not checked outside online mode (the prober is stopped).
	if st.Connectivity.Value != "" || mode == "offline" || mode == "survival" {
		conn := st.Connectivity.Value
		if mode == "offline" || mode == "survival" {
			conn = "not_checked"
		}
		var v string
		switch conn {
		case "full":
			v = col(sym.OK+" "+tr("full", "completo")) + tr("(cloud reachable)", "(la nube responde)")
		case "internet":
			v = col(sym.Warn+" "+tr("internet", "internet")) + tr("(cloud not reachable)", "(la nube no responde)")
		case "tailnet":
			v = col(sym.Warn+" "+tr("tailnet", "tailnet")) + tr("(no internet)", "(sin internet)")
		case "none":
			v = sym.Error + " " + tr("no connection", "sin conexión")
		case "not_checked", "":
			v = tr("not checked", "sin comprobar")
		default:
			v = conn
		}
		add("Internet", v)
	}

	// Brain.
	if st.Models != nil {
		add(tr("Brain", "Cerebro"), brainLine(p, st.Models, label))
	}

	// Graphics and memory.
	hw := st.Hardware
	if hw.VRAMTotalMiB > 0 {
		v := out.GBFromMiB(hw.VRAMUsedMiB) + " / " + out.GBFromMiB(hw.VRAMTotalMiB) + tr(" used", " en uso")
		if hw.GPUTempC != nil {
			v += dot + fmt.Sprintf("%.0f %s", *hw.GPUTempC, roDegC(p))
		}
		add(tr("Graphics", "Gráficos"), v)
	} else if hw.GPU != "" || hw.RAMTotalMiB > 0 {
		add(tr("Graphics", "Gráficos"), tr("no graphics memory (CPU only)", "sin memoria gráfica (solo CPU)"))
	}
	if hw.RAMTotalMiB > 0 {
		add(tr("Memory", "Memoria"), out.GBFromMiB(hw.RAMAvailMiB)+tr(" free of ", " libres de ")+out.GBFromMiB(hw.RAMTotalMiB))
	}

	// Power.
	if pw := hw.Power; pw != nil && pw.Source != "" {
		var v string
		switch pw.Source {
		case "ac":
			v = sym.OK + " " + tr("plugged in", "conectado a la corriente")
			if pw.Charging {
				v += tr(", charging", ", cargando")
			}
		case "battery":
			v = sym.Warn + " " + tr("on battery", "con batería")
		default:
			v = tr("power source unknown", "fuente de energía desconocida")
		}
		if pw.BatteryPercent != nil {
			v += dot + tr("battery ", "batería ") + fmt.Sprintf("%.0f%%", *pw.BatteryPercent)
		}
		add(tr("Power", "Energía"), v)
	}

	// Services.
	if len(st.Services) > 0 {
		parts := make([]string, 0, len(st.Services))
		for _, s := range st.Services {
			parts = append(parts, serviceSymbol(p, s.Active, s.Healthy)+" "+s.Name)
		}
		add(tr("Services", "Servicios"), strings.Join(parts, "   "))
	}

	// Ark.
	if st.Ark != nil {
		add("Ark", arkLine(p, st.Ark))
	}

	// Cloud and tailnet.
	if st.Cloud != nil {
		add(tr("Cloud", "Nube"), cloudLine(p, st.Cloud))
	}
	if ts := st.Tailscale; ts != nil {
		var v string
		pat := ""
		if ts.Pattern != "" && ts.Pattern != "none" {
			pat = " (" + tr("pattern ", "patrón ") + ts.Pattern + ")"
		}
		switch {
		case !ts.Manage:
			v = tr("not managed by this node", "no lo administra este nodo")
		case ts.Applied:
			v = sym.OK + " " + tr("published", "publicado") + pat
		default:
			v = sym.Warn + " " + tr("not published", "sin publicar") + pat
		}
		if ts.Manage && ts.BackendState != "" && ts.BackendState != "Running" {
			v += dot + ts.BackendState
		}
		add("Tailnet", v)
	}
	p.KV(rows)

	// Alerts, most serious first as the agent sorted them, in the problem layout.
	if len(st.Alerts) > 0 {
		p.Blank()
		for _, a := range st.Alerts {
			p.Finding(out.Problem{Severity: roSeverity(a.Severity), Code: a.Code, Message: sentence(a.Title), Fix: a.Fix, Details: a.Details})
		}
	}
}

// sentence makes sure a title ends with a period, as every message of the style guide does.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// modeWord is the mode in the user's language.
func modeWord(p *out.Printer, m string) string {
	switch m {
	case "online":
		return pick(p.Lang(), "online", "en línea")
	case "offline":
		return pick(p.Lang(), "offline", "sin conexión")
	case "survival":
		return pick(p.Lang(), "survival", "supervivencia")
	}
	return m
}

// serviceSymbol: running and healthy is ok, running but unhealthy a warning, anything else stopped.
func serviceSymbol(p *out.Printer, active string, healthy bool) string {
	sym := p.Symbols()
	switch {
	case active == "active" && healthy:
		return sym.OK
	case active == "active" || active == "activating":
		return sym.Warn
	}
	return sym.Error
}

func brainLine(p *out.Printer, m *wireStatusModels, label func(string) string) string {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	em := roEm(p)
	switch {
	case len(m.Loaded) > 0:
		first := m.Loaded[0]
		v := sym.OK + " " + label(first) + em
		if first == m.Default {
			v += tr("default, loaded", "predeterminado, cargado")
		} else {
			v += tr("loaded", "cargado")
		}
		if n := len(m.Loaded) - 1; n > 0 {
			v += fmt.Sprintf(tr(" (+%d more)", " (+%d más)"), n)
		}
		if m.Default != "" && !contains(m.Loaded, m.Default) {
			v += roDot(p) + tr("default: ", "predeterminado: ") + label(m.Default)
		}
		return v
	case len(m.Loading) > 0:
		return sym.Progress + " " + tr("loading ", "cargando ") + label(m.Loading[0])
	case m.Default != "":
		return sym.Warn + " " + tr("no model loaded", "ningún modelo cargado") + roDot(p) +
			tr("default is ", "el predeterminado es ") + label(m.Default)
	case m.Present == 0:
		return sym.Warn + " " + tr("no model stored on this computer", "ningún modelo guardado en este equipo")
	}
	return sym.Warn + " " + tr("no model loaded, no default chosen", "ningún modelo cargado ni predeterminado elegido")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func arkLine(p *out.Printer, a *wireStatusArk) string {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	dot := roDot(p)
	worst := sym.OK
	var parts []string
	for _, r := range a.Roots {
		kind := r.Kind
		switch kind {
		case "internal":
			kind = tr("internal", "interno")
		case "drive", "ark_drive":
			kind = tr("ark drive", "unidad ark")
		}
		if r.ReadyPct == nil {
			parts = append(parts, kind+tr(" not checked", " sin comprobar"))
			continue
		}
		if *r.ReadyPct < 100 {
			worst = sym.Warn
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%% %s", kind, *r.ReadyPct, tr("ready", "listo")))
	}
	if len(parts) == 0 {
		return sym.Warn + " " + tr("no content roots", "sin carpetas de contenido")
	}
	v := worst + " " + strings.Join(parts, ", ")
	switch {
	case a.Sync.State == "running" || a.Sync.State == "cancelling":
		v += dot + tr("sync running", "sincronización en curso")
	case a.Sync.LastSuccessAt != "":
		v += dot + tr("last sync ", "última sincronización ") + roAgo(p, a.Sync.LastSuccessAt)
	default:
		v += dot + tr("never synced", "nunca sincronizado")
	}
	if a.Drive.Mounted {
		v += dot + tr("ark drive connected", "unidad ark conectada")
	} else {
		v += dot + tr("no ark drive", "sin unidad ark")
	}
	return v
}

func cloudLine(p *out.Printer, c *wireCloud) string {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	dot := roDot(p)
	if !c.Enrolled {
		return tr("not linked (optional)", "sin vincular (opcional)")
	}
	switch c.State {
	case "linked":
		v := sym.OK + " " + tr("linked to ", "vinculado a ") + c.URL
		if ago := roAgo(p, c.LastHeartbeatAt); ago != "" {
			v += dot + tr("last heartbeat ", "último latido ") + ago
		}
		return v
	case "backoff":
		return sym.Warn + " " + tr("not connected right now, retrying", "sin conexión por ahora, reintentando") + dot + c.URL
	case "stopped":
		return sym.Info + " " + tr("paused until the computer is online", "en pausa hasta que el equipo esté en línea") + dot + c.URL
	}
	return c.State + dot + c.URL
}

// statusWatch redraws the summary every statusWatchInterval and whenever the agent publishes an
// event (a model loaded, a mode change, a service going down), until Ctrl-C. A failed refresh (the
// agent restarting) is drawn as the problem and the next tick tries again; the event subscription is
// an extra trigger, so if it cannot be opened the watch simply polls.
func statusWatch(c *Call) error {
	if c.Global.JSON || !c.env.StdoutIsTTY {
		return c.Err(CodeUsage, pick(c.Lang,
			"--watch needs a terminal and cannot be combined with --json.",
			"--watch necesita una terminal y no se combina con --json."))
	}
	wctx, stop := context.WithCancel(c.Ctx)
	defer stop()
	kick := make(chan struct{}, 1)
	if cl, err := c.Client(); err == nil {
		go watchEvents(wctx, c.eventsOpener(cl), kick)
	}
	for {
		var frame bytes.Buffer
		p := roFramePrinter(c, &frame)
		ctx, cancel := roRequestContext(c)
		var st wireStatus
		_, err := roGetCtx(c, ctx, "/status", &st)
		if err == nil {
			renderStatus(p, &st, statusBrainNames(c, ctx, &st))
		}
		cancel()
		if cerr := c.Ctx.Err(); cerr != nil {
			if errors.Is(cerr, context.Canceled) {
				return nil // Ctrl-C is how --watch ends
			}
			return cerr
		}
		if err != nil {
			p.Problem(problemOf(err))
		}
		io.WriteString(c.Stdout, "\x1b[H\x1b[2J")
		c.Stdout.Write(frame.Bytes())
		select {
		case <-c.Ctx.Done():
			if errors.Is(c.Ctx.Err(), context.Canceled) {
				return nil
			}
			return c.Ctx.Err()
		case <-time.After(statusWatchInterval):
		case <-kick:
			// An event: redraw soon, but let a burst of them become one redraw.
			select {
			case <-c.Ctx.Done():
			case <-time.After(statusWatchMinGap):
			}
			select {
			case <-kick:
			default:
			}
		}
	}
}

// watchEvents sends on kick (without blocking) for every event of the agent until ctx ends. It
// reconnects after a failure, waiting statusWatchRetry; a keepalive is not an event.
func watchEvents(ctx context.Context, open events.OpenFunc, kick chan<- struct{}) {
	for ctx.Err() == nil {
		if s, err := events.Open(ctx, open, events.Query{}); err == nil {
			for {
				it, err := s.Next()
				if err != nil {
					break
				}
				if it.Ping || it.Event.Type == "" {
					continue
				}
				select {
				case kick <- struct{}{}:
				default:
				}
			}
			s.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(statusWatchRetry):
		}
	}
}

// roFramePrinter is a Printer like c.Out that writes both streams into w (one redraw frame).
func roFramePrinter(c *Call, w io.Writer) *out.Printer {
	return out.New(out.Options{
		Stdout: w, Stderr: w, Lang: c.Lang, Color: !c.Global.NoColor, UTF8: out.UTF8Enabled(c.env.Getenv),
		Quiet: c.Global.Quiet, Verbose: c.Global.Verbose,
	})
}
