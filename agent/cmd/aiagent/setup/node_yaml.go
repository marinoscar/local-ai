package setup

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func init() { register("node-yaml", nodeYAML) }

var (
	nodeIDRe  = regexp.MustCompile(`^ai-n[1-9][0-9]{0,3}$`)
	backendRe = regexp.MustCompile(`^(cuda|vulkan|cpu)$`)
)

// inventoryNode is the part of an inventory.local.yaml node entry the installer reads.
type inventoryNode struct {
	ID           string `yaml:"id"`
	PublicHost   string `yaml:"public_host"`
	Backend      string `yaml:"backend"`
	Profile      string `yaml:"profile"`
	ServePattern string `yaml:"serve_pattern"`
}

// nodeYAML is `aiagent setup node-yaml` (installer.md section 3.5, P9 step 1): write /etc/localai/node.yaml
// when it is missing; on a re-run fill only the keys that are missing and never change node.id.
func nodeYAML(_ context.Context, e *Env, args []string) int {
	fs := flag.NewFlagSet("node-yaml", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var nodeID, profile, backend, pattern, inventory string
	var dry bool
	fs.StringVar(&nodeID, "node-id", "", "")
	fs.StringVar(&profile, "profile", "", "")
	fs.StringVar(&backend, "backend", "", "")
	fs.StringVar(&pattern, "serve-pattern", "", "")
	fs.StringVar(&inventory, "inventory", "", "")
	fs.BoolVar(&dry, "dry-run", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(e.Stderr, "usage: aiagent setup node-yaml --node-id <ai-nN> [--profile <id>] [--backend cuda|vulkan|cpu] [--serve-pattern A|B|none] [--inventory <file>] [--dry-run]")
		return ExitUsage
	}
	if !needRoot(e, dry, "node-yaml") {
		return ExitNotRoot
	}
	if nodeID != "" && !nodeIDRe.MatchString(nodeID) {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: --node-id %q must look like ai-n4 (ai-n and a number from 1 to 9999)\n", nodeID)
		return ExitUsage
	}
	if backend != "" && !backendRe.MatchString(backend) {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: --backend %q must be cuda, vulkan or cpu\n", backend)
		return ExitUsage
	}
	if pattern != "" && pattern != "A" && pattern != "B" && pattern != "none" {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: --serve-pattern %q must be A, B or none\n", pattern)
		return ExitUsage
	}

	path := config.NodeYAMLPath(e.Root)
	cur, present, err := currentNodeID(path)
	if err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: cannot read %s: %v\n", path, err)
		return ExitFailed
	}
	if cur != "" && nodeID != "" && cur != nodeID {
		fmt.Fprintf(e.Stderr, "ERROR %s: this computer already has the node id %s, and you asked for %s. Keep the old id, or uninstall with --purge-config first.\n", doctor.INS020, cur, nodeID)
		return ExitFailed
	}
	if cur == "" && nodeID == "" {
		fmt.Fprintln(e.Stderr, "aiagent setup node-yaml: --node-id is required because this computer has no node id yet")
		return ExitUsage
	}
	if nodeID == "" {
		nodeID = cur
	}

	var inv inventoryNode
	if inventory != "" {
		n, err := readInventoryNode(inventory, nodeID)
		if err != nil {
			fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: %v\n", err)
			return ExitFailed
		}
		inv = n
	}
	profile, backend, pattern = firstNonEmpty(profile, inv.Profile), firstNonEmpty(backend, inv.Backend), firstNonEmpty(pattern, inv.ServePattern)

	var added []string
	edit := func(root *yaml.Node) error {
		added = added[:0]
		set := func(v any, p ...string) error {
			if config.LookupKey(root, p...) != nil {
				return nil
			}
			added = append(added, strings.Join(p, "."))
			return config.SetKey(root, v, p...)
		}
		type kv struct {
			v any
			p []string
		}
		want := []kv{{config.SchemaVersion, []string{"schema_version"}}, {nodeID, []string{"node", "id"}}}
		if inv.PublicHost != "" {
			want = append(want, kv{inv.PublicHost, []string{"node", "public_host"}})
		}
		if profile != "" {
			want = append(want, kv{profile, []string{"profile"}})
		}
		if backend != "" {
			want = append(want, kv{backend, []string{"llama", "backend"}})
		}
		if pattern != "" {
			want = append(want, kv{pattern, []string{"tailscale", "serve_pattern"}})
		}
		for _, w := range want {
			if err := set(w.v, w.p...); err != nil {
				return err
			}
		}
		return nil
	}

	if dry {
		verb := "create"
		if present {
			verb = "fill missing keys in"
		}
		e.say("[dry-run] would %s %s (mode 0640, owner root:localai)", verb, path)
		probe := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if data, rerr := os.ReadFile(path); rerr == nil {
			var doc yaml.Node
			if yaml.Unmarshal(data, &doc) == nil && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
				probe = doc.Content[0]
			}
		}
		_ = edit(probe)
		for _, k := range added {
			e.say("[dry-run]   + %s", k)
		}
		return ExitOK
	}

	if err := os.MkdirAll(strings.TrimSuffix(path, "/node.yaml"), 0o755); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: cannot create the folder of %s: %v\n", path, err)
		return ExitFailed
	}
	store := config.Open(config.Options{Root: e.Root})
	if err := store.Update(edit); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: node.yaml was not changed: %v\n", err)
		return ExitFailed
	}
	if gid, ok := e.groupID("localai"); ok {
		if err := os.Chown(path, 0, gid); err != nil && os.Geteuid() == 0 {
			fmt.Fprintf(e.Stderr, "aiagent setup node-yaml: cannot set the group of %s: %v\n", path, err)
			return ExitFailed
		}
	}
	if len(added) == 0 {
		e.say("node.yaml already complete: %s", path)
	} else {
		e.say("node.yaml %s: added %s", path, strings.Join(added, ", "))
	}
	return ExitOK
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// currentNodeID returns node.id of an existing node.yaml ("" when absent or unset).
func currentNodeID(path string) (id string, present bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var doc struct {
		Node struct {
			ID string `yaml:"id"`
		} `yaml:"node"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", true, fmt.Errorf("it is not valid YAML: %w", err)
	}
	return doc.Node.ID, true, nil
}

// readInventoryNode returns the inventory entry for id; a node that is not listed gives an empty entry.
func readInventoryNode(path, id string) (inventoryNode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return inventoryNode{}, fmt.Errorf("cannot read the inventory %s: %w", path, err)
	}
	var inv struct {
		Nodes []inventoryNode `yaml:"nodes"`
	}
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return inventoryNode{}, fmt.Errorf("the inventory %s is not valid YAML: %w", path, err)
	}
	for _, n := range inv.Nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return inventoryNode{}, nil
}
