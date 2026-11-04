package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/docsrender"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// DefaultRepo and DefaultCodeloadBase name the public repository the manuals come from (ark-sync.md
// section 4.9.8): https://codeload.github.com/<repo>/tar.gz/refs/tags/catalog-<CATALOG_VERSION>.
const (
	DefaultRepo         = "marinoscar/local-ai"
	DefaultCodeloadBase = "https://codeload.github.com"
	// repoMirrorName is the kit mirror a node builds the docs from offline.
	repoMirrorName = "local-ai"
	tarballName    = "src.tar.gz"
	// maxDocsTarball bounds the tarball of the tag: the repository without its history is megabytes.
	maxDocsTarball = 256 << 20
)

// Codes of the docs producer besides the shared ones.
const (
	// CodeVerifyDocs: the produced tree failed verification (a listed runbook has no HTML, the index
	// or the survival card is missing) or the tarball was refused as unsafe: the item is incomplete
	// and is fetched again (LAI-SYN-052).
	CodeVerifyDocs = doctor.SYN052
)

var (
	catalogVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	repoRe           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	runbookLinkRe    = regexp.MustCompile(`\]\((?:\./)?runbooks/([A-Za-z0-9][A-Za-z0-9._-]*)\.md(?:#[^)\s]*)?\)`)
	profileFileRe    = regexp.MustCompile(`^profiles/[A-Za-z0-9][A-Za-z0-9._-]*\.yaml$`)
)

// RepoDocs is the repo_docs resolver (ark-sync.md section 4.9.8): the manuals of the repository at
// the tag catalog-<CATALOG_VERSION>, rendered offline by the renderer of `aictl docs render`
// (in-process), plus kit/catalog/ from the same tarball.
type RepoDocs struct {
	Env *Env
	// Repo is owner/name on GitHub; default DefaultRepo.
	Repo string
	// CodeloadBase is the tarball host; default DefaultCodeloadBase. A test points it at a local server.
	CodeloadBase string
	// CatalogVersion returns the version of the catalog the plan is made with, for Resolve (Produce
	// uses Request.CatalogVersion).
	CatalogVersion func() string
	// Inventory returns the path of inventory.local.yaml (/etc/localai/inventory.local.yaml, else the
	// one of a mounted drive; docsrender.FindInventory), "" when there is none.
	Inventory func() string
	// State says where the survival card reads the node state from (docsrender.StateSources).
	State docsrender.StateSources
	// Hostname is this machine's name, the renderer's last resort to pick the node from the inventory.
	Hostname string
	// NoPDF skips the PDFs (the survival card then has no PDF and the notice LAI-CLI-014 is raised).
	NoPDF bool
	// Owner owns the rendered files when set (the localai service user).
	Owner *docsrender.Owner

	// allowLocal is the test seam of the git calls (see GitMirror.allowLocal).
	allowLocal bool
}

var _ plan.LocalResolver = (*RepoDocs)(nil)

// SourceType implements NamedResolver.
func (*RepoDocs) SourceType() string { return "repo_docs" }

func (r *RepoDocs) repo() string {
	if r.Repo != "" {
		return r.Repo
	}
	return DefaultRepo
}

func (r *RepoDocs) base() string {
	if r.CodeloadBase != "" {
		return strings.TrimSuffix(r.CodeloadBase, "/")
	}
	return DefaultCodeloadBase
}

func (r *RepoDocs) inventory() string {
	if r.Inventory == nil {
		return ""
	}
	return r.Inventory()
}

// version is "catalog-<V>", plus "+inv" when real values are rendered.
func docsVersion(catalogVersion string, inventory bool) string {
	v := "catalog-" + catalogVersion
	if inventory {
		v += "+inv"
	}
	return v
}

// Resolve implements plan.Resolver. It reads nothing from the network: the version follows the
// catalog's, and gains "+inv" when an inventory file is available, which makes the next sync render
// the manuals again with the real values.
func (r *RepoDocs) Resolve(_ context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	if it.Content == nil || it.Content.Source.Type != "repo_docs" {
		return plan.Resolved{}, fail(CodeRefused, nil, "item %s is not a repo_docs item", it.ID)
	}
	var v string
	if r.CatalogVersion != nil {
		v = r.CatalogVersion()
	}
	if !catalogVersionRe.MatchString(v) {
		return plan.Resolved{}, fail(CodeRefused, nil, "the catalog has no usable version")
	}
	// The kit catalog is copied from the tarball as it is: the inventory has no part in it, so it keeps
	// the plain catalog version and is not made again when the inventory appears.
	return plan.Resolved{Version: docsVersion(v, r.inventory() != "" && !isCatalogItem(it)), SortKey: v}, nil
}

// ResolveLocal implements plan.LocalResolver: with no network, the docs and the kit catalog can still
// be built when a mounted root holds a local-ai.git mirror (kit/git/local-ai.git) that has the tag of
// the catalog (Produce runs `git archive` from it, whatever the mode). The answer is Resolve's, since
// Resolve reads nothing from the network either. It reads the mirror's ref files only (the loose tag,
// or packed-refs): no tool runs and nothing dials, so the planner can call it on a node that is
// truly offline. A mirror that has the tag but is damaged is found by Produce (LAI-SYN-024).
func (r *RepoDocs) ResolveLocal(ctx context.Context, it plan.CatalogItem, rootPaths []string) (plan.Resolved, bool) {
	if r.CatalogVersion == nil {
		return plan.Resolved{}, false
	}
	v := r.CatalogVersion()
	if !catalogVersionRe.MatchString(v) {
		return plan.Resolved{}, false
	}
	tag := "catalog-" + v
	found := false
	for _, root := range rootPaths {
		if ctx.Err() != nil {
			return plan.Resolved{}, false
		}
		if mirrorHasTag(filepath.Join(root, "kit", "git", repoMirrorName+".git"), tag) {
			found = true
			break
		}
	}
	if !found {
		return plan.Resolved{}, false
	}
	res, err := r.Resolve(ctx, it)
	if err != nil {
		return plan.Resolved{}, false
	}
	return res, true
}

// maxPackedRefs bounds the packed-refs file ResolveLocal reads.
const maxPackedRefs = 16 << 20

// mirrorHasTag reports whether the bare repository at mirror lists refs/tags/<tag>, as a loose ref or
// in packed-refs. Files only: nothing runs.
func mirrorHasTag(mirror, tag string) bool {
	ref := "refs/tags/" + tag
	if fi, err := os.Stat(filepath.Join(mirror, filepath.FromSlash(ref))); err == nil && fi.Mode().IsRegular() {
		return true
	}
	f, err := os.Open(filepath.Join(mirror, "packed-refs"))
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPackedRefs))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, ' '); i > 0 && line[i+1:] == ref {
			return true
		}
	}
	return false
}

// isCatalogItem reports whether the item installs the kit catalog (a kit item) rather than the manuals.
func isCatalogItem(it plan.CatalogItem) bool { return it.Kind == manifest.KindKit }

// The parts of the product (Part.Name, Request.Want).
const (
	PartNameDocs    = "docs"
	PartNameCatalog = "catalog"
)

// Produce implements Producer. It stages two directories: <staging>/tree (the rendered manuals, to
// docs/) and <staging>/catalog (to kit/catalog/); the engine swaps both and writes both manifests.
// The catalog is linted first: when it does not pass (LAI-CAT-002) nothing is installed and the old
// kit/catalog/ stays, with the manuals: the two travel together.
func (r *RepoDocs) Produce(ctx context.Context, req Request) (Product, error) {
	if req.Item.Content == nil || req.Item.Content.Source.Type != "repo_docs" {
		return Product{}, fail(CodeRefused, nil, "item %s is not a repo_docs item", req.Item.ID)
	}
	v := req.CatalogVersion
	if !catalogVersionRe.MatchString(v) || !repoRe.MatchString(r.repo()) {
		return Product{}, fail(CodeRefused, nil, "the catalog version or the repository name is not usable")
	}
	if !manifest.SafeName(req.Version) || req.Root == "" {
		return Product{}, fail(CodeRefused, nil, "the request has no version or root")
	}
	tag := "catalog-" + v
	staging := req.StagingDir()
	if err := fsperm.MkdirAll(staging, 0o755); err != nil {
		return Product{}, fail(CodeVerifyDocs, err, "cannot create the staging directory")
	}

	tarPath, tarSHA, origin, err := r.fetchTarball(ctx, req, tag, staging)
	if err != nil {
		return Product{}, err
	}

	srcDir := filepath.Join(staging, "src")
	catDir := filepath.Join(staging, "catalog")
	f, err := os.Open(tarPath)
	if err != nil {
		return Product{}, fail(CodeVerifyDocs, err, "cannot read the source tarball")
	}
	_, err = extractTar(f, staging, []tarRule{
		{Prefix: "docs/", Dest: "src/docs"},
		{Prefix: "inventory.example.yaml", Dest: "src/inventory.example.yaml"},
		{Prefix: "catalog/", Dest: "catalog", Allow: func(rel string) bool {
			return rel == "models.yaml" || rel == "ark.yaml" || rel == catalog.FileVersion || rel == "pins.json" || profileFileRe.MatchString(rel)
		}},
	})
	f.Close()
	if err != nil {
		return Product{}, fail(CodeVerifyDocs, err, "the source tarball of %s was refused", tag)
	}
	docsSrc := filepath.Join(srcDir, "docs")
	if fi, err := os.Stat(filepath.Join(docsSrc, "README.md")); err != nil || !fi.Mode().IsRegular() {
		return Product{}, fail(CodeVerifyDocs, nil, "the tarball of %s has no docs/README.md", tag)
	}

	if err := r.checkCatalog(catDir, v, tag); err != nil {
		return Product{}, err
	}
	source := map[string]any{"repo": r.repo(), "tag": tag, "origin": origin, "tarball_sha256": tarSHA}
	if req.Want == PartNameCatalog {
		// Only the kit catalog is wanted: nothing is rendered, and the inventory has no part in it.
		catPart, err := sealTree(PartNameCatalog, catDir, "catalog", "kit/catalog")
		if err != nil {
			return Product{}, fail(CodeVerifyDocs, err, "cannot seal the kit catalog")
		}
		return Product{Version: docsVersion(v, false), SortKey: v, Trust: manifest.TrustComputed, Parts: []Part{catPart}, Source: source}, nil
	}

	rep, err := r.render(ctx, docsSrc, srcDir, filepath.Join(staging, "tree"), catDir, staging)
	if err != nil {
		return Product{}, err
	}
	notices, err := r.verify(docsSrc, filepath.Join(staging, "tree"), rep)
	if err != nil {
		return Product{}, err
	}

	docsPart, err := sealTree(PartNameDocs, filepath.Join(staging, "tree"), "tree", "docs")
	if err != nil {
		return Product{}, fail(CodeVerifyDocs, err, "cannot seal the rendered manuals")
	}
	parts := []Part{docsPart}
	if req.Want != PartNameDocs {
		catPart, err := sealTree(PartNameCatalog, catDir, "catalog", "kit/catalog")
		if err != nil {
			return Product{}, fail(CodeVerifyDocs, err, "cannot seal the kit catalog")
		}
		parts = append(parts, catPart)
	}
	return Product{
		Version: docsVersion(v, rep.Inventory), SortKey: v, Trust: manifest.TrustComputed,
		Parts: parts, Notices: notices, Source: source,
	}, nil
}

// fetchTarball puts the tarball of the tag at <staging>/src.tar(.gz) and returns its path, sha256 and
// where it came from: "git_archive" from a local-ai.git mirror of any mounted root (works offline),
// else "codeload" over https (online only).
func (r *RepoDocs) fetchTarball(ctx context.Context, req Request, tag, staging string) (file, sum, origin string, err error) {
	mirrorErr := error(nil)
	for _, root := range append([]string{req.Root}, req.OtherRoots...) {
		mirror := filepath.Join(root, "kit", "git", repoMirrorName+".git")
		if !isDir(mirror) {
			continue
		}
		f, s, aerr := r.archiveFromMirror(ctx, mirror, tag, staging)
		if aerr == nil {
			return f, s, "git_archive", nil
		}
		if errors.Is(aerr, context.Canceled) || errors.Is(aerr, context.DeadlineExceeded) {
			return "", "", "", aerr
		}
		mirrorErr = aerr
	}
	if err := r.Env.requireOnline(); err != nil {
		if mirrorErr != nil {
			return "", "", "", mirrorErr
		}
		return "", "", "", fail(CodeMirror, err, "no local-ai.git mirror holds %s and the node is offline", tag)
	}
	if r.Env.Downloader == nil {
		return "", "", "", fail(CodeVerifyDocs, nil, "no downloader is configured")
	}
	job := download.FileJob{
		RootID: req.RootID, Root: req.Root, Item: req.Item.ID, Version: req.Version, Name: tarballName,
		URL:          fmt.Sprintf("%s/%s/tar.gz/refs/tags/%s", r.base(), r.repo(), tag),
		MinFreeBytes: req.MinFreeBytes, Commit: true,
	}
	res, derr := r.Env.Downloader.Fetch(ctx, job, req.Checkpoints)
	if derr != nil {
		if errors.Is(derr, context.Canceled) || errors.Is(derr, context.DeadlineExceeded) {
			return "", "", "", derr
		}
		return "", "", "", fmt.Errorf("downloading the source of %s: %w", tag, derr)
	}
	if res.Bytes > maxDocsTarball {
		return "", "", "", fail(CodeVerifyDocs, nil, "the source tarball of %s is larger than %d bytes", tag, maxDocsTarball)
	}
	return job.FinalPath(), res.SHA256, "codeload", nil
}

// archiveFromMirror writes `git archive` of the tag of a local mirror to <staging>/src.tar and returns
// its sha256.
func (r *RepoDocs) archiveFromMirror(ctx context.Context, mirror, tag, staging string) (file, sum string, err error) {
	g := &GitMirror{Env: r.Env, allowLocal: r.allowLocal}
	bin, err := r.Env.tool("git")
	if err != nil {
		return "", "", err
	}
	ref := "refs/tags/" + tag
	if _, err := g.revParse(ctx, bin, mirror, ref); err != nil {
		return "", "", fail(CodeMirror, nil, "the local-ai mirror has no tag %s", tag)
	}
	out := filepath.Join(staging, "src.tar")
	res, err := g.git(ctx, bin, gitArchiveTimeout, append(g.repoArgs(mirror), "archive", "--format=tar", "--prefix=local-ai-"+tag+"/", "-o", out, ref)...)
	if err != nil {
		return "", "", err
	}
	if res.ExitCode != 0 {
		return "", "", fail(CodeMirror, nil, "git archive of %s failed: %s", tag, Clip(res.Stderr, 200))
	}
	sum, _, err = hashFile(out)
	if err != nil {
		return "", "", fail(CodeMirror, err, "cannot read the archive of %s", tag)
	}
	return out, sum, nil
}

// checkCatalog lints the staged catalog and checks that its CATALOG_VERSION is the tag's. A catalog
// that fails lint is never installed (LAI-CAT-002). pins.json is added when the release has none.
func (r *RepoDocs) checkCatalog(dir, version, tag string) error {
	rep := catalog.Lint(dir, catalog.LintOptions{})
	if !rep.Valid() {
		first := rep.Problems[0]
		return fail(CodeCatalogLint, nil, "the catalog of %s failed validation (%d problems, first %s %s): the previous kit/catalog is kept",
			tag, len(rep.Problems), first.Code, Clip([]byte(first.Message), 160))
	}
	data, err := os.ReadFile(filepath.Join(dir, catalog.FileVersion))
	if err != nil || strings.TrimSpace(string(data)) != version {
		return fail(CodeCatalogLint, nil, "the CATALOG_VERSION of %s is not %s: the previous kit/catalog is kept", tag, version)
	}
	pins := filepath.Join(dir, "pins.json")
	if _, err := os.Stat(pins); errors.Is(err, os.ErrNotExist) {
		skel, _ := json.MarshalIndent(map[string]any{
			"schema_version": 1, "updated_at": r.Env.now().UTC().Format("2006-01-02T15:04:05Z"),
			"llama_cpp": []any{}, "whisper_cpp": []any{}, "agent": []any{}, "codenames": []any{}, "nvidia_branches": []any{},
		}, "", "  ")
		if err := writeFileAtomic(pins, append(skel, '\n')); err != nil {
			return fail(CodeVerifyDocs, err, "cannot write pins.json")
		}
	} else if err != nil {
		return fail(CodeVerifyDocs, err, "cannot read pins.json")
	}
	return nil
}

// render runs the docs renderer in-process: --src <staging>/src/docs --out <staging>/tree
// --inventory <path>. It never executes anything from the tarball.
func (r *RepoDocs) render(ctx context.Context, docsSrc, srcDir, out, catDir, staging string) (*docsrender.Report, error) {
	tmp := filepath.Join(staging, "tmp")
	if err := fsperm.MkdirAll(tmp, 0o755); err != nil {
		return nil, fail(CodeVerifyDocs, err, "cannot create the staging directory")
	}
	state := r.State
	state.CatalogDirs = append([]string{catDir}, state.CatalogDirs...)
	var runner execx.Runner = r.Env.runner()
	cfg := docsrender.Config{
		Src: docsSrc, Outs: []string{out}, InventoryPath: r.inventory(),
		ExamplePath: filepath.Join(srcDir, "inventory.example.yaml"),
		NoPDF:       r.NoPDF, State: state, Hostname: r.Hostname, Now: r.Env.now(),
		Runner: runner, Lookup: r.Env.lookup, TmpDir: tmp, Owner: r.Owner,
	}
	rep, err := docsrender.Run(ctx, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fail(renderCode(err), err, "rendering the manuals failed")
	}
	// Warnings can name documents but never carry a value of the inventory; only counts are logged.
	r.Env.log().Info("manuals rendered", "component", "sync", "pages", rep.Pages, "pdfs", rep.PDFs, "warnings", len(rep.Warnings), "inventory", rep.Inventory)
	return rep, nil
}

// renderCode maps the renderer's fatal errors to the doctor codes of `aictl docs render`.
func renderCode(err error) string {
	var de *docsrender.Error
	if errors.As(err, &de) {
		switch de.Kind {
		case docsrender.KindCardPages:
			return doctor.CLI015
		case docsrender.KindCardSecret:
			return doctor.CLI016
		}
	}
	return doctor.CLI012
}

// verify checks the rendered tree (ark-sync.md section 4.9.8): index.html exists, every runbook the
// docs README lists has an HTML file, and the survival card HTML exists. A missing HTML file fails the
// item. A missing survival-card PDF does not: the item verifies and READY shows docs yellow with
// LAI-CLI-014. Rendering without the real inventory is the yellow LAI-SYN-062.
func (r *RepoDocs) verify(docsSrc, tree string, rep *docsrender.Report) ([]Notice, error) {
	if !isFile(filepath.Join(tree, "index.html")) {
		return nil, fail(CodeVerifyDocs, nil, "the rendered manuals have no index.html")
	}
	readme, err := os.ReadFile(filepath.Join(docsSrc, "README.md"))
	if err != nil {
		return nil, fail(CodeVerifyDocs, err, "cannot read docs/README.md")
	}
	var missing []string
	cardSlug := strings.TrimSuffix(path.Base(docsrender.CardTemplateRel), ".md")
	for _, slug := range listedRunbooks(readme) {
		if slug == cardSlug {
			continue // the card template is not a page: it renders as survival-card-<node>.html, checked below
		}
		if !isFile(filepath.Join(tree, slug+".html")) {
			missing = append(missing, slug+".html")
		}
	}
	if rep.Card == "" || !isFile(filepath.Join(tree, rep.Card+".html")) {
		missing = append(missing, "the survival card")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fail(CodeVerifyDocs, nil, "the rendered manuals lack: %s", strings.Join(missing, ", "))
	}
	var notices []Notice
	if !rep.Inventory {
		notices = append(notices, Notice{Code: CodeNoInventory, Message: "manuals rendered without inventory.local.yaml: addresses stay as placeholders"})
	}
	if !isFile(filepath.Join(tree, rep.Card+".pdf")) {
		notices = append(notices, Notice{Code: CodeNoPDF, Message: "no PDF of the survival card (Chromium is missing or failed): the HTML pages are complete"})
	}
	return notices, nil
}

// listedRunbooks returns the slugs of the runbooks docs/README.md links to ("01-survival-mode" for
// runbooks/01-survival-mode.md), sorted, each once.
func listedRunbooks(readme []byte) []string {
	set := map[string]bool{}
	for _, m := range runbookLinkRe.FindAllSubmatch(readme, -1) {
		if name := string(m[1]); name != "README" {
			set[path.Clean(name)] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
