package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
)

// Shared parts of the local root commands (AICTL-07): `pin set`, `token rotate`, `catalog restore`,
// `catalog use --test` and `backup-config`. They run in this process as root, with no agent, and do
// their work through package local; this file holds the seam to the machine, the mapping of local's
// errors to doctor codes and the audit line.

// localEnv is the machine the local root commands work on (real paths, os/user, execx); tests
// replace it with temporary directories.
var localEnv = func() local.Env { return local.DefaultEnv(app.Version) }

// declareYes declares -y/--yes on a command whose synopsis shows it after the command name. The
// global flags are parsed only before the command word (aictl.md section 3.1), so without this
// `aictl catalog use ./catalog --test --yes` would be an unknown option.
func declareYes(fs *flag.FlagSet) {
	fs.Bool("yes", false, "answer yes to the confirmation")
	fs.Bool("y", false, "same as --yes")
}

// applyYes folds the command's own --yes into the global one Confirm reads.
func applyYes(c *Call) {
	if c.Bool("yes") || c.Bool("y") {
		c.Global.Yes = true
	}
}

// usageErr is a LAI-CLI-005 error (exit 2) with a message and a fix that fit the command.
func (c *Call) usageErr(message, fix string) *CLIError {
	e := c.Err(CodeUsage, message)
	e.Fix = fix
	return e
}

// reasonText translates the plain reasons of local.Reason; unknown text is shown as it is.
func reasonText(lang Lang, reason string) string {
	if lang != LangES {
		return reason
	}
	es := map[string]string{
		"the disk is full":                       "el disco está lleno",
		"the disk is read-only":                  "el disco es de solo lectura",
		"the drive is mounted read-only":         "el disco está montado como solo lectura",
		"permission denied":                      "permiso denegado",
		"it does not exist":                      "no existe",
		"it already exists":                      "ya existe",
		"it is not a regular file":               "no es un archivo normal",
		"part of the path is not a folder":       "una parte de la ruta no es una carpeta",
		"it is not a folder":                     "no es una carpeta",
		"it holds no node configuration to save": "no contiene configuración del nodo para guardar",
	}
	if t, ok := es[reason]; ok {
		return t
	}
	return reason
}

// localErr turns what package local returns into the error the command reports: a doctor code, a
// plain sentence naming the file and the reason, and the exit code of aictl.md section 3.4. An error
// it does not know is returned as it is (exit 1).
func localErr(c *Call, err error) error {
	var (
		fe  *local.FileError
		ge  *local.GroupError
		be  *local.BackupError
		rej *catalog.RejectedError
	)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rej):
		return catalogRejected(c, rej)
	case errors.As(err, &fe):
		verb := map[string][2]string{
			"read":   {"Could not read %s (%s).", "No se pudo leer %s (%s)."},
			"write":  {"Could not write %s (%s).", "No se pudo escribir %s (%s)."},
			"remove": {"Could not remove %s (%s).", "No se pudo borrar %s (%s)."},
		}[fe.Op]
		if verb[0] == "" {
			verb = [2]string{"Could not use %s (%s).", "No se pudo usar %s (%s)."}
		}
		msg := fmt.Sprintf(pick(c.Lang, verb[0], verb[1]), fe.Path, reasonText(c.Lang, fe.Reason()))
		return c.Err(CodeFileIO, msg).WithCause(err).WithDetails("path", fe.Path)
	case errors.As(err, &ge):
		e := c.Err(CodeFileIO, fmt.Sprintf(pick(c.Lang,
			"The group %q does not exist on this computer.", "El grupo %q no existe en este equipo."), ge.Name))
		e.Fix = pick(c.Lang, "Run `sudo install-node.sh --repair`, which creates it.",
			"Ejecute `sudo install-node.sh --repair`, que lo crea.")
		return e.WithCause(err)
	case errors.As(err, &be):
		return c.Err(CodeFileIO, pick(c.Lang, "This file cannot be restored: ", "Este archivo no se puede restaurar: ")+be.Reason+".").
			WithCause(err)
	case errors.Is(err, local.ErrBusy):
		e := c.Err(CodeFileIO, pick(c.Lang,
			"Another aictl command is changing the files of this node right now.",
			"Otro comando de aictl está cambiando los archivos de este nodo en este momento."))
		e.Fix = pick(c.Lang, "Wait for it to finish, then run this command again.",
			"Espere a que termine y vuelva a ejecutar este comando.")
		e.Exit = ExitRefused
		return e
	case errors.Is(err, local.ErrNoKit):
		return c.Err(CodeKitMissing, pick(c.Lang,
			"No ark kit with a catalog was found on this computer or on a mounted AI-ARK drive.",
			"No se encontró un kit del arca con catálogo en este equipo ni en un disco AI-ARK montado."))
	case errors.Is(err, local.ErrNoDrive):
		return c.Err(CodeNoDrive, "")
	}
	return err
}

// catalogRejected is LAI-CAT-002: the catalog failed validation. The first problem is in the message
// (the same line `catalog lint` prints) and up to ten are in the details (-v shows them).
func catalogRejected(c *Call, rej *catalog.RejectedError) *CLIError {
	var lines []any
	for i, p := range rej.Report.Problems {
		if i == 10 {
			break
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", p.Location(), p.Code, p.Message))
	}
	n := len(rej.Report.Problems)
	msg := pick(c.Lang, "The catalog failed validation, so the one in use was not changed.",
		"El catálogo no pasó la validación, así que no se cambió el que está en uso.")
	if len(lines) > 0 {
		msg += fmt.Sprintf(pick(c.Lang, " %d problem(s); first: %s", " %d problema(s); el primero: %s"), n, lines[0])
	}
	e := c.Err(catalog.CodeRejected, msg)
	e.Details = map[string]any{"problems": lines}
	return e
}

// codeOf is the doctor code of a command error ("" when it has none), for the audit line.
func codeOf(err error) string {
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// recordLocal appends the audit line of a local root command (actor cli-root, written with one
// write(2) on an O_APPEND descriptor and never rotated; only the agent rotates). opErr is the
// command's result: a failure is recorded as result "fail" with its code. If the audit log cannot
// be written the command is not failed (the action already happened); it says so on stderr.
func recordLocal(c *Call, env local.Env, action string, opErr error, target map[string]string) {
	result, code := audit.ResultOK, ""
	if opErr != nil {
		result, code = audit.ResultFail, codeOf(opErr)
	}
	if err := env.Audit(action, result, code, target); err != nil {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang,
			"The change was made, but it could not be written to the audit log. [%s]",
			"El cambio se hizo, pero no se pudo anotar en el registro de auditoría. [%s]"), audit.CodeUnwritable))
	}
}

// noExtraArgs is the usage error for a command that takes no positional arguments.
func noExtraArgs(c *Call) error {
	if len(c.Args) > 0 {
		return c.usageErr(fmt.Sprintf(pick(c.Lang, "Unexpected argument %q.", "Argumento inesperado %q."), strings.Join(c.Args, " ")),
			pick(c.Lang, "Run `aictl help "+strings.Join(c.Path, " ")+"`.", "Ejecute `aictl help "+strings.Join(c.Path, " ")+"`."))
	}
	return nil
}
