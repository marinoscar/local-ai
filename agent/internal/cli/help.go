package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// globalFlagsHelp documents the global flags of aictl.md section 3.1. A test keeps it in step with
// the flags parseGlobal registers; globalFlagsHelpES (help_es.go) is its Spanish twin.
const globalFlagsHelp = `Global flags (before the command name):
  --json              Machine output on stdout: one JSON document, no colour, no prompts
  --lang en|es        Language of human output (default: from $LANG, else en)
  --no-color          No colour (also: NO_COLOR set, TERM=dumb, or stdout not a terminal)
  -y, --yes           Answer "yes" to confirmations
  -q, --quiet         Only errors and the essential result line
  -v, --verbose       More detail: request ids, details, the transport used
  --socket <path>     Unix socket (default /run/localai/agent.sock, or $LOCALAI_SOCKET)
  --url <url>         TCP fallback base URL, must be this computer
                      (default http://127.0.0.1:7070, or $LOCALAI_URL)
  --token-file <path> Token for the TCP fallback
                      (default /etc/localai/agent-token, or $LOCALAI_TOKEN_FILE)
  --timeout <dur>     Overall deadline, for example 90s or 10m (0: none)
  --pin-stdin         Read the admin PIN from stdin (one line) instead of asking
  -h, --help          Help for the command
`

// summaryOf is the one-line summary of the command at path in language l: the Spanish table for
// --lang es, else (and for a command the table does not know) the command's own English text.
func summaryOf(l Lang, path []string, c *Command) string {
	if l == LangES {
		if s, ok := helpES[strings.Join(path, " ")]; ok {
			return s
		}
	}
	return c.Summary
}

// timeoutText is a default deadline for the command list: "10 s", "30 min", "1 h 30 min"; "-" for none.
func timeoutText(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d%time.Minute == 0 && d < time.Hour:
		return fmt.Sprintf("%d min", d/time.Minute)
	case d%time.Second == 0 && d < time.Minute:
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return out.Duration(d)
}

// sortedSubs returns the subcommands of c by name, so help does not depend on the order in which the
// files registered them.
func sortedSubs(c *Command) []*Command {
	subs := append([]*Command(nil), c.Sub...)
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	return subs
}

// writeCommandList writes one row per command and per subcommand, by full path, with its marks, its
// default deadline and its summary: the list `aictl help` prints and the one aictl.md section 4 is
// checked against (help_test.go).
func writeCommandList(w io.Writer, reg *Registry, l Lang) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var row func(c *Command, path []string)
	row = func(c *Command, path []string) {
		deadline := "-"
		if c.Run != nil {
			deadline = timeoutText(c.Timeout)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", strings.Join(path, " "), c.Marks, deadline, summaryOf(l, path, c))
		for _, s := range sortedSubs(c) {
			row(s, append(append([]string(nil), path...), s.Name))
		}
	}
	for _, c := range reg.Commands() {
		row(c, []string{c.Name})
	}
	tw.Flush()
}

// writeHelp prints the top-level help: usage, the registered commands with their marks, the global
// flags.
func writeHelp(w io.Writer, reg *Registry, l Lang) {
	hw := helpFor(l)
	fmt.Fprint(w, hw.intro+"\n\n")
	fmt.Fprint(w, hw.usage+"\n  aictl [global flags] <command> [subcommand] [flags] [args]\n  aictl help [command]\n\n")
	if len(reg.Commands()) == 0 {
		fmt.Fprint(w, pick(l, "No commands are available in this build yet.", "Todavía no hay comandos disponibles en esta compilación.")+"\n\n")
	} else {
		fmt.Fprint(w, hw.commands+"\n")
		writeCommandList(w, reg, l)
		fmt.Fprintln(w)
		fmt.Fprint(w, hw.marksLegend+"\n\n")
	}
	fmt.Fprint(w, hw.globalFlags)
	fmt.Fprint(w, "\n"+hw.run+"\n")
}

// writeCommandHelp prints the help of one command: synopsis, summary, marks, default timeout, its
// flags and its subcommands.
func writeCommandHelp(w io.Writer, res resolved, l Lang) {
	hw := helpFor(l)
	cmd := res.cmd
	path := strings.Join(res.path, " ")
	usage := "aictl [global flags] " + path
	if l == LangES {
		usage = "aictl [opciones globales] " + path
	}
	if len(cmd.Sub) > 0 && cmd.Run == nil {
		usage += pick(l, " <subcommand>", " <subcomando>")
	}
	if cmd.Usage != "" {
		usage += " " + cmd.Usage
	}
	fmt.Fprintf(w, "%s\n  %s\n\n%s\n", hw.usage, usage, summaryOf(l, res.path, cmd))
	if cmd.Description != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(cmd.Description))
	}
	if m := cmd.Marks.String(); m != "" {
		fmt.Fprintf(w, "\n%s %s\n", hw.marks, m)
	}
	if cmd.Timeout > 0 {
		fmt.Fprintf(w, hw.timeout+"\n", cmd.Timeout.Round(time.Second))
	}
	if cmd.Flags != nil {
		fs := flag.NewFlagSet(path, flag.ContinueOnError)
		cmd.Flags(fs)
		var n int
		fs.VisitAll(func(*flag.Flag) { n++ })
		if n > 0 {
			fmt.Fprint(w, "\n"+hw.flags+"\n")
			fs.SetOutput(w)
			fs.PrintDefaults()
		}
	}
	if len(cmd.Sub) > 0 {
		fmt.Fprint(w, "\n"+hw.subcommands+"\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range sortedSubs(cmd) {
			subPath := append(append([]string(nil), res.path...), s.Name)
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, summaryOf(l, subPath, s), s.Marks)
		}
		tw.Flush()
	}
}

// shortUsage is the two-line hint printed with a usage error.
func shortUsage(w io.Writer, path string, l Lang) {
	if path == "" {
		fmt.Fprint(w, pick(l, "Usage: aictl [global flags] <command> [args]\n", "Uso: aictl [opciones globales] <comando> [argumentos]\n"))
		return
	}
	fmt.Fprintf(w, helpFor(l).usageHint+"\n", path)
}
