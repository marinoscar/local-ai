package untar

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	name, link, body string
	typ              byte
	mode             int64
}

func archive(t *testing.T, es ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The shape of python-build-standalone's install_only tarball: no directory entries, executables, and
// relative links in bin/ and between sibling folders.
func TestAProgramTreeWithLinks(t *testing.T) {
	dest := t.TempDir()
	data := archive(t,
		entry{name: "python/bin/python3.12", body: "#!elf", typ: tar.TypeReg, mode: 0o4755},
		entry{name: "python/bin/python3", link: "python3.12", typ: tar.TypeSymlink},
		entry{name: "python/bin/python", link: "python3.12", typ: tar.TypeSymlink},
		entry{name: "python/share/terminfo/a/adm", body: "x", typ: tar.TypeReg},
		entry{name: "python/share/terminfo/b/adm-link", link: "../a/adm", typ: tar.TypeSymlink},
		entry{name: "python/lib/", typ: tar.TypeDir, mode: 0o755},
	)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(data)
	_ = w.Close()
	src := filepath.Join(t.TempDir(), "py.tar.gz")
	if err := os.WriteFile(src, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := ExtractTarGz(context.Background(), src, dest, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 2 || st.Symlinks != 3 || st.Dirs != 1 {
		t.Errorf("stats = %+v", st)
	}
	fi, err := os.Stat(filepath.Join(dest, "python", "bin", "python3"))
	if err != nil || fi.Mode().Perm() != 0o755 || fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("python3 -> %v %v (setuid must be dropped)", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "python", "share", "terminfo", "b", "adm-link")); err != nil || string(b) != "x" {
		t.Errorf("sibling link: %q %v", b, err)
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string][]entry{
		"absolute name":            {{name: "/etc/passwd", body: "x", typ: tar.TypeReg}},
		"dot-dot name":             {{name: "python/../../x", body: "x", typ: tar.TypeReg}},
		"backslash":                {{name: `python\x`, body: "x", typ: tar.TypeReg}},
		"control character":        {{name: "python/\x1bx", body: "x", typ: tar.TypeReg}},
		"absolute link":            {{name: "python/l", link: "/etc", typ: tar.TypeSymlink}},
		"link out of the folder":   {{name: "python/l", link: "../../etc", typ: tar.TypeSymlink}},
		"hard link":                {{name: "python/a", body: "x", typ: tar.TypeReg}, {name: "python/b", link: "python/a", typ: tar.TypeLink}},
		"device":                   {{name: "python/dev", typ: tar.TypeChar}},
		"fifo":                     {{name: "python/fifo", typ: tar.TypeFifo}},
		"write through a link":     {{name: "python/d", link: ".", typ: tar.TypeSymlink}, {name: "python/d/x", body: "x", typ: tar.TypeReg}},
		"the same name twice":      {{name: "python/a", body: "x", typ: tar.TypeReg}, {name: "python/a", body: "y", typ: tar.TypeReg}},
		"a link replacing a file":  {{name: "python/a", body: "x", typ: tar.TypeReg}, {name: "python/a", link: "b", typ: tar.TypeSymlink}},
		"a file under a file name": {{name: "python/a", body: "x", typ: tar.TypeReg}, {name: "python/a/b", body: "y", typ: tar.TypeReg}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if _, err := Extract(context.Background(), bytes.NewReader(archive(t, es...)), dest, Limits{}); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	big := archive(t, entry{name: "a", body: "12345", typ: tar.TypeReg}, entry{name: "b", body: "12345", typ: tar.TypeReg})
	for name, lim := range map[string]Limits{"entries": {MaxEntries: 1}, "one file": {MaxFileBytes: 4}, "total": {MaxTotalBytes: 8}} {
		t.Run(name, func(t *testing.T) {
			if _, err := Extract(context.Background(), bytes.NewReader(big), t.TempDir(), lim); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := archive(t, entry{name: "a", body: "x", typ: tar.TypeReg})
	if _, err := Extract(ctx, bytes.NewReader(data), t.TempDir(), Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// The real interpreter tarball, when a kit or a spike copy is at hand (UNTAR_REAL_TARBALL=<path>).
func TestTheRealInterpreterTarball(t *testing.T) {
	src := os.Getenv("UNTAR_REAL_TARBALL")
	if src == "" {
		t.Skip("UNTAR_REAL_TARBALL is not set")
	}
	dest := t.TempDir()
	st, err := ExtractTarGz(context.Background(), src, dest, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", st)
	if fi, err := os.Stat(filepath.Join(dest, "python", "bin", "python3")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("python/bin/python3: %v %v", fi, err)
	}
}
