package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/untar"
)

// PipWheelhouse is the pip_wheelhouse resolver (ark-sync.md section 4.9.11, rebuild-kit.md section 3.2.2): the
// offline install set of a Python engine, kit/engines/<engine>/<version>/ with
//
//	python/<tarball>        the pinned standalone Python (files of the item, sha256 pinned in the catalog)
//	wheels/*.whl            every wheel of the lock, each verified against the lock's sha256, plus the wheels sync
//	                        built itself (the git builds at their pinned commit, the shim of this repository)
//	requirements.lock       the lock of this repository at the catalog tag (sha256 pinned in the catalog) with one
//	                        line per built wheel appended: what `pip install --no-index --require-hashes` reads
//	SHA256SUMS              written by the seal; the engine writes MANIFEST.json at the commit point
//
// Its version needs no network (catalog.WheelhouseVersion). Produce runs online only (Env.Online): the lock and
// the shim come from this repository's tarball at the tag catalog-<CATALOG_VERSION> (the one the docs come from,
// or a local-ai.git mirror), the wheels and the Python through the sync downloader (resume, size and sha256, the
// quarantine of a mismatch, the disk floor), the index pages through the read-only metadata client, and the
// sources of the git builds with a shallow fetch of the pinned commit. The builds themselves run with no network:
// pip with --no-index and --no-build-isolation, from the pinned Python and the build backends of the lock, and an
// unusable proxy so that a stray request fails instead of leaving the node. A wheel already staged by an earlier,
// interrupted attempt, or identical in the present version (same file name and sha256), is not downloaded again.
type PipWheelhouse struct {
	Env *Env
	// Repo fetches this repository at the catalog tag (the codeload base, a local-ai.git mirror); its Env is Env.
	Repo *RepoDocs
	// HTTP is the client of the index pages; nil builds the read-only metadata client.
	HTTP *http.Client
	// Version is the agent version in the User-Agent of the index requests.
	Version string

	meta readOnlyHTTP
	// allowLocal lets a test use plain http indexes and a local git repository.
	allowLocal bool
}

var _ NamedResolver = (*PipWheelhouse)(nil)

// SourceType implements NamedResolver.
func (*PipWheelhouse) SourceType() string { return "pip_wheelhouse" }

// Run-time bounds of the build.
const (
	whUnpackTimeout = 15 * time.Minute
	whVenvTimeout   = 5 * time.Minute
	whPipTimeout    = 30 * time.Minute
	whWheelTimeout  = 30 * time.Minute
	whGitTimeout    = time.Hour
	// maxIndexPage bounds one PEP 503 page (torch's lists every build of every version: a few MB).
	maxIndexPage = 32 << 20
	// whLockName is the lock inside the version folder.
	whLockName = "requirements.lock"
	// whNoProxy is an address nothing listens on: a request the build should never make fails at once.
	whNoProxy = "http://127.0.0.1:9"
)

// wheelhouse is a validated pip_wheelhouse item.
type wheelhouse struct {
	id, engine, backend, version string
	src                          catalog.ContentSource
}

func (p *PipWheelhouse) item(it plan.CatalogItem) (wheelhouse, error) {
	if it.Content == nil || it.Content.Source.Type != "pip_wheelhouse" {
		return wheelhouse{}, fail(CodeRefused, nil, "item %s is not a pip_wheelhouse item", it.ID)
	}
	engine, backend, ok := catalog.WheelhouseItem(it.ID)
	if !ok {
		return wheelhouse{}, fail(CodeRefused, nil, "item %s is not kit-engine-<engine>-<backend>", it.ID)
	}
	src := it.Content.Source
	v := catalog.WheelhouseVersion(it.ID, src)
	switch {
	case v == "" || !manifest.SafeName(v):
		return wheelhouse{}, fail(CodeRefused, nil, "item %s has no usable version", it.ID)
	case !catalog.ValidRepoPath(src.Lock) || !manifest.IsSHA256(src.SHA256):
		return wheelhouse{}, fail(CodeRefused, nil, "item %s does not pin its lock", it.ID)
	case len(src.Files) != 1 || !manifest.IsSHA256(src.Files[0].SHA256) || !manifest.ValidFilePath(src.Files[0].ItemPath()):
		return wheelhouse{}, fail(CodeRefused, nil, "item %s does not pin exactly one Python tarball", it.ID)
	case len(src.Indexes) == 0:
		return wheelhouse{}, fail(CodeRefused, nil, "item %s names no package index", it.ID)
	}
	for _, ix := range src.Indexes {
		if err := p.checkURL(ix); err != nil {
			return wheelhouse{}, fail(CodeRefused, err, "item %s: an index is not usable", it.ID)
		}
	}
	for _, b := range src.Builds {
		switch {
		case b.RepoPath != "":
			if !catalog.ValidRepoPath(b.RepoPath) {
				return wheelhouse{}, fail(CodeRefused, nil, "item %s: repo_path %q is not usable", it.ID, Clip([]byte(b.RepoPath), 60))
			}
		case !catalog.ValidCommit(b.Commit) || (b.Subdirectory != "" && !catalog.ValidRepoPath(b.Subdirectory)):
			return wheelhouse{}, fail(CodeRefused, nil, "item %s: a git build is not pinned to a commit", it.ID)
		default:
			if err := p.checkGitURL(b.URL); err != nil {
				return wheelhouse{}, fail(CodeRefused, err, "item %s: a git build URL is not usable", it.ID)
			}
		}
	}
	return wheelhouse{id: it.ID, engine: engine, backend: backend, version: v, src: src}, nil
}

// checkURL accepts https (and, in tests, http) with a host and no credentials.
func (p *PipWheelhouse) checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return errors.New("not a URL")
	case u.Scheme != "https" && !(p.allowLocal && u.Scheme == "http"):
		return errors.New("not https")
	case u.User != nil:
		return errors.New("carries credentials")
	}
	return nil
}

func (p *PipWheelhouse) checkGitURL(raw string) error {
	if p.allowLocal && filepath.IsAbs(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.Scheme != "https":
		return errors.New("not an https URL")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return errors.New("carries credentials, a query or a fragment")
	}
	return nil
}

// Resolve implements plan.Resolver: the version is what the catalog pins; no network. It is deliberately not a
// plan.LocalResolver: with no network the planner takes the same version from plan.FromCatalog and keeps the
// present copy, and nothing is built offline (Produce needs the indexes).
func (p *PipWheelhouse) Resolve(_ context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	w, err := p.item(it)
	if err != nil {
		return plan.Resolved{}, err
	}
	return plan.Resolved{Version: w.version, SortKey: w.version, SizeBytes: it.Content.ExpectedSizeBytes}, nil
}

// Produce implements Producer.
func (p *PipWheelhouse) Produce(ctx context.Context, req Request) (Product, error) {
	w, err := p.item(req.Item)
	if err != nil {
		return Product{}, err
	}
	dest := req.ItemDir
	if dest == "" {
		dest = path.Join("kit", "engines", w.engine)
	}
	if !inside(dest) || req.Root == "" || !manifest.SafeName(req.Version) {
		return Product{}, fail(CodeRefused, nil, "the request has no version, root or item directory")
	}
	prod := Product{Version: w.version, SortKey: w.version, Trust: manifest.TrustPinned,
		Source: map[string]any{"engine": w.engine, "backend": w.backend, "runtime_version": w.version, "lock": w.src.Lock, "lock_sha256": w.src.SHA256}}
	if req.PresentDir != "" && req.PresentVersion == w.version {
		prod.Keep = true
		return prod, nil
	}
	if req.Version != w.version {
		return Product{}, fail(CodeNoVersion, ErrVersionMoved, "the plan was made for %s and the catalog now pins %s", req.Version, w.version)
	}
	if err := p.Env.requireOnline(); err != nil {
		return Product{}, err
	}
	if p.Env.Downloader == nil || p.Repo == nil {
		return Product{}, fail(CodeApt, nil, "no downloader or repository source is configured")
	}
	git := ""
	for _, b := range w.src.Builds {
		if b.RepoPath == "" {
			if git, err = p.Env.tool("git"); err != nil {
				return Product{}, err
			}
			break
		}
	}

	staging := req.StagingDir()
	treeDir := filepath.Join(staging, "tree")
	vdir := filepath.Join(treeDir, w.version)
	wheels := filepath.Join(vdir, "wheels")
	if err := fsperm.MkdirAll(wheels, 0o755); err != nil {
		return Product{}, fail(CodeApt, err, "cannot create the staging directory")
	}

	lockData, lock, repoDir, err := p.repoFiles(ctx, req, w, staging)
	if err != nil {
		return Product{}, err
	}
	present := presentByDigest(req)
	total := 1 + len(lock.Reqs)
	done := 0
	progress := func(name string) {
		done++
		if req.Progress != nil {
			req.Progress(name, done, total)
		}
	}

	// The pinned Python.
	pyFile := w.src.Files[0]
	pyRel := path.Join(w.version, pyFile.ItemPath())
	if err := p.stage(ctx, req, treeDir, pyRel, pyFile.URL, pyFile.SHA256, present); err != nil {
		return Product{}, err
	}
	progress(path.Base(pyRel))

	// Every wheel of the lock.
	idx := &indexCache{pages: map[string][]indexLink{}}
	want := map[string]bool{}
	for _, r := range lock.Reqs {
		name, err := p.stageWheel(ctx, req, w, treeDir, r, present, idx)
		if err != nil {
			return Product{}, err
		}
		want[name] = true
		progress(name)
	}

	// The wheels sync builds, offline.
	built, err := p.build(ctx, req, w, lock, staging, git, filepath.Join(treeDir, filepath.FromSlash(pyRel)), wheels, repoDir)
	if err != nil {
		return Product{}, err
	}
	var extra strings.Builder
	extra.WriteString("\n# Built by ark sync from the pinned sources (rebuild-kit.md section 3.2.2):\n")
	for _, b := range built {
		want[b.file] = true
		extra.WriteString(b.line + "\n")
	}
	// Nothing but the wheels of the lock may be in wheels/ (pip would see them): a leftover of an earlier attempt
	// is moved out of the tree, into staging, which the engine cleans (a producer never deletes, ark-sync.md
	// section 9.1).
	if ents, err := os.ReadDir(wheels); err == nil {
		for _, e := range ents {
			if !want[e.Name()] {
				if err := os.Rename(filepath.Join(wheels, e.Name()), p.scratch(staging, "unused")); err != nil {
					return Product{}, fail(CodeApt, err, "cannot move a stray file out of the wheelhouse")
				}
			}
		}
	}
	finalLock := append(append([]byte{}, lockData...), []byte(extra.String())...)
	if err := writeFileAtomic(filepath.Join(vdir, whLockName), finalLock); err != nil {
		return Product{}, fail(CodeApt, err, "cannot write the lock")
	}

	part, err := sealTree("wheelhouse", treeDir, "tree", dest)
	if err != nil {
		return Product{}, fail(CodeApt, err, "cannot seal the wheelhouse")
	}
	prod.Parts = []Part{part}
	return prod, nil
}

// repoFiles fetches this repository at the catalog tag and takes the lock and the repo_path sources out of it:
// the lock must be the one the catalog pins, have nothing unresolved, and record the tree hash of every
// repo_path source, which must match.
func (p *PipWheelhouse) repoFiles(ctx context.Context, req Request, w wheelhouse, staging string) ([]byte, PipLock, string, error) {
	v := req.CatalogVersion
	if !catalogVersionRe.MatchString(v) {
		return nil, PipLock{}, "", fail(CodeRefused, nil, "the catalog has no usable version")
	}
	tag := "catalog-" + v
	tarPath, _, _, err := p.Repo.fetchTarball(ctx, req, tag, staging)
	if err != nil {
		return nil, PipLock{}, "", err
	}
	// A fresh folder per attempt: what an earlier attempt extracted (perhaps from another tag) never mixes in.
	repoDir := p.scratch(staging, "repo")
	repoRel := filepath.Base(repoDir)
	rules := []tarRule{{Prefix: w.src.Lock, Dest: path.Join(repoRel, w.src.Lock)}}
	for _, b := range w.src.Builds {
		if b.RepoPath != "" {
			rules = append(rules, tarRule{Prefix: b.RepoPath + "/", Dest: path.Join(repoRel, b.RepoPath), Allow: repoTreeFile})
		}
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, PipLock{}, "", fail(CodeApt, err, "cannot read the source tarball")
	}
	_, err = extractTar(f, staging, rules)
	f.Close()
	if err != nil {
		return nil, PipLock{}, "", fail(CodeApt, err, "the source tarball of %s was refused", tag)
	}
	data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(w.src.Lock)))
	if err != nil {
		return nil, PipLock{}, "", fail(CodeRefused, err, "the source of %s has no %s", tag, w.src.Lock)
	}
	if got := sha256Hex(data); got != strings.ToLower(w.src.SHA256) {
		return nil, PipLock{}, "", fail(CodeRefused, nil, "%s at %s is not the lock the catalog pins (sha256 %s, the catalog says %s)",
			w.src.Lock, tag, download.ShortHash(got), download.ShortHash(w.src.SHA256))
	}
	lock, err := ParsePipLock(data)
	if err != nil {
		return nil, PipLock{}, "", fail(CodeApt, err, "%s is not a usable lock", w.src.Lock)
	}
	if len(lock.Unresolved) > 0 {
		return nil, PipLock{}, "", fail(CodeApt, nil, "%s is incomplete: %d packages were never pinned (%s); run scripts/release/lock-acestep.sh with internet",
			w.src.Lock, len(lock.Unresolved), strings.Join(lock.Unresolved, ", "))
	}
	for _, b := range w.src.Builds {
		if b.RepoPath == "" {
			continue
		}
		want, ok := lock.RepoTrees[b.RepoPath]
		if !ok {
			return nil, PipLock{}, "", fail(CodeApt, nil, "%s records no tree hash for %s", w.src.Lock, b.RepoPath)
		}
		got, err := RepoTreeSHA256(filepath.Join(repoDir, filepath.FromSlash(b.RepoPath)))
		if err != nil || got != want {
			return nil, PipLock{}, "", fail(CodeApt, err, "%s at %s is not the source the lock records (run scripts/release/lock-acestep.sh --shim-only)", b.RepoPath, tag)
		}
	}
	return data, lock, repoDir, nil
}

// scratch is a path below staging that does not exist yet (<staging>/<kind>-<n>): each attempt works in its own
// folder, and the engine removes the whole staging directory after the swap or a permanent failure.
func (p *PipWheelhouse) scratch(staging, kind string) string {
	base := filepath.Join(staging, kind+"-"+strconv.FormatInt(p.Env.now().UnixNano(), 36))
	for i := 0; ; i++ {
		c := base
		if i > 0 {
			c += "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
}

// repoTreeFile is the filter of a repo_path source: no tests, no lock or input files, no byte code. The same rule
// as scripts/release/lock-acestep.sh.
func repoTreeFile(rel string) bool {
	if rel == "tests" || strings.HasPrefix(rel, "tests/") || strings.HasSuffix(rel, ".lock") || strings.HasSuffix(rel, ".in") ||
		strings.HasSuffix(rel, ".pyc") || strings.Contains("/"+rel+"/", "/__pycache__/") {
		return false
	}
	return true
}

// RepoTreeSHA256 is the tree hash of a repo_path source (manifest.TreeSHA256 of the "<sha256>  <path>" lines of
// every file repoTreeFile keeps, paths relative to dir): what lock-acestep.sh writes as repo-tree-sha256.
func RepoTreeSHA256(dir string) (string, error) {
	var sums []manifest.Sum
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && !repoTreeFile(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if !repoTreeFile(rel) {
			return nil
		}
		sum, _, herr := hashFile(p)
		if herr != nil {
			return herr
		}
		sums = append(sums, manifest.Sum{SHA256: sum, Path: rel})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(sums) == 0 {
		return "", errors.New("no files")
	}
	return manifest.TreeSHA256(sums)
}

// presentByDigest maps the sha256 of every file of the present version of the item to its path, so an identical
// wheel or Python is hard-linked instead of downloaded.
func presentByDigest(req Request) map[string]string {
	sums := presentSums(req)
	if len(sums) == 0 {
		return nil
	}
	m := make(map[string]string, len(sums))
	for p, s := range sums {
		m[s] = filepath.Join(req.PresentDir, filepath.FromSlash(p))
	}
	return m
}

// stage puts one file at <treeDir>/<rel> with the given sha256: a verified file of an earlier attempt is kept, an
// identical file of the present version is hard-linked, anything else is downloaded (verified, resumable).
func (p *PipWheelhouse) stage(ctx context.Context, req Request, treeDir, rel, rawURL, sha string, present map[string]string) error {
	final := filepath.Join(treeDir, filepath.FromSlash(rel))
	if sum, _, err := hashFile(final); err == nil && sum == sha {
		return nil
	}
	if linkPresent(final, filepath.Dir(treeDir), sha, present) {
		return nil
	}
	job := download.FileJob{
		RootID: req.RootID, Root: req.Root, Item: req.Item.ID, Version: req.Version,
		Name: path.Join("tree", rel), URL: rawURL, ExpectedSHA256: sha, MinFreeBytes: req.MinFreeBytes, Commit: true,
	}
	if _, err := p.Env.Downloader.Fetch(ctx, job, req.Checkpoints); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("downloading %s: %w", path.Base(rel), err)
	}
	return nil
}

// linkPresent hard-links the present version's file with this sha256 to final and checks it. A link that fails
// (another filesystem, a damaged file) is false: the caller downloads.
func linkPresent(final, tmpDir, sha string, present map[string]string) bool {
	src, ok := present[sha]
	if !ok {
		return false
	}
	if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if err := fsperm.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return false
	}
	// Link under a fresh name, check, then rename over final (a producer never deletes; a failed check leaves
	// the link where the engine's staging cleanup takes it).
	tmp := filepath.Join(tmpDir, fmt.Sprintf(".link-%d-%s", time.Now().UnixNano(), filepath.Base(final)))
	if err := os.Link(src, tmp); err != nil {
		return false
	}
	if sum, _, err := hashFile(tmp); err != nil || sum != sha {
		return false
	}
	return os.Rename(tmp, final) == nil
}

// stageWheel finds the wheel of one requirement on the indexes (the link whose sha256 is the lock's) and stages it
// under <version>/wheels/. It returns the file name.
func (p *PipWheelhouse) stageWheel(ctx context.Context, req Request, w wheelhouse, treeDir string, r PipReq, present map[string]string, idx *indexCache) (string, error) {
	wheels := filepath.Join(treeDir, w.version, "wheels")
	// A file of an earlier attempt or of the present version with the right digest needs no index request.
	if ents, err := os.ReadDir(wheels); err == nil {
		for _, e := range ents {
			if dist, _, ok := WheelName(e.Name()); ok && dist == NormalizeDist(r.Name) {
				if sum, _, err := hashFile(filepath.Join(wheels, e.Name())); err == nil && sum == r.SHA256 {
					return e.Name(), nil
				}
			}
		}
	}
	if src, ok := present[r.SHA256]; ok {
		name := filepath.Base(src)
		if dist, _, okName := WheelName(name); okName && dist == NormalizeDist(r.Name) && linkPresent(filepath.Join(wheels, name), filepath.Dir(treeDir), r.SHA256, present) {
			return name, nil
		}
	}
	link, err := p.findWheel(ctx, w, r, idx)
	if err != nil {
		return "", err
	}
	return link.file, p.stage(ctx, req, treeDir, path.Join(w.version, "wheels", link.file), link.url, r.SHA256, present)
}

// indexLink is one wheel link of a PEP 503 page.
type indexLink struct {
	file, url, sha string
}

type indexCache struct {
	pages map[string][]indexLink
}

var anchorRe = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*"([^"]+)"[^>]*>`)

// findWheel asks each index, in catalog order, for the page of the requirement's name and takes the link whose
// #sha256= fragment is the lock's. The file name must be a wheel of that distribution.
func (p *PipWheelhouse) findWheel(ctx context.Context, w wheelhouse, r PipReq, idx *indexCache) (indexLink, error) {
	var lastErr error
	for _, ix := range w.src.Indexes {
		links, err := p.indexPage(ctx, ix, NormalizeDist(r.Name), idx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return indexLink{}, err
			}
			lastErr = err
			continue
		}
		for _, l := range links {
			if l.sha != r.SHA256 {
				continue
			}
			if dist, _, ok := WheelName(l.file); !ok || dist != NormalizeDist(r.Name) || !manifest.SafeName(l.file) {
				return indexLink{}, fail(CodeRefused, nil, "the index link with the sha256 of %s is not a wheel of it", r.Name)
			}
			return l, nil
		}
	}
	if lastErr != nil {
		return indexLink{}, lastErr
	}
	return indexLink{}, fail(CodeRefused, nil, "no index has the wheel of %s==%s with the sha256 of the lock", r.Name, r.Version)
}

func (p *PipWheelhouse) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	p.meta.Version = p.Version
	return p.meta.client()
}

// indexPage reads and parses <index>/<name>/ (cached per run).
func (p *PipWheelhouse) indexPage(ctx context.Context, index, name string, idx *indexCache) ([]indexLink, error) {
	pageURL := strings.TrimRight(index, "/") + "/" + name + "/"
	if l, ok := idx.pages[pageURL]; ok {
		return l, nil
	}
	h := readOnlyHTTP{Client: p.client(), Version: p.Version}
	res, err := h.do(ctx, http.MethodGet, pageURL, true, maxIndexPage, map[string]string{"Accept": "text/html"})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fail(CodeFetch, err, "the package index could not be read")
	}
	switch {
	case res.Status == http.StatusNotFound || res.Status == http.StatusGone || res.Status == http.StatusForbidden:
		idx.pages[pageURL] = nil
		return nil, nil
	case res.Status != http.StatusOK:
		return nil, fail(CodeFetch, nil, "the package index answered %s", strconv.Itoa(res.Status))
	case res.Truncated:
		return nil, fail(CodeFetch, nil, "the index page of %s is larger than %d bytes", name, maxIndexPage)
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, fail(CodeRefused, err, "bad index URL")
	}
	var links []indexLink
	for _, m := range anchorRe.FindAllStringSubmatch(string(res.Body), -1) {
		href := strings.ReplaceAll(m[1], "&amp;", "&")
		u, err := base.Parse(href)
		if err != nil {
			continue
		}
		frag := u.Fragment
		u.Fragment, u.RawFragment = "", ""
		sha, ok := strings.CutPrefix(frag, "sha256=")
		if !ok || !sha256HexRe.MatchString(sha) {
			continue
		}
		file, err := url.PathUnescape(path.Base(u.EscapedPath()))
		if err != nil || !strings.HasSuffix(file, ".whl") {
			continue
		}
		if p.checkURL(u.String()) != nil {
			continue
		}
		links = append(links, indexLink{file: file, url: u.String(), sha: sha})
	}
	idx.pages[pageURL] = links
	return links, nil
}

// builtWheel is a wheel sync built: its file name below wheels/ and its lock line.
type builtWheel struct {
	file, line string
}

// build makes the wheels of the builds, offline: unpack the pinned Python into <staging>/build, a build venv with
// the lock's build backends, the sources (a shallow fetch of each pinned commit, or the repo_path tree), then
// `pip wheel --no-deps --no-build-isolation --no-index` per build.
func (p *PipWheelhouse) build(ctx context.Context, req Request, w wheelhouse, lock PipLock, staging, git, pyTar, wheels, repoDir string) ([]builtWheel, error) {
	if len(w.src.Builds) == 0 {
		return nil, nil
	}
	if len(lock.BuildRequires) == 0 {
		return nil, fail(CodeApt, nil, "%s names no build backends (build-requires)", w.src.Lock)
	}
	work := p.scratch(staging, "build") // a leftover of an interrupted build is never reused
	home := filepath.Join(work, "home")
	if err := fsperm.MkdirAll(home, 0o755); err != nil {
		return nil, fail(CodeApt, err, "cannot create the build directory")
	}
	env := []string{"PATH=" + execx.FixedPath, "HOME=" + home, "LC_ALL=C.UTF-8", "PIP_NO_INDEX=1", "PIP_NO_CACHE_DIR=1",
		"PIP_DISABLE_PIP_VERSION_CHECK=1", "PYTHONDONTWRITEBYTECODE=1", "SOURCE_DATE_EPOCH=946684800",
		"http_proxy=" + whNoProxy, "https_proxy=" + whNoProxy, "HTTP_PROXY=" + whNoProxy, "HTTPS_PROXY=" + whNoProxy, "no_proxy=", "NO_PROXY="}
	// In-process, not the tar program: GNU tar 1.35 needs openat2, which the agent's sandbox refuses (untar).
	uctx, cancel := context.WithTimeout(ctx, whUnpackTimeout)
	_, err := untar.ExtractTarGz(uctx, pyTar, work, untar.Limits{})
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, fail(CodeApt, err, "unpacking the Python failed: %v", err)
	}
	py := filepath.Join(work, "python", "bin", "python3")
	if fi, err := os.Stat(py); err != nil || fi.IsDir() {
		return nil, fail(CodeApt, nil, "the Python tarball has no python/bin/python3")
	}
	venv := filepath.Join(work, "venv")
	if err := p.step(ctx, "making the build venv", py, whVenvTimeout, env, "-m", "venv", venv); err != nil {
		return nil, err
	}
	vpy := filepath.Join(venv, "bin", "python")
	var bl strings.Builder
	need := map[string]bool{}
	for _, b := range lock.BuildRequires {
		need[b] = true
	}
	for _, r := range lock.Reqs {
		if need[NormalizeDist(r.Name)] {
			bl.WriteString(r.Line + "\n")
		}
	}
	buildLock := filepath.Join(work, "build.lock")
	if err := writeFileAtomic(buildLock, []byte(bl.String())); err != nil {
		return nil, fail(CodeApt, err, "cannot write the build lock")
	}
	if err := p.step(ctx, "installing the build backends", vpy, whPipTimeout, env, "-m", "pip", "install", "--no-index", "--find-links", wheels,
		"--require-hashes", "-r", buildLock); err != nil {
		return nil, err
	}

	checkouts := map[string]string{}
	var out []builtWheel
	for i, b := range w.src.Builds {
		src := ""
		if b.RepoPath != "" {
			src = filepath.Join(repoDir, filepath.FromSlash(b.RepoPath))
		} else {
			key := b.URL + "@" + b.Commit
			dir, ok := checkouts[key]
			if !ok {
				dir = filepath.Join(work, "src", strconv.Itoa(len(checkouts)))
				if err := p.checkout(ctx, git, b.URL, b.Commit, dir); err != nil {
					return nil, err
				}
				checkouts[key] = dir
			}
			src = dir
			if b.Subdirectory != "" {
				src = filepath.Join(dir, filepath.FromSlash(b.Subdirectory))
			}
		}
		outDir := filepath.Join(work, "out", strconv.Itoa(i))
		if err := fsperm.MkdirAll(outDir, 0o755); err != nil {
			return nil, fail(CodeApt, err, "cannot create the build directory")
		}
		if err := p.step(ctx, "building a wheel", vpy, whWheelTimeout, env, "-m", "pip", "wheel", "--no-deps", "--no-build-isolation", "--no-index",
			"-w", outDir, src); err != nil {
			return nil, err
		}
		ents, _ := os.ReadDir(outDir)
		if len(ents) != 1 {
			return nil, fail(CodeApt, nil, "the build of %s made %d files, not one wheel", buildLabel(b), len(ents))
		}
		file := ents[0].Name()
		dist, version, ok := WheelName(file)
		if !ok || !manifest.SafeName(file) {
			return nil, fail(CodeApt, nil, "the build of %s made %q, not a wheel", buildLabel(b), Clip([]byte(file), 60))
		}
		for _, r := range lock.Reqs {
			if NormalizeDist(r.Name) == dist {
				return nil, fail(CodeApt, nil, "the build of %s made %s, which the lock already pins", buildLabel(b), dist)
			}
		}
		if err := os.Rename(filepath.Join(outDir, file), filepath.Join(wheels, file)); err != nil {
			return nil, fail(CodeApt, err, "cannot move the built wheel")
		}
		sum, _, err := hashFile(filepath.Join(wheels, file))
		if err != nil {
			return nil, fail(CodeApt, err, "cannot read the built wheel")
		}
		out = append(out, builtWheel{file: file, line: dist + "==" + version + " --hash=sha256:" + sum})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].file < out[j].file })
	return out, nil
}

func buildLabel(b catalog.WheelBuild) string {
	if b.RepoPath != "" {
		return b.RepoPath
	}
	l := path.Base(strings.TrimSuffix(b.URL, ".git")) + "@" + b.Commit[:12]
	if b.Subdirectory != "" {
		l += "/" + b.Subdirectory
	}
	return l
}

// step runs one build program and turns its outcome into an error (LAI-SYN-060 with the program's last words).
func (p *PipWheelhouse) step(ctx context.Context, what, bin string, timeout time.Duration, env []string, args ...string) error {
	res, err := p.Env.run(ctx, cmd{bin: bin, args: args, env: env, timeout: timeout, maxOut: smallOut})
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return err
	case err != nil:
		return fail(CodeApt, err, "%s did not finish", what)
	case res.ExitCode != 0:
		return fail(CodeApt, nil, "%s failed: %s", what, Clip(lastLine(res.Stderr), 300))
	}
	return nil
}

func lastLine(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return []byte(s)
}

// checkout fetches exactly the pinned commit into dir (a shallow fetch: git verifies every object it receives)
// and checks it out; HEAD must then be the commit.
func (p *PipWheelhouse) checkout(ctx context.Context, git, rawURL, commit, dir string) error {
	g := &GitMirror{Env: p.Env, allowLocal: p.allowLocal}
	if err := fsperm.MkdirAll(dir, 0o755); err != nil {
		return fail(CodeFetch, err, "cannot create the source directory")
	}
	if res, err := g.git(ctx, git, gitQuickTimeout, "-c", "init.defaultBranch=main", "init", "-q", dir); err != nil || res.ExitCode != 0 {
		return g.fetchResult("preparing the source", res, err)
	}
	var host []string
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host = []string{strings.ToLower(u.Hostname())}
	}
	proxy, stop, err := p.Env.startProxy(ctx, host)
	if err != nil {
		return fail(CodeFetch, err, "cannot start the bandwidth limiter")
	}
	defer stop()
	args := append(append(g.netArgs(proxy), "-c", "safe.directory="+dir, "-C", dir), "fetch", "-q", "--depth", "1", "--no-tags", "--", rawURL, commit)
	res, err := g.git(ctx, git, whGitTimeout, args...)
	if err := g.fetchResult("fetching the pinned source", res, err); err != nil {
		return err
	}
	res, err = g.git(ctx, git, gitArchiveTimeout, append(g.repoArgs(dir), "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", commit)...)
	if err != nil || res.ExitCode != 0 {
		return fail(CodeMirror, err, "the pinned commit %s could not be checked out", commit[:12])
	}
	head, err := g.revParse(ctx, git, dir, "HEAD")
	if err != nil || head != commit {
		return fail(CodeMirror, err, "the source is not the pinned commit %s", commit[:12])
	}
	return nil
}
