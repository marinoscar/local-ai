package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The state-changing `aictl ark` commands of AICTL-06 (aictl.md section 4.10, ark-sync.md section
// 3.4): sync (and --cancel), verify, import, and `drives set-profile`. Reading the ark (`status`,
// `drives`) is AICTL-04; eject, init and mount are SYNC-03; prune is SYNC-08.
//
// They join the `ark` group through RegisterLate (register_late.go), so the group may be created by
// whichever file defines it.

var (
	// rootIDRe is a root id: "internal" or "ark-" and eight hex digits (ark-sync.md section 2).
	rootIDRe = regexp.MustCompile(`^(internal|ark-[0-9a-f]{8})$`)
	// itemIDRe is a model or content id (catalog.md).
	itemIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)
	// profileIDRe is a profile id: the stem of profiles/<id>.yaml.
	profileIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

func init() {
	rootFlag := func(fs *flag.FlagSet) {
		fs.Var(&multiFlag{}, "root", "a root id (internal, ark-<8 hex>) or a path; may be repeated")
	}
	itemFlag := func(fs *flag.FlagSet) { fs.Var(&multiFlag{}, "item", "an item id; may be repeated") }
	attachAll(
		at(&Command{
			Name: "sync", Summary: "Download and update the offline content (the ark).",
			Usage: "[--root <id>]... [--item <id>]... [--dry-run] [--allow-offline-mode] [--scheduled] [--wait] | --cancel",
			Description: `Starts one sync run. Without --wait it returns as soon as the run has started.
  --wait                Follow the run and exit 0 (all items ok), 8 (some failed or were skipped,
                        the last good copies are kept) or 9 (the run failed or was stopped).
  --dry-run             Show what would be downloaded and change nothing.
  --allow-offline-mode  Allow a manual sync while the node is in offline mode (on a terminal,
                        the command asks instead).
  --scheduled           What the nightly timer runs: implies --wait; a run that is skipped (offline,
                        battery, other mode, turned off, another sync running) exits 0.
  --cancel              Stop the running sync (partial downloads stay and resume later).
Ctrl-C while following asks "Cancel the sync too?".`,
			Flags: func(fs *flag.FlagSet) {
				rootFlag(fs)
				itemFlag(fs)
				fs.Bool("dry-run", false, "show the plan and change nothing")
				fs.Bool("allow-offline-mode", false, "allow a manual sync in offline mode")
				fs.Bool("scheduled", false, "run as the nightly timer does (implies --wait; a skipped run exits 0)")
				fs.Bool("wait", false, "follow the run and exit 0, 8 or 9")
				fs.Bool("cancel", false, "stop the running sync")
			},
			Run: arkSync,
		}, "ark"),
		at(&Command{
			Name: "verify", Summary: "Check that the stored content matches its checksums.",
			Usage: "[--root <id>]... [--item <id>]... [--full] [--wait]",
			Description: `Re-hashes stored content. --full ignores the re-check interval and hashes everything.
Without --wait it returns as soon as the run has started.`,
			Flags: func(fs *flag.FlagSet) {
				rootFlag(fs)
				itemFlag(fs)
				fs.Bool("full", false, "hash everything, whatever its last check")
				fs.Bool("wait", false, "follow the run and exit 0, 8 or 9")
			},
			Run: arkVerify,
		}, "ark"),
		at(&Command{
			Name: "import", Summary: "Copy content from an ark drive to this computer (no internet needed).",
			Usage: "[--from <root-id>] [--item <id>]... [--add-to-profile] [--catalog] [--dry-run] [--wait]",
			Description: `Works in every mode, including survival. Without --from it uses the only connected drive; with
several, it asks which one on a terminal (and stops and lists them otherwise).
  --add-to-profile  Add an item that is not in this node's profile to the profile overrides.
  --catalog         Also install a newer catalog found on the drive.`,
			Flags: func(fs *flag.FlagSet) {
				fs.String("from", "", "the root id of the drive to copy from")
				itemFlag(fs)
				fs.Bool("add-to-profile", false, "add items that are not in the profile to the profile overrides")
				fs.Bool("catalog", false, "also install a newer catalog from the drive")
				fs.Bool("dry-run", false, "show the plan and change nothing")
				fs.Bool("wait", false, "follow the run and exit 0, 8 or 9")
			},
			Run: arkImport,
		}, "ark"),
		at(&Command{
			Name: "set-profile", Summary: "Choose the profile an ark drive follows.", Marks: MarkAdmin,
			Usage: "<root-id> <profile>", Timeout: 30 * time.Second,
			Run: arkSetProfile,
		}, "ark", "drives"),
	)
}

// checkIDs validates the values of --root and --item before anything is sent.
func (c *Call) rootsAndItems() (roots, items []string, err error) {
	roots, items = c.multi("root"), c.multi("item")
	for _, r := range roots {
		if err := c.checkRoot("--root", r, true); err != nil {
			return nil, nil, err
		}
	}
	for _, it := range items {
		if !itemIDRe.MatchString(it) {
			return nil, nil, c.usageErr(
				pick(c.Lang, fmt.Sprintf("--item %q is not an item id.", it), fmt.Sprintf("--item %q no es un identificador de elemento.", it)),
				pick(c.Lang, "Item ids are lower-case words like `wikipedia-es-nopic`.", "Los identificadores son palabras en minúsculas como `wikipedia-es-nopic`."))
		}
	}
	return roots, items, nil
}

// checkRoot accepts a root id, and (when paths is set) an absolute path.
func (c *Call) checkRoot(flagName, v string, paths bool) error {
	if rootIDRe.MatchString(v) || paths && strings.HasPrefix(v, "/") {
		return nil
	}
	return c.usageErr(
		pick(c.Lang, fmt.Sprintf("%s %q is not a root id.", flagName, v), fmt.Sprintf("%s %q no es un identificador de raíz.", flagName, v)),
		pick(c.Lang, "Use `internal` or `ark-` and eight hex digits; `aictl ark drives` lists them.",
			"Use `internal` o `ark-` y ocho dígitos hexadecimales; `aictl ark drives` los muestra."))
}

func arkSync(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	if c.Bool("cancel") {
		return arkSyncCancel(c)
	}
	roots, items, err := c.rootsAndItems()
	if err != nil {
		return err
	}
	scheduled := c.Bool("scheduled")
	trigger := "manual"
	if scheduled {
		trigger = "scheduled"
	}
	body := map[string]any{
		"roots": roots, "items": items, "dry_run": c.Bool("dry-run"),
		"allow_offline_mode": c.Bool("allow-offline-mode"), "trigger": trigger,
	}
	if c.Bool("dry-run") {
		return c.arkPlan("/ark/sync", body)
	}
	spec := arkRunSpec{kind: kindSync, path: "/ark/sync", body: body, wait: c.Bool("wait") || scheduled, scheduled: scheduled}
	err = c.arkRun(spec)
	if !scheduled && isOfflineRefusal(err) && c.canAsk() {
		// A person at a terminal is asked instead of being told to type a flag (ADR-0009).
		ok, cerr := c.Confirm(pick(c.Lang, "This node is offline. Sync anyway over the internet?",
			"Este nodo está sin conexión. ¿Sincronizar de todos modos por internet?"))
		if cerr != nil {
			return cerr
		}
		if !ok {
			c.Out.Info(pick(c.Lang, "Nothing was started. This computer stays in offline mode.",
				"No se inició nada. Este equipo sigue en modo sin conexión."))
			return nil
		}
		body["allow_offline_mode"] = true
		return c.arkRun(spec)
	}
	return err
}

func arkSyncCancel(c *Call) error {
	ctx, cancel := c.withDefaultTimeout(30 * time.Second)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return err
	}
	raw, _, err := c.callWith(cl, ctx, http.MethodDelete, "/ark/sync", nil, nil)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		"The sync is stopping. Downloads in progress are kept and continue next time.",
		"La sincronización se está deteniendo. Las descargas en curso se conservan y continúan la próxima vez."))
	return nil
}

func arkVerify(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	roots, items, err := c.rootsAndItems()
	if err != nil {
		return err
	}
	body := map[string]any{"roots": roots, "items": items, "full": c.Bool("full")}
	return c.arkRun(arkRunSpec{kind: kindVerify, path: "/ark/verify", body: body, wait: c.Bool("wait")})
}

func arkImport(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	_, items, err := c.rootsAndItems()
	if err != nil {
		return err
	}
	if c.Bool("add-to-profile") && len(items) == 0 {
		// "Add everything on the drive to the profile" is never what a person means: they name what they want.
		return c.usageErr(pick(c.Lang, "--add-to-profile needs at least one --item.", "--add-to-profile necesita al menos un --item."),
			pick(c.Lang, "Name what to add, for example `aictl ark import --item whisper-small --add-to-profile`.", "Indique qué añadir, por ejemplo `aictl ark import --item whisper-small --add-to-profile`."))
	}
	from := c.String("from")
	if from != "" {
		if err := c.checkRoot("--from", from, false); err != nil {
			return err
		}
	} else if from, err = c.onlyDrive(); errors.Is(err, errNoDriveChosen) {
		return nil
	} else if err != nil {
		return err
	}
	body := map[string]any{
		"from_root": from, "items": items, "add_to_profile": c.Bool("add-to-profile"),
		"catalog": c.Bool("catalog"), "dry_run": c.Bool("dry-run"),
	}
	if c.Bool("dry-run") {
		return c.arkPlan("/ark/import", body)
	}
	return c.arkRun(arkRunSpec{kind: kindImport, path: "/ark/import", body: body, wait: c.Bool("wait")})
}

// onlyDrive is the root id of the one connected ark drive: a drive with a root id that is ready to
// read from. None is "not found" (5). Several: a person at a terminal picks one from a numbered list
// (pickDrive); otherwise (no terminal, --json, --yes) it is a usage error (2) that lists them, the
// same list the agent would answer with LAI-ARK-015.
func (c *Call) onlyDrive() (string, error) {
	ctx, cancel := c.withDefaultTimeout(30 * time.Second)
	defer cancel()
	cl, err := c.Client()
	if err != nil {
		return "", err
	}
	all, err := c.listDrives(ctx, cl)
	if err != nil {
		return "", err
	}
	var usable []wireDrive
	var ids []string
	for _, d := range all {
		if d.id() != "" && (d.State == "ready" || d.State == "read_only") {
			usable = append(usable, d)
			ids = append(ids, d.id())
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		e := c.codeError(doctor.ARK001, ExitNotFound, "")
		e.Message = pick(c.Lang, "No ark drive is connected.", "No hay ninguna unidad del arca conectada.")
		e.Fix = pick(c.Lang, "Plug in the AI-ARK drive and check it with `aictl ark drives`.",
			"Conecte la unidad AI-ARK y revísela con `aictl ark drives`.")
		return "", e
	}
	several := c.codeError(doctor.ARK015, ExitUsage, pick(c.Lang, "Connected: ", "Conectadas: ")+strings.Join(ids, ", ")+".")
	if !c.canAsk() {
		return "", several
	}
	return c.pickDrive(usable, several)
}

func arkSetProfile(c *Call) error {
	if err := c.exactArgs(2, "<root-id> <profile>"); err != nil {
		return err
	}
	root, profile := c.Args[0], c.Args[1]
	if err := c.checkRoot("<root-id>", root, false); err != nil {
		return err
	}
	if !profileIDRe.MatchString(profile) {
		return c.usageErr(pick(c.Lang, fmt.Sprintf("%q is not a profile id.", profile), fmt.Sprintf("%q no es un identificador de perfil.", profile)),
			pick(c.Lang, "Profile ids are lower-case words like `ark-drive-full`.", "Los perfiles son palabras en minúsculas como `ark-drive-full`."))
	}
	var raw json.RawMessage
	err := c.Admin(func() (e error) {
		raw, _, e = c.call(http.MethodPut, "/ark/drives/"+root+"/profile", map[string]string{"profile": profile}, nil)
		return e
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("The drive %s now follows the profile %s. Run `aictl ark sync` with the drive connected to fill it.", root, profile),
		fmt.Sprintf("La unidad %s ahora sigue el perfil %s. Ejecute `aictl ark sync` con la unidad conectada para llenarla.", root, profile)))
	return nil
}
