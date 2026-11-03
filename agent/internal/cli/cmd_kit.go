package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// The `aictl kit` group (aictl.md section 4.23c, rebuild-kit.md section 3.5).
//
// This file owns the group and the pieces every kit subcommand shares. A subcommand lives in its own
// file and joins the group with registerKitSub from an init function; the group itself, its
// "needs a subcommand" answer and the code table entries below are never edited to add one:
//
//	func init() {
//		registerKitSub(&Command{Name: "status", Summary: "...", Marks: MarkLocal, Run: kitStatus})
//	}
//
// The shared pieces: kitRoots (which content roots a command looks at, honouring --root), kitHostFn
// (the seam to the machine that tests replace) and the doctor code entries of the kit.
// Subcommands: install and install-debs (cmd_kit_install.go, KIT-06); status, verify and export-build
// belong to later issues.

// kitCommand is the group. It has a Run so that `aictl kit` alone and `aictl kit <typo>` answer in
// plain language instead of falling through to the generic "unknown command".
func kitCommand() *Command {
	return &Command{
		Name:    "kit",
		Summary: "Work with the rebuild kit (offline packages and tools).",
		Description: `The rebuild kit is the folder <content root>/kit/ on this computer and on every AI-ARK drive: the
packages, tools and sources that rebuild a node with no internet. Run "aictl kit <subcommand>".
Specification: docs/specs/aictl.md section 4.23c, docs/specs/rebuild-kit.md`,
		Run: kitGroupRun,
	}
}

func init() {
	Register(kitCommand())
}

// registerKitSub adds a subcommand to the kit group. Call it from an init function in the file that
// defines the subcommand. The command is attached after every init has run (RegisterLate), so file
// order never matters; a subcommand of the same name replaces an earlier one.
func registerKitSub(cmd *Command) { attachAll(at(cmd, "kit")) }

// kitSubLines lists the subcommands of the group as "name  summary" for the plain-language
// answer.
func kitSubLines(c *Call) []string {
	subs := append([]*Command(nil), c.Command.Sub...)
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	var out []string
	width := 0
	for _, s := range subs {
		if len(s.Name) > width {
			width = len(s.Name)
		}
	}
	for _, s := range subs {
		out = append(out, "  "+s.Name+strings.Repeat(" ", width-len(s.Name)+2)+s.Summary)
	}
	return out
}

// kitGroupRun is `aictl kit` with no subcommand, or with one that does not exist: it lists what is
// available and exits 2.
func kitGroupRun(c *Call) error {
	names := make([]string, 0, len(c.Command.Sub))
	for _, s := range c.Command.Sub {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	list := strings.Join(names, ", ")
	if len(c.Args) > 0 {
		return c.usageErr(
			pick(c.Lang, "Unknown kit command "+quote(c.Args[0])+".", "Comando de kit desconocido "+quote(c.Args[0])+"."),
			pick(c.Lang, "Use one of: "+list+". Run `aictl help kit`.", "Use uno de estos: "+list+". Ejecute `aictl help kit`."))
	}
	if c.Global.JSON {
		return c.usageErr(
			pick(c.Lang, "`aictl kit` needs a subcommand.", "`aictl kit` necesita un subcomando."),
			pick(c.Lang, "Use one of: "+list+".", "Use uno de estos: "+list+"."))
	}
	c.Out.Result(pick(c.Lang, "What you can do with the rebuild kit:", "Qué puede hacer con el kit de reconstrucción:"))
	for _, l := range kitSubLines(c) {
		c.Out.Result(l)
	}
	c.Out.Result(pick(c.Lang, "Run `aictl help kit <command>` for the details.", "Ejecute `aictl help kit <comando>` para ver los detalles."))
	return ExitWith(ExitUsage)
}

// ---- doctor codes of the kit commands -----------------------------------------------------------

// Doctor codes the kit commands raise (the constants alias internal/doctor, the only file that spells
// a code literal).
const (
	// CodeKitDamaged: a file of the kit does not match its checksum (nothing was installed).
	CodeKitDamaged = doctor.KIT001
	// CodeKitWrongRelease: the kit's packages are for another Ubuntu release.
	CodeKitWrongRelease = doctor.KIT042
	// CodeKitSignature: the kit's agent files failed their signature check.
	CodeKitSignature = doctor.KIT044
	// CodeKitInstall: installing from the kit failed; nothing was replaced.
	CodeKitInstall = doctor.KIT047
)

func init() {
	for code, info := range map[string]codeInfo{
		CodeKitDamaged: {ExitFailed, sevError,
			text{"A file of the rebuild kit is damaged.", "Run `aictl kit verify --full`; sync the drive on a computer with internet."},
			text{"Un archivo del kit de reconstrucción está dañado.", "Ejecute `aictl kit verify --full`; sincronice el disco en una computadora con internet."}},
		CodeKitWrongRelease: {ExitFailed, sevError,
			text{"The kit is for a different Ubuntu version.", "Sync the kit after the upgrade, with internet."},
			text{"El kit es de otra versión de Ubuntu.", "Sincronice el kit después de la actualización, con internet."}},
		CodeKitSignature: {ExitFailed, out.SevCritical,
			text{"The kit's agent files failed their signature check.", "Don't install them. Use another drive and report it."},
			text{"Los archivos del agente del kit no superaron la comprobación de firma.", "No los instale. Use otro disco e infórmelo."}},
		CodeKitInstall: {ExitFailed, sevError,
			text{"Installing from the kit failed.", "Nothing was replaced. Read the message, then run `aictl kit verify --full` and try again."},
			text{"Falló la instalación desde el kit.", "No se reemplazó nada. Lea el mensaje, ejecute `aictl kit verify --full` e intente otra vez."}},
	} {
		codeTable[code] = info
	}
}

// ---- the machine and the roots ------------------------------------------------------------------

// kitHost is where the kit commands touch the machine. Tests replace kitHostFn with temporary
// directories and fakes.
type kitHost struct {
	FS       kit.FS
	Runner   execx.Runner
	LookPath func(name string) (string, error)
	// OSRelease is /etc/os-release; GOARCH is this machine's architecture.
	OSRelease string
	GOARCH    string
	// SourceList is the temporary apt source ("" is the default); ScratchDir is where a dry run keeps
	// its private apt state ("" is the system temp dir).
	SourceList, ScratchDir string
	// OptRoot is /opt/localai.
	OptRoot string
	// Kiwix and Piper are the install functions of their packages.
	Kiwix, Piper kit.TarballInstaller
	// Keys are the trusted release keys (nil: the embedded ones).
	Keys []update.PublicKey
}

var kitHostFn = func() kitHost {
	return kitHost{
		FS: kit.OSFS{}, Runner: execx.Exec{}, LookPath: execx.Lookup,
		OSRelease: "/etc/os-release", GOARCH: runtime.GOARCH,
		OptRoot: kit.DefaultOptRoot,
		Kiwix:   kiwix.Install, Piper: piper.Install,
		Keys: update.EmbeddedKeys(),
	}
}

// kitRoot is a content root that may hold a kit.
type kitRoot struct {
	// ID is "internal" or the id of the drive's ROOT.json ("ark-3f9c0a12"), else the mount point.
	ID string
	// Path is the content root; the kit is Path/kit.
	Path string
}

// KitDir is <root>/kit.
func (r kitRoot) KitDir() string { return filepath.Join(r.Path, kit.DirName) }

// kitRoots lists the content roots a kit command looks at, in order: with --root only that one (an id
// "internal" or "ark-<8 hex>", or an absolute path to a content root or to its kit/ folder), else the
// internal root first and then every mounted AI-ARK drive (as the other local root commands find
// them). Roots without a kit/ folder are left out. The error is LAI-KIT-040 (exit 5) when no root
// has a kit, and a usage error for a --root that cannot be an id or a path.
func kitRoots(c *Call, env local.Env, rootFlag string) ([]kitRoot, error) {
	var all []kitRoot
	for _, r := range local.FindRoots(c.Ctx, env) {
		id := "internal"
		if r.Kind == local.KindDrive {
			id = r.Mount
			if rj, err := ark.ReadRootJSON(filepath.Join(r.Path, "ROOT.json")); err == nil && ark.ValidRootID(rj.RootID) {
				id = rj.RootID
			}
		}
		all = append(all, kitRoot{ID: id, Path: r.Path})
	}
	var picked []kitRoot
	switch {
	case rootFlag == "":
		picked = all
	case filepath.IsAbs(rootFlag):
		p := filepath.Clean(rootFlag)
		if filepath.Base(p) == kit.DirName {
			p = filepath.Dir(p)
		}
		picked = []kitRoot{{ID: p, Path: p}}
	case ark.ValidRootID(rootFlag):
		for _, r := range all {
			if r.ID == rootFlag {
				picked = append(picked, r)
			}
		}
	default:
		return nil, c.usageErr(
			pick(c.Lang, "The root "+quote(rootFlag)+" is not a root id or a path.", "La raíz "+quote(rootFlag)+" no es un id de raíz ni una ruta."),
			pick(c.Lang, "Use `internal`, an id such as `ark-3f9c0a12`, or the full path of a content root.",
				"Use `internal`, un id como `ark-3f9c0a12`, o la ruta completa de una raíz de contenido."))
	}
	var withKit []kitRoot
	for _, r := range picked {
		if fi, err := os.Stat(r.KitDir()); err == nil && fi.IsDir() {
			withKit = append(withKit, r)
		}
	}
	if len(withKit) == 0 {
		msg := pick(c.Lang, "No rebuild kit was found on this computer or on a mounted AI-ARK drive.",
			"No se encontró un kit de reconstrucción en este equipo ni en un disco AI-ARK montado.")
		if rootFlag != "" {
			msg = pick(c.Lang, "There is no rebuild kit in the root "+quote(rootFlag)+".", "No hay un kit de reconstrucción en la raíz "+quote(rootFlag)+".")
		}
		return nil, c.Err(CodeKitMissing, msg)
	}
	return withKit, nil
}

// kitLock serialises the kit commands with the other aictl commands that change files. A rebuilt
// node may have no /etc/localai yet (the kit is what rebuilds it), and then there is nothing to
// lock against: the lock is skipped rather than refusing the recovery.
func kitLock(c *Call, env local.Env) (release func(), err error) {
	if fi, serr := os.Stat(env.Paths.EtcDir); serr != nil || !fi.IsDir() {
		return func() {}, nil
	}
	release, lerr := local.Lock(env)
	if lerr != nil {
		return nil, localErr(c, lerr)
	}
	return release, nil
}

// kitRecord writes the audit line of a kit command, unless the node has no state folder yet (a
// rebuild from scratch): there is no audit log to append to, and that is not worth a warning.
func kitRecord(c *Call, env local.Env, action string, opErr error, target map[string]string) {
	if fi, err := os.Stat(env.Paths.StateDir); err != nil || !fi.IsDir() {
		return
	}
	recordLocal(c, env, action, opErr, target)
}

// kitBackend is the llama backend of this node ("cuda", "vulkan", "cpu") from node.yaml, "" when it
// cannot be read: the default package set then is the cpu one.
func kitBackend(env local.Env) string {
	data, err := os.ReadFile(env.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return ""
	}
	cfg, _ := config.Parse(data, nil)
	return cfg.Llama.Backend
}

// kitTimeout is the default deadline of the kit install commands: an install of the CUDA toolkit
// from a slow drive takes a while.
const kitTimeout = 90 * time.Minute
