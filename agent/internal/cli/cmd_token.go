package cli

import (
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
)

// `aictl token rotate` (aictl.md section 4.21): runs in this process as root, with no agent, and
// writes a new agent token to /etc/localai/agent-token atomically (root:localai-operators 0640). The
// agent notices the new mtime within 10 s and sessions stay valid. The token is never printed:
// callers read the file.

func init() {
	Register(&Command{
		Name:    "token",
		Summary: "Manage the agent token.",
		Sub: []*Command{{
			Name:    "rotate",
			Summary: "Create a new agent token.",
			Description: `Writes a new token to /etc/localai/agent-token (root:localai-operators, mode 0640) and records
the change in the audit log. The agent re-reads the file within 10 seconds; sessions stay valid.
The token is not printed. Works with no agent and no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.21`,
			Marks: MarkLocal | MarkRoot,
			Run:   tokenRotate,
		}},
	})
}

func tokenRotate(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()
	werr := localErr(c, local.RotateToken(env))
	recordLocal(c, env, audit.ActionTokenRotate, werr, nil)
	if werr != nil {
		return werr
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"rotated": true, "path": env.Paths.TokenFile()})
	}
	c.Out.OK(pick(c.Lang,
		"A new agent token was created. ai-desktop picks it up by itself. Scripts that copied the old token must read the file again.",
		"Se creó un nuevo token del agente. ai-desktop lo toma por sí solo. Los scripts que copiaron el token anterior deben leer el archivo otra vez."))
	return nil
}
