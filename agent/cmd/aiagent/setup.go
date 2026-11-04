package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
	"github.com/marinoscar/local-ai/agent/internal/setup/summary"
	"github.com/marinoscar/local-ai/agent/internal/setup/verify"
)

// geteuid is replaced by tests: `aiagent setup` is root only (installer.md section 3.5, exit 77).
var geteuid = os.Geteuid

const setupUsage = `usage: aiagent setup <step> [flags]      (internal, root only; run by install-node.sh)
  serve-check  decide the Tailscale serve pattern, LAI-INS-050 on a mismatch with the inventory (P11)
  verify       post-install checks that can pass before a reboot (P15)
  summary      print the install summary and the VPS checklist (P15)
  migrate      run every pending migration (llama-qwen) with the installer's logging (P8)
  harden       laptop hardening, --check, --revert (P4; never restarts systemd-logind)
`

// runSetup is `aiagent setup <step>`. Steps owned by other installer items register in setupSteps.
func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, setupUsage)
		return 2
	}
	step, ok := setupSteps[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "aiagent setup: unknown step %q\n%s", args[0], setupUsage)
		return 2
	}
	if geteuid() != 0 {
		fmt.Fprintf(stderr, "aiagent setup %s: must run as root (use sudo)\n", args[0])
		return 77
	}
	return step(args[1:], stdout, stderr)
}

var setupSteps = map[string]func(args []string, stdout, stderr io.Writer) int{
	"serve-check": setupServeCheck,
	"verify":      setupVerify,
	"summary":     setupSummary,
	"migrate":     setupMigrate,
}

type commonFlags struct {
	nodeID, servePattern, nodeYAML, inventory string
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.nodeID, "node-id", "", "node id (default: node.yaml node.id)")
	fs.StringVar(&c.servePattern, "serve-pattern", "", "serve pattern from the command line (A or B)")
	fs.StringVar(&c.nodeYAML, "node-yaml", "/etc/localai/node.yaml", "node.yaml path")
	fs.StringVar(&c.inventory, "inventory", inventory.DefaultPath, "inventory.local.yaml path")
}

// loadInventory returns nil (and a note on stderr only for a damaged file) when there is no usable inventory.
func loadInventory(path string, stderr io.Writer) *inventory.Inventory {
	inv, err := inventory.Load(path)
	switch {
	case err == nil:
		return inv
	case errors.Is(err, inventory.ErrNotFound):
		return nil
	default:
		fmt.Fprintf(stderr, "aiagent setup: ignoring the inventory %s: %v\n", path, err)
		return nil
	}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("aiagent setup "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// setupServeCheck prints the pattern to record ("A", "B" or "none") on stdout; a mismatch with the inventory
// prints the LAI-INS-050 text on stderr and exits 1. It reads only (no --dry-run needed).
func setupServeCheck(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := newFlagSet("serve-check", stderr)
	c.register(fs)
	if fs.Parse(args) != nil {
		return 2
	}
	node, err := verify.LoadNodeYAML(c.nodeYAML)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup serve-check: %v\n", err)
		return 1
	}
	id := c.nodeID
	if id == "" {
		id = node.Node.ID
	}
	p, bad := verify.ServePattern(id, c.servePattern, node, loadInventory(c.inventory, stderr))
	if bad != nil {
		fmt.Fprintln(stderr, bad.Format())
		return 1
	}
	if p == "" {
		p = "none"
	}
	fmt.Fprintln(stdout, p)
	return 0
}

func setupVerify(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	var healthURL string
	fs := newFlagSet("verify", stderr)
	c.register(fs)
	fs.StringVar(&healthURL, "health-url", "", "agent health URL (default http://127.0.0.1:7070/api/v1/health)")
	if fs.Parse(args) != nil {
		return 2
	}
	results := verify.Run(verify.Env{NodeID: c.nodeID, ServeFlag: c.servePattern, NodeYAMLPath: c.nodeYAML,
		InventoryPath: c.inventory, HealthURL: healthURL})
	for _, r := range results {
		fmt.Fprintln(stdout, r.Format())
	}
	if verify.Failed(results) {
		return 1
	}
	return 0
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) < 12 {
		return "(not set)"
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func setupSummary(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	in := summary.Input{}
	var apiKeyFile, brain string
	fs := newFlagSet("summary", stderr)
	c.register(fs)
	fs.StringVar(&apiKeyFile, "api-key-file", "/etc/localai/api-key", "llama API key file (only a masked form is printed)")
	fs.StringVar(&brain, "brain", "chosen by the profile (loading in the background)", "brain model id")
	fs.StringVar(&in.Content, "content", "not started (run: aictl ark sync)", "content line")
	fs.StringVar(&in.Cloud, "cloud", "not enrolled (run: sudo aictl cloud enroll)", "cloud line")
	fs.StringVar(&in.User, "user", "", "desktop user who must log in again")
	fs.StringVar(&in.Machine, "machine", "", "tailnet machine name (default hostname -s)")
	fs.BoolVar(&in.Laptop, "laptop", false, "this computer is a laptop")
	fs.BoolVar(&in.RebootRequired, "reboot-required", false, "a reboot is needed")
	fs.BoolVar(&in.SkipTailscale, "skip-tailscale", false, "--no-tailscale was given")
	fs.BoolVar(&in.TailnetJoined, "tailnet-joined", false, "tailscale status shows Running")
	fs.BoolVar(&in.CloudPublishes, "cloud-publishes", false, "the cloud publishes the node (no manual checklist)")
	if fs.Parse(args) != nil {
		return 2
	}
	node, err := verify.LoadNodeYAML(c.nodeYAML)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup summary: %v\n", err)
		return 1
	}
	in.NodeID = c.nodeID
	if in.NodeID == "" {
		in.NodeID = node.Node.ID
	}
	if in.NodeID == "" {
		fmt.Fprintln(stderr, "aiagent setup summary: no node id (pass --node-id or write node.yaml first)")
		return 2
	}
	in.Profile, in.Backend, in.AgentVersion, in.Brain = orDefault(node.Profile, "unknown"), orDefault(node.Llama.Backend, "unknown"), app.Version, brain
	if in.Machine == "" {
		h, _ := os.Hostname()
		in.Machine, _, _ = strings.Cut(h, ".")
	}
	if key, err := os.ReadFile(apiKeyFile); err == nil {
		in.APIKeyMasked = maskKey(string(key))
	} else {
		in.APIKeyMasked = "(not set)"
	}
	in.Inventory = loadInventory(c.inventory, stderr)
	in.Pattern, _ = verify.ServePattern(in.NodeID, c.servePattern, node, in.Inventory)
	fmt.Fprint(stdout, summary.Render(in))
	return 0
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
