package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/prep"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// `aictl ark format`, `aictl ark repair` and `aictl ark new-id` (aictl.md section 4.10, ark-sync.md
// section 4.17, issue #434): prepare a new AI-ARK drive, repair one with filesystem errors (LAI-ARK-004),
// and give a copied drive an id of its own (LAI-ARK-006), with no raw shell command.
//
// The privileged split (ark-sync.md section 4.17.1): the disk programs run here, in `sudo aictl`, never in
// the agent. The agent keeps doing what it does for every drive (it lists them, ejects them, sets them up)
// through its op routes; it never formats, checks or relabels a disk, and no HTTP route can. So the only
// way to erase a disk is a person with sudo at a terminal. Every disk program is a fixed argv behind
// prep.Tools, and every device path comes from lsblk's JSON (prep.ParseLsblk) or the agent's drive list,
// never from what a person typed.
//
//   - format [root]: lists only the disks prep.Classify offers (removable or USB, nothing mounted, not
//     the disk of /, /boot, swap or a Local AI data directory), the person picks one by number, types the
//     admin PIN (asked even as root; checked by the agent) and then the word ERASE. `--yes` answers none
//     of these and `--json` is refused. Then: wipefs, one GPT partition, mkfs.ext4 -L AI-ARK, mount, and
//     the agent's init.
//   - repair <root-id> [root]: asks y/N (--yes answers), ejects the drive through the agent with the disk
//     kept powered, runs fsck.ext4 -f -y with progress lines, mounts it again and says what it found.
//   - new-id <root-id|mount-path> [root]: the copy that shows LAI-ARK-006; asks y/N, unmounts it, checks
//     it (tune2fs needs a freshly checked filesystem), runs tune2fs -U random and mounts it again; the
//     agent then lists it under its new root id.
//
// Once the person has said yes, Ctrl-C no longer stops the disk work: a half-written partition table or a
// check cut in the middle is worse than waiting. The command says so and goes on to the end.

// arkPrepTools returns the disk programs. Tests replace it with a fake; nothing else touches a disk.
var arkPrepTools = func() prep.Tools { return prep.ExecTools{} }

// arkPrepSystemPaths are the directories whose disk is never offered or touched (with the targets of
// the ones that are symbolic links).
var arkPrepSystemPaths = func() []string {
	return prep.SystemPaths(prep.DefaultSystemPaths, filepath.EvalSymlinks)
}

// arkPrepPoll and arkPrepAgentWait are how often and how long a command looks at GET /ark/drives for a
// drive it has just mounted: the agent sees a new mount within a second, a new filesystem within the
// probe that follows.
var (
	arkPrepPoll      = time.Second
	arkPrepAgentWait = 45 * time.Second
)

const (
	arkFormatTimeout = 4 * time.Hour
	arkRepairTimeout = 7 * time.Hour
	arkNewIDTimeout  = 2 * time.Hour
	// eraseWord is what the person types to erase a disk. --yes never answers it.
	eraseWord = "ERASE"
)

func init() {
	attachAll(
		at(&Command{
			Name: "format", Summary: "Erase a new disk and make it an AI-ARK drive (it asks first).",
			Marks: MarkRoot, Timeout: arkFormatTimeout,
			Description: `Lists only the disks that can be erased safely: removable or USB disks with nothing open on them.
This computer's own disk, a disk with swap or Local AI data, and a disk the desktop opened are never
offered. Pick one by its number, type the admin PIN, then type ERASE. The disk is then erased, given one
ext4 partition named AI-ARK, mounted and set up for Local AI. --yes does not answer these questions and
--json cannot be used. Runbook 04 has the manual steps, as a last resort.`,
			Run: arkFormat,
		}, "ark"),
		at(&Command{
			Name: "repair", Summary: "Check and repair an AI-ARK drive that has filesystem errors.",
			Usage: "<root-id> [--yes]", Marks: MarkRoot, Timeout: arkRepairTimeout,
			Description: `Ejects the drive (it stays plugged in), runs a full filesystem check that repairs what it can,
mounts it again and says what was found. It can take from minutes to over an hour on a big drive; do not
unplug it meanwhile. Asks first (--yes answers). This is the fix of LAI-ARK-004.`,
			Flags: declareYes,
			Run:   arkRepair,
		}, "ark"),
		at(&Command{
			Name: "new-id", Summary: "Give a copied AI-ARK drive an id of its own.",
			Usage: "<root-id|mount-path> [--yes]", Marks: MarkRoot, Timeout: arkNewIDTimeout,
			Description: `When two drives have the same id (one was copied from the other, LAI-ARK-006), this gives the
copy that is not in use a new random id. Nothing on the drive is deleted. With both drives plugged in, give
the shared root id; or give the mount path of the copy. Asks first (--yes answers).`,
			Flags: declareYes,
			Run:   arkNewID,
		}, "ark"),
	)
}

// ---------------------------------------------------------------------------------------------------
// format

func arkFormat(c *Call) error {
	if err := c.exactArgs(0, ""); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	// The questions need a person: a pipe, --json or --yes cannot answer them.
	if err := c.requireTerminalForConfirm(true); err != nil {
		return err
	}
	tools := arkPrepTools()
	system := arkPrepSystemPaths()
	devs, err := tools.ListBlock(c.Ctx)
	if err != nil {
		return c.prepToolErr(err, "", "")
	}
	offered, refused := prep.Classify(devs, system)
	c.printRefused(refused)
	if len(offered) == 0 {
		return c.codeError(doctor.ARK016, ExitNotFound, "")
	}
	c.Out.Info(pick(c.Lang, "Disks that can be erased and made an AI-ARK drive:", "Discos que se pueden borrar y convertir en unidad AI-ARK:"))
	for i, d := range offered {
		c.Out.Result(fmt.Sprintf("  %d  %s", i+1, c.diskLine(d)))
	}

	// 1. The number.
	fmt.Fprint(c.Stderr, pick(c.Lang,
		"Type the number of the disk to erase, or press Enter to stop: ",
		"Escriba el número del disco que quiere borrar, o pulse Enter para no hacer nada: "))
	line, err := c.Term.ReadLine(c.Ctx)
	if err != nil {
		if perr := c.endPrompt(err); perr != nil {
			return perr
		}
		return c.nothingChanged()
	}
	n, aerr := strconv.Atoi(strings.TrimSpace(line))
	if aerr != nil || n < 1 || n > len(offered) {
		if strings.TrimSpace(line) != "" {
			c.Out.Warn(pick(c.Lang, fmt.Sprintf("%q is not one of the numbers above.", strings.TrimSpace(line)),
				fmt.Sprintf("%q no es uno de los números de arriba.", strings.TrimSpace(line))))
		}
		return c.nothingChanged()
	}
	disk := offered[n-1]

	// 2. The admin PIN, asked even as root: erasing a disk is the one thing that needs both.
	c.Out.Info(pick(c.Lang, "Erasing a disk needs the admin PIN.", "Borrar un disco necesita el PIN de administración."))
	if err := c.obtainGrant(); err != nil {
		return err
	}

	// 3. The word.
	ok, err := c.ConfirmTyped(pick(c.Lang,
		fmt.Sprintf("Everything on this disk will be erased:\n  %s\nThis cannot be undone. Type %s to erase it, or press Enter to stop:", c.diskLine(disk), eraseWord),
		fmt.Sprintf("Se borrará todo lo que hay en este disco:\n  %s\nNo se puede deshacer. Escriba %s para borrarlo, o pulse Enter para no hacer nada:", c.diskLine(disk), eraseWord)),
		eraseWord)
	if err != nil {
		return err
	}
	if !ok {
		return c.nothingChanged()
	}

	// 4. The same disk, still safe? A disk swapped or opened since the list is never erased.
	devs, err = tools.ListBlock(c.Ctx)
	if err != nil {
		return c.prepToolErr(err, "", "")
	}
	again, _ := prep.Classify(devs, system)
	var current prep.BlockDevice
	found := false
	for _, d := range again {
		if prep.SameDisk(disk, d) {
			current, found = d, true
		}
	}
	if !found {
		e := c.codeError(doctor.ARK016, ExitRefused, "")
		e.Message = pick(c.Lang, "The disk changed or was opened since it was listed, so nothing was erased.",
			"El disco cambió o se abrió después de listarlo, así que no se borró nada.")
		return e
	}

	work := c.diskWork()
	partlyEN := "The disk was partly erased; nothing else was touched. Run `sudo aictl ark format` again to finish."
	partlyES := "El disco quedó borrado a medias; no se tocó nada más. Ejecute otra vez `sudo aictl ark format` para terminar."
	// Old filesystem signatures inside the partitions first, so the new partition (which starts where the
	// old first one usually did) does not show an old filesystem the desktop could open.
	work.step(pick(c.Lang, "Erasing the old partitions and signatures", "Borrando las particiones y firmas anteriores"))
	for _, p := range prep.Partitions(current) {
		if err := tools.Wipe(work.ctx, p.Path); err != nil {
			return c.prepToolErr(err, partlyEN, partlyES)
		}
	}
	if err := tools.Wipe(work.ctx, current.Path); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	work.step(pick(c.Lang, "Creating one partition that fills the disk", "Creando una partición que ocupa todo el disco"))
	if err := tools.Partition(work.ctx, current.Path); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	if err := tools.Settle(work.ctx); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	devs, err = tools.ListBlock(work.ctx)
	if err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	_, after, okDisk := prep.Find(devs, current.Path)
	parts := prep.Partitions(after)
	if !okDisk || len(parts) != 1 || !prep.ValidDevice(parts[0].Path) || prep.Mounted(after) {
		return c.prepToolErr(&prep.ToolError{Tool: "parted", ExitCode: -1, Reason: "the new partition did not appear"}, partlyEN, partlyES)
	}
	part := parts[0].Path
	if err := tools.Wipe(work.ctx, part); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	work.step(pick(c.Lang, "Formatting it as ext4 named AI-ARK (this can take several minutes)",
		"Formateándolo como ext4 con el nombre AI-ARK (puede tardar varios minutos)"))
	if err := tools.MakeFS(work.ctx, part); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	if err := tools.Settle(work.ctx); err != nil {
		return c.prepToolErr(err, partlyEN, partlyES)
	}
	work.step(pick(c.Lang, "Mounting it", "Montándolo"))
	notMountedEN := "The disk is formatted as AI-ARK but is not mounted. Unplug it, plug it in again and run `aictl ark init` with its mount path."
	notMountedES := "El disco está formateado como AI-ARK pero no está montado. Desconéctelo, conéctelo de nuevo y ejecute `aictl ark init` con su ruta de montaje."
	if err := tools.Mount(work.ctx, part); err != nil {
		return c.prepToolErr(err, notMountedEN, notMountedES)
	}

	// 5. The agent's own set-up, as `aictl ark init` does it (the person already said ERASE).
	work.step(pick(c.Lang, "Setting it up for Local AI", "Preparándolo para Local AI"))
	ctx, cancel := context.WithTimeout(work.ctx, arkPrepAgentWait+arkDriveCmdTimeout)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	d, seen := c.waitDriveAt(ctx, cl, part, "")
	if !seen {
		e := c.codeError(doctor.ARK017, ExitFailed, "")
		e.Message = pick(c.Lang, "The agent did not list the new drive.", "El agente no listó la unidad nueva.")
		e.Fix = pick(c.Lang, "Unplug it, plug it in again, then run `aictl ark drives` and `aictl ark init` with its id.",
			"Desconéctela, conéctela de nuevo y ejecute `aictl ark drives` y `aictl ark init` con su id.")
		return e
	}
	if err := c.initable(d); err != nil {
		return err
	}
	if _, _, err := c.callWith(cl, ctx, http.MethodPost, "/ark/drives/"+d.id()+"/init", map[string]any{}, nil); err != nil {
		return err
	}
	work.end()
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("The new AI-ARK drive is ready (%s, mounted at %s). Fill it with `aictl ark sync --root %s --wait` (needs internet; leave it overnight).", d.id(), d.Mount, d.id()),
		fmt.Sprintf("La nueva unidad AI-ARK está lista (%s, montada en %s). Llénela con `aictl ark sync --root %s --wait` (necesita internet; déjela toda la noche).", d.id(), d.Mount, d.id())))
	return nil
}

// diskLine is one disk as the person sees it: path, model, size, bus and the names of what is on it.
func (c *Call) diskLine(d prep.BlockDevice) string {
	parts := []string{d.Path, orText(strings.TrimSpace(d.Model), pick(c.Lang, "disk", "disco")), sizeText(d.Size)}
	if d.Tran != "" {
		parts = append(parts, d.Tran)
	}
	var labels []string
	if d.Label != "" {
		labels = append(labels, d.Label)
	}
	for _, p := range d.Children {
		if p.Label != "" {
			labels = append(labels, p.Label)
		}
	}
	switch {
	case len(labels) > 0:
		parts = append(parts, pick(c.Lang, "named ", "nombre ")+strings.Join(labels, ", "))
	case len(d.Children) == 0 && d.FSType == "":
		parts = append(parts, pick(c.Lang, "empty", "vacío"))
	}
	return strings.Join(parts, "  ")
}

// printRefused says which disks are not offered and why, so a person whose disk is missing knows what to do.
func (c *Call) printRefused(refused []prep.Refused) {
	if len(refused) == 0 {
		return
	}
	c.Out.Info(pick(c.Lang, "Not offered:", "No se ofrecen:"))
	for _, r := range refused {
		var why string
		switch {
		case r.Reason == prep.ReasonSystem && r.Mount == prep.Swap:
			why = pick(c.Lang, "this computer uses it as swap", "esta computadora lo usa como memoria de intercambio")
		case r.Reason == prep.ReasonSystem:
			why = pick(c.Lang, "this computer's own disk (it holds "+r.Mount+")", "el disco de esta computadora (tiene "+r.Mount+")")
		case r.Reason == prep.ReasonMounted:
			why = pick(c.Lang, "open at "+r.Mount+"; eject it in the file manager first", "abierto en "+r.Mount+"; expúlselo primero en el gestor de archivos")
		case r.Reason == prep.ReasonInUse:
			why = pick(c.Lang, "in use (encrypted, RAID or LVM)", "en uso (cifrado, RAID o LVM)")
		case r.Reason == prep.ReasonInternal:
			why = pick(c.Lang, "a disk inside this computer", "un disco dentro de esta computadora")
		case r.Reason == prep.ReasonNoMedia:
			why = pick(c.Lang, "no media (an empty card reader)", "sin medio (un lector de tarjetas vacío)")
		default:
			why = pick(c.Lang, "its device name cannot be used", "su nombre de dispositivo no se puede usar")
		}
		c.Out.Result(fmt.Sprintf("  %s  %s: %s", r.Disk.Path, orText(strings.TrimSpace(r.Disk.Model), pick(c.Lang, "disk", "disco")), why))
	}
}

// ---------------------------------------------------------------------------------------------------
// repair

func arkRepair(c *Call) error {
	applyYes(c)
	if err := c.exactArgs(1, "<root-id>"); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	drives, err := c.listDrives(c.Ctx, cl)
	if err != nil {
		return err
	}
	d, err := c.findDrive(drives, c.Args[0])
	if err != nil {
		return err
	}
	if err := c.prepRefuse(d); err != nil {
		return err
	}
	tools := arkPrepTools()
	system := arkPrepSystemPaths()
	if err := c.checkArkDevice(tools, d.Device, d.Mount, system); err != nil {
		return err
	}
	ok, err := c.Confirm(pick(c.Lang,
		fmt.Sprintf("Check and repair %s (%s)? It is taken out of use while the check runs, from minutes to over an hour. Do not unplug it meanwhile.", d.name(), d.id()),
		fmt.Sprintf("¿Comprobar y reparar %s (%s)? Deja de usarse mientras dura la comprobación, de minutos a más de una hora. No la desconecte mientras tanto.", d.name(), d.id())))
	if err != nil {
		return err
	}
	if !ok {
		return c.nothingChanged()
	}

	work := c.diskWork()
	if d.Mount != "" {
		work.step(pick(c.Lang, "Ejecting the drive (it stays plugged in)", "Expulsando la unidad (sigue conectada)"))
		if err := c.ejectKeepPowered(work.ctx, cl, d.id()); err != nil {
			return err
		}
	}
	if err := c.checkArkDevice(tools, d.Device, "", system); err != nil {
		return err
	}
	res, code, err := c.runCheck(work, tools, d.Device)
	mounted := c.remount(work, tools, cl, d.Device, "")
	if err != nil {
		return c.prepToolErr(err, c.remountedText(mounted), c.remountedTextES(mounted))
	}
	work.end()
	if err := c.checkFailure(d, res, mounted); err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"root_id": d.id(), "device": d.Device, "result": string(res), "fsck_exit": code, "mount": mounted.Mount})
	}
	if res == prep.CheckFixed {
		c.Out.OK(pick(c.Lang, "Errors were found and fixed. "+d.name()+" is mounted again at "+mounted.Mount+".",
			"Se encontraron errores y se arreglaron. "+d.name()+" está montada de nuevo en "+mounted.Mount+"."))
		return nil
	}
	c.Out.OK(pick(c.Lang, "No errors were found. "+d.name()+" is mounted again at "+mounted.Mount+".",
		"No se encontraron errores. "+d.name()+" está montada de nuevo en "+mounted.Mount+"."))
	return nil
}

// checkFailure is the error of a check that left errors, could not run, or whose drive could not be
// mounted again; nil when the drive is checked and back in use.
func (c *Call) checkFailure(d wireDrive, res prep.CheckOutcome, mounted wireDrive) error {
	switch res {
	case prep.CheckErrorsLeft:
		e := c.codeError(doctor.ARK018, ExitFailed, "")
		e.Message = pick(c.Lang, "The check fixed what it could, but some errors are left on "+d.name()+". "+c.remountedText(mounted),
			"La comprobación arregló lo que pudo, pero quedan errores en "+d.name()+". "+c.remountedTextES(mounted))
		return e
	case prep.CheckFailed:
		e := c.codeError(doctor.ARK017, ExitFailed, "")
		e.Message = pick(c.Lang, "The check of "+d.name()+" could not run to the end. "+c.remountedText(mounted),
			"La comprobación de "+d.name()+" no pudo terminar. "+c.remountedTextES(mounted))
		return e
	}
	if mounted.Mount == "" {
		e := c.codeError(doctor.ARK017, ExitFailed, "")
		e.Message = pick(c.Lang, "The check finished, but the drive could not be mounted again.", "La comprobación terminó, pero la unidad no se pudo montar de nuevo.")
		e.Fix = pick(c.Lang, "Unplug it and plug it in again.", "Desconéctela y conéctela de nuevo.")
		return e
	}
	return nil
}

func (c *Call) remountedText(m wireDrive) string {
	if m.Mount != "" {
		return "The drive is mounted again at " + m.Mount + "."
	}
	return "The drive is not mounted; unplug it and plug it in again."
}

func (c *Call) remountedTextES(m wireDrive) string {
	if m.Mount != "" {
		return "La unidad está montada de nuevo en " + m.Mount + "."
	}
	return "La unidad no está montada; desconéctela y conéctela de nuevo."
}

// ---------------------------------------------------------------------------------------------------
// new-id

func arkNewID(c *Call) error {
	applyYes(c)
	if err := c.exactArgs(1, "<root-id|mount-path>"); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	drives, err := c.listDrives(c.Ctx, cl)
	if err != nil {
		return err
	}
	d, err := c.findCopy(drives, c.Args[0])
	if err != nil {
		return err
	}
	tools := arkPrepTools()
	system := arkPrepSystemPaths()
	if err := c.checkArkDevice(tools, d.Device, d.Mount, system); err != nil {
		return err
	}
	where := d.Device
	if d.Mount != "" {
		where = d.Mount
	}
	ok, err := c.Confirm(pick(c.Lang,
		fmt.Sprintf("Give the copy at %s a new id? The other drive keeps %s. Nothing on the drive is deleted.", where, d.id()),
		fmt.Sprintf("¿Dar un id nuevo a la copia en %s? La otra unidad conserva %s. No se borra nada de la unidad.", where, d.id())))
	if err != nil {
		return err
	}
	if !ok {
		return c.nothingChanged()
	}

	work := c.diskWork()
	if d.Mount != "" {
		// Not through the agent's eject: the agent would eject the twin that has the same root id and is
		// in use. The copy is unusable, so nothing of the agent holds it.
		work.step(pick(c.Lang, "Unmounting the copy", "Desmontando la copia"))
		if err := tools.Unmount(work.ctx, d.Device); err != nil {
			return c.prepToolErr(err, "Nothing was changed.", "No se cambió nada.")
		}
	}
	if err := c.checkArkDevice(tools, d.Device, "", system); err != nil {
		return err
	}
	// tune2fs changes the id only on a freshly checked filesystem.
	res, code, err := c.runCheck(work, tools, d.Device)
	if err == nil && (res == prep.CheckErrorsLeft || res == prep.CheckFailed) {
		err = &prep.ToolError{Tool: "fsck.ext4", ExitCode: code, Reason: string(res)}
	}
	if err != nil {
		mounted := c.remount(work, tools, cl, d.Device, "")
		return c.prepToolErr(err, "The id was not changed. "+c.remountedText(mounted), "No se cambió el id. "+c.remountedTextES(mounted))
	}
	work.step(pick(c.Lang, "Giving it a new id", "Dándole un id nuevo"))
	if err := tools.NewUUID(work.ctx, d.Device); err != nil {
		mounted := c.remount(work, tools, cl, d.Device, "")
		return c.prepToolErr(err, c.remountedText(mounted), c.remountedTextES(mounted))
	}
	mounted := c.remount(work, tools, cl, d.Device, d.id())
	if mounted.Mount == "" {
		e := c.codeError(doctor.ARK017, ExitFailed, "")
		e.Message = pick(c.Lang, "The copy has a new id, but it could not be mounted again.", "La copia tiene un id nuevo, pero no se pudo montar de nuevo.")
		e.Fix = pick(c.Lang, "Unplug it and plug it in again; `aictl ark drives` then shows its new id.",
			"Desconéctela y conéctela de nuevo; `aictl ark drives` mostrará su id nuevo.")
		return e
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"old_root_id": d.id(), "root_id": mounted.id(), "device": d.Device, "mount": mounted.Mount})
	}
	work.end()
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("The copy now has its own id, %s, and is mounted at %s. Both drives can be used.", mounted.id(), mounted.Mount),
		fmt.Sprintf("La copia ya tiene su propio id, %s, y está montada en %s. Se pueden usar las dos unidades.", mounted.id(), mounted.Mount)))
	return nil
}

// findCopy resolves the argument of new-id to the one drive that shows LAI-ARK-006: by root id (both
// clones match; the one in use has no LAI-ARK-006), by mount path or by device.
func (c *Call) findCopy(drives []wireDrive, arg string) (wireDrive, error) {
	var match, copies []wireDrive
	for _, d := range drives {
		if arg != "" && (d.id() == arg || d.Device == arg ||
			d.Mount != "" && strings.HasPrefix(arg, "/") && path.Clean(d.Mount) == path.Clean(arg)) {
			match = append(match, d)
			if d.has(doctor.ARK006) {
				copies = append(copies, d)
			}
		}
	}
	switch {
	case len(match) == 0:
		return wireDrive{}, c.driveNotFound(fmt.Sprintf("No ark drive matches %q.", arg), fmt.Sprintf("Ninguna unidad del arca coincide con %q.", arg),
			"`aictl ark drives` lists the drives with their mount path and id.", "`aictl ark drives` lista las unidades con su ruta de montaje y su id.")
	case len(copies) == 0:
		e := c.codeError(doctor.AGT020, ExitRefused, "")
		e.Message = pick(c.Lang, "This drive does not share its id with another drive, so nothing was changed.",
			"Esta unidad no comparte su id con otra, así que no se cambió nada.")
		e.Fix = pick(c.Lang, "Plug in both copies, then run it again with the id `aictl ark drives` shows twice.",
			"Conecte las dos copias y ejecútelo otra vez con el id que `aictl ark drives` muestra dos veces.")
		return wireDrive{}, e
	case len(copies) > 1:
		return wireDrive{}, c.usageErr(
			pick(c.Lang, fmt.Sprintf("More than one copy matches %q.", arg), fmt.Sprintf("Más de una copia coincide con %q.", arg)),
			pick(c.Lang, "Use the mount path of the copy, from `aictl ark drives`.", "Use la ruta de montaje de la copia, de `aictl ark drives`."))
	}
	return copies[0], nil
}

// ---------------------------------------------------------------------------------------------------
// shared

// prepRefuse refuses, before any question, a drive repair cannot work on.
func (c *Call) prepRefuse(d wireDrive) error {
	refuse := func(en, es, fixEN, fixES string) error {
		e := c.codeError(doctor.AGT020, ExitRefused, "")
		e.Message, e.Fix = pick(c.Lang, en, es), pick(c.Lang, fixEN, fixES)
		return e
	}
	name := d.name()
	switch {
	case d.has(doctor.ARK006):
		return refuse(name+" has the same id as another drive.", name+" tiene el mismo id que otra unidad.",
			"Give it its own id first: `sudo aictl ark new-id "+d.id()+"`.", "Dele primero su propio id: `sudo aictl ark new-id "+d.id()+"`.")
	case d.State == "unusable" || d.id() == "":
		return refuse(name+" is not an ext4 AI-ARK drive, so it cannot be checked.", name+" no es una unidad AI-ARK ext4, así que no se puede comprobar.",
			"To use it, format it: `sudo aictl ark format` (this erases it).", "Para usarla, formatéela: `sudo aictl ark format` (esto la borra).")
	case d.State == "ejecting":
		return refuse(name+" is being ejected.", name+" se está expulsando.", "Wait for the eject to finish.", "Espere a que termine la expulsión.")
	}
	return nil
}

// checkArkDevice checks with lsblk that dev is an ext4 AI-ARK filesystem on a disk that holds nothing of
// the system and has nothing else mounted (ownMount, the drive's own mount point, is allowed).
func (c *Call) checkArkDevice(tools prep.Tools, dev, ownMount string, system []string) error {
	devs, err := tools.ListBlock(c.Ctx)
	if err != nil {
		return c.prepToolErr(err, "Nothing was changed.", "No se cambió nada.")
	}
	refuse := func(en, es string) error {
		e := c.codeError(doctor.AGT020, ExitRefused, "")
		e.Message = pick(c.Lang, en+" Nothing was changed.", es+" No se cambió nada.")
		e.Fix = pick(c.Lang, "Check the drive with `aictl ark drives`.", "Revise la unidad con `aictl ark drives`.")
		return e
	}
	switch prep.CheckDrive(devs, dev, ownMount, system) {
	case prep.DriveOK:
		return nil
	case prep.DriveUnknown:
		return refuse(dev+" is not plugged in.", dev+" no está conectado.")
	case prep.DriveNotArk:
		return refuse(dev+" is not an ext4 filesystem named AI-ARK.", dev+" no es un sistema de archivos ext4 llamado AI-ARK.")
	case prep.DriveSystem:
		return refuse(dev+" is on this computer's own disk and is never touched.", dev+" está en el disco de esta computadora y nunca se toca.")
	}
	return refuse(dev+" is still mounted, or its disk has another filesystem open.", dev+" sigue montado, o su disco tiene otro sistema de archivos abierto.")
}

// ejectKeepPowered ejects the drive through the agent (every user lets go of it) without powering the
// disk off, and waits for the unmount.
func (c *Call) ejectKeepPowered(ctx context.Context, cl *Client, root string) error {
	ctx, cancel := context.WithTimeout(ctx, arkDriveCmdTimeout)
	defer cancel()
	if _, _, err := c.callWith(cl, ctx, http.MethodPost, "/ark/drives/"+root+"/eject", map[string]any{"keep_powered": true}, nil); err != nil {
		c.printBlockers(err, true)
		return err
	}
	return c.waitUnmounted(ctx, cl, root)
}

// runCheck runs fsck.ext4 -f -y with a progress line per pass and per tenth.
func (c *Call) runCheck(work *diskWork, tools prep.Tools, dev string) (prep.CheckOutcome, int, error) {
	work.step(pick(c.Lang, "Checking the drive (do not unplug it)", "Comprobando la unidad (no la desconecte)"))
	namesEN := []string{"", "files", "folders", "folder links", "file links", "free space"}
	namesES := []string{"", "archivos", "carpetas", "enlaces de carpetas", "enlaces de archivos", "espacio libre"}
	last := -1
	code, err := tools.Check(work.ctx, dev, func(p prep.CheckProgress) {
		key := p.Pass*100 + p.Percent()/10*10
		if key == last {
			return
		}
		last = key
		work.check()
		c.Out.Progress(pick(c.Lang,
			fmt.Sprintf("Checking step %d of 5 (%s): %d%%", p.Pass, namesEN[p.Pass], p.Percent()/10*10),
			fmt.Sprintf("Comprobando paso %d de 5 (%s): %d%%", p.Pass, namesES[p.Pass], p.Percent()/10*10)))
	})
	if err != nil {
		return prep.CheckFailed, code, err
	}
	return prep.CheckResult(code), code, nil
}

// remount mounts dev again and waits for the agent to list it; the drive is empty when it did not work.
func (c *Call) remount(work *diskWork, tools prep.Tools, cl *Client, dev, notID string) wireDrive {
	work.step(pick(c.Lang, "Mounting the drive again", "Montando la unidad de nuevo"))
	if err := tools.Mount(work.ctx, dev); err != nil {
		c.Out.Warn(pick(c.Lang, "The drive could not be mounted: "+err.Error(), "No se pudo montar la unidad: "+err.Error()))
		return wireDrive{}
	}
	ctx, cancel := context.WithTimeout(work.ctx, arkPrepAgentWait+5*time.Second)
	defer cancel()
	d, _ := c.waitDriveAt(ctx, cl, dev, notID)
	return d
}

// waitDriveAt polls GET /ark/drives until the drive on device is mounted and has a root id other than
// notID. A list that fails is tried again: the agent may be busy with the new drive.
func (c *Call) waitDriveAt(ctx context.Context, cl *Client, device, notID string) (wireDrive, bool) {
	deadline := time.Now().Add(arkPrepAgentWait)
	for {
		if drives, err := c.listDrives(ctx, cl); err == nil {
			for _, d := range drives {
				if d.Device == device && d.Mount != "" && d.id() != "" && d.id() != notID {
					return d, true
				}
			}
		}
		if time.Now().After(deadline) {
			return wireDrive{}, false
		}
		select {
		case <-ctx.Done():
			return wireDrive{}, false
		case <-time.After(arkPrepPoll):
		}
	}
}

// prepToolErr is LAI-ARK-017 for a disk program that did not work, with what it left changed.
func (c *Call) prepToolErr(err error, changedEN, changedES string) error {
	var te *prep.ToolError
	if !errors.As(err, &te) {
		return err
	}
	e := c.codeError(doctor.ARK017, ExitFailed, "")
	if errors.Is(err, prep.ErrMissing) {
		pkg := map[string]string{"lsblk": "util-linux", "wipefs": "util-linux", "parted": "parted", "udevadm": "udev",
			"mkfs.ext4": "e2fsprogs", "fsck.ext4": "e2fsprogs", "tune2fs": "e2fsprogs", "udisksctl": "udisks2"}[te.Tool]
		e.Message = pick(c.Lang, "The program "+te.Tool+" is not installed on this computer. "+changedEN,
			"El programa "+te.Tool+" no está instalado en esta computadora. "+changedES)
		e.Fix = pick(c.Lang, "Install it from the rebuild kit: `sudo aictl kit install-debs --packages "+pkg+"`.",
			"Instálelo desde el kit de reconstrucción: `sudo aictl kit install-debs --packages "+pkg+"`.")
		return e
	}
	reason := te.Reason
	if reason == "" && te.Err != nil {
		reason = te.Err.Error()
	}
	if reason == "" && te.ExitCode >= 0 {
		reason = "exit " + strconv.Itoa(te.ExitCode)
	}
	e.Message = strings.TrimSpace(pick(c.Lang, te.Tool+" did not work ("+reason+"). "+changedEN,
		te.Tool+" no funcionó ("+reason+"). "+changedES))
	return e
}

func (c *Call) nothingChanged() error {
	c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
	return nil
}

// diskWork is the part of a command after the person said yes: its context is not stopped by Ctrl-C, and
// each step is announced. When Ctrl-C was pressed the command says, once, that it goes on to the end.
type diskWork struct {
	c      *Call
	ctx    context.Context
	warned bool
}

func (c *Call) diskWork() *diskWork {
	c.Out.Warn(pick(c.Lang, "Do not unplug the drive or turn the computer off until this says it is done.",
		"No desconecte la unidad ni apague la computadora hasta que esto diga que terminó."))
	return &diskWork{c: c, ctx: context.WithoutCancel(c.Ctx)}
}

// check says once that Ctrl-C does not stop the disk work.
func (w *diskWork) check() {
	if w.warned || w.c.Ctx.Err() == nil {
		return
	}
	w.warned = true
	w.c.Out.Warn(pick(w.c.Lang, "Stopping now would leave the drive half done, so this goes on to the end.",
		"Parar ahora dejaría la unidad a medias, así que esto sigue hasta el final."))
}

func (w *diskWork) step(msg string) {
	w.check()
	w.c.Out.Progress(msg)
}

func (w *diskWork) end() { w.check() }
