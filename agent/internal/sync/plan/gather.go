package plan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The gatherer is the only part of the planner that does I/O, and every piece of it sits behind a
// small interface or function (the catalog, node.yaml, the mounted roots, the best-copy index, the
// journal, statfs, the filesystem, the network state, the clock, the resolvers), so a test drives it
// with fakes and the planner itself never learns where its input came from.

// CodeUnknownProfile: the root's profile is not in the catalog (LAI-CAT-003).
const CodeUnknownProfile = doctor.CAT003

// Default bounds of the dry run's metadata lookups.
const (
	// DefaultResolveTimeout bounds one resolver call.
	DefaultResolveTimeout = 5 * time.Second
	// resolveWorkers bounds the resolver calls in flight, so a profile of thirty items does not
	// open thirty connections.
	resolveWorkers = 8
	// maxTrashDepth bounds the walk that sizes a trashed version.
	maxTrashDepth = 6
)

// ErrUnknownRoot is returned for a root id that is not a mounted content root.
type ErrUnknownRoot struct{ Root string }

func (e *ErrUnknownRoot) Error() string { return "plan: not a mounted content root: " + e.Root }

// ErrUnknownProfile is returned when the profile a root is planned with is not in the catalog
// (LAI-CAT-003): there is nothing to plan.
type ErrUnknownProfile struct{ Profile, Root string }

func (e *ErrUnknownProfile) Error() string {
	return fmt.Sprintf("%s: profile %q of root %s is not in the catalog", CodeUnknownProfile, e.Profile, e.Root)
}

// Code is the doctor code of the error.
func (e *ErrUnknownProfile) Code() string { return CodeUnknownProfile }

// NodeSettings is what the planner reads from node.yaml: the internal root's profile and overrides.
type NodeSettings struct {
	Profile   string
	Overrides catalog.Overrides
}

// Network is the node's network state: its mode and whether the internet is reachable (connectivity
// is not "none"). The planner may use the network only in online mode with the internet reachable.
type Network struct {
	Mode   string
	Online bool
}

// MayUse says whether a run may use the network in this state: the internet is reachable and the mode
// allows it. Survival never does; the offline mode does only when the person asked for it
// (allow_offline_mode); an unknown (empty) mode is treated as online. This is the one rule the dry run
// and the run engine's preflight both follow, so the two plans agree.
func (n Network) MayUse(allowOfflineMode bool) bool {
	if !n.Online {
		return false
	}
	switch n.Mode {
	case ModeSurvival:
		return false
	case ModeOffline:
		return allowOfflineMode
	}
	return true
}

// CopyIndex is what the gatherer reads from the best-copy index (*ark.Index implements it).
type CopyIndex interface {
	ItemIDs() []string
	Copies(itemID string) []ark.Copy
}

// JournalItems is what the gatherer reads from the journal (*journal.Items implements it).
type JournalItems interface {
	List() []journal.Item
}

// StatfsFunc returns the size of the filesystem that holds path and the bytes a non-root process may
// still use. OSStatfs is the real one.
type StatfsFunc func(path string) (total, free int64, err error)

// Resolver turns a catalog item into its target version and size, reading metadata only (HEAD, a
// directory listing, a release record), never a body. SYNC-09 to SYNC-11 implement one per source
// type and register it; their richer answer is reduced to Resolved by a small adapter there.
type Resolver interface {
	Resolve(ctx context.Context, it CatalogItem) (Resolved, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(ctx context.Context, it CatalogItem) (Resolved, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ctx context.Context, it CatalogItem) (Resolved, error) {
	return f(ctx, it)
}

// LocalResolver is the optional second half of a Resolver for a source type whose producer can work
// with no network from material already on a mounted root (repo_docs from a local-ai.git mirror).
// When the node may not use the network, the gatherer asks it instead of falling back to what the
// catalog pins, and a "yes" makes the item plannable: the planner builds it here (source local_build)
// instead of skipping it as unresolved.
//
// ResolveLocal reads local files only: it never dials and never runs a tool. rootPaths are the paths
// of every mounted content root. ok is false when the item needs the network or the material is not
// there.
type LocalResolver interface {
	ResolveLocal(ctx context.Context, it CatalogItem, rootPaths []string) (Resolved, bool)
}

// Resolvers is the registry of resolvers by catalog source type ("git_mirror", "apt_closure",
// "repo_docs"; the agent's wiring registers those). An item with no resolver is planned from what the
// catalog pins, and an item the catalog cannot pin is unresolved. Safe for concurrent use.
type Resolvers struct {
	mu sync.RWMutex
	m  map[string]Resolver
}

// NewResolvers returns an empty registry.
func NewResolvers() *Resolvers { return &Resolvers{m: map[string]Resolver{}} }

// Register adds the resolver of a source type. A second registration of the same type is an error:
// two resolvers for one type would make the plan depend on init order.
func (r *Resolvers) Register(sourceType string, res Resolver) error {
	if sourceType == "" || res == nil {
		return errors.New("plan: a resolver needs a source type and an implementation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[sourceType]; dup {
		return fmt.Errorf("plan: a resolver for source type %q is already registered", sourceType)
	}
	r.m[sourceType] = res
	return nil
}

// For returns the resolver of a source type.
func (r *Resolvers) For(sourceType string) (Resolver, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.m[sourceType]
	return res, ok
}

// Deps are the gatherer's inputs. Everything but Resolvers, Logger and Now is required.
type Deps struct {
	// FS reads the staging and trash directories of a root.
	FS config.FS
	// Catalog returns the catalog in use.
	Catalog func() *catalog.Catalog
	// Settings returns node.yaml's profile and overrides.
	Settings func() NodeSettings
	// Roots returns the mounted content roots.
	Roots func() []ark.Root
	// Copies is the best-copy index.
	Copies CopyIndex
	// Journal is the journal's item records.
	Journal JournalItems
	// Statfs measures a root's filesystem.
	Statfs StatfsFunc
	// Node returns the machine's facts (backend, memory, codename, arch).
	Node func() NodeInfo
	// Network returns the node's mode and reachability.
	Network func() Network
	// Resolvers is the optional registry of resolvers.
	Resolvers *Resolvers
	// ResolveTimeout bounds one resolver call when GatherOptions.ResolveTimeout is zero;
	// DefaultResolveTimeout when this is zero too.
	ResolveTimeout time.Duration
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// Gatherer builds a PlanInput per root.
type Gatherer struct{ d Deps }

// NewGatherer checks the dependencies and returns a gatherer.
func NewGatherer(d Deps) (*Gatherer, error) {
	var missing []string
	for name, ok := range map[string]bool{
		"FS": d.FS != nil, "Catalog": d.Catalog != nil, "Settings": d.Settings != nil, "Roots": d.Roots != nil,
		"Copies": d.Copies != nil, "Journal": d.Journal != nil, "Statfs": d.Statfs != nil, "Node": d.Node != nil, "Network": d.Network != nil,
	} {
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("plan: the gatherer needs %s", strings.Join(missing, ", "))
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Gatherer{d: d}, nil
}

// Roots returns the mounted content roots, internal first, then drives in mount order.
func (g *Gatherer) Roots() []ark.Root {
	roots := append([]ark.Root(nil), g.d.Roots()...)
	sort.SliceStable(roots, func(i, j int) bool {
		a, b := roots[i], roots[j]
		if (a.Kind == ark.KindInternal) != (b.Kind == ark.KindInternal) {
			return a.Kind == ark.KindInternal
		}
		if a.MountID != b.MountID {
			return a.MountID < b.MountID
		}
		return a.ID < b.ID
	})
	return roots
}

// GatherOptions tune one gather.
type GatherOptions struct {
	// Resolve asks the resolvers for the target version and size (network metadata). Without it, or
	// offline, items are planned from what the catalog pins; offline (with Resolve set), an item whose
	// producer can work from material on a root (LocalResolver) is planned from that material.
	Resolve bool
	// Online, when set, replaces the gatherer's own reading of the network (Deps.Network) for this
	// gather: the run's real connectivity and allow_offline_mode (Network.MayUse). True means the
	// network may be used: resolvers run, the plan is not offline and its mode is online (an
	// allow_offline_mode run in offline mode is planned as online). False means no network, whatever
	// the mode says. Nil: Deps.Network decides, as before.
	Online *bool
	// ResolveTimeout bounds one resolver call; DefaultResolveTimeout when zero.
	ResolveTimeout time.Duration
}

// GatherAll gathers the inputs of several roots, in the order given.
func (g *Gatherer) GatherAll(ctx context.Context, rootIDs []string, opt GatherOptions) ([]PlanInput, error) {
	out := make([]PlanInput, 0, len(rootIDs))
	for _, id := range rootIDs {
		in, err := g.Gather(ctx, id, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

// Gather builds the PlanInput of one root: the catalog, the root's profile and overrides, the facts
// of statfs, the verified copies on this root and on the others, the journal's partial downloads, the
// trash, and, when asked and online, the resolvers' answers. Errors: *ErrUnknownRoot,
// *ErrUnknownProfile, a statfs failure, or the context's.
func (g *Gatherer) Gather(ctx context.Context, rootID string, opt GatherOptions) (PlanInput, error) {
	var root ark.Root
	found := false
	for _, r := range g.d.Roots() {
		if r.ID == rootID {
			root, found = r, true
			break
		}
	}
	if !found {
		return PlanInput{}, &ErrUnknownRoot{Root: rootID}
	}
	cat := g.d.Catalog()
	if cat == nil {
		cat = catalog.Empty()
	}
	settings := g.d.Settings()
	in := PlanInput{Catalog: cat, Node: g.d.Node(), Now: g.d.Now()}

	// The internal root follows node.yaml; a drive follows its own ROOT.json and takes no overrides.
	profileID := root.Profile
	if root.Kind == ark.KindInternal {
		profileID = settings.Profile
		ov := settings.Overrides
		in.Overrides = &ov
	}
	if profileID == "" && root.Kind == ark.KindArkDrive {
		profileID = ark.DefaultDriveProfile
	}
	prof, ok := cat.Profile(profileID)
	if !ok {
		return PlanInput{}, &ErrUnknownProfile{Profile: profileID, Root: rootID}
	}
	in.Profile = prof

	total, free, err := g.d.Statfs(root.Path)
	if err != nil {
		return PlanInput{}, fmt.Errorf("plan: statfs %s: %w", root.Path, err)
	}
	in.Root = RootInfo{ID: root.ID, Kind: root.Kind, Path: root.Path, Profile: profileID, TotalBytes: total, FreeBytes: free,
		ReadOnly: root.ReadOnly || root.State == ark.StateReadOnly || root.State == ark.StateUnclean}

	net := g.d.Network()
	in.Mode = net.Mode
	in.Offline = !net.Online || (net.Mode != "" && net.Mode != ModeOnline)
	if opt.Online != nil {
		in.Offline = !*opt.Online
		if *opt.Online {
			in.Mode = ModeOnline // the planner reads any other mode as "no network"
		}
	}

	g.copies(&in, root.ID)
	journalItems := g.journalItems(root.ID)
	g.partial(&in, root, journalItems)
	g.trash(&in, root, journalItems)

	if err := ctx.Err(); err != nil {
		return PlanInput{}, err
	}
	g.resolve(ctx, &in, root, opt)
	return in, ctx.Err()
}

// copies fills Present and OtherRoots from the best-copy index.
func (g *Gatherer) copies(in *PlanInput, rootID string) {
	in.Present = map[string]manifest.Manifest{}
	in.OtherRoots = map[string][]RootCopy{}
	for _, id := range g.d.Copies.ItemIDs() {
		for _, c := range g.d.Copies.Copies(id) {
			if c.RootID == rootID {
				if _, dup := in.Present[id]; !dup {
					in.Present[id] = c.Manifest
				}
				continue
			}
			in.OtherRoots[id] = append(in.OtherRoots[id], RootCopy{RootID: c.RootID, Manifest: c.Manifest})
		}
	}
}

func (g *Gatherer) journalItems(rootID string) map[string]journal.Item {
	out := map[string]journal.Item{}
	for _, it := range g.d.Journal.List() {
		if it.RootID == rootID {
			out[it.ItemID] = it
		}
	}
	return out
}

// partial fills Partial from the journal records of unfinished downloads and the staged files that
// wait in .staging/<item>/<version>/. The journal's PartialBytes is the .part total; completed files
// are read from the directory (anything that does not end in .part).
func (g *Gatherer) partial(in *PlanInput, root ark.Root, items map[string]journal.Item) {
	in.Partial = map[string]PartState{}
	for id, it := range items {
		switch it.State {
		case journal.StateDownloading, journal.StateVerifying, journal.StateStaged, journal.StateSwapping, journal.StateFailed:
		default:
			continue
		}
		if it.Version == "" || !manifest.SafeName(id) || !manifest.SafeName(it.Version) {
			continue
		}
		staged, _ := g.dirBytes(filepath.Join(root.Path, ".staging", id, it.Version), func(name string) bool { return !strings.HasSuffix(name, ".part") }, 0)
		if it.PartialBytes > 0 || staged > 0 {
			in.Partial[id] = PartState{Version: it.Version, PartBytes: it.PartialBytes, StagedBytes: staged}
		}
	}
}

// trash fills Trash from .trash/<item>/<version>/. Eligible means retention may purge it to make
// room (ark-sync.md section 4.10 R3): the live copy of the item is verified on this root, and the
// journal does not say the item is mid-swap. Whether R1 allows the deletion at that moment is
// retention's call when the engine runs; the plan only counts the space.
func (g *Gatherer) trash(in *PlanInput, root ark.Root, items map[string]journal.Item) {
	dir := filepath.Join(root.Path, ".trash")
	ids, err := g.d.FS.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			g.d.Logger.Warn("plan: cannot read the trash", "root", root.ID, "err", err)
		}
		return
	}
	for _, idEnt := range ids {
		id := idEnt.Name()
		if !idEnt.IsDir() || !manifest.SafeName(id) {
			continue
		}
		versions, err := g.d.FS.ReadDir(filepath.Join(dir, id))
		if err != nil {
			continue
		}
		_, live := in.Present[id]
		if j, ok := items[id]; ok && j.State != journal.StateVerified && j.State != journal.StateSkipped && j.State != journal.StateFailed {
			live = false
		}
		for _, vEnt := range versions {
			if !vEnt.IsDir() || !manifest.SafeName(vEnt.Name()) {
				continue
			}
			vdir := filepath.Join(dir, id, vEnt.Name())
			e := TrashEntry{ItemID: id, Version: vEnt.Name(), Eligible: live}
			if info, err := vEnt.Info(); err == nil {
				e.TrashedAt = info.ModTime()
			}
			if m, err := manifest.Read(g.d.FS, manifest.Path(vdir)); err == nil && m.TotalBytes > 0 {
				e.Bytes = m.TotalBytes
			} else {
				e.Bytes, _ = g.dirBytes(vdir, nil, 0)
			}
			in.Trash = append(in.Trash, e)
		}
	}
	sort.Slice(in.Trash, func(i, j int) bool {
		a, b := in.Trash[i], in.Trash[j]
		if a.ItemID != b.ItemID {
			return a.ItemID < b.ItemID
		}
		return a.Version < b.Version
	})
}

// dirBytes sums the sizes of the regular files below dir whose base name keep accepts (all when keep
// is nil), a bounded walk. A directory that cannot be read counts as empty.
func (g *Gatherer) dirBytes(dir string, keep func(name string) bool, depth int) (int64, error) {
	if depth > maxTrashDepth {
		return 0, nil
	}
	ents, err := g.d.FS.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range ents {
		switch {
		case e.IsDir():
			sub, _ := g.dirBytes(filepath.Join(dir, e.Name()), keep, depth+1)
			n += sub
		case e.Type().IsRegular() && (keep == nil || keep(e.Name())):
			if info, err := e.Info(); err == nil {
				n += info.Size()
			}
		}
	}
	return n, nil
}

// resolve fills Resolved for the desired items. An item a registered resolver answers takes the
// answer; one the resolver fails on is left unresolved (the plan then keeps what is present and
// skips what is not); an item with no resolver, or any item when resolving is off or the node is
// offline, takes what the catalog pins (FromCatalog), and stays unresolved when it pins nothing. The
// one exception offline is an item whose resolver is a LocalResolver and says yes: it takes that
// answer and is marked in LocalBuild.
func (g *Gatherer) resolve(ctx context.Context, in *PlanInput, root ark.Root, opt GatherOptions) {
	in.Resolved = map[string]Resolved{}
	d := DesiredItems(in.Catalog, in.Profile, in.Overrides, in.Root, in.Node)

	type job struct {
		it  CatalogItem
		res Resolver
	}
	var jobs []job
	var rootPaths []string
	for _, it := range d.Items {
		res, hasRes := g.d.Resolvers.For(it.SourceType())
		if hasRes && opt.Resolve && !in.Offline {
			jobs = append(jobs, job{it, res})
			continue
		}
		if hasRes && opt.Resolve && in.Offline {
			// No network: an item whose producer can work from material on a root is still planned.
			if lr, ok := res.(LocalResolver); ok {
				if rootPaths == nil {
					for _, r := range g.d.Roots() {
						rootPaths = append(rootPaths, r.Path)
					}
				}
				if r, ok := lr.ResolveLocal(ctx, it, rootPaths); ok && r.Version != "" {
					in.Resolved[it.ID] = r
					if in.LocalBuild == nil {
						in.LocalBuild = map[string]bool{}
					}
					in.LocalBuild[it.ID] = true
					continue
				}
			}
		}
		if r, ok := FromCatalog(it, in.Catalog.Version); ok {
			in.Resolved[it.ID] = r
		}
	}
	if len(jobs) == 0 {
		return
	}
	timeout := opt.ResolveTimeout
	if timeout <= 0 {
		timeout = g.d.ResolveTimeout
	}
	if timeout <= 0 {
		timeout = DefaultResolveTimeout
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, resolveWorkers)
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			r, err := j.res.Resolve(cctx, j.it)
			if err != nil || r.Version == "" {
				g.d.Logger.Debug("plan: item left unresolved", "root", root.ID, "item", j.it.ID, "err", err)
				return
			}
			mu.Lock()
			in.Resolved[j.it.ID] = r
			mu.Unlock()
		}(j)
	}
	wg.Wait()
}
