package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Marks say where a command runs and who may run it, as shown in help (aictl.md section 4).
type Marks uint8

const (
	// MarkLocal: runs on this machine without the agent (offline recovery commands).
	MarkLocal Marks = 1 << iota
	// MarkRoot: must run as root (RequireRoot).
	MarkRoot
	// MarkAdmin: needs the admin PIN unless the caller is root over the socket.
	MarkAdmin
)

// String renders the marks as help shows them: "[local] [root] [admin]".
func (m Marks) String() string {
	var parts []string
	if m&MarkLocal != 0 {
		parts = append(parts, "[local]")
	}
	if m&MarkRoot != 0 {
		parts = append(parts, "[root]")
	}
	if m&MarkAdmin != 0 {
		parts = append(parts, "[admin]")
	}
	return strings.Join(parts, " ")
}

// Command is one node of the command tree. Later issues (AICTL-02..10) add commands by calling
// Register from an init function in their own file; the framework needs no change.
//
//	func init() {
//		cli.Register(&cli.Command{
//			Name: "status", Summary: "Show the state of this node.", Timeout: 10 * time.Second,
//			Flags: func(fs *flag.FlagSet) { fs.Bool("all", false, "include passed checks") },
//			Run:   func(c *cli.Call) error { ... },
//		})
//	}
type Command struct {
	// Name is the word typed after the global flags: no spaces, no leading dash, not "help".
	Name string
	// Summary is the one line shown in the command list. Required.
	Summary string
	// Usage is the synopsis after the command path, for example "<model> [--wait]".
	Usage string
	// Description is optional longer help text.
	Description string
	// Marks are shown in help; the framework does not enforce them (commands call RequireRoot).
	Marks Marks
	// Timeout is the default overall deadline (--timeout overrides it); zero means none.
	Timeout time.Duration
	// Flags declares this command's flags on its own FlagSet (stdlib flag, one set per command).
	// Flags may appear before or after the positional arguments.
	Flags func(fs *flag.FlagSet)
	// Run executes the command. It returns nil, an error from the Client, a *CLIError, or
	// ExitWith(code) for a result that is not a failure (exit 8 or 9). Nil Run with Sub set means
	// the command only groups subcommands.
	Run func(c *Call) error
	// Sub are subcommands, matched on the first positional argument.
	Sub []*Command
}

// Registry holds the command tree. Register adds to DefaultRegistry; tests build their own.
type Registry struct {
	cmds map[string]*Command
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{cmds: map[string]*Command{}} }

// DefaultRegistry is the registry Main dispatches on.
var DefaultRegistry = NewRegistry()

// Register adds cmd to DefaultRegistry and panics on a programming error (invalid or duplicate
// command), so a bad registration fails at start-up and in every test of the binary.
func Register(cmd *Command) { DefaultRegistry.MustAdd(cmd) }

// MustAdd is Add that panics on error.
func (r *Registry) MustAdd(cmd *Command) {
	if err := r.Add(cmd); err != nil {
		panic("cli: " + err.Error())
	}
}

// Add validates cmd (and its subcommands) and adds it. It refuses a duplicate name and the reserved
// name "help".
func (r *Registry) Add(cmd *Command) error {
	if err := validateCommand(cmd, ""); err != nil {
		return err
	}
	if cmd.Name == "help" {
		return errors.New(`command name "help" is reserved`)
	}
	if _, dup := r.cmds[cmd.Name]; dup {
		return fmt.Errorf("command %q is already registered", cmd.Name)
	}
	r.cmds[cmd.Name] = cmd
	return nil
}

func validateCommand(cmd *Command, parent string) error {
	path := strings.TrimSpace(parent + " " + cmd.Name)
	switch {
	case cmd.Name == "" || strings.ContainsAny(cmd.Name, " \t\n") || strings.HasPrefix(cmd.Name, "-"):
		return fmt.Errorf("invalid command name %q", path)
	case cmd.Summary == "":
		return fmt.Errorf("command %q has no summary", path)
	case cmd.Run == nil && len(cmd.Sub) == 0:
		return fmt.Errorf("command %q has neither Run nor subcommands", path)
	}
	seen := map[string]bool{}
	for _, s := range cmd.Sub {
		if err := validateCommand(s, path); err != nil {
			return err
		}
		if seen[s.Name] {
			return fmt.Errorf("subcommand %q of %q is declared twice", s.Name, path)
		}
		seen[s.Name] = true
	}
	return nil
}

// Lookup returns the top-level command called name.
func (r *Registry) Lookup(name string) (*Command, bool) {
	c, ok := r.cmds[name]
	return c, ok
}

// Commands returns the top-level commands sorted by name.
func (r *Registry) Commands() []*Command {
	out := make([]*Command, 0, len(r.cmds))
	for _, c := range r.cmds {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolved is the outcome of walking the command tree.
type resolved struct {
	cmd  *Command
	path []string // command words, e.g. ["key", "show"]
	rest []string // arguments after the command path
}

// resolve walks the tree from args. A command with subcommands descends when the next word names
// one; a command that also has its own Run takes any other word as a positional argument; a
// group-only command with an unknown or missing word is an error (the caller prints its help).
func (r *Registry) resolve(args []string) (res resolved, unknown string, err error) {
	if len(args) == 0 {
		return resolved{}, "", errors.New("no command")
	}
	top, ok := r.cmds[args[0]]
	if !ok {
		return resolved{}, args[0], errors.New("unknown command")
	}
	res = resolved{cmd: top, path: []string{top.Name}, rest: args[1:]}
	for len(res.cmd.Sub) > 0 && len(res.rest) > 0 && !strings.HasPrefix(res.rest[0], "-") {
		var next *Command
		for _, s := range res.cmd.Sub {
			if s.Name == res.rest[0] {
				next = s
				break
			}
		}
		if next == nil {
			if res.cmd.Run != nil {
				return res, "", nil
			}
			return res, res.rest[0], errors.New("unknown subcommand")
		}
		res.cmd, res.path, res.rest = next, append(res.path, next.Name), res.rest[1:]
	}
	if res.cmd.Run == nil {
		return res, "", errors.New("missing subcommand")
	}
	return res, "", nil
}
