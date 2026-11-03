package kit_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// putSrc writes kit/<part>/<name> on a root and records its digest in kit/<part>/MANIFEST.json (the
// manifest lists exactly the files given in recorded: name -> digest).
func putKitFile(t *testing.T, root, part, name, body string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "kit", part, name), body)
}

func recordFiles(t *testing.T, root, part string, files map[string]string) {
	t.Helper()
	list := ""
	for n, sum := range files {
		if list != "" {
			list += ","
		}
		list += fmt.Sprintf(`{"path":%q,"sha256":%q}`, n, sum)
	}
	writeFile(t, filepath.Join(root, "kit", part, "MANIFEST.json"), `{"schema_version":1,"files":[`+list+`]}`)
}

func TestFindAgentSource(t *testing.T) {
	good := func(root, tag string) string {
		name := "local-ai-" + tag + "-src.tar.gz"
		putKitFile(t, root, "src", name, "tarball of "+tag+root)
		return name
	}
	t.Run("the newest tag wins, by semver and not by text", func(t *testing.T) {
		root := t.TempDir()
		files := map[string]string{}
		for _, tag := range []string{"node-v0.9.0", "node-v0.10.0", "node-v0.10.0-rc.1"} {
			n := good(root, tag)
			files[n] = shaOf("tarball of " + tag + root)
		}
		recordFiles(t, root, "src", files)
		got, err := kit.SourceFinder{Roots: []string{root}}.FindAgentSource("")
		if err != nil || got.Tag != "node-v0.10.0" || got.Version != "0.10.0" || got.Root != root {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("a tag is looked for by name", func(t *testing.T) {
		root := t.TempDir()
		n1, n2 := good(root, "node-v0.9.0"), good(root, "node-v0.10.0")
		recordFiles(t, root, "src", map[string]string{n1: shaOf("tarball of node-v0.9.0" + root), n2: shaOf("tarball of node-v0.10.0" + root)})
		f := kit.SourceFinder{Roots: []string{root}}
		if got, err := f.FindAgentSource("node-v0.9.0"); err != nil || got.Version != "0.9.0" {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := f.FindAgentSource("node-v1.0.0"); !errors.Is(err, kit.ErrSourceNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a tarball nobody recorded or that changed is skipped for the next one", func(t *testing.T) {
		root := t.TempDir()
		n1, n2, n3 := good(root, "node-v0.3.0"), good(root, "node-v0.2.0"), good(root, "node-v0.1.0")
		_ = n1 // the newest is not recorded at all
		recordFiles(t, root, "src", map[string]string{n2: shaOf("something else"), n3: shaOf("tarball of node-v0.1.0" + root)})
		got, err := kit.SourceFinder{Roots: []string{root}}.FindAgentSource("")
		if err != nil || got.Tag != "node-v0.1.0" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("only bad ones is damaged, none at all is not found", func(t *testing.T) {
		root := t.TempDir()
		good(root, "node-v0.3.0")
		if _, err := (kit.SourceFinder{Roots: []string{root}}).FindAgentSource(""); !errors.Is(err, kit.ErrSourceDamaged) {
			t.Fatalf("err = %v", err)
		}
		if _, err := (kit.SourceFinder{Roots: []string{t.TempDir()}}).FindAgentSource(""); !errors.Is(err, kit.ErrSourceNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("names that are not source tarballs are ignored", func(t *testing.T) {
		root := t.TempDir()
		for _, n := range []string{"local-ai-node-v0.3.0.tar.gz", "local-ai-v0.3.0-src.tar.gz", "local-ai-node-v0.3-src.tar.gz", "llama.cpp-abc.tar.gz"} {
			putKitFile(t, root, "src", n, "x")
		}
		if _, err := (kit.SourceFinder{Roots: []string{root}}).FindAgentSource(""); !errors.Is(err, kit.ErrSourceNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the second root is used when the first has none", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		n := good(b, "node-v0.3.0")
		recordFiles(t, b, "src", map[string]string{n: shaOf("tarball of node-v0.3.0" + b)})
		got, err := kit.SourceFinder{Roots: []string{a, b}}.FindAgentSource("")
		if err != nil || got.Root != b {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestFindGoToolchain(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for _, n := range []string{"go1.24.7.linux-amd64.tar.gz", "go1.24.10.linux-amd64.tar.gz", "go1.24.10.linux-arm64.tar.gz", "go1.23.1.linux-amd64.tar.gz"} {
		putKitFile(t, root, "toolchains", n, "go "+n)
		files[n] = shaOf("go " + n)
	}
	recordFiles(t, root, "toolchains", files)
	f := kit.SourceFinder{Roots: []string{root}}
	if got, err := f.FindGoToolchain("amd64"); err != nil || got.Version != "1.24.10" || got.Name != "go1.24.10.linux-amd64.tar.gz" {
		t.Fatalf("amd64: %+v %v", got, err)
	}
	if got, err := f.FindGoToolchain("arm64"); err != nil || got.Version != "1.24.10" {
		t.Fatalf("arm64: %+v %v", got, err)
	}
	// A changed newest file falls back to the older one that matches.
	if err := os.WriteFile(filepath.Join(root, "kit", "toolchains", "go1.24.10.linux-amd64.tar.gz"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := f.FindGoToolchain("amd64"); err != nil || got.Version != "1.24.7" {
		t.Fatalf("after tampering: %+v %v", got, err)
	}
	if _, err := f.FindGoToolchain("riscv64"); !errors.Is(err, kit.ErrSourceNotFound) {
		t.Fatalf("an architecture the kit lacks: %v", err)
	}
	if !kit.GoVersionAtLeast("1.24.7", "1.24") || kit.GoVersionAtLeast("1.24.7", "1.24.8") || !kit.GoVersionAtLeast("1.25", "1.24.9") || kit.GoVersionAtLeast("1.23.9", "1.24") {
		t.Error("GoVersionAtLeast is wrong")
	}
}

func TestRecordedIn(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	sum := shaOf("a")
	cases := []struct {
		name, manifest string
		ok             bool
	}{
		{"recorded", fmt.Sprintf(`{"files":[{"path":"a","sha256":%q}]}`, sum), true},
		{"by name", fmt.Sprintf(`{"files":[{"name":"a","sha256":%q}]}`, sum), true},
		{"not listed", fmt.Sprintf(`{"files":[{"path":"b","sha256":%q}]}`, sum), false},
		{"bad digest", `{"files":[{"path":"a","sha256":"abc"}]}`, false},
		{"two digests", fmt.Sprintf(`{"files":[{"path":"a","sha256":%q},{"path":"a","sha256":%q}]}`, sum, shaOf("b")), false},
		{"not JSON", "{no", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, filepath.Join(dir, "MANIFEST.json"), tc.manifest)
			got, ok := kit.SourceFinder{}.RecordedIn(dir, "a")
			if ok != tc.ok || (ok && got != sum) {
				t.Fatalf("%q %v", got, ok)
			}
		})
	}
	if _, ok := (kit.SourceFinder{}).RecordedIn(filepath.Join(root, "none"), "a"); ok {
		t.Error("no manifest, no record")
	}
}
