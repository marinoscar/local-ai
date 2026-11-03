package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// `aictl kit install-debs` and `aictl kit install <tool>` (aictl.md section 4.23c, rebuild-kit.md
// sections 3.5 and 4.1.4, KIT-06). Both run in this process as root, need no agent and no network,
// and verify the kit before they install a byte of it. The work is in internal/kit (kit.Apt,
// kit.ToolInstaller); this file is flags, roots, messages and the audit line.

// Audit actions of these commands (agent.md section 4.11).
const (
	actionKitInstallDebs = "kit.install_debs"
	actionKitInstall     = "kit.install"
)

func init() {
	registerKitSub(&Command{
		Name:    "install-debs",
		Summary: "Install packages from the kit's offline apt repository.",
		Usage:   "[--root <id>] [--packages <p,…>] [--dry-run]",
		Description: `Installs build tools and drivers from <root>/kit/debs/<this Ubuntu>-<arch>/ (and the NVIDIA driver
packages from kit/nvidia/) with no internet. Every file of the repository is checked against
SHA256SUMS and PACKAGES.lock first; if anything does not match, nothing is installed. Without
--packages it installs the build and tool packages, plus the Vulkan or CUDA ones for this node's
backend; packages that are already installed are skipped.
  --packages <p,…>  Install only these packages (names such as cmake or nvidia-driver-595-open).
  --root <id>       Use one root: internal, an id such as ark-3f9c0a12, or a full path.
  --dry-run         Show what would be installed; change nothing. Exit 0 only if it all resolves.
The fix for LAI-KIT-010 and LAI-KIT-011. Run it with sudo.
Specification: docs/specs/rebuild-kit.md section 4.1.4`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: kitTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("root", "", "use this root: internal, an id such as ark-3f9c0a12, or a full path")
			fs.String("packages", "", "install only these packages (comma separated)")
			fs.Bool("dry-run", false, "show what would be installed and change nothing")
		},
		Run: kitInstallDebs,
	})
	registerKitSub(&Command{
		Name:    "install",
		Summary: "Install kiwix-tools, pmtiles, piper or the agent from the kit.",
		Usage:   "<kiwix-tools|pmtiles|piper|agent> [--root <id>]",
		Description: `Installs a tool from <root>/kit/ into /opt/localai/<tool>/<version>/ with no internet and makes it
the current version; the previous version is kept. The file is checked against the kit's
MANIFEST.json first (the agent: against SHA256SUMS and its signature, with the key built into this
program). "agent" replaces /opt/localai/bin/aiagent, keeps aiagent.prev and restarts the agent.
Run it with sudo.
Specification: docs/specs/rebuild-kit.md section 3.5`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: kitTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("root", "", "use this root: internal, an id such as ark-3f9c0a12, or a full path")
		},
		Run: kitInstallTool,
	})
}

// ---- install-debs -------------------------------------------------------------------------------

func kitInstallDebs(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	var names []string
	if flagSet(c, "packages") {
		parsed, err := kit.ParsePackageList(c.String("packages"))
		if err != nil {
			return kitDebsErr(c, err)
		}
		names = parsed
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	host, env := kitHostFn(), localEnv()
	codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH)
	if err != nil {
		return kitDebsErr(c, err)
	}
	roots, err := kitRoots(c, env, c.String("root"))
	if err != nil {
		return err
	}
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()

	dry := c.Bool("dry-run")
	apt := &kit.Apt{
		FS: host.FS, Runner: host.Runner, LookPath: host.LookPath,
		Codename: codename, Arch: arch, SourceList: host.SourceList, ScratchDir: host.ScratchDir,
		Progress: func(s kit.Step, detail string) { kitDebsProgress(c, s, detail) },
	}
	req := kit.Request{Packages: names, DryRun: dry}
	if len(names) == 0 {
		req.Backend = kitBackend(env)
	}

	var res *kit.Result
	var root kitRoot
	var runErr error
	for _, r := range roots {
		got, err := apt.Run(c.Ctx, r.KitDir(), req)
		if err != nil && skipRoot(err) && len(roots) > 1 {
			runErr = betterDebsErr(runErr, err)
			continue
		}
		res, root, runErr = got, r, err
		break
	}
	mapped := kitDebsErr(c, runErr)
	if !dry {
		target := map[string]string{"root": root.ID}
		if res != nil {
			target["packages"] = strings.Join(res.Requested, ",")
			target["installed"] = pkgNames(res.Installed)
		}
		recordLocal(c, env, actionKitInstallDebs, mapped, target)
	}
	if mapped != nil {
		return mapped
	}
	return kitDebsReport(c, res, root)
}

// flagSet reports whether the command's own flag was given on the command line.
func flagSet(c *Call, name string) bool {
	set := false
	c.Flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// skipRoot says whether a failure only means "this root's kit has nothing for this computer", so the
// next root is tried. A damaged repository, an apt failure and a refused plan are never skipped.
func skipRoot(err error) bool {
	return errors.Is(err, kit.ErrNoRepo) || errors.Is(err, kit.ErrWrongRelease) || errors.Is(err, kit.ErrNotInKit)
}

// betterDebsErr keeps the most telling of two "nothing for this computer" failures: another Ubuntu
// release beats a missing package beats no repository at all.
func betterDebsErr(old, next error) error {
	rank := func(err error) int {
		switch {
		case err == nil:
			return 0
		case errors.Is(err, kit.ErrWrongRelease):
			return 3
		case errors.Is(err, kit.ErrNotInKit):
			return 2
		}
		return 1
	}
	if rank(next) > rank(old) {
		return next
	}
	return old
}

func kitDebsProgress(c *Call, s kit.Step, detail string) {
	if c.Global.JSON {
		return
	}
	switch s {
	case kit.StepVerify:
		if strings.HasPrefix(detail, "debs/") || strings.HasPrefix(detail, "nvidia/") {
			c.Out.Progress(pick(c.Lang, "Checking every file of the kit's "+detail, "Comprobando cada archivo de "+detail+" del kit"))
		}
	case kit.StepUpdate:
		c.Out.Progress(pick(c.Lang, "Reading the package list of the kit", "Leyendo la lista de paquetes del kit"))
	case kit.StepPlan:
		c.Out.Progress(pick(c.Lang, "Working out what to install", "Calculando qué instalar"))
	case kit.StepInstall:
		c.Out.Progress(pick(c.Lang, "Installing from the kit", "Instalando desde el kit"))
	}
}

func pkgNames(p []kit.Pkg) string {
	names := make([]string, len(p))
	for i, x := range p {
		names[i] = x.Name
	}
	return strings.Join(names, ",")
}

// pkgCount is "1 package" / "3 packages" in the language of the output.
func pkgCount(l Lang, n int) string {
	if n == 1 {
		return pick(l, "1 package", "1 paquete")
	}
	return fmt.Sprintf(pick(l, "%d packages", "%d paquetes"), n)
}

// esReasons are the reasons package kit puts in brackets after a file name, in Spanish.
var esReasons = strings.NewReplacer(
	"(changed)", "(cambiado)", "(missing)", "(falta)", "(unreadable)", "(no se puede leer)",
	"(permission denied)", "(permiso denegado)", "(not a regular file)", "(no es un archivo normal)",
	"(not listed in SHA256SUMS)", "(no está en SHA256SUMS)",
	"(no matching .deb in pool/)", "(no hay un .deb que coincida en pool/)",
	"(a .deb that PACKAGES.lock does not list)", "(un .deb que PACKAGES.lock no lista)")

// nameList joins names for a sentence, keeping at most 12.
func nameList(l Lang, names []string) string {
	if l == LangES {
		names = append([]string(nil), names...)
		for i, n := range names {
			names[i] = esReasons.Replace(n)
		}
	}
	if len(names) <= 12 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:12], ", ") + fmt.Sprintf(pick(l, " and %d more", " y %d más"), len(names)-12)
}

// kitDebsReport prints the result of a run that did not fail.
func kitDebsReport(c *Call, res *kit.Result, root kitRoot) error {
	if c.Global.JSON {
		return c.Out.JSON().Document(res)
	}
	var skipped, deps, planNames []string
	for _, p := range res.Skipped {
		skipped = append(skipped, p.Name)
	}
	asked := map[string]bool{}
	for _, n := range res.Requested {
		asked[n] = true
	}
	for _, p := range res.Plan {
		planNames = append(planNames, p.Name)
		if !asked[p.Name] {
			deps = append(deps, p.Name)
		}
	}
	switch {
	case res.DryRun && len(planNames) == 0:
		c.Out.OK(pick(c.Lang, "Dry run: nothing was changed, and nothing needs installing.",
			"Prueba: no se cambió nada, y no hay nada que instalar."))
	case res.DryRun:
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Dry run: nothing was changed. These %s would be installed from the kit: %s.",
			"Prueba: no se cambió nada. Estos %s se instalarían desde el kit: %s."),
			pkgCount(c.Lang, len(planNames)), nameList(c.Lang, planNames)))
	case len(res.Installed) == 0:
		c.Out.OK(fmt.Sprintf(pick(c.Lang,
			"Everything asked for is already installed (%s).", "Todo lo pedido ya está instalado (%s)."),
			pkgCount(c.Lang, len(res.Skipped))))
	default:
		var names []string
		for _, p := range res.Installed {
			names = append(names, p.Name)
		}
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "Installed %s from the kit: %s.", "Se instalaron %s desde el kit: %s."),
			pkgCount(c.Lang, len(names)), nameList(c.Lang, names)))
	}
	if len(deps) > 0 {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Also needed by them: %s.", "También los necesitan: %s."), nameList(c.Lang, deps)))
	}
	if len(skipped) > 0 && (res.DryRun || len(res.Installed) > 0) {
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "Already installed, left as they are: %s.", "Ya instalados, se dejan como están: %s."), nameList(c.Lang, skipped)))
	}
	if len(res.NotInKit) > 0 {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang, "Not in this kit, so skipped: %s.", "No están en este kit, así que se omitieron: %s."), nameList(c.Lang, res.NotInKit)))
	}
	return nil
}

// kitDebsErr turns the failures of package kit into the command's error: a doctor code, one plain
// sentence naming what is wrong and the code's fix.
func kitDebsErr(c *Call, err error) error {
	if err == nil {
		return nil
	}
	var e *kit.Error
	if !errors.As(err, &e) {
		return err
	}
	var out error
	switch {
	case errors.Is(e.Kind, kit.ErrBadPackage):
		name := ""
		if len(e.Names) > 0 {
			name = e.Names[0]
		}
		ce := c.usageErr(
			fmt.Sprintf(pick(c.Lang, "%q is not a valid package name.", "%q no es un nombre de paquete válido."), name),
			pick(c.Lang, "Package names use lowercase letters, digits and + - . only; separate several with commas.",
				"Los nombres de paquete usan solo minúsculas, dígitos y + - . ; separe varios con comas."))
		out = ce
	case errors.Is(e.Kind, kit.ErrRepoDamaged):
		out = c.Err(CodeKitDamaged, fmt.Sprintf(pick(c.Lang,
			"These files in the kit do not match their checksums, so nothing was installed: %s.",
			"Estos archivos del kit no coinciden con sus sumas de comprobación, así que no se instaló nada: %s."),
			nameList(c.Lang, e.Names))).WithDetails("repository", e.Repo)
	case errors.Is(e.Kind, kit.ErrWrongRelease):
		have := strings.Join(e.Have, ", ")
		if have == "" {
			out = c.Err(CodeKitWrongRelease, fmt.Sprintf(pick(c.Lang,
				"This computer is not one the kit has packages for (%s).", "Este equipo no es uno para el que el kit tiene paquetes (%s)."), e.Detail))
		} else {
			out = c.Err(CodeKitWrongRelease, fmt.Sprintf(pick(c.Lang,
				"This computer needs packages for %s, but the kit only has: %s.",
				"Este equipo necesita paquetes para %s, pero el kit solo tiene: %s."), e.Want, have))
		}
	case errors.Is(e.Kind, kit.ErrNoRepo):
		out = c.Err(CodeKitMissing, pick(c.Lang,
			"The kit has no packages for this computer (no debs folder for it).",
			"El kit no tiene paquetes para este equipo (no hay carpeta debs para él)."))
	case errors.Is(e.Kind, kit.ErrNotInKit):
		out = c.Err(CodeKitMissing, fmt.Sprintf(pick(c.Lang,
			"These packages are not in the kit for this computer: %s.", "Estos paquetes no están en el kit para este equipo: %s."),
			nameList(c.Lang, e.Names)))
	case errors.Is(e.Kind, kit.ErrUnsafePlan):
		out = c.Err(CodeKitInstall, fmt.Sprintf(pick(c.Lang,
			"apt wanted to install or remove packages that the kit does not vouch for (%s), so nothing was installed.",
			"apt quería instalar o quitar paquetes que el kit no garantiza (%s), así que no se instaló nada."), nameList(c.Lang, e.Names)))
	case errors.Is(e.Kind, kit.ErrCleanup):
		out = c.Err(CodeKitInstall, fmt.Sprintf(pick(c.Lang,
			"The temporary apt source %s could not be removed. Delete it with `sudo rm %s`.",
			"No se pudo quitar la fuente temporal de apt %s. Bórrela con `sudo rm %s`."), kit.AptSourceList, kit.AptSourceList))
	case errors.Is(e.Kind, kit.ErrApt):
		last := ""
		if n := len(e.Names); n > 0 {
			last = " " + fmt.Sprintf(pick(c.Lang, "It said: %s", "Dijo: %s"), e.Names[n-1])
		}
		out = c.Err(CodeKitInstall, pick(c.Lang, "apt could not finish, so the install stopped.", "apt no pudo terminar, así que la instalación se detuvo.")+last)
	default:
		out = c.Err(CodeKitInstall, pick(c.Lang, "The packages could not be installed: ", "No se pudieron instalar los paquetes: ")+e.Detail+".")
	}
	if ce, ok := out.(*CLIError); ok {
		ce.Cause = err
		if len(e.Names) > 0 {
			ce.WithDetails("files", e.Names)
		}
	}
	// A temporary source that could not be removed after another failure is told as well.
	if errors.Is(err, kit.ErrCleanup) && !errors.Is(e.Kind, kit.ErrCleanup) {
		c.Out.Warn(fmt.Sprintf(pick(c.Lang,
			"The temporary apt source %s could not be removed. Delete it with `sudo rm %s`.",
			"No se pudo quitar la fuente temporal de apt %s. Bórrela con `sudo rm %s`."), kit.AptSourceList, kit.AptSourceList))
	}
	return out
}

// ---- install <tool> -----------------------------------------------------------------------------

func kitInstallTool(c *Call) error {
	if err := c.exactArgs(1, "<kiwix-tools|pmtiles|piper|agent>"); err != nil {
		return err
	}
	tool := c.Args[0]
	if !kit.ValidTool(tool) {
		return c.usageErr(
			pick(c.Lang, "Unknown tool "+quote(tool)+".", "Herramienta desconocida "+quote(tool)+"."),
			pick(c.Lang, "Use one of: "+strings.Join(kit.Tools, ", ")+".", "Use una de estas: "+strings.Join(kit.Tools, ", ")+"."))
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	host, env := kitHostFn(), localEnv()
	roots, err := kitRoots(c, env, c.String("root"))
	if err != nil {
		return err
	}
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	defer release()

	ti := &kit.ToolInstaller{
		FS: host.FS, Runner: host.Runner, LookPath: host.LookPath, Arch: host.GOARCH,
		Keys: host.Keys, OptRoot: host.OptRoot, Kiwix: host.Kiwix, Piper: host.Piper,
		Progress: func(step, name string) { kitToolProgress(c, step, name) },
	}
	var res *kit.ToolResult
	var root kitRoot
	var runErr error
	for _, r := range roots {
		got, err := ti.Install(c.Ctx, r.KitDir(), tool)
		if err != nil && errors.Is(err, kit.ErrToolNotInKit) && len(roots) > 1 {
			if runErr == nil {
				runErr = err
			}
			continue
		}
		res, root, runErr = got, r, err
		break
	}
	mapped := kitToolErr(c, tool, runErr)
	target := map[string]string{"tool": tool, "root": root.ID}
	if res != nil {
		target["version"] = res.Version
	}
	recordLocal(c, env, actionKitInstall, mapped, target)
	if mapped != nil {
		return mapped
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(res)
	}
	kitToolReport(c, res)
	return nil
}

func kitToolProgress(c *Call, step, tool string) {
	if c.Global.JSON {
		return
	}
	switch step {
	case "verify":
		c.Out.Progress(fmt.Sprintf(pick(c.Lang, "Checking %s in the kit", "Comprobando %s en el kit"), tool))
	case "install":
		c.Out.Progress(fmt.Sprintf(pick(c.Lang, "Installing %s", "Instalando %s"), tool))
	case "restart":
		c.Out.Progress(pick(c.Lang, "Restarting the agent", "Reiniciando el agente"))
	}
}

func kitToolReport(c *Call, res *kit.ToolResult) {
	switch {
	case res.Unchanged:
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "%s %s is already the current version; nothing was changed.",
			"%s %s ya es la versión actual; no se cambió nada."), res.Tool, res.Version))
	case res.Tool == kit.ToolAgent && res.RestartError == "":
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "Installed the agent %s from the kit and restarted it.",
			"Se instaló el agente %s desde el kit y se reinició."), res.Version))
	default:
		c.Out.OK(fmt.Sprintf(pick(c.Lang, "Installed %s %s from the kit and made it the current version.",
			"Se instaló %s %s desde el kit y se hizo la versión actual."), res.Tool, res.Version))
	}
	if res.Previous != "" {
		keep := "aiagent.prev"
		if res.Tool != kit.ToolAgent {
			keep = "/opt/localai/" + res.Tool + "/" + res.Previous
		}
		c.Out.Info(fmt.Sprintf(pick(c.Lang, "The previous version %s is kept (%s).", "Se conserva la versión anterior %s (%s)."), res.Previous, keep))
	}
	if res.RestartError != "" {
		c.Out.Warn(pick(c.Lang, "The new agent is installed, but it could not be restarted: run `sudo systemctl restart localai-agent`.",
			"El agente nuevo está instalado, pero no se pudo reiniciar: ejecute `sudo systemctl restart localai-agent`."))
	}
	if res.Tool == kit.ToolKiwix && !res.Unchanged {
		c.Out.Info(pick(c.Lang, "The library service keeps the old version until it restarts: run `aictl restart kiwix`.",
			"El servicio de biblioteca mantiene la versión anterior hasta que se reinicie: ejecute `aictl restart kiwix`."))
	}
}

// kitToolErr turns the failures of ToolInstaller.Install into the command's error.
func kitToolErr(c *Call, tool string, err error) error {
	if err == nil {
		return nil
	}
	var te *kit.ToolError
	if !errors.As(err, &te) {
		return err
	}
	file := ""
	if len(te.Names) > 0 {
		file = te.Names[0]
	}
	var ce *CLIError
	switch {
	case errors.Is(te.Kind, kit.ErrToolNotInKit):
		ce = c.Err(CodeKitMissing, fmt.Sprintf(pick(c.Lang, "The kit has no %s for this computer.", "El kit no tiene %s para este equipo."), tool))
	case errors.Is(te.Kind, kit.ErrToolDamaged):
		if file == "" {
			file = tool
		}
		ce = c.Err(CodeKitDamaged, fmt.Sprintf(pick(c.Lang,
			"The kit file %s does not match its checksum, so it was not installed.",
			"El archivo %s del kit no coincide con su suma de comprobación, así que no se instaló."), file))
	case errors.Is(te.Kind, kit.ErrSignature):
		ce = c.Err(CodeKitSignature, pick(c.Lang,
			"The agent files in the kit failed their signature check, so nothing was replaced.",
			"Los archivos del agente del kit no superaron la comprobación de firma, así que no se reemplazó nada."))
	default:
		code := te.Code()
		reason := te.Detail
		if reason == "" && te.Err != nil {
			reason = te.Err.Error()
		}
		ce = c.Err(code, fmt.Sprintf(pick(c.Lang, "%s could not be installed from the kit; the version in use was not changed.",
			"%s no se pudo instalar desde el kit; no se cambió la versión en uso."), tool))
		if _, known := codeTable[code]; !known {
			if _, fix, ok := doctor.Text(code, string(c.Lang)); ok {
				ce.Fix = fix
			}
		}
		ce.WithDetails("reason", reason)
	}
	ce.Cause = err
	if te.Detail != "" {
		ce.WithDetails("detail", te.Detail)
	}
	if len(te.Names) > 0 {
		ce.WithDetails("files", te.Names)
	}
	return ce
}
