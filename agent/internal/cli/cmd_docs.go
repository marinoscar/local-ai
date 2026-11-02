package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/docsrender"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// The `aictl docs` group. Only `render` exists (AICTL-08, aictl.md 4.22): a [local] command that
// turns docs/ into the offline manuals and the survival card. It runs in-process with no agent, no
// root (except to write /srv/localai) and no network, and never builds the agent client.

// docsEnvironment is what `docs render` reads from the machine; tests replace it.
type docsEnvironment struct {
	Runner execx.Runner
	Lookup func(string) (string, error)
	Now    func() time.Time
	Getwd  func() (string, error)
	// Hostname is the machine name (a last resort to find the node in the inventory).
	Hostname func() (string, error)
	// EtcDir is /etc/localai, the first place for inventory.local.yaml.
	EtcDir string
	// ContentRoot is /srv/localai, the internal content root.
	ContentRoot string
	// InstallState is /var/lib/localai/install/state.json.
	InstallState string
	// Drives returns the mount points of mounted ark drives.
	Drives func() []string
	// TmpDir is where the browser's scratch folder goes ("" is the system default).
	TmpDir string
	// Owner returns the owner of the written files when running as root (nil: keep the caller's).
	Owner func() *docsrender.Owner
}

func defaultDocsEnvironment() docsEnvironment {
	return docsEnvironment{
		Runner:       execx.Exec{},
		Lookup:       execx.Lookup,
		Now:          time.Now,
		Getwd:        os.Getwd,
		Hostname:     os.Hostname,
		EtcDir:       "/etc/localai",
		ContentRoot:  "/srv/localai",
		InstallState: "/var/lib/localai/install/state.json",
		Drives:       mountedArkDrives,
		Owner:        localaiOwner,
	}
}

var docsEnv = defaultDocsEnvironment()

// arkDriveLabel is the filesystem label, and so the mount-point name, of an ark drive (overview 3.2).
const arkDriveLabel = "AI-ARK"

// mountedArkDrives lists the mount points named AI-ARK in /proc/mounts.
func mountedArkDrives() []string {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	return arkDrivesIn(string(b))
}

// arkDrivesIn picks the AI-ARK mount points out of /proc/mounts text. The kernel escapes spaces in
// mount points as \040.
func arkDrivesIn(mounts string) []string {
	var out []string
	for _, line := range strings.Split(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if filepath.Base(mp) == arkDriveLabel {
			out = append(out, mp)
		}
	}
	return out
}

// localaiOwner is the `localai` service user, when it exists and this process is root.
func localaiOwner() *docsrender.Owner {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup("localai")
	if err != nil {
		return nil
	}
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil {
		return nil
	}
	return &docsrender.Owner{UID: uid, GID: gid}
}

// stringList is a repeatable string flag (--out).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func init() {
	Register(&Command{
		Name:    "docs",
		Summary: "Make the offline manuals and the survival card.",
		Sub: []*Command{{
			Name:    "render",
			Summary: "Render the runbooks and specs to HTML (and PDF) and make the survival card.",
			Usage:   "[--src <docs-dir>] [--inventory <file>] [--out <dir>]... [--no-pdf] [--node <id>]",
			Description: `Fills the <PLACEHOLDERS> of the documents from inventory.local.yaml and writes untracked,
self-contained HTML (and PDF when Chromium is installed) to every --out folder, plus the one-page
survival card of this node. Refuses an --out inside a git checkout (exit 2). Works offline.
Specification: docs/specs/aictl.md section 4.22`,
			Marks:   MarkLocal,
			Timeout: 10 * time.Minute,
			Flags: func(fs *flag.FlagSet) {
				fs.String("src", "", "documents folder (default ./docs when run in a repository checkout)")
				fs.String("inventory", "", "inventory.local.yaml (default: /etc/localai, the ark drive, then the current folder)")
				fs.Var(new(stringList), "out", "output folder; repeat for several (default /srv/localai/docs and the ark drive)")
				fs.Bool("no-pdf", false, "make HTML only")
				fs.String("node", "", "node id for the survival card (default: from node.yaml)")
			},
			Run: docsRender,
		}},
	})
}

func docsRender(c *Call) error {
	env := docsEnv
	if len(c.Args) > 0 {
		return c.Err(CodeUsage, fmt.Sprintf("unexpected argument %q", c.Args[0]))
	}
	cwd, _ := env.Getwd()
	drives := env.Drives()

	src := c.String("src")
	if src == "" {
		if fi, err := os.Stat(filepath.Join(cwd, "docs", "runbooks")); cwd != "" && err == nil && fi.IsDir() {
			src = filepath.Join(cwd, "docs")
		} else {
			return c.Err(CodeUsage, pick(c.Lang,
				"--src is required: this is not a repository checkout (there is no ./docs/runbooks).",
				"Falta --src: esta no es una copia del repositorio (no existe ./docs/runbooks)."))
		}
	}

	var outs []string
	if l, ok := c.Flags.Lookup("out").Value.(*stringList); ok {
		outs = append(outs, *l...)
	}
	if len(outs) == 0 {
		outs = append(outs, filepath.Join(env.ContentRoot, "docs"))
		for _, d := range drives {
			outs = append(outs, filepath.Join(d, "localai", "docs"))
		}
	}

	contentRoots := []string{env.ContentRoot}
	catalogDirs := []string{filepath.Join(env.EtcDir, "catalog"), filepath.Join(env.ContentRoot, "kit", "catalog")}
	for _, d := range drives {
		contentRoots = append(contentRoots, filepath.Join(d, "localai"))
		catalogDirs = append(catalogDirs, filepath.Join(d, "localai", "kit", "catalog"))
	}
	host, _ := env.Hostname()
	rep, err := docsrender.Run(c.Ctx, docsrender.Config{
		Src:           src,
		Outs:          outs,
		InventoryPath: docsrender.FindInventory(docsrender.InventoryLookup{Explicit: c.String("inventory"), EtcDir: env.EtcDir, DriveRoots: drives, Cwd: cwd}),
		NoPDF:         c.Bool("no-pdf"),
		NodeID:        c.String("node"),
		State: docsrender.StateSources{
			NodeYAML:     filepath.Join(env.EtcDir, "node.yaml"),
			InstallState: env.InstallState,
			ContentRoots: contentRoots,
			CatalogDirs:  catalogDirs,
		},
		Hostname: host,
		Now:      env.Now(),
		Runner:   env.Runner,
		Lookup:   env.Lookup,
		TmpDir:   env.TmpDir,
		Owner:    env.Owner(),
	})
	if err != nil {
		return docsError(c, err)
	}
	docsReport(c, rep)
	return nil
}

// docsError maps a renderer error to its doctor code.
func docsError(c *Call, err error) error {
	var de *docsrender.Error
	if !errors.As(err, &de) {
		return c.Err(CodeFileIO, err.Error()).WithCause(err)
	}
	switch de.Kind {
	case docsrender.KindUsage:
		return c.Err(CodeUsage, de.Error()).WithCause(err)
	case docsrender.KindCardPages:
		return c.Err(CodeCardTooLong, "").WithDetails("template", docsrender.CardTemplateRel).WithCause(err)
	case docsrender.KindCardSecret:
		e := c.Err(CodeCardPrivate, "")
		names := make([]string, len(de.Placeholders))
		for i, n := range de.Placeholders {
			names[i] = "<" + n + ">"
		}
		e.Message += " " + pick(c.Lang, "Placeholders: ", "Marcadores: ") + strings.Join(names, ", ") + "."
		return e.WithDetails("placeholders", de.Placeholders).WithCause(err)
	}
	return c.Err(CodeFileIO, de.Error()).WithCause(err)
}

// docsReport prints the result and queues the warnings.
func docsReport(c *Call, rep *docsrender.Report) {
	others := rep.Pages - rep.Runbooks - rep.Specs
	where := strings.Join(rep.Outputs, pick(c.Lang, " and ", " y "))
	var what string
	switch {
	case others > 0:
		what = pick(c.Lang,
			fmt.Sprintf("%d runbooks, %d specs and %d other documents", rep.Runbooks, rep.Specs, others),
			fmt.Sprintf("%d guías, %d especificaciones y %d documentos más", rep.Runbooks, rep.Specs, others))
	default:
		what = pick(c.Lang,
			fmt.Sprintf("%d runbooks and %d specs", rep.Runbooks, rep.Specs),
			fmt.Sprintf("%d guías y %d especificaciones", rep.Runbooks, rep.Specs))
	}
	pdf := pick(c.Lang, "HTML only", "solo HTML")
	if rep.PDFs > 0 {
		pdf = pick(c.Lang, fmt.Sprintf("PDF: %d files", rep.PDFs), fmt.Sprintf("PDF: %d archivos", rep.PDFs))
	}
	c.Out.OK(pick(c.Lang,
		fmt.Sprintf("Rendered %s to %s (%s).", what, where, pdf),
		fmt.Sprintf("Se generaron %s en %s (%s).", what, where, pdf)))
	if rep.Card != "" && len(rep.Outputs) > 0 {
		c.Out.Info(pick(c.Lang,
			"Survival card: "+filepath.Join(rep.Outputs[0], rep.Card+".html"),
			"Tarjeta de supervivencia: "+filepath.Join(rep.Outputs[0], rep.Card+".html")))
	}

	broken := 0
	for _, w := range rep.Warnings {
		switch w.Kind {
		case docsrender.WarnNoInventory:
			if w.Path == "" {
				c.Warn(c.Err(CodeInventoryMissing, ""))
			} else {
				c.Warn(c.Err(CodeInventoryMissing, w.Message))
			}
		case docsrender.WarnNoPDF:
			c.Warn(c.Err(CodeNoPDFPrinter, ""))
		case docsrender.WarnBrokenLink:
			broken++
			if c.Global.Verbose {
				c.Out.Warn(w.Path + ": " + w.Message)
			}
		default:
			msg := w.Message
			if w.Path != "" {
				msg = w.Path + ": " + msg
			}
			c.Out.Warn(msg)
		}
	}
	if broken > 0 {
		c.Out.Warn(pick(c.Lang,
			fmt.Sprintf("%d links in the documents point nowhere (run with -v to list them).", broken),
			fmt.Sprintf("%d enlaces de los documentos no llevan a ningún sitio (use -v para verlos).", broken)))
	}

	if c.Global.JSON {
		var card any
		if rep.Card != "" {
			card = rep.Card
		}
		outputs := rep.Outputs
		if outputs == nil {
			outputs = []string{}
		}
		_ = c.Out.JSON().Document(map[string]any{
			"outputs": outputs, "pages": rep.Pages, "pdf": rep.PDFs,
			"inventory": rep.Inventory, "survival_card": card,
		})
	}
}
