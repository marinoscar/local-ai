package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
	"github.com/marinoscar/local-ai/agent/internal/sync/run/adapt"
)

// The kit and docs source types (#271): git_mirror, repo_docs and apt_closure are registered on the
// planner's and the engine's registries by the real daemon, and a run of them goes through the real
// engine, machine, journal and producers. The fakes are the outside world only: the one network (a
// dialer that counts and refuses every connection), the repositories (real bare repositories made at
// run time and served to git through a runner that points the catalog's https URL at them) and apt
// (a scripted apt-get). Nothing sleeps: a test waits on the engine's own Wait.

const (
	srcMirrorURL = "https://git.example.test/org-alpha/llama.cpp.git"
	srcNodeHost  = "ai-n3"
)

var srcShaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// srcTool is the real version control tool and gzip, for the fixtures and the runner of the node.
type srcTool struct {
	t         *testing.T
	git, gzip string
	env       []string
}

func newSrcTool(t *testing.T) *srcTool {
	t.Helper()
	g := &srcTool{t: t}
	var err error
	if g.git, err = execx.Lookup("git"); err != nil {
		t.Skip("git is not installed: this test needs the real tool")
	}
	if g.gzip, err = execx.Lookup("gzip"); err != nil {
		t.Skip("gzip is not installed: this test needs the real tool")
	}
	when := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC).Format(time.RFC3339)
	g.env = []string{
		"PATH=" + execx.FixedPath, "HOME=" + t.TempDir(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Person A", "GIT_AUTHOR_EMAIL=person.a@example.test", "GIT_COMMITTER_NAME=Person A", "GIT_COMMITTER_EMAIL=person.a@example.test",
		"GIT_AUTHOR_DATE=" + when, "GIT_COMMITTER_DATE=" + when,
	}
	return g
}

func (g *srcTool) must(args ...string) string {
	g.t.Helper()
	res, err := execx.Exec{}.Run(context.Background(), execx.Cmd{Path: g.git, Args: args, Env: g.env, Timeout: time.Minute, MaxOut: 8 << 20})
	if err != nil || res.ExitCode != 0 {
		g.t.Fatalf("git %v: %v exit %d %s", args, err, res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// srcFixture is the outside world of the three source types.
type srcFixture struct {
	// llamaOrigin is the bare repository the catalog's mirror URL stands for; llamaPinned is its commit.
	llamaOrigin, llamaPinned string
	// localAI is a bare repository of the lab repository with the tag of the catalog version; seed
	// copies it to <root>/kit/git/local-ai.git, where the docs are built from, offline.
	localAI string
	// version is the catalog version of the tag.
	version string
}

func newSrcFixture(t *testing.T, g *srcTool) *srcFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &srcFixture{}

	work := filepath.Join(dir, "llama-work")
	g.must("init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("llama\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.must("-C", work, "add", "-A")
	g.must("-C", work, "commit", "-q", "-m", "first")
	fx.llamaPinned = g.must("-C", work, "rev-parse", "HEAD")
	fx.llamaOrigin = filepath.Join(dir, "llama.cpp.git")
	g.must("clone", "-q", "--bare", work, fx.llamaOrigin)

	// The lab repository at the catalog tag: docs, the inventory example and a lint-clean catalog.
	files := map[string]string{
		"docs/README.md":                    "# Docs map\n\nRunbooks:\n\n- [Survival card](runbooks/00-survival-card.md)\n- [First](runbooks/01-first.md)\n",
		"docs/runbooks/00-survival-card.md": "<!-- Card template for the tests. -->\n\n| CARD — computer <NODE_ID> | TARJETA — computadora <NODE_ID> |\n|---|---|\n| **Start** Log in as **<DESKTOP_USER>**. | **Inicio** Entre como **<DESKTOP_USER>**. |\n\nComputer / Computadora **<NODE_ID>** · Catalog / Catálogo <CATALOG_VERSION> · Printed / Impreso <RENDER_DATE>\n",
		"docs/runbooks/01-first.md":         "# 01. First\n\nLog in as `<DESKTOP_USER>` and reach node one at <TAILNET_IP_AI_N1>.\n",
	}
	src := filepath.Join("..", "catalog", "testdata", "valid")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		files["catalog/"+filepath.ToSlash(rel)] = srcShaToken.ReplaceAllStringFunc(string(b), func(m string) string {
			return strings.Repeat(srcShaToken.FindStringSubmatch(m)[1], 64)
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := os.ReadFile(filepath.Join("..", "docsrender", "testdata", "inventory.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files["inventory.example.yaml"] = string(ex)
	fx.version = strings.TrimSpace(files["catalog/CATALOG_VERSION"])

	lab := filepath.Join(dir, "lab-work")
	g.must("init", "-q", "-b", "main", lab)
	for name, content := range files {
		p := filepath.Join(lab, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g.must("-C", lab, "add", "-A")
	g.must("-C", lab, "commit", "-q", "-m", "catalog")
	g.must("-C", lab, "tag", "catalog-"+fx.version)
	fx.localAI = filepath.Join(dir, "local-ai.git")
	g.must("clone", "-q", "--bare", lab, fx.localAI)
	return fx
}

// seed puts the lab repository's mirror on a root, where repo_docs reads it (kit/git/local-ai.git).
func (fx *srcFixture) seed(t *testing.T, g *srcTool, root string) {
	t.Helper()
	dest := filepath.Join(root, "kit", "git", "local-ai.git")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	g.must("clone", "-q", "--bare", fx.localAI, dest)
}

// aptDebs is what the scripted apt-get prints for --print-uris: one package per name, with a digest
// made at run time.
type aptDeb struct{ name, version string }

func aptPrintURIs(arch string, debs []aptDeb) (out string, version string, total int64) {
	host := "http://archive.ubuntu.com/ubuntu"
	if arch != "amd64" {
		host = "http://ports.ubuntu.com/ubuntu-ports"
	}
	var list []string
	for _, d := range debs {
		file := d.name + "_" + d.version + "_" + arch + ".deb"
		sum := sha256.Sum256([]byte(file))
		const size = 2048
		total += size
		out += fmt.Sprintf("'%s/pool/main/%s/%s/%s' %s %d SHA256:%s\n", host, d.name[:1], d.name, file, file, size, hex.EncodeToString(sum[:]))
		list = append(list, d.name+"="+d.version)
	}
	sum := sha256.Sum256([]byte(strings.Join(list, "\n") + "\n"))
	return out, hex.EncodeToString(sum[:])[:12], total
}

// srcRunner is the runner of the node in these tests. The git of the producers runs for real against
// the fixture repositories: the catalog's https URL is replaced by the local bare repository and the
// two guards that forbid a local transport are dropped (the production argv and environment are
// otherwise untouched). apt-get is scripted. Everything else (tailscale and the like) goes to the rig's
// fake. It records which tools ran.
type srcRunner struct {
	inner  execx.Runner
	origin map[string]string
	apt    string
	mu     sync.Mutex
	ran    []string
	// aptBudget is how long the context of each apt-get call had left to run.
	aptBudget []time.Duration
}

func (r *srcRunner) record(what string) {
	r.mu.Lock()
	r.ran = append(r.ran, what)
	r.mu.Unlock()
}

func (r *srcRunner) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ran...)
}

// gitVerbs are the git subcommands of a call, after the -c and -C options.
func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-C":
			i++
		default:
			return args[i]
		}
	}
	return ""
}

func (r *srcRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	switch filepath.Base(c.Path) {
	case "git":
		r.record("git " + gitVerb(c.Args))
		var args []string
		for i := 0; i < len(c.Args); i++ {
			if c.Args[i] == "-c" && i+1 < len(c.Args) && c.Args[i+1] == "protocol.file.allow=never" {
				i++
				continue
			}
			a := c.Args[i]
			if local, ok := r.origin[a]; ok {
				a = local
			}
			args = append(args, a)
		}
		var env []string
		for _, e := range c.Env {
			if e != "GIT_ALLOW_PROTOCOL=https" {
				env = append(env, e)
			}
		}
		c.Args, c.Env = args, env
		return execx.Exec{}.Run(ctx, c)
	case "gzip":
		r.record("gzip")
		return execx.Exec{}.Run(ctx, c)
	case "apt-get":
		r.record("apt-get")
		if dl, ok := ctx.Deadline(); ok {
			r.mu.Lock()
			r.aptBudget = append(r.aptBudget, time.Until(dl))
			r.mu.Unlock()
		}
		for _, a := range c.Args {
			if a == "--print-uris" {
				return execx.Result{Stdout: []byte(r.apt)}, nil
			}
		}
		return execx.Result{}, nil // update
	}
	return r.inner.Run(ctx, c)
}

// srcLookup finds the real git and gzip, a scripted apt-get, and leaves every other name to the rig.
func srcLookup(inner func(string) (string, error)) func(string) (string, error) {
	return func(name string) (string, error) {
		switch name {
		case "git", "gzip":
			return execx.Lookup(name)
		case "apt-get", "apt-cache":
			return "/usr/bin/" + name, nil
		}
		return inner(name)
	}
}

// srcNet is the node's network for a test: the picture the daemon is told, and a dialer for the sync
// downloader that counts every attempt and refuses it. A test that expects no network asserts
// dials() == 0 when it ends. The hosts of the catalog's kiwix and pmtiles_extract items are counted
// apart (libraryDials): while the node is online the planner asks every wanted item's resolver, and
// those two resolvers read metadata from their origin, which is their job and not a producer's.
type srcNet struct {
	mu      sync.Mutex
	state   run.NetState
	dial    atomic.Int32
	libDial atomic.Int32
	addrs   map[string]int
}

// srcLibraryHosts are the origins of the kiwix and pmtiles_extract items of the fixture catalog.
var srcLibraryHosts = []string{"mirror.example.test:", "tiles.example.test:"}

func (n *srcNet) set(mode string, online bool) {
	n.mu.Lock()
	n.state = run.NetState{Mode: mode, Online: online}
	n.mu.Unlock()
}

func (n *srcNet) get() run.NetState {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

func (n *srcNet) client() *download.HTTPClient {
	return download.NewHTTPClient(download.ClientConfig{
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			for _, h := range srcLibraryHosts {
				if strings.HasPrefix(addr, h) {
					n.libDial.Add(1)
					return nil, errors.New("no network in this test")
				}
			}
			n.dial.Add(1)
			n.mu.Lock()
			if n.addrs == nil {
				n.addrs = map[string]int{}
			}
			n.addrs[addr]++
			n.mu.Unlock()
			return nil, errors.New("the test has no network: " + network + " " + addr)
		},
	})
}

func (n *srcNet) dials() int { return int(n.dial.Load()) }

// dialsOutside counts the attempts to any address but the given ones.
func (n *srcNet) dialsOutside(allowed ...string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := 0
	for addr, c := range n.addrs {
		ok := false
		for _, a := range allowed {
			ok = ok || a == addr
		}
		if !ok {
			out += c
		}
	}
	return out
}

// libraryDials are the attempts to reach the origin of a kiwix or pmtiles_extract item.
func (n *srcNet) libraryDials() int { return int(n.libDial.Load()) }

// srcRig is a node that has the kit and docs source types wired.
type srcRig struct {
	*rig
	fx     *srcFixture
	g      *srcTool
	net    *srcNet
	runner *srcRunner
	// deb is the scripted closure: its expected version and size.
	debVersion string
	debBytes   int64
	arch       string
}

// newSrcRig builds a node whose catalog has the kit items of the source types: the catalog of the
// rig, with the mirror of llama.cpp under its real name, an apt closure for this machine's
// architecture (no extra repositories: their keyrings are not embedded yet) and kit-catalog, all in
// the profile, and a roomy minimum of free disk.
func newSrcRig(t *testing.T, mode string, online bool) *srcRig {
	t.Helper()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("the kit has packages for amd64 and arm64 only")
	}
	g := newSrcTool(t)
	fx := newSrcFixture(t, g)
	arch := runtime.GOARCH
	sr := &srcRig{fx: fx, g: g, net: &srcNet{}, arch: arch}
	sr.net.set(mode, online)
	debs := []aptDeb{{"libexample-alpha", "1.0-1"}, {"tool-beta", "2.1-3"}}
	apt, version, total := aptPrintURIs(arch, debs)
	sr.debVersion, sr.debBytes = version, total

	r := newRig(t, func(r *rig) {
		r.write("etc/os-release", "NAME=\"Ubuntu\"\nVERSION_CODENAME=resolute\n", 0o644)
		// The Ubuntu archive keyring the producer copies into its private apt tree (any bytes: apt is scripted).
		r.write("usr/share/keyrings/ubuntu-archive-keyring.gpg", "keyring\n", 0o644)
		r.write("opt/localai/llama.cpp/current/BUILD.json", `{"commit":"`+fx.llamaPinned+`"}`, 0o644)
		editFile(t, r.path("etc/localai/catalog/ark.yaml"), func(s string) string {
			s = mustReplace(t, s, "git.example.test/org-alpha/llama-cpp.git", "git.example.test/org-alpha/llama.cpp.git")
			// The apt item of this machine's architecture, without extra repositories.
			start := strings.Index(s, "  - id: kit-debs-resolute-amd64")
			end := strings.Index(s, "  - id: kit-nvidia-resolute-amd64-595-open")
			if start < 0 || end < start {
				t.Fatal("the fixture catalog has no kit-debs-resolute-amd64 item")
			}
			item := "  - id: kit-debs-resolute-" + arch + "\n    kind: kit\n    source:\n      type: apt_closure\n      codename: resolute\n      arch: " + arch +
				"\n      packages: [libexample-alpha, tool-beta]\n      extra_repos: []\n\n"
			s = s[:start] + item + s[end:]
			return s + "\n  - id: kit-catalog\n    kind: kit\n    source:\n      type: repo_docs\n"
		})
		editFile(t, r.path("etc/localai/catalog/profiles/small-8gb.yaml"), func(s string) string {
			s = mustReplace(t, s, "kit-llama-cpp, kit-debs-resolute-amd64, docs-bundle", "kit-catalog, kit-llama-cpp, kit-debs-resolute-"+arch+", docs-bundle")
			return mustReplace(t, s, "min_free_bytes: 25000000000", "min_free_bytes: 1000000")
		})
		sr.runner = &srcRunner{inner: r.env.Runner, origin: map[string]string{srcMirrorURL: fx.llamaOrigin}, apt: apt}
		inner := r.env.Lookup
		r.env.Runner, r.env.Lookup = sr.runner, srcLookup(inner)
		r.env.NetState, r.env.SyncHTTP = sr.net.get, sr.net.client()
	})
	sr.rig = r
	return sr
}

func editFile(t *testing.T, path string, f func(string) string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(f(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("the fixture has no %q", old)
	}
	return strings.Replace(s, old, repl, 1)
}

// planOf is the dry-run plan of the internal root for these items.
func (sr *srcRig) planOf(items ...string) plan.RootPlan {
	sr.t.Helper()
	got := sr.do("POST", "/ark/sync", map[string]any{"dry_run": true, "items": items})
	if got.status != http.StatusOK {
		sr.t.Fatalf("dry run = %d %s", got.status, got.body)
	}
	var plans []plan.RootPlan
	if err := json.Unmarshal([]byte(got.body), &plans); err != nil || len(plans) != 1 {
		sr.t.Fatalf("dry run body %s: %v", got.body, err)
	}
	return plans[0]
}

// syncItems runs a manual sync of these items and returns the finished status of the run.
func (sr *srcRig) syncItems(items ...string) run.RunStatus {
	sr.t.Helper()
	got := sr.do("POST", "/ark/sync", map[string]any{"items": items})
	if got.status != http.StatusAccepted {
		sr.t.Fatalf("POST /ark/sync = %d %s", got.status, got.body)
	}
	id, _ := got.json()["run_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := sr.d.Components().SyncEngine.Wait(ctx, id); err != nil {
		sr.t.Fatalf("the run %s did not end: %v\n%s", id, err, sr.logText())
	}
	st, ok := sr.d.Components().SyncEngine.Status()
	if !ok || st.RunID != id {
		sr.t.Fatalf("the engine has no status for %s: %+v", id, st)
	}
	return st
}

func (sr *srcRig) root() string { return sr.path("srv/localai") }

func (sr *srcRig) manifestAt(rel string) manifest.Manifest {
	sr.t.Helper()
	m, err := manifest.Read(config.OSFS{}, manifest.Path(filepath.Join(sr.root(), filepath.FromSlash(rel))))
	if err != nil {
		sr.t.Fatalf("manifest of %s: %v", rel, err)
	}
	return m
}

// aptCanRun lets the producer run apt in the sandbox of a test: a test process may be root, and the
// producer refuses to run apt as root without the service user (which the rig does not have). The
// credential rule itself is the wiring's (registerSources); this only says the agent is not root.
func (sr *srcRig) aptCanRun() {
	sr.d.ark.sources.apt.Euid = func() int { return 65534 }
}

// catalogItem is the planner's view of one catalog item of the internal root.
func (sr *srcRig) catalogItem(id string) plan.CatalogItem {
	sr.t.Helper()
	in, err := sr.d.ark.plans.Gather(context.Background(), "internal", plan.GatherOptions{})
	if err != nil {
		sr.t.Fatalf("gather: %v", err)
	}
	for _, it := range plan.DesiredItems(in.Catalog, in.Profile, in.Overrides, in.Root, in.Node).Items {
		if it.ID == id {
			return it
		}
	}
	sr.t.Fatalf("the profile does not want %s", id)
	return plan.CatalogItem{}
}

// networkVerbs are the tool calls that reach a remote or compute from one: none may run on a node that
// must not use the network.
func networkVerbs(calls []string) []string {
	var out []string
	for _, c := range calls {
		switch c {
		case "git ls-remote", "git clone", "git fetch", "git remote", "apt-get":
			out = append(out, c)
		}
	}
	return out
}

func resultOf(st run.RunStatus, item string) (result, code string) {
	for _, it := range st.Items {
		if it.Item == item {
			return it.Result, it.Code
		}
	}
	return "", ""
}

// The three producers are registered on both registries by the real daemon, and they are bound to the
// node: the pins of the build in use, the inventory file, the network state.
func TestSourceProducersAreWiredToTheNode(t *testing.T) {
	sr := newSrcRig(t, "online", true)
	d := sr.start()
	sr.waitStarted()
	c := d.Components()
	for _, typ := range []string{"git_mirror", "repo_docs", "apt_closure"} {
		if _, ok := c.SyncResolvers.For(typ); !ok {
			t.Errorf("no planner resolver for %s", typ)
		}
		if _, ok := c.SyncItemResolvers.TaskFor(typ); !ok {
			t.Errorf("no engine resolver for %s", typ)
		}
	}
	// The library and maps types (SYNC-10) are plain resolvers: kiwix and pmtiles_extract.
	if r, ok := c.SyncResolvers.For("kiwix"); !ok {
		t.Error("no planner resolver for kiwix")
	} else if _, ok := r.(*resolve.Kiwix); !ok {
		t.Errorf("the kiwix planner resolver is a %T", r)
	}
	if r, ok := c.SyncResolvers.For("pmtiles_extract"); !ok {
		t.Error("no planner resolver for pmtiles_extract")
	} else if pm, ok := r.(*resolve.PMTiles); !ok {
		t.Errorf("the pmtiles_extract planner resolver is a %T", r)
	} else if want := sr.path("opt/localai/pmtiles/current/pmtiles"); pm.Tool != want {
		t.Errorf("the pmtiles tool is %s, want %s", pm.Tool, want)
	}
	for _, typ := range []string{"kiwix", "pmtiles_extract"} {
		if _, ok := c.SyncItemResolvers.For(typ); !ok {
			t.Errorf("no engine resolver for %s", typ)
		}
	}

	// The pins come from the BUILD.json of the build in use (40 hex), not from node.yaml's short form.
	if got := d.ark.facts.mirrorCommits("llama.cpp"); len(got) != 1 || got[0] != sr.fx.llamaPinned {
		t.Errorf("llama.cpp pins = %v, want [%s]", got, sr.fx.llamaPinned)
	}
	if got := d.ark.facts.mirrorCommits("whisper.cpp"); len(got) != 0 {
		t.Errorf("whisper.cpp pins = %v before a build exists", got)
	}
	if got := d.ark.facts.mirrorCommits("other.cpp"); len(got) != 0 {
		t.Errorf("a mirror the node does not build has pins %v", got)
	}
	whisperPin := strings.Repeat("ab12", 10)
	sr.write("opt/localai/whisper.cpp/current/BUILD.json", `{"commit":"`+whisperPin+`"}`, 0o644)
	if got := d.ark.facts.mirrorCommits("whisper.cpp"); len(got) != 1 || got[0] != whisperPin {
		t.Errorf("whisper.cpp pins = %v, want [%s]", got, whisperPin)
	}

	// The inventory: none, then the node's, then (without the node's) the one of a mounted drive.
	if got := d.ark.inventory(); got != "" {
		t.Errorf("inventory with no file = %q", got)
	}
	sr.write("etc/localai/inventory.local.yaml", "schema_version: 1\n", 0o600)
	if got := d.ark.inventory(); got != sr.path("etc/localai/inventory.local.yaml") {
		t.Errorf("inventory = %q", got)
	}
	if err := os.Remove(sr.path("etc/localai/inventory.local.yaml")); err != nil {
		t.Fatal(err)
	}
	mount := sr.path("media/ark")
	sr.write("media/ark/inventory.local.yaml", "schema_version: 1\n", 0o600)
	d.ark.roots.SetDrives([]ark.Root{{ID: "ark-0a1b2c3d", Kind: ark.KindArkDrive, Path: filepath.Join(mount, "localai"), Mount: mount}})
	if got := d.ark.inventory(); got != filepath.Join(mount, "inventory.local.yaml") {
		t.Errorf("drive inventory = %q", got)
	}

	// The second lock: reachable internet and not survival. A producer asked to resolve while it does
	// not hold refuses with LAI-SYN-002 before any tool runs.
	for _, tc := range []struct {
		mode   string
		online bool
		want   bool
	}{{"online", true, true}, {"online", false, false}, {"offline", false, false}, {"survival", true, false}, {"survival", false, false}} {
		sr.net.set(tc.mode, tc.online)
		if got := d.sourcesOnline(); got != tc.want {
			t.Errorf("sourcesOnline in %s mode, internet %v = %v, want %v", tc.mode, tc.online, got, tc.want)
		}
	}
	sr.net.set("offline", false)
	_, err := d.ark.sources.git.Resolve(context.Background(), sr.catalogItem("kit-llama-cpp"))
	if code := resolve.CodeOf(err); code != doctor.SYN002 {
		t.Errorf("a git_mirror resolve while offline failed with %q (%v), want %s", code, err, doctor.SYN002)
	}
	if calls := sr.runner.calls(); len(calls) != 0 {
		t.Errorf("tools ran while offline: %v", calls)
	}
}

// sync.apt_unverified_repos is wired to the apt producer: allow by default (the owner's decision for
// now, issue #274), and a change of node.yaml applies to the next closure without a restart.
func TestAptUnverifiedReposSettingIsWiredAndReloads(t *testing.T) {
	sr := newSrcRig(t, "online", true)
	d := sr.start()
	sr.waitStarted()
	apt := d.ark.sources.apt
	if apt.AllowUnverifiedRepos == nil || !apt.AllowUnverifiedRepos() {
		t.Fatal("the default of sync.apt_unverified_repos must be allow")
	}
	for _, tc := range []struct {
		name, block string
		want        bool
	}{
		{"refuse", "sync:\n  apt_unverified_repos: refuse\n", false},
		{"allow written out", "sync:\n  apt_unverified_repos: allow\n", true},
		{"another sync key leaves the default", "sync:\n  concurrency: 3\n", true},
		{"refuse again", "sync:\n  apt_unverified_repos: refuse\n", false},
		{"the key removed", "", true},
	} {
		sr.writeNodeYAMLText(sr.nodeYAML() + tc.block)
		d.Components().Config.ReloadNow()
		if got := apt.AllowUnverifiedRepos(); got != tc.want {
			t.Errorf("%s: allow = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A value the validator refuses never reaches the producer: the config store keeps the last valid
	// file, so the setting stays what it was.
	sr.writeNodeYAMLText(sr.nodeYAML() + "sync:\n  apt_unverified_repos: refuse\n")
	d.Components().Config.ReloadNow()
	sr.writeNodeYAMLText(sr.nodeYAML() + "sync:\n  apt_unverified_repos: maybe\n")
	d.Components().Config.ReloadNow()
	if apt.AllowUnverifiedRepos() {
		t.Error("an invalid value replaced the last valid refuse")
	}
}

// An online node plans the three source types to the right items and a run of them goes through the
// engine end to end: the llama.cpp mirror with its tarball, the docs and the kit catalog as two items
// of the one repo_docs source, and (planned only) the apt closure. This is `aictl ark sync --item
// kit-llama-cpp --item docs-bundle --item kit-catalog --wait` over fakes. The docs are built from the
// lab repository's mirror on the root (repo_docs prefers it to the network), so nothing dials out.
func TestSourceItemsAreResolvedAndSyncedOnline(t *testing.T) {
	sr := newSrcRig(t, "online", true)
	sr.fx.seed(t, sr.g, sr.root())
	sr.start()
	sr.waitStarted()
	sr.aptCanRun()
	pinned, version := sr.fx.llamaPinned, sr.fx.version

	if _, err := sr.d.ark.plans.Gather(context.Background(), "internal", plan.GatherOptions{Resolve: true}); err != nil {
		t.Fatalf("gather: %v", err)
	}
	// The planner gave the resolver of the apt closure far more than its 5 s default: a closure takes
	// minutes, and with the default it would stay unresolved on a real node. (Gathered here with a
	// background context: the dry-run route has a request deadline of its own.)
	sr.runner.mu.Lock()
	budgets := append([]time.Duration(nil), sr.runner.aptBudget...)
	sr.runner.mu.Unlock()
	if len(budgets) == 0 {
		t.Error("apt-get never ran with a deadline")
	}
	for _, b := range budgets {
		if b <= 10*plan.DefaultResolveTimeout {
			t.Errorf("apt-get ran with %v left, want the long resolve timeout of the wiring", b)
		}
	}

	// The plan: each item is a row with its own directory and version.
	p := sr.planOf("kit-llama-cpp", "docs-bundle", "kit-catalog", "kit-debs-resolute-"+sr.arch)
	if p.Offline {
		t.Error("the plan of an online node is offline")
	}
	want := map[string]struct{ dir, version string }{
		"kit-llama-cpp":                {"kit/git/llama.cpp.git", pinned[:12]},
		"docs-bundle":                  {"docs", "catalog-" + version},
		"kit-catalog":                  {"kit/catalog", "catalog-" + version},
		"kit-debs-resolute-" + sr.arch: {"kit/debs/resolute-" + sr.arch, sr.debVersion},
	}
	if len(p.Items) != len(want) {
		t.Fatalf("the plan has %d rows, want %d: %+v", len(p.Items), len(want), p.Items)
	}
	for _, it := range p.Items {
		w, ok := want[it.ID]
		if !ok {
			t.Errorf("unexpected row %s", it.ID)
			continue
		}
		if it.Action != plan.ActionDownload || it.Dir != w.dir || it.ToVersion != w.version {
			t.Errorf("%s: action %s dir %q version %q, want download %q %q (reason %q)", it.ID, it.Action, it.Dir, it.ToVersion, w.dir, w.version, it.Reason)
		}
		if it.ID == "kit-debs-resolute-"+sr.arch && it.FinalBytes != sr.debBytes {
			t.Errorf("the apt closure weighs %d, want %d", it.FinalBytes, sr.debBytes)
		}
	}

	// The run.
	st := sr.syncItems("kit-llama-cpp", "docs-bundle", "kit-catalog")
	if st.State != run.StateDone || st.Result != run.ResultOK {
		t.Fatalf("the run ended %+v\n%s", st, sr.logText())
	}
	for _, id := range []string{"kit-llama-cpp", "docs-bundle", "kit-catalog"} {
		if res, code := resultOf(st, id); res != "verified" && res != "done" {
			t.Errorf("%s ended %q %s: %v", id, res, code, st.Items)
		}
	}

	// The mirror: its own manifest at the pinned version, the pinned commit inside, and the tarball in
	// kit/src recorded in that manifest where the rebuild kit looks for it.
	mm := sr.manifestAt("kit/git/llama.cpp.git")
	if mm.ItemID != "kit-llama-cpp" || mm.State != manifest.StateVerified || mm.Version != pinned[:12] {
		t.Errorf("mirror manifest = %+v", mm)
	}
	mirror := filepath.Join(sr.root(), "kit", "git", "llama.cpp.git")
	sr.g.must("-c", "safe.directory="+mirror, "-C", mirror, "cat-file", "-e", pinned+"^{commit}")
	tb, err := (kit.SourceFinder{Roots: []string{sr.root()}}).FindTarball("llama.cpp", pinned)
	if err != nil || tb.Commit != pinned {
		t.Fatalf("the kit finds no tarball of %s: %+v %v", pinned, tb, err)
	}
	if _, err := os.Stat(filepath.Join(sr.root(), "kit", "src", "llama.cpp-"+pinned+".tar.gz")); err != nil {
		t.Errorf("the tarball is not in kit/src: %v", err)
	}

	// The docs and the catalog: two items of one source, each with its own manifest.
	for _, w := range []struct{ rel, id string }{{"docs", "docs-bundle"}, {"kit/catalog", "kit-catalog"}} {
		m := sr.manifestAt(w.rel)
		if m.ItemID != w.id || m.State != manifest.StateVerified || m.Version != "catalog-"+version {
			t.Errorf("%s manifest = %+v", w.rel, m)
		}
	}
	for _, f := range []string{"docs/index.html", "kit/catalog/CATALOG_VERSION", "kit/catalog/ark.yaml"} {
		if _, err := os.Stat(filepath.Join(sr.root(), filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sr.root(), "kit", "catalog", "index.html")); err == nil {
		t.Error("the manuals were installed into the catalog")
	}
	// The kit and docs sources use no network of their own here (git and apt go through the runner). The
	// only dials are the planner's HEAD of the places item, a trust-on-first-use url item that has no pin
	// to plan from; the catalog's other url items are planned from what it pins.
	if n := sr.net.dialsOutside("data.example.test:443"); n != 0 {
		t.Errorf("the sync dialled out %d times to hosts other than the url item's", n)
	}

	// A second run finds everything current: no clone, no new archive.
	before := sr.runner.calls()
	st = sr.syncItems("kit-llama-cpp", "docs-bundle", "kit-catalog")
	if st.State != run.StateDone || st.Result != run.ResultOK {
		t.Fatalf("the second run ended %+v\n%s", st, sr.logText())
	}
	count := func(calls []string, what string) int {
		n := 0
		for _, c := range calls {
			if c == what {
				n++
			}
		}
		return n
	}
	after := sr.runner.calls()
	if count(after, "git clone") != count(before, "git clone") || count(after, "gzip") != count(before, "gzip") {
		t.Errorf("the second run produced again: %v", after[len(before):])
	}
}

// getRun is GET /ark/sync as the operator's tool sees it.
func (sr *srcRig) getRun() (body map[string]any, items map[string]map[string]any) {
	sr.t.Helper()
	got := sr.do("GET", "/ark/sync", nil)
	if got.status != http.StatusOK {
		sr.t.Fatalf("GET /ark/sync = %d %s", got.status, got.body)
	}
	body = got.json()
	items = map[string]map[string]any{}
	list, _ := body["items"].([]any)
	for _, v := range list {
		m, _ := v.(map[string]any)
		id, _ := m["item"].(string)
		items[id] = m
	}
	return body, items
}

// engineRows is the plan the run engine would make now, for the internal root, with the engine's own
// verdict on the network: what a real run of these flags plans (not the dry-run route's).
func (sr *srcRig) engineRows(allowOfflineMode bool) plan.RootPlan {
	sr.t.Helper()
	n := sr.net.get()
	pl := adapt.GathererPlanner{G: sr.d.ark.plans}
	plans, err := pl.Plan(context.Background(), run.PlanRequest{RootIDs: []string{"internal"}, Offline: !plan.Network{Mode: n.Mode, Online: n.Online}.MayUse(allowOfflineMode)})
	if err != nil || len(plans.Plans) != 1 {
		sr.t.Fatalf("engine plan: %v %+v", err, plans)
	}
	return plans.Plans[0]
}

// rowsOf keeps the rows of these items, as JSON, so two plans compare byte for byte.
func rowsOf(t *testing.T, p plan.RootPlan, ids ...string) string {
	t.Helper()
	var rows []plan.ItemPlan
	for _, id := range ids {
		it, ok := p.Item(id)
		if !ok {
			t.Fatalf("no row for %s", id)
		}
		rows = append(rows, it)
	}
	b, err := json.Marshal(struct {
		Offline bool
		Rows    []plan.ItemPlan
	}{p.Offline, rows})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A node with no internet, with the lab repository's mirror on its root (#275): the docs and the kit
// catalog are planned and built through the engine from that mirror, with no network at all, while the
// llama.cpp mirror and the apt closure, which need the network, are skipped with their reason. No tool
// that reaches a remote runs, nothing dials out, and the dry run shows what the run does.
func TestOfflineNodeBuildsFromALocalMirrorAndSkipsWhatNeedsTheNetwork(t *testing.T) {
	sr := newSrcRig(t, "online", false)
	sr.fx.seed(t, sr.g, sr.root())
	sr.start()
	sr.waitStarted()
	sr.aptCanRun()
	all := []string{"kit-llama-cpp", "docs-bundle", "kit-catalog", "kit-debs-resolute-" + sr.arch}

	p := sr.planOf(all...)
	if !p.Offline {
		t.Error("the plan of a node with no internet says it is online")
	}
	for _, it := range p.Items {
		switch it.ID {
		case "docs-bundle", "kit-catalog":
			if it.Action != plan.ActionDownload || it.Source != plan.SourceLocalBuild || it.ToVersion != "catalog-"+sr.fx.version {
				t.Errorf("%s: %s source %q version %q, want a local build of the catalog's version", it.ID, it.Action, it.Source, it.ToVersion)
			}
		default:
			if it.Action != plan.ActionSkip || it.Reason != plan.ReasonUnresolved || it.ToVersion != "" {
				t.Errorf("%s: %s (%s) version %q, want skip unresolved", it.ID, it.Action, it.Reason, it.ToVersion)
			}
		}
	}
	// The dry run shows what the run plans.
	if got, want := rowsOf(t, p, all...), rowsOf(t, sr.engineRows(false), all...); got != want {
		t.Errorf("the dry run and the run's plan differ:\n dry run %s\n run     %s", got, want)
	}
	if v := networkVerbs(sr.runner.calls()); len(v) != 0 {
		t.Errorf("planning ran network verbs: %v", v)
	}

	st := sr.syncItems(all...)
	if st.State != run.StateDone {
		t.Fatalf("the run ended %+v\n%s", st, sr.logText())
	}
	for _, id := range []string{"docs-bundle", "kit-catalog"} {
		if res, code := resultOf(st, id); res != "verified" && res != "done" {
			t.Errorf("%s ended %q %s, want it built from the mirror: %v\n%s", id, res, code, st.Items, sr.logText())
		}
	}
	for _, id := range []string{"kit-llama-cpp", "kit-debs-resolute-" + sr.arch} {
		if res, _ := resultOf(st, id); res != "skipped" {
			t.Errorf("%s ended %q, want skipped (%v)", id, res, st.Items)
		}
	}
	if v := networkVerbs(sr.runner.calls()); len(v) != 0 {
		t.Errorf("network verbs ran on a node with no internet: %v", v)
	}
	if n := sr.net.dials(); n != 0 {
		t.Errorf("the node dialled out %d times", n)
	}
	for _, w := range []struct{ rel, id string }{{"docs", "docs-bundle"}, {"kit/catalog", "kit-catalog"}} {
		m := sr.manifestAt(w.rel)
		if m.ItemID != w.id || m.State != manifest.StateVerified || m.Version != "catalog-"+sr.fx.version {
			t.Errorf("%s manifest = %+v", w.rel, m)
		}
	}
	for _, f := range []string{"docs/index.html", "kit/catalog/CATALOG_VERSION"} {
		if _, err := os.Stat(filepath.Join(sr.root(), filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, rel := range []string{"kit/git/llama.cpp.git", "kit/debs"} {
		if _, err := os.Stat(filepath.Join(sr.root(), filepath.FromSlash(rel), manifest.FileName)); err == nil {
			t.Errorf("%s was installed", rel)
		}
	}

	// GET /ark/sync says what became of each item (the spec: the ended run carries items), and the
	// totals the CLI's exit code reads: two skipped, none failed.
	body, items := sr.getRun()
	totals, _ := body["totals"].(map[string]any)
	if totals["failed"] != float64(0) || totals["skipped"] != float64(2) {
		t.Errorf("totals = %v, want 0 failed and 2 skipped", totals)
	}
	if len(items) != 4 {
		t.Fatalf("GET /ark/sync items = %v, want one per item", body["items"])
	}
	for id, want := range map[string]string{"docs-bundle": "verified", "kit-catalog": "verified", "kit-llama-cpp": "skipped", "kit-debs-resolute-" + sr.arch: "skipped"} {
		got := items[id]
		if got["result"] != want && !(want == "verified" && got["result"] == "done") {
			t.Errorf("GET item %s = %v, want %s", id, got, want)
		}
		if want == "skipped" && got["reason"] != string(plan.ReasonUnresolved) {
			t.Errorf("GET item %s has no reason: %v", id, got)
		}
	}

	// A second offline run finds the docs and the catalog current and builds nothing again.
	archives := func() int {
		n := 0
		for _, c := range sr.runner.calls() {
			if c == "git archive" {
				n++
			}
		}
		return n
	}
	built := archives()
	if st = sr.syncItems("docs-bundle", "kit-catalog"); st.State != run.StateDone || st.Result != run.ResultOK {
		t.Fatalf("the second run ended %+v\n%s", st, sr.logText())
	}
	if archives() != built {
		t.Error("the second offline run built the docs again")
	}
}

// With no internet and no mirror on any root, nothing can be built: every source item is skipped,
// no tool runs and nothing dials out (the offline-first rule of #271 kept by #275).
func TestOfflineNodeWithoutAMirrorSkipsEverySourceItem(t *testing.T) {
	sr := newSrcRig(t, "online", false)
	sr.start()
	sr.waitStarted()
	sr.aptCanRun()
	all := []string{"kit-llama-cpp", "docs-bundle", "kit-catalog", "kit-debs-resolute-" + sr.arch}
	before := sr.runner.calls()

	p := sr.planOf(all...)
	if !p.Offline {
		t.Error("the plan of a node with no internet says it is online")
	}
	for _, it := range p.Items {
		if it.Action != plan.ActionSkip || it.Reason != plan.ReasonUnresolved {
			t.Errorf("%s: %s (%s), want skip unresolved", it.ID, it.Action, it.Reason)
		}
		switch it.ID {
		case "docs-bundle", "kit-catalog":
			if it.ToVersion != "catalog-"+sr.fx.version {
				t.Errorf("%s: version %q, want the catalog's pin", it.ID, it.ToVersion)
			}
		default:
			if it.ToVersion != "" {
				t.Errorf("%s: version %q from nowhere", it.ID, it.ToVersion)
			}
		}
	}

	st := sr.syncItems(all[:3]...)
	if st.State != run.StateDone {
		t.Fatalf("the run ended %v", st)
	}
	for _, id := range all[:3] {
		if res, _ := resultOf(st, id); res != "skipped" {
			t.Errorf("%s ended %q, want skipped (%v)", id, res, st.Items)
		}
	}
	if calls := sr.runner.calls(); len(calls) != len(before) {
		t.Errorf("tools ran on a node with no internet: %v", calls[len(before):])
	}
	if n := sr.net.dials(); n != 0 {
		t.Errorf("the node dialled out %d times", n)
	}
	for _, rel := range []string{"kit/git/llama.cpp.git", "docs", "kit/catalog"} {
		if _, err := os.Stat(filepath.Join(sr.root(), filepath.FromSlash(rel), manifest.FileName)); err == nil {
			t.Errorf("%s was installed", rel)
		}
	}
}

// In offline mode with the internet reachable, a run with allow_offline_mode uses the network: the
// resolvers run and every source item is planned and built, as in online mode (#275). Without the
// flag, the same node plans offline, and the run is refused (LAI-SYN-011). The dry run follows the
// same rule as the run.
func TestAllowOfflineModeResolvesEverySourceItem(t *testing.T) {
	sr := newSrcRig(t, "offline", true)
	sr.fx.seed(t, sr.g, sr.root())
	sr.start()
	sr.waitStarted()
	sr.aptCanRun()
	pinned, version := sr.fx.llamaPinned, sr.fx.version
	all := []string{"kit-llama-cpp", "docs-bundle", "kit-catalog", "kit-debs-resolute-" + sr.arch}

	// Without the flag: offline, the llama mirror and the apt closure are skipped, and the run is refused.
	p := sr.planOf(all...)
	if !p.Offline {
		t.Error("the dry run of an offline-mode node without the flag is not offline")
	}
	if it, _ := p.Item("kit-llama-cpp"); it.Action != plan.ActionSkip {
		t.Errorf("kit-llama-cpp = %+v, want skipped while offline", it)
	}
	if got := sr.do("POST", "/ark/sync", map[string]any{"items": all}); got.status != http.StatusConflict || errCode(got) != doctor.SYN011 {
		t.Errorf("POST /ark/sync in offline mode = %d %s, want 409 %s", got.status, got.body, doctor.SYN011)
	}

	// With the flag, the dry run resolves everything...
	dry := sr.do("POST", "/ark/sync", map[string]any{"dry_run": true, "items": all, "allow_offline_mode": true})
	if dry.status != http.StatusOK {
		t.Fatalf("dry run = %d %s", dry.status, dry.body)
	}
	var plans []plan.RootPlan
	if err := json.Unmarshal([]byte(dry.body), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("dry run body %s: %v", dry.body, err)
	}
	p = plans[0]
	if p.Offline {
		t.Error("an allow_offline_mode dry run with the internet reachable is offline")
	}
	want := map[string]string{
		"kit-llama-cpp":                pinned[:12],
		"docs-bundle":                  "catalog-" + version,
		"kit-catalog":                  "catalog-" + version,
		"kit-debs-resolute-" + sr.arch: sr.debVersion,
	}
	for _, it := range p.Items {
		if it.Action != plan.ActionDownload || it.ToVersion != want[it.ID] || it.Source != plan.SourceNetwork {
			t.Errorf("%s: %s source %q version %q, want a network download of %q (reason %q)", it.ID, it.Action, it.Source, it.ToVersion, want[it.ID], it.Reason)
		}
	}
	// ...and it is the plan the run makes.
	if got, want := rowsOf(t, p, all...), rowsOf(t, sr.engineRows(true), all...); got != want {
		t.Errorf("the dry run and the run's plan differ:\n dry run %s\n run     %s", got, want)
	}

	// The run: the llama mirror is cloned (the fixture's origin stands for the network), and the docs
	// and catalog are built.
	got := sr.do("POST", "/ark/sync", map[string]any{"items": []string{"kit-llama-cpp", "docs-bundle", "kit-catalog"}, "allow_offline_mode": true})
	if got.status != http.StatusAccepted {
		t.Fatalf("POST /ark/sync with allow_offline_mode = %d %s", got.status, got.body)
	}
	id, _ := got.json()["run_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := sr.d.Components().SyncEngine.Wait(ctx, id); err != nil {
		t.Fatalf("the run %s did not end: %v\n%s", id, err, sr.logText())
	}
	st, _ := sr.d.Components().SyncEngine.Status()
	if st.State != run.StateDone || st.Result != run.ResultOK {
		t.Fatalf("the run ended %+v\n%s", st, sr.logText())
	}
	for _, it := range []string{"kit-llama-cpp", "docs-bundle", "kit-catalog"} {
		if res, code := resultOf(st, it); res != "verified" && res != "done" {
			t.Errorf("%s ended %q %s: %v", it, res, code, st.Items)
		}
	}
	if m := sr.manifestAt("kit/git/llama.cpp.git"); m.State != manifest.StateVerified || m.Version != pinned[:12] {
		t.Errorf("mirror manifest = %+v", m)
	}
}

// A producer that is reached while the node must not use the network fails fast with its own doctor
// code and touches nothing: no tool runs and nothing dials out. The lab repository's mirror, when the
// root has one, still builds the docs (that is local), and without it the docs fail with LAI-SYN-024.
func TestProducersFailFastWhenOffline(t *testing.T) {
	sr := newSrcRig(t, "offline", false)
	sr.start()
	sr.waitStarted()
	sr.aptCanRun()
	d := sr.d
	ctx := context.Background()
	req := func(id, version string) resolve.Request {
		return resolve.Request{
			Item: sr.catalogItem(id), RootID: "internal", Root: sr.root(), Version: version, CatalogVersion: sr.fx.version,
			Checkpoints: nil, MinFreeBytes: 0,
		}
	}
	before := sr.runner.calls()

	// git_mirror: refused before git runs (an offline mirror is never cloned from nowhere).
	if _, err := d.ark.sources.git.Produce(ctx, req("kit-llama-cpp", sr.fx.llamaPinned[:12])); resolve.CodeOf(err) != doctor.SYN002 {
		t.Errorf("git_mirror Produce offline: %v, want %s", err, doctor.SYN002)
	}
	// apt_closure: the closure is not computed (apt-get is never run).
	if _, err := d.ark.sources.apt.Resolve(ctx, sr.catalogItem("kit-debs-resolute-"+sr.arch)); resolve.CodeOf(err) != doctor.SYN002 {
		t.Errorf("apt_closure Resolve offline: %v, want %s", err, doctor.SYN002)
	}
	if _, err := d.ark.sources.apt.Produce(ctx, req("kit-debs-resolute-"+sr.arch, sr.debVersion)); resolve.CodeOf(err) != doctor.SYN002 {
		t.Errorf("apt_closure Produce offline: %v, want %s", err, doctor.SYN002)
	}
	// repo_docs: no mirror on any root and no network is LAI-SYN-024, in plain words.
	docsReq := req("docs-bundle", "catalog-"+sr.fx.version)
	docsReq.Want = resolve.PartNameDocs
	_, err := d.ark.sources.docs.Produce(ctx, docsReq)
	if code := resolve.CodeOf(err); code != doctor.SYN024 {
		t.Errorf("repo_docs Produce offline with no mirror: %v, want %s", err, doctor.SYN024)
	}
	if calls := sr.runner.calls(); len(calls) != len(before) {
		t.Errorf("tools ran: %v", calls[len(before):])
	}
	if n := sr.net.dials(); n != 0 {
		t.Errorf("the producers dialled out %d times", n)
	}

	// With the mirror on the root the docs are built offline.
	sr.fx.seed(t, sr.g, sr.root())
	prod, err := d.ark.sources.docs.Produce(ctx, docsReq)
	if err != nil || len(prod.Parts) == 0 || prod.Version != "catalog-"+sr.fx.version {
		t.Fatalf("repo_docs Produce offline with a mirror: %+v %v", prod, err)
	}
	if v := networkVerbs(sr.runner.calls()); len(v) != 0 {
		t.Errorf("network verbs ran: %v", v)
	}
	if n := sr.net.dials(); n != 0 {
		t.Errorf("the producers dialled out %d times", n)
	}
}

// In survival mode a sync is refused before any producer is asked: 409 LAI-SYN-010, nothing runs.
func TestSurvivalModeRefusesTheSourceRun(t *testing.T) {
	sr := newSrcRig(t, "survival", true)
	sr.start()
	sr.waitStarted()
	before := sr.runner.calls()
	got := sr.do("POST", "/ark/sync", map[string]any{"items": []string{"kit-llama-cpp", "docs-bundle", "kit-catalog"}})
	if got.status != http.StatusConflict || errCode(got) != doctor.SYN010 {
		t.Errorf("POST /ark/sync in survival mode = %d %s, want 409 %s", got.status, got.body, doctor.SYN010)
	}
	// allow_offline_mode is for the offline mode only: survival still refuses.
	got = sr.do("POST", "/ark/sync", map[string]any{"items": []string{"kit-llama-cpp"}, "allow_offline_mode": true})
	if got.status != http.StatusConflict || errCode(got) != doctor.SYN010 {
		t.Errorf("POST /ark/sync in survival mode with allow_offline_mode = %d %s, want 409 %s", got.status, got.body, doctor.SYN010)
	}
	// And a dry run in survival mode plans offline even with the flag.
	if p := sr.planOf("kit-llama-cpp"); !p.Offline {
		t.Error("the dry run in survival mode is not offline")
	}
	dry := sr.do("POST", "/ark/sync", map[string]any{"dry_run": true, "items": []string{"kit-llama-cpp"}, "allow_offline_mode": true})
	var plans []plan.RootPlan
	if err := json.Unmarshal([]byte(dry.body), &plans); dry.status != http.StatusOK || err != nil || len(plans) != 1 || !plans[0].Offline {
		t.Errorf("the dry run in survival mode with allow_offline_mode = %d %s", dry.status, dry.body)
	}
	if calls := sr.runner.calls(); len(calls) != len(before) {
		t.Errorf("tools ran in survival mode: %v", calls[len(before):])
	}
	if n := sr.net.dials(); n != 0 {
		t.Errorf("the node dialled out %d times", n)
	}
}
