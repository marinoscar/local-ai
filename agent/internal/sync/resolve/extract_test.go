package resolve

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// archEntry is one entry of a generated archive.
type archEntry struct {
	name  string
	typ   byte
	body  string
	link  string
	mode  int64
	major int64
}

func archTar(t *testing.T, entries ...archEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link, Devmajor: e.major, ModTime: fixedNow, Format: tar.FormatPAX}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func archGz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type archZipEntry struct {
	name string
	body string
	mode os.FileMode
}

func archZip(t *testing.T, entries ...archZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(e.name, "/") {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// extractTo writes data to a file and extracts it into a fresh sub-directory of a scratch directory, so
// anything an archive wrote above its destination would show up next to it.
func extractTo(t *testing.T, w *Web, data []byte) (scratch, dest string, st ExtractStats, err error) {
	t.Helper()
	scratch = t.TempDir()
	src := filepath.Join(scratch, "archive.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(scratch, "tree")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err = w.ExtractArchive(context.Background(), src, dest)
	return scratch, dest, st, err
}

func newExtractWeb() *Web { return &Web{Env: &Env{Online: func() bool { return true }}} }

// listTree returns the regular files and directories below dir as "path" and "path/".
func listTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		if !d.Type().IsRegular() {
			t.Errorf("%s is not a regular file", rel)
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var goodEntries = []archEntry{
	{name: "./", typ: tar.TypeDir},
	{name: "fonts/", typ: tar.TypeDir},
	{name: "fonts/Alpha Regular/0-255.pbf", body: "glyphs"},
	{name: "sprites/sprite.json", body: `{"a":1}`},
	{name: "sprites/sprite.png", body: "png", mode: 0o600},
	{name: "bin/run.sh", body: "#!/bin/sh\n", mode: 0o755},
	{name: "empty/", typ: tar.TypeDir},
}

func wantGoodTree(t *testing.T, dest string) {
	t.Helper()
	got := listTree(t, dest)
	delete(got, manifest.SumsName) // the sealed tree of a producer carries its SHA256SUMS
	want := map[string]string{
		"fonts/": "", "fonts/Alpha Regular/": "", "fonts/Alpha Regular/0-255.pbf": "glyphs",
		"sprites/": "", "sprites/sprite.json": `{"a":1}`, "sprites/sprite.png": "png",
		"bin/": "", "bin/run.sh": "#!/bin/sh\n", "empty/": "",
	}
	if len(got) != len(want) {
		t.Errorf("tree = %v", got)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %q (present %v), want %q", k, g, ok, v)
		}
	}
	// Modes are normalised: 0755 for an executable, 0644 for the rest (0600 is not kept).
	for rel, mode := range map[string]os.FileMode{"bin/run.sh": 0o755, "sprites/sprite.png": 0o644, "sprites/sprite.json": 0o644} {
		fi, err := os.Stat(filepath.Join(dest, rel))
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s mode = %v (%v), want %v", rel, fi.Mode().Perm(), err, mode)
		}
	}
}

// A normal tree is accepted in every format: .tar.gz, .tar and .zip, with its directories, an empty
// directory and an executable file, and nothing is stripped from the paths.
func TestExtractAcceptsANormalTree(t *testing.T) {
	w := newExtractWeb()
	raw := archTar(t, goodEntries...)
	for name, data := range map[string][]byte{"tar.gz": archGz(t, raw), "tar": raw} {
		t.Run(name, func(t *testing.T) {
			_, dest, st, err := extractTo(t, w, data)
			if err != nil {
				t.Fatal(err)
			}
			wantGoodTree(t, dest)
			if st.Files != 4 || st.Bytes != int64(len("glyphs")+len(`{"a":1}`)+len("png")+len("#!/bin/sh\n")) {
				t.Errorf("stats = %+v", st)
			}
		})
	}
	t.Run("zip", func(t *testing.T) {
		_, dest, _, err := extractTo(t, w, archZip(t,
			archZipEntry{name: "fonts/"},
			archZipEntry{name: "fonts/Alpha Regular/0-255.pbf", body: "glyphs"},
			archZipEntry{name: "sprites/sprite.json", body: `{"a":1}`},
			archZipEntry{name: "sprites/sprite.png", body: "png", mode: 0o600},
			archZipEntry{name: "bin/run.sh", body: "#!/bin/sh\n", mode: 0o755},
			archZipEntry{name: "empty/"},
		))
		if err != nil {
			t.Fatal(err)
		}
		wantGoodTree(t, dest)
	})
}

// .tar.xz goes through the xz program: with the real one when it is installed, and with a scripted one
// the wiring (argv, environment, exit status, a missing tool) is proved without it.
func TestExtractTarXz(t *testing.T) {
	t.Run("real xz", func(t *testing.T) {
		tools := needTool(t, "xz")
		scratch := t.TempDir()
		tarPath := filepath.Join(scratch, "a.tar")
		if err := os.WriteFile(tarPath, archTar(t, goodEntries...), 0o644); err != nil {
			t.Fatal(err)
		}
		runTool(t, []string{"PATH=" + execx.FixedPath}, tools["xz"], "-k", "--", tarPath)
		xzData, err := os.ReadFile(tarPath + ".xz")
		if err != nil {
			t.Fatal(err)
		}
		w := newExtractWeb()
		_, dest, _, err := extractTo(t, w, xzData)
		if err != nil {
			t.Fatal(err)
		}
		wantGoodTree(t, dest)

		// A truncated xz stream is an error, not a half tree taken as good.
		if _, _, _, err := extractTo(t, w, xzData[:len(xzData)-20]); err == nil {
			t.Error("a truncated .tar.xz was accepted")
		}
	})
	xzMagic := append([]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, make([]byte, 64)...)
	t.Run("scripted xz", func(t *testing.T) {
		st := fake.NewStreamer(fake.Script{Chunks: [][]byte{archTar(t, goodEntries...)}})
		w := &Web{Env: &Env{Lookup: func(string) (string, error) { return "/usr/bin/xz", nil }}, Streamer: st}
		scratch, dest, _, err := extractTo(t, w, xzMagic)
		if err != nil {
			t.Fatal(err)
		}
		wantGoodTree(t, dest)
		call := st.Calls()[0]
		if call.Path != "/usr/bin/xz" || strings.Join(call.Args, " ") != "-dc --memlimit-decompress=2GiB -- "+filepath.Join(scratch, "archive.bin") {
			t.Errorf("xz was started as %s %v", call.Path, call.Args)
		}
		for _, e := range call.Env {
			if strings.HasPrefix(e, "HF") || strings.Contains(e, "TOKEN") {
				t.Errorf("environment %q", e)
			}
		}
	})
	t.Run("xz fails", func(t *testing.T) {
		st := fake.NewStreamer(fake.Script{ExitCode: 1, Stderr: "xz: the file is corrupt"})
		w := &Web{Env: &Env{Lookup: func(string) (string, error) { return "/usr/bin/xz", nil }}, Streamer: st}
		_, _, _, err := extractTo(t, w, xzMagic)
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no xz", func(t *testing.T) {
		w := &Web{Env: &Env{Lookup: func(string) (string, error) { return "", execx.ErrNotFound }}}
		_, _, _, err := extractTo(t, w, xzMagic)
		if CodeOf(err) != doctor.SYN061 || !errors.Is(err, ErrNoXZ) {
			t.Errorf("err = %v", err)
		}
	})
}

// Every unsafe entry refuses the whole archive, with ErrUnsafeArchive, and nothing is written outside the
// destination: the scratch directory next to it holds the archive and the destination only.
func TestExtractRejectsUnsafeTarEntries(t *testing.T) {
	cases := map[string][]archEntry{
		"dot dot":           {{name: "../x", body: "x"}},
		"dot dot deep":      {{name: "a/b/../../../x", body: "x"}},
		"dot dot dir":       {{name: "../", typ: tar.TypeDir}},
		"absolute":          {{name: "/etc/x", body: "x"}},
		"absolute dir":      {{name: "/", typ: tar.TypeDir}},
		"symlink to root":   {{name: "link", typ: tar.TypeSymlink, link: "/"}},
		"symlink out":       {{name: "a/link", typ: tar.TypeSymlink, link: "../../x"}},
		"symlink in":        {{name: "a", body: "x"}, {name: "link", typ: tar.TypeSymlink, link: "a"}},
		"hard link":         {{name: "a", body: "x"}, {name: "hard", typ: tar.TypeLink, link: "a"}},
		"hard link out":     {{name: "hard", typ: tar.TypeLink, link: "/etc/passwd"}},
		"char device":       {{name: "null", typ: tar.TypeChar, major: 1}},
		"block device":      {{name: "sda", typ: tar.TypeBlock, major: 8}},
		"fifo":              {{name: "pipe", typ: tar.TypeFifo}},
		"backslash":         {{name: `a\..\x`, body: "x"}},
		"control character": {{name: "a\x01b", body: "x"}},
		"after good files":  {{name: "ok.txt", body: "fine"}, {name: "../escape", body: "x"}},
	}
	w := newExtractWeb()
	for name, entries := range cases {
		for _, gz := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/gz", false: "/tar"}[gz], func(t *testing.T) {
				data := archTar(t, entries...)
				if gz {
					data = archGz(t, data)
				}
				scratch, _, _, err := extractTo(t, w, data)
				if !errors.Is(err, ErrUnsafeArchive) {
					t.Fatalf("err = %v, want ErrUnsafeArchive", err)
				}
				ents, _ := os.ReadDir(scratch)
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				if len(names) != 2 {
					t.Errorf("the scratch directory holds %v: something was written outside the destination", names)
				}
				if _, err := os.Lstat(filepath.Join(filepath.Dir(scratch), "x")); err == nil {
					t.Error("a file was written above the scratch directory")
				}
			})
		}
	}
}

func TestExtractRejectsUnsafeZipEntries(t *testing.T) {
	cases := map[string][]archZipEntry{
		"dot dot":         {{name: "../x", body: "x"}},
		"dot dot nested":  {{name: "a/../../x", body: "x"}},
		"absolute":        {{name: "/etc/x", body: "x"}},
		"backslash":       {{name: `..\x`, body: "x"}},
		"symlink":         {{name: "link", body: "/", mode: os.ModeSymlink | 0o777}},
		"symlink in tree": {{name: "a", body: "x"}, {name: "link", body: "a", mode: os.ModeSymlink | 0o777}},
		"device":          {{name: "dev", body: "", mode: os.ModeDevice | os.ModeCharDevice | 0o666}},
		"fifo":            {{name: "pipe", body: "", mode: os.ModeNamedPipe | 0o666}},
		"after good":      {{name: "ok.txt", body: "fine"}, {name: "../escape", body: "x"}},
	}
	w := newExtractWeb()
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			scratch, _, _, err := extractTo(t, w, archZip(t, entries...))
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("err = %v, want ErrUnsafeArchive", err)
			}
			ents, _ := os.ReadDir(scratch)
			if len(ents) != 2 {
				t.Errorf("the scratch directory holds %d entries: something was written outside the destination", len(ents))
			}
		})
	}
}

// A file cannot be written through a directory that is also a file, or over a directory.
func TestExtractRejectsCollisions(t *testing.T) {
	w := newExtractWeb()
	for name, entries := range map[string][]archEntry{
		"file then dir": {{name: "a", body: "x"}, {name: "a/b", body: "y"}},
		"dir then file": {{name: "a/", typ: tar.TypeDir}, {name: "a", body: "y"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := extractTo(t, w, archTar(t, entries...)); !errors.Is(err, ErrUnsafeArchive) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// The bounds stop a bomb at the limit.
func TestExtractLimits(t *testing.T) {
	big := strings.Repeat("a", 100)
	data := archTar(t, archEntry{name: "a", body: big}, archEntry{name: "b", body: big}, archEntry{name: "c", body: big})
	for name, lim := range map[string]ExtractLimits{
		"entries": {MaxEntries: 2},
		"file":    {MaxFileBytes: 99},
		"total":   {MaxTotalBytes: 250},
	} {
		t.Run(name, func(t *testing.T) {
			w := newExtractWeb()
			w.Limits = lim
			if _, _, _, err := extractTo(t, w, data); !errors.Is(err, ErrUnsafeArchive) {
				t.Errorf("err = %v", err)
			}
			w2 := newExtractWeb()
			w2.Limits = lim
			zdata := archZip(t, archZipEntry{name: "a", body: big}, archZipEntry{name: "b", body: big}, archZipEntry{name: "c", body: big})
			if _, _, _, err := extractTo(t, w2, zdata); !errors.Is(err, ErrUnsafeArchive) {
				t.Errorf("zip: err = %v", err)
			}
		})
	}
	// A zip whose header lies about the size is caught when the bytes are counted.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "a", Method: zip.Store}
	w, _ := zw.CreateHeader(h)
	_, _ = w.Write([]byte("0123456789"))
	_ = zw.Close()
	lying := bytes.Replace(buf.Bytes(), []byte{10, 0, 0, 0, 10, 0, 0, 0}, []byte{4, 0, 0, 0, 4, 0, 0, 0}, -1)
	if _, _, _, err := extractTo(t, newExtractWeb(), lying); err == nil {
		t.Error("a zip with a lying size was accepted")
	}
}

// Not an archive, and a cancelled context.
func TestExtractOtherFailures(t *testing.T) {
	w := newExtractWeb()
	if _, _, _, err := extractTo(t, w, []byte("just some text, not an archive")); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("text: %v", err)
	}
	if _, _, _, err := extractTo(t, w, append([]byte{0x1f, 0x8b}, 1, 2, 3)); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("bad gzip: %v", err)
	}
	if _, _, _, err := extractTo(t, w, []byte("PK\x03\x04junk")); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("bad zip: %v", err)
	}
	scratch := t.TempDir()
	src := filepath.Join(scratch, "a.tgz")
	if err := os.WriteFile(src, archGz(t, archTar(t, goodEntries...)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.ExtractArchive(ctx, src, scratch); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
}

// archiveEntryPath on its own.
func TestArchiveEntryPath(t *testing.T) {
	ok := map[string]string{"a": "a", "a/b/": "a/b", "./a/./b": "a/b", "a//b": "a/b", "./": "", ".": "", "dir/": "dir", "a b/c d.txt": "a b/c d.txt", "...": "..."}
	for in, want := range ok {
		if got, err := archiveEntryPath(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/", "/a", "../a", "a/..", "a/../b", "a\\b", "a\x00b", "a\nb", "..", "../"} {
		if _, err := archiveEntryPath(in); !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%q accepted (%v)", in, err)
		}
	}
}
