package build

import (
	"os"
	"path/filepath"
	"testing"
)

// chownRecorder is the real filesystem that records Chown instead of changing owners.
type chownRecorder struct {
	OSFS
	owned []string
}

func (f *chownRecorder) Chown(path string, uid, gid int) error {
	if uid == 1234 && gid == 2345 {
		f.owned = append(f.owned, path)
	}
	return nil
}

// whisper.cpp's configure step writes into its own source tree, so a scratch source tree is handed to the
// compile user with the build directory; llama.cpp's source (not scratch, root's) is left alone.
func TestHandOverGivesAScratchSourceTreeToTheCompileUser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scratch bool
		want    bool
	}{{"scratch tree (whisper.cpp)", true, true}, {"shared tree (llama.cpp)", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			out := filepath.Join(dir, "out")
			for _, d := range []string{filepath.Join(src, "bindings", "javascript"), filepath.Join(out, "bin")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(src, "bindings", "javascript", "package.json.in"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			fsys := &chownRecorder{}
			m := &Manager{opt: Options{
				FS: fsys, CompileUser: "localai", Recipe: Recipe{ScratchSource: tc.scratch},
				LookupUser: func(string) (int, int, error) { return 1234, 2345, nil },
				Lookup:     func(string) (string, error) { return "/usr/bin/setpriv", nil },
			}}
			r := &run{m: m, plan: Plan{OutDir: out, SourceDir: src}}
			if e := r.handOver(); e != nil {
				t.Fatal(e)
			}
			got := false
			for _, p := range fsys.owned {
				if p == filepath.Join(src, "bindings", "javascript", "package.json.in") {
					got = true
				}
			}
			if got != tc.want {
				t.Errorf("source file handed over = %v, want %v (owned: %v)", got, tc.want, fsys.owned)
			}
		})
	}
}
