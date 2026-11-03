package kit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// srcCommit and srcOther are full commits built at run time (no 40-hex literal in the source).
var (
	srcCommit = "a1b2c3d4e" + strings.Repeat("f0", 15) + "1"
	srcOther  = "a1b2c3d4e" + strings.Repeat("0f", 15) + "2"
)

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func srcWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// srcTarball writes kit/src/llama.cpp-<commit>.tar.gz on root and returns its digest.
func srcTarball(t *testing.T, root, commit, content string) string {
	t.Helper()
	srcWrite(t, filepath.Join(kit.SourceTarballDir(root), kit.SourceTarballName(kit.SourceStemLlama, commit)), content)
	return digest([]byte(content))
}

// recordInMirror writes the mirror item's manifest with source.tarballs.
func recordInMirror(t *testing.T, root string, entries map[string]string) {
	t.Helper()
	var parts []string
	for name, sum := range entries {
		parts = append(parts, fmt.Sprintf(`{"kind":"source_tarball","path":%q,"sha256":%q}`, name, sum))
	}
	srcWrite(t, filepath.Join(kit.MirrorDir(root, kit.SourceStemLlama), "MANIFEST.json"),
		`{"schema_version":1,"source":{"type":"git_mirror","tarballs":[`+strings.Join(parts, ",")+`]}}`)
}

// recordInSrc writes kit/src/MANIFEST.json with files.
func recordInSrc(t *testing.T, root string, entries map[string]string) {
	t.Helper()
	var parts []string
	for name, sum := range entries {
		parts = append(parts, fmt.Sprintf(`{"kind":"source_tarball","path":%q,"sha256":%q}`, name, sum))
	}
	srcWrite(t, filepath.Join(kit.SourceTarballDir(root), "MANIFEST.json"), `{"schema_version":1,"files":[`+strings.Join(parts, ",")+`]}`)
}

func tarName(commit string) string { return kit.SourceTarballName(kit.SourceStemLlama, commit) }

func TestFindTarball(t *testing.T) {
	type setup func(t *testing.T, r1, r2 string)
	tests := []struct {
		name     string
		ref      string
		setup    setup
		wantRoot int // 1, 2, or 0 for an error
		wantErr  error
	}{
		{"full commit, recorded in the mirror manifest", srcCommit, func(t *testing.T, r1, _ string) {
			sum := srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): sum})
		}, 1, nil},
		{"full commit, recorded in kit/src manifest", srcCommit, func(t *testing.T, r1, _ string) {
			sum := srcTarball(t, r1, srcCommit, "tree one")
			recordInSrc(t, r1, map[string]string{tarName(srcCommit): sum})
		}, 1, nil},
		{"short commit finds the one tarball", srcCommit[:9], func(t *testing.T, r1, _ string) {
			sum := srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): sum})
		}, 1, nil},
		{"a short commit that names two tarballs is not guessed", srcCommit[:8], func(t *testing.T, r1, _ string) {
			a := srcTarball(t, r1, srcCommit, "tree one")
			b := srcTarball(t, r1, srcOther, "tree two")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): a, tarName(srcOther): b})
		}, 0, kit.ErrSourceNotFound},
		{"a damaged tarball on the first root falls through to the second", srcCommit, func(t *testing.T, r1, r2 string) {
			srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): digest([]byte("something else"))})
			sum := srcTarball(t, r2, srcCommit, "tree two")
			recordInMirror(t, r2, map[string]string{tarName(srcCommit): sum})
		}, 2, nil},
		{"only a damaged tarball", srcCommit, func(t *testing.T, r1, _ string) {
			srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): digest([]byte("something else"))})
		}, 0, kit.ErrSourceDamaged},
		{"a tarball nobody recorded cannot be trusted", srcCommit, func(t *testing.T, r1, _ string) {
			srcTarball(t, r1, srcCommit, "tree one")
		}, 0, kit.ErrSourceDamaged},
		{"two manifests that disagree cannot be trusted", srcCommit, func(t *testing.T, r1, _ string) {
			sum := srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): sum})
			recordInSrc(t, r1, map[string]string{tarName(srcCommit): digest([]byte("other"))})
		}, 0, kit.ErrSourceDamaged},
		{"a recorded value that is not a sha256 cannot be trusted", srcCommit, func(t *testing.T, r1, _ string) {
			srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): "not-a-digest"})
		}, 0, kit.ErrSourceDamaged},
		{"no tarball anywhere", srcCommit, func(*testing.T, string, string) {}, 0, kit.ErrSourceNotFound},
		{"a tarball for another commit does not match", srcOther, func(t *testing.T, r1, _ string) {
			sum := srcTarball(t, r1, srcCommit, "tree one")
			recordInMirror(t, r1, map[string]string{tarName(srcCommit): sum})
		}, 0, kit.ErrSourceNotFound},
		{"a ref that is not hex finds nothing", "../../etc", func(*testing.T, string, string) {}, 0, kit.ErrSourceNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r1, r2 := t.TempDir(), t.TempDir()
			tc.setup(t, r1, r2)
			f := kit.SourceFinder{FS: kit.OSSourceFS{}, Roots: []string{r1, r2}}
			got, err := f.FindTarball(kit.SourceStemLlama, tc.ref)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v (%+v)", tc.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := map[int]string{1: r1, 2: r2}[tc.wantRoot]
			if got.Root != want || got.Commit != srcCommit || got.Name != tarName(srcCommit) {
				t.Fatalf("got %+v, want root %s commit %s", got, want, srcCommit)
			}
			if got.SHA256 != digest(mustRead(t, got.Path)) || got.Size != int64(len(mustRead(t, got.Path))) {
				t.Fatalf("digest or size wrong: %+v", got)
			}
		})
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSourceNames(t *testing.T) {
	if got := kit.SourceTarballName("llama.cpp", srcCommit); got != "llama.cpp-"+srcCommit+".tar.gz" {
		t.Fatal(got)
	}
	if got := kit.MirrorDir("/srv/localai", "llama.cpp"); got != "/srv/localai/kit/git/llama.cpp.git" {
		t.Fatal(got)
	}
	if got := kit.SourceTarballDir("/srv/localai"); got != "/srv/localai/kit/src" {
		t.Fatal(got)
	}
}

func TestMirrorsInRootOrder(t *testing.T) {
	r1, r2, r3 := t.TempDir(), t.TempDir(), t.TempDir()
	for _, r := range []string{r1, r3} {
		if err := os.MkdirAll(kit.MirrorDir(r, kit.SourceStemLlama), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A file where the mirror should be is not a mirror.
	srcWrite(t, kit.MirrorDir(r2, kit.SourceStemLlama), "not a directory")
	f := kit.SourceFinder{Roots: []string{r1, r2, r3}}
	got := f.Mirrors(kit.SourceStemLlama)
	want := []string{kit.MirrorDir(r1, kit.SourceStemLlama), kit.MirrorDir(r3, kit.SourceStemLlama)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCudaRedists(t *testing.T) {
	r1 := t.TempDir()
	sum := digest([]byte("x"))
	write := func(dir, manifest string, files ...string) {
		for _, f := range files {
			srcWrite(t, filepath.Join(r1, "kit", "cuda", dir, f), "x")
		}
		srcWrite(t, filepath.Join(r1, "kit", "cuda", dir, "MANIFEST.json"), manifest)
	}
	entry := func(name string) string { return fmt.Sprintf(`{"path":%q,"sha256":%q}`, name, sum) }
	write("12.4-amd64", `{"files":[`+entry("cuda_nvcc.tar.xz")+`,`+entry("libcublas.tar.xz")+`,`+entry("../evil.tar.xz")+`,`+entry("notes.txt")+`]}`, "cuda_nvcc.tar.xz", "libcublas.tar.xz")
	write("12.8-amd64", `{"files":[`+entry("cuda_nvcc.tar.xz")+`]}`, "cuda_nvcc.tar.xz")
	write("12.9-arm64", `{"files":[`+entry("cuda_nvcc.tar.xz")+`]}`, "cuda_nvcc.tar.xz")
	write("12.6-amd64", `not json`)
	write("12.7-amd64", `{"files":[]}`)
	write("bogus", `{"files":[`+entry("a.tar.xz")+`]}`)

	got := kit.SourceFinder{Roots: []string{r1}}.CudaRedists("amd64")
	if len(got) != 2 || got[0].Version != "12.8" || got[1].Version != "12.4" {
		t.Fatalf("got %+v", got)
	}
	if n := len(got[1].Files); n != 2 || got[1].Files[0].Name != "cuda_nvcc.tar.xz" || got[1].Files[0].SHA256 != sum {
		t.Fatalf("files: %+v", got[1].Files)
	}
	if got := (kit.SourceFinder{Roots: []string{r1}}).CudaRedists("arm64"); len(got) != 1 || got[0].Version != "12.9" {
		t.Fatalf("arm64: %+v", got)
	}
}
