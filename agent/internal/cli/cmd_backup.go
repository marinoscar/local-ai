package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// CodeNoConfigBackup: no config backup of this node in any kit config folder (LAI-KIT-050).
const CodeNoConfigBackup = doctor.KIT050

// restoreLatest is the --restore value that picks the newest backup of this node.
const restoreLatest = "latest"

// maxBackupChoices bounds the numbered list of --restore with no file.
const maxBackupChoices = 20

// `aictl backup-config` (aictl.md section 4.25): saves the node's configuration (config files,
// secrets unless --no-secrets, catalog, state, generated units) in one .tar.gz with a MANIFEST.json
// of sha256 sums, and `--restore` puts such a file back so a node can be rebuilt from the ark drive.
// Runs in this process as root, with no agent and no network. The command prints file counts and
// paths only, never a secret.

func init() {
	Register(&Command{
		Name:    "backup-config",
		Summary: "Save this node's configuration to a file, or restore it from one.",
		Usage:   "[--out <file>] [--to-ark] [--no-secrets] | --restore [<file> | latest] [--yes]",
		Description: `Creates localai-config-<node-id>-<time>.tar.gz (mode 0600, root) in the current directory, or at
--out, or with --to-ark in <drive>/localai/kit/config/<node-id>/ (the last 5 are kept). It holds
the files of /etc/localai (including keys and tokens unless --no-secrets), the catalog, the
state and the generated service files. Keep a file with secrets private: the ark drive is not
encrypted, so --to-ark asks before writing secrets to it.
--restore <file> checks the file, lists what it will replace, asks for confirmation, keeps each
replaced file as <file>.pre-restore, restores the files with their original modes and owners and
restarts the agent. --restore latest takes the newest backup of this node in localai/kit/config/
on this computer or a mounted AI-ARK drive; --restore alone, at a terminal, lists the backups
found there to choose one by its number. Works with no agent and no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.25`,
		Marks: MarkLocal | MarkRoot,
		Flags: func(fs *flag.FlagSet) {
			fs.String("out", "", "file (or folder) to write the backup to; default is the current directory")
			fs.Bool("to-ark", false, "write into the kit config folder of the mounted AI-ARK drive")
			fs.Bool("no-secrets", false, "leave out api-key, agent-token, cloud-credential, admin-pin and hf-token")
			fs.Var(&optionalValue{}, "restore", "restore a backup instead of making one: its file, latest (the newest of this node), or nothing to choose from a list")
			declareYes(fs)
		},
		Run: backupConfig,
	})
}

func backupConfig(c *Call) error {
	applyYes(c)
	restoring, restore := false, ""
	if v, ok := c.Flags.Lookup("restore").Value.(*optionalValue); ok && v.set {
		restoring, restore = true, v.val
		// `--restore <file>` leaves the file as an argument: --restore takes no value of its own so
		// that it can be given alone.
		if restore == "" && len(c.Args) == 1 {
			restore, c.Args = c.Args[0], nil
		}
	}
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if restoring {
		for _, f := range []string{"out", "to-ark", "no-secrets"} {
			if fl := c.Flags.Lookup(f); fl != nil && fl.Value.String() != "" && fl.Value.String() != "false" {
				return c.usageErr(fmt.Sprintf(pick(c.Lang, "--restore cannot be combined with --%s.", "--restore no se puede combinar con --%s."), f),
					pick(c.Lang, "Run `aictl help backup-config`.", "Ejecute `aictl help backup-config`."))
			}
		}
	}
	if c.Bool("to-ark") && c.String("out") != "" {
		return c.usageErr(pick(c.Lang, "--to-ark and --out cannot be used together.", "--to-ark y --out no se pueden usar juntos."),
			pick(c.Lang, "Run `aictl help backup-config`.", "Ejecute `aictl help backup-config`."))
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	if restoring {
		file, err := restoreFile(c, restore)
		if err != nil || file == "" {
			return err
		}
		return restoreConfig(c, file)
	}
	return saveConfig(c)
}

// optionalValue is a flag that may be given alone (`--restore`) or with a value (`--restore=<file>`;
// `--restore <file>` leaves the file as the command's one argument).
type optionalValue struct {
	set bool
	val string
}

func (v *optionalValue) String() string {
	if v == nil {
		return ""
	}
	return v.val
}

func (v *optionalValue) Set(s string) error {
	v.set = true
	if s != "true" {
		v.val = s
	}
	return nil
}

// IsBoolFlag lets the flag package accept --restore without a value.
func (v *optionalValue) IsBoolFlag() bool { return true }

// restoreFile turns the value of --restore into the file to restore: a file as given, `latest` (the
// newest backup of this node in a kit config folder), or, with no value, the one the person picks
// from a numbered list (a terminal only). "" with a nil error means the person chose nothing.
func restoreFile(c *Call, value string) (string, error) {
	if value != "" && value != restoreLatest {
		return value, nil
	}
	if value == "" && (c.Global.JSON || !c.Term.Interactive()) {
		return "", c.usageErr(pick(c.Lang, "--restore needs a backup file, or latest.", "--restore necesita un archivo de copia, o latest."),
			pick(c.Lang, "Run `sudo aictl backup-config --restore latest`, or give the file: `--restore <file>`.",
				"Ejecute `sudo aictl backup-config --restore latest`, o indique el archivo: `--restore <archivo>`."))
	}
	env := localEnv()
	node := local.NodeID(env)
	backups := local.FindBackups(local.FindRoots(c.Ctx, env))
	if value == restoreLatest {
		b, ok := local.LatestBackup(backups, node)
		if !ok {
			return "", noBackupErr(c, node, backups)
		}
		if !c.Global.JSON {
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Using the newest backup of %s, made %s: %s",
				"Se usa la copia más reciente de %s, hecha el %s: %s"), node, backupTime(b.Time), b.Path))
		}
		return b.Path, nil
	}
	return chooseBackup(c, node, backups)
}

// noBackupErr is LAI-KIT-050 (exit 5): no backup of node anywhere, naming the nodes that have some.
func noBackupErr(c *Call, node string, backups []local.BackupFile) error {
	e := c.codeError(CodeNoConfigBackup, ExitNotFound, "")
	e.Message = fmt.Sprintf(pick(c.Lang,
		"No backup of node %s was found in localai/kit/config/%s/ on this computer or on a mounted AI-ARK drive.",
		"No se encontró ninguna copia del nodo %s en localai/kit/config/%s/ en este equipo ni en un disco AI-ARK conectado."), node, node)
	var others []string
	for _, b := range backups {
		if !contains(others, b.NodeID) {
			others = append(others, b.NodeID)
		}
	}
	if len(others) > 0 {
		e.Message += " " + fmt.Sprintf(pick(c.Lang, "There are backups of %s: run `sudo aictl backup-config --restore` to choose one.",
			"Hay copias de %s: ejecute `sudo aictl backup-config --restore` para elegir una."), nameList(c.Lang, others))
	}
	return e.WithDetails("node", node)
}

// chooseBackup shows the backups found, this node's first and then the others, newest first, and
// reads the number of the one to restore (Enter is 1). The end of the input chooses nothing.
func chooseBackup(c *Call, node string, backups []local.BackupFile) (string, error) {
	var list []local.BackupFile
	for _, b := range backups {
		if b.NodeID == node {
			list = append(list, b)
		}
	}
	for _, b := range backups {
		if b.NodeID != node {
			list = append(list, b)
		}
	}
	if len(list) == 0 {
		return "", noBackupErr(c, node, nil)
	}
	if len(list) > maxBackupChoices {
		list = list[:maxBackupChoices]
	}
	fmt.Fprintln(c.Stderr, pick(c.Lang, "Backups found (newest first):", "Copias encontradas (la más reciente primero):"))
	for i, b := range list {
		where := pick(c.Lang, "this computer", "este equipo")
		if b.Root.Mount != "" {
			where = b.Root.Mount
		}
		mark := ""
		if b.NodeID != node {
			mark = pick(c.Lang, "  (another node)", "  (otro nodo)")
		}
		fmt.Fprintf(c.Stderr, "  %2d) %s  %s  %s%s\n", i+1, b.NodeID, backupTime(b.Time), where, mark)
	}
	for {
		fmt.Fprintf(c.Stderr, pick(c.Lang, "Which one? Type its number (1 to %d, Enter is 1): ", "¿Cuál? Escriba su número (1 a %d, Enter es 1): "), len(list))
		line, err := c.Term.ReadLine(c.Ctx)
		if err != nil {
			if e := c.endPrompt(err); e != nil {
				return "", e
			}
			c.Out.Info(pick(c.Lang, "Cancelled. Nothing was changed.", "Cancelado. No se cambió nada."))
			return "", nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return list[0].Path, nil
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(list) {
			return list[n-1].Path, nil
		}
		fmt.Fprintf(c.Stderr, pick(c.Lang, "Type a number from 1 to %d.\n", "Escriba un número del 1 al %d.\n"), len(list))
	}
}

// backupTime is "2026-11-02 15:04 UTC" for the time in a backup's name.
func backupTime(stamp string) string {
	t, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return stamp
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

func saveConfig(c *Call) error {
	env := localEnv()
	opts := local.BackupOptions{Out: c.String("out"), NoSecrets: c.Bool("no-secrets")}
	if c.Bool("to-ark") {
		drive, err := pickDrive(c, env)
		if err != nil {
			return err
		}
		opts.ToDrive = drive
		if !opts.NoSecrets {
			ok, err := c.Confirm(fmt.Sprintf(pick(c.Lang,
				"The AI-ARK drive at %s is not encrypted and this backup holds secrets (keys, the agent token, the PIN hash). Anyone with the drive can read them. Save it there anyway?",
				"El disco AI-ARK de %s no está cifrado y esta copia contiene secretos (claves, el token del agente, el hash del PIN). Quien tenga el disco puede leerlos. ¿Guardarla ahí de todos modos?"), drive.Mount))
			if err != nil {
				return err
			}
			if !ok {
				if c.Global.JSON {
					return c.Out.JSON().Document(map[string]any{"cancelled": true})
				}
				c.Out.Info(pick(c.Lang, "Cancelled. Nothing was written. Use --no-secrets to save without the secret files.",
					"Cancelado. No se escribió nada. Use --no-secrets para guardar sin los archivos secretos."))
				return nil
			}
		}
	}
	res, berr := local.Backup(env, opts)
	mapped := localErr(c, berr)
	target := map[string]string{}
	if res != nil {
		target["path"], target["node"] = res.Path, res.NodeID
		target["includes_secrets"] = strconv.FormatBool(res.SecretsIncluded)
	}
	recordLocal(c, env, local.ActionConfigBackup, mapped, target)
	if mapped != nil {
		return mapped
	}
	if res.PruneErr != nil {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang, "The backup is saved, but older backups could not all be removed: %s",
			"La copia se guardó, pero no se pudieron borrar todas las anteriores: %s"), plainPruneErr(c, res.PruneErr)))
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"path": res.Path, "node_id": res.NodeID, "files": res.Files, "secrets_included": res.SecretsIncluded})
	}
	if res.SecretsIncluded {
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "Saved %s (%d files, includes secrets — keep it private).",
			"Se guardó %s (%d archivos, incluye secretos: manténgalo en privado)."), res.Path, res.Files))
	} else {
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "Saved %s (%d files, no secrets).", "Se guardó %s (%d archivos, sin secretos)."), res.Path, res.Files))
	}
	if n := len(res.Pruned); n > 0 {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Removed %d older backups from the drive (the last %d are kept).",
			"Se borraron %d copias anteriores del disco (se conservan las últimas %d)."), n, local.KeepPerNode))
	}
	return nil
}

// plainPruneErr words the first prune failure without the Go error chain.
func plainPruneErr(c *Call, err error) string {
	if e, ok := localErr(c, err).(*CLIError); ok {
		return e.Message
	}
	return err.Error()
}

// pickDrive chooses the AI-ARK drive for --to-ark: the first writable one by mount path. No mounted
// drive is exit 5.
func pickDrive(c *Call, env local.Env) (*local.Root, error) {
	var drives []local.Root
	for _, r := range local.FindRoots(c.Ctx, env) {
		if r.Kind == local.KindDrive {
			drives = append(drives, r)
		}
	}
	if len(drives) == 0 {
		return nil, localErr(c, local.ErrNoDrive)
	}
	chosen := drives[0]
	for _, d := range drives {
		if !d.ReadOnly {
			chosen = d
			break
		}
	}
	if len(drives) > 1 {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Several AI-ARK drives are mounted; using the one at %s.",
			"Hay varios discos AI-ARK montados; se usa el de %s."), chosen.Mount))
	}
	return &chosen, nil
}

func restoreConfig(c *Call, file string) error {
	env := localEnv()
	plan, err := local.PrepareRestore(env, file)
	if err != nil {
		return localErr(c, err)
	}
	if plan.Changes() == 0 {
		if c.Global.JSON {
			return c.Out.JSON().Document(map[string]any{"restored": 0})
		}
		c.Out.OK(pick(c.Lang, "Nothing to restore: this node already matches the backup.",
			"No hay nada que restaurar: este nodo ya coincide con la copia."))
		return nil
	}
	if plan.Foreign {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang, "This backup is from node %s, and this node is %s.",
			"Esta copia es del nodo %s, y este nodo es %s."), plan.Manifest.NodeID, plan.ThisNode))
	}
	c.Out.Info(fmt.Sprintf(pick(c.Lang, "Backup of node %s made %s: %d files, %d will be written.",
		"Copia del nodo %s hecha el %s: %d archivos, se escribirán %d."), plan.Manifest.NodeID, plan.Manifest.CreatedAt, len(plan.Items), plan.Changes()))
	for _, it := range plan.Items {
		if it.Same {
			continue
		}
		line := it.Path
		switch {
		case it.Secret:
			line += pick(c.Lang, "  (secret)", "  (secreto)")
		case !it.Exists:
			line += pick(c.Lang, "  (new)", "  (nuevo)")
		}
		c.Out.Secondary("  " + line)
	}
	ok, err := c.Confirm(pick(c.Lang,
		"Replace these files with the backup? Each replaced file is kept as <file>.pre-restore and the agent is restarted.",
		"¿Reemplazar estos archivos con la copia? Cada archivo reemplazado se conserva como <archivo>.pre-restore y se reinicia el agente."))
	if err != nil {
		return err
	}
	if !ok {
		if c.Global.JSON {
			return c.Out.JSON().Document(map[string]any{"cancelled": true})
		}
		c.Out.Info(pick(c.Lang, "Cancelled. Nothing was changed.", "Cancelado. No se cambió nada."))
		return nil
	}

	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	res, aerr := plan.Apply(c.Ctx, env)
	release()
	mapped := localErr(c, aerr)
	target := map[string]string{"path": file, "node": plan.Manifest.NodeID}
	if res != nil {
		target["files"] = strconv.Itoa(res.Written)
	}
	recordLocal(c, env, local.ActionConfigRestore, mapped, target)
	if mapped != nil {
		return mapped
	}
	restarted := res.ServiceErr == nil
	if c.Global.JSON {
		if err := c.Out.JSON().Document(map[string]any{"restored": res.Written, "kept": len(res.Kept), "restarted": restarted}); err != nil {
			return err
		}
	} else if restarted {
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Restored %d files from %s. The replaced files are kept as *.pre-restore. The agent was restarted.",
			"Se restauraron %d archivos desde %s. Los archivos reemplazados se conservan como *.pre-restore. Se reinició el agente."), res.Written, file))
	}
	if !restarted {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang,
			"Restored %d files, but the agent could not be restarted. Run `sudo systemctl daemon-reload && sudo systemctl restart localai-agent`.",
			"Se restauraron %d archivos, pero no se pudo reiniciar el agente. Ejecute `sudo systemctl daemon-reload && sudo systemctl restart localai-agent`."), res.Written))
		return ExitWith(ExitFailed)
	}
	return nil
}
