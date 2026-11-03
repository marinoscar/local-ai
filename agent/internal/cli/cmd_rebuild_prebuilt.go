package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// The ready-made build half of the rebuild commands (rebuild-kit.md section 4.2, KIT-05), shared by
// `aictl rebuild-whisper` (cmd_rebuild_whisper.go) and, when its command surface lands, `aictl
// rebuild-llama` (AICTL-09): reading the record of the file named by --prebuilt, refusing a build that
// does not fit this computer and saying what the kit has instead, the confirmation, and the offer of a
// file that fits exactly. The restore itself (verification, placement, smoke test, activation and
// rollback) is the engine's (build.Request.Prebuilt).

// prebuiltCmd names a rebuild command for the shared words.
type prebuiltCmd struct {
	// Program is the kit name (kit.ProgramWhisper), Label how the messages call it, Command the aictl
	// command that restores it.
	Program, Label, Command string
}

var whisperPrebuiltCmd = prebuiltCmd{Program: kit.ProgramWhisper, Label: "whisper.cpp", Command: "rebuild-whisper"}

// restoreEngine is the part of the engine the preparation needs.
type restoreEngine interface {
	Resolve(ctx context.Context, req build.Request) (build.Inputs, error)
}

// restorePlan is a verified choice: the request to give the engine, the record of the file and the
// resolved inputs of this computer.
type restorePlan struct {
	Req      build.Request
	Rec      kit.PrebuiltRecord
	Recorded bool
	In       build.Inputs
}

// prepareRestore reads the record of req.Prebuilt, resolves this computer's backend and compute
// capability, refuses a build that does not fit (LAI-KIT-048, exit 1) or that the kit does not list
// (LAI-KIT-001), warns about a file the kit does not list, and asks for confirmation (not with
// --dry-run, which changes nothing). Resolve errors are returned through resolveErr, the command's own
// mapping of engine errors.
func prepareRestore(c *Call, pc prebuiltCmd, eng restoreEngine, host kitHost, roots []string, req build.Request,
	resolveErr func(*Call, error) error) (restorePlan, error) {
	rec, recorded, err := kit.LoadPrebuilt(host.FS, req.Prebuilt)
	if err != nil {
		return restorePlan{}, prebuiltErr(c, pc, req.Prebuilt, err)
	}
	rq := req
	rq.Prebuilt, rq.PrebuiltSHA256, rq.DryRun = "", "", false
	if rq.Commit == "" {
		rq.Commit = rec.Commit
	}
	in, err := eng.Resolve(c.Ctx, rq)
	if err != nil {
		return restorePlan{}, resolveErr(c, err)
	}
	want, err := prebuiltWant(host, pc.Program, req.Commit, in.Backend, in.CudaArch)
	if err != nil {
		return restorePlan{}, prebuiltErr(c, pc, req.Prebuilt, err)
	}
	if err := kit.CheckPrebuilt(host.FS, roots, rec, want); err != nil {
		return restorePlan{}, prebuiltErr(c, pc, req.Prebuilt, err)
	}
	if !recorded {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang,
			"%s is not in a kit's list of ready-made builds, so only the checksums inside it vouch for it.",
			"%s no está en la lista de compilaciones listas de un kit, así que solo lo respaldan las sumas de comprobación que lleva dentro."), filepath.Base(req.Prebuilt)))
	}
	out := restorePlan{Rec: rec, Recorded: recorded, In: in, Req: req}
	out.Req.Backend = in.Backend // "auto" resolved: the engine compares it with the build
	if recorded {
		out.Req.PrebuiltSHA256 = rec.SHA256
	}
	if req.DryRun {
		return out, nil
	}
	if !c.Global.JSON {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Restoring the ready-made %s build %s for %s%s from %s",
			"Restaurando la compilación lista %s de %s para %s%s desde %s"), pc.Label, shortOf9(rec.Commit), in.Backend, sm(in), filepath.Base(req.Prebuilt)))
	}
	prompt := fmt.Sprintf(pick(c.Lang,
		"Restore the ready-made %s build %s and switch to it? Nothing is compiled; it takes a moment.",
		"¿Restaurar la compilación lista %s de %s y cambiar a ella? No se compila nada; tarda un momento."), pc.Label, shortOf9(rec.Commit))
	if req.NoActivate {
		prompt = fmt.Sprintf(pick(c.Lang,
			"Restore the ready-made %s build %s without switching to it?",
			"¿Restaurar la compilación lista %s de %s sin cambiar a ella?"), pc.Label, shortOf9(rec.Commit))
	}
	ok, cerr := c.Confirm(prompt)
	if cerr != nil {
		return restorePlan{}, cerr
	}
	if !ok {
		c.Out.Info(pick(c.Lang, "Nothing was changed.", "No se cambió nada."))
		return restorePlan{}, ExitWith(ExitRefused)
	}
	return out, nil
}

func shortOf9(commit string) string {
	if len(commit) > 9 {
		return commit[:9]
	}
	return commit
}

// sm is " (sm 75)" for a CUDA build.
func sm(in build.Inputs) string {
	if in.CudaArch == "" {
		return ""
	}
	return " (sm " + in.CudaArch + ")"
}

// offerPrebuilt looks for a ready-made build that fits this computer exactly (the commit, backend,
// Ubuntu release and processor, and the compute capability for CUDA) and offers it before anything is
// compiled. At a terminal the question is asked and a yes returns the file; anywhere else (--yes,
// --json, no terminal) nothing is chosen for the operator: the file is named in a hint and the build
// goes on, so a script keeps doing what it said.
func offerPrebuilt(c *Call, host kitHost, roots []string, program, command string, in build.Inputs) (string, bool) {
	want, err := prebuiltWant(host, program, in.Commit, in.Backend, in.CudaArch)
	if err != nil {
		return "", false
	}
	f, err := kit.SelectPrebuilt(host.FS, roots, want)
	if err != nil {
		return "", false
	}
	if c.Global.JSON {
		return "", false
	}
	hint := fmt.Sprintf(pick(c.Lang,
		"A ready-made build that fits this computer is in the kit: to use it instead of compiling, run `sudo aictl %s --prebuilt %s`.",
		"En el kit hay una compilación lista que corresponde a este equipo: para usarla en vez de compilar, ejecute `sudo aictl %s --prebuilt %s`."), command, f.Path)
	if c.Global.Yes || !c.Term.Interactive() {
		c.Out.Info(hint)
		return "", false
	}
	// A question of its own: --yes is the answer to "build?", not to "use the ready-made one?", and it
	// was handled above.
	prompt := fmt.Sprintf(pick(c.Lang,
		"The kit has a ready-made build for exactly this computer (%s). Use it instead of compiling? It takes a moment instead of a long wait.",
		"El kit tiene una compilación lista para este equipo exacto (%s). ¿Usarla en vez de compilar? Tarda un momento en vez de una larga espera."), filepath.Base(f.Path))
	yes, cerr := c.Confirm(prompt)
	if cerr != nil || !yes {
		return "", false
	}
	return f.Path, true
}

// prebuiltErr maps the failures of reading and checking a ready-made build to the command's error.
func prebuiltErr(c *Call, pc prebuiltCmd, file string, err error) error {
	base := filepath.Base(file)
	var mm *kit.PrebuiltMismatchError
	switch {
	case errors.As(err, &mm):
		e := c.codeError(CodePrebuiltMismatch, ExitFailed, "")
		e.Message = mismatchText(c, pc, base, mm)
		e.Cause = err
		details := []any{"file", base}
		for _, d := range mm.Diffs {
			details = append(details, d.Field, d.Has+" != "+d.Wants)
		}
		if len(mm.Have) > 0 {
			details = append(details, "kit_has", kit.PrebuiltFiles(mm.Have))
		}
		return e.WithDetails(details...)
	case errors.Is(err, kit.ErrPrebuiltUnrecorded):
		e := c.codeError(CodeKitDamaged, ExitFailed, "")
		e.Message = fmt.Sprintf(pick(c.Lang,
			"The ready-made build %s is not in the kit's list (kit/prebuilt/MANIFEST.json), or does not match it, so it cannot be trusted. Nothing was changed.",
			"La compilación lista %s no está en la lista del kit (kit/prebuilt/MANIFEST.json), o no coincide con ella, así que no es de fiar. No se cambió nada."), base)
		e.Cause = err
		return e.WithDetails("file", base, "reason", strings.TrimPrefix(err.Error(), kit.ErrPrebuiltUnrecorded.Error()+": "))
	}
	var ke *kit.Error
	if errors.As(err, &ke) {
		e := c.codeError(CodePrebuiltMismatch, ExitFailed, "")
		e.Message = fmt.Sprintf(pick(c.Lang, "This computer's Ubuntu release is unknown, so it cannot be said whether %s fits it: %s.",
			"No se conoce la versión de Ubuntu de este equipo, así que no se puede decir si %s le corresponde: %s."), base, ke.Detail)
		e.Cause = err
		return e
	}
	if errors.Is(err, os.ErrNotExist) {
		e := c.codeError(CodeKitDamaged, ExitFailed, "")
		e.Message = fmt.Sprintf(pick(c.Lang, "The file %s cannot be read.", "No se puede leer el archivo %s."), file)
		e.Cause = err
		return e
	}
	return err
}

// fieldLabel names what differs, in the language of the output.
func fieldLabel(l Lang, field string) string {
	switch field {
	case "ubuntu":
		return pick(l, "Ubuntu version", "versión de Ubuntu")
	case "arch":
		return pick(l, "processor", "procesador")
	case "backend":
		return pick(l, "backend", "backend")
	case "cuda_arch":
		return pick(l, "graphics chip (CUDA architecture)", "chip gráfico (arquitectura CUDA)")
	case "commit":
		return pick(l, "version of the source", "versión del código fuente")
	case "program":
		return pick(l, "program", "programa")
	}
	return field
}

// mismatchText says what is different and what the kit has, in plain words.
func mismatchText(c *Call, pc prebuiltCmd, base string, mm *kit.PrebuiltMismatchError) string {
	var parts []string
	for _, d := range mm.Diffs {
		parts = append(parts, fmt.Sprintf(pick(c.Lang, "%s: the build is for %s, this computer has %s", "%s: la compilación es para %s, este equipo tiene %s"),
			fieldLabel(c.Lang, d.Field), noneIfEmpty(c.Lang, d.Has), noneIfEmpty(c.Lang, d.Wants)))
	}
	msg := fmt.Sprintf(pick(c.Lang, "The ready-made build %s does not fit this computer (%s). Nothing was changed.",
		"La compilación lista %s no corresponde a este equipo (%s). No se cambió nada."), base, strings.Join(parts, "; "))
	var others []string
	for _, r := range mm.Have {
		if r.File != base {
			others = append(others, r.File)
		}
	}
	if len(others) > 0 {
		msg += " " + fmt.Sprintf(pick(c.Lang, "The kit has: %s.", "El kit tiene: %s."), nameList(c.Lang, others))
	} else {
		msg += " " + fmt.Sprintf(pick(c.Lang, "The kit has no other ready-made %s build.", "El kit no tiene otra compilación lista de %s."), pc.Label)
	}
	return msg
}

func noneIfEmpty(l Lang, s string) string {
	if s == "" {
		return pick(l, "none", "ninguno")
	}
	return s
}
