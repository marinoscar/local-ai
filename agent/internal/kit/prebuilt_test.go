package kit_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// commitOf is a full commit built at run time (no 40-hex literal in the source).
func commitOf(prefix string) string {
	s := prefix + strings.Repeat("a1", 20)
	return s[:40]
}

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---- naming -------------------------------------------------------------------------------------

func TestPrebuiltNaming(t *testing.T) {
	c := commitOf("f3586e66b")
	for _, tc := range []struct {
		name                                       string
		program, backend, codename, arch, cudaArch string
		want                                       string
	}{
		{"cuda has the compute capability", "llama.cpp", "cuda", "resolute", "amd64", "75", "llama.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz"},
		{"several capabilities are joined", "llama.cpp", "cuda", "resolute", "amd64", "75;86", "llama.cpp-f3586e66b-cuda-resolute-amd64-sm75_86.tar.gz"},
		{"vulkan has no suffix", "llama.cpp", "vulkan", "noble", "arm64", "", "llama.cpp-f3586e66b-vulkan-noble-arm64.tar.gz"},
		{"a cpu build ignores a stray capability", "whisper.cpp", "cpu", "noble", "amd64", "75", "whisper.cpp-f3586e66b-cpu-noble-amd64.tar.gz"},
		{"whisper cuda", "whisper.cpp", "cuda", "resolute", "amd64", "75", "whisper.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kit.PrebuiltName(tc.program, c, tc.backend, tc.codename, tc.arch, tc.cudaArch)
			if got != tc.want {
				t.Fatalf("name = %q, want %q", got, tc.want)
			}
			rec, ok := kit.ParsePrebuiltName(got)
			if !ok || rec.Program != tc.program || rec.Backend != tc.backend || rec.Codename != tc.codename || rec.Arch != tc.arch || rec.Commit != c[:9] {
				t.Fatalf("parsed %+v ok=%v", rec, ok)
			}
			if tc.backend == "cuda" && rec.CudaArch != tc.cudaArch {
				t.Errorf("cuda arch = %q, want %q", rec.CudaArch, tc.cudaArch)
			}
		})
	}
	for _, bad := range []string{"llama.cpp-f3586e66b-cuda-resolute-amd64.tar.gz.part", "llama.cpp-f3586e66-cuda-resolute-amd64.tar.gz", "other-f3586e66b-cuda-resolute-amd64.tar.gz",
		"llama.cpp-f3586e66b-metal-resolute-amd64.tar.gz", "../llama.cpp-f3586e66b-cpu-resolute-amd64.tar.gz", "llama.cpp-f3586e66b-cpu-resolute-riscv.tar.gz"} {
		if _, ok := kit.ParsePrebuiltName(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}

// ---- a node with a build to export --------------------------------------------------------------

type exportRig struct {
	t      *testing.T
	roots  []string
	fs     kit.FS
	run    *testRunner
	node   string
	kernel string
}

func newExportRig(t *testing.T, nRoots int) *exportRig {
	t.Helper()
	r := &exportRig{t: t, fs: kit.OSFS{}, node: "ai-n-test", kernel: "6.14.0-test"}
	for i := 0; i < nRoots; i++ {
		r.roots = append(r.roots, filepath.Join(t.TempDir(), "root"))
	}
	r.run = &testRunner{}
	return r
}

type buildSpec struct {
	commit   string
	backend  string
	cudaArch string
	builtAt  string
	programs []string
	version  string
	lib      map[string]string // file name -> content; "link:<target>" makes a symlink
	tamper   string            // a program whose bytes are changed after BUILD.json was written
}

func (r *exportRig) makeBuild(s buildSpec) string {
	r.t.Helper()
	if s.backend == "" {
		s.backend = "cuda"
	}
	if s.cudaArch == "" && s.backend == "cuda" {
		s.cudaArch = "75"
	}
	if s.programs == nil {
		s.programs = []string{"llama-server", "llama-cli"}
	}
	if s.builtAt == "" {
		s.builtAt = "2026-11-02T10:00:00Z"
	}
	if s.version == "" {
		s.version = "version: 6000 (f3586e6)"
	}
	dir := filepath.Join(r.t.TempDir(), "builds", s.commit[:9]+"-"+s.backend)
	sums := map[string]string{}
	for _, p := range s.programs {
		body := []byte("ELF " + p + " " + s.commit + s.backend)
		writeFile(r.t, filepath.Join(dir, "bin", p), string(body))
		sums[p] = sum256(body)
	}
	doc := map[string]any{
		"build_id": s.commit[:9] + "-" + s.backend, "commit": s.commit, "backend": s.backend, "cmake_flags": []string{"-DCMAKE_BUILD_TYPE=Release", "-DGGML_CUDA=ON"},
		"compiler": "GNU 15.2.0", "built_at": s.builtAt, "built_by": "aiagent 0.1.0", "sha256": sums, "smoke": map[string]any{"version": s.version},
	}
	if s.backend == "cuda" {
		doc["cuda_arch"] = s.cudaArch
		doc["nvcc"] = "12.4.131"
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	writeFile(r.t, filepath.Join(dir, "BUILD.json"), string(raw))
	for n, c := range s.lib {
		p := filepath.Join(dir, "lib", n)
		if strings.HasPrefix(c, "link:") {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				r.t.Fatal(err)
			}
			if err := os.Symlink(strings.TrimPrefix(c, "link:"), p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		writeFile(r.t, p, c)
	}
	if s.tamper != "" {
		writeFile(r.t, filepath.Join(dir, "bin", s.tamper), "something else entirely")
	}
	return dir
}

func (r *exportRig) exporter(program string) *kit.PrebuiltExporter {
	return &kit.PrebuiltExporter{
		FS: r.fs, Runner: r.run, LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Roots:   func() []string { return r.roots },
		Program: program, Codename: "resolute", Arch: "amd64",
		NodeID: func() string { return r.node }, Kernel: func() string { return r.kernel },
	}
}

func (r *exportRig) driver(version string) {
	r.run.fn = func(c execx.Cmd) (execx.Result, error) {
		if filepath.Base(c.Path) == "nvidia-smi" {
			return execx.Result{Stdout: []byte(version + "\n")}, nil
		}
		return execx.Result{}, errors.New("unexpected command " + c.Path)
	}
}

// testRunner records commands and answers through fn.
type testRunner struct {
	calls []execx.Cmd
	fn    func(c execx.Cmd) (execx.Result, error)
}

func (t *testRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	t.calls = append(t.calls, c)
	if t.fn == nil {
		return execx.Result{}, errors.New("unexpected command " + c.Path)
	}
	return t.fn(c)
}

func (r *exportRig) manifestOf(root string) kit.PrebuiltManifest {
	r.t.Helper()
	m, err := kit.ReadPrebuiltManifest(r.fs, kit.PrebuiltDir(root))
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func readTar(t *testing.T, path string) (names []string, bodies map[string]string, headers map[string]*tar.Header, gzHeader gzip.Header) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	gzHeader = gz.Header
	bodies, headers = map[string]string{}, map[string]*tar.Header{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		names = append(names, h.Name)
		bodies[h.Name], headers[h.Name] = string(b), h
	}
}

// ---- export -------------------------------------------------------------------------------------

func TestExportWritesTheArchiveAndItsManifestOnEveryRoot(t *testing.T) {
	r := newExportRig(t, 2)
	r.driver("550.163.01")
	c := commitOf("f3586e66b")
	dir := r.makeBuild(buildSpec{commit: c, lib: map[string]string{"libcudart.so.12": "cudart", "libcudart.so": "link:libcudart.so.12"}})
	res, err := r.exporter("llama.cpp").Export(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "llama.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz"
	if res.File != want || len(res.Roots) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for _, root := range r.roots {
		p := filepath.Join(kit.PrebuiltDir(root), want)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := sum256(data); got != res.SHA256 {
			t.Errorf("%s has sha256 %s, result says %s", root, got, res.SHA256)
		}
		names, bodies, hdrs, gzh := readTar(t, p)
		if !reflect.DeepEqual(names, []string{"BUILD.json", "bin/", "bin/llama-cli", "bin/llama-server", "lib/", "lib/libcudart.so", "lib/libcudart.so.12"}) {
			t.Errorf("entries = %v", names)
		}
		if gzh.Name != "" || !gzh.ModTime.IsZero() {
			t.Errorf("the gzip header carries a name or a time (gzip -n): %+v", gzh)
		}
		if h := hdrs["bin/llama-server"]; h.Mode != 0o755 || h.Uid != 0 || h.Uname != "" || h.ModTime.Unix() != 0 {
			t.Errorf("header = %+v", h)
		}
		if h := hdrs["lib/libcudart.so"]; h.Typeflag != tar.TypeSymlink || h.Linkname != "libcudart.so.12" {
			t.Errorf("lib link = %+v", h)
		}
		onDisk, _ := os.ReadFile(filepath.Join(dir, "BUILD.json"))
		if bodies["BUILD.json"] != string(onDisk) {
			t.Error("BUILD.json must be exported byte for byte")
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(kit.PrebuiltDir(root), ".staging", want+".part")); err == nil {
			t.Error("a staging file was left behind")
		}
		m := r.manifestOf(root)
		if m.Schema != "localai.prebuilt-manifest" || m.SchemaVersion != 1 || len(m.Files) != 1 {
			t.Fatalf("manifest = %+v", m)
		}
		got := m.Files[0]
		wantRec := kit.PrebuiltRecord{
			File: want, Program: "llama.cpp", Commit: c, Backend: "cuda", Codename: "resolute", Arch: "amd64", CudaArch: "75",
			SHA256: res.SHA256, Size: int64(len(data)), CMakeFlags: []string{"-DCMAKE_BUILD_TYPE=Release", "-DGGML_CUDA=ON"},
			VersionOutput: "version: 6000 (f3586e6)", CudaVersion: "12.4.131", DriverVersion: "550.163.01", Kernel: "6.14.0-test",
			NodeID: "ai-n-test", BuiltAt: "2026-11-02T10:00:00Z",
		}
		if !reflect.DeepEqual(got, wantRec) {
			t.Errorf("record:\n got %+v\nwant %+v", got, wantRec)
		}
	}
}

func TestExportRecordsOnlyWhatABackendHas(t *testing.T) {
	r := newExportRig(t, 1)
	c := commitOf("0a1b2c3d4")
	dir := r.makeBuild(buildSpec{commit: c, backend: "cpu"})
	res, err := r.exporter("llama.cpp").Export(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.CudaArch != "" || res.Record.CudaVersion != "" || res.Record.DriverVersion != "" {
		t.Errorf("a cpu build has no CUDA or driver version: %+v", res.Record)
	}
	for _, c := range r.run.calls {
		t.Errorf("a cpu export must run nothing, ran %s", c.Path)
	}
	if res.File != "llama.cpp-0a1b2c3d4-cpu-resolute-amd64.tar.gz" {
		t.Errorf("file = %s", res.File)
	}
}

func TestExportOfTheSameBuildIsIdempotent(t *testing.T) {
	r := newExportRig(t, 1)
	dir := r.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"})
	ex := r.exporter("llama.cpp")
	first, err := ex.Export(context.Background(), dir)
	if err != nil || !first.Roots[0].Written {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := ex.Export(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Roots[0].Unchanged || second.Roots[0].Written || second.SHA256 != first.SHA256 {
		t.Fatalf("a second export of the same build must change nothing: %+v", second.Roots[0])
	}
	if got := len(r.manifestOf(r.roots[0]).Files); got != 1 {
		t.Errorf("the manifest has %d records", got)
	}
}

func TestExportKeepsTheNewestTwoPerProgramNodeAndBackend(t *testing.T) {
	r := newExportRig(t, 1)
	root := r.roots[0]
	ex := r.exporter("llama.cpp")
	export := func(commit, backend, at string) kit.PrebuiltRootResult {
		t.Helper()
		dir := r.makeBuild(buildSpec{commit: commit, backend: backend, builtAt: at})
		res, err := ex.Export(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		return res.Roots[0]
	}
	c1, c2, c3 := commitOf("111111111"), commitOf("222222222"), commitOf("333333333")
	export(c1, "cpu", "2026-11-01T10:00:00Z")
	export(c2, "cpu", "2026-11-02T10:00:00Z")
	// Another backend and another program are separate groups, and a third cpu export prunes the oldest.
	export(c1, "vulkan", "2026-11-01T10:00:00Z")
	otherProgram := r.exporter("whisper.cpp")
	if _, err := otherProgram.Export(context.Background(), r.makeBuild(buildSpec{commit: c1, backend: "cpu", builtAt: "2026-10-01T10:00:00Z"})); err != nil {
		t.Fatal(err)
	}
	third := export(c3, "cpu", "2026-11-03T10:00:00Z")
	if !reflect.DeepEqual(third.Pruned, []string{"llama.cpp-111111111-cpu-resolute-amd64.tar.gz"}) {
		t.Fatalf("pruned = %v", third.Pruned)
	}
	var files []string
	for _, f := range r.manifestOf(root).Files {
		files = append(files, f.File)
	}
	want := []string{
		"llama.cpp-111111111-vulkan-resolute-amd64.tar.gz", "llama.cpp-222222222-cpu-resolute-amd64.tar.gz",
		"llama.cpp-333333333-cpu-resolute-amd64.tar.gz", "whisper.cpp-111111111-cpu-resolute-amd64.tar.gz",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("manifest files = %v\nwant %v", files, want)
	}
	entries, _ := os.ReadDir(kit.PrebuiltDir(root))
	var onDisk []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			onDisk = append(onDisk, e.Name())
		}
	}
	if !reflect.DeepEqual(onDisk, want) {
		t.Errorf("files on disk = %v\nwant %v", onDisk, want)
	}
}

func TestExportOfAnOlderBuildNeverDeletesTheLastCopies(t *testing.T) {
	r := newExportRig(t, 1)
	root := r.roots[0]
	ex := r.exporter("llama.cpp")
	cs := []string{commitOf("111111111"), commitOf("222222222"), commitOf("333333333")}
	for i, c := range cs[:2] {
		if _, err := ex.Export(context.Background(), r.makeBuild(buildSpec{commit: c, backend: "cpu", builtAt: "2026-11-0" + string(rune('1'+i)) + "T10:00:00Z"})); err != nil {
			t.Fatal(err)
		}
	}
	// The newer of the two kept files vanished from the drive: only one copy exists, so the oldest must stay.
	if err := os.Remove(filepath.Join(kit.PrebuiltDir(root), "llama.cpp-222222222-cpu-resolute-amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Export(context.Background(), r.makeBuild(buildSpec{commit: cs[2], backend: "cpu", builtAt: "2026-11-03T10:00:00Z"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Roots[0].Pruned) != 0 {
		t.Fatalf("pruned %v with only two copies left", res.Roots[0].Pruned)
	}
	for _, n := range []string{"llama.cpp-111111111-cpu-resolute-amd64.tar.gz", "llama.cpp-333333333-cpu-resolute-amd64.tar.gz"} {
		if _, err := os.Stat(filepath.Join(kit.PrebuiltDir(root), n)); err != nil {
			t.Errorf("%s must still be there: %v", n, err)
		}
	}
	for _, f := range r.manifestOf(root).Files {
		if strings.Contains(f.File, "222222222") {
			t.Error("the record of a file that is gone must be dropped")
		}
	}
}

func TestExportRefusesABuildThatWouldNotRestore(t *testing.T) {
	c := commitOf("f3586e66b")
	for _, tc := range []struct {
		name string
		spec buildSpec
		edit func(dir string)
	}{
		{"a program differs from BUILD.json", buildSpec{commit: c, backend: "cpu", tamper: "llama-cli"}, nil},
		{"a program is missing", buildSpec{commit: c, backend: "cpu"}, func(d string) { _ = os.Remove(filepath.Join(d, "bin", "llama-cli")) }},
		{"a program is a link", buildSpec{commit: c, backend: "cpu"}, func(d string) {
			p := filepath.Join(d, "bin", "llama-cli")
			_ = os.Remove(p)
			_ = os.Symlink("/etc/passwd", p)
		}},
		{"no BUILD.json", buildSpec{commit: c, backend: "cpu"}, func(d string) { _ = os.Remove(filepath.Join(d, "BUILD.json")) }},
		{"BUILD.json is not JSON", buildSpec{commit: c, backend: "cpu"}, func(d string) { writeFile(t, filepath.Join(d, "BUILD.json"), "{no") }},
		{"lib links outside its folder", buildSpec{commit: c, backend: "cpu", lib: map[string]string{"libx.so": "link:../../etc/passwd"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newExportRig(t, 2)
			dir := r.makeBuild(tc.spec)
			if tc.edit != nil {
				tc.edit(dir)
			}
			_, err := r.exporter("llama.cpp").Export(context.Background(), dir)
			if !errors.Is(err, kit.ErrExportRefused) {
				t.Fatalf("err = %v, want ErrExportRefused", err)
			}
			for _, root := range r.roots {
				entries, _ := os.ReadDir(kit.PrebuiltDir(root))
				for _, e := range entries {
					if e.Name() != ".staging" {
						t.Errorf("%s was left in %s", e.Name(), root)
					}
				}
				if left, _ := os.ReadDir(filepath.Join(kit.PrebuiltDir(root), ".staging")); len(left) > 0 {
					t.Errorf("staging not cleaned: %v", left)
				}
			}
		})
	}
}

// readOnlyFS refuses to create anything below a folder, like a drive mounted read-only.
type readOnlyFS struct {
	kit.FS
	below string
}

func (f readOnlyFS) MkdirAll(p string, perm os.FileMode) error {
	if strings.HasPrefix(p, f.below) {
		return &os.PathError{Op: "mkdir", Path: p, Err: syscall.EROFS}
	}
	return f.FS.MkdirAll(p, perm)
}

func TestExportSkipsAReadOnlyRootAndWritesTheOthers(t *testing.T) {
	r := newExportRig(t, 2)
	r.fs = readOnlyFS{FS: kit.OSFS{}, below: r.roots[0]}
	dir := r.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"})
	res, err := r.exporter("llama.cpp").Export(context.Background(), dir)
	if err != nil {
		t.Fatalf("a read-only drive is expected, not a failure: %v", err)
	}
	if !res.Roots[0].ReadOnly || res.Roots[0].Written || !res.Roots[1].Written {
		t.Fatalf("roots = %+v", res.Roots)
	}
	if _, err := os.Stat(kit.PrebuiltDir(r.roots[0])); err == nil {
		t.Error("nothing may be created on a read-only root")
	}
	// Every root read-only: nothing was exported, which is an error.
	r2 := newExportRig(t, 1)
	r2.fs = readOnlyFS{FS: kit.OSFS{}, below: r2.roots[0]}
	_, err = r2.exporter("llama.cpp").Export(context.Background(), r2.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"}))
	if !errors.Is(err, kit.ErrNoWritableRoot) {
		t.Fatalf("err = %v, want ErrNoWritableRoot", err)
	}
}

func TestExportReplacesADamagedManifest(t *testing.T) {
	r := newExportRig(t, 1)
	writeFile(t, filepath.Join(kit.PrebuiltDir(r.roots[0]), "MANIFEST.json"), "{not json")
	if _, err := r.exporter("llama.cpp").Export(context.Background(), r.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"})); err != nil {
		t.Fatal(err)
	}
	if got := len(r.manifestOf(r.roots[0]).Files); got != 1 {
		t.Errorf("records = %d", got)
	}
}

func TestExportBuildHookChecksTheBuildID(t *testing.T) {
	r := newExportRig(t, 1)
	dir := r.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"})
	ex := r.exporter("llama.cpp")
	if err := ex.ExportBuild(context.Background(), "f3586e66b-cpu", dir); err != nil {
		t.Fatal(err)
	}
	if err := ex.ExportBuild(context.Background(), "000000000-cpu", dir); !errors.Is(err, kit.ErrExportRefused) {
		t.Fatalf("a build id that is not the folder's: %v", err)
	}
}

// ---- selection ----------------------------------------------------------------------------------

func TestSelectAndCheckPrebuilt(t *testing.T) {
	r := newExportRig(t, 2)
	ex := r.exporter("llama.cpp")
	cA, cB := commitOf("f3586e66b"), commitOf("0a1b2c3d4")
	for _, s := range []buildSpec{
		{commit: cA, backend: "cuda", cudaArch: "75", builtAt: "2026-11-02T10:00:00Z"},
		{commit: cB, backend: "cuda", cudaArch: "75", builtAt: "2026-11-01T10:00:00Z"},
		{commit: cA, backend: "vulkan", builtAt: "2026-11-02T09:00:00Z"},
	} {
		if _, err := ex.Export(context.Background(), r.makeBuild(s)); err != nil {
			t.Fatal(err)
		}
	}
	want := kit.PrebuiltWant{Program: "llama.cpp", Commit: cA, Backend: "cuda", Codename: "resolute", Arch: "amd64", CudaArch: "75"}

	t.Run("the matching file is chosen", func(t *testing.T) {
		got, err := kit.SelectPrebuilt(r.fs, r.roots, want)
		if err != nil {
			t.Fatal(err)
		}
		if got.Record.File != "llama.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz" || got.Root != r.roots[0] {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a short commit and no commit also match, the newest first", func(t *testing.T) {
		w := want
		w.Commit = cA[:7]
		if got, err := kit.SelectPrebuilt(r.fs, r.roots, w); err != nil || !strings.Contains(got.Record.File, "f3586e66b") {
			t.Fatalf("short: %+v %v", got, err)
		}
		w.Commit = ""
		if got, err := kit.SelectPrebuilt(r.fs, r.roots, w); err != nil || !strings.Contains(got.Record.File, "f3586e66b") {
			t.Fatalf("none: %+v %v", got, err)
		}
	})
	for _, tc := range []struct {
		name  string
		edit  func(w *kit.PrebuiltWant)
		field string
	}{
		{"another Ubuntu release", func(w *kit.PrebuiltWant) { w.Codename = "noble" }, "ubuntu"},
		{"another architecture", func(w *kit.PrebuiltWant) { w.Arch = "arm64" }, "arch"},
		{"another backend", func(w *kit.PrebuiltWant) { w.Backend = "cpu" }, "backend"},
		{"another compute capability", func(w *kit.PrebuiltWant) { w.CudaArch = "86" }, "cuda_arch"},
		{"another commit", func(w *kit.PrebuiltWant) { w.Commit = commitOf("999999999") }, "commit"},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			w := want
			tc.edit(&w)
			_, err := kit.SelectPrebuilt(r.fs, r.roots, w)
			var mm *kit.PrebuiltMismatchError
			if !errors.As(err, &mm) || !errors.Is(err, kit.ErrNoPrebuilt) {
				t.Fatalf("err = %v", err)
			}
			if len(mm.Have) != 3 {
				t.Errorf("the error must name what the kit has: %v", kit.PrebuiltFiles(mm.Have))
			}
			// CheckPrebuilt of an explicit file names the differing field.
			rec := mm.Have[0]
			for _, h := range mm.Have {
				if strings.Contains(h.File, "f3586e66b-cuda") {
					rec = h
				}
			}
			cerr := kit.CheckPrebuilt(r.fs, r.roots, rec, w)
			var cm *kit.PrebuiltMismatchError
			if !errors.As(cerr, &cm) || !errors.Is(cerr, kit.ErrPrebuiltMismatch) || len(cm.Diffs) != 1 || cm.Diffs[0].Field != tc.field {
				t.Fatalf("check = %v (%+v)", cerr, cm)
			}
			if !strings.Contains(cerr.Error(), "the kit has: ") {
				t.Errorf("message does not say what the kit has: %v", cerr)
			}
		})
	}
	t.Run("another program has no build", func(t *testing.T) {
		w := want
		w.Program = "whisper.cpp"
		_, err := kit.SelectPrebuilt(r.fs, r.roots, w)
		var mm *kit.PrebuiltMismatchError
		if !errors.As(err, &mm) || len(mm.Have) != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a vulkan node ignores the compute capability", func(t *testing.T) {
		w := kit.PrebuiltWant{Program: "llama.cpp", Backend: "vulkan", Codename: "resolute", Arch: "amd64", CudaArch: "99"}
		if _, err := kit.SelectPrebuilt(r.fs, r.roots, w); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a file with the wrong size is not offered", func(t *testing.T) {
		p := filepath.Join(kit.PrebuiltDir(r.roots[0]), "llama.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz")
		data, _ := os.ReadFile(p)
		if err := os.WriteFile(p, append(data, 0), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := kit.SelectPrebuilt(r.fs, r.roots, want)
		if err != nil || got.Root != r.roots[1] {
			t.Fatalf("the intact copy on the second root must be chosen: %+v %v", got, err)
		}
	})
}

func TestLoadPrebuilt(t *testing.T) {
	r := newExportRig(t, 1)
	res, err := r.exporter("llama.cpp").Export(context.Background(), r.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"}))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(kit.PrebuiltDir(r.roots[0]), res.File)

	t.Run("a recorded file", func(t *testing.T) {
		rec, recorded, err := kit.LoadPrebuilt(r.fs, file)
		if err != nil || !recorded || rec.SHA256 != res.SHA256 || rec.Commit != commitOf("f3586e66b") {
			t.Fatalf("%+v %v %v", rec, recorded, err)
		}
	})
	t.Run("a copy made by hand is read from its name", func(t *testing.T) {
		data, _ := os.ReadFile(file)
		copyPath := filepath.Join(t.TempDir(), res.File)
		if err := os.WriteFile(copyPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		rec, recorded, err := kit.LoadPrebuilt(r.fs, copyPath)
		if err != nil || recorded || rec.Backend != "cpu" || rec.Codename != "resolute" || rec.SHA256 != "" {
			t.Fatalf("%+v %v %v", rec, recorded, err)
		}
	})
	for _, tc := range []struct {
		name string
		edit func(dir string)
	}{
		{"no manifest in the kit folder", func(d string) { _ = os.Remove(filepath.Join(d, "MANIFEST.json")) }},
		{"a damaged manifest", func(d string) { writeFile(t, filepath.Join(d, "MANIFEST.json"), "{no") }},
		{"a manifest of another schema", func(d string) {
			writeFile(t, filepath.Join(d, "MANIFEST.json"), `{"schema":"other","schema_version":1,"files":[]}`)
		}},
		{"a file the manifest does not record", func(d string) {
			writeFile(t, filepath.Join(d, "MANIFEST.json"), `{"schema":"localai.prebuilt-manifest","schema_version":1,"files":[]}`)
		}},
		{"a file whose size changed", func(d string) {
			data, _ := os.ReadFile(filepath.Join(d, res.File))
			_ = os.WriteFile(filepath.Join(d, res.File), data[:len(data)-1], 0o644)
		}},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			rr := newExportRig(t, 1)
			if _, err := rr.exporter("llama.cpp").Export(context.Background(), rr.makeBuild(buildSpec{commit: commitOf("f3586e66b"), backend: "cpu"})); err != nil {
				t.Fatal(err)
			}
			dir := kit.PrebuiltDir(rr.roots[0])
			tc.edit(dir)
			_, _, err := kit.LoadPrebuilt(rr.fs, filepath.Join(dir, res.File))
			if !errors.Is(err, kit.ErrPrebuiltUnrecorded) {
				t.Fatalf("err = %v, want ErrPrebuiltUnrecorded", err)
			}
		})
	}
	t.Run("a stray name outside the kit", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "mine.tar.gz")
		writeFile(t, p, "x")
		if _, _, err := kit.LoadPrebuilt(r.fs, p); !errors.Is(err, kit.ErrPrebuiltUnrecorded) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a path that is not a file", func(t *testing.T) {
		if _, _, err := kit.LoadPrebuilt(r.fs, filepath.Join(t.TempDir(), "none.tar.gz")); !errors.Is(err, kit.ErrPrebuiltUnrecorded) {
			t.Fatalf("err = %v", err)
		}
	})
}
