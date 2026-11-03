package kiwix

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// ent is one entry of a fixture tarball.
type ent struct {
	name string
	typ  byte // tar.TypeReg (default), TypeDir, TypeSymlink, TypeLink, TypeChar
	body string
	link string
	mode int64
}

// buildTar builds a gzip+tar archive in memory.
func buildTar(t testing.TB, ents []ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644 // the extractor, not the archive, decides the final mode
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// release is the shape of the real tarball: one top directory holding the three programs.
func release(top, tag string) []ent {
	pre := ""
	if top != "" {
		pre = top + "/"
	}
	var es []ent
	if top != "" {
		es = append(es, ent{name: top + "/", typ: tar.TypeDir, mode: 0o755})
	}
	for _, b := range Binaries {
		// mode 0644 in the archive on purpose: the installer must chmod 0755 itself.
		es = append(es, ent{name: pre + b, body: b + " " + tag, mode: 0o644})
	}
	return es
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// writeTarball writes data into dir and returns its path and sha256.
func writeTarball(t testing.TB, dir, name string, data []byte) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, shaHex(data)
}

// versionOut is what kiwix-serve --version prints (first line "kiwix-tools <v>") (the upstream number, no packaging revision).
func versionOut(v string) execx.Result {
	return execx.Result{Stdout: []byte("kiwix-tools " + v + "\n\nlibkiwix 13.1.0\nlibzim 9.2.0\n")}
}

// env is one test installation: a root under a temp dir, a fake runner, an Installer.
type env struct {
	t       *testing.T
	base    string // temp dir; root and the tarballs live in it
	root    string
	run     *fake.Runner
	inst    *Installer
	tarDir  string
	outside string // a file outside root that no install may touch
}

func newEnv(t *testing.T, fsys FS) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, base: base, root: filepath.Join(base, "opt", "kiwix-tools"), run: fake.New(), tarDir: filepath.Join(base, "kit")}
	if err := os.MkdirAll(e.tarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.outside = filepath.Join(base, "opt", "outside.txt")
	if err := os.MkdirAll(filepath.Dir(e.outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.inst = NewInstaller(Options{Root: e.root, FS: fsys, Runner: e.run, Arch: "amd64", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return e
}

// scriptVersion scripts the smoke test of directory dir (a version dir, its .tmp, or "current").
func (e *env) scriptVersion(dir, printed string) {
	e.run.Expect(fake.Key(filepath.Join(e.root, dir, BinServe), "--version"), versionOut(printed), nil)
}

// tarball writes a release tarball and returns its path and sha.
func (e *env) tarball(name string, ents []ent) (string, string) {
	return writeTarball(e.t, e.tarDir, name, buildTar(e.t, ents))
}

// target returns where current points ("" when there is no current).
func (e *env) target() string {
	e.t.Helper()
	l, err := os.Readlink(filepath.Join(e.root, CurrentName))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.root, rel))
	return err == nil
}

// assertClean checks that nothing was left behind or written outside the root.
func (e *env) assertClean(version string) {
	e.t.Helper()
	for _, n := range []string{version + ".tmp", version + ".bad", CurrentName + ".tmp"} {
		if e.exists(n) {
			e.t.Errorf("%s was left behind", n)
		}
	}
	b, err := os.ReadFile(e.outside)
	if err != nil || string(b) != "untouched" {
		e.t.Errorf("a file outside the root changed: %q %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(e.root))
	for _, en := range entries {
		if en.Name() != "kiwix-tools" && en.Name() != "outside.txt" {
			e.t.Errorf("unexpected entry next to the root: %s", en.Name())
		}
	}
}
