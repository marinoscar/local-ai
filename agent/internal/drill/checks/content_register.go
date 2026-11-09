package checks

import (
	"context"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// ContentDeps are the ports of the content and service checks (DRILL-04). A port that is nil makes the
// checks that need it record a skip with reason not_available, so a node that could not build a piece
// (no sync stack, no kiwix) still runs the rest of the drill.
type ContentDeps struct {
	// HTTP reaches the agent's own routes as the internal admin.
	HTTP AgentHTTP

	// Roots, FS and Ready are the mounted content roots, the filesystem their manifests are read
	// through, and the readiness report per root (checks 3, 4 and 16).
	Roots func() []ark.Root
	FS    config.FS
	Ready ReadyReports

	// Verifier is ark-sync's re-verifier (check 4). Leave it nil, not a typed nil pointer, when there
	// is none.
	Verifier     HashVerifier
	ModelEngine  func(itemID string) string
	NodeKit      func(ctx context.Context) []string
	ReverifyDays func() int

	// Books lists the library's books with their tiers (check 8).
	Books func(ctx context.Context) ([]LibraryBook, error)

	// NodeID and CatalogVersion identify this node's survival card and the catalog the manuals must
	// have been rendered for (check 16).
	NodeID         func() string
	CatalogVersion func() string

	// Health is the companion-service supervisor (check 19).
	Health ServiceHealth

	// Scan overrides ark.ScanRoot (tests).
	Scan func(ctx context.Context, fsys config.FS, root ark.Root) ([]ark.ScannedItem, []ark.Problem, error)
}

// ContentChecks returns the checks of DRILL-04 in the order of drill.md section 4.2: 3, 4, 8, 9, 10 (en,
// es), 11 (en, es), 12, 13, 14, 15, 16 and 19. Checks 1 and 2, 5 to 7, 17, 18 and 20 to 22 are other
// issues' and are registered by their own wiring.
func ContentChecks(d ContentDeps) []drill.Check {
	speech := SpeechDeps{HTTP: d.HTTP}
	maps := MapsDeps{HTTP: d.HTTP}
	return []drill.Check{
		NewManifestCheck(ManifestDeps{Roots: d.Roots, FS: d.FS, Scan: d.Scan, Ready: d.Ready}),
		NewHashCheck(HashDeps{
			Verifier: d.Verifier, Roots: d.Roots, Ready: d.Ready, ModelEngine: d.ModelEngine,
			NodeKit: d.NodeKit, ReverifyDays: d.ReverifyDays,
		}),
		NewKiwixCheck(KiwixDeps{HTTP: d.HTTP, Books: d.Books}),
		NewFirstAidCheck(FirstAidDeps{HTTP: d.HTTP}),
		NewSTTCheck(speech, "en"),
		NewSTTCheck(speech, "es"),
		NewTTSCheck(speech, "en"),
		NewTTSCheck(speech, "es"),
		NewRoundtripCheck(speech),
		NewMapTileCheck(maps),
		NewMapSearchCheck(maps),
		NewPortalCheck(PortalDeps{HTTP: d.HTTP}),
		NewDocsCheck(DocsDeps{Roots: d.Roots, FS: d.FS, NodeID: d.NodeID, CatalogVersion: d.CatalogVersion}),
		NewServicesCheck(ServicesDeps{Health: d.Health}),
	}
}

// RegisterContent adds the checks of ContentChecks to reg. It is the one call the daemon wiring makes.
func RegisterContent(reg *drill.Registry, d ContentDeps) error {
	if reg == nil {
		return fmt.Errorf("checks: no registry to register the content checks in")
	}
	for _, c := range ContentChecks(d) {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}
