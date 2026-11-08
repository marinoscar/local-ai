package cli

import (
	"flag"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl ark hf-token set` and `aictl ark hf-token status` (aictl.md section 4.10, issue #435): the fix
// of LAI-SYN-034 names them instead of asking a person to create /etc/localai/hf-token by hand. Both
// run in this process as root, with no agent (the file is root:root 0600 and sync reads it at request
// time, ark-sync.md section 4.9.1).
//
// The token is read only from a no-echo prompt or one line of stdin (--stdin). It is never an
// argument (argv is visible to every user of the computer), never printed, never in an error, the
// audit line or --json, and the variable is dropped once the file is written.

func init() {
	attachAll(at(&Command{
		Name:    "hf-token",
		Summary: "Set the Hugging Face token sync uses for models that need a login.",
		Sub: []*Command{
			{
				Name: "set", Summary: "Save a Hugging Face token (asked without showing it).", Usage: "[--stdin]",
				Description: `Asks for the token without showing it, or reads one line from stdin with --stdin. The token
is never given as an argument. Writes /etc/localai/hf-token (root, mode 0600) and records the
change in the audit log; the next sync uses it. Works with no agent and no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.10`,
				Marks: MarkLocal | MarkRoot,
				Flags: func(fs *flag.FlagSet) {
					fs.Bool("stdin", false, "read the token from one line of stdin instead of asking")
				},
				Run: hfTokenSet,
			},
			{
				Name: "status", Summary: "Say whether a Hugging Face token is set (never shows it).",
				Description: `Prints only whether /etc/localai/hf-token holds a token. Run it with sudo.
Specification: docs/specs/aictl.md section 4.10`,
				Marks: MarkLocal | MarkRoot,
				Run:   hfTokenStatus,
			},
		},
	}, "ark"))
}

func hfTokenSet(c *Call) error {
	if len(c.Args) > 0 {
		// Never echo the argument: it may be the token itself.
		return c.usageErr(pick(c.Lang, "The token is never given on the command line (other users of this computer could see it).",
			"El token nunca se escribe en la línea de comandos (otros usuarios de este equipo podrían verlo)."),
			pick(c.Lang, "Run `sudo aictl ark hf-token set` and paste the token when it asks, or send it on stdin with `--stdin`.",
				"Ejecute `sudo aictl ark hf-token set` y pegue el token cuando lo pida, o envíelo por la entrada estándar con `--stdin`."))
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	stdin := c.Bool("stdin")
	interactive := !c.Global.JSON && c.Term.Interactive()
	if !stdin && !interactive {
		return c.usageErr(pick(c.Lang, "There is no terminal to ask for the token.", "No hay una terminal para pedir el token."),
			pick(c.Lang, "Run it in a terminal, or pass `--stdin` and send the token on stdin.",
				"Ejecútelo en una terminal, o use `--stdin` y envíe el token por la entrada estándar."))
	}
	token, err := c.readSecret(pick(c.Lang, "Hugging Face token (it is not shown): ", "Token de Hugging Face (no se muestra): "), interactive)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return c.usageErr(pick(c.Lang, "No token was entered, so nothing was changed.", "No se escribió ningún token, así que no se cambió nada."),
			pick(c.Lang, "Copy the token from your Hugging Face account (Settings, Access Tokens) and run the command again.",
				"Copie el token de su cuenta de Hugging Face (Settings, Access Tokens) y ejecute el comando otra vez."))
	}
	if !local.ValidHFToken(token) {
		return c.usageErr(pick(c.Lang, "That does not look like a Hugging Face token (8 to 256 characters, no spaces), so nothing was changed.",
			"Eso no parece un token de Hugging Face (de 8 a 256 caracteres, sin espacios), así que no se cambió nada."),
			pick(c.Lang, "Copy the whole token again from your Hugging Face account and run the command again.",
				"Copie otra vez el token completo desde su cuenta de Hugging Face y ejecute el comando de nuevo."))
	}

	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()
	werr := localErr(c, local.SetHFToken(env, token))
	token = "" // never kept
	recordLocal(c, env, local.ActionHFTokenSet, werr, nil)
	if werr != nil {
		return werr
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"set": true, "path": env.Paths.HFTokenFile()})
	}
	c.Out.OK(pick(c.Lang,
		"The Hugging Face token was saved. The next sync uses it for models that need a login.",
		"El token de Hugging Face se guardó. La próxima sincronización lo usa para los modelos que piden iniciar sesión."))
	return nil
}

func hfTokenStatus(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env := localEnv()
	set, err := local.HFTokenSet(env)
	if err != nil {
		return localErr(c, err)
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"set": set})
	}
	if set {
		c.Out.OK(pick(c.Lang, "A Hugging Face token is set.", "Hay un token de Hugging Face guardado."))
		return nil
	}
	c.Out.Info(pick(c.Lang,
		"No Hugging Face token is set. Models that need a login are skipped ["+doctor.SYN034+"]. Add one with `sudo aictl ark hf-token set`.",
		"No hay ningún token de Hugging Face. Los modelos que piden iniciar sesión se omiten ["+doctor.SYN034+"]. Añada uno con `sudo aictl ark hf-token set`."))
	return nil
}
