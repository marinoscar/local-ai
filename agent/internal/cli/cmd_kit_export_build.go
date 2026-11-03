package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// `aictl kit export-build` (aictl.md section 4.23c, rebuild-kit.md section 4.2, KIT-05): packs the
// llama.cpp and whisper.cpp builds this computer is running into kit/prebuilt/ of every writable
// content root, so that a node with the same Ubuntu release, CPU, backend and driver can be brought
// back in seconds with `aictl rebuild-llama --prebuilt <file>` or `aictl rebuild-whisper --prebuilt
// <file>`. The work is internal/kit (kit.PrebuiltExporter); this file is the command, the wiring of the
// exporter that the rebuild commands also hand to the build manager, and the words.

const (
	// CodePrebuiltMismatch: the ready-made build does not fit this computer (LAI-KIT-048).
	CodePrebuiltMismatch = doctor.KIT048
	// CodeExportFailed: a build could not be exported to the kit (LAI-KIT-049).
	CodeExportFailed = doctor.KIT049

	actionKitExportBuild = audit.ActionKitExportBuild
)

// kitKernelFn is `uname -r`; tests replace it.
var kitKernelFn = func() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func init() {
	registerKitSub(&Command{
		Name:    "export-build",
		Summary: "Save this computer's AI program builds in the kit.",
		Usage:   "",
		Description: `Packs the llama.cpp and whisper.cpp builds that are in use on this computer into
<content root>/kit/prebuilt/ on this computer and on every mounted AI-ARK drive that can be written. Each
file is named after the commit, backend, Ubuntu release and processor it was built for, and the
list in kit/prebuilt/MANIFEST.json records its checksum, how it was built, the driver and the kernel.
At most two exports are kept per program and backend; the newest are never removed.
A ready-made build only works on a computer with the same Ubuntu release, processor, backend and,
for NVIDIA, graphics chip. Restore one with "sudo aictl rebuild-llama --prebuilt <file>" or
"sudo aictl rebuild-whisper --prebuilt <file>". The rebuild commands also export their new build
by themselves after they switch to it. Run it with sudo.
Specification: docs/specs/rebuild-kit.md section 4.2`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: kitTimeout,
		Run:     kitExportBuild,
	})
}

// prebuiltProgram is a program whose builds can be exported.
type prebuiltProgram struct {
	// Name is the recipe name (kit.ProgramLlama), Dir the folder of its builds below /opt/localai.
	Name, Dir string
}

var prebuiltPrograms = []prebuiltProgram{
	{Name: kit.ProgramLlama, Dir: "llama.cpp"},
	{Name: kit.ProgramWhisper, Dir: "whisper.cpp"},
}

// contentRootPaths lists the content roots that may take an export: the internal one first, then every
// mounted AI-ARK drive that is not mounted read-only (one that cannot be written for another reason is
// found out when it is written).
func contentRootPaths(ctx context.Context, env local.Env) []string {
	var paths []string
	for _, r := range local.FindRoots(ctx, env) {
		if r.ReadOnly {
			continue // mounted read-only: nothing can be written there
		}
		paths = append(paths, r.Path)
	}
	return paths
}

// kitNodeID is node.id of node.yaml ("" when it cannot be read).
func kitNodeID(env local.Env) string {
	data, err := os.ReadFile(env.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return ""
	}
	cfg, _ := config.Parse(data, nil)
	return cfg.Node.ID
}

// newPrebuiltExporter is the exporter of a program: what the build manager calls after a switch
// (Options.Exporter of `aictl rebuild-whisper`, and of `aictl rebuild-llama`) and what `aictl kit
// export-build` runs. It fails when this computer's Ubuntu release is not known, since no file name
// could say what the build is for.
func newPrebuiltExporter(c *Call, env local.Env, host kitHost, program string) (*kit.PrebuiltExporter, error) {
	codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH)
	if err != nil {
		return nil, err
	}
	return &kit.PrebuiltExporter{
		FS: host.FS, Runner: host.Runner, LookPath: host.LookPath,
		Roots:   func() []string { return contentRootPaths(c.Ctx, env) },
		Program: program, Codename: codename, Arch: arch,
		NodeID: func() string { return kitNodeID(env) },
		Kernel: kitKernelFn,
	}, nil
}

// prebuiltWant is what a ready-made build must be to fit this computer for the program, from the
// resolved inputs of the engine.
func prebuiltWant(host kitHost, program, commit, backend, cudaArch string) (kit.PrebuiltWant, error) {
	codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH)
	if err != nil {
		return kit.PrebuiltWant{}, err
	}
	return kit.PrebuiltWant{Program: program, Commit: commit, Backend: backend, Codename: codename, Arch: arch, CudaArch: cudaArch}, nil
}

// currentBuildDir is the folder of the build `current` points at ("" when there is none).
func currentBuildDir(host kitHost, programRoot string) (id, dir string) {
	target, err := host.FS.Readlink(filepath.Join(programRoot, kit.CurrentName))
	if err != nil || target == "" {
		return "", ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(programRoot, target)
	}
	return filepath.Base(target), filepath.Clean(target)
}

// exportedProgram is one program's line of the --json document.
type exportedProgram struct {
	Program string `json:"program"`
	*kit.PrebuiltExport
}

type skippedProgram struct {
	Program string `json:"program"`
	Reason  string `json:"reason"`
}

func kitExportBuild(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	host, env := kitHostFn(), localEnv()
	release, err := kitLock(c, env)
	if err != nil {
		return err
	}
	defer release()

	var done []exportedProgram
	var skipped []skippedProgram
	var failures []error
	for _, p := range prebuiltPrograms {
		id, dir := currentBuildDir(host, filepath.Join(host.OptRoot, p.Dir))
		if dir == "" {
			skipped = append(skipped, skippedProgram{Program: p.Name, Reason: "no build in use"})
			continue
		}
		ex, err := newPrebuiltExporter(c, env, host, p.Name)
		if err != nil {
			failures = append(failures, exportErr(c, p.Name, err))
			continue
		}
		if !c.Global.JSON {
			c.Out.Progress(fmt.Sprintf(pick(c.Lang, "Exporting the %s build %s", "Exportando la compilación %s de %s"), p.Name, id))
		}
		res, err := ex.Export(c.Ctx, dir)
		if c.Ctx.Err() != nil {
			return c.Ctx.Err()
		}
		target := map[string]string{"program": p.Name, "build": id}
		if res != nil {
			target["file"] = res.File
		}
		mapped := exportErr(c, p.Name, err)
		kitRecord(c, env, actionKitExportBuild, mapped, target)
		if res != nil && res.File != "" {
			done = append(done, exportedProgram{Program: p.Name, PrebuiltExport: res})
		}
		if mapped != nil {
			failures = append(failures, mapped)
		}
	}

	if c.Global.JSON {
		if len(done) == 0 && len(failures) > 0 {
			return failures[0]
		}
		if len(done) == 0 {
			return c.codeError(CodeExportFailed, ExitFailed, pick(c.Lang, nothingToExportEN, nothingToExportES))
		}
		return c.Out.JSON().Document(map[string]any{"exports": done, "skipped": skipped})
	}
	for _, d := range done {
		exportReport(c, d)
	}
	for _, s := range skipped {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "No %s build is in use on this computer, so there is nothing to export.",
			"No hay una compilación de %s en uso en este equipo, así que no hay nada que exportar."), s.Program))
	}
	switch {
	case len(failures) > 0:
		return failures[0]
	case len(done) == 0:
		return c.codeError(CodeExportFailed, ExitFailed, pick(c.Lang, nothingToExportEN, nothingToExportES))
	}
	return nil
}

const (
	nothingToExportEN = "There is no llama.cpp or whisper.cpp build in use on this computer, so there is nothing to export."
	nothingToExportES = "No hay una compilación de llama.cpp ni de whisper.cpp en uso en este equipo, así que no hay nada que exportar."
)

// exportReport prints what was written for one program.
func exportReport(c *Call, d exportedProgram) {
	written := 0
	for _, r := range d.Roots {
		if r.Written || r.Unchanged {
			written++
		}
	}
	if written == 0 {
		return
	}
	c.Out.OK(fmt.Sprintf(pick(c.Lang, "Exported the %s build %s as %s (%s).", "Se exportó la compilación %s de %s como %s (%s)."),
		d.Program, d.BuildID, d.File, sizeText(d.Size)))
	for _, r := range d.Roots {
		dir := kit.PrebuiltDir(r.Root)
		switch {
		case r.Written:
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Written to %s.", "Escrito en %s."), dir))
		case r.Unchanged:
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Already in %s, unchanged.", "Ya estaba en %s, sin cambios."), dir))
		case r.ReadOnly:
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Skipped %s: it cannot be written.", "Se omitió %s: no se puede escribir."), r.Root))
		case r.Error != "":
			c.Out.Warn(fmt.Sprintf(pick(c.Lang, "Could not write to %s: %s", "No se pudo escribir en %s: %s"), dir, r.Error))
		}
		if len(r.Pruned) > 0 {
			c.Out.Info(fmt.Sprintf(pick(c.Lang, "Removed the older exports from %s: %s.", "Se quitaron las exportaciones más antiguas de %s: %s."), dir, strings.Join(r.Pruned, ", ")))
		}
	}
}

// exportErr turns a failure of the exporter into the command's error (nil for nil).
func exportErr(c *Call, program string, err error) error {
	if err == nil {
		return nil
	}
	var msg string
	switch {
	case errors.Is(err, kit.ErrExportRefused):
		msg = fmt.Sprintf(pick(c.Lang, "The %s build in use cannot be exported: %s.", "La compilación de %s en uso no se puede exportar: %s."),
			program, strings.TrimPrefix(err.Error(), kit.ErrExportRefused.Error()+": "))
	case errors.Is(err, kit.ErrNoWritableRoot):
		msg = fmt.Sprintf(pick(c.Lang, "The %s build was not exported: no content root can be written (every drive is read-only, or none is mounted).",
			"La compilación de %s no se exportó: no se puede escribir en ninguna raíz de contenido (todos los discos son de solo lectura, o no hay ninguno)."), program)
		if rest := strings.TrimPrefix(err.Error(), kit.ErrNoWritableRoot.Error()); strings.Trim(rest, ": ") != "" && !strings.Contains(rest, "read-only or there is none") {
			msg += " " + strings.TrimLeft(rest, ": ")
		}
	default:
		var ke *kit.Error
		if errors.As(err, &ke) && errors.Is(ke.Kind, kit.ErrWrongRelease) {
			msg = fmt.Sprintf(pick(c.Lang, "The %s build cannot be named because this computer's Ubuntu release is unknown: %s.",
				"La compilación de %s no se puede nombrar porque no se conoce la versión de Ubuntu de este equipo: %s."), program, ke.Detail)
		} else {
			msg = fmt.Sprintf(pick(c.Lang, "The %s build could not be exported: %s", "La compilación de %s no se pudo exportar: %s"), program, err.Error())
		}
	}
	e := c.codeError(CodeExportFailed, ExitFailed, "")
	e.Message = msg
	e.Cause = err
	return e
}
