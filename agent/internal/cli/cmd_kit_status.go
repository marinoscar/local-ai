package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// `aictl kit` / `aictl kit status [--root <id>]` (aictl.md section 4.23c, rebuild-kit.md section 3.5,
// KIT-03): which parts of the rebuild kit are present and verified on this computer and on every
// mounted ark drive, what this computer needs from the kit, and, on a drive, what every other computer
// of the lab needs. It reads the disk directly and re-reads no file (that is `aictl kit verify`), so it
// answers in a moment with the agent up or down.

func init() {
	status := &Command{
		Name:    "status",
		Summary: "Show which parts of the rebuild kit are present and verified.",
		Usage:   "[--root <id>]",
		Description: `Shows, for this computer's disk and for every mounted ark drive, which parts of the rebuild kit
are there and checked, what this computer needs from the kit (sources, the agent, the Go toolchain,
offline packages, CUDA, tools) and, on a drive, what every other computer of the lab needs. Nothing
is read again from the disk: run "aictl kit verify" for that. "aictl kit" alone is the same as
"aictl kit status".
  --root <id>  Use one root: internal, an id such as ark-3f9c0a12, or a full path.
--json prints {"overall", "roots": [...]}. Exit 5 when no root has a kit.
Specification: docs/specs/rebuild-kit.md section 3.5`,
		Marks: MarkLocal,
		Flags: func(fs *flag.FlagSet) {
			fs.String("root", "", "use this root: internal, an id such as ark-3f9c0a12, or a full path")
		},
		Timeout: 2 * time.Minute,
		Run:     kitStatus,
	}
	registerKitSub(status)
	// `aictl kit` alone is `aictl kit status`; a word that is not a subcommand keeps the group's own
	// answer (the list of subcommands, exit 2).
	RegisterLate(func(r *Registry) {
		group, ok := r.Lookup("kit")
		if !ok {
			return
		}
		list := group.Run
		group.Flags = status.Flags
		if group.Timeout == 0 {
			group.Timeout = status.Timeout
		}
		group.Run = func(c *Call) error {
			if len(c.Args) > 0 && list != nil {
				return list(c)
			}
			return kitStatus(c)
		}
	})
}

func kitStatus(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	env, host := localEnv(), kitHostFn()
	roots, err := kitRoots(c, env, c.String("root"))
	if err != nil {
		return err
	}
	facts := kitReadFacts(c.Ctx, env, host)
	st, err := kit.Verifier{FS: kitVerifyFS(host.FS)}.Status(c.Ctx, statusRoots(roots), kit.StatusOptions{
		Now: env.Now(), ReverifyDays: facts.ReverifyDays, Node: facts.Node, Keys: host.Keys,
	})
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(st)
	}
	for _, rs := range st.Roots {
		if rs.HasKit {
			kitRenderStatus(c, env.Now(), rs)
		}
	}
	return nil
}

// kitRenderStatus prints the kit of one root: the parts, what this computer needs, what the lab needs
// (on a drive) and the problems, each code's fix said once.
func kitRenderStatus(c *Call, now time.Time, rs kit.RootStatus) {
	p := c.Out
	l := c.Lang
	p.Blank()
	p.Result(fmt.Sprintf(pick(l, "Rebuild kit on %s: %s", "Kit de reconstrucción en %s: %s"),
		kitRootName(l, rs.Root, rs.Internal), kitColourWord(l, rs.Overall)))
	var rows [][]string
	for _, part := range rs.Parts {
		state := kitStateWord(l, part.Status)
		checked := ""
		switch {
		case !part.Present:
			state = pick(l, "not here", "no está")
		case part.OldestReverifiedAt != nil:
			checked = out.Relative(*part.OldestReverifiedAt, now, l)
		}
		rows = append(rows, []string{part.Part, state, part.Code, checked})
	}
	p.Table([]out.Col{{Title: pick(l, "PART", "PARTE")}, {Title: pick(l, "STATE", "ESTADO")}, {Title: pick(l, "CODE", "CÓDIGO")},
		{Title: pick(l, "LAST READ AGAIN", "ÚLTIMA LECTURA")}}, rows)

	var lackingNode, lackingLab []string
	if rs.Node != nil && len(rs.Node.Needs) > 0 {
		p.Blank()
		p.Result(fmt.Sprintf(pick(l, "What %s needs from the kit:", "Lo que %s necesita del kit:"), kitNodeLabel(l, rs.Node)))
		p.Table(needCols(l), needRows(l, rs.Node.Needs))
		for _, n := range rs.Node.Needs {
			if n.Status == kit.StatusMissing {
				lackingNode = append(lackingNode, kitNeedText(l, n.Need, n.Arg))
			}
		}
	}
	if len(rs.Lab) > 0 {
		p.Blank()
		p.Result(pick(l, "What the other computers of the lab need (from pins.json):", "Lo que necesitan los otros equipos del laboratorio (de pins.json):"))
		for i := range rs.Lab {
			lab := &rs.Lab[i]
			var lacking []string
			for _, n := range lab.Needs {
				if n.Status != kit.StatusOK && !n.Optional {
					lacking = append(lacking, kitNeedText(l, n.Need, n.Arg))
				}
			}
			state := pick(l, "complete", "completo")
			if len(lacking) > 0 {
				state = pick(l, "something is missing", "falta algo")
				lackingLab = append(lackingLab, kitNodeLabel(l, lab)+": "+strings.Join(lacking, "; "))
			}
			p.Result("  " + kitNodeLabel(l, lab) + "  " + state)
		}
	}
	if len(lackingNode) > 0 {
		colour := kit.Yellow
		for _, f := range rs.Findings {
			if f.Code == kit.CodeKitMissingNode && f.Colour == kit.Red {
				colour = kit.Red
			}
		}
		p.Finding(kitSummary(l, kit.CodeKitMissingNode, colour,
			fmt.Sprintf(pick(l, "The kit is missing parts for this computer: %s.", "Al kit le faltan partes para este equipo: %s."), strings.Join(lackingNode, "; "))))
	}
	if len(lackingLab) > 0 {
		p.Finding(kitSummary(l, kit.CodeKitMissingLab, kit.Yellow,
			fmt.Sprintf(pick(l, "The drive lacks kit parts for other computers of the lab. %s.", "Al disco le faltan partes del kit para otros equipos del laboratorio. %s."),
				strings.Join(lackingLab, ". "))))
	}
	for _, f := range rs.Findings {
		if (f.Code != kit.CodeKitMissingNode && f.Code != kit.CodeKitMissingLab) || f.Optional {
			p.Finding(kitProblem(l, f))
		}
	}
	for _, n := range rs.Notes {
		if msg, code := kitNoteText(l, n); msg != "" {
			if code != "" {
				msg += " [" + code + "]"
			}
			p.Info(msg)
		}
	}
	if len(lackingNode) == 0 && len(lackingLab) == 0 && len(rs.Findings) == 0 {
		p.OK(pick(l, "Everything is present. Run `aictl kit verify` to read the files again and prove they are intact.",
			"Todo está presente. Ejecute `aictl kit verify` para leer los archivos otra vez y comprobar que están intactos."))
	}
}

// kitSummary is a problem about several missing parts at once, with the code's own fix.
func kitSummary(l Lang, code, colour, message string) out.Problem {
	sev := out.SevWarning
	if colour == kit.Red {
		sev = out.SevError
	}
	return out.Problem{Severity: sev, Code: code, Message: message, Fix: kitFixOf(l, code)}
}

// kitNodeLabel names a computer: "ai-n3 (resolute, amd64, cuda)".
func kitNodeLabel(l Lang, n *kit.NodeResult) string {
	id := n.Node
	if id == "" {
		id = pick(l, "this computer", "este equipo")
	}
	var bits []string
	for _, s := range []string{n.Codename, n.Arch, n.Backend} {
		if s != "" {
			bits = append(bits, s)
		}
	}
	if len(bits) == 0 {
		return id
	}
	return id + " (" + strings.Join(bits, ", ") + ")"
}

func needCols(l Lang) []out.Col {
	return []out.Col{{Title: pick(l, "NEEDED", "NECESARIO")}, {Title: pick(l, "STATE", "ESTADO")}, {Title: pick(l, "CODE", "CÓDIGO")}}
}

func needRows(l Lang, needs []kit.Need) [][]string {
	var rows [][]string
	for _, n := range needs {
		rows = append(rows, []string{kitNeedText(l, n.Need, n.Arg), kitStateWord(l, n.Status), n.Code})
	}
	return rows
}
