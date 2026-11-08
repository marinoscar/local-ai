package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `aictl docs inventory import <file>` (issue #435): validates and installs the lab inventory, root
// 0600, and never prints a value from it.

// invSecret stands for a real lab value: it must never be printed.
const invSecret = "PlaceholderSecretAlpha"

const validInventory = "schema_version: 1\nlab:\n  owner_email: person-a@example.test\n  tailnet: " + invSecret + "\nnodes:\n  - id: ai-n1\n    serve_pattern: A\n"

func writeInv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "inventory.local.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDocsInventoryImport(t *testing.T) {
	applyLate(DefaultRegistry)
	f := newLocalFixture(t)
	src := writeInv(t, validInventory)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "docs", "inventory", "import", src)
	dst := filepath.Join(f.env.Paths.EtcDir, "inventory.local.yaml")
	if r.code != 0 || unwrap(r.out) != "✓ The lab inventory was installed in "+dst+" (only root can read it). The next sync fills the manuals with the real values.\n" {
		t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
	}
	if readFile(t, dst) != validInventory || fileMode(t, dst) != 0o600 {
		t.Errorf("installed copy wrong (mode %o)", fileMode(t, dst))
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "inventory.import" || lines[0]["result"] != "ok" {
		t.Errorf("audit %v", lines)
	}
	secretFree(t, invSecret, r.out, r.errOut, readFile(t, filepath.Join(f.env.Paths.StateDir, "audit.jsonl")))

	// The same file again: nothing written, nothing audited.
	again := runCLI(t, DefaultRegistry, localOpts(nil), "--json", "docs", "inventory", "import", src)
	if again.code != 0 || again.out != `{"changed":false,"path":"`+dst+`"}`+"\n" || len(f.auditLines()) != 1 {
		t.Errorf("again %+v", again)
	}
	// A wrong mode on the installed copy is repaired.
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, DefaultRegistry, localOpts(nil), "docs", "inventory", "import", src); r.code != 0 || fileMode(t, dst) != 0o600 {
		t.Errorf("mode not repaired: %+v", r)
	}
}

func TestDocsInventoryImportRefusals(t *testing.T) {
	applyLate(DefaultRegistry)
	cases := []struct {
		name string
		body string // "" with dir: a folder
		dir  bool
		lang string
		code int
		want string
	}{
		{"not YAML", "lab: [" + invSecret + "\n  : :", false, "en", ExitUsage, "is not a lab inventory: it is not valid YAML. Nothing was changed."},
		{"wrong schema_version", "schema_version: " + invSecret + "\nlab: {}\n", false, "en", ExitUsage, "it does not follow inventory.example.yaml"},
		{"bad serve_pattern", "schema_version: 1\nnodes:\n  - id: ai-n1\n    serve_pattern: " + invSecret + "\n", false, "es", ExitUsage, "no es un inventario del laboratorio: no sigue inventory.example.yaml"},
		{"empty", " \n", false, "en", ExitUsage, "it is empty"},
		{"a folder", "", true, "en", ExitFailed, "LAI-CLI-012"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLocalFixture(t)
			src := t.TempDir()
			if !tc.dir {
				src = writeInv(t, tc.body)
			}
			o := localOpts(nil)
			o.env["LANG"] = tc.lang + "_US.UTF-8"
			if tc.lang == "es" {
				o.env["LANG"] = "es_ES.UTF-8"
			}
			r := runCLI(t, DefaultRegistry, o, "docs", "inventory", "import", src)
			if r.code != tc.code || !strings.Contains(unwrap(r.errOut), tc.want) {
				t.Fatalf("exit %d\n%q\n%q", r.code, r.out, r.errOut)
			}
			secretFree(t, invSecret, r.out, r.errOut)
			if _, err := os.Stat(filepath.Join(f.env.Paths.EtcDir, "inventory.local.yaml")); err == nil {
				t.Error("an invalid inventory was installed")
			}
		})
	}
	t.Run("not root, missing file, no argument", func(t *testing.T) {
		f := newLocalFixture(t)
		o := localOpts(nil)
		o.euid = 1000
		if r := runCLI(t, DefaultRegistry, o, "docs", "inventory", "import", writeInv(t, validInventory)); r.code != ExitDenied {
			t.Errorf("not root: %+v", r)
		}
		f.untouched()
		if r := runCLI(t, DefaultRegistry, localOpts(nil), "docs", "inventory", "import", filepath.Join(t.TempDir(), "nope.yaml")); r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-CLI-012") {
			t.Errorf("missing: %+v", r)
		}
		if r := runCLI(t, DefaultRegistry, localOpts(nil), "docs", "inventory", "import"); r.code != ExitUsage {
			t.Errorf("no argument: %+v", r)
		}
		if lines := f.auditLines(); len(lines) != 1 || lines[0]["result"] != "fail" || lines[0]["code"] != "LAI-CLI-012" {
			t.Errorf("audit %v", lines)
		}
	})
}
