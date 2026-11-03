package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// Run-time bounds of the apt subprocesses and the lifetime of a computed closure.
const (
	aptUpdateTimeout = 15 * time.Minute
	aptQueryTimeout  = 10 * time.Minute
	// closureTTL is how long a computed closure is reused: the planner's Resolve and the engine's
	// Produce of one run share it, so they agree on the version.
	closureTTL = 6 * time.Hour
)

// ErrVersionMoved is wrapped when the package set Produce computed is not the version the plan was
// made for (a security update landed in between). Nothing was staged; the next run picks it up.
var ErrVersionMoved = errors.New("resolve: the package set changed while syncing")

// AptClosure is the apt_closure resolver (ark-sync.md section 4.9.7): the complete set of .deb files
// needed to install a package list on a fresh Ubuntu codename and architecture, computed without
// root, without touching the system's apt state and without a chroot, and laid out as a flat local
// apt repository (pool/, Packages, Packages.gz, PACKAGES.lock, README.txt).
//
// The private apt tree lives at <SyncHome>/apt/<item id>/ (the spec's per-codename path would let the
// kit-nvidia items of one codename share and thrash a tree). Apt reads its own configuration only
// from that tree: APT_CONFIG names a file that sets Dir first, so the hooks in the system's
// /etc/apt/apt.conf.d never run (they delete files under /var/cache/apt and call PackageKit).
type AptClosure struct {
	Env *Env
	// Keyrings returns an embedded keyring by file name (catalog.EmbeddedKeyrings). #52 provides it
	// over an embed.FS. For the Ubuntu archive keyring a missing entry falls back to
	// SystemKeyringDir, the copy of the build machine's /usr/share/keyrings.
	Keyrings func(name string) ([]byte, error)
	// SystemKeyringDir is where the Ubuntu archive keyring is read when Keyrings has none; default
	// /usr/share/keyrings.
	SystemKeyringDir string
	// Mirror replaces the Ubuntu archive URI (a lab mirror, or a test server). "" uses
	// archive.ubuntu.com for amd64 and ports.ubuntu.com for the rest. Never read from the catalog.
	Mirror string
	// Credential is the unprivileged user apt runs as (the localai service user). The agent must
	// not run apt as root: with a nil Credential and a root agent, Produce and Resolve refuse.
	Credential *execx.Credential
	// User is that user's name for APT::Sandbox::User; default the invoking user.
	User string
	// Euid reports the effective uid of the agent; default os.Geteuid. A seam for tests.
	Euid func() int

	mu    sync.Mutex
	cache map[string]*Closure
	locks map[string]*sync.Mutex
}

// SourceType implements NamedResolver.
func (*AptClosure) SourceType() string { return "apt_closure" }

// Closure is a computed package set.
type Closure struct {
	// Version is the first 12 hex of the sha256 of the sorted name=version list.
	Version string
	Pkgs    []Pkg
	// At is when it was computed.
	At time.Time

	key    string
	src    aptSource
	tree   string
	wanted []string
}

// Size is the total size of the debs.
func (c *Closure) Size() int64 { return totalSize(c.Pkgs) }

// item validates an apt_closure item and returns its source description and sorted, de-duplicated
// package list.
func (a *AptClosure) item(it plan.CatalogItem) (aptSource, []string, error) {
	if it.Content == nil || it.Content.Source.Type != "apt_closure" {
		return aptSource{}, nil, fail(CodeApt, nil, "item %s is not an apt_closure item", it.ID)
	}
	if !manifest.SafeName(it.ID) || strings.ContainsAny(it.ID, " ") {
		return aptSource{}, nil, fail(CodeApt, nil, "item id %q is not usable", it.ID)
	}
	s := it.Content.Source
	src := aptSource{Codename: s.Codename, Arch: s.Arch, Mirror: a.Mirror, Extra: s.ExtraRepos}
	if err := src.validate(); err != nil {
		return aptSource{}, nil, err
	}
	set := map[string]bool{}
	for _, p := range s.Packages {
		if !pkgNameRe.MatchString(p) {
			return aptSource{}, nil, fail(CodeApt, nil, "%q is not a Debian package name", Clip([]byte(p), 60))
		}
		set[p] = true
	}
	if len(set) == 0 {
		return aptSource{}, nil, fail(CodeApt, nil, "item %s lists no packages", it.ID)
	}
	wanted := make([]string, 0, len(set))
	for p := range set {
		wanted = append(wanted, p)
	}
	sort.Strings(wanted)
	return src, wanted, nil
}

func (a *AptClosure) euid() int {
	if a.Euid != nil {
		return a.Euid()
	}
	return os.Geteuid()
}

func (a *AptClosure) sandboxUser() string {
	if a.User != "" {
		return a.User
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func (a *AptClosure) treeLock(tree string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks == nil {
		a.locks = map[string]*sync.Mutex{}
	}
	l, ok := a.locks[tree]
	if !ok {
		l = &sync.Mutex{}
		a.locks[tree] = l
	}
	return l
}

func (a *AptClosure) aptEnv(tree string) []string {
	return []string{
		"PATH=" + execx.FixedPath,
		"LC_ALL=C",
		"APT_CONFIG=" + tree + "/etc/apt/apt.conf",
		"HOME=" + tree + "/home",
	}
}

// Resolve implements plan.Resolver. It computes the closure (apt-get update and --print-uris), which
// takes minutes: callers that resolve with a short timeout (the planner's default is 5 s) leave the
// item unresolved, and the engine's run, which uses a longer one, resolves it. The result is cached
// for closureTTL so the engine's Produce does not repeat it.
func (a *AptClosure) Resolve(ctx context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	c, err := a.closure(ctx, it)
	if err != nil {
		return plan.Resolved{}, err
	}
	return plan.Resolved{Version: c.Version, SortKey: c.At.UTC().Format(time.RFC3339), SizeBytes: c.Size()}, nil
}

// Closure computes (or returns the cached) package set of an item.
func (a *AptClosure) Closure(ctx context.Context, it plan.CatalogItem) (*Closure, error) {
	return a.closure(ctx, it)
}

func (a *AptClosure) closure(ctx context.Context, it plan.CatalogItem) (*Closure, error) {
	src, wanted, err := a.item(it)
	if err != nil {
		return nil, err
	}
	if err := a.Env.requireOnline(); err != nil {
		return nil, err
	}
	key := it.ID + "|" + src.Codename + "|" + src.Arch + "|" + a.Mirror + "|" + strings.Join(wanted, ",") + "|" + extraKey(src)
	a.mu.Lock()
	if c, ok := a.cache[key]; ok && a.Env.now().Sub(c.At) < closureTTL {
		a.mu.Unlock()
		return c, nil
	}
	a.mu.Unlock()
	if a.Env.SyncHome == "" {
		return nil, fail(CodeApt, nil, "the sync directory is not configured")
	}
	if a.Credential == nil && a.euid() == 0 {
		return nil, fail(CodeApt, nil, "apt must not run as root and no unprivileged user is configured")
	}
	aptGet, err := a.Env.tool("apt-get")
	if err != nil {
		return nil, err
	}

	tree := filepath.Join(a.Env.SyncHome, "apt", it.ID)
	l := a.treeLock(tree)
	l.Lock()
	defer l.Unlock()

	proxy, stop, perr := a.Env.startProxy(ctx, src.proxyHosts())
	if perr != nil {
		return nil, fail(CodeApt, perr, "cannot start the bandwidth limiter")
	}
	defer stop()
	src.HTTPS = proxy != ""
	if err := a.prepareTree(tree, src); err != nil {
		return nil, err
	}
	opts := aptOptions(tree, src.Arch, a.sandboxUser(), proxy)

	if err := a.aptStep(ctx, aptGet, tree, "updating the package lists", aptUpdateTimeout, append(append([]string{}, opts...), "update")); err != nil {
		return nil, err
	}
	args := append(append([]string{}, opts...), "--print-uris", "--yes", "install")
	args = append(args, wanted...)
	res, err := a.runApt(ctx, aptGet, tree, aptQueryTimeout, aptOut, args)
	if err := aptOutcome("computing the package closure", res, err); err != nil {
		return nil, err
	}
	pkgs, err := parsePrintURIs(res.Stdout, src.Arch, src.hosts())
	if err != nil {
		return nil, err
	}
	c := &Closure{Version: closureVersion(pkgs), Pkgs: pkgs, At: a.Env.now(), key: key, src: src, tree: tree, wanted: wanted}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]*Closure{}
	}
	a.cache[key] = c
	a.mu.Unlock()
	return c, nil
}

func extraKey(s aptSource) string {
	var b strings.Builder
	for _, r := range s.Extra {
		fmt.Fprintf(&b, "%s %s %s %s %s;", r.Name, r.URI, r.Suite, strings.Join(r.Components, " "), r.Key)
	}
	return b.String()
}

func (a *AptClosure) runApt(ctx context.Context, bin, tree string, timeout time.Duration, maxOut int64, args []string) (execx.Result, error) {
	return a.Env.run(ctx, cmd{bin: bin, args: args, env: a.aptEnv(tree), timeout: timeout, maxOut: maxOut, cred: a.Credential})
}

func (a *AptClosure) aptStep(ctx context.Context, bin, tree, what string, timeout time.Duration, args []string) error {
	res, err := a.runApt(ctx, bin, tree, timeout, aptOut, args)
	return aptOutcome(what, res, err)
}

// aptOutcome turns a finished apt call into an error with apt's own "E:" message (LAI-SYN-060).
func aptOutcome(what string, res execx.Result, err error) error {
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return err
	case err != nil:
		return fail(CodeApt, err, "%s did not finish", what)
	case res.Truncated:
		return fail(CodeApt, nil, "%s printed more than the allowed output", what)
	case res.ExitCode != 0 || hasAptError(res):
		return fail(CodeApt, nil, "%s failed: %s", what, aptMessage(res))
	}
	return nil
}

func eLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "E:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(l, "E:")))
		}
	}
	return out
}

func hasAptError(res execx.Result) bool {
	return len(eLines(res.Stderr)) > 0
}

// aptMessage is apt's E: lines and, for a dependency problem, the lines that say which package wants
// what (apt prints those on stdout), clipped; else the tail of stderr.
func aptMessage(res execx.Result) string {
	lines := append(eLines(res.Stderr), eLines(res.Stdout)...)
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "Depends:") || strings.Contains(l, "Conflicts:") || strings.Contains(l, "not installable") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return Clip(res.Stderr, 300)
	}
	return Clip([]byte(strings.Join(lines, "; ")), 500)
}

// prepareTree writes the private apt tree (ark-sync.md section 4.9.7 step 1): the generated sources,
// the keyrings, an empty dpkg status, an empty preferences directory and the apt.conf that keeps apt
// off the system configuration. It rewrites the generated files every time and leaves apt's own
// state (the downloaded lists) alone.
func (a *AptClosure) prepareTree(tree string, src aptSource) error {
	dirs := []string{
		"etc/apt/sources.list.d", "etc/apt/trusted.gpg.d", "etc/apt/preferences.d", "etc/apt/apt.conf.d",
		"var/lib/apt/lists/partial", "var/cache/apt/archives/partial", "var/lib/dpkg", "home",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o755); err != nil {
			return fail(CodeApt, err, "cannot create the private apt tree")
		}
	}
	keyDir := filepath.Join(tree, "etc", "apt", "trusted.gpg.d")
	names := []string{ubuntuKeyring}
	for _, r := range src.Extra {
		names = append(names, r.Key)
	}
	done := map[string]bool{}
	for _, n := range names {
		if done[n] {
			continue
		}
		done[n] = true
		data, err := a.keyring(n)
		if err != nil {
			return fail(CodeApt, err, "the keyring %s is not available", n)
		}
		if err := writeFileAtomic(filepath.Join(keyDir, n), data); err != nil {
			return fail(CodeApt, err, "cannot write the keyring %s", n)
		}
	}
	files := map[string]string{
		"etc/apt/sources.list.d/ubuntu.sources": src.renderUbuntuSources(filepath.Join(keyDir, ubuntuKeyring)),
		"etc/apt/sources.list.d/extra.sources":  src.renderExtraSources(keyDir),
		"etc/apt/apt.conf":                      fmt.Sprintf("Dir %q;\n", tree+"/"),
		"var/lib/dpkg/status":                   "",
	}
	for rel, content := range files {
		if err := writeFileAtomic(filepath.Join(tree, filepath.FromSlash(rel)), []byte(content)); err != nil {
			return fail(CodeApt, err, "cannot write the private apt tree")
		}
	}
	if a.Credential != nil {
		cred := a.Credential
		err := filepath.WalkDir(tree, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(p, int(cred.UID), int(cred.GID))
		})
		if err != nil {
			return fail(CodeApt, err, "cannot hand the private apt tree to the apt user")
		}
	}
	return nil
}

func (a *AptClosure) keyring(name string) ([]byte, error) {
	if a.Keyrings != nil {
		if b, err := a.Keyrings(name); err == nil && len(b) > 0 {
			return b, nil
		} else if name != ubuntuKeyring {
			if err == nil {
				err = errors.New("empty keyring")
			}
			return nil, err
		}
	}
	if name != ubuntuKeyring {
		return nil, errors.New("no keyring source")
	}
	dir := a.SystemKeyringDir
	if dir == "" {
		dir = "/usr/share/keyrings"
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// itemDir is where the closure installs: the engine's ItemDir when given, else derived from the id
// (kit-debs-<codename>-<arch> and kit-nvidia-<codename>-<arch>-<branch>, rebuild-kit.md section 3.1).
func itemDir(req Request) (string, error) {
	if req.ItemDir != "" {
		if !inside(req.ItemDir) {
			return "", fail(CodeApt, nil, "the item directory %q is not inside the root", req.ItemDir)
		}
		return req.ItemDir, nil
	}
	switch {
	case strings.HasPrefix(req.Item.ID, "kit-debs-"):
		return "kit/debs/" + strings.TrimPrefix(req.Item.ID, "kit-debs-"), nil
	case strings.HasPrefix(req.Item.ID, "kit-nvidia-"):
		return "kit/nvidia/" + strings.TrimPrefix(req.Item.ID, "kit-nvidia-"), nil
	}
	return "", fail(CodeApt, nil, "item %s has no known kit directory", req.Item.ID)
}

// Produce implements Producer: stage the local repository of the closure under <staging>/tree/ and
// seal it (SHA256SUMS written, tree hash computed). The debs come through the downloader, which
// enforces the size and sha256 of the signed index (a mismatch is quarantined by the engine); an
// unchanged deb (same file name, size and sha256 as in the present version's SHA256SUMS) is
// hard-linked from the present tree instead. When the closure's version equals the present one the
// product is Keep and nothing is staged.
func (a *AptClosure) Produce(ctx context.Context, req Request) (Product, error) {
	dest, err := itemDir(req)
	if err != nil {
		return Product{}, err
	}
	if !manifest.SafeName(req.Version) || req.Root == "" {
		return Product{}, fail(CodeApt, nil, "the request has no version or root")
	}
	c, err := a.closure(ctx, req.Item)
	if err != nil {
		return Product{}, err
	}
	defer a.forget(c)
	prod := Product{Version: c.Version, SortKey: c.At.UTC().Format(time.RFC3339), Trust: manifest.TrustSignedIndex,
		Source: map[string]any{"codename": c.src.Codename, "arch": c.src.Arch, "packages": c.wanted}}
	if req.PresentDir != "" && req.PresentVersion == c.Version {
		prod.Keep = true
		return prod, nil
	}
	if req.Version != "" && req.Version != c.Version {
		return Product{}, fail(CodeApt, ErrVersionMoved, "the plan was made for %s and apt now computes %s", req.Version, c.Version)
	}
	if a.Env.Downloader == nil {
		return Product{}, fail(CodeApt, nil, "no downloader is configured")
	}
	staging := req.StagingDir()
	treeDir := filepath.Join(staging, "tree")
	if err := os.MkdirAll(filepath.Join(treeDir, "pool"), 0o755); err != nil {
		return Product{}, fail(CodeApt, err, "cannot create the staging directory")
	}
	present := presentSums(req)
	for i, p := range c.Pkgs {
		if err := a.stage(ctx, req, treeDir, p, present); err != nil {
			return Product{}, err
		}
		if req.Progress != nil {
			req.Progress(p.PoolName, i+1, len(c.Pkgs))
		}
	}

	aptCache, err := a.Env.tool("apt-cache")
	if err != nil {
		return Product{}, err
	}
	show := make([]string, 0, len(c.Pkgs))
	for _, p := range c.Pkgs {
		show = append(show, p.NameVersion())
	}
	args := append(aptOptions(c.tree, c.src.Arch, a.sandboxUser(), ""), "show")
	args = append(args, show...)
	res, err := a.runApt(ctx, aptCache, c.tree, aptQueryTimeout, showOut, args)
	if err := aptOutcome("reading the package records", res, err); err != nil {
		return Product{}, err
	}
	index, err := buildPackages(res.Stdout, c.Pkgs)
	if err != nil {
		return Product{}, err
	}
	gz, err := gzipBytes(index)
	if err != nil {
		return Product{}, fail(CodeApt, err, "cannot compress the package index")
	}
	generated := map[string][]byte{
		"Packages":      index,
		"Packages.gz":   gz,
		"PACKAGES.lock": packagesLock(c.Pkgs),
		"README.txt":    readme(c.src.Codename, c.src.Arch, c.Version, a.Env.now(), c.Pkgs, c.wanted),
	}
	sums := make([]manifest.Sum, 0, len(c.Pkgs)+len(generated))
	var total int64
	for _, p := range c.Pkgs {
		sums = append(sums, manifest.Sum{SHA256: p.SHA256, Path: "pool/" + p.PoolName})
		total += p.Size
	}
	for name, data := range generated {
		if err := writeFileAtomic(filepath.Join(treeDir, name), data); err != nil {
			return Product{}, fail(CodeApt, err, "cannot write %s", name)
		}
		sums = append(sums, manifest.Sum{SHA256: sha256Hex(data), Path: name})
		total += int64(len(data))
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].Path < sums[j].Path })
	if err := manifest.WriteSums(osFS, treeDir, sums); err != nil {
		return Product{}, fail(CodeApt, err, "cannot write SHA256SUMS")
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		return Product{}, fail(CodeApt, err, "cannot hash the tree")
	}
	prod.Parts = []Part{{Name: "debs", Kind: PartTree, From: "tree", To: dest, Sums: sums, TreeSHA256: tree, Bytes: total}}
	return prod, nil
}

// forget drops a closure from the cache once it was produced (or failed): the next run computes anew.
func (a *AptClosure) forget(c *Closure) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache[c.key] == c {
		delete(a.cache, c.key)
	}
}

// presentSums reads the SHA256SUMS of the present version of the item: path to sha256. Any problem
// means "nothing to reuse".
func presentSums(req Request) map[string]string {
	if req.PresentDir == "" {
		return nil
	}
	sums, err := manifest.ReadSums(osFS, req.PresentDir)
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(sums))
	for _, s := range sums {
		m[s.Path] = s.SHA256
	}
	return m
}

// stage puts one deb into <treeDir>/pool: reuse a verified staged file of an earlier attempt, else a
// hard link of the present version's identical file, else download it.
func (a *AptClosure) stage(ctx context.Context, req Request, treeDir string, p Pkg, present map[string]string) error {
	rel := "pool/" + p.PoolName
	final := filepath.Join(treeDir, rel)
	if sum, size, err := hashFile(final); err == nil && sum == p.SHA256 && size == p.Size {
		return nil // an earlier attempt already committed this file, and it is the right one
	}
	if present[rel] == p.SHA256 && req.PresentDir != "" {
		src := filepath.Join(req.PresentDir, filepath.FromSlash(rel))
		if fi, err := os.Stat(src); err == nil && fi.Mode().IsRegular() && fi.Size() == p.Size {
			if err := os.Link(src, final); err == nil {
				return nil
			}
			// A link that fails (another filesystem, a leftover of other content in the way) falls
			// through to the download, whose commit replaces whatever is there.
		}
	}
	job := download.FileJob{
		RootID: req.RootID, Root: req.Root, Item: req.Item.ID, Version: req.Version,
		Name: path.Join("tree", rel), URL: p.URI, ExpectedSize: p.Size, ExpectedSHA256: p.SHA256,
		MinFreeBytes: req.MinFreeBytes, Commit: true,
	}
	if _, err := a.Env.Downloader.Fetch(ctx, job, req.Checkpoints); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("downloading %s: %w", p.PoolName, err)
	}
	return nil
}
