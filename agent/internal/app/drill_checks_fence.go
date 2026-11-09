package app

import (
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/ui"
)

// The network fence and the first two drill checks (DRILL-02, drill.md sections 4.2 and 4.4). This
// file is the whole wiring of the slice: build.go calls newDrillFence for Deps.Fence and
// registerFenceChecks for the registry.

// newDrillFence is the drill's fence: the cloud link stands still for the run, and the agent's shared
// outbound transport (internal/netx) refuses every non-loopback dial and counts it.
func newDrillFence(link drill.LinkPauser) drill.Fence {
	return &drill.PausingFence{Link: link, Inner: &checks.NetFence{}}
}

// registerFenceChecks adds check 1 (drl.fence) and check 2 (drl.config.offline_lint) to the registry.
// Check 2 lints the generated files at their canonical paths under the filesystem root, and the Portal
// and maps bundle embedded in this binary.
func registerFenceChecks(reg *drill.Registry, p Paths) error {
	lint := checks.OfflineLint{
		ModelsINI:  p.Join(router.UnitPresetFile),
		LibraryXML: p.Join(kiwix.DefaultStateDir + "/" + kiwix.LibraryFile),
		WhisperEnv: p.Join(whisper.EnvPath),
		Bundle:     ui.Dist(),
	}
	for _, c := range []drill.Check{checks.Fence(checks.FenceConfig{}), lint.Check()} {
		if err := reg.Add(c); err != nil {
			return fmt.Errorf("drill check %s: %w", c.ID, err)
		}
	}
	return nil
}
