package resolve

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

func gitItem(url, also string) plan.CatalogItem {
	return contentItem("kit-llama-cpp", "kit", catalog.ContentSource{Type: "git_mirror", URL: url, AlsoTarballOf: also})
}

// at keeps the URL fixtures below from looking like an email address to the redaction check.
const at = "@"

func TestMirrorSourceTable(t *testing.T) {
	g := &GitMirror{Env: &Env{}}
	tests := []struct {
		name    string
		url     string
		want    string
		host    string
		wantErr bool
	}{
		{"github style", "https://git.example.test/org/llama.cpp.git", "llama.cpp", "git.example.test", false},
		{"no .git suffix", "https://git.example.test/org/whisper.cpp", "whisper.cpp", "git.example.test", false},
		{"trailing slash", "https://git.example.test/org/local-ai.git/", "local-ai", "git.example.test", false},
		{"upper host is lowered", "https://GIT.Example.TEST/org/x.git", "x", "git.example.test", false},
		{"http refused", "http://git.example.test/org/x.git", "", "", true},
		{"ssh refused", "ssh://git" + at + "git.example.test/org/x.git", "", "", true},
		{"file refused", "file:///srv/x.git", "", "", true},
		{"bare path refused", "/srv/x.git", "", "", true},
		{"credentials refused", "https://login:spoken" + at + "git.example.test/org/x.git", "", "", true},
		{"query refused", "https://git.example.test/org/x.git?a=b", "", "", true},
		{"no host", "https:///org/x.git", "", "", true},
		{"dot dot name", "https://git.example.test/org/%2e%2e.git", "", "", true},
		{"leading dash name", "https://git.example.test/org/-x.git", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, host, _, err := g.mirrorSource(gitItem(tc.url, ""))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if CodeOf(err) != CodeRefused {
					t.Errorf("code = %q, want %q", CodeOf(err), CodeRefused)
				}
				return
			}
			if name != tc.want || host != tc.host {
				t.Errorf("got (%q, %q), want (%q, %q)", name, host, tc.want, tc.host)
			}
		})
	}
}

func TestMirrorSourceWrongType(t *testing.T) {
	g := &GitMirror{Env: &Env{}}
	_, _, _, err := g.mirrorSource(contentItem("x", "kit", catalog.ContentSource{Type: "url"}))
	if err == nil {
		t.Fatal("a url item was accepted by git_mirror")
	}
}

func TestParseLsRemoteHead(t *testing.T) {
	c := absentCommit("head")
	tests := []struct {
		name string
		out  string
		ok   bool
	}{
		{"ok", c + "\tHEAD\n" + absentCommit("b") + "\trefs/heads/main\n", true},
		{"head not first", absentCommit("b") + "\trefs/heads/main\n" + c + "\tHEAD\n", true},
		{"no head", absentCommit("b") + "\trefs/heads/main\n", false},
		{"short sha", "abc123\tHEAD\n", false},
		{"empty", "", false},
		{"upper hex refused", strings.ToUpper(c) + "\tHEAD\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLsRemoteHead([]byte(tc.out))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != c {
				t.Errorf("sha = %q, want %q", got, c)
			}
		})
	}
}

// gitFake scripts the git calls of a production-mode run (https URL, no local transport).
func TestGitMirrorArgvAndEnvAreFixed(t *testing.T) {
	const gitBin = "/usr/bin/git"
	head := absentCommit("fake-head")
	root := t.TempDir()
	staging := filepath.Join(root, ".staging", "kit-llama-cpp", "v1")
	dest := filepath.Join(staging, "llama.cpp.git")
	url := "https://git.example.test/org/llama.cpp.git"
	base := []string{"-c", "protocol.file.allow=never", "-c", "core.hooksPath=/dev/null", "-c", "http.proxy=http://127.0.0.1:9"}
	repo := []string{"-c", "safe.directory=" + dest, "-c", "core.hooksPath=/dev/null", "-C", dest}
	f := fake.New()
	f.Expect(fake.Key(gitBin, append(append([]string{}, base...), "clone", "--mirror", "--no-tags", "--", url, dest)...), execx.Result{}, nil)
	f.Expect(fake.Key(gitBin, append(append([]string{}, base...), "-C", dest, "fetch", "--tags")...), execx.Result{}, nil)
	f.Expect(fake.Key(gitBin, append(append([]string{}, repo...), "fsck", "--connectivity-only", "--no-dangling")...), execx.Result{}, nil)
	f.Expect(fake.Key(gitBin, append(append([]string{}, repo...), "rev-parse", "--verify", "--quiet", "HEAD^{commit}")...), execx.Result{Stdout: []byte(head + "\n")}, nil)

	var proxyHosts []string
	stopped := false
	env := &Env{
		Runner:   f,
		Lookup:   func(string) (string, error) { return gitBin, nil },
		Online:   func() bool { return true },
		Now:      func() time.Time { return fixedNow },
		SyncHome: "/var/lib/localai/sync",
		StartProxy: func(_ context.Context, _ throttle.Bucket, hosts []string) (string, func(), error) {
			proxyHosts = hosts
			return "127.0.0.1:9", func() { stopped = true }, nil
		},
	}
	g := &GitMirror{Env: env}
	prod, err := g.Produce(context.Background(), Request{Item: gitItem(url, ""), RootID: "internal", Root: root, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if prod.Version != head[:12] {
		t.Errorf("version = %q, want %q", prod.Version, head[:12])
	}
	if len(prod.Parts) != 1 || prod.Parts[0].Kind != PartTree || prod.Parts[0].To != "kit/git/llama.cpp.git" || prod.Parts[0].From != "llama.cpp.git" {
		t.Errorf("parts = %+v", prod.Parts)
	}
	if !stopped {
		t.Error("the proxy was not stopped")
	}
	if len(proxyHosts) != 1 || proxyHosts[0] != "git.example.test" {
		t.Errorf("proxy hosts = %v, want only the item's host", proxyHosts)
	}
	for i, c := range f.Calls() {
		if c.Path != gitBin {
			t.Errorf("call %d runs %s", i, c.Path)
		}
		if c.Timeout <= 0 || c.MaxOut <= 0 {
			t.Errorf("call %d has no bounds: %+v", i, c)
		}
		env := strings.Join(c.Env, "\n")
		for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "HOME=/var/lib/localai/sync", "GIT_ALLOW_PROTOCOL=https", "PATH=" + execx.FixedPath} {
			if !strings.Contains(env, want) {
				t.Errorf("call %d env lacks %s: %v", i, want, c.Env)
			}
		}
		if len(c.Env) != 7 {
			t.Errorf("call %d env is not minimal: %v", i, c.Env)
		}
		for _, a := range c.Args {
			if strings.ContainsAny(a, ";|&$`") && !strings.HasPrefix(a, "HEAD^") {
				t.Errorf("call %d argument looks interpreted: %q", i, a)
			}
		}
	}
}

func TestGitMirrorRefusesWhenOfflineWithoutRunningAnything(t *testing.T) {
	f := fake.New()
	g := &GitMirror{Env: &Env{Runner: f, Lookup: func(string) (string, error) { return "/usr/bin/git", nil }, SyncHome: t.TempDir(),
		Online: func() bool { return false }}}
	_, err := g.Produce(context.Background(), Request{Item: gitItem("https://git.example.test/o/x.git", ""), Root: t.TempDir(), Version: "v"})
	if !errors.Is(err, ErrOffline) || CodeOf(err) != CodeOffline {
		t.Fatalf("err = %v (code %q), want ErrOffline / %s", err, CodeOf(err), CodeOffline)
	}
	if _, err := g.Resolve(context.Background(), gitItem("https://git.example.test/o/x.git", "")); !errors.Is(err, ErrOffline) {
		t.Fatalf("Resolve err = %v, want ErrOffline", err)
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("%d commands ran while offline", n)
	}
	// An Env with no Online function is offline too (the safe default).
	g.Env.Online = nil
	if _, err := g.Produce(context.Background(), Request{Item: gitItem("https://git.example.test/o/x.git", ""), Root: t.TempDir(), Version: "v"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("nil Online: err = %v, want ErrOffline", err)
	}
}

func TestGitMirrorMissingGit(t *testing.T) {
	g := &GitMirror{Env: &Env{Runner: fake.New(), Lookup: func(string) (string, error) { return "", execx.ErrNotFound },
		Online: func() bool { return true }, SyncHome: t.TempDir()}}
	_, err := g.Produce(context.Background(), Request{Item: gitItem("https://git.example.test/o/x.git", ""), Root: t.TempDir(), Version: "v"})
	if CodeOf(err) != CodeTool || CodeTool != doctor.SYN061 {
		t.Fatalf("code = %q, want LAI-SYN-061 (err %v)", CodeOf(err), err)
	}
}

func TestGitMirrorResolveWithFake(t *testing.T) {
	const gitBin = "/usr/bin/git"
	url := "https://git.example.test/org/llama.cpp.git"
	head := absentCommit("resolve")
	key := fake.Key(gitBin, "-c", "protocol.file.allow=never", "-c", "core.hooksPath=/dev/null", "ls-remote", "--", url, "HEAD")
	tests := []struct {
		name     string
		res      execx.Result
		err      error
		wantCode string
		version  string
	}{
		{"ok", execx.Result{Stdout: []byte(head + "\tHEAD\n")}, nil, "", head[:12]},
		{"unreachable", execx.Result{ExitCode: 128, Stderr: []byte("fatal: unable to access: could not resolve host")}, nil, CodeNoVersion, ""},
		{"truncated", execx.Result{Stdout: []byte(head + "\tHEAD\n"), Truncated: true}, nil, CodeNoVersion, ""},
		{"no head line", execx.Result{Stdout: []byte("\n")}, nil, CodeNoVersion, ""},
		{"timeout", execx.Result{}, execx.ErrTimeout, CodeNoVersion, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fake.New().Expect(key, tc.res, tc.err)
			g := &GitMirror{Env: &Env{Runner: f, Lookup: func(string) (string, error) { return gitBin, nil }, Online: func() bool { return true }, SyncHome: "/x"}}
			r, err := g.Resolve(context.Background(), gitItem(url, ""))
			if tc.wantCode == "" {
				if err != nil || r.Version != tc.version {
					t.Fatalf("got (%+v, %v), want version %s", r, err, tc.version)
				}
				return
			}
			if CodeOf(err) != tc.wantCode {
				t.Fatalf("code = %q, want %q (err %v)", CodeOf(err), tc.wantCode, err)
			}
		})
	}
}

// newMirrorProducer builds a GitMirror over the real git and a local origin.
func newMirrorProducer(t *testing.T, node ...string) *GitMirror {
	t.Helper()
	needTool(t, "git", "gzip")
	return &GitMirror{Env: testEnv(t), allowLocal: true, NodeCommits: func(string) []string { return node }}
}

func produce(t *testing.T, g *GitMirror, root string, u *upstream, also string) (Product, error) {
	t.Helper()
	return g.Produce(context.Background(), Request{
		Item:   gitItem(u.Bare, also),
		RootID: "internal", Root: root, Version: "v1",
	})
}

// install moves a staged tree into the root the way the engine's swap does, so the next run sees a
// present mirror.
func install(t *testing.T, root, staging string, p Part) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(p.To))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(staging, filepath.FromSlash(p.From)), dst); err != nil {
		t.Fatal(err)
	}
}

func TestGitMirrorCloneUpdateFsckAndTarball(t *testing.T) {
	u := newUpstream(t)
	pinned := u.head()
	g := newMirrorProducer(t, pinned)
	root := t.TempDir()
	name := strings.TrimSuffix(filepath.Base(u.Bare), ".git")

	// First time: clone into staging, verify, archive the pinned commit.
	prod, err := produce(t, g, root, u, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Version != pinned[:12] || prod.Trust != manifest.TrustComputed {
		t.Errorf("version %q trust %q", prod.Version, prod.Trust)
	}
	if len(prod.Parts) != 2 || prod.Parts[0].Kind != PartTree || prod.Parts[0].To != "kit/git/"+name+".git" {
		t.Fatalf("parts = %+v", prod.Parts)
	}
	tb := prod.Parts[1]
	if tb.Kind != PartFile || tb.To != "kit/src" || len(tb.Files) != 1 {
		t.Fatalf("tarball part = %+v", tb)
	}
	f := tb.Files[0]
	if f.Path != name+"-"+pinned+".tar.gz" || f.SHA256Source != "computed_after_verify" || f.Kind != "source_tarball" {
		t.Errorf("tarball file = %+v", f)
	}
	staging := (Request{Item: gitItem(u.Bare, ""), Root: root, Version: "v1"}).StagingDir()
	sum, size, err := hashFile(filepath.Join(staging, "src", f.Path))
	if err != nil || sum != f.SHA256 || size != f.SizeBytes {
		t.Fatalf("recorded sha256/size do not match the file (%v)", err)
	}
	// The tar's single top-level folder is <name>-<commit>/ and holds the committed file.
	listing := runTool(t, toolEnv(t.TempDir()), needTool(t, "tar")["tar"], "-tzf", filepath.Join(staging, "src", f.Path))
	if !strings.Contains(listing, name+"-"+pinned+"/README") {
		t.Errorf("tarball listing lacks the prefixed README:\n%s", listing)
	}

	// Reproducible: the same commit in a second root gives byte-identical output.
	root2 := t.TempDir()
	prod2, err := produce(t, g, root2, u, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if prod2.Parts[1].Files[0].SHA256 != f.SHA256 {
		t.Errorf("tarball sha256 differs between two runs")
	}

	// Update in place: after the swap, a new upstream commit is fetched into the present mirror.
	install(t, root, staging, prod.Parts[0])
	newHead := u.commit("NEWS", "second\n")
	prod3, err := produce(t, g, root, u, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if prod3.Parts[0].Kind != PartInPlace || prod3.Parts[0].From != "" {
		t.Errorf("the present mirror should be updated in place: %+v", prod3.Parts[0])
	}
	if prod3.Version != newHead[:12] {
		t.Errorf("version = %q, want the new head %q", prod3.Version, newHead[:12])
	}
	present := filepath.Join(root, "kit", "git", name+".git")
	got := runTool(t, toolEnv(t.TempDir()), g.mustGit(t), "-C", present, "rev-parse", "HEAD")
	if got != newHead {
		t.Errorf("the mirror HEAD = %s, want %s", got, newHead)
	}
	// The pinned (older) commit still produces the same tarball.
	if prod3.Parts[1].Files[0].SHA256 != f.SHA256 {
		t.Errorf("tarball of the pinned commit changed after an update")
	}
}

func (g *GitMirror) mustGit(t *testing.T) string {
	t.Helper()
	return needTool(t, "git")["git"]
}

func TestGitMirrorPinnedCommitMissingIsMirrorError(t *testing.T) {
	u := newUpstream(t)
	g := newMirrorProducer(t, absentCommit("never-pushed"))
	_, err := produce(t, g, t.TempDir(), u, "pinned")
	if CodeOf(err) != CodeMirror || CodeMirror != doctor.SYN024 {
		t.Fatalf("code = %q, want LAI-SYN-024 (err %v)", CodeOf(err), err)
	}
	// Without also_tarball_of the pin is not checked.
	if _, err := produce(t, g, t.TempDir(), u, ""); err != nil {
		t.Fatalf("no also_tarball_of: %v", err)
	}
}

func TestGitMirrorBadPinIsRefusedBeforeItReachesGit(t *testing.T) {
	u := newUpstream(t)
	for _, bad := range []string{"--upload-pack=x", "HEAD", "abc", strings.Repeat("g", 40), absentCommit("x") + "; rm -rf /"} {
		g := newMirrorProducer(t, bad)
		_, err := produce(t, g, t.TempDir(), u, "pinned")
		if CodeOf(err) != CodeMirror {
			t.Errorf("pin %q: code = %q, want %s (err %v)", bad, CodeOf(err), CodeMirror, err)
		}
	}
}

func TestGitMirrorDamagedMirrorFailsFsck(t *testing.T) {
	u := newUpstream(t)
	g := newMirrorProducer(t)
	root := t.TempDir()
	prod, err := produce(t, g, root, u, "")
	if err != nil {
		t.Fatal(err)
	}
	staging := (Request{Item: gitItem(u.Bare, ""), Root: root, Version: "v1"}).StagingDir()
	install(t, root, staging, prod.Parts[0])
	mirror := filepath.Join(root, "kit", "git", strings.TrimSuffix(filepath.Base(u.Bare), ".git")+".git")
	// Overwrite every object (loose and packed) with garbage: the refs now point at nothing usable.
	objs := filepath.Join(mirror, "objects")
	err = filepath.WalkDir(objs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if err := os.Chmod(p, 0o644); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("garbage"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = produce(t, g, root, u, "")
	if CodeOf(err) != CodeMirror {
		t.Fatalf("code = %q, want LAI-SYN-024 (err %v)", CodeOf(err), err)
	}
}

func TestGitMirrorUnreachableSource(t *testing.T) {
	needTool(t, "git")
	g := &GitMirror{Env: testEnv(t), allowLocal: true}
	missing := filepath.Join(t.TempDir(), "no-such.git")
	_, err := g.Produce(context.Background(), Request{Item: gitItem(missing, ""), RootID: "internal", Root: t.TempDir(), Version: "v1"})
	if CodeOf(err) != CodeFetch || CodeFetch != doctor.SYN031 {
		t.Fatalf("code = %q, want LAI-SYN-031 (err %v)", CodeOf(err), err)
	}
}

func TestGitMirrorResumesAnEarlierStagedClone(t *testing.T) {
	u := newUpstream(t)
	g := newMirrorProducer(t)
	root := t.TempDir()
	name := strings.TrimSuffix(filepath.Base(u.Bare), ".git")
	staging := (Request{Item: gitItem(u.Bare, ""), Root: root, Version: "v1"}).StagingDir()
	dest := filepath.Join(staging, name+".git")

	// Case 1: a valid earlier clone is finished with a fetch.
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	runTool(t, toolEnv(t.TempDir()), g.mustGit(t), "clone", "-q", "--mirror", u.Bare, dest)
	newHead := u.commit("MORE", "x\n")
	prod, err := produce(t, g, root, u, "")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Version != newHead[:12] {
		t.Errorf("resumed clone is stale: version %q, want %q", prod.Version, newHead[:12])
	}

	// Case 2: junk where the clone should be is moved aside (never deleted) and the clone redone.
	root2 := t.TempDir()
	staging2 := (Request{Item: gitItem(u.Bare, ""), Root: root2, Version: "v1"}).StagingDir()
	dest2 := filepath.Join(staging2, name+".git")
	if err := os.MkdirAll(dest2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest2, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := produce(t, g, root2, u, ""); err != nil {
		t.Fatal(err)
	}
	aside, _ := filepath.Glob(dest2 + ".unusable-*")
	if len(aside) != 1 {
		t.Errorf("junk was not moved aside: %v", aside)
	}
}

func TestGitMirrorPinsFromADriveAreAllArchived(t *testing.T) {
	u := newUpstream(t)
	first := u.head()
	second := u.commit("B", "b\n")
	root := t.TempDir()
	pins := `{"schema_version":1,"llama_cpp":[{"commit":"` + first + `","nodes":["ai-n1"]},{"commit":"` + second + `","nodes":["ai-n2"]}],"whisper_cpp":[{"commit":"` + absentCommit("whisper") + `"}]}`
	if err := os.MkdirAll(filepath.Join(root, "kit", "catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kit", "catalog", "pins.json"), []byte(pins), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newMirrorProducer(t)
	g.PinKeys = map[string]string{strings.TrimSuffix(filepath.Base(u.Bare), ".git"): "llama_cpp"}
	req := Request{Item: gitItem(u.Bare, "pinned"), RootID: "ark-0001", Root: root, Version: "v1", Drive: true}
	prod, err := g.Produce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(prod.Parts[1].Files); got != 2 {
		t.Fatalf("a drive keeps a tarball per pinned commit, got %d files", got)
	}
	// The same item on the internal root archives only the node's own commit.
	g.NodeCommits = func(string) []string { return []string{second} }
	req.Drive, req.Root = false, t.TempDir()
	prod, err = g.Produce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(prod.Parts[1].Files); got != 1 {
		t.Fatalf("an internal root keeps only its own node's tarball, got %d files", got)
	}

	// A damaged pins.json is an error: a drive must not silently stop keeping sources.
	if err := os.WriteFile(filepath.Join(root, "kit", "catalog", "pins.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	req.Drive, req.Root = true, root
	if _, err := g.Produce(context.Background(), req); CodeOf(err) != CodeMirror {
		t.Errorf("damaged pins.json: code = %q (err %v)", CodeOf(err), err)
	}
}

func TestGitMirrorResolveReadsTheRemoteHead(t *testing.T) {
	u := newUpstream(t)
	g := newMirrorProducer(t)
	r, err := g.Resolve(context.Background(), gitItem(u.Bare, "pinned"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != u.head()[:12] {
		t.Errorf("version = %q, want %q", r.Version, u.head()[:12])
	}
}

func TestReadPins(t *testing.T) {
	dir := t.TempDir()
	good := absentCommit("pin")
	p := filepath.Join(dir, "pins.json")
	if cs, err := readPins(p, "llama_cpp"); err != nil || cs != nil {
		t.Fatalf("missing file: (%v, %v)", cs, err)
	}
	if err := os.WriteFile(p, []byte(`{"llama_cpp":[{"commit":"`+good+`"}],"agent":"not a list"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cs, err := readPins(p, "llama_cpp"); err != nil || len(cs) != 1 || cs[0] != good {
		t.Fatalf("got (%v, %v)", cs, err)
	}
	if cs, err := readPins(p, "whisper_cpp"); err != nil || cs != nil {
		t.Fatalf("absent key: (%v, %v)", cs, err)
	}
	if _, err := readPins(p, "agent"); err == nil {
		t.Fatal("a non-list key was accepted")
	}
}
