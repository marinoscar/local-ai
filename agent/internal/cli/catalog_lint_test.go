package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// writeCatalog writes a small valid catalog into <root>/catalog (placeholders only) and returns
// root. The repository's redaction check forbids a literal 64-hex string, hence strings.Repeat.
func writeCatalog(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sha := strings.Repeat("1", 64)
	files := map[string]string{
		"CATALOG_VERSION": "2026.11.02-1\n",
		"models.yaml": `schema_version: 1
models:
  - id: whisper-small
    display_name: "Whisper Small"
    summary: "Speech to text."
    engine: whisper
    roles: [stt]
    languages: [multi]
    license: mit
    survival_tier: 1
    source:
      type: huggingface
      repo: org-alpha/whisper-small-ggml
      revision: 0123456789abcdef0123456789abcdef01234567
    files:
      - {kind: whisper, name: ggml-small.bin, size_bytes: 487000000, sha256: "` + sha + `"}
    requirements: {backends: [cpu]}
`,
		"ark.yaml": `schema_version: 1
content:
  - id: docs-bundle
    kind: docs
    source: {type: repo_docs}
`,
		"profiles/cpu-only.yaml": `schema_version: 1
id: cpu-only
display_name: "Any machine"
match: {backends: [cpu]}
router: {models_max: 1, autoload: false}
storage: {target: internal, budget_bytes: 1000000000, min_free_bytes: 1000000}
include: {models: [whisper-small], content: [docs-bundle]}
default_model: whisper-small
survival: {models: [whisper-small], default_model: whisper-small}
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, "catalog", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func breakRevision(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, "catalog", "models.yaml")
	data, _ := os.ReadFile(p)
	out := strings.Replace(string(data), "0123456789abcdef0123456789abcdef01234567", "main", 1)
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = Main(args, &o, &e)
	return code, o.String(), e.String()
}

func TestCatalogLintValid(t *testing.T) {
	t.Chdir(writeCatalog(t))
	code, out, errOut := run("catalog", "lint", "catalog/")
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit %d stderr %q stdout %q", code, errOut, out)
	}
	if out != "✓ Catalog 2026.11.02-1 is valid (1 models, 1 content items, 1 profiles).\n" {
		t.Errorf("stdout %q", out)
	}
}

// Golden: one problem line is exactly <file>:<yaml-path>  LAI-CAT-0NN  <message>, then the summary
// (aictl.md 4.11.1, catalog.md 6).
func TestCatalogLintHumanGolden(t *testing.T) {
	root := writeCatalog(t)
	breakRevision(t, root)
	t.Chdir(root)
	code, out, errOut := run("catalog", "lint", "catalog/")
	const golden = `catalog/models.yaml:models[0].source.revision  LAI-CAT-011  revision must be a 40-hex commit, got "main"
✗ 1 problem found.
`
	if code != 2 || errOut != "" || out != golden {
		t.Fatalf("exit %d\nstderr %q\nstdout:\n%s\nwant:\n%s", code, errOut, out, golden)
	}
}

func TestCatalogLintHumanSeveralProblems(t *testing.T) {
	root := writeCatalog(t)
	breakRevision(t, root)
	if err := os.WriteFile(filepath.Join(root, "catalog", "CATALOG_VERSION"), []byte("nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	code, out, _ := run("catalog", "lint", "catalog")
	const golden = `catalog/CATALOG_VERSION:  LAI-CAT-019  CATALOG_VERSION must be one line YYYY.MM.DD-N (a real date, N >= 1), got "nonsense"
catalog/models.yaml:models[0].source.revision  LAI-CAT-011  revision must be a 40-hex commit, got "main"
✗ 2 problems found.
`
	if code != 2 || out != golden {
		t.Fatalf("exit %d stdout:\n%s", code, out)
	}
}

// The cloud depends on this shape (cloud-app.md 4.6): keys code, file, path, message; file is
// relative to the catalog directory; stderr stays empty.
func TestCatalogLintJSONInvalid(t *testing.T) {
	root := writeCatalog(t)
	breakRevision(t, root)
	t.Chdir(root)
	for _, args := range [][]string{
		{"catalog", "lint", "--json", "catalog"},
		{"catalog", "lint", "catalog", "--json"},
	} {
		code, out, errOut := run(args...)
		const golden = `{"valid":false,"problems":[{"code":"LAI-CAT-011","file":"models.yaml","path":"models[0].source.revision","message":"revision must be a 40-hex commit, got \"main\""}]}` + "\n"
		if code != 2 || errOut != "" || out != golden {
			t.Errorf("%v: exit %d stderr %q stdout %q", args, code, errOut, out)
		}
	}
}

func TestCatalogLintJSONValid(t *testing.T) {
	t.Chdir(writeCatalog(t))
	code, out, errOut := run("catalog", "lint", "--json", "catalog")
	if code != 0 || errOut != "" || out != `{"valid":true,"problems":[]}`+"\n" {
		t.Fatalf("exit %d stderr %q stdout %q", code, errOut, out)
	}
	var v struct {
		Valid    bool              `json:"valid"`
		Problems []json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || !v.Valid || v.Problems == nil {
		t.Errorf("not parseable as the cloud expects: %v %+v", err, v)
	}
}

func TestCatalogLintJSONMissingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errOut := run("catalog", "lint", "--json", "nope")
	if code != 2 || errOut != "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	var v struct {
		Valid    bool              `json:"valid"`
		Problems []catalog.Problem `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Valid || len(v.Problems) != 4 {
		t.Fatalf("%v %s", err, out)
	}
	for _, p := range v.Problems {
		if p.Code != "LAI-CAT-019" {
			t.Errorf("%+v", p)
		}
	}
}

func TestCatalogLintOnlineFlagReachesTheLinter(t *testing.T) {
	var got catalog.LintOptions
	var gotDir string
	old := lintCatalog
	lintCatalog = func(dir string, opts catalog.LintOptions) catalog.Report {
		gotDir, got = dir, opts
		return catalog.Report{}
	}
	t.Cleanup(func() { lintCatalog = old })

	if code, _, _ := run("catalog", "lint", "--online", "somewhere"); code != 0 || !got.Online || gotDir != "somewhere" {
		t.Errorf("online: code %d dir %q opts %+v", code, gotDir, got)
	}
	if code, _, _ := run("catalog", "lint", "somewhere"); code != 0 || got.Online {
		t.Errorf("offline by default: code %d opts %+v", code, got)
	}
}

func TestCatalogCommandUsage(t *testing.T) {
	cases := []struct {
		args       []string
		code       int
		out, errIn string
	}{
		{[]string{"catalog"}, 2, "", "Usage:"},
		{[]string{"catalog", "--help"}, 0, "Subcommands:", ""},
		{[]string{"catalog", "lint"}, 2, "", "a catalog directory is required"},
		{[]string{"catalog", "lint", "a", "b"}, 2, "", "unexpected argument"},
		{[]string{"catalog", "lint", "--bogus", "a"}, 2, "", `Unknown option -bogus`},
		{[]string{"catalog", "lint", "--help"}, 0, "--online", ""},
		{[]string{"catalog", "nope"}, 2, "", `Unknown subcommand "nope"`},
		{[]string{"catalog", "refresh", "extra"}, 2, "", "Unexpected argument"},
		// restore and use are implemented (AICTL-07, cmd_catalog_local.go); only usage mistakes are
		// tried here, because anything else would work on the real /etc/localai
		{[]string{"catalog", "restore", "extra"}, 2, "", "Unexpected argument"},
		{[]string{"catalog", "use", "--test"}, 2, "", "A catalog directory is required"},
		{[]string{"catalog", "use", "x"}, 2, "", "needs --test"},
	}
	for _, c := range cases {
		code, out, errOut := run(c.args...)
		if code != c.code || !strings.Contains(out, c.out) || !strings.Contains(errOut, c.errIn) {
			t.Errorf("%v: exit %d stdout %q stderr %q", c.args, code, out, errOut)
		}
	}
}
