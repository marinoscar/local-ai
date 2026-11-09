package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// node is a temp directory laid out like a node: models, ZIMs, the generated files.
type node struct {
	t                                     *testing.T
	dir                                   string
	model, zim1, zim2, ini, lib, env, whs string
}

func newNode(t *testing.T) *node {
	t.Helper()
	d := t.TempDir()
	n := &node{t: t, dir: d}
	n.model = n.file("models/model-alpha-9b/model.gguf", "gguf")
	n.zim1 = n.file("kiwix/wiki-alpha.zim", "zim")
	n.zim2 = n.file("kiwix/medicine-beta.zim", "zim")
	n.whs = n.file("speech/whisper-small/ggml-small.bin", "bin")
	n.ini = filepath.Join(d, "etc", "models.ini")
	n.lib = filepath.Join(d, "var", "kiwix", "library.xml")
	n.env = filepath.Join(d, "etc", "whisper.env")
	n.copy("models.ini.good", n.ini)
	n.copy("library.xml.tmpl", n.lib)
	n.copy("whisper.env.tmpl", n.env)
	return n
}

func (n *node) file(rel, content string) string {
	n.t.Helper()
	p := filepath.Join(n.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		n.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		n.t.Fatal(err)
	}
	return p
}

// copy writes a testdata template to dst with the @PLACEHOLDERS@ filled in.
func (n *node) copy(name, dst string) {
	n.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		n.t.Fatal(err)
	}
	model := n.model
	if name == "whisper.env.tmpl" {
		model = n.whs
	}
	s := strings.NewReplacer("@MODEL@", model, "@ZIM1@", n.zim1, "@ZIM2@", n.zim2).Replace(string(b))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		n.t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(s), 0o644); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) lint(bundle string) OfflineLint {
	o := OfflineLint{ModelsINI: n.ini, LibraryXML: n.lib, WhisperEnv: n.env}
	if bundle != "" {
		o.Bundle = os.DirFS(filepath.Join("testdata", bundle))
	}
	return o
}

func TestOfflineLintPassesOnALocalNode(t *testing.T) {
	n := newNode(t)
	out := run(t, n.lint("dist-clean").Check())
	if out.Result != drill.ResultPass || out.Code != "" || out.StopRun {
		t.Fatalf("outcome = %+v", out)
	}
	d := out.Details
	if d["remote_count"] != 0 || d["models_ini"] != "ok" || d["library_xml"] != "ok" || d["whisper_env"] != "ok" || d["portal_bundle"] != "ok" {
		t.Errorf("details = %v", d)
	}
	if d["books"] != 2 || d["bundle_files"] != 2 {
		t.Errorf("counts = %v", d)
	}
	if _, has := d["remote"]; has {
		t.Errorf("a pass must not carry remote: %v", d)
	}
}

func TestOfflineLintDetailsAreFlat(t *testing.T) {
	n := newNode(t)
	n.copy("models.ini.hf-repo", n.ini)
	for _, o := range []drill.Outcome{run(t, n.lint("dist-clean").Check()), run(t, n.lint("dist-bad").Check())} {
		for k, v := range o.Details {
			switch v.(type) {
			case string, int, bool, nil:
			default:
				t.Errorf("detail %s is a %T: details must be flat", k, v)
			}
		}
		if s, _ := o.Details["remote"].(string); len(s) > 500 {
			t.Errorf("remote is %d characters", len(s))
		}
	}
}

func TestOfflineLintModelsINI(t *testing.T) {
	cases := []struct {
		name, file string
		wantIn     string // what the details must name
		fail       bool
	}{
		{"hf-repo", "models.ini.hf-repo", "models.ini line 8 [model-alpha-9b-q4] hf-repo", true},
		{"model-url", "models.ini.model-url", "model-url", true},
		{"a web address as a value", "models.ini.http-value", "chat-template-file (a web address)", true},
		{"local only", "models.ini.good", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := newNode(t)
			n.copy(c.file, n.ini)
			out := run(t, n.lint("dist-clean").Check())
			if !c.fail {
				if out.Result != drill.ResultPass {
					t.Fatalf("outcome = %+v", out)
				}
				return
			}
			if out.Result != drill.ResultFail || out.Code != doctor.DRL010 {
				t.Fatalf("outcome = %+v, want fail LAI-DRL-010", out)
			}
			if s, _ := out.Details["remote"].(string); !strings.Contains(s, c.wantIn) {
				t.Errorf("remote = %q, want it to name %q", s, c.wantIn)
			}
			if out.Details["models_ini"] != "1 finding(s)" || out.Details["library_xml"] != "ok" {
				t.Errorf("details = %v", out.Details)
			}
		})
	}
}

func TestOfflineLintModelsINIKeyRules(t *testing.T) {
	remote := []string{"hf-repo", "hf-repo-draft", "hf-file", "hf-token", "hf", "hfd", "hff", "hft", "model-url", "mmproj-url", "docker-repo", "dr", "--hf-repo", " HF-REPO ", "mu"}
	local := []string{"model", "c", "n-gpu-layers", "mmproj", "model-draft", "chat-template-file", "tags", "alias", "load-on-startup", "stop-timeout", "flash-attn"}
	for _, k := range remote {
		if !remoteKey(k) {
			t.Errorf("%q must be flagged", k)
		}
	}
	for _, k := range local {
		if remoteKey(k) {
			t.Errorf("%q must not be flagged", k)
		}
	}
}

func TestOfflineLintINIDoesNotLeakValues(t *testing.T) {
	n := newNode(t)
	secretish := "hf-token = word-" + strings.Repeat("a", 12)
	n.file("etc/extra.ini", "")
	if err := os.WriteFile(n.ini, []byte("[m]\n"+secretish+"\nmodel-url = https://models.example.test/private-path.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, n.lint("").Check())
	s, _ := out.Details["remote"].(string)
	if out.Result != drill.ResultFail || strings.Contains(s, "word-aaaa") || strings.Contains(s, "private-path") || strings.Contains(s, "example.test") {
		t.Fatalf("a value reached the details: %q", s)
	}
	if !strings.Contains(s, "hf-token") || !strings.Contains(s, "model-url") {
		t.Errorf("the settings are not named: %q", s)
	}
}

func TestOfflineLintLibrary(t *testing.T) {
	t.Run("a ZIM that does not exist", func(t *testing.T) {
		n := newNode(t)
		if err := os.Remove(n.zim2); err != nil {
			t.Fatal(err)
		}
		out := run(t, n.lint("dist-clean").Check())
		if out.Result != drill.ResultFail || out.Code != doctor.DRL010 {
			t.Fatalf("outcome = %+v", out)
		}
		s, _ := out.Details["remote"].(string)
		if !strings.Contains(s, "library.xml book "+n.zim2+" (does not exist)") {
			t.Errorf("remote = %q", s)
		}
		if out.Details["library_xml"] != "1 finding(s)" || out.Details["books"] != 2 {
			t.Errorf("details = %v", out.Details)
		}
	})
	t.Run("a ZIM that is a directory", func(t *testing.T) {
		n := newNode(t)
		_ = os.Remove(n.zim1)
		if err := os.Mkdir(n.zim1, 0o755); err != nil {
			t.Fatal(err)
		}
		out := run(t, n.lint("").Check())
		if out.Result != drill.ResultFail || !strings.Contains(out.Details["remote"].(string), "(not a file)") {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("a web address as a path", func(t *testing.T) {
		n := newNode(t)
		n.file("var/kiwix/library.xml", `<library><book id="1" path="https://download.example.test/wiki.zim"/></library>`)
		out := run(t, n.lint("").Check())
		if out.Result != drill.ResultFail || !strings.Contains(out.Details["remote"].(string), "(a web address)") {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("a relative path is relative to the library", func(t *testing.T) {
		n := newNode(t)
		n.file("var/kiwix/rel.zim", "zim")
		n.file("var/kiwix/library.xml", `<library><book id="1" path="rel.zim"/><book id="2" path="gone.zim"/></library>`)
		out := run(t, n.lint("").Check())
		s, _ := out.Details["remote"].(string)
		if out.Result != drill.ResultFail || !strings.Contains(s, "gone.zim") || strings.Contains(s, "rel.zim") {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("an empty library is fine", func(t *testing.T) {
		n := newNode(t)
		n.file("var/kiwix/library.xml", `<?xml version="1.0"?><library version="20110515"></library>`)
		if out := run(t, n.lint("").Check()); out.Result != drill.ResultPass || out.Details["books"] != 0 {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("a broken library is reported, not guessed at", func(t *testing.T) {
		n := newNode(t)
		n.file("var/kiwix/library.xml", `<library><book path="`)
		out := run(t, n.lint("").Check())
		if out.Result != drill.ResultPass || out.Details["library_xml"] != "unparseable" {
			t.Fatalf("outcome = %+v", out)
		}
	})
}

func TestOfflineLintWhisperEnv(t *testing.T) {
	t.Run("model missing", func(t *testing.T) {
		n := newNode(t)
		if err := os.Remove(n.whs); err != nil {
			t.Fatal(err)
		}
		out := run(t, n.lint("").Check())
		s, _ := out.Details["remote"].(string)
		if out.Result != drill.ResultFail || out.Code != doctor.DRL010 || !strings.Contains(s, "whisper.env WHISPER_MODEL "+n.whs+" (does not exist)") {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("model is a web address", func(t *testing.T) {
		n := newNode(t)
		n.file("etc/whisper.env", "export WHISPER_MODEL=\"https://models.example.test/ggml-small.bin\"\n")
		out := run(t, n.lint("").Check())
		if out.Result != drill.ResultFail || !strings.Contains(out.Details["remote"].(string), "whisper.env WHISPER_MODEL (a web address)") {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("quoted path that exists", func(t *testing.T) {
		n := newNode(t)
		n.file("etc/whisper.env", "WHISPER_MODEL='"+n.whs+"'\n")
		if out := run(t, n.lint("").Check()); out.Result != drill.ResultPass {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("no model line", func(t *testing.T) {
		n := newNode(t)
		n.file("etc/whisper.env", "WHISPER_THREADS=4\n")
		out := run(t, n.lint("").Check())
		if out.Result != drill.ResultPass || out.Details["whisper_env"] != "no_model" {
			t.Fatalf("outcome = %+v", out)
		}
	})
}

func TestOfflineLintBundle(t *testing.T) {
	n := newNode(t)
	out := run(t, n.lint("dist-bad").Check())
	s, _ := out.Details["remote"].(string)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL010 {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.Contains(s, "portal bundle assets/app.js:1: https://unpkg.com/maplibre-gl@4/dist/maplibre-gl.js") ||
		!strings.Contains(s, `portal bundle assets/app.js:1: forbidden name "unpkg"`) {
		t.Errorf("remote = %q", s)
	}
	if out.Details["portal_bundle"] != "2 finding(s)" || out.Details["models_ini"] != "ok" {
		t.Errorf("details = %v", out.Details)
	}
	// The injected-fixture case of the acceptance list, built in memory.
	injected := fstest.MapFS{
		"portal.html":   {Data: []byte("<!doctype html>")},
		"assets/app.js": {Data: []byte(`import "https://unpkg.com/x.js";`)},
	}
	o := OfflineLint{Bundle: injected}
	if out := run(t, o.Check()); out.Result != drill.ResultFail || out.Code != doctor.DRL010 {
		t.Fatalf("injected unpkg: %+v", out)
	}
	delete(injected, "assets/app.js")
	if out := run(t, o.Check()); out.Result != drill.ResultPass {
		t.Fatalf("clean bundle: %+v", out)
	}
}

func TestOfflineLintAbsentAndUnconfiguredTargets(t *testing.T) {
	// A node with nothing generated yet: nothing points anywhere.
	empty := OfflineLint{
		ModelsINI: filepath.Join(t.TempDir(), "models.ini"), LibraryXML: filepath.Join(t.TempDir(), "library.xml"),
		WhisperEnv: filepath.Join(t.TempDir(), "whisper.env"),
	}
	out := run(t, empty.Check())
	if out.Result != drill.ResultPass || out.Details["models_ini"] != "absent" || out.Details["library_xml"] != "absent" ||
		out.Details["whisper_env"] != "absent" || out.Details["portal_bundle"] != "not_linted" {
		t.Fatalf("outcome = %+v", out)
	}
	if out := run(t, OfflineLint{}.Check()); out.Result != drill.ResultPass || out.Details["models_ini"] != "not_linted" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestOfflineLintManyFindingsAreBounded(t *testing.T) {
	n := newNode(t)
	var b strings.Builder
	b.WriteString("<library>")
	for i := 0; i < 200; i++ {
		b.WriteString(`<book path="/missing/` + strings.Repeat("d", 30) + `/book-` + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + `.zim"/>`)
	}
	b.WriteString("</library>")
	n.file("var/kiwix/library.xml", b.String())
	out := run(t, n.lint("").Check())
	s, _ := out.Details["remote"].(string)
	if out.Result != drill.ResultFail || out.Details["remote_count"] != 200 || len(s) > 500 {
		t.Fatalf("count=%v len=%d", out.Details["remote_count"], len(s))
	}
}

func TestOfflineLintCountsTheFilesOfTheBundle(t *testing.T) {
	// An unbuilt tree has only a .gitkeep; internal/app runs the lint on the real embedded ui.Dist().
	o := OfflineLint{Bundle: fstest.MapFS{".gitkeep": {}}}
	if out := run(t, o.Check()); out.Result != drill.ResultPass || out.Details["bundle_files"] != 1 {
		t.Fatalf("outcome = %+v", out)
	}
}
