package ark_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

// epoch is the one clock reading every test shares between what it serves and what it expects.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

func fixedNow() time.Time { return epoch }

// chownCall is one recorded Chown.
type chownCall struct {
	Path     string
	UID, GID int
}

// tfs is the ark.FS of the tests: a real temp directory behind config's fault-injecting FS, with a
// settable filesystem type per path prefix, per-path Mkdir failures and a recording Chown (so
// ownership is checked without needing root).
type tfs struct {
	*fake.FaultFS

	magic    map[string]uint64 // path prefix -> statfs magic (longest prefix wins)
	magicErr error             // FSType fails with this when set
	mkdirErr map[string]error  // path prefix -> error for Mkdir below it
	chowns   []chownCall
}

func newFS() *tfs {
	return &tfs{FaultFS: fake.New(config.OSFS{}), magic: map[string]uint64{}, mkdirErr: map[string]error{}}
}

func (f *tfs) Mkdir(name string, perm os.FileMode) error {
	for prefix, err := range f.mkdirErr {
		if name == prefix || strings.HasPrefix(name, prefix+string(filepath.Separator)) {
			return err
		}
	}
	return os.Mkdir(name, perm)
}

func (f *tfs) FSType(path string) (uint64, error) {
	if f.magicErr != nil {
		return 0, f.magicErr
	}
	best, found := "", false
	var magic uint64 = 0xEF53 // ext4
	for prefix, m := range f.magic {
		if (path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))) && (!found || len(prefix) > len(best)) {
			best, magic, found = prefix, m, true
		}
	}
	return magic, nil
}

func (f *tfs) Chown(name string, uid, gid int) error {
	f.chowns = append(f.chowns, chownCall{name, uid, gid})
	return nil
}

var _ ark.FS = (*tfs)(nil)

// writeFile writes a file with mode 0644, creating parents.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// fixture reads testdata/root/<name>.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "root", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// names lists the entry names of dir, sorted.
func names(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func mode(t testing.TB, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func codes(ps []ark.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

// noTemps fails if a ROOT.json temp file is left in dir.
func noTemps(t testing.TB, dir string) {
	t.Helper()
	for _, n := range names(t, dir) {
		if strings.HasPrefix(n, ark.RootJSONName+".tmp-") {
			t.Errorf("temp file %s left in %s", n, dir)
		}
	}
}
