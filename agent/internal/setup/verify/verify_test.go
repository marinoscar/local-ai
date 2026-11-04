package verify

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
)

func inv(t *testing.T, pattern string) *inventory.Inventory {
	t.Helper()
	i, err := inventory.Parse([]byte("schema_version: 1\nnodes:\n  - {id: ai-n4, serve_pattern: " + pattern + "}\n"))
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func nodeWith(p string) NodeYAML {
	var n NodeYAML
	n.Tailscale.ServePattern = p
	return n
}

func TestServePattern(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		node     NodeYAML
		inv      *inventory.Inventory
		want     string
		wantCode bool
	}{
		{"flag only", "A", NodeYAML{}, nil, "A", false},
		{"node.yaml only", "", nodeWith("B"), nil, "B", false},
		{"node.yaml none is nothing", "", nodeWith("none"), nil, "", false},
		{"inventory only", "", NodeYAML{}, inv(t, "B"), "B", false},
		{"flag wins over node.yaml without inventory", "A", nodeWith("B"), nil, "A", false},
		{"flag matches inventory", "b", NodeYAML{}, inv(t, "B"), "B", false},
		{"flag contradicts inventory", "A", NodeYAML{}, inv(t, "B"), "A", true},
		{"node.yaml contradicts inventory", "", nodeWith("A"), inv(t, "B"), "A", true},
		{"inventory without this node", "A", NodeYAML{}, func() *inventory.Inventory {
			i, _ := inventory.Parse([]byte("schema_version: 1\nnodes: [{id: ai-n9, serve_pattern: B}]\n"))
			return i
		}(), "A", false},
		{"nothing known", "", NodeYAML{}, nil, "", false},
	}
	for _, tc := range tests {
		got, bad := ServePattern("ai-n4", tc.flag, tc.node, tc.inv)
		if got != tc.want {
			t.Errorf("%s: pattern %q, want %q", tc.name, got, tc.want)
		}
		if (bad != nil) != tc.wantCode {
			t.Errorf("%s: result %+v, want code=%v", tc.name, bad, tc.wantCode)
		}
		if bad != nil && (bad.Code != "LAI-INS-050" || bad.Severity != Error || bad.Fix == "") {
			t.Errorf("%s: wrong result %+v", tc.name, bad)
		}
	}
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer healthy.Close()
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer sick.Close()

	dir := t.TempDir()
	nodeB := write(t, dir, "nodeB.yaml", "node: {id: ai-n4}\ntailscale: {serve_pattern: B}\n")
	invA := write(t, dir, "invA.yaml", "schema_version: 1\nnodes: [{id: ai-n4, serve_pattern: A}]\n")
	pin := write(t, dir, "pin", "x")
	reboot := write(t, dir, "reboot-required", "")
	none := filepath.Join(dir, "none")

	tests := []struct {
		name      string
		env       Env
		wantCodes []string
		failed    bool
	}{
		{"all good", Env{HealthURL: healthy.URL, NodeYAMLPath: nodeB, InventoryPath: none, PINPath: pin, RebootRequiredPath: none}, nil, false},
		{"agent answers 503", Env{HealthURL: sick.URL, NodeYAMLPath: nodeB, InventoryPath: none, PINPath: pin, RebootRequiredPath: none}, []string{"LAI-INS-034"}, true},
		{"agent down", Env{HealthURL: "http://127.0.0.1:1/x", NodeYAMLPath: nodeB, InventoryPath: none, PINPath: pin, RebootRequiredPath: none}, []string{"LAI-INS-034"}, true},
		{"pattern mismatch", Env{HealthURL: healthy.URL, NodeYAMLPath: nodeB, InventoryPath: invA, PINPath: pin, RebootRequiredPath: none}, []string{"LAI-INS-050"}, true},
		{"no pin is a warning", Env{HealthURL: healthy.URL, NodeYAMLPath: nodeB, InventoryPath: none, PINPath: none, RebootRequiredPath: none}, []string{"LAI-INS-060"}, false},
		{"reboot is info", Env{HealthURL: healthy.URL, NodeYAMLPath: nodeB, InventoryPath: none, PINPath: pin, RebootRequiredPath: reboot}, []string{"LAI-INS-070"}, false},
		{"flag contradicts inventory", Env{HealthURL: healthy.URL, NodeYAMLPath: none, InventoryPath: invA, NodeID: "ai-n4", ServeFlag: "B", PINPath: pin, RebootRequiredPath: none}, []string{"LAI-INS-050"}, true},
	}
	for _, tc := range tests {
		rs := Run(tc.env)
		var codes []string
		for _, r := range rs {
			if r.Code != "" {
				codes = append(codes, r.Code)
			}
		}
		if strings.Join(codes, ",") != strings.Join(tc.wantCodes, ",") {
			t.Errorf("%s: codes %v, want %v", tc.name, codes, tc.wantCodes)
		}
		if Failed(rs) != tc.failed {
			t.Errorf("%s: Failed = %v, want %v", tc.name, Failed(rs), tc.failed)
		}
	}
}

func TestFormat(t *testing.T) {
	r := codeResult("pin", "LAI-INS-060", Warning, "")
	if got := r.Format(); !strings.Contains(got, "LAI-INS-060") || !strings.Contains(got, "Fix: Run `sudo aictl pin set`.") {
		t.Errorf("format = %q", got)
	}
	if got := (Result{Check: "x", Severity: OK, Message: "fine"}).Format(); !strings.HasPrefix(got, "  ok") {
		t.Errorf("format = %q", got)
	}
}
