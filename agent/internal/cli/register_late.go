package cli

import "sync"

// Several issues add subcommands to the same group (`ark`, `catalog`, `profile`, `cloud`): the
// read-only ones (AICTL-04), the state-changing ones (AICTL-06), the local root ones (AICTL-07).
// Register panics on a duplicate name, and Go runs init functions in file-name order, so two files
// that each Register("ark") would break every command of the binary the moment both are merged.
//
// A file that only adds subcommands to a group therefore does not register in init. It queues a
// hook with RegisterLate, and Main applies the hooks once, after every init has run, so the group
// exists whatever the file order: the hook finds the group (or creates it when this build has no
// other source for it) and adds its subcommands. A subcommand that already exists under the same
// name is replaced: that is how a real command takes over from a "not implemented yet" stub.

var (
	lateMu    sync.Mutex
	lateHooks []func(*Registry)
	lateDone  bool
)

// RegisterLate queues fn to run against the registry Main dispatches on, after every init. Call it
// from an init function.
func RegisterLate(fn func(*Registry)) {
	lateMu.Lock()
	defer lateMu.Unlock()
	lateHooks = append(lateHooks, fn)
}

// applyLate runs the queued hooks against r, once per process (Main calls it for DefaultRegistry;
// tests that exercise the real command tree call it too).
func applyLate(r *Registry) {
	lateMu.Lock()
	defer lateMu.Unlock()
	if lateDone {
		return
	}
	lateDone = true
	for _, fn := range lateHooks {
		fn(r)
	}
}

// groupSummaries are the summaries of the groups a late hook may have to create.
var groupSummaries = map[string]string{
	"ark":     "Check and fill the offline content (the ark).",
	"catalog": "Check and manage the catalog of models and ark content.",
	"profile": "Show and change the profile of this node.",
	"cloud":   "Connect this node to the cloud.",
}

// attach adds cmd under the group path (for example ["ark", "drives"]) in r. A missing group on the
// way is created (a pure group: no Run). A subcommand of the same name is replaced.
func attach(r *Registry, path []string, cmd *Command) {
	top, ok := r.Lookup(path[0])
	if !ok {
		top = &Command{Name: path[0], Summary: groupSummaries[path[0]]}
		if top.Summary == "" {
			top.Summary = "Commands of " + path[0] + "."
		}
		r.cmds[path[0]] = top
	}
	parent := top
	for _, name := range path[1:] {
		next := findSub(parent, name)
		if next == nil {
			next = &Command{Name: name, Summary: "Commands of " + name + "."}
			parent.Sub = append(parent.Sub, next)
		}
		parent = next
	}
	for i, s := range parent.Sub {
		if s.Name == cmd.Name {
			parent.Sub[i] = cmd
			return
		}
	}
	parent.Sub = append(parent.Sub, cmd)
}

func findSub(c *Command, name string) *Command {
	for _, s := range c.Sub {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// attachAll registers hook that attaches every (path, command) pair.
func attachAll(pairs ...attachment) {
	RegisterLate(func(r *Registry) {
		for _, p := range pairs {
			attach(r, p.path, p.cmd)
		}
	})
}

type attachment struct {
	path []string
	cmd  *Command
}

func at(cmd *Command, path ...string) attachment { return attachment{path: path, cmd: cmd} }
