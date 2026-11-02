package local

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// testNow is the fixed clock of every test: backups are named from it.
var testNow = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

// testEnv builds an Env over a temp directory standing in for /etc/localai, /var/lib/localai,
// /srv/localai and /etc/systemd/system. Only root may chown to another owner, so the "root" and the
// groups of the tests are the user running them. The runner is a fake that records argv.
func testEnv(t *testing.T) (Env, *fake.Runner) {
	t.Helper()
	root := t.TempDir()
	p := Paths{
		EtcDir:       filepath.Join(root, "etc", "localai"),
		StateDir:     filepath.Join(root, "var", "lib", "localai"),
		InternalRoot: filepath.Join(root, "srv", "localai"),
		SystemdDir:   filepath.Join(root, "etc", "systemd", "system"),
		Mountinfo:    filepath.Join(root, "mountinfo"),
	}
	for _, d := range []string{p.EtcDir, p.StateDir, p.SystemdDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := fake.New()
	gid := os.Getgid()
	return Env{
		Paths: p, RootUID: os.Getuid(), RootGID: gid,
		LookupGroup:  func(string) (int, error) { return gid, nil },
		LookupUser:   func(string) (int, error) { return os.Getuid(), nil },
		GroupName:    func(int) string { return "testgroup" },
		UserName:     func(int) string { return "testowner" },
		Now:          func() time.Time { return testNow },
		Runner:       run,
		LookPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		AgentVersion: "0.0.0-test",
	}, run
}

// write creates path (and its folders) with content and mode.
func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // the umask must not change it
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// shaToken: the repository's redaction check rejects any 64-hex string outside its fixtures, so the
// catalog testdata writes a sha256 as @sha:<hex digit>@.
var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// writeCatalog writes a valid catalog with the given version into dir (the testdata of package
// catalog), and returns dir.
func writeCatalog(t *testing.T, dir, version string) string {
	t.Helper()
	src := filepath.Join("..", "..", "catalog", "testdata", "valid")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := shaToken.ReplaceAllStringFunc(string(b), func(m string) string {
			return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
		})
		return os.WriteFile(dst, []byte(s), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CATALOG_VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// listFiles returns every file under dir as relative slash paths.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}
