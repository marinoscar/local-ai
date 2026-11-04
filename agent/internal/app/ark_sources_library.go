package app

import (
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/maps/places"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
)

// This file wires the library and maps source types of ark sync (ark-sync.md sections 4.9.2 and
// 4.9.3): kiwix (the ZIM books) and pmtiles_extract (regional extracts of the Protomaps planet). Each is
// registered twice, as the planner's resolver (plan.Resolvers) and as the engine's resolver
// (run.ItemResolvers), over the resolve.Env of registerSources, and their content checks (the ZIM's
// internal MD5, pmtiles verify and the header) are registered on the item machine's validators.

// pmtilesToolPath is where the installer puts the pmtiles binary (rebuild-kit.md "Contents").
const pmtilesToolPath = "/opt/localai/pmtiles/current/pmtiles"

// registerLibrarySources registers kiwix and pmtiles_extract. It runs after registerSources, whose
// resolve.Env it shares (s.sources.env), and registers the validators on vals, the registry the item
// machine was built with. The resolvers are reachable through the registries (the tests assert their
// types).
func (s *arkStack) registerLibrarySources(vals *item.Validators) error {
	d := s.d
	env := s.sources.env

	// The metadata requests (a listing, a sidecar, a HEAD) use the sync downloader's own HTTP client:
	// the same transport, TLS rules and redirect rule, and the one a test points at its fake network.
	httpc := d.env.SyncHTTP
	if httpc == nil {
		httpc = download.NewHTTPClient(download.ClientConfig{Version: d.env.Version})
	}
	kx := &resolve.Kiwix{Env: env, HTTP: httpc.Client(), Version: d.env.Version}

	// pmtiles runs as the service user when there is one (an unprivileged development run has no
	// Credential and runs it as the agent's own user).
	pm := &resolve.PMTiles{Env: env, HTTP: httpc.Client(), Version: d.env.Version, Tool: d.c.Paths.Join(pmtilesToolPath)}
	if d.haveLocalAI && !d.env.Unprivileged {
		pm.Credential = &execx.Credential{UID: uint32(d.localaiUID), GID: uint32(d.localaiGID)}
	}

	if err := resolve.Register(s.resolvers, kx, pm); err != nil {
		return err
	}
	if err := s.itemResolvers.Register("kiwix", run.ItemResolverFunc(kx.ResolveItem)); err != nil {
		return err
	}
	if err := s.itemResolvers.Register("pmtiles_extract", run.ItemResolverFunc(pm.ResolveItem)); err != nil {
		return err
	}
	if err := resolve.RegisterZIMValidator(vals); err != nil {
		return err
	}
	if err := pm.RegisterValidator(vals); err != nil {
		return err
	}
	// The GeoNames dumps have no pinned hash: their content is checked instead, against the rows of the
	// version in use (companion-services.md section 4.4.4).
	return places.RegisterValidator(vals, func(string) int {
		if d.places == nil {
			return 0
		}
		return d.places.Rows()
	})
}
