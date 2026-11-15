package resolve

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// The pip_wheelhouse producer against a fake origin (the repository tarball at the catalog tag, a PEP 503 index,
// the wheel and Python files) and a fake runner for tar, python, pip and git (ark-sync.md section 4.9.11).

const whCatalogVersion = "2026.11.15-1"

// whWheels are the wheels of the fake index: file name and content.
var whWheels = []struct{ name, version, file, body string }{
	{"alpha", "1.0", "alpha-1.0-py3-none-any.whl", "alpha wheel"},
	{"Beta_Tool", "2.0", "beta_tool-2.0-cp312-cp312-manylinux_2_28_x86_64.whl", "beta wheel"},
	{"hatchling", "1.33.0", "hatchling-1.33.0-py3-none-any.whl", "hatchling wheel"},
}

// whShim is the repo_path source in the fake repository tarball (tests/ and the lock are not part of its tree).
var whShim = map[string]string{
	"pyproject.toml":                   "[project]\nname = \"localai-music-shim\"\n",
	"README.md":                        "shim\n",
	"localai_music_shim/__init__.py":   "",
	"localai_music_shim/__main__.py":   "print('hi')\n",
	"tests/test_shim.py":               "assert True\n",
	"localai_music_shim/x.cpython.pyc": "bytecode",
}

const whCommit = "0123456789abcdef0123456789abcdef01234567"

type whOrigin struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	hits map[string]int
	// wrong serves other bytes for a file name; hook runs before a file is served.
	wrong map[string]bool
	hook  func(file string)
	// lock is the lock in the tarball, shim the shim tree.
	lock []byte
	shim map[string]string
}

func newWhOrigin(t *testing.T) *whOrigin {
	o := &whOrigin{t: t, hits: map[string]int{}, wrong: map[string]bool{}, shim: map[string]string{}}
	for k, v := range whShim {
		o.shim[k] = v
	}
	o.srv = httptest.NewServer(http.HandlerFunc(o.serve))
	t.Cleanup(o.srv.Close)
	o.lock = o.makeLock(nil)
	return o
}

// makeLock writes a lock for the fake index; extra lines are added verbatim.
func (o *whOrigin) makeLock(extra []string) []byte {
	var b strings.Builder
	b.WriteString("# generated for a test\n")
	b.WriteString("# repo-tree-sha256: engines/acestep " + o.shimTree() + "\n")
	b.WriteString("# build-requires: hatchling\n")
	for _, l := range extra {
		b.WriteString(l + "\n")
	}
	for _, w := range whWheels {
		b.WriteString(w.name + "==" + w.version + " --hash=sha256:" + sha256Hex([]byte(w.body)) + "\n")
	}
	return []byte(b.String())
}

func (o *whOrigin) shimTree() string {
	dir := o.t.TempDir()
	for rel, body := range o.shim {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	sum, err := RepoTreeSHA256(dir)
	if err != nil {
		o.t.Fatal(err)
	}
	return sum
}

func (o *whOrigin) tarball() []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: "local-ai-catalog/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "local-ai-catalog/", Mode: 0o755, Typeflag: tar.TypeDir})
	add("engines/acestep/requirements-cuda.lock", string(o.lock))
	add("docs/README.md", "docs\n")
	names := make([]string, 0, len(o.shim))
	for k := range o.shim {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		add("engines/acestep/"+k, o.shim[k])
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func (o *whOrigin) serve(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.hits[r.URL.Path]++
	hook, wrong := o.hook, o.wrong
	o.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/example/local-ai/tar.gz/refs/tags/catalog-"):
		_, _ = w.Write(o.tarball())
	case r.URL.Path == "/py/cpython-3.12.16-install_only.tar.gz":
		_, _ = io.WriteString(w, "python tarball")
	case strings.HasPrefix(r.URL.Path, "/simple/"):
		name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/simple/"), "/")
		fmt.Fprintf(w, "<html><body>\n")
		for _, wh := range whWheels {
			if NormalizeDist(wh.name) == name {
				// a relative link (the PyTorch index style) and a decoy with another digest
				fmt.Fprintf(w, "<a href=\"../../files/%s#sha256=%s\">%s</a>\n", wh.file, sha256Hex([]byte("decoy")), wh.file)
				fmt.Fprintf(w, "<a data-requires-python=\"&gt;=3.8\" href=\"/files/%s#sha256=%s\">%s</a>\n", wh.file, sha256Hex([]byte(wh.body)), wh.file)
			}
		}
		fmt.Fprintf(w, "</body></html>\n")
	case strings.HasPrefix(r.URL.Path, "/files/"):
		file := strings.TrimPrefix(r.URL.Path, "/files/")
		if hook != nil {
			hook(file)
		}
		for _, wh := range whWheels {
			if wh.file == file {
				if wrong[file] {
					_, _ = io.WriteString(w, strings.Repeat("x", len(wh.body)))
					return
				}
				_, _ = io.WriteString(w, wh.body)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (o *whOrigin) hit(p string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits[p]
}

// whRunner plays tar, python -m venv, pip install, pip wheel and git. failAt makes the call whose argv contains it
// exit 1.
type whRunner struct {
	mu     sync.Mutex
	calls  []execx.Cmd
	failAt string
}

func (f *whRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	fail := f.failAt
	f.mu.Unlock()
	argv := filepath.Base(c.Path) + " " + strings.Join(c.Args, " ")
	if fail != "" && strings.Contains(argv, fail) {
		return execx.Result{ExitCode: 1, Stderr: []byte("error: boom\n")}, nil
	}
	arg := func(flag string) string {
		for i, a := range c.Args {
			if a == flag && i+1 < len(c.Args) {
				return c.Args[i+1]
			}
		}
		return ""
	}
	switch {
	case filepath.Base(c.Path) == "tar":
		dst := arg("-C")
		_ = os.MkdirAll(filepath.Join(dst, "python", "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(dst, "python", "bin", "python3"), []byte("#!/bin/sh\n"), 0o755)
	case strings.Contains(argv, "-m venv"):
		dst := c.Args[len(c.Args)-1]
		_ = os.MkdirAll(filepath.Join(dst, "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(dst, "bin", "python"), []byte("#!/bin/sh\n"), 0o755)
	case strings.Contains(argv, "-m pip wheel"):
		src := c.Args[len(c.Args)-1]
		file := "ace_step-1.5.0-py3-none-any.whl"
		switch {
		case strings.HasSuffix(src, filepath.FromSlash("engines/acestep")):
			file = "localai_music_shim-0.1.0-py3-none-any.whl"
		case strings.HasSuffix(src, filepath.FromSlash("third/nano")):
			file = "nano_vllm-0.2.0-py3-none-any.whl"
		}
		_ = os.WriteFile(filepath.Join(arg("-w"), file), []byte("built "+file), 0o644)
	case filepath.Base(c.Path) == "git" && strings.Contains(argv, " init "):
		_ = os.MkdirAll(c.Args[len(c.Args)-1], 0o755)
	case filepath.Base(c.Path) == "git" && strings.Contains(argv, "rev-parse"):
		return execx.Result{Stdout: []byte(whCommit + "\n")}, nil
	}
	return execx.Result{}, nil
}

func (f *whRunner) argvs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	}
	return out
}

type whRig struct {
	o      *whOrigin
	run    *whRunner
	p      *PipWheelhouse
	env    *Env
	root   string
	it     plan.CatalogItem
	online bool
	free   int64
}

func newWhRig(t *testing.T) *whRig {
	t.Helper()
	r := &whRig{o: newWhOrigin(t), run: &whRunner{}, root: t.TempDir(), online: true, free: 1 << 50}
	r.env = &Env{
		Runner: r.run,
		Lookup: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		Online: func() bool { return r.online },
		Now:    func() time.Time { return fixedNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		StartProxy: func(context.Context, throttle.Bucket, []string) (string, func(), error) {
			return "", func() {}, nil
		},
		SyncHome: t.TempDir(),
		Downloader: download.New(download.Options{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Backoff: download.Backoff{Steps: []time.Duration{time.Nanosecond}, Jitter: -1},
			Free:    func(string) (int64, int64, error) { return 1 << 50, r.free, nil },
		}),
	}
	r.p = &PipWheelhouse{Env: r.env, Repo: &RepoDocs{Env: r.env, CodeloadBase: r.o.srv.URL, Repo: "example/local-ai"}, allowLocal: true}
	r.setLock(r.o.lock)
	return r
}

// setLock puts lock in the tarball and pins its sha256 in the item.
func (r *whRig) setLock(lock []byte) {
	r.o.lock = lock
	r.it = contentItem("kit-engine-acestep-cuda", "kit", catalog.ContentSource{
		Type: "pip_wheelhouse", Version: "1.5.0", Lock: "engines/acestep/requirements-cuda.lock", SHA256: sha256Hex(lock),
		Files:   []catalog.URLFile{{URL: r.o.srv.URL + "/py/cpython-3.12.16-install_only.tar.gz", SHA256: sha256Hex([]byte("python tarball")), Name: "python/cpython-3.12.16-install_only.tar.gz"}},
		Indexes: []string{r.o.srv.URL + "/simple"},
		Builds: []catalog.WheelBuild{
			{URL: "https://example.test/ACE-Step.git", Commit: whCommit},
			{URL: "https://example.test/ACE-Step.git", Commit: whCommit, Subdirectory: "third/nano"},
			{RepoPath: "engines/acestep"},
		},
	})
}

func (r *whRig) version() string { return catalog.WheelhouseVersion(r.it.ID, r.it.Content.Source) }

func (r *whRig) request() Request {
	return Request{Item: r.it, RootID: "internal", Root: r.root, Version: r.version(), CatalogVersion: whCatalogVersion,
		ItemDir: "kit/engines/acestep", Checkpoints: &download.MemoryCheckpointer{}}
}

func (r *whRig) produce(ctx context.Context) (Product, error) { return r.p.Produce(ctx, r.request()) }

func TestWheelhouseHappyPath(t *testing.T) {
	r := newWhRig(t)
	prod, err := r.produce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := r.version()
	if prod.Version != v || !strings.HasPrefix(v, "1.5.0-") || len(prod.Parts) != 1 || prod.Keep {
		t.Fatalf("product = %+v", prod)
	}
	part := prod.Parts[0]
	if part.Kind != PartTree || part.From != "tree" || part.To != "kit/engines/acestep" || part.TreeSHA256 == "" {
		t.Fatalf("part = %+v", part)
	}
	var got []string
	for _, s := range part.Sums {
		got = append(got, s.Path)
	}
	want := []string{
		v + "/python/cpython-3.12.16-install_only.tar.gz",
		v + "/requirements.lock",
		v + "/wheels/ace_step-1.5.0-py3-none-any.whl",
		v + "/wheels/alpha-1.0-py3-none-any.whl",
		v + "/wheels/beta_tool-2.0-cp312-cp312-manylinux_2_28_x86_64.whl",
		v + "/wheels/hatchling-1.33.0-py3-none-any.whl",
		v + "/wheels/localai_music_shim-0.1.0-py3-none-any.whl",
		v + "/wheels/nano_vllm-0.2.0-py3-none-any.whl",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sums:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	tree := filepath.Join(r.request().StagingDir(), "tree")
	if sums, err := manifest.ReadSums(osFS, tree); err != nil || len(sums) != len(want) {
		t.Fatalf("SHA256SUMS = %v, %v", sums, err)
	}
	lock, err := os.ReadFile(filepath.Join(tree, v, "requirements.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(lock, r.o.lock) {
		t.Error("the kit lock does not start with the pinned lock")
	}
	pl, err := ParsePipLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	built := map[string]string{}
	for _, q := range pl.Reqs {
		built[NormalizeDist(q.Name)] = q.SHA256
	}
	for _, f := range []string{"ace_step-1.5.0-py3-none-any.whl", "nano_vllm-0.2.0-py3-none-any.whl", "localai_music_shim-0.1.0-py3-none-any.whl"} {
		dist, _, _ := WheelName(f)
		if built[dist] != sha256Hex([]byte("built "+f)) {
			t.Errorf("the lock has no line with the digest of %s", f)
		}
	}
	// The build ran offline: --no-index, no build isolation, an unusable proxy, and the git source was fetched
	// once at the pinned commit for both builds of the same repository.
	argv := strings.Join(r.run.argvs(), "\n")
	for _, w := range []string{"tar -xzf", "-m venv", "-m pip install --no-index --find-links", "--require-hashes -r",
		"-m pip wheel --no-deps --no-build-isolation --no-index", "fetch -q --depth 1 --no-tags -- https://example.test/ACE-Step.git " + whCommit} {
		if !strings.Contains(argv, w) {
			t.Errorf("no %q in:\n%s", w, argv)
		}
	}
	if n := strings.Count(argv, " fetch "); n != 1 {
		t.Errorf("%d git fetches, want 1", n)
	}
	for _, c := range r.run.calls {
		if strings.Contains(strings.Join(c.Args, " "), "-m pip") {
			env := strings.Join(c.Env, " ")
			if !strings.Contains(env, "PIP_NO_INDEX=1") || !strings.Contains(env, "https_proxy="+whNoProxy) {
				t.Errorf("pip ran with %q", env)
			}
		}
	}
	// The build venv installs only the build backends, and nothing of the build is in the tree.
	builds, _ := filepath.Glob(filepath.Join(r.request().StagingDir(), "build-*", "build.lock"))
	if len(builds) != 1 {
		t.Fatalf("build locks: %v", builds)
	}
	if bl, _ := os.ReadFile(builds[0]); strings.TrimSpace(string(bl)) != "hatchling==1.33.0 --hash=sha256:"+sha256Hex([]byte("hatchling wheel")) {
		t.Errorf("build lock = %q", bl)
	}
	if prod.Source["lock_sha256"] != r.it.Content.Source.SHA256 || prod.Source["backend"] != "cuda" {
		t.Errorf("source = %v", prod.Source)
	}
}

func TestWheelhouseResumesAfterAnInterruption(t *testing.T) {
	r := newWhRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.o.hook = func(file string) {
		if strings.HasPrefix(file, "beta_tool") {
			cancel()
		}
	}
	if _, err := r.produce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancel", err)
	}
	tree := filepath.Join(r.request().StagingDir(), "tree")
	if _, err := os.Stat(filepath.Join(tree, manifest.SumsName)); !os.IsNotExist(err) {
		t.Fatal("an interrupted wheelhouse was sealed")
	}
	r.o.hook = nil
	if _, err := r.produce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.o.hit("/files/alpha-1.0-py3-none-any.whl"); n != 1 {
		t.Errorf("alpha was downloaded %d times, want 1 (kept from the interrupted run)", n)
	}
	if n := r.o.hit("/py/cpython-3.12.16-install_only.tar.gz"); n != 1 {
		t.Errorf("the Python was downloaded %d times, want 1", n)
	}
}

func TestWheelhouseHashMismatchIsNotKept(t *testing.T) {
	r := newWhRig(t)
	r.o.wrong["beta_tool-2.0-cp312-cp312-manylinux_2_28_x86_64.whl"] = true
	_, err := r.produce(context.Background())
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindMismatch || CodeOf(err) != doctor.SYN020 {
		t.Fatalf("err = %v, want a checksum mismatch (LAI-SYN-020)", err)
	}
	tree := filepath.Join(r.request().StagingDir(), "tree")
	if _, err := os.Stat(filepath.Join(tree, r.version(), "wheels", "beta_tool-2.0-cp312-cp312-manylinux_2_28_x86_64.whl")); !os.IsNotExist(err) {
		t.Error("the mismatching wheel became a staged file")
	}
	if _, err := os.Stat(filepath.Join(tree, manifest.SumsName)); !os.IsNotExist(err) {
		t.Error("a wheelhouse with a bad wheel was sealed")
	}
}

func TestWheelhouseDiskFull(t *testing.T) {
	r := newWhRig(t)
	r.free = 0
	req := r.request()
	req.MinFreeBytes = 1 << 30
	_, err := r.p.Produce(context.Background(), req)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindDisk || CodeOf(err) != doctor.SYN040 {
		t.Fatalf("err = %v, want disk full (LAI-SYN-040)", err)
	}
	if len(r.run.calls) != 0 {
		t.Errorf("a build ran on a full disk: %v", r.run.argvs())
	}
}

func TestWheelhouseOfflineTouchesNothing(t *testing.T) {
	r := newWhRig(t)
	r.online = false
	_, err := r.produce(context.Background())
	if CodeOf(err) != doctor.SYN002 || !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v, want LAI-SYN-002", err)
	}
	r.o.mu.Lock()
	n := len(r.o.hits)
	r.o.mu.Unlock()
	if n != 0 || len(r.run.calls) != 0 {
		t.Errorf("offline: %d requests, %d programs", n, len(r.run.calls))
	}
	// The planner still knows the version, from the catalog alone.
	res, err := r.p.Resolve(context.Background(), r.it)
	if err != nil || res.Version != r.version() {
		t.Errorf("Resolve = %+v, %v", res, err)
	}
	if fc, ok := plan.FromCatalog(r.it, whCatalogVersion); !ok || fc.Version != r.version() {
		t.Errorf("FromCatalog = %+v, %v", fc, ok)
	}
	// Nothing is planned as a local build: offline, the present copy is kept and nothing runs.
	if _, ok := any(r.p).(plan.LocalResolver); ok {
		t.Error("the wheelhouse resolver offers an offline build")
	}
}

func TestWheelhouseRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *whRig)
		code  string
	}{
		{"unresolved package in the lock", func(r *whRig) {
			r.setLock(r.o.makeLock([]string{"# unresolved: torch==2.10.0+cu128 (download.pytorch.org)"}))
		}, doctor.SYN060},
		{"lock is not the pinned one", func(r *whRig) { r.it.Content.Source.SHA256 = sha256Hex([]byte("other")) }, doctor.SYN030},
		{"shim changed without a new lock", func(r *whRig) { r.o.shim["localai_music_shim/__main__.py"] = "changed\n" }, doctor.SYN060},
		{"a wheel no index has", func(r *whRig) {
			r.setLock(append(r.o.makeLock(nil), []byte("gamma==1.0 --hash=sha256:"+sha256Hex([]byte("gamma"))+"\n")...))
		}, doctor.SYN030},
		{"a build fails", func(r *whRig) { r.run.failAt = "pip wheel" }, doctor.SYN060},
		{"the pinned commit cannot be fetched", func(r *whRig) { r.run.failAt = " fetch " }, doctor.SYN031},
		{"tar is missing", func(r *whRig) {
			r.env.Lookup = func(n string) (string, error) {
				if n == "tar" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + n, nil
			}
		}, doctor.SYN061},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWhRig(t)
			tc.setup(r)
			_, err := r.produce(context.Background())
			if CodeOf(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if _, serr := os.Stat(filepath.Join(r.request().StagingDir(), "tree", manifest.SumsName)); !os.IsNotExist(serr) {
				t.Error("a refused wheelhouse was sealed")
			}
		})
	}
}

func TestWheelhouseReusesThePresentVersion(t *testing.T) {
	r := newWhRig(t)
	prod, err := r.produce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Install the first product as the present copy, then change only the Python pin: the wheels are linked.
	present := filepath.Join(r.root, "kit", "engines", "acestep")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(r.request().StagingDir(), "tree"), present); err != nil {
		t.Fatal(err)
	}
	before := r.o.hit("/files/alpha-1.0-py3-none-any.whl")
	r.it.Content.Source.Indexes = append(r.it.Content.Source.Indexes, r.o.srv.URL+"/other")
	req := r.request()
	req.PresentDir, req.PresentVersion = present, prod.Version
	if req.Version == prod.Version {
		t.Fatal("the version did not change with the pins")
	}
	if _, err := r.p.Produce(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if r.o.hit("/files/alpha-1.0-py3-none-any.whl") != before {
		t.Error("a wheel identical in the present version was downloaded again")
	}
	// The same version as the present one is kept as it is.
	req.PresentVersion = req.Version
	if p, err := r.p.Produce(context.Background(), req); err != nil || !p.Keep {
		t.Errorf("same version = %+v, %v", p, err)
	}
}

func TestParsePipLock(t *testing.T) {
	h := sha256Hex([]byte("x"))
	for _, tc := range []struct {
		name, data string
		ok         bool
	}{
		{"plain", "a==1 --hash=sha256:" + h + "\n", true},
		{"comments and tree", "# x\n# repo-tree-sha256: engines/acestep " + h + "\n# build-requires: a\na==1 --hash=sha256:" + h + "\n", true},
		{"range", "a>=1 --hash=sha256:" + h + "\n", false},
		{"no hash", "a==1\n", false},
		{"two hashes", "a==1 --hash=sha256:" + h + " --hash=sha256:" + h + "\n", false},
		{"option line", "--index-url https://example.test\na==1 --hash=sha256:" + h + "\n", false},
		{"url", "a @ https://example.test/a.whl --hash=sha256:" + h + "\n", false},
		{"twice", "a==1 --hash=sha256:" + h + "\nA==2 --hash=sha256:" + h + "\n", false},
		{"short hash", "a==1 --hash=sha256:abc\n", false},
		{"build backend not pinned", "# build-requires: b\na==1 --hash=sha256:" + h + "\n", false},
		{"empty", "# nothing\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePipLock([]byte(tc.data))
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, ok want %v", err, tc.ok)
			}
		})
	}
	if d, v, ok := WheelName("torch-2.10.0+cu128-cp312-cp312-manylinux_2_28_x86_64.whl"); !ok || d != "torch" || v != "2.10.0+cu128" {
		t.Errorf("WheelName = %s %s %v", d, v, ok)
	}
	if _, _, ok := WheelName("../x.tar.gz"); ok {
		t.Error("a non-wheel was accepted")
	}
}

// The tree hash rule is the one of scripts/release/lock-acestep.sh: tests, locks, inputs and byte code do not count.
func TestRepoTreeSHA256IgnoresTestsAndByteCode(t *testing.T) {
	mk := func(files map[string]string) string {
		dir := t.TempDir()
		for rel, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(body), 0o644)
		}
		s, err := RepoTreeSHA256(dir)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	base := map[string]string{"a.py": "1", "pkg/b.py": "2"}
	with := map[string]string{"a.py": "1", "pkg/b.py": "2", "tests/t.py": "x", "r.lock": "x", "r.in": "x", "pkg/__pycache__/b.pyc": "x"}
	if mk(base) != mk(with) {
		t.Error("ignored files changed the tree hash")
	}
	if mk(base) == mk(map[string]string{"a.py": "1", "pkg/b.py": "3"}) {
		t.Error("a changed source did not change the tree hash")
	}
	_ = path.Join
}
