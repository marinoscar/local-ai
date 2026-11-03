package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- helpers -----------------------------------------------------------------------------

type tarEntry struct {
	name     string
	typ      byte
	body     string
	linkname string
	mode     int64
}

func makeTar(t *testing.T, entries []tarEntry, gz bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w interface {
		Write([]byte) (int, error)
		Close() error
	} = nopCloser{&buf}
	if gz {
		w = gzip.NewWriter(&buf)
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.linkname}
		if typ == tar.TypeXGlobalHeader {
			hdr = &tar.Header{Name: e.name, Typeflag: typ, PAXRecords: map[string]string{"comment": "0123456789"}}
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

// treeTar is a minimal llama.cpp tree as git archive writes it: a pax header with the commit, no
// leading directory.
func treeTar(t *testing.T) []byte {
	return makeTar(t, []tarEntry{
		{name: "pax_global_header", typ: tar.TypeXGlobalHeader},
		{name: "CMakeLists.txt", body: "project(llama)\n"},
		{name: "ggml/", typ: tar.TypeDir},
		{name: "ggml/src/ggml.c", body: "int x;\n"},
		{name: "scripts/run.sh", body: "#!/bin/sh\n", mode: 0o755},
	}, false)
}

// stubSource is a Source scripted by a test.
type stubSource struct {
	name    string
	network bool
	sha     string
	err     error
	noTree  bool
	calls   *[]string
	hook    func(dest string)
}

func (s stubSource) Name() string  { return s.name }
func (s stubSource) Network() bool { return s.network }
func (s stubSource) Fetch(_ context.Context, env SourceEnv, ref, dest string) (string, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, s.name+":"+ref)
	}
	if s.hook != nil {
		s.hook(dest)
	}
	if s.err != nil {
		return "", s.err
	}
	if !s.noTree {
		if err := os.WriteFile(filepath.Join(dest, "CMakeLists.txt"), []byte("project(x)\n"), 0o644); err != nil {
			return "", err
		}
	}
	return s.sha, nil
}

// gitSub splits a git argv into the repository it works in, the subcommand and what follows.
func gitSub(args []string) (dir, sub string, rest []string) {
	i := 0
	for i+1 < len(args) {
		switch args[i] {
		case "-c":
			i += 2
			continue
		case "-C":
			dir = args[i+1]
			i += 2
			continue
		}
		break
	}
	if i < len(args) {
		return dir, args[i], args[i+1:]
	}
	return dir, "", nil
}

// fakeGit answers the git commands of the sources. clone writes a tree and a .git directory.
type fakeGit struct {
	sha        string
	cloneFail  bool
	checkoutRC int
	haveCommit bool // the repository of an archive source has the commit
	archive    []byte
	calls      []execx.Cmd
}

func (g *fakeGit) handler(c execx.Cmd) (execx.Result, error, bool) {
	if filepath.Base(c.Path) != "git" {
		return execx.Result{}, nil, false
	}
	g.calls = append(g.calls, c)
	dir, sub, rest := gitSub(c.Args)
	switch sub {
	case "clone":
		if g.cloneFail {
			return exitWith(128, "fatal: unable to access: Could not resolve host: github.com"), nil, true
		}
		dest := rest[len(rest)-1]
		_ = os.MkdirAll(filepath.Join(dest, ".git"), 0o755)
		_ = os.WriteFile(filepath.Join(dest, ".git", "HEAD"), []byte("ref"), 0o644)
		_ = os.WriteFile(filepath.Join(dest, "CMakeLists.txt"), []byte("project(llama)\n"), 0o644)
		return execx.Result{}, nil, true
	case "checkout":
		return execx.Result{ExitCode: g.checkoutRC, Stderr: []byte("error: pathspec did not match")}, nil, true
	case "rev-parse":
		if len(rest) == 1 && rest[0] == "HEAD" {
			return okOut(g.sha + "\n"), nil, true
		}
		if g.haveCommit {
			return okOut(g.sha + "\n"), nil, true
		}
		_ = dir
		return execx.Result{ExitCode: 1}, nil, true
	case "archive":
		out := ""
		for i, a := range rest {
			if a == "-o" {
				out = rest[i+1]
			}
		}
		if err := os.WriteFile(out, g.archive, 0o644); err != nil {
			return execx.Result{}, err, true
		}
		return execx.Result{}, nil, true
	}
	return execx.Result{}, nil, false
}

func (g *fakeGit) lines() []string {
	var out []string
	for _, c := range g.calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func sourceHarness(t *testing.T, opts ...hopt) *harness {
	h := newHarness(t, append([]hopt{withoutSource()}, opts...)...)
	h.req.NoActivate = true
	return h
}

// ---- the chain ---------------------------------------------------------------------------

func TestExtractedTreeIsUsedWithoutFetching(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if h.exec.ran("git") {
		t.Error("git ran although the tree is already extracted")
	}
	fi, err := os.Stat(filepath.Join(h.root, "src", testSHA))
	if err != nil || !fi.ModTime().Equal(testNow) {
		t.Errorf("the tree is not marked as used now: %v %v", fi, err)
	}
}

func TestShortCommitFindsTheExtractedTree(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	h.req.Commit = testSHA[:7]
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != testID || res.BuildJSON.Commit != testSHA {
		t.Errorf("the build id and commit come from the full sha: %+v", res.BuildJSON)
	}
}

func TestExistingTree(t *testing.T) {
	h := newHarness(t)
	other := testSHA[:7] + strings.Repeat("0", 33)
	h.write(filepath.Join(h.root, "src", other, "CMakeLists.txt"), "x")
	h.write(filepath.Join(h.root, "src", strings.Repeat("9", 40), "README"), "no marker")
	m := h.manager()
	tests := []struct {
		name, ref, want string
	}{
		{"full sha", testSHA, testSHA},
		{"unknown full sha", strings.Repeat("e", 40), ""},
		{"ambiguous short sha", testSHA[:7], ""},
		{"unique short sha", testSHA[:8], testSHA},
		{"tree without CMakeLists.txt", strings.Repeat("9", 7), ""},
		{"no tree", "1234567", ""},
	}
	for _, tc := range tests {
		if got := m.existingTree(tc.ref); got != tc.want {
			t.Errorf("%s: existingTree(%q) = %q, want %q", tc.name, tc.ref, got, tc.want)
		}
	}
}

func TestOfflineWithoutASourceIsKitError002(t *testing.T) {
	tests := []struct {
		name    string
		sources []Source
	}{
		{"default chain (only the network source, skipped)", nil},
		{"empty chain", []Source{}},
		{"only a source that does not have it", []Source{stubSource{name: "kit", err: ErrNotAvailable}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := sourceHarness(t)
			g := &fakeGit{}
			h.exec.on(g.handler)
			h.opts.Sources = tc.sources
			h.req.Offline = true
			_, err := h.build()
			e := mustErr(t, err)
			if e.Code != doctor.KIT002 || e.Code == doctor.LLM018 || e.Stage != StageSource || e.Details["cause"] != "no_source" {
				t.Fatalf("got %s %v; offline with no source must be LAI-KIT-002, never LAI-LLM-018", e.Code, e)
			}
			if len(g.calls) != 0 || h.exec.ran("cmake") {
				t.Error("nothing may run offline without a source")
			}
			if h.exists("builds/" + testID + ".tmp") {
				t.Error("no build directory may be made without a source")
			}
		})
	}
}

func TestOnlineCloneProducesAPlainTree(t *testing.T) {
	h := sourceHarness(t)
	g := &fakeGit{sha: testSHA}
	h.exec.on(g.handler)
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != testID {
		t.Fatal(res.BuildID)
	}
	tmp := filepath.Join(h.root, "src", testSHA+".tmp")
	want := []string{
		"clone --filter=blob:none https://github.com/ggml-org/llama.cpp.git " + tmp,
		"-C " + tmp + " checkout " + testSHA,
		"-C " + tmp + " rev-parse HEAD",
	}
	if got := g.lines(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("git calls\n got %q\nwant %q", got, want)
	}
	if !h.exists("src/"+testSHA+"/CMakeLists.txt") || h.exists("src/"+testSHA+"/.git") || h.exists("src/"+testSHA+".tmp") {
		t.Error("the tree must be in src/<sha>/ without .git and without the .tmp directory")
	}
	for _, c := range g.calls {
		if !strings.Contains(strings.Join(c.Env, " "), "GIT_TERMINAL_PROMPT=0") || filepath.Base(c.Path) != "git" || !filepath.IsAbs(c.Path) {
			t.Errorf("git is run with %v %v", c.Path, c.Env)
		}
	}
}

func TestOnlineShortCommitResolvesToTheFullSha(t *testing.T) {
	h := sourceHarness(t)
	g := &fakeGit{sha: testSHA}
	h.exec.on(g.handler)
	h.req.Commit = testSHA[:9]
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildJSON.Commit != testSHA || !h.exists("src/"+testSHA+"/CMakeLists.txt") {
		t.Errorf("%+v", res.BuildJSON)
	}
}

func TestOnlineCloneFailureIsABuildFailureNotAMissingKit(t *testing.T) {
	h := sourceHarness(t)
	g := &fakeGit{cloneFail: true}
	h.exec.on(g.handler)
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "source_failed" || !strings.Contains(e.Message, "Could not resolve host") {
		t.Fatalf("%+v", e)
	}
	if h.exists("src/"+testSHA+".tmp") || h.exec.ran("cmake") {
		t.Error("a failed clone must leave no temporary tree and start no build")
	}
}

func TestOnlineUnknownCommit(t *testing.T) {
	h := sourceHarness(t)
	g := &fakeGit{checkoutRC: 1}
	h.exec.on(g.handler)
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 || !strings.Contains(e.Message, "checking out") {
		t.Fatalf("%+v", e)
	}
}

func TestGitIsNeededByTheCloneSource(t *testing.T) {
	h := sourceHarness(t)
	h.tools["git"] = false
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 || !strings.Contains(e.Message, "git is needed") {
		t.Fatalf("%+v", e)
	}
}

func TestChainOrderAndFallThrough(t *testing.T) {
	var calls []string
	h := sourceHarness(t)
	h.opts.Sources = []Source{
		stubSource{name: "first", err: fmt.Errorf("%w: nope", ErrNotAvailable), calls: &calls},
		stubSource{name: "second", sha: testSHA, calls: &calls},
		stubSource{name: "third", sha: testSHA, calls: &calls},
	}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != fmt.Sprintf("[first:%s second:%s]", testSHA, testSHA) {
		t.Errorf("calls = %v", calls)
	}
}

func TestSourceThatFailsHardThenAnotherDelivers(t *testing.T) {
	var calls []string
	h := sourceHarness(t)
	h.opts.Sources = []Source{
		stubSource{name: "broken", err: errors.New("corrupt archive"), calls: &calls},
		stubSource{name: "good", sha: testSHA, calls: &calls},
	}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Errorf("calls = %v", calls)
	}
}

func TestSourceThatFailsHardAndNoOtherHasItIsABuildFailure(t *testing.T) {
	h := sourceHarness(t)
	h.opts.Sources = []Source{
		stubSource{name: "broken", err: errors.New("corrupt archive")},
		stubSource{name: "kit", err: ErrNotAvailable},
	}
	h.req.Offline = true
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "source_failed" || !strings.Contains(e.Message, "corrupt archive") {
		t.Fatalf("a source that has the commit but cannot deliver it is not 'the kit lacks it': %+v", e)
	}
}

func TestBadSourceAnswers(t *testing.T) {
	tests := []struct {
		name string
		src  stubSource
		want string
	}{
		{"another commit", stubSource{name: "s", sha: strings.Repeat("1", 40)}, "answered the commit"},
		{"not a sha", stubSource{name: "s", sha: "main"}, "answered the commit"},
		{"no tree", stubSource{name: "s", sha: testSHA, noTree: true}, "without CMakeLists.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := sourceHarness(t)
			h.opts.Sources = []Source{tc.src}
			_, err := h.build()
			e := mustErr(t, err)
			if e.Code != doctor.LLM018 || !strings.Contains(e.Message, tc.want) {
				t.Fatalf("%+v", e)
			}
			if h.exists("src/" + testSHA + ".tmp") {
				t.Error("temporary tree left behind")
			}
		})
	}
}

func TestIncompleteTreeIsReplaced(t *testing.T) {
	h := sourceHarness(t)
	h.write(filepath.Join(h.root, "src", testSHA, "half-extracted.c"), "x")
	h.opts.Sources = []Source{stubSource{name: "kit", sha: testSHA}}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if h.exists("src/"+testSHA+"/half-extracted.c") || !h.exists("src/"+testSHA+"/CMakeLists.txt") {
		t.Error("an incomplete tree must be removed before the new one is put in place")
	}
}

func TestRequestSourcesReplaceTheDefaultChain(t *testing.T) {
	var calls []string
	h := sourceHarness(t)
	h.opts.Sources = []Source{stubSource{name: "default", sha: testSHA, calls: &calls}}
	h.req.Sources = []Source{stubSource{name: "kit", sha: testSHA, calls: &calls}}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "kit:") {
		t.Errorf("calls = %v", calls)
	}
}

func TestOfflineSkipsNetworkSources(t *testing.T) {
	var calls []string
	h := sourceHarness(t)
	h.opts.Sources = []Source{
		stubSource{name: "net", network: true, sha: testSHA, calls: &calls},
		stubSource{name: "local", sha: testSHA, calls: &calls},
	}
	h.req.Offline = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "local:") {
		t.Errorf("calls = %v", calls)
	}
}

func TestCancelledFetchIsACancellation(t *testing.T) {
	h := sourceHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.opts.Sources = []Source{stubSource{name: "slow", err: errors.New("interrupted"), hook: func(string) { cancel() }}}
	_, err := h.manager().Build(ctx, h.req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if h.exists("src/" + testSHA + ".tmp") {
		t.Error("temporary tree left behind")
	}
}

// ---- git archive -------------------------------------------------------------------------

func TestGitArchiveSource(t *testing.T) {
	h := sourceHarness(t)
	repo := filepath.Join(t.TempDir(), "llama.cpp.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	g := &fakeGit{sha: testSHA, haveCommit: true, archive: treeTar(t)}
	h.exec.on(g.handler)
	h.opts.Sources = []Source{GitArchiveSource{Repo: repo, Label: "kit-mirror"}}
	h.req.Offline = true
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != testID {
		t.Fatal(res.BuildID)
	}
	tmp := filepath.Join(h.root, "src", testSHA+".tmp")
	pre := "-c safe.directory=" + repo + " -C " + repo + " "
	want := []string{
		pre + "rev-parse --verify --quiet " + testSHA + "^{commit}",
		pre + "archive --format=tar -o " + tmp + ".tar " + testSHA,
	}
	if got := g.lines(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("git calls\n got %q\nwant %q", got, want)
	}
	for _, rel := range []string{"CMakeLists.txt", "ggml/src/ggml.c", "scripts/run.sh"} {
		if !h.exists("src/" + testSHA + "/" + rel) {
			t.Errorf("%s was not extracted", rel)
		}
	}
	if fi, _ := os.Stat(filepath.Join(h.root, "src", testSHA, "scripts", "run.sh")); fi == nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the executable bit is lost: %v", fi)
	}
	if h.exists("src/"+testSHA+".tmp.tar") || h.exists("src/"+testSHA+".tmp") {
		t.Error("temporary files left behind")
	}
}

func TestGitArchiveSourceNotAvailable(t *testing.T) {
	t.Run("repository missing", func(t *testing.T) {
		h := sourceHarness(t)
		g := &fakeGit{}
		h.exec.on(g.handler)
		h.opts.Sources = []Source{GitArchiveSource{Repo: filepath.Join(t.TempDir(), "absent.git")}}
		h.req.Offline = true
		_, err := h.build()
		if e := mustErr(t, err); e.Code != doctor.KIT002 {
			t.Fatalf("%+v", e)
		}
		if len(g.calls) != 0 {
			t.Error("git ran for a repository that does not exist")
		}
	})
	t.Run("repository without the commit", func(t *testing.T) {
		h := sourceHarness(t)
		repo := t.TempDir()
		g := &fakeGit{haveCommit: false}
		h.exec.on(g.handler)
		h.opts.Sources = []Source{GitArchiveSource{Repo: repo}}
		h.req.Offline = true
		_, err := h.build()
		if e := mustErr(t, err); e.Code != doctor.KIT002 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a repository that has it but whose archive fails", func(t *testing.T) {
		h := sourceHarness(t)
		repo := t.TempDir()
		g := &fakeGit{sha: testSHA, haveCommit: true}
		h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
			if _, sub, _ := gitSub(c.Args); sub == "archive" {
				return exitWith(128, "fatal: not a tree object"), nil, true
			}
			return g.handler(c)
		})
		h.opts.Sources = []Source{GitArchiveSource{Repo: repo}}
		h.req.Offline = true
		_, err := h.build()
		if e := mustErr(t, err); e.Code != doctor.LLM018 || !strings.Contains(e.Message, "not a tree object") {
			t.Fatalf("%+v", e)
		}
	})
}

// ---- a directory the operator names ------------------------------------------------------

func TestDirSourceCopiesAPlainTree(t *testing.T) {
	h := sourceHarness(t)
	dir := t.TempDir()
	h.write(filepath.Join(dir, "CMakeLists.txt"), "project(llama)\n")
	h.write(filepath.Join(dir, "ggml", "a.c"), "int a;\n")
	h.write(filepath.Join(dir, "vendor", ".git"), "gitdir: elsewhere")
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ggml/a.c", filepath.Join(dir, "link-in")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link-abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(dir, "ggml", "link-out")); err != nil {
		t.Fatal(err)
	}
	h.req.SourceDir = dir
	h.req.Commit = testSHA
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(h.root, "src", testSHA)
	for _, rel := range []string{"CMakeLists.txt", "ggml/a.c", "run.sh", "link-in"} {
		if _, err := os.Lstat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	for _, rel := range []string{"vendor/.git", "link-abs", "ggml/link-out"} {
		if _, err := os.Lstat(filepath.Join(dst, rel)); err == nil {
			t.Errorf("%s must not be copied", rel)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dst, "run.sh")); fi == nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode: %v", fi)
	}
	if h.exec.ran("git") {
		t.Error("a tree without .git needs no git")
	}
}

func TestDirSourceReplacesTheTreeThatIsThere(t *testing.T) {
	h := newHarness(t) // the tree is already extracted
	h.req.NoActivate = true
	dir := t.TempDir()
	h.write(filepath.Join(dir, "CMakeLists.txt"), "project(patched)\n")
	h.req.SourceDir = dir
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(h.root, "src", testSHA, "CMakeLists.txt")); got != "project(patched)\n" {
		t.Errorf("the named directory did not replace the extracted tree: %q", got)
	}
}

func TestDirSourcePointingAtTheCanonicalTree(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	h.req.SourceDir = filepath.Join(h.root, "src", testSHA)
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if !h.exists("src/" + testSHA + "/CMakeLists.txt") {
		t.Error("the canonical tree was destroyed by copying onto itself")
	}
}

func TestDirSourceErrors(t *testing.T) {
	t.Run("not a directory", func(t *testing.T) {
		h := sourceHarness(t)
		h.req.SourceDir = filepath.Join(t.TempDir(), "absent")
		_, err := h.build()
		if e := mustErr(t, err); e.Code != doctor.LLM018 || !strings.Contains(e.Message, "is not a directory") {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("not a llama.cpp tree", func(t *testing.T) {
		h := sourceHarness(t)
		h.req.SourceDir = t.TempDir()
		_, err := h.build()
		if e := mustErr(t, err); !strings.Contains(e.Message, "no CMakeLists.txt") {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("short commit and no git history", func(t *testing.T) {
		h := sourceHarness(t)
		dir := t.TempDir()
		h.write(filepath.Join(dir, "CMakeLists.txt"), "x")
		h.req.SourceDir = dir
		h.req.Commit = testSHA[:9]
		_, err := h.build()
		if e := mustErr(t, err); !strings.Contains(e.Message, "full 40-character sha") {
			t.Fatalf("%+v", e)
		}
	})
}

func TestDirSourceWithGitHistoryUsesGitArchive(t *testing.T) {
	h := sourceHarness(t)
	dir := t.TempDir()
	h.write(filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")
	g := &fakeGit{sha: testSHA, haveCommit: true, archive: treeTar(t)}
	h.exec.on(g.handler)
	h.req.SourceDir = dir
	h.req.Commit = testSHA[:9]
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildJSON.Commit != testSHA || len(g.calls) != 2 {
		t.Errorf("%+v %v", res.BuildJSON, g.lines())
	}
	if !strings.Contains(g.lines()[0], "-c safe.directory="+dir) {
		t.Errorf("git does not trust the directory explicitly: %v", g.lines()[0])
	}

	// The commit is not in that repository: the operator asked for this directory, so it is an error.
	h = sourceHarness(t)
	h.write(filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")
	h.exec.on((&fakeGit{haveCommit: false}).handler)
	h.req.SourceDir = dir
	_, err = h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 || !strings.Contains(e.Message, "is not in "+dir) {
		t.Fatalf("%+v", e)
	}
}

// ---- tar extraction ----------------------------------------------------------------------

func TestExtractTar(t *testing.T) {
	entries := []tarEntry{
		{name: "pax_global_header", typ: tar.TypeXGlobalHeader},
		{name: "llama.cpp-x/", typ: tar.TypeDir},
		{name: "llama.cpp-x/CMakeLists.txt", body: "project()\n"},
		{name: "llama.cpp-x/sub/f.c", body: "int f;\n", mode: 0o755},
		{name: "llama.cpp-x/sub/link", typ: tar.TypeSymlink, linkname: "f.c"},
		{name: "llama.cpp-x/sub/up", typ: tar.TypeSymlink, linkname: "../CMakeLists.txt"},
	}
	for _, gz := range []bool{false, true} {
		t.Run(fmt.Sprint("gzip ", gz), func(t *testing.T) {
			dest := t.TempDir()
			if err := extractTar(OSFS{}, bytes.NewReader(makeTar(t, entries, gz)), dest, 1); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(dest, "sub", "link")); got != "int f;\n" {
				t.Errorf("link: %q", got)
			}
			if got := readFile(t, filepath.Join(dest, "sub", "up")); got != "project()\n" {
				t.Errorf("up link: %q", got)
			}
			if fi, _ := os.Stat(filepath.Join(dest, "sub", "f.c")); fi == nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("mode: %v", fi)
			}
			if _, err := os.Stat(filepath.Join(dest, "pax_global_header")); err == nil {
				t.Error("the pax header was extracted as a file")
			}
		})
	}
}

func TestExtractTarRefusesUnsafeArchives(t *testing.T) {
	tests := []struct {
		name  string
		entry tarEntry
		want  string
	}{
		{"parent directory", tarEntry{name: "../evil", body: "x"}, "unsafe path"},
		{"nested parent", tarEntry{name: "a/../../evil", body: "x"}, "unsafe path"},
		{"absolute", tarEntry{name: "/etc/evil", body: "x"}, "unsafe path"},
		{"absolute link", tarEntry{name: "l", typ: tar.TypeSymlink, linkname: "/etc/passwd"}, "points outside"},
		{"escaping link", tarEntry{name: "d/l", typ: tar.TypeSymlink, linkname: "../../x"}, "points outside"},
		{"hard link", tarEntry{name: "h", typ: tar.TypeLink, linkname: "CMakeLists.txt"}, "unsupported"},
		{"device", tarEntry{name: "dev", typ: tar.TypeChar}, "unsupported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "tree")
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			err := extractTar(OSFS{}, bytes.NewReader(makeTar(t, []tarEntry{tc.entry}, false)), dest, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil")); err == nil {
				t.Error("a file was written outside the destination")
			}
		})
	}
	t.Run("not a tar", func(t *testing.T) {
		if err := extractTar(OSFS{}, strings.NewReader(strings.Repeat("not a tar archive ", 100)), t.TempDir(), 0); err == nil {
			t.Error("garbage must be an error")
		}
	})
	t.Run("truncated gzip", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "a", body: strings.Repeat("x", 4096)}}, true)
		if err := extractTar(OSFS{}, bytes.NewReader(raw[:len(raw)/2]), t.TempDir(), 0); err == nil {
			t.Error("a truncated archive must be an error")
		}
	})
}

func TestStripName(t *testing.T) {
	tests := []struct {
		name  string
		strip int
		want  string
		ok    bool
	}{
		{"a/b/c", 0, "a/b/c", true},
		{"a/b/c", 1, "b/c", true},
		{"./a/b", 1, "b", true},
		{"a/", 1, "", true},
		{"a", 1, "", true},
		{"a//b", 0, "a/b", true},
		{"../a", 0, "", false},
		{"a/../b", 1, "", false},
		{"/a", 0, "", false},
	}
	for _, tc := range tests {
		got, ok := stripName(tc.name, tc.strip)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stripName(%q, %d) = %q, %v; want %q, %v", tc.name, tc.strip, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTreeTouchUsesTheClock(t *testing.T) {
	h := newHarness(t)
	old := testNow.Add(-72 * time.Hour)
	dir := filepath.Join(h.root, "src", testSHA)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if !fi.ModTime().Equal(testNow) {
		t.Errorf("mtime = %v, want %v", fi.ModTime(), testNow)
	}
}
