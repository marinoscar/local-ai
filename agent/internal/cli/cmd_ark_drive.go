package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl ark init|mount|eject` (aictl.md section 4.10, ark-sync.md sections 3.4, 3.5 and 4.1.5): the
// drive actions behind the desktop "Set up this drive", "Mount" and "Eject" buttons. They call the op
// routes of SYNC-03 (POST /ark/drives/{root_id}/init|mount|eject); `drives set-profile` is in
// cmd_ark_write.go.
//
//   - init never formats anything (the agent cannot either): it creates `localai/` and ROOT.json on a
//     drive that is mounted, empty and already ext4. Because it writes to a person's disk it needs a
//     typed answer (the root id): `--yes` does not answer it, and without a terminal or with `--json`
//     it is LAI-CLI-007 / exit 2 before any write. aictl checks the drive first (state must be
//     `uninitialized`), so a drive that is in use, not mounted or unusable is refused with a plain
//     sentence before the question; the agent checks again (a `localai/` folder with content is
//     `409 LAI-AGT-020`, `details.reason: localai_exists`).
//   - eject asks nothing, it only unmounts. With --force ("eject anyway") it asks y/N first because it
//     stops the brains that run from the drive. A program with a file open is never closed by it.
//   - Nothing here needs the admin PIN: the routes are op routes.

// ejectPoll is how often `eject` re-reads GET /ark/drives while the unmount runs after the 202.
var ejectPoll = time.Second

// arkDriveCmdTimeout is the default deadline of these commands: the agent checks for up to 30 s, then
// unmounts.
const arkDriveCmdTimeout = 90 * time.Second

func init() {
	attachAll(
		at(&Command{
			Name: "init", Summary: "Set up a new, empty AI-ARK drive (it does not format it).",
			Usage: "<mount-path|device|root-id>", Timeout: arkDriveCmdTimeout,
			Description: `Creates the localai folder and the drive's identity file on a drive that is mounted, formatted
as ext4 and empty. It never formats a disk; see runbook 04 for how to format one. It refuses a drive that
is already set up, not mounted, in use or not usable, and the internal disk. You must type the drive's
id to confirm; --yes does not answer this question and --json cannot be used.
The drive is found by its mount path, its device or its id (aictl ark drives lists them).`,
			Run: arkInit,
		}, "ark"),
		at(&Command{
			Name: "mount", Summary: "Mount an AI-ARK drive that is plugged in but not mounted.",
			Usage: "<root-id>", Timeout: arkDriveCmdTimeout,
			Run: arkMount,
		}, "ark"),
		at(&Command{
			Name: "eject", Summary: "Safely stop using an ark drive so you can unplug it.",
			Usage: "<root-id> [--force] [--yes]", Timeout: arkDriveCmdTimeout,
			Description: `Stops what uses the drive, flushes it and unmounts it. When it says "You can unplug the
drive now" it is safe to pull it out. If a program has a file open on the drive the eject stops and
names it; close it and run the command again.
  --force   Eject even if a brain runs only from this drive: that brain is stopped. A program with a
            file open is never closed. It asks first (--yes answers).`,
			Flags: func(fs *flag.FlagSet) {
				fs.Bool("force", false, "eject even if a brain runs from this drive (it is stopped)")
				declareYes(fs)
			},
			Run: arkEject,
		}, "ark"),
	)
}

// wireDrive is one drive of GET /ark/drives with the fields these commands read.
type wireDrive struct {
	RootID    *string `json:"root_id"`
	Device    string  `json:"device"`
	Mount     string  `json:"mount"`
	Label     string  `json:"label"`
	SizeBytes int64   `json:"size_bytes"`
	State     string  `json:"state"`
	Problems  []struct {
		Code string `json:"code"`
	} `json:"problems"`
}

// has reports whether the drive lists the problem code.
func (d wireDrive) has(code string) bool {
	for _, p := range d.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func (d wireDrive) id() string {
	if d.RootID != nil {
		return *d.RootID
	}
	return ""
}

func (d wireDrive) name() string {
	if d.Label != "" {
		return d.Label
	}
	return d.Device
}

func (c *Call) listDrives(ctx context.Context, cl *Client) ([]wireDrive, error) {
	var res struct {
		Drives []wireDrive `json:"drives"`
	}
	if _, _, err := c.callWith(cl, ctx, http.MethodGet, "/ark/drives", nil, &res); err != nil {
		return nil, err
	}
	return res.Drives, nil
}

// driveNotFound is exit 5 with a plain sentence.
func (c *Call) driveNotFound(en, es, fixEN, fixES string) *CLIError {
	e := c.codeError(doctor.ARK001, ExitNotFound, "")
	e.Message = pick(c.Lang, en, es)
	e.Fix = pick(c.Lang, fixEN, fixES)
	return e
}

func arkInit(c *Call) error {
	if err := c.exactArgs(1, "<mount-path|device|root-id>"); err != nil {
		return err
	}
	arg := c.Args[0]
	if arg == "internal" {
		return c.usageErr(
			pick(c.Lang, "The internal disk is not an ark drive and cannot be set up.", "El disco interno no es una unidad del arca y no se puede preparar."),
			pick(c.Lang, "Plug in the AI-ARK drive and use `aictl ark drives` to find it.", "Conecte la unidad AI-ARK y búsquela con `aictl ark drives`."))
	}
	// Nobody to type the answer (a pipe, --json): say so before anything is read or written. --yes does
	// not help; ConfirmTyped is never answered by it.
	if err := c.requireTerminalForConfirm(true); err != nil {
		return err
	}
	ctx, cancel := c.withDefaultTimeout(arkDriveCmdTimeout)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	drives, err := c.listDrives(ctx, cl)
	if err != nil {
		return err
	}
	d, err := c.findDrive(drives, arg)
	if err != nil {
		return err
	}
	if err := c.initable(d); err != nil {
		return err
	}
	root := d.id()
	prompt := pick(c.Lang,
		fmt.Sprintf("Set up %s as an ark drive?\n  %s, mounted at %s, %s\nIt adds a folder named localai and nothing else.\nIt does not format or erase the drive.\nType %s to confirm:",
			d.name(), d.Device, d.Mount, sizeText(d.SizeBytes), root),
		fmt.Sprintf("¿Preparar %s como unidad del arca?\n  %s, montado en %s, %s\nSolo añade una carpeta llamada localai.\nNo formatea ni borra la unidad.\nEscriba %s para confirmar:",
			d.name(), d.Device, d.Mount, sizeText(d.SizeBytes), root))
	ok, err := c.ConfirmTyped(prompt, root)
	if err != nil {
		return err
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return nil
	}
	raw, _, err := c.callWith(cl, ctx, http.MethodPost, "/ark/drives/"+root+"/init", map[string]any{}, nil)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("%s is set up as an ark drive (%s). Fill it with `aictl ark sync --root %s`.", d.name(), root, root),
		fmt.Sprintf("%s está preparada como unidad del arca (%s). Llénela con `aictl ark sync --root %s`.", d.name(), root, root)))
	return nil
}

// findDrive resolves a mount path, a device or a root id against the listed drives. No match is exit 5;
// a root id that two clones share is exit 2 (a mount path tells them apart).
func (c *Call) findDrive(drives []wireDrive, arg string) (wireDrive, error) {
	if len(drives) == 0 {
		return wireDrive{}, c.driveNotFound("No ark drive was found.", "No se encontró ninguna unidad del arca.",
			"Plug in the AI-ARK drive and check it with `aictl ark drives`.", "Conecte la unidad AI-ARK y revísela con `aictl ark drives`.")
	}
	var hit []wireDrive
	for _, d := range drives {
		if d.id() == arg && arg != "" || d.Device == arg && arg != "" ||
			d.Mount != "" && strings.HasPrefix(arg, "/") && path.Clean(d.Mount) == path.Clean(arg) {
			hit = append(hit, d)
		}
	}
	switch len(hit) {
	case 0:
		return wireDrive{}, c.driveNotFound(fmt.Sprintf("No ark drive matches %q.", arg), fmt.Sprintf("Ninguna unidad del arca coincide con %q.", arg),
			"`aictl ark drives` lists the drives with their mount path and id.", "`aictl ark drives` lista las unidades con su ruta de montaje y su id.")
	case 1:
		return hit[0], nil
	}
	return wireDrive{}, c.usageErr(
		pick(c.Lang, fmt.Sprintf("More than one drive matches %q (a copied drive has the same id).", arg), fmt.Sprintf("Más de una unidad coincide con %q (una unidad copiada tiene el mismo id).", arg)),
		pick(c.Lang, "Use the mount path of the one you mean, from `aictl ark drives`.", "Use la ruta de montaje de la que quiere, de `aictl ark drives`."))
}

// initable refuses, before the question, every drive init could not or must not touch.
func (c *Call) initable(d wireDrive) error {
	refuse := func(en, es, fixEN, fixES string) error {
		e := c.codeError(doctor.AGT020, ExitRefused, "")
		e.Message = pick(c.Lang, en, es)
		e.Fix = pick(c.Lang, fixEN, fixES)
		return e
	}
	name := d.name()
	switch d.State {
	case "uninitialized":
		if d.id() == "" || d.Mount == "" {
			return refuse(name+" has no mounted filesystem to set up.", name+" no tiene un sistema de archivos montado para preparar.",
				"Mount it first with `aictl ark mount`.", "Móntela primero con `aictl ark mount`.")
		}
		return nil
	case "not_mounted":
		return refuse(name+" is not mounted, so nothing was changed.", name+" no está montada, así que no se cambió nada.",
			"Run `aictl ark mount "+d.id()+"` first.", "Ejecute primero `aictl ark mount "+d.id()+"`.")
	case "unusable":
		return refuse(name+" cannot be used as an ark drive.", name+" no se puede usar como unidad del arca.",
			"It must be ext4 and named AI-ARK; see runbook 04 for how to format one. Nothing was changed.",
			"Debe ser ext4 y llamarse AI-ARK; vea el runbook 04 para formatearla. No se cambió nada.")
	case "ejecting":
		return refuse(name+" is being ejected.", name+" se está expulsando.", "Wait for the eject to finish.", "Espere a que termine la expulsión.")
	}
	return refuse(name+" is already set up or in use, so it was not touched.", name+" ya está preparada o en uso, así que no se tocó.",
		"Nothing to do. Check it with `aictl ark drives`.", "No hay nada que hacer. Revísela con `aictl ark drives`.")
}

func arkMount(c *Call) error {
	if err := c.exactArgs(1, "<root-id>"); err != nil {
		return err
	}
	root := c.Args[0]
	if err := c.checkRoot("<root-id>", root, false); err != nil {
		return err
	}
	ctx, cancel := c.withDefaultTimeout(arkDriveCmdTimeout)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	var drive wireDrive
	raw, _, err := c.callWith(cl, ctx, http.MethodPost, "/ark/drives/"+root+"/mount", map[string]any{}, &drive)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("The drive %s is mounted at %s.", root, orText(drive.Mount, "its mount point")),
		fmt.Sprintf("La unidad %s está montada en %s.", root, orText(drive.Mount, "su punto de montaje"))))
	return nil
}

func arkEject(c *Call) error {
	applyYes(c)
	if err := c.exactArgs(1, "<root-id>"); err != nil {
		return err
	}
	root := c.Args[0]
	if err := c.checkRoot("<root-id>", root, false); err != nil {
		return err
	}
	force := c.Bool("force")
	if force {
		ok, err := c.Confirm(pick(c.Lang,
			"Eject anyway? A brain that runs only from this drive will be stopped. Programs with files open are not closed.",
			"¿Expulsar de todos modos? Se detendrá el cerebro que funciona solo desde esta unidad. Los programas con archivos abiertos no se cierran."))
		if err != nil {
			return err
		}
		if !ok {
			c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
			return nil
		}
	}
	ctx, cancel := c.withDefaultTimeout(arkDriveCmdTimeout)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	raw, _, err := c.callWith(cl, ctx, http.MethodPost, "/ark/drives/"+root+"/eject", map[string]any{"force": force}, nil)
	if err != nil {
		c.printBlockers(err, force)
		return err
	}
	// The 202 means the checks passed; the unmount runs after it. The list decides when it is done.
	if err := c.waitUnmounted(ctx, cl, root); err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"root_id": root, "state": "ejected", "safe_to_unplug": true, "accepted": json.RawMessage(raw)})
	}
	c.Out.OK(pick(c.Lang, "You can unplug the drive now.", "Ya puede desconectar el disco."))
	return nil
}

// waitUnmounted polls GET /ark/drives until root is gone or no longer mounted. A drive that comes back
// in a mounted state other than `ejecting` means the unmount failed and the drive was put back.
func (c *Call) waitUnmounted(ctx context.Context, cl *Client, root string) error {
	for {
		drives, err := c.listDrives(ctx, cl)
		if err != nil {
			return err
		}
		var found *wireDrive
		for i := range drives {
			if drives[i].id() == root {
				found = &drives[i]
				break
			}
		}
		if found == nil || found.State == "not_mounted" {
			return nil
		}
		if found.State != "ejecting" {
			e := c.codeError(doctor.ARK009, ExitFailed, "")
			e.Message = pick(c.Lang, "The drive could not be unmounted and is back in use.", "No se pudo desmontar la unidad y vuelve a estar en uso.")
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ejectPoll):
		}
	}
}

// printBlockers lists what keeps a drive from being ejected (the details.blockers of LAI-ARK-009).
func (c *Call) printBlockers(err error, force bool) {
	var ae *APIError
	if c.Global.JSON || !errors.As(err, &ae) || ae.Code != doctor.ARK009 {
		return
	}
	list, _ := ae.Details["blockers"].([]any)
	onlyModels := len(list) > 0
	for _, b := range list {
		m, _ := b.(map[string]any)
		name, _ := m["name"].(string)
		kind, _ := m["kind"].(string)
		pid, _ := m["pid"].(float64)
		switch kind {
		case "model":
			c.Out.Warn(pick(c.Lang, fmt.Sprintf("The brain %s is running from this drive.", name), fmt.Sprintf("El cerebro %s está funcionando desde esta unidad.", name)))
		case "sync":
			onlyModels = false
			c.Out.Warn(pick(c.Lang, "A copy or check of this drive is still running.", "Aún hay una copia o comprobación de esta unidad en curso."))
		default:
			onlyModels = false
			c.Out.Warn(pick(c.Lang, fmt.Sprintf("%s (process %d) has files open on this drive.", name, int(pid)), fmt.Sprintf("%s (proceso %d) tiene archivos abiertos en esta unidad.", name, int(pid))))
		}
	}
	if onlyModels && !force {
		c.Out.Warn(pick(c.Lang, "Switch brain first, or add --force to eject anyway.", "Cambie de cerebro primero, o agregue --force para expulsar de todos modos."))
	}
}
