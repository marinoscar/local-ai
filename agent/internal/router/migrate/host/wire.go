package host

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ---- users and the clock --------------------------------------------------------------------------

// Users is migrate.Users over os/user.
type Users struct{}

// LookupUser implements migrate.Users.
func (Users) LookupUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	return uid, gid, err
}

// LookupGroup implements migrate.Users.
func (Users) LookupGroup(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// Clock is migrate.Clock on the wall clock.
type Clock struct{}

// Now implements migrate.Clock.
func (Clock) Now() time.Time { return time.Now() }

// Sleep implements migrate.Clock.
func (Clock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- the agent's own unit -------------------------------------------------------------------------

// Agent is migrate.AgentUnit over the systemctl Manager, without the allowlist of the daemon: this runs
// in the aiagent migrate process of a root shell, where stopping the agent is exactly the single-writer
// rule of llama-router.md section 4.9.
type Agent struct{ M services.Manager }

// Active implements migrate.AgentUnit.
func (a Agent) Active(ctx context.Context) (bool, error) {
	st, err := a.M.State(ctx, services.UnitAgent)
	if err != nil {
		return false, err
	}
	return st.ActiveState == services.ActiveActive || st.ActiveState == services.ActiveActivating || st.ActiveState == services.ActiveReloading, nil
}

// Stop implements migrate.AgentUnit.
func (a Agent) Stop(ctx context.Context) error { return a.M.Stop(ctx, services.UnitAgent) }

// Start implements migrate.AgentUnit.
func (a Agent) Start(ctx context.Context) error { return a.M.Start(ctx, services.UnitAgent) }

// ---- the build manager ----------------------------------------------------------------------------

// Builder is migrate.Builder over the build manager. The build never activates (it never restarts
// localai-llama while the old service holds the port): the migration switches current itself.
type Builder struct {
	M        *build.Manager
	KitRoots func() []string
}

// codedError carries a doctor code through the migrate.Builder interface.
type codedError struct {
	code string
	err  error
}

func (c codedError) Error() string { return c.err.Error() }
func (c codedError) Unwrap() error { return c.err }
func (c codedError) Code() string  { return c.code }

func fromBuildError(err *build.Error) error {
	if err == nil {
		return nil
	}
	return codedError{code: err.Code, err: err}
}

// CanBuild implements migrate.Builder.
func (b Builder) CanBuild(_ context.Context, backend string) error {
	_, err := b.M.Preflight(backend)
	return fromBuildError(err)
}

// Build implements migrate.Builder.
func (b Builder) Build(ctx context.Context, req migrate.BuildRequest) (migrate.BuildResult, error) {
	var roots []string
	if b.KitRoots != nil {
		roots = b.KitRoots()
	}
	// The legacy checkout first, then the kit mirror; never the network.
	sources := append([]build.Source{build.GitArchiveSource{Repo: req.SourceRepo, Label: "legacy"}}, build.KitSources(kit.SourceStemLlama, roots)...)
	res, err := b.M.Build(ctx, build.Request{
		Commit: req.Commit, Backend: req.Backend, Sources: sources, Offline: true, NoActivate: true,
		Progress: func(p build.Progress) {
			if req.Progress != nil {
				req.Progress(p.Message)
			}
		},
	})
	if err != nil {
		var be *build.Error
		if errors.As(err, &be) {
			return migrate.BuildResult{}, fromBuildError(be)
		}
		return migrate.BuildResult{}, err
	}
	return migrate.BuildResult{BuildID: res.BuildID}, nil
}

// ---- node.yaml ------------------------------------------------------------------------------------

// Config is migrate.NodeConfig over the config store: node.yaml is edited in place, comments kept.
type Config struct{ Store *config.Store }

// Get implements migrate.NodeConfig.
func (c Config) Get() config.Config { return c.Store.Get() }

// Record implements migrate.NodeConfig. An alias is added to the ones already there; an environment
// variable that node.yaml already sets keeps its value (the operator's setting wins).
func (c Config) Record(_ context.Context, ch migrate.ConfigChange) error {
	cur := c.Store.Get().Llama
	return c.Store.Update(func(root *yaml.Node) error {
		for id, aliases := range ch.LegacyAliases {
			merged := append([]string{}, cur.LegacyAliases[id]...)
			for _, a := range aliases {
				if !has(merged, a) {
					merged = append(merged, a)
				}
			}
			if err := config.SetKey(root, merged, "llama", "legacy_aliases", id); err != nil {
				return err
			}
		}
		for k, v := range ch.ExtraEnv {
			if _, set := cur.ExtraEnv[k]; set {
				continue
			}
			if err := config.SetKey(root, v, "llama", "extra_env", k); err != nil {
				return err
			}
		}
		if ch.Commit != "" {
			return config.SetKey(root, ch.Commit, "llama", "commit")
		}
		return nil
	})
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- state.json -----------------------------------------------------------------------------------

// State is migrate.State over the state store. The agent is stopped while the migration runs, so the
// migration is its only writer.
type State struct{ Store *state.Store }

type syncMutator struct{ s *state.Store }

func (m syncMutator) Mutate(fn func(*state.State) error) error { return m.s.MutateSync(fn) }

// Migration implements migrate.State.
func (s State) Migration() (state.MigrationEntry, bool) {
	e, ok := s.Store.Snapshot().Migration[migrate.StateKey]
	return e, ok
}

// SetMigration implements migrate.State.
func (s State) SetMigration(e state.MigrationEntry) error {
	return s.Store.MutateSync(func(st *state.State) error {
		if st.Migration == nil {
			st.Migration = map[string]state.MigrationEntry{}
		}
		old := st.Migration[migrate.StateKey]
		e.Extra = old.Extra
		st.Migration[migrate.StateKey] = e
		return nil
	})
}

// SetDefaultModel implements migrate.State.
func (s State) SetDefaultModel(id string) error {
	return s.Store.MutateSync(func(st *state.State) error {
		st.Models.Default = &id
		return nil
	})
}

// RecordBuild implements migrate.State.
func (s State) RecordBuild(buildID string) error {
	return build.StoreRecorder{Store: syncMutator{s.Store}}.RecordBuild(buildID)
}

// Connectivity implements migrate.State.
func (s State) Connectivity() string { return s.Store.Snapshot().Connectivity.Value }

// ---- models.ini and the unit ----------------------------------------------------------------------

// Renderer is migrate.Renderer over the preset generator and the unit generator of internal/router. It
// assembles their inputs from node.yaml, the catalog, the hardware and the state exactly as the daemon
// does (the daemon is stopped), runs both and starts nothing.
type Renderer struct {
	Paths    local.Paths
	Config   *config.Store
	State    *state.Store
	Systemd  services.Manager
	Version  string
	LocalAI  *router.Identity
	Hardware func(context.Context) hardware.Snapshot

	once sync.Once
	gen  *router.Generator
	ugen *router.UnitGenerator
	err  error
}

func (r *Renderer) build() {
	ini, drop, unit, cache := router.Ownership{UID: -1, GID: -1}, router.Ownership{UID: -1, GID: -1}, router.Ownership{UID: -1, GID: -1}, router.Ownership{UID: -1, GID: -1}
	if r.LocalAI != nil {
		ini = router.Ownership{UID: 0, GID: r.LocalAI.GID}
		drop, unit = router.Ownership{UID: 0, GID: 0}, router.Ownership{UID: 0, GID: 0}
		cache = router.Ownership{UID: r.LocalAI.UID, GID: r.LocalAI.GID}
	}
	r.gen, r.err = router.NewGenerator(router.GeneratorOptions{
		Paths: router.DefaultPaths(""), IniState: router.StateIni{Store: r.State}, Systemd: r.Systemd,
		LocalAI: r.LocalAI, INIOwner: ini, DropInOwner: drop, Version: r.Version,
	})
	if r.err != nil {
		return
	}
	r.ugen, r.err = router.NewUnitGenerator(router.UnitGeneratorOptions{
		Paths: router.DefaultUnitPaths(""), State: router.StateUnit{Store: r.State}, Systemd: r.Systemd,
		LocalAI: r.LocalAI, UnitOwner: unit, CacheOwner: cache,
	})
}

// Render implements migrate.Renderer.
func (r *Renderer) Render(ctx context.Context) (migrate.RenderResult, error) {
	r.once.Do(r.build)
	if r.err != nil {
		return migrate.RenderResult{}, r.err
	}
	cat, err := catalog.Load(r.Paths.CatalogDir())
	if err != nil {
		return migrate.RenderResult{}, fmt.Errorf("the catalog cannot be loaded: %w", err)
	}
	cfg := r.Config.Get()
	prof, _ := cat.Profile(cfg.Profile)
	var rcfg catalog.Router
	if prof != nil {
		rcfg = prof.Router
	}
	def := ""
	if d := r.State.Snapshot().Models.Default; d != nil {
		def = *d
	}
	snap := r.Hardware(ctx)
	in := router.Inputs{
		NodeID: cfg.Node.ID, Catalog: cat, Profile: prof, Overrides: cfg.ProfileOverrides, Llama: cfg.Llama,
		Roots: router.Roots{Internal: r.Paths.InternalRoot}, Fit: router.NewEnv(snap, cfg.Llama.Backend, rcfg, 0),
		StateDefault: def,
	}
	ures, uerr := r.ugen.Generate(ctx, router.UnitInputs{Mode: router.LlamaModeRouter, Port: cfg.Ports.Llama, Router: rcfg})
	if uerr != nil {
		return migrate.RenderResult{}, fmt.Errorf("localai-llama.service: %w", uerr)
	}
	gres, gerr := r.gen.Generate(ctx, in)
	if gerr != nil {
		return migrate.RenderResult{}, fmt.Errorf("models.ini: %w", gerr)
	}
	return migrate.RenderResult{INIPath: gres.INIPath, UnitPath: ures.Path}, nil
}

// ---- assembly -------------------------------------------------------------------------------------

// Machine is the migration's view of the real node.
type Machine struct {
	Options migrate.Options
	// Close releases the state store.
	Close func()
}

// Assemble builds the Options of migrate.New from the real machine. flags are the command's options.
func Assemble(ctx context.Context, env local.Env, version string, accept, restore bool) (*Machine, error) {
	run := execx.Exec{}
	sc := services.NewSystemctl(run)
	guarded := services.NewGuard(sc)
	fsys := OSFS{}

	cfg := config.Open(config.Options{Path: env.Paths.NodeYAML(), Hooks: config.Hooks{CatalogModelIDs: func() []string { return catalogIDs(env) }}})
	st, err := state.Open(env.Paths.StateFile(), state.Options{AgentVersion: version, Owner: stateOwner(env)})
	if err != nil {
		return nil, err
	}
	cat, _ := catalog.Load(env.Paths.CatalogDir())
	ident := localAIIdentity()
	hw := hardware.New(hardware.Options{})
	roots := func() []string {
		var out []string
		for _, r := range local.FindRoots(context.Background(), env) {
			out = append(out, r.Path)
		}
		return out
	}
	llamaCfg := func() config.LlamaBlock { return cfg.Get().Llama }
	mgr := build.New(build.Options{
		Root: build.DefaultRoot, BuiltBy: "aiagent " + version, Node: llamaCfg, Exec: run, Lookup: execx.Lookup,
		KitRoots: roots,
	})
	paths := migrate.DefaultPaths("")
	paths.NodeYAML = env.Paths.NodeYAML()
	paths.InternalRoot = env.Paths.InternalRoot
	return &Machine{
		Close: func() { _ = st.Close() },
		Options: migrate.Options{
			Paths: paths, FS: fsys, Exec: run, Lookup: execx.Lookup, Systemd: guarded, Agent: Agent{M: sc}, Users: Users{},
			Probe:   &Probe{Port: config.DefaultLlamaPort, KeyFiles: []string{paths.NewKey, paths.LegacyKey}},
			Builder: Builder{M: mgr, KitRoots: roots}, Config: Config{Store: cfg}, State: State{Store: st},
			Renderer: &Renderer{
				Paths: env.Paths, Config: cfg, State: st, Systemd: guarded, Version: version, LocalAI: ident,
				Hardware: func(ctx context.Context) hardware.Snapshot { return hw.Refresh(ctx) },
			},
			Catalog: cat, Audit: env.Audit, Clock: Clock{}, AcceptCatalogFlags: accept, RestoreFiles: restore,
			RootUID: env.RootUID, RootGID: env.RootGID, Version: version,
		},
	}, nil
}

func catalogIDs(env local.Env) []string {
	cat, err := catalog.Load(env.Paths.CatalogDir())
	if err != nil || cat == nil {
		return nil
	}
	ids := make([]string, 0, len(cat.Models))
	for _, m := range cat.Models {
		ids = append(ids, m.ID)
	}
	return ids
}

func stateOwner(env local.Env) *state.Owner {
	if env.LookupGroup == nil {
		return nil
	}
	gid, err := env.LookupGroup(local.ServiceGroup)
	if err != nil {
		return nil
	}
	return &state.Owner{UID: env.RootUID, GID: gid}
}

func localAIIdentity() *router.Identity {
	uid, gid, err := Users{}.LookupUser(migrate.ServiceUser)
	if err != nil {
		return nil
	}
	if g, err := (Users{}).LookupGroup(migrate.ServiceUser); err == nil {
		gid = g
	}
	return &router.Identity{UID: uid, GID: gid}
}
