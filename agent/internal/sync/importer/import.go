// Package importer plans the import of content from an AI-ARK drive into the internal root
// (docs/specs/ark-sync.md section 4.14, `aictl ark import`, POST /ark/import).
//
// An import is how an offline node receives new brains, books and maps: it copies verified items from a
// drive to the internal disk with no network at all, in every mode including survival. The copy itself
// is the normal run engine's, through the item state machine (internal/sync/item) with a local source:
// a streaming sha256 against the drive's MANIFEST and the catalog's pin, a .part and a checkpoint, the
// swap committed by MANIFEST.json, the old version moved to .trash. What this package adds is the part
// before the copy, the plan:
//
//   - the source drive: --from, or the only mounted drive (several: LAI-ARK-015 with their ids);
//   - the catalog on the drive (catalog: true), installed first when it is newer and valid (an invalid
//     one is LAI-CAT-002 and the import goes on with the catalog the node has);
//   - the internal root planned by the ordinary planner with the network off, with Resolved read from
//     the drive's MANIFESTs instead of the resolvers and OtherRoots holding only the drive's copies, so
//     every download or update is source = local_copy; an item the drive does not hold is
//     skip (unresolved);
//   - the items filter, and add_to_profile, which appends an item that is not in the node's profile to
//     node.yaml profile_overrides.add_models or add_content; without it such an item is refused
//     (skip, not_in_profile);
//   - the drive's version never replaces a newer one on the internal root.
//
// The package reads the drive through the planner's gatherer and the best-copy index; it writes only
// node.yaml (through a ProfileEditor) and the installed catalog (through a CatalogInstaller), and not
// at all for a dry run. It opens no socket: nonet.go is the transport and the dialer an import's
// downloader is built over.
package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// Request is one import, as the route decoded it.
type Request struct {
	// From is the root id (or path) of the drive; empty means the only mounted drive.
	From string
	// Items limits the import to these catalog ids; empty means everything the profile wants that the
	// drive holds.
	Items []string
	// AddToProfile appends a named item that is not in the profile to node.yaml. It needs Items.
	AddToProfile bool
	// Catalog installs the drive's catalog first when it is newer and valid.
	Catalog bool
	// DryRun plans and writes nothing: no catalog is installed and node.yaml is not touched.
	DryRun bool
}

// Errors of planning. The adapter turns each into the status and code of the route.
type (
	// SeveralDrivesError: no source was named and more than one drive is mounted (LAI-ARK-015).
	SeveralDrivesError struct{ Roots []string }
	// UnknownDriveError: the named root is not a mounted ark drive.
	UnknownDriveError struct{ Root string }
	// UnusableDriveError: the named drive cannot be read from now (unclean, ejecting, ...).
	UnusableDriveError struct{ Root, State string }
)

// Code is the doctor code of each error, which a run that failed on it reports.
func (*SeveralDrivesError) Code() string { return doctor.ARK015 }

// Code implements the same for a drive that is not mounted.
func (*UnknownDriveError) Code() string { return doctor.AGT020 }

// Code is LAI-ARK-004 for a drive with filesystem errors, LAI-AGT-020 for any other state.
func (e *UnusableDriveError) Code() string {
	if e.State == string(ark.StateUnclean) {
		return doctor.ARK004
	}
	return doctor.AGT020
}

func (e *SeveralDrivesError) Error() string {
	return doctor.ARK015 + ": several ark drives are connected: " + strings.Join(e.Roots, ", ")
}
func (e *UnknownDriveError) Error() string { return "importer: not a mounted ark drive: " + e.Root }
func (e *UnusableDriveError) Error() string {
	return fmt.Sprintf("importer: the drive %s cannot be imported from while it is %s", e.Root, e.State)
}

// ErrNoDrive: no source was named and no ark drive can be read from.
var ErrNoDrive = errors.New("importer: no ark drive is connected")

// ErrNeedItems: add_to_profile without items. Adding "everything on the drive" to a profile is never
// what a person means; they name what they want.
var ErrNeedItems = errors.New("importer: add_to_profile needs the items to add")

// Gatherer is the part of *plan.Gatherer the importer reads.
type Gatherer interface {
	Roots() []ark.Root
	Gather(ctx context.Context, rootID string, opt plan.GatherOptions) (plan.PlanInput, error)
}

// CatalogInstaller installs the catalog of a drive as the catalog in use.
type CatalogInstaller interface {
	// Version is the version of the catalog in use ("" when there is none).
	Version() string
	// Install lints the catalog in dir and, when it is valid, installs it so that the next plan sees it
	// (catalog.Install, then the in-memory store). A rejected catalog is an error whose Code() is
	// LAI-CAT-002 (*catalog.RejectedError).
	Install(ctx context.Context, dir string) (version string, err error)
}

// ProfileEditor records items in node.yaml profile_overrides (with the .bak the config store keeps).
type ProfileEditor interface {
	// AddItems appends model ids to add_models and content ids to add_content, and takes them out of
	// remove_models and remove_content, so the items are wanted after the call.
	AddItems(models, content []string) error
}

// Deps are what a Planner reads and writes. G is required.
type Deps struct {
	G Gatherer
	// Catalog installs a drive's catalog; nil means catalog: true only warns.
	Catalog CatalogInstaller
	// Profile writes node.yaml; nil means add_to_profile is refused.
	Profile ProfileEditor
	Logger  *slog.Logger
}

// Planner makes the plan of an import.
type Planner struct{ d Deps }

// New checks the dependencies and returns a Planner.
func New(d Deps) (*Planner, error) {
	if d.G == nil {
		return nil, errors.New("importer: needs a Gatherer")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Planner{d: d}, nil
}

// Result is the plan of an import and what was done to make it.
type Result struct {
	// Drive is the source.
	Drive ark.Root
	// Plan is the plan of the internal root; its rows that copy have FromRoot set to the drive.
	Plan plan.RootPlan
	// Input is what the plan was made from (the catalog it used, for item lookups).
	Input plan.PlanInput
	// Warnings are plain notes: an invalid catalog on the drive (LAI-CAT-002), a profile that was
	// changed.
	Warnings []string
	// Added are the ids appended to the profile.
	Added []string
	// CatalogInstalled is the version of the drive's catalog that was installed ("" when none was).
	CatalogInstalled string
}

// Lookup returns the catalog item with an id, from the catalog the plan was made with.
func (r Result) Lookup(id string) (plan.CatalogItem, bool) {
	cat := r.Input.Catalog
	if cat == nil {
		return plan.CatalogItem{}, false
	}
	if m, ok := cat.Model(id); ok {
		return plan.ModelItem(m), true
	}
	if c, ok := cat.ContentItem(id); ok {
		return plan.ContentItem(c), true
	}
	return plan.CatalogItem{}, false
}

// Source resolves the drive an import reads from: the named root, or the only drive that can be read.
func (p *Planner) Source(from string) (ark.Root, error) {
	roots := p.d.G.Roots()
	if from != "" {
		for _, r := range roots {
			if r.ID == from || (r.Path != "" && filepath.Clean(r.Path) == filepath.Clean(from)) || (r.Mount != "" && filepath.Clean(r.Mount) == filepath.Clean(from)) {
				if r.Kind != ark.KindArkDrive {
					return ark.Root{}, &UnknownDriveError{Root: from}
				}
				if !importable(r) {
					return ark.Root{}, &UnusableDriveError{Root: r.ID, State: string(r.State)}
				}
				return r, nil
			}
		}
		return ark.Root{}, &UnknownDriveError{Root: from}
	}
	var drives []ark.Root
	for _, r := range roots {
		if r.Kind == ark.KindArkDrive && importable(r) {
			drives = append(drives, r)
		}
	}
	switch len(drives) {
	case 0:
		return ark.Root{}, ErrNoDrive
	case 1:
		return drives[0], nil
	}
	ids := make([]string, len(drives))
	for i, d := range drives {
		ids[i] = d.ID
	}
	return ark.Root{}, &SeveralDrivesError{Roots: ids}
}

// importable: a drive the agent can read verified copies from. An unclean drive is not: its copies are
// invisible to the best-copy index until they pass their serve-time check (section 4.1.3), and a person
// who wants the content of a drive with filesystem errors should repair it first.
func importable(r ark.Root) bool {
	return r.Path != "" && (r.State == "" || r.State == ark.StateReady || r.State == ark.StateReadOnly)
}

// Plan makes the plan of an import. It installs the drive's catalog and changes node.yaml when asked,
// unless it is a dry run.
func (p *Planner) Plan(ctx context.Context, req Request) (Result, error) {
	if req.AddToProfile && len(req.Items) == 0 {
		return Result{}, ErrNeedItems
	}
	drive, err := p.Source(req.From)
	if err != nil {
		return Result{}, err
	}
	res := Result{Drive: drive}
	log := p.d.Logger.With("component", "sync", "drive", drive.ID)

	if req.Catalog {
		p.installCatalog(ctx, drive, req.DryRun, &res, log)
	}

	offline := false
	in, err := p.d.G.Gather(ctx, ark.InternalID, plan.GatherOptions{Resolve: false, Online: &offline})
	if err != nil {
		return Result{}, err
	}
	in.Offline = true
	in.Import = true

	// The drive's copies, the only others the planner may copy from.
	onDrive := map[string]manifest.Manifest{}
	others := map[string][]plan.RootCopy{}
	for id, copies := range in.OtherRoots {
		for _, c := range copies {
			if c.RootID == drive.ID {
				if _, dup := onDrive[id]; !dup {
					onDrive[id] = c.Manifest
					others[id] = []plan.RootCopy{c}
				}
			}
		}
	}
	in.OtherRoots = others

	// The items asked for that the profile does not list.
	desired := map[string]bool{}
	for _, it := range plan.DesiredItems(in.Catalog, in.Profile, in.Overrides, in.Root, in.Node).Items {
		desired[it.ID] = true
	}
	var refused, missing []string
	var addModels, addContent []string
	for _, id := range dedupe(req.Items) {
		if desired[id] {
			continue
		}
		m, onIt := onDrive[id]
		_, isModel := catalogModel(in.Catalog, id)
		_, isContent := catalogContent(in.Catalog, id)
		switch {
		case !onIt || (!isModel && !isContent):
			missing = append(missing, id)
		case !req.AddToProfile:
			refused = append(refused, id)
		case isModel && m.ItemKind == manifest.KindModel:
			addModels = append(addModels, id)
		default:
			addContent = append(addContent, id)
		}
	}
	if len(addModels)+len(addContent) > 0 {
		if !req.DryRun {
			if p.d.Profile == nil {
				return Result{}, errors.New("importer: node.yaml cannot be changed here")
			}
			if err := p.d.Profile.AddItems(addModels, addContent); err != nil {
				return Result{}, fmt.Errorf("importer: adding to the profile: %w", err)
			}
			log.Info("items were added to the profile for an import", "models", addModels, "content", addContent)
		}
		ov := in.Overrides
		if ov == nil {
			ov = &plan.ProfileOverrides{}
		}
		merged := *ov
		merged.AddModels = appendUnique(merged.AddModels, addModels)
		merged.AddContent = appendUnique(merged.AddContent, addContent)
		merged.RemoveModels = without(merged.RemoveModels, addModels)
		merged.RemoveContent = without(merged.RemoveContent, addContent)
		in.Overrides = &merged
		res.Added = append(append([]string{}, addModels...), addContent...)
		sort.Strings(res.Added)
		for _, id := range res.Added {
			desired[id] = true
		}
	}

	// Resolved comes from the drive, not from a resolver. An older version on the drive never replaces a
	// newer one here, and a newer one is taken even when its refresh is not due: someone asked.
	in.Resolved = map[string]plan.Resolved{}
	for id, dm := range onDrive {
		if !desired[id] {
			continue
		}
		if pres, ok := in.Present[id]; ok {
			if pres.Version == dm.Version || pres.SortKey >= dm.SortKey {
				in.Resolved[id] = plan.Resolved{Version: pres.Version, SortKey: pres.SortKey, SizeBytes: pres.TotalBytes}
				continue
			}
			pres.DownloadedAt = time.Time{} // makes the refresh due, whatever the copy's age
			in.Present[id] = pres
		}
		in.Resolved[id] = plan.Resolved{Version: dm.Version, SortKey: dm.SortKey, SizeBytes: dm.TotalBytes}
	}

	rp := plan.Plan(in)
	for _, id := range refused {
		rp.Items = append(rp.Items, plan.ItemPlan{ID: id, Kind: onDrive[id].ItemKind, Action: plan.ActionSkip, Reason: plan.ReasonNotInProfile, ToVersion: onDrive[id].Version})
	}
	for _, id := range missing {
		rp.Items = append(rp.Items, plan.ItemPlan{ID: id, Action: plan.ActionSkip, Reason: plan.ReasonUnresolved})
	}
	res.Plan, res.Input = rp, in
	return res, nil
}

// installCatalog installs the catalog on the drive when it is newer than the one in use and valid. A
// catalog that is not valid is a warning with LAI-CAT-002 and the import goes on with the catalog the
// node has (ark-sync.md section 4.14 step 2).
func (p *Planner) installCatalog(ctx context.Context, drive ark.Root, dry bool, res *Result, log *slog.Logger) {
	dir := filepath.Join(drive.Path, "kit", "catalog")
	data, err := os.ReadFile(filepath.Join(dir, "CATALOG_VERSION"))
	if err != nil {
		return // the drive carries no catalog: nothing to do
	}
	driveVersion := strings.TrimSpace(string(data))
	cur := ""
	if p.d.Catalog != nil {
		cur = p.d.Catalog.Version()
	}
	if catalog.ValidVersion(driveVersion) && cur != "" && catalog.CompareVersions(driveVersion, cur) <= 0 {
		return // not newer
	}
	invalid := func(why string) {
		log.Warn("the catalog on the drive was not installed", "code", doctor.CAT002, "reason", why)
		res.Warnings = append(res.Warnings, doctor.CAT002+": the catalog on the drive is not valid, so the catalog this computer has is used")
	}
	if !catalog.ValidVersion(driveVersion) {
		invalid("CATALOG_VERSION is not a version")
		return
	}
	if dry || p.d.Catalog == nil {
		return
	}
	v, err := p.d.Catalog.Install(ctx, dir)
	if err != nil {
		invalid(err.Error())
		return
	}
	res.CatalogInstalled = v
	log.Info("the catalog on the drive was installed", "version", v)
}

func catalogModel(c *catalog.Catalog, id string) (*catalog.Model, bool) {
	if c == nil {
		return nil, false
	}
	return c.Model(id)
}

func catalogContent(c *catalog.Catalog, id string) (*catalog.ContentItem, bool) {
	if c == nil {
		return nil, false
	}
	return c.ContentItem(id)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func appendUnique(list, add []string) []string {
	out := append([]string(nil), list...)
	for _, a := range add {
		found := false
		for _, o := range out {
			if o == a {
				found = true
				break
			}
		}
		if !found {
			out = append(out, a)
		}
	}
	return out
}

func without(list, drop []string) []string {
	var out []string
	for _, s := range list {
		keep := true
		for _, d := range drop {
			if s == d {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}
