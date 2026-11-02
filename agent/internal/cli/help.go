package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// globalFlagsHelp documents the global flags of aictl.md section 3.1. A test keeps it in step with
// the flags parseGlobal registers.
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

// writeHelp prints the top-level help: usage, the registered commands with their marks, the global
// flags.
func writeHelp(w io.Writer, reg *Registry) {
	fmt.Fprint(w, "aictl: command line for the Local AI node agent.\n\n")
	fmt.Fprint(w, "Usage:\n  aictl [global flags] <command> [subcommand] [flags] [args]\n  aictl help [command]\n\n")
	cmds := reg.Commands()
	if len(cmds) == 0 {
		fmt.Fprint(w, "No commands are available in this build yet.\n\n")
	} else {
		fmt.Fprint(w, "Commands:\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, c := range cmds {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", c.Name, c.Summary, c.Marks)
		}
		tw.Flush()
		fmt.Fprintln(w)
	}
	fmt.Fprint(w, globalFlagsHelp)
	fmt.Fprint(w, "\nRun `aictl help <command>` for the details of one command.\n")
}

// writeCommandHelp prints the help of one command: synopsis, summary, marks, default timeout, its
// flags and its subcommands.
func writeCommandHelp(w io.Writer, res resolved) {
	cmd := res.cmd
	path := strings.Join(res.path, " ")
	usage := "aictl [global flags] " + path
	if len(cmd.Sub) > 0 && cmd.Run == nil {
		usage += " <subcommand>"
	}
	if cmd.Usage != "" {
		usage += " " + cmd.Usage
	}
	fmt.Fprintf(w, "Usage:\n  %s\n\n%s\n", usage, cmd.Summary)
	if cmd.Description != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(cmd.Description))
	}
	if m := cmd.Marks.String(); m != "" {
		fmt.Fprintf(w, "\nMarks: %s\n", m)
	}
	if cmd.Timeout > 0 {
		fmt.Fprintf(w, "Default timeout: %s (change it with --timeout)\n", cmd.Timeout.Round(time.Second))
	}
	if cmd.Flags != nil {
		fs := flag.NewFlagSet(path, flag.ContinueOnError)
		cmd.Flags(fs)
		var n int
		fs.VisitAll(func(*flag.Flag) { n++ })
		if n > 0 {
			fmt.Fprint(w, "\nFlags:\n")
			fs.SetOutput(w)
			fs.PrintDefaults()
		}
	}
	if len(cmd.Sub) > 0 {
		fmt.Fprint(w, "\nSubcommands:\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range cmd.Sub {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, s.Summary, s.Marks)
		}
		tw.Flush()
	}
}

// shortUsage is the two-line hint printed with a usage error.
func shortUsage(w io.Writer, path string) {
	if path == "" {
		fmt.Fprint(w, "Usage: aictl [global flags] <command> [args]\n")
		return
	}
	fmt.Fprintf(w, "Usage: aictl [global flags] %s [flags] [args]\n", path)
}
