package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// `aictl mode` (aictl.md section 4.9). `aictl mode survival` is the terminal fallback written on the
// survival card, so it must work for anyone: no token, no PIN, no network. It goes to the route that
// needs no authentication (POST /mode/survival) and follows the transition through the events.

// modeInfo is GET /mode (agent.md section 3.8.15).
type modeInfo struct {
	Mode       string `json:"mode"`
	Since      string `json:"since"`
	Transition *struct {
		Target    string `json:"target"`
		Step      string `json:"step"`
		StartedAt string `json:"started_at"`
	} `json:"transition"`
	AllowedTargets     []string `json:"allowed_targets"`
	LeaveRequiresAdmin bool     `json:"leave_requires_admin"`
	Connectivity       string   `json:"connectivity"`
	SuggestSurvival    bool     `json:"suggest_survival"`
}

// modeAnswer is what POST /mode and POST /mode/survival answer (every shape of the 200 and the 202).
type modeAnswer struct {
	OpID     string                `json:"op_id"`
	From     string                `json:"from"`
	Target   string                `json:"target"`
	Mode     string                `json:"mode"`
	Already  bool                  `json:"already"`
	Queued   bool                  `json:"queued"`
	Warnings []agentevents.Warning `json:"warnings"`
}

// Test seams for the clock and the time zone of "since yesterday 22:10".
var (
	modeNow   = time.Now
	modeLocal = time.Local
)

func init() {
	Register(&Command{
		Name:    "mode",
		Summary: "Show the mode of this node, or switch it (online, offline, survival).",
		Usage:   "[online|offline|survival] [--no-wait] [--reapply]",
		Description: `Without a mode it shows the current one. Switching to survival needs no PIN and no network:
anyone at the keyboard can do it. Leaving survival needs the admin PIN unless you run as root or as a
member of localai-operators over the socket.
  --no-wait   Return as soon as the change has started.
  --reapply   Run every step again even when the node is already in that mode.`,
		Timeout: 20 * time.Minute,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("no-wait", false, "return as soon as the change has started")
			fs.Bool("reapply", false, "run every step again even when already in that mode")
		},
		Run: modeRun,
	})
}

func modeRun(c *Call) error {
	switch len(c.Args) {
	case 0:
		return modeShow(c)
	case 1:
		switch t := c.Args[0]; t {
		case "online", "offline", "survival":
			return modeChange(c, t)
		default:
			return c.usageErr(pick(c.Lang,
				fmt.Sprintf("Unknown mode %q. Use online, offline or survival.", t),
				fmt.Sprintf("Modo desconocido %q. Use online, offline o survival.", t)), "")
		}
	}
	return c.usageErr(pick(c.Lang, "Give one mode: online, offline or survival.", "Indique un solo modo: online, offline o survival."), "")
}

// modeWord is the mode in a sentence ("survival mode").
func modeWord(l Lang, mode string) string {
	switch mode {
	case "online":
		return pick(l, "online mode", "modo en línea")
	case "offline":
		return pick(l, "offline mode", "modo sin conexión")
	case "survival":
		return pick(l, "survival mode", "modo supervivencia")
	}
	return mode
}

// modeName is the mode as a bare word ("survival").
func modeName(l Lang, mode string) string {
	switch mode {
	case "online":
		return pick(l, "online", "en línea")
	case "offline":
		return pick(l, "offline", "sin conexión")
	case "survival":
		return pick(l, "survival", "supervivencia")
	}
	return mode
}

// modeStepLabel is what a mode.step names, in words; "" for the steps nobody needs to read about.
func modeStepLabel(l Lang, step string) string {
	switch step {
	case "link_stop":
		return pick(l, "Stopping cloud connection", "Deteniendo la conexión con la nube")
	case "sync_cancel":
		return pick(l, "Stopping the running sync", "Deteniendo la sincronización en curso")
	case "tailscale_off":
		return pick(l, "Unpublishing this node from the tailnet", "Quitando este nodo de la tailnet")
	case "survival_models":
		return pick(l, "Starting the models", "Iniciando los modelos")
	case "survival_services":
		return pick(l, "Starting the library and speech", "Iniciando la biblioteca y la voz")
	case "tailscale_on":
		return pick(l, "Publishing this node on the tailnet", "Publicando este nodo en la tailnet")
	case "link_start":
		return pick(l, "Connecting to the cloud", "Conectando con la nube")
	}
	return ""
}

// sinceText is "yesterday 22:10": the day word (today, yesterday, or the date) and the local time.
func sinceText(l Lang, t, now time.Time) string {
	t, now = t.In(modeLocal), now.In(modeLocal)
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	clock := t.Format("15:04")
	switch days := int(day(now).Sub(day(t)).Hours()/24 + 0.5); {
	case days == 0:
		return pick(l, "today ", "hoy ") + clock
	case days == 1:
		return pick(l, "yesterday ", "ayer ") + clock
	}
	return t.Format("2006-01-02") + " " + clock
}

func modeShow(c *Call) error {
	ctx, cancel := c.withDefaultTimeout(10 * time.Second)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	var m modeInfo
	raw, _, err := c.callWith(cl, ctx, http.MethodGet, "/mode", nil, &m)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	head := modeName(c.Lang, m.Mode)
	if t, perr := time.Parse(time.RFC3339, m.Since); perr == nil {
		since := sinceText(c.Lang, t, modeNow())
		if c.Global.Verbose {
			since += " (" + out.Absolute(t) + ")"
		}
		head += pick(c.Lang, " (since ", " (desde ") + since + ")"
	}
	label := pick(c.Lang, "Mode", "Modo")
	pad := strings.Repeat(" ", len(label)+2)
	c.Out.Result(label + "  " + head)
	if m.Transition != nil {
		step := modeStepLabel(c.Lang, m.Transition.Step)
		if step == "" {
			step = m.Transition.Step
		}
		to := m.Transition.Target
		if to == "" {
			to = "?"
		}
		c.Out.Result(pad + pick(c.Lang, "Changing to "+to+": "+step, "Cambiando a "+to+": "+step))
	}
	var cmds []string
	for _, t := range m.AllowedTargets {
		cmds = append(cmds, "aictl mode "+t)
	}
	if len(cmds) > 0 {
		c.Out.Result(pad + pick(c.Lang, "Switch with: ", "Cambie con: ") + strings.Join(cmds, " | "))
	}
	if m.LeaveRequiresAdmin {
		c.Out.Result(pad + pick(c.Lang,
			"Leaving this mode needs the admin PIN (not with sudo).",
			"Salir de este modo necesita el PIN de administrador (con sudo no)."))
	}
	if m.SuggestSurvival {
		c.Out.Info(pick(c.Lang,
			"No internet for 30 minutes. Everything here still works. To save power and keep the essentials, run `aictl mode survival`.",
			"Sin internet desde hace 30 minutos. Todo lo local sigue funcionando. Para ahorrar energía y conservar lo esencial, ejecute `aictl mode survival`."))
	}
	return nil
}

// errModeAnswered ends a Follow whose request already has the final answer in hand (an "already",
// or a finished change answered with wait: true): there is nothing to wait for.
var errModeAnswered = errors.New("mode: the answer is complete")
