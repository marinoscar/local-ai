package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// `aictl verify-release <dir>` (aictl.md section 4.27a, AICTL-10): check a downloaded or kit release
// directory offline. It is update.VerifyDir, the one verifier of self-update and of the kit, so there
// is no second minisign implementation here; this file is the argument, the kit-directory rule, the
// words, the --json document and the exit code. It needs no agent, no network and no root.

// verifyReleaseTimeout is the default deadline (aictl.md section 4.27a).
const verifyReleaseTimeout = 60 * time.Second

// CodeReleaseBad is LAI-AGT-010: a release directory failed its signature or checksum check. A kit
// directory raises CodeKitSignature (LAI-KIT-044, cmd_kit.go) for the same failure.
const CodeReleaseBad = doctor.AGT010

// verifyReleaseTrust returns the trusted release public keys: the embedded ones. Tests replace it.
var verifyReleaseTrust = update.EmbeddedKeys

func init() {
	Register(&Command{
		Name:    "verify-release",
		Summary: "Check a release folder (signature and checksums) with no internet.",
		Usage:   "<dir>",
		Description: `Checks a downloaded or kit release folder on this computer, with no agent and no internet: the
signature in SHA256SUMS.minisig against the release key built into this program, then every file listed
in SHA256SUMS. Use it for the desktop .deb and the agent files before you install them.
Exit 0: signature and every file are right. Exit 1: a file does not match, the signature is wrong, or it
was made by a key this program does not know (LAI-AGT-010; LAI-KIT-044 when <dir> is a kit folder: a
folder that holds kit/MANIFEST.json, or a kit/ folder with a MANIFEST.json). Exit 2: SHA256SUMS or
SHA256SUMS.minisig is missing.
Put the global option --json before the command name: aictl --json verify-release <dir>. It prints
{"signature": "ok|bad", "key_id", "files": [{"name", "status": "ok|mismatch|missing"}], "ok"}.
Specification: docs/specs/aictl.md section 4.27a`,
		Marks:   MarkLocal,
		Timeout: verifyReleaseTimeout,
		Run:     runVerifyRelease,
	})
}

// releaseFile is one file of the --json document.
type releaseFile struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// releaseReport is the --json document of verify-release.
type releaseReport struct {
	Signature string        `json:"signature"`
	KeyID     string        `json:"key_id"`
	Files     []releaseFile `json:"files"`
	OK        bool          `json:"ok"`
}

// isKitDir applies the rule of aictl.md section 4.27a: dir holds kit/MANIFEST.json, or is itself a
// folder called kit that holds a MANIFEST.json.
func isKitDir(dir string) bool {
	if fileExists(filepath.Join(dir, kit.DirName, kit.IndexFileName)) {
		return true
	}
	abs, err := filepath.Abs(dir)
	return err == nil && filepath.Base(abs) == kit.DirName && fileExists(filepath.Join(dir, kit.IndexFileName))
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func runVerifyRelease(c *Call) error {
	if err := c.exactArgs(1, "<dir>"); err != nil {
		return err
	}
	dir := c.Args[0]
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return c.usageErr(
			fmt.Sprintf(pick(c.Lang, "%q is not a folder.", "%q no es una carpeta."), dir),
			pick(c.Lang, "Give the folder that holds SHA256SUMS and SHA256SUMS.minisig.",
				"Indique la carpeta que contiene SHA256SUMS y SHA256SUMS.minisig."))
	}
	code := CodeReleaseBad
	if isKitDir(dir) {
		code = CodeKitSignature
	}

	rep, verr := update.VerifyDir(update.OSFS{}, dir, verifyReleaseTrust(), update.DirOptions{})
	if errors.Is(verr, update.ErrNoSums) {
		return c.usageErr(
			fmt.Sprintf(pick(c.Lang, "SHA256SUMS or SHA256SUMS.minisig is missing in %q.", "Falta SHA256SUMS o SHA256SUMS.minisig en %q."), dir),
			pick(c.Lang, "Copy the whole release folder, including both files, and try again.",
				"Copie la carpeta completa del lanzamiento, con los dos archivos, e intente otra vez."))
	}

	doc := releaseReport{Signature: "bad", KeyID: rep.KeyID, Files: []releaseFile{}}
	if rep.KeyID != "" {
		doc.Signature = "ok" // VerifyDir fills KeyID only once the signature verified
	} else if sig, perr := readSignatureID(dir); perr == nil {
		doc.KeyID = sig
	}
	for _, f := range rep.Files {
		doc.Files = append(doc.Files, releaseFile(f))
	}
	doc.OK = verr == nil

	if c.Global.JSON {
		if err := c.Out.JSON().Document(doc); err != nil {
			return err
		}
		if verr != nil {
			return ExitWith(ExitFailed)
		}
		return nil
	}
	if verr == nil {
		line := fmt.Sprintf(pick(c.Lang,
			"Signature OK (Local AI release key). %d files checked: all match.",
			"Firma correcta (clave de publicación de Local AI). %d archivos comprobados: todos coinciden."), rep.Checked())
		if rep.Checked() == 1 {
			line = pick(c.Lang, "Signature OK (Local AI release key). 1 file checked: it matches.",
				"Firma correcta (clave de publicación de Local AI). 1 archivo comprobado: coincide.")
		}
		c.Out.OK(line)
		if c.Global.Verbose {
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Signed with key %s. Signed note: %s", "Firmada con la clave %s. Nota firmada: %s"), rep.KeyID, rep.TrustedComment))
		}
		return nil
	}
	return c.releaseFailure(code, rep, verr)
}

// releaseFailure words why a release did not verify: the code's own text and fix, then the reason in
// plain language (the files that do not match are named, at most ten).
func (c *Call) releaseFailure(code string, rep update.DirReport, verr error) error {
	var why string
	var mm *update.MismatchError
	switch {
	case errors.Is(verr, update.ErrNoKeys):
		why = pick(c.Lang, "This program has no release key built in, so nothing can be checked.",
			"Este programa no tiene ninguna clave de publicación incorporada, así que no se puede comprobar nada.")
	case errors.Is(verr, update.ErrUnknownKey):
		why = pick(c.Lang, "It was signed with a key this program does not know.", "Fue firmada con una clave que este programa no conoce.")
	case errors.Is(verr, update.ErrBadSignature):
		why = pick(c.Lang, "The signature is not valid: the checksum list was changed or the signature is damaged.",
			"La firma no es válida: la lista de sumas se modificó o la firma está dañada.")
	case errors.Is(verr, update.ErrMalformed):
		why = pick(c.Lang, "SHA256SUMS or its signature is not in the expected format.", "SHA256SUMS o su firma no tiene el formato esperado.")
	case errors.As(verr, &mm):
		names := mm.Files
		more := ""
		if len(names) > 10 {
			more = fmt.Sprintf(pick(c.Lang, " and %d more", " y %d más"), len(names)-10)
			names = names[:10]
		}
		why = fmt.Sprintf(pick(c.Lang, "The signature is right, but these files do not match their checksum or are missing: %s%s.",
			"La firma es correcta, pero estos archivos no coinciden con su suma o faltan: %s%s."), strings.Join(names, ", "), more)
	default:
		why = fmt.Sprintf(pick(c.Lang, "The release could not be read: %v.", "No se pudo leer el lanzamiento: %v."), verr)
	}
	_ = rep
	return c.codeError(code, ExitFailed, why).WithCause(verr)
}

// readSignatureID returns the key id a signature file names, to say in the report which key a bad
// signature claims.
func readSignatureID(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, update.SigName))
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	sig, err := update.ParseSignature(buf[:n])
	if err != nil {
		return "", err
	}
	return update.KeyIDString(sig.KeyID), nil
}
