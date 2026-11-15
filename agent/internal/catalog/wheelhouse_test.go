package catalog

import (
	"strings"
	"testing"
)

// The pip_wheelhouse source type (catalog.md section 3, rebuild-kit.md section 3.2.2).

const wheelhouseItem = `
  - id: kit-engine-acestep-cuda
    kind: kit
    source:
      type: pip_wheelhouse
      version: "1.5.0"
      lock: engines/acestep/requirements-cuda.lock
      sha256: "@sha:e@"
      files:
        - {url: "https://downloads.example.test/cpython-3.12.16+1-install_only.tar.gz", sha256: "@sha:f@", name: "python/cpython-3.12.16+1-install_only.tar.gz"}
      indexes: [https://pypi.example.test/simple]
      builds:
        - {url: "https://git.example.test/engine.git", commit: "0123456789abcdef0123456789abcdef01234567"}
        - {url: "https://git.example.test/engine.git", commit: "0123456789abcdef0123456789abcdef01234567", subdirectory: "third/part"}
        - {repo_path: engines/acestep}
`

func withWheelhouse(edit func(string) string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		item := wheelhouseItem
		if edit != nil {
			item = edit(item)
		}
		writeFile(t, dir, fA, readFile(t, dir, fA)+expand(item))
	}
}

func TestWheelhouseItemIsValid(t *testing.T) {
	dir := validDir(t)
	withWheelhouse(nil)(t, dir)
	if rep := Lint(dir, LintOptions{}); !rep.Valid() {
		t.Fatalf("expected valid, got:\n%s", problemsText(rep))
	}
	cat, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := cat.ContentItem("kit-engine-acestep-cuda")
	if !ok || len(it.Source.Builds) != 3 || it.Source.Builds[1].Subdirectory != "third/part" {
		t.Fatalf("item = %+v", it)
	}
	v := WheelhouseVersion(it.ID, it.Source)
	if !strings.HasPrefix(v, "1.5.0-") || len(v) != len("1.5.0-")+8 {
		t.Fatalf("version = %q", v)
	}
	// Every pin is part of the version: a change of any of them is a new runtime.
	for name, change := range map[string]func(*ContentSource){
		"lock": func(s *ContentSource) { s.SHA256 = strings.Repeat("0", 64) },
		"python": func(s *ContentSource) {
			s.Files = []URLFile{{URL: s.Files[0].URL, SHA256: strings.Repeat("1", 64), Name: s.Files[0].Name}}
		},
		"index": func(s *ContentSource) { s.Indexes = []string{"https://other.example.test/simple"} },
		"build": func(s *ContentSource) {
			s.Builds = append([]WheelBuild{}, s.Builds...)
			s.Builds[0].Commit = strings.Repeat("a", 40)
		},
	} {
		src := it.Source
		change(&src)
		if WheelhouseVersion(it.ID, src) == v {
			t.Errorf("a change of the %s pin kept the version", name)
		}
	}
	if WheelhouseVersion("kit-engine-acestep-vulkan", it.Source) == v {
		t.Error("the backend is not part of the version")
	}
	if e, b, ok := WheelhouseItem("kit-engine-acestep-cuda"); !ok || e != "acestep" || b != "cuda" {
		t.Errorf("WheelhouseItem = %s %s %v", e, b, ok)
	}
	for _, bad := range []string{"kit-engine-acestep", "kit-engine-other-cuda", "kit-engine-acestep-tpu", "engine-acestep-cuda"} {
		if _, _, ok := WheelhouseItem(bad); ok {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestWheelhouseRules(t *testing.T) {
	const ip = "content[12]"
	cases := []struct {
		name string
		edit func(string) string
		want []want
	}{
		{"015 id is not kit-engine-<engine>-<backend>", replacing("id: kit-engine-acestep-cuda", "id: kit-engine-music-cuda"), []want{w(CodeArkItem, fA, ip+".id")}},
		{"010 no version", replacing(`      version: "1.5.0"`+"\n", ""), []want{w(CodeSchema, fA, ip+".source.version")}},
		{"010 version with a slash", replacing(`version: "1.5.0"`, `version: "1.5/0"`), []want{w(CodeSchema, fA, ip+".source.version")}},
		{"010 lock outside the repository", replacing("lock: engines/acestep/requirements-cuda.lock", "lock: ../requirements.lock"), []want{w(CodeSchema, fA, ip+".source.lock")}},
		{"011 lock not pinned", replacing(`      sha256: "@sha:e@"`+"\n", ""), []want{w(CodePinning, fA, ip+".source.sha256")}},
		{"010 Python not under python/", replacing(`name: "python/cpython-3.12.16+1-install_only.tar.gz"`, `name: "cpython.tar.gz"`), []want{w(CodeSchema, fA, ip+".source.files[0].name")}},
		{"010 Python over plain http", replacing(`url: "https://downloads.example.test/`, `url: "http://downloads.example.test/`), []want{w(CodeSchema, fA, ip+".source.files[0].url")}},
		{"010 an index that is not https", replacing("indexes: [https://pypi.example.test/simple]", "indexes: [ftp://pypi.example.test/simple]"), []want{w(CodeSchema, fA, ip+".source.indexes[0]")}},
		{"011 a git build without a full commit", replacing(`commit: "0123456789abcdef0123456789abcdef01234567"}`, `commit: "main"}`), []want{w(CodePinning, fA, ip+".source.builds[0].commit")}},
		{"015 a build that is both", replacing("{repo_path: engines/acestep}", `{repo_path: engines/acestep, url: "https://git.example.test/x.git"}`), []want{w(CodeArkItem, fA, ip+".source.builds[2]")}},
		{"010 a field of another source type", replacing("      type: pip_wheelhouse\n", "      type: pip_wheelhouse\n      codename: resolute\n"), []want{w(CodeSchema, fA, ip+".source.codename")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := validDir(t)
			withWheelhouse(c.edit)(t, dir)
			rep := Lint(dir, LintOptions{})
			if len(rep.Problems) != len(c.want) {
				t.Fatalf("want %d problem(s), got %d:\n%s", len(c.want), len(rep.Problems), problemsText(rep))
			}
			for _, wnt := range c.want {
				if !has(rep, wnt.code, wnt.file, wnt.path) {
					t.Errorf("missing %s %s:%s in:\n%s", wnt.code, wnt.file, wnt.path, problemsText(rep))
				}
			}
		})
	}
}
