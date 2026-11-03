package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
)

// Products (ark-sync.md section 4.9.9). The producers of internal/sync/resolve (git_mirror, apt_closure,
// repo_docs) answer with a resolve.Product made of several parts. The engine installs them per catalog
// item, because an item is what has a journal record, a verification, a swap and a MANIFEST.json:
//
//   - the part whose destination is the item's own directory (Part.To equals the directory the layout
//     gives the item: docs/, kit/catalog/, kit/git/<name>.git, kit/debs/<codename>-<arch>) is the
//     item's content: a tree swapped in, or, for a mirror that is already there, the directory
//     updated in place;
//   - a file set that belongs beside the item (the source tarballs of a mirror, destination kit/src/)
//     is a companion of that item, when the kit layout says the item writes there (kit.LayoutOf Also,
//     a part of kit.Parts); it is installed by the item's own swap and recorded in its manifest;
//   - any other part of the product belongs to another catalog item (repo_docs makes the docs tree and
//     the kit catalog, which are two items) and is that item's job, not this one's.
//
// So a product with two items is two tasks, each with its own journal, verification, swap and
// manifest, and one part failing leaves the other verified.

// Task is what the engine knows about one item on one root when it asks a resolver for it.
type Task struct {
	Item plan.CatalogItem
	// Row is the planner's row: ToVersion is the version the plan expected.
	Row  plan.ItemPlan
	Root ark.Root
	// Ref and RelDir place the item (DefaultItemRef, ark.ItemDir).
	Ref    ark.ItemRef
	RelDir string
	// CatalogVersion is the version of the catalog the plan was made with.
	CatalogVersion string
	MinFree        int64
	// OtherRoots are the other mounted content roots.
	OtherRoots []ark.Root
	// Present is the verified copy of the item on this root, nil when there is none.
	Present *ark.Copy
}

// TaskResolver is an ItemResolver that needs to know where the item goes (the root, the present copy,
// the other roots): the resolvers that make their item with a producer. Register it with
// ItemResolvers.RegisterTask.
type TaskResolver interface {
	ResolveTask(ctx context.Context, tk Task) (item.Resolution, error)
	// CopiesLocally reports whether a second root may be filled by copying the first root's verified
	// directory file by file (run.LocalCopy). A git mirror may not: its SHA256SUMS records HEAD only,
	// so each root fetches its own.
	CopiesLocally() bool
}

// ProductResolver turns a resolve.Producer into an item.Resolution with a Produce: the engine-facing
// half of git_mirror, apt_closure and repo_docs.
type ProductResolver struct {
	Producer resolve.Producer
	// NewerOK: the product may be newer than the plan expected (a git remote that moved between the plan
	// and the fetch). The item is then installed under the planned version and the manifest records
	// the commit it holds; the next plan sees the newer one. Without it a different version is an error
	// (the plan no longer holds: the next run plans again).
	NewerOK bool
	// NoLocalCopy: the product is never copied file by file between roots (a git mirror).
	NoLocalCopy bool
	// Want names the part the item takes from the product (Request.Want); nil asks for all of them.
	Want   func(plan.CatalogItem) string
	Logger *slog.Logger
}

// CopiesLocally implements TaskResolver.
func (p ProductResolver) CopiesLocally() bool { return !p.NoLocalCopy }

func (p ProductResolver) log() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// RegisterProducer registers a producer as the engine-facing resolver of its source type, with the
// behaviour of each type: git_mirror may be newer than planned and is not copied between roots,
// repo_docs asks for the part of its item (the docs tree for the docs item, the catalog for the kit
// catalog item), apt_closure is plain.
func RegisterProducer(r *ItemResolvers, sourceType string, p resolve.Producer) error {
	pr := ProductResolver{Producer: p}
	switch sourceType {
	case "git_mirror":
		pr.NewerOK, pr.NoLocalCopy = true, true
	case "repo_docs":
		pr.Want = func(it plan.CatalogItem) string {
			if it.Kind == manifest.KindKit {
				return resolve.PartNameCatalog
			}
			return resolve.PartNameDocs
		}
	case "apt_closure":
	default:
		return fmt.Errorf("run: %q is not a source type with a producer", sourceType)
	}
	return r.RegisterTask(sourceType, pr)
}

// ResolveTask implements TaskResolver.
func (p ProductResolver) ResolveTask(_ context.Context, tk Task) (item.Resolution, error) {
	fail := func(code string, permanent bool, format string, a ...any) (item.Resolution, error) {
		return item.Resolution{}, &item.ResolveError{Code: code, Permanent: permanent, Err: fmt.Errorf(format, a...)}
	}
	version := tk.Row.ToVersion
	if !manifest.SafeName(version) {
		return fail(doctor.SYN033, true, "the plan has no version for %s", tk.Item.ID)
	}
	if tk.Item.Content == nil {
		return fail(item.CodeInternal, true, "%s is not a content item", tk.Item.ID)
	}
	var also []string
	if tk.Item.Kind == manifest.KindKit {
		l, err := kit.LayoutOf(*tk.Item.Content)
		if err != nil {
			return fail(doctor.SYN033, true, "%v", err)
		}
		also = l.Also
	}
	srcType := tk.Item.SourceType()
	req := resolve.Request{
		Item: tk.Item, RootID: tk.Root.ID, Root: tk.Root.Path, Drive: tk.Root.Kind == ark.KindArkDrive,
		Version: version, CatalogVersion: tk.CatalogVersion, ItemDir: tk.RelDir, MinFreeBytes: tk.MinFree,
	}
	for _, r := range tk.OtherRoots {
		req.OtherRoots = append(req.OtherRoots, r.Path)
	}
	if tk.Present != nil {
		req.PresentDir, req.PresentVersion = tk.Present.Dir, tk.Present.Manifest.Version
	}
	if p.Want != nil {
		req.Want = p.Want(tk.Item)
	}
	return item.Resolution{
		Version: version, Source: manifest.Source{Type: srcType}, Dir: true, Resolver: srcType,
		Produce: func(ctx context.Context, env item.ProduceEnv) (item.Produced, error) {
			if env.StagingDir != req.StagingDir() {
				return item.Produced{}, &item.ResolveError{Code: item.CodeInternal, Permanent: true,
					Err: fmt.Errorf("the staging directory of %s is not where the producer writes", tk.Item.ID)}
			}
			req.Checkpoints = env.Checkpoints
			prod, err := p.Producer.Produce(ctx, req)
			if err != nil {
				return item.Produced{}, mapProducerError(err)
			}
			return p.produced(tk, req, also, prod)
		},
	}, nil
}

// permanentCodes are the producer errors that the next run will meet again unless something changes:
// the source refuses, a tool is missing, the catalog of the release does not pass lint.
var permanentCodes = map[string]bool{doctor.SYN030: true, doctor.SYN061: true, doctor.CAT002: true}

// mapProducerError gives a producer's own error the shape the machine reads (*item.ResolveError with
// the doctor code); the downloader's errors and a cancel pass through unchanged.
func mapProducerError(err error) error {
	var re *resolve.Error
	if errors.As(err, &re) {
		return &item.ResolveError{Code: re.Code, Permanent: permanentCodes[re.Code], Err: err}
	}
	return err
}

// produced maps what a producer made to what the machine installs for this item.
func (p ProductResolver) produced(tk Task, req resolve.Request, also []string, prod resolve.Product) (item.Produced, error) {
	fail := func(code string, permanent bool, format string, a ...any) (item.Produced, error) {
		return item.Produced{}, &item.ResolveError{Code: code, Permanent: permanent, Err: fmt.Errorf(format, a...)}
	}
	out := item.Produced{SortKey: prod.SortKey, Trust: prod.Trust}
	if len(prod.Source) > 0 {
		out.Source = make(map[string]any, len(prod.Source)+1)
		for k, v := range prod.Source {
			out.Source[k] = v
		}
	}
	for _, n := range prod.Notices {
		out.Notices = append(out.Notices, n.Code)
		p.log().Info("item produced with a notice", "component", "sync", "item", tk.Item.ID, "code", n.Code)
	}
	if prod.Version != req.Version {
		if !p.NewerOK {
			return fail(doctor.SYN033, false, "the plan was made for %s and the source now gives %s: the next run plans again", req.Version, prod.Version)
		}
		p.log().Info("the source moved since the plan", "component", "sync", "item", tk.Item.ID, "planned", req.Version, "produced", prod.Version)
		if out.Source == nil {
			out.Source = map[string]any{}
		}
		out.Source["produced_version"] = prod.Version
	}
	if prod.Keep {
		out.Keep = true
		return out, nil
	}
	var primary *resolve.Part
	for i := range prod.Parts {
		part := prod.Parts[i]
		switch {
		case part.To == tk.RelDir:
			if primary != nil {
				return fail(item.CodeInternal, true, "the producer made two parts for %s", tk.RelDir)
			}
			primary = &prod.Parts[i]
		case part.Kind == resolve.PartFile && isCompanionDir(part.To, also):
			files := make([]item.CompanionFile, len(part.Files))
			for j, f := range part.Files {
				files[j] = item.CompanionFile{Path: f.Path, Kind: f.Kind, Size: f.SizeBytes, SHA256: f.SHA256, SHA256Source: f.SHA256Source}
			}
			out.Companions = append(out.Companions, item.Companion{Key: part.Name, From: part.From, To: part.To, Files: files})
		}
		// any other part is another item's: its own task installs it
	}
	if primary == nil {
		return fail(item.CodeInternal, true, "the producer made no part for %s", tk.RelDir)
	}
	switch primary.Kind {
	case resolve.PartTree:
		out.TreeFrom = primary.From
		out.Record = primary.Record
		if len(primary.Record) == 0 {
			out.TreeSHA256 = primary.TreeSHA256
		}
	case resolve.PartInPlace:
		if len(primary.Record) == 0 {
			return fail(item.CodeInternal, true, "the in-place part for %s records no file", tk.RelDir)
		}
		out.InPlace, out.Record = true, primary.Record
	default:
		return fail(item.CodeInternal, true, "the part for %s is a file set: only a tree or an in-place update is an item", tk.RelDir)
	}
	return out, nil
}

// isCompanionDir reports whether dir (a slash path below the root) is kit/<part> for a part of the kit
// layout that the item is allowed to write into (kit.LayoutOf Also).
func isCompanionDir(dir string, also []string) bool {
	if !strings.HasPrefix(dir, kit.DirName+"/") {
		return false
	}
	rel := strings.TrimPrefix(dir, kit.DirName+"/")
	if !kit.ValidPart(rel) {
		return false
	}
	for _, a := range also {
		if a == rel {
			return true
		}
	}
	return false
}

// taskOf builds the Task of a scheduled task.
func (e *Engine) taskOf(t *task) Task {
	tk := Task{Item: t.cat, Row: t.row, Root: t.root, Ref: t.ref, RelDir: t.rel, CatalogVersion: t.catalogVersion, MinFree: t.minFree}
	for _, r := range e.d.Planner.Roots() {
		if r.ID != t.root.ID {
			tk.OtherRoots = append(tk.OtherRoots, r)
		}
	}
	for _, c := range e.d.Copies.Copies(t.row.ID) {
		if c.RootID == t.root.ID {
			c := c
			tk.Present = &c
			break
		}
	}
	return tk
}
