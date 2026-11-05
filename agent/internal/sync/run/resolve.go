package run

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// ItemResolver turns a catalog item into what the machine downloads: the target version, where it
// comes from and its files (item.Resolution). It is the engine-facing half of a source resolver; the
// planner's half (plan.Resolver, version and size only) is registered separately in plan.Resolvers.
// SYNC-09 to SYNC-11 implement one per source type (huggingface, kiwix, pmtiles_extract,
// github_release, url) and register it with ItemResolvers.Register. git_mirror, apt_closure and repo_docs
// are made by the producers of internal/sync/resolve: RegisterProducer (product.go) registers them.
// An error should be an *item.ResolveError carrying the doctor code (LAI-SYN-021, -022, -030, -033,
// -034, -061); a plain error is a transient failure.
type ItemResolver interface {
	// Resolve is called once per item per run, in the item's resolving state. target is the planner's
	// row (ToVersion is the version the plan expected; the resolver may find a newer one).
	Resolve(ctx context.Context, it plan.CatalogItem, target plan.ItemPlan) (item.Resolution, error)
}

// ItemResolverFunc adapts a function to ItemResolver.
type ItemResolverFunc func(ctx context.Context, it plan.CatalogItem, target plan.ItemPlan) (item.Resolution, error)

// Resolve implements ItemResolver.
func (f ItemResolverFunc) Resolve(ctx context.Context, it plan.CatalogItem, target plan.ItemPlan) (item.Resolution, error) {
	return f(ctx, it, target)
}

// ItemResolvers is the registry of ItemResolvers by catalog source type. Safe for concurrent use.
type ItemResolvers struct {
	mu    sync.RWMutex
	m     map[string]ItemResolver
	tasks map[string]TaskResolver
}

// NewItemResolvers returns an empty registry.
func NewItemResolvers() *ItemResolvers {
	return &ItemResolvers{m: map[string]ItemResolver{}, tasks: map[string]TaskResolver{}}
}

// Register adds the resolver of a source type; a second one for the same type is an error.
func (r *ItemResolvers) Register(sourceType string, res ItemResolver) error {
	if sourceType == "" || res == nil {
		return errors.New("run: a resolver needs a source type and an implementation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[sourceType]; dup {
		return fmt.Errorf("run: a resolver for source type %q is already registered", sourceType)
	}
	if _, dup := r.tasks[sourceType]; dup {
		return fmt.Errorf("run: a resolver for source type %q is already registered", sourceType)
	}
	r.m[sourceType] = res
	return nil
}

// RegisterTask adds the TaskResolver of a source type (the resolvers made with a producer, see
// RegisterProducer); a second resolver of either kind for the same type is an error.
func (r *ItemResolvers) RegisterTask(sourceType string, res TaskResolver) error {
	if sourceType == "" || res == nil {
		return errors.New("run: a resolver needs a source type and an implementation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[sourceType]; dup {
		return fmt.Errorf("run: a resolver for source type %q is already registered", sourceType)
	}
	if _, dup := r.tasks[sourceType]; dup {
		return fmt.Errorf("run: a resolver for source type %q is already registered", sourceType)
	}
	r.tasks[sourceType] = res
	return nil
}

// TaskFor returns the TaskResolver of a source type.
func (r *ItemResolvers) TaskFor(sourceType string) (TaskResolver, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.tasks[sourceType]
	return res, ok
}

// For returns the resolver of a source type.
func (r *ItemResolvers) For(sourceType string) (ItemResolver, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.m[sourceType]
	return res, ok
}

// Pinned resolves what the catalog itself pins, the fallback for a source type with no registered
// resolver: a model whose files carry a sha256 and size (the URL is the file's own, or the Hugging
// Face URL built from the repo, the revision and the file name), a `url` item with its pinned sha256,
// and a pinned kiwix file. Anything else is unresolved (LAI-SYN-033): without a resolver there is no
// way to know what to fetch. The digest of a pinned file is the catalog's (trust: pinned).
func Pinned(it plan.CatalogItem, target plan.ItemPlan, catalogVersion string) (item.Resolution, error) {
	fail := func(format string, a ...any) (item.Resolution, error) {
		return item.Resolution{}, &item.ResolveError{Code: doctor.SYN033, Permanent: true, Err: fmt.Errorf(format, a...)}
	}
	version := target.ToVersion
	if version == "" {
		if r, ok := plan.FromCatalog(it, catalogVersion); ok {
			version = r.Version
		}
	}
	if version == "" {
		return fail("the catalog does not pin a version of %s", it.ID)
	}
	switch {
	case it.Model != nil:
		m := it.Model
		if len(m.Files) == 0 {
			return fail("model %s lists no files", m.ID)
		}
		src := manifest.Source{Type: m.Source.Type}
		var err error
		for k, v := range map[string]string{"repo": m.Source.Repo, "revision": m.Source.Revision} {
			if v != "" {
				if src, err = src.With(k, v); err != nil {
					return fail("%v", err)
				}
			}
		}
		res := item.Resolution{Version: version, SortKey: version, Source: src, Trust: manifest.TrustPinned, Resolver: "catalog_pin"}
		for _, f := range m.Files {
			u := f.URL
			if u == "" && m.Source.Type == "huggingface" && m.Source.Repo != "" && m.Source.Revision != "" {
				u = "https://huggingface.co/" + m.Source.Repo + "/resolve/" + m.Source.Revision + "/" + f.Name
			}
			if u == "" || f.SHA256 == "" {
				return fail("file %s of model %s is not pinned (no URL or sha256)", f.Name, m.ID)
			}
			res.Files = append(res.Files, item.File{
				Path: f.Name, Kind: f.Kind, URL: u, Size: f.SizeBytes, SHA256: strings.ToLower(f.SHA256), SHA256Source: "catalog",
				Auth: m.Source.Type == "huggingface",
			})
		}
		return res, nil
	case it.Content != nil && it.Content.Source.Type == "url":
		c := it.Content
		src := c.Source
		files := src.Files
		if len(files) == 0 && src.URL != "" {
			files = []catalog.URLFile{{URL: src.URL, SHA256: src.SHA256}}
		}
		if len(files) == 0 {
			return fail("item %s has no URL", c.ID)
		}
		res := item.Resolution{Version: version, SortKey: version, Source: manifest.Source{Type: "url"}, Trust: manifest.TrustPinned, Resolver: "catalog_pin"}
		for _, f := range files {
			name := urlBase(f.URL)
			if f.Name != "" {
				name = f.Name
				if !manifest.SafePath(name) {
					name = ""
				}
			}
			if name == "" {
				return fail("the URL of item %s has no file name", c.ID)
			}
			if f.SHA256 == "" {
				return fail("file %s of item %s is not pinned (no sha256)", name, c.ID)
			}
			size := int64(0)
			if len(files) == 1 {
				size = c.ExpectedSizeBytes
			}
			res.Files = append(res.Files, item.File{Path: name, Kind: "file", URL: f.URL, Size: size, SHA256: strings.ToLower(f.SHA256), SHA256Source: "catalog"})
		}
		return res, nil
	case it.Content != nil && it.Content.Source.Type == "kiwix":
		src := it.Content.Source
		if src.VersionPolicy != "pinned" || src.Pinned == nil || src.BaseURL == "" || src.Pinned.SHA256 == "" {
			return fail("item %s follows the newest file: it needs the kiwix resolver", it.ID)
		}
		u := strings.TrimRight(src.BaseURL, "/") + "/" + src.Pinned.File
		srcM, _ := manifest.Source{Type: "kiwix"}.With("book", src.Book)
		return item.Resolution{
			Version: version, SortKey: version, Source: srcM, Trust: manifest.TrustPinned, Resolver: "catalog_pin",
			Files: []item.File{{Path: src.Pinned.File, Kind: "zim", URL: u, Size: it.Content.ExpectedSizeBytes, SHA256: strings.ToLower(src.Pinned.SHA256), SHA256Source: "catalog"}},
		}, nil
	}
	return fail("source type %q has no resolver registered", it.SourceType())
}

func urlBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	b := path.Base(u.Path)
	if b == "." || b == "/" || !manifest.SafeName(b) {
		return ""
	}
	return b
}

// CopyFinder lists the verified copies of an item (*ark.Index).
type CopyFinder interface {
	Copies(itemID string) []ark.Copy
}

// LocalCopy builds the resolution of a local_copy task: the files of the verified copy on the root the
// plan says to copy from, each read through a download.LocalSource (the same .part, checkpoint and
// streaming sha256 code as a download, and the bytes are verified against the source's own manifest
// before they are trusted). A directory item copies every file of the source's SHA256SUMS into the
// staged tree, and the tree hash of the source manifest is the expected one. Source failures are
// LAI-SYN-070 (a damaged source copy), reported by the downloader.
func LocalCopy(fsys config.FS, copies CopyFinder, target plan.ItemPlan) (item.Resolution, error) {
	fail := func(format string, a ...any) (item.Resolution, error) {
		return item.Resolution{}, &item.ResolveError{Code: doctor.SYN070, Permanent: true, Err: fmt.Errorf(format, a...)}
	}
	var src *ark.Copy
	for _, c := range copies.Copies(target.ID) {
		c := c
		if c.RootID == target.FromRoot && (target.ToVersion == "" || c.Manifest.Version == target.ToVersion) {
			src = &c
			break
		}
	}
	if src == nil {
		return fail("root %s holds no verified copy of %s to copy from", target.FromRoot, target.ID)
	}
	m := src.Manifest
	res := item.Resolution{
		Version: m.Version, SortKey: m.SortKey, Source: m.Source, Trust: m.Trust, Resolver: "local_copy", Local: true, Dir: m.IsDir(),
	}
	local := download.OSFS{}
	if m.IsDir() {
		sums, err := manifest.ReadSums(fsys, src.Dir)
		if err != nil {
			return fail("the source copy has no usable %s: %v", manifest.SumsName, err)
		}
		res.ExpectedTree = m.TreeSHA256
		for _, s := range sums {
			res.Files = append(res.Files, item.File{
				Path: s.Path, Kind: "file", SHA256: s.SHA256, SHA256Source: "source_manifest",
				Source: download.NewLocalSource(local, filepath.Join(src.Dir, filepath.FromSlash(s.Path))),
			})
		}
		return res, nil
	}
	for _, f := range m.Files {
		res.Files = append(res.Files, item.File{
			Path: f.Path, Kind: f.Kind, Size: f.SizeBytes, SHA256: f.SHA256, SHA256Source: f.SHA256Source, ZimMD5OK: f.ZimMD5OK,
			Source: download.NewLocalSource(local, filepath.Join(src.Dir, filepath.FromSlash(f.Path))),
		})
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return res, nil
}
