// Package inventory reads the lab inventory file (inventory.local.yaml, schema in inventory.example.yaml)
// for the installer's summary and serve-pattern check (docs/specs/installer.md section 4.3 P11 and P15).
// The file holds real lab values: they are shown on the local terminal and written to the local log only,
// never sent to the cloud (installer.md section 10 INST-07, implementation note 2).
package inventory

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where --keep-inventory copies the file (overview.md section 3.2).
const DefaultPath = "/etc/localai/inventory.local.yaml"

// Lab is the lab block.
type Lab struct {
	OwnerEmail    string `yaml:"owner_email"`
	Tailnet       string `yaml:"tailnet"`
	Route53ZoneID string `yaml:"route53_zone_id"`
}

// VPS is the vps block.
type VPS struct {
	PublicIP string `yaml:"public_ip"`
}

// Node is one entry of nodes[].
type Node struct {
	ID           string `yaml:"id"`
	PublicHost   string `yaml:"public_host"`
	Machine      string `yaml:"machine"`
	TailnetIP    string `yaml:"tailnet_ip"`
	ServePattern string `yaml:"serve_pattern"`
}

// Inventory is the parts of the file the installer reads.
type Inventory struct {
	SchemaVersion int    `yaml:"schema_version"`
	Lab           Lab    `yaml:"lab"`
	VPS           VPS    `yaml:"vps"`
	Nodes         []Node `yaml:"nodes"`
}

// ErrNotFound is returned by Load for a missing file: callers treat it as "no inventory".
var ErrNotFound = errors.New("inventory: file not found")

// Parse reads inventory YAML. Unknown fields are ignored (the file also carries ark_drives and more).
func Parse(data []byte) (*Inventory, error) {
	var inv Inventory
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("inventory: not valid YAML: %w", err)
	}
	if inv.SchemaVersion != 1 {
		return nil, fmt.Errorf("inventory: schema_version %d is not supported (want 1)", inv.SchemaVersion)
	}
	for i := range inv.Nodes {
		p := strings.ToUpper(strings.TrimSpace(inv.Nodes[i].ServePattern))
		if p != "" && p != "A" && p != "B" {
			return nil, fmt.Errorf("inventory: node %s has serve_pattern %q (want A or B)", inv.Nodes[i].ID, inv.Nodes[i].ServePattern)
		}
		inv.Nodes[i].ServePattern = p
	}
	return &inv, nil
}

// Load reads and parses a file; a missing file is ErrNotFound.
func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return Parse(data)
}

// Node returns the entry for a node id, or nil.
func (inv *Inventory) Node(id string) *Node {
	if inv == nil {
		return nil
	}
	for i := range inv.Nodes {
		if inv.Nodes[i].ID == id {
			return &inv.Nodes[i]
		}
	}
	return nil
}
