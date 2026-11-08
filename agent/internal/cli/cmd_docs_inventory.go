package cli

import (
	"errors"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
)

// `aictl docs inventory import <file>` (aictl.md section 4.22a, issue #435): validates a lab inventory
// and copies it to /etc/localai/inventory.local.yaml (root 0600), where `docs render` and sync's
// manuals look first. The fix of LAI-SYN-062 names it instead of a manual copy. Runs in this process
// as root, with no agent and no network. No value of the file is ever printed: the errors carry fixed
// reasons only.

func init() {
	attachAll(at(&Command{
		Name:    "inventory",
		Summary: "Install the lab inventory the manuals are filled from.",
		Sub: []*Command{{
			Name: "import", Summary: "Check a lab inventory file and install it on this computer.", Usage: "<file>",
			Description: `Checks that the file is a lab inventory (valid YAML, schema_version 1) and copies it to
/etc/localai/inventory.local.yaml (root, mode 0600). The next sync, and "aictl docs render", fill
the manuals with its real values. No value from the file is printed. Works with no agent and no
network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.22a`,
			Marks: MarkLocal | MarkRoot,
			Run:   docsInventoryImport,
		}},
	}, "docs"))
}

func docsInventoryImport(c *Call) error {
	if err := c.exactArgs(1, "<file>"); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	src := c.Args[0]
	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()
	changed, ierr := local.ImportInventory(env, src)
	werr := inventoryErr(c, ierr)
	if werr == nil && !changed {
		// Already installed: nothing written, nothing to audit.
		if c.Global.JSON {
			return c.Out.JSON().Document(map[string]any{"changed": false, "path": env.Paths.InventoryFile()})
		}
		c.Out.Info(pick(c.Lang, "This inventory is already installed on this computer; nothing was changed.",
			"Este inventario ya está instalado en este equipo; no se cambió nada."))
		return nil
	}
	recordLocal(c, env, local.ActionInventoryImport, werr, nil)
	if werr != nil {
		return werr
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"changed": true, "path": env.Paths.InventoryFile()})
	}
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("The lab inventory was installed in %s (only root can read it). The next sync fills the manuals with the real values.", env.Paths.InventoryFile()),
		fmt.Sprintf("El inventario del laboratorio se instaló en %s (solo root puede leerlo). La próxima sincronización llena los manuales con los valores reales.", env.Paths.InventoryFile())))
	return nil
}

// inventoryErr words a file that is not an inventory (exit 2, the file's reason in plain words, never
// a value from it); every other error goes through localErr.
func inventoryErr(c *Call, err error) error {
	var ie *local.InventoryError
	if !errors.As(err, &ie) {
		return localErr(c, err)
	}
	es := map[string]string{
		local.InvEmpty:    "está vacío",
		local.InvTooLarge: "es demasiado grande para ser un inventario (más de 1 MiB)",
		local.InvNotYAML:  "no es YAML válido",
		local.InvSchema:   "no sigue inventory.example.yaml (schema_version debe ser 1 y serve_pattern A o B)",
	}
	reason := ie.Reason
	if c.Lang == LangES && es[reason] != "" {
		reason = es[reason]
	}
	return c.usageErr(
		pick(c.Lang, fmt.Sprintf("%s is not a lab inventory: %s. Nothing was changed.", ie.Path, reason),
			fmt.Sprintf("%s no es un inventario del laboratorio: %s. No se cambió nada.", ie.Path, reason)),
		pick(c.Lang, "Use the inventory.local.yaml kept on the ark drive or on the owner's computer, then run the command again.",
			"Use el inventory.local.yaml guardado en la unidad del arca o en el equipo del dueño, y ejecute el comando otra vez."))
}
