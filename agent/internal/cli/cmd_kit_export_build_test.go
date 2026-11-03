package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// Tests of `aictl kit export-build` (KIT-05). The node is a temporary directory with builds in use
// under its /opt/localai; the exports are real archives.

// installBuild puts a build of a program in use under the node's opt root: builds/<id> and current.
func (f *kitFix) installBuild(t *testing.T, e exportSpec) string {
	t.Helper()
	e = e.withDefaults()
	dir := "llama.cpp"
	if e.program == kit.ProgramWhisper {
		dir = "whisper.cpp"
	}
	id := e.commit[:9] + "-" + e.backend
	base := filepath.Join(f.opt, dir)
	buildDirAt(t, filepath.Join(base, "builds", id), e)
	cur := filepath.Join(base, "current")
	_ = os.Remove(cur)
	if err := os.Symlink("builds/"+id, cur); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "builds", id)
}

func newExportFix(t *testing.T) *kitFix {
	t.Helper()
	f := newKitFix(t)
	osr := filepath.Join(t.TempDir(), "os-release")
	kitWrite(t, osr, "NAME=Ubuntu\nVERSION_CODENAME=resolute\n")
	old, oldK := kitHostFn, kitKernelFn
	host := kitHostFn()
	host.OSRelease = osr
	kitHostFn = func() kitHost { return host }
	kitKernelFn = func() string { return "6.14.0-test" }
	t.Cleanup(func() { kitHostFn, kitKernelFn = old, oldK })
	return f
}

var sizeRe = regexp.MustCompile(`\(\d+(\.\d+)? ?[KMG]?B\)`)

// stable makes an output comparable across runs: the wrapped lines are joined (a temporary path would
// wrap them differently every time), the node's root becomes <root> and the size of an archive (the
// compressor's output may differ between Go versions) becomes <size>.
func stable(r runResult, root string) runResult {
	fix := func(s string) string {
		s = unwrap(s)
		s = strings.ReplaceAll(s, root, "<root>")
		return sizeRe.ReplaceAllString(s, "(<size>)")
	}
	r.out, r.errOut = fix(r.out), fix(r.errOut)
	return r
}

// checkGoldenFree is checkGolden for an output that has been made stable: its lines are not wrapped, so
// the 80 column rule does not apply.
func checkGoldenFree(t *testing.T, name string, r runResult) {
	t.Helper()
	var b strings.Builder
	b.WriteString("== stdout ==\n" + r.out)
	if r.errOut != "" {
		b.WriteString("== stderr ==\n" + r.errOut)
	}
	fmt.Fprintf(&b, "== exit %d ==\n", r.code)
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateReadonly {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-readonly): %v", path, err)
	}
	if b.String() != string(want) {
		t.Errorf("%s differs.\n--- got ---\n%s--- want ---\n%s", name, b.String(), want)
	}
}

func TestKitExportBuildWritesBothProgramsToTheRoot(t *testing.T) {
	f := newExportFix(t)
	f.installBuild(t, exportSpec{program: kit.ProgramLlama, commit: "f3586e66b" + strings.Repeat("1", 31)})
	f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
	r := f.run(t, "kit", "export-build")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGoldenFree(t, "kit.export-build.en", stable(r, f.env.Paths.InternalRoot))
	es := f.run(t, "--lang", "es", "kit", "export-build")
	if es.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", es.code, es.out, es.errOut)
	}
	checkGoldenFree(t, "kit.export-build.es", stable(es, f.env.Paths.InternalRoot))

	root := f.env.Paths.InternalRoot
	files := kit.ListPrebuilt(kit.OSFS{}, []string{root}, "")
	var names []string
	for _, x := range files {
		names = append(names, x.Record.File)
	}
	want := []string{"llama.cpp-f3586e66b-cuda-resolute-amd64-sm75.tar.gz", "whisper.cpp-1a2b3c4d5-cuda-resolute-amd64-sm75.tar.gz"}
	if len(names) != 2 || !(names[0] == want[0] && names[1] == want[1] || names[0] == want[1] && names[1] == want[0]) {
		t.Fatalf("exports = %v, want %v", names, want)
	}
	for _, x := range files {
		if x.Record.Kernel != "6.14.0-test" || x.Record.Codename != "resolute" || x.Record.CudaVersion != "12.4.131" {
			t.Errorf("record = %+v", x.Record)
		}
	}
	// The audit line names the program and the file.
	var seen int
	for _, l := range f.auditLines() {
		if l["action"] == "kit.export_build" && l["result"] == "ok" {
			seen++
		}
	}
	if seen != 4 { // two programs, run twice
		t.Errorf("kit.export_build audit lines = %d, want 4: %v", seen, f.auditLines())
	}
}

func TestKitExportBuildSecondRunChangesNothing(t *testing.T) {
	f := newExportFix(t)
	f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
	if r := f.run(t, "kit", "export-build"); r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	r := f.run(t, "kit", "export-build")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Already in") || strings.Contains(r.out, "Written to") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestKitExportBuildOneProgramAndTheOtherIsNotInstalled(t *testing.T) {
	f := newExportFix(t)
	f.installBuild(t, exportSpec{program: kit.ProgramLlama})
	r := f.run(t, "kit", "export-build")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "No whisper.cpp build is in use on this computer, so there is nothing to export.") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestKitExportBuildWithNothingInstalled(t *testing.T) {
	f := newExportFix(t)
	r := f.run(t, "kit", "export-build")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT049+"]") || !strings.Contains(unwrap(r.errOut), "nothing to export") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	js := f.run(t, "--json", "kit", "export-build")
	var doc struct{ Error struct{ Code string } }
	if js.code != ExitFailed || json.Unmarshal([]byte(js.out), &doc) != nil || doc.Error.Code != doctor.KIT049 {
		t.Fatalf("exit %d out %q err %q", js.code, js.out, js.errOut)
	}
}

func TestKitExportBuildJSON(t *testing.T) {
	f := newExportFix(t)
	f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
	r := f.run(t, "--json", "kit", "export-build")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		Exports []struct {
			Program string `json:"program"`
			BuildID string `json:"build_id"`
			File    string `json:"file"`
			SHA256  string `json:"sha256"`
			Roots   []struct {
				Root    string `json:"root"`
				Written bool   `json:"written"`
			} `json:"roots"`
		} `json:"exports"`
		Skipped []struct{ Program, Reason string } `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		t.Fatalf("%v: %q", err, r.out)
	}
	if len(doc.Exports) != 1 || doc.Exports[0].Program != "whisper.cpp" || doc.Exports[0].BuildID != "1a2b3c4d5-cuda" ||
		len(doc.Exports[0].SHA256) != 64 || len(doc.Exports[0].Roots) != 1 || !doc.Exports[0].Roots[0].Written {
		t.Errorf("document = %+v", doc)
	}
	if len(doc.Skipped) != 1 || doc.Skipped[0].Program != "llama.cpp" {
		t.Errorf("skipped = %+v", doc.Skipped)
	}
}

func TestKitExportBuildFailures(t *testing.T) {
	tests := []struct {
		name string
		set  func(t *testing.T, f *kitFix)
		want string
	}{
		{"a program changed since BUILD.json", func(t *testing.T, f *kitFix) {
			dir := f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
			kitWrite(t, filepath.Join(dir, "bin", "whisper-cli"), "someone replaced this program")
		}, "cannot be exported: bin/whisper-cli is not the program BUILD.json describes."},
		{"a program is missing", func(t *testing.T, f *kitFix) {
			dir := f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
			_ = os.Remove(filepath.Join(dir, "bin", "whisper-server"))
		}, "bin/whisper-server is missing or not a file."},
		{"no content root can be written", func(t *testing.T, f *kitFix) {
			f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
			// kit/ is a file, so nothing can be created below it.
			_ = os.RemoveAll(f.kitDir)
			kitWrite(t, f.kitDir, "not a folder")
		}, "was not exported: no content root can be written"},
		{"the release of Ubuntu is unknown", func(t *testing.T, f *kitFix) {
			f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
			host := kitHostFn()
			kitWrite(t, host.OSRelease, "NAME=Mystery\n")
		}, "Ubuntu release is unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newExportFix(t)
			tc.set(t, f)
			r := f.run(t, "kit", "export-build")
			if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT049+"]") || !strings.Contains(unwrap(r.errOut), tc.want) {
				t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
			}
			if n := len(kit.ListPrebuilt(kit.OSFS{}, []string{f.env.Paths.InternalRoot}, "")); n != 0 {
				t.Errorf("%d exports exist after a failure", n)
			}
			var failed bool
			for _, l := range f.auditLines() {
				if l["action"] == "kit.export_build" && l["result"] == "fail" && l["code"] == doctor.KIT049 {
					failed = true
				}
			}
			if !failed && tc.name != "the release of Ubuntu is unknown" {
				t.Errorf("no failed audit line: %v", f.auditLines())
			}
		})
	}
}

func TestKitExportBuildNeedsRootAndTakesNoArguments(t *testing.T) {
	f := newExportFix(t)
	f.installBuild(t, exportSpec{program: kit.ProgramWhisper})
	if r := f.runAs(t, 1000, "kit", "export-build"); r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-006]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if r := f.run(t, "kit", "export-build", "now"); r.code != ExitUsage || !strings.Contains(r.errOut, "Unexpected argument") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if n := len(kit.ListPrebuilt(kit.OSFS{}, []string{f.env.Paths.InternalRoot}, "")); n != 0 {
		t.Errorf("%d exports exist after refused runs", n)
	}
}
