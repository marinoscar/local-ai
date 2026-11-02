package cli

import (
	"flag"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/auth"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
)

// `aictl pin set` (aictl.md section 4.19): runs in this process as root, with no agent, and writes
// /etc/localai/admin-pin (argon2id) atomically, root 0600. The agent reads the file on every
// verification, so nothing restarts. The PIN is read only from a no-echo prompt or one line of
// stdin: never a flag, never the environment, never printed, never in an error or the audit line.

func init() {
	Register(&Command{
		Name:    "pin",
		Summary: "Manage the admin PIN of this node.",
		Sub: []*Command{{
			Name:    "set",
			Summary: "Change the admin PIN (4 to 8 digits).",
			Usage:   "[--pin-stdin]",
			Description: `Asks for the new PIN twice (no echo), or reads one line from stdin with --pin-stdin.
Writes /etc/localai/admin-pin and records the change in the audit log. Works with no agent and
no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.19`,
			Marks: MarkLocal | MarkRoot,
			Flags: func(fs *flag.FlagSet) {
				fs.Bool("pin-stdin", false, "read the new PIN from one line of stdin instead of asking twice")
			},
			Run: pinSet,
		}},
	})
}

func pinSet(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if c.Bool("pin-stdin") {
		c.Global.PinStdin = true
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	interactive := !c.Global.JSON && c.Term.Interactive()
	if !c.Global.PinStdin && !interactive {
		e := c.Err(CodeNeedPIN, pick(c.Lang, "There is no terminal to ask for the new PIN.", "No hay una terminal para pedir el nuevo PIN."))
		e.Fix = pick(c.Lang, "Run it in a terminal, or pass `--pin-stdin` and send the PIN on stdin.",
			"Ejecútelo en una terminal, o use `--pin-stdin` y envíe el PIN por la entrada estándar.")
		return e
	}
	pin, err := askNewPIN(c, interactive)
	if err != nil {
		return err
	}

	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()
	werr := localErr(c, local.SetPIN(env, pin))
	pin = "" // never kept
	recordLocal(c, env, audit.ActionPINSet, werr, nil)
	if werr != nil {
		return werr
	}

	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"changed": true})
	}
	c.Out.OK(pick(c.Lang,
		"The admin PIN was changed. Keep it somewhere safe (for example, on paper with the ark drive).",
		"El PIN de administrador se cambió. Guárdelo en un lugar seguro (por ejemplo, en papel junto al disco del arca)."))
	return nil
}

// askNewPIN gets the new PIN: with --pin-stdin one entry (prompted only on a terminal), otherwise
// two entries on the terminal that must match. A PIN that is not 4 to 8 digits, or two entries that
// differ, is a usage error (exit 2); nothing has been written by then.
func askNewPIN(c *Call, interactive bool) (string, error) {
	first, err := c.readSecret(pick(c.Lang, "New admin PIN (4 to 8 digits): ", "Nuevo PIN de administrador (4 a 8 dígitos): "), interactive)
	if err != nil {
		return "", err
	}
	if first == "" { // Enter on nothing, or the input ended
		return "", c.Err(CodeNeedPIN, pick(c.Lang, "No PIN was entered.", "No se escribió ningún PIN."))
	}
	if !auth.ValidPIN(first) {
		return "", c.usageErr(pick(c.Lang, "The PIN must be 4 to 8 digits, numbers only.", "El PIN debe tener de 4 a 8 dígitos, solo números."),
			pick(c.Lang, "Run `sudo aictl pin set` again with a PIN such as 4 to 8 digits.", "Ejecute `sudo aictl pin set` otra vez con un PIN de 4 a 8 dígitos."))
	}
	if c.Global.PinStdin {
		return first, nil
	}
	again, err := c.readSecret(pick(c.Lang, "Repeat: ", "Repita: "), true)
	if err != nil {
		return "", err
	}
	if again != first {
		return "", c.usageErr(pick(c.Lang, "The two entries are not the same.", "Las dos entradas no son iguales."),
			pick(c.Lang, "Run `sudo aictl pin set` again and type the same PIN twice.", "Ejecute `sudo aictl pin set` otra vez y escriba el mismo PIN dos veces."))
	}
	return first, nil
}
