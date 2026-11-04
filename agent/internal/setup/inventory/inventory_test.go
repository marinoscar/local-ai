package inventory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = `schema_version: 1
lab: {owner_email: "admin@example.test", tailnet: "tailtest", route53_zone_id: "ZTEST123"}
vps: {public_ip: "192.0.2.10"}
nodes:
  - {id: ai-n4, machine: test-box, tailnet_ip: "198.51.100.4", serve_pattern: b}
  - {id: ai-n1, serve_pattern: A}
ark_drives: [{label: AI-ARK}]
`

func TestParse(t *testing.T) {
	inv, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Lab.Tailnet != "tailtest" || inv.VPS.PublicIP != "192.0.2.10" {
		t.Errorf("lab/vps wrong: %+v %+v", inv.Lab, inv.VPS)
	}
	n := inv.Node("ai-n4")
	if n == nil || n.ServePattern != "B" || n.TailnetIP != "198.51.100.4" {
		t.Errorf("node wrong: %+v", n)
	}
	if inv.Node("ai-n9") != nil {
		t.Error("unknown node must be nil")
	}
	var nilInv *Inventory
	if nilInv.Node("ai-n4") != nil {
		t.Error("nil inventory must have no node")
	}
}

func TestParseErrors(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"not yaml":    {"a: [", "not valid YAML"},
		"no schema":   {"lab: {}\n", "schema_version"},
		"bad pattern": {"schema_version: 1\nnodes: [{id: ai-n4, serve_pattern: C}]\n", "serve_pattern"},
	} {
		if _, err := Parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "none.yaml")); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
