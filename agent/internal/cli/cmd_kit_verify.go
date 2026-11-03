package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// `aictl kit verify [--root <id>] [--part <name>] [--full]` (aictl.md section 4.23c, rebuild-kit.md
// sections 3.5 and 4.4, KIT-03). It runs in this process and needs no agent, no network and no root; it
// reads files and never changes one. The work is in kit.Verifier; this file is flags, roots, the progress
// line, the words and the exit code.

// kitVerifyTimeout is the default deadline: a full read of a drive of hundreds of gigabytes takes hours.
const kitVerifyTimeout = 6 * time.Hour

func init() {
	registerKitSub(&Command{
		Name:    "verify",
		Summary: "Check that the rebuild kit is complete, signed and undamaged.",
		Usage:   "[--root <id>] [--part <name>] [--full]",
		Description: `Reads the kit again from the disk and checks it against its records: every file of an item that
has not been re-checked within sync.reverify_days (30 by default), or every file with --full; the
signature of the agent checksums; that this computer has everything it needs to rebuild itself
(sources, the agent, the Go toolchain, offline packages, CUDA, tools) and, on an ark drive, that
every computer listed in the drive's pins.json has too; that the packages are for this Ubuntu release
and the CUDA toolkit fits the driver; and that rebuilding llama.cpp from the kit would work (the dry
run of "aictl rebuild-llama"). Nothing is ever changed, moved or deleted.
  --part <name>  Check one part only: catalog, installers, bin, src, git, toolchains, tools, desktop,
                 debs, nvidia, cuda, prebuilt or iso.
  --full         Read every file again, not only those that are due.
  --root <id>    Use one root: internal, an id such as ark-3f9c0a12, or a full path.
Exit 0: all fine. Exit 8: warnings only. Exit 9: parts missing or inconsistent. Exit 1: a file does
not match its record or the signature failed (LAI-KIT-001, LAI-KIT-044). Exit 2: unknown part. Exit
5: no root has a kit. --json prints {"overall", "roots": [<one report per root>]}.
Specification: docs/specs/rebuild-kit.md section 4.4`,
		Marks:   MarkLocal,
		Timeout: kitVerifyTimeout,
		Flags: func(fs *flag.FlagSet) {
			fs.String("root", "", "use this root: internal, an id such as ark-3f9c0a12, or a full path")
			fs.String("part", "", "check one part only ("+strings.Join(kit.Parts, ", ")+")")
			fs.Bool("full", false, "read every file again, not only those that are due")
		},
		Run: kitVerify,
	})
}

func kitVerify(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	part := c.String("part")
	if part != "" && !kit.ValidPart(part) {
		return c.usageErr(
			pick(c.Lang, "There is no kit part called "+quote(part)+".", "No hay una parte del kit llamada "+quote(part)+"."),
			pick(c.Lang, "Use one of: "+strings.Join(kit.Parts, ", ")+".", "Use una de estas: "+strings.Join(kit.Parts, ", ")+"."))
	}
	env, host := localEnv(), kitHostFn()
	roots, err := kitRoots(c, env, c.String("root"))
	if err != nil {
		return err
	}
	facts := kitReadFacts(c.Ctx, env, host)
	mode := kit.ModeBudgeted
	if c.Bool("full") {
		mode = kit.ModeFull
	}
	v := kit.Verifier{FS: kitVerifyFS(host.FS)}
	var reports []kit.VerifyReport
	for _, r := range roots {
		internal := r.ID == "internal"
		opts := kit.Options{
			Mode: mode, Part: part, Now: env.Now(), ReverifyDays: facts.ReverifyDays,
			RootID: r.ID, Internal: internal, Node: facts.Node, Keys: host.Keys,
			Runner: host.Runner, LookPath: host.LookPath,
			Progress: kitVerifyProgress(c),
		}
		if internal {
			opts.Build = kitBuildFn(env, host, facts, roots)
		}
		rep, err := v.Verify(c.Ctx, r.Path, opts)
		switch {
		case errors.Is(err, kit.ErrNoKit):
			continue
		case err != nil:
			return err
		}
		reports = append(reports, rep)
		if !c.Global.JSON {
			kitRenderVerify(c, rep)
		}
	}
	overall, damaged := "", false
	for _, rep := range reports {
		overall = worseKitColour(overall, rep.Overall)
		damaged = damaged || rep.Damaged()
	}
	if c.Global.JSON {
		if err := c.Out.JSON().Document(map[string]any{"overall": overall, "roots": reports}); err != nil {
			return err
		}
	}
	switch {
	case damaged:
		return ExitWith(ExitFailed)
	case overall == kit.Red:
		return ExitWith(ExitErrors)
	case overall == kit.Yellow:
		return ExitWith(ExitWarnings)
	}
	return nil
}

func worseKitColour(a, b string) string {
	rank := map[string]int{"": 0, kit.Green: 1, kit.Yellow: 2, kit.Red: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// kitVerifyProgress prints one progress line per stage and per item read (stderr, hidden by --json).
func kitVerifyProgress(c *Call) func(kit.Progress) {
	if c.Global.JSON {
		return nil
	}
	lastItem := ""
	return func(p kit.Progress) {
		switch p.Stage {
		case kit.StageScan:
			lastItem = ""
			c.Out.Progress(pick(c.Lang, "Reading the records of the kit", "Leyendo los registros del kit"))
		case kit.StageHash:
			if p.Item != lastItem {
				lastItem = p.Item
				c.Out.Progress(pick(c.Lang, "Reading "+p.Item+" again from the disk", "Leyendo "+p.Item+" otra vez desde el disco"))
			}
		case kit.StageSignature:
			c.Out.Progress(pick(c.Lang, "Checking the signature of the agent files", "Comprobando la firma de los archivos del agente"))
		case kit.StageComplete:
			c.Out.Progress(pick(c.Lang, "Checking what this computer needs", "Comprobando lo que necesita este equipo"))
		case kit.StageBuild:
			c.Out.Progress(pick(c.Lang, "Checking that llama.cpp could be rebuilt from the kit", "Comprobando que llama.cpp se podría reconstruir desde el kit"))
		}
	}
}

// kitRenderVerify prints one report: a summary, the parts, the problems and the notes.
func kitRenderVerify(c *Call, rep kit.VerifyReport) {
	p := c.Out
	l := c.Lang
	p.Blank()
	p.Result(fmt.Sprintf(pick(l, "Rebuild kit on %s: %s", "Kit de reconstrucción en %s: %s"),
		kitRootName(l, rep.Root, rep.Internal), kitColourWord(l, rep.Overall)))
	for _, line := range out.Wrap(kitWorkLine(l, rep), out.DefaultWidth, "  ", "  ") {
		p.Secondary(line)
	}
	if len(rep.Parts) > 0 {
		var rows [][]string
		for _, part := range rep.Parts {
			code := part.Code
			state := kitStateWord(l, part.Status)
			if !part.Present && part.Status == kit.StatusOK {
				state = pick(l, "not here (not needed)", "no está (no hace falta)")
			}
			rows = append(rows, []string{part.Part, state, code})
		}
		p.Table([]out.Col{{Title: pick(l, "PART", "PARTE")}, {Title: pick(l, "STATE", "ESTADO")}, {Title: pick(l, "CODE", "CÓDIGO")}}, rows)
	}
	for _, f := range rep.Findings {
		p.Finding(kitProblem(l, f))
	}
	if rep.Build != nil && rep.Build.Skipped != "" {
		p.Info(pick(l, "The dry run of the llama.cpp rebuild was not made: "+rep.Build.Skipped+".",
			"No se hizo la prueba de reconstrucción de llama.cpp: "+rep.Build.Skipped+"."))
	}
	for _, n := range rep.Notes {
		if msg, code := kitNoteText(l, n); msg != "" {
			if code != "" {
				msg += " [" + code + "]"
			}
			p.Info(msg)
		}
	}
	if rep.Overall == kit.Green && len(rep.Findings) == 0 {
		p.OK(pick(l, "Everything this computer needs is in the kit and every file checked out.",
			"Todo lo que este equipo necesita está en el kit y cada archivo es correcto."))
	}
}

// kitWorkLine says how much was read.
func kitWorkLine(l Lang, rep kit.VerifyReport) string {
	t := rep.Totals
	switch {
	case rep.Mode == kit.ModeQuick || t.FilesHashed == 0:
		return pick(l, fmt.Sprintf("Nothing needed reading again (%d items checked by name, size and record).", t.Items),
			fmt.Sprintf("No hizo falta leer nada otra vez (%d elementos comprobados por nombre, tamaño y registro).", t.Items))
	case rep.Mode == kit.ModeFull:
		return fmt.Sprintf(pick(l, "Everything was read again from the disk: %d files, %s.", "Todo se leyó otra vez desde el disco: %d archivos, %s."),
			t.FilesHashed, kitSize(t.BytesHashed))
	}
	return fmt.Sprintf(pick(l, "%d files (%s) that were due were read again from the disk; the rest was checked by name and size.",
		"%d archivos (%s) que tocaba revisar se leyeron otra vez desde el disco; el resto se comprobó por nombre y tamaño."),
		t.FilesHashed, kitSize(t.BytesHashed))
}
