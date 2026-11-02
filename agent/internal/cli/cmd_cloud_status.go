package cli

import (
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl cloud status` (aictl.md section 4.18): whether this node is linked to the cloud app, from
// GET /cloud. enroll, unenroll and resend-key belong to AICTL-06. It works in every mode; in offline
// and survival the link is paused by design and the command says so.

func init() {
	Register(&Command{
		Name:    "cloud",
		Summary: "Show or change the link to the cloud app.",
		Sub: []*Command{{
			Name:        "status",
			Summary:     "Show whether this node is linked to the cloud and how the link is doing.",
			Description: "--json prints the agent body unchanged (the credential in it is already masked).",
			Timeout:     readOnlyTimeout,
			Run:         runCloudStatus,
		}},
	})
}

func runCloudStatus(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var cl wireCloud
	raw, err := roGet(c, "/cloud", &cl)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return roEmit(c, raw)
	}
	renderCloudStatus(c.Out, &cl)
	return nil
}

// maskCredential keeps only what is safe to print: the agent sends the credential already masked
// ("nod_…a1b2"); if it ever sent more, only the prefix and the last four characters are shown.
func maskCredential(s string) string {
	if s == "" || strings.Contains(s, "…") || strings.Contains(s, "...") {
		return s
	}
	r := []rune(s)
	if len(r) <= 8 {
		return "…"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

func renderCloudStatus(p *out.Printer, cl *wireCloud) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	sym := p.Symbols()
	if !cl.Enrolled {
		p.Info(tr("This node is not linked to a cloud app. That is fine: everything works without it.",
			"Este nodo no está vinculado a una app en la nube. No pasa nada: todo funciona sin ella."))
		return
	}
	var rows []out.Pair
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, out.Pair{Label: k, Value: v})
		}
	}
	switch cl.State {
	case "linked":
		add(tr("State", "Estado"), sym.OK+" "+tr("linked", "vinculado"))
	case "backoff":
		add(tr("State", "Estado"), sym.Warn+" "+tr("not connected right now, retrying", "sin conexión por ahora, reintentando"))
	case "stopped":
		add(tr("State", "Estado"), sym.Info+" "+tr("paused until the computer is online", "en pausa hasta que el equipo esté en línea"))
	default:
		add(tr("State", "Estado"), cl.State)
	}
	add(tr("Address", "Dirección"), cl.URL)
	add(tr("Node name", "Nombre del nodo"), cl.Name)
	add(tr("Last heartbeat", "Último latido"), roAgo(p, cl.LastHeartbeatAt))
	add(tr("Credential", "Credencial"), maskCredential(cl.Credential))
	if cl.KeySynced != nil {
		if *cl.KeySynced {
			add(tr("Key synced", "Clave sincronizada"), sym.OK+" "+tr("yes", "sí"))
		} else {
			add(tr("Key synced", "Clave sincronizada"), sym.Warn+" "+tr("no", "no"))
		}
	}
	if msg, ok := cl.LastError.(string); ok && msg != "" {
		add(tr("Last error", "Último error"), msg)
	}
	p.KV(rows)
}
