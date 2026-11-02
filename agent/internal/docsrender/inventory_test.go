package docsrender

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixturePairs(t *testing.T) *Pairs {
	t.Helper()
	p, err := PairInventory(readFixture(t, "inventory.example.yaml"), readFixture(t, "inventory.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPairInventoryByPath(t *testing.T) {
	p := fixturePairs(t)
	byName := map[string][]Pair{}
	for _, pr := range p.Pairs {
		byName[pr.Name] = append(byName[pr.Name], pr)
	}
	one := func(name string) Pair {
		t.Helper()
		if len(byName[name]) != 1 {
			t.Fatalf("%s: %d pairs", name, len(byName[name]))
		}
		return byName[name][0]
	}
	// Lists are matched by their id: the fixture lists ai-n3 first, the example ai-n1 first.
	if pr := one("TAILNET_IP_AI_N1"); pr.Path != "nodes[id=ai-n1].tailnet_ip" || pr.Value != "192.0.2.21" || !pr.Has || pr.NodeID != "ai-n1" {
		t.Errorf("TAILNET_IP_AI_N1 = %+v", pr)
	}
	if pr := one("TAILNET_IP_AI_N3"); pr.Value != "192.0.2.23" {
		t.Errorf("TAILNET_IP_AI_N3 = %+v", pr)
	}
	// ark_drives entries have a label, not an id.
	if pr := one("ARK_DRIVE_UUID"); pr.Path != "ark_drives[label=AI-ARK].fs_uuid" || pr.Value != "fixture-uuid-1" {
		t.Errorf("ARK_DRIVE_UUID = %+v", pr)
	}
	if pr := one("ADMIN_EMAIL"); pr.Path != "lab.owner_email" || pr.Value != "person-a@example.test" {
		t.Errorf("ADMIN_EMAIL = %+v", pr)
	}
	// A placeholder the local file has no value for stays unresolved.
	if pr := one("TAILNET_IP_VPS"); !pr.Has {
		t.Errorf("TAILNET_IP_VPS should have a value: %+v", pr)
	}
	if _, ok := p.Resolve("NO_SUCH_NAME", ""); ok {
		t.Error("an unknown placeholder resolved")
	}
	if got := p.SecretFields; !reflect.DeepEqual(got, map[string]bool{"owner_email": true, "tailnet": true, "public_ip": true,
		"tailnet_ip": true, "lan_ip": true, "fs_uuid": true, "hostname": true, "token_label": true}) {
		t.Errorf("secret_fields = %v", got)
	}
}

func TestResolvePerNode(t *testing.T) {
	p := fixturePairs(t)
	cases := []struct {
		name, node, want string
		ok               bool
	}{
		{"DESKTOP_USER", "ai-n1", "avery", true},
		{"DESKTOP_USER", "ai-n3", "casey", true},
		{"DESKTOP_USER", "", "", false},      // the nodes disagree and none was named
		{"DESKTOP_USER", "ai-n9", "", false}, // a node the inventory does not know
		{"TAILNET", "", "example-tailnet", true},
		{"TAILNET_IP_AI_N1", "ai-n3", "192.0.2.21", true}, // a node-specific name needs no node
	}
	for _, c := range cases {
		got, ok := p.Resolve(c.name, c.node)
		if got != c.want || ok != c.ok {
			t.Errorf("Resolve(%s, %q) = %q, %v; want %q, %v", c.name, c.node, got, ok, c.want, c.ok)
		}
	}
	if v := p.Values("ai-n3"); v["DESKTOP_USER"] != "casey" || v["TAILNET"] != "example-tailnet" {
		t.Errorf("Values = %v", v)
	}
}

func TestSecretMarkers(t *testing.T) {
	p := fixturePairs(t)
	// By the secret_fields list of the example (token_label is on no built-in list).
	for _, n := range []string{"NODE_TOKEN_LABEL", "ARK_DRIVE_UUID", "TAILNET_IP_AI_N3", "ADMIN_EMAIL", "VPS_PUBLIC_IP"} {
		if !p.IsSecret(n) {
			t.Errorf("%s must be secret", n)
		}
	}
	// A field outside the list is not secret.
	for _, n := range []string{"DESKTOP_USER", "NODE_LABEL"} {
		if p.IsSecret(n) {
			t.Errorf("%s must not be secret", n)
		}
	}
	// The built-in list needs no inventory at all, and always wins.
	var none *Pairs
	if !none.IsSecret("TAILNET_IP_AI_N2") || none.IsSecret("NODE_ID") {
		t.Error("built-in list not applied without an inventory")
	}
	noSecrets, err := PairInventory([]byte("a:\n  tailnet_ip: \"<TAILNET_IP_AI_N1>\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !noSecrets.IsSecret("TAILNET_IP_AI_N1") {
		t.Error("the built-in list must win over an empty secret_fields")
	}
}

func TestPairInventoryWithoutLocal(t *testing.T) {
	p, err := PairInventory(readFixture(t, "inventory.example.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasLocal || len(p.Values("ai-n1")) != 0 {
		t.Error("no local inventory must give no values")
	}
	if !p.IsSecret("NODE_TOKEN_LABEL") {
		t.Error("the secret markers must be known without a local inventory")
	}
}

func TestPairInventoryErrors(t *testing.T) {
	good := readFixture(t, "inventory.example.yaml")
	cases := []struct {
		name           string
		example, local []byte
	}{
		{"example is not YAML", []byte("a: [unclosed"), nil},
		{"example is a list", []byte("- a\n- b\n"), nil},
		{"local is not YAML", good, []byte("nodes: [unclosed")},
	}
	for _, c := range cases {
		if _, err := PairInventory(c.example, c.local); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestPairInventoryIgnoresPlaceholderLocalValues(t *testing.T) {
	// A local file that still holds the placeholder (copied but not filled in) is "no value".
	p, err := PairInventory([]byte("lab:\n  tailnet: \"<TAILNET>\"\n"), []byte("lab:\n  tailnet: \"<TAILNET>\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Resolve("TAILNET", ""); ok {
		t.Error("an unfilled local value must not resolve")
	}
}

func TestFindInventoryOrder(t *testing.T) {
	root := t.TempDir()
	etc, drive, cwd := filepath.Join(root, "etc"), filepath.Join(root, "drive"), filepath.Join(root, "cwd")
	for _, d := range []string{etc, drive, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put := func(dir string) string {
		p := filepath.Join(dir, InventoryFile)
		if err := os.WriteFile(p, []byte("schema_version: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	l := InventoryLookup{EtcDir: etc, DriveRoots: []string{drive}, Cwd: cwd}
	if got := FindInventory(l); got != "" {
		t.Errorf("nothing exists, got %q", got)
	}
	cwdFile := put(cwd)
	if got := FindInventory(l); got != cwdFile {
		t.Errorf("only ./ has one: got %q", got)
	}
	driveFile := put(drive)
	if got := FindInventory(l); got != driveFile {
		t.Errorf("the drive beats ./: got %q", got)
	}
	etcFile := put(etc)
	if got := FindInventory(l); got != etcFile {
		t.Errorf("/etc/localai beats the drive: got %q", got)
	}
	// An explicit --inventory is used as is; if it is missing nothing else is tried.
	other := filepath.Join(root, "other.yaml")
	if got := FindInventory(InventoryLookup{Explicit: other, EtcDir: etc}); got != "" {
		t.Errorf("a missing explicit file fell back to %q", got)
	}
	if err := os.WriteFile(other, []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindInventory(InventoryLookup{Explicit: other, EtcDir: etc}); got != other {
		t.Errorf("explicit = %q", got)
	}
	// A folder is not an inventory.
	if err := os.Mkdir(filepath.Join(root, "etc2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "etc2", InventoryFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindInventory(InventoryLookup{EtcDir: filepath.Join(root, "etc2")}); got != "" {
		t.Errorf("a folder was taken as the inventory: %q", got)
	}
}

func TestNodeIDByMachine(t *testing.T) {
	inv := []byte("nodes:\n  - id: ai-n1\n    machine: box-one\n  - id: ai-n3\n    machine: Box-Three\n")
	for host, want := range map[string]string{"box-one": "ai-n1", "box-three": "ai-n3", "other": "", "": ""} {
		if got := NodeIDByMachine(inv, host); got != want {
			t.Errorf("NodeIDByMachine(%q) = %q, want %q", host, got, want)
		}
	}
	if NodeIDByMachine([]byte("nodes: [unclosed"), "x") != "" {
		t.Error("bad YAML must give no node")
	}
}
