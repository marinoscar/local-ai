package jobs

import (
	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// The real types the daemon wiring (AGENT-20) passes. A change to one of the operations an executor
// calls fails the build here, next to the code that calls it.
var (
	_ Router        = (*router.Orchestrator)(nil)
	_ Models        = (*modelsapi.Service)(nil)
	_ Modes         = (*modes.Engine)(nil)
	_ Services      = api.ServicesDeps{}
	_ Catalog       = statusapi.Deps{}
	_ Link          = (*cloud.Link)(nil)
	_ Status        = (*statusapi.StatusBuilder)(nil)
	_ StateView     = (*state.Store)(nil)
	_ Hardware      = (*hardware.Service)(nil)
	_ api.LogTailer = (*services.Journal)(nil)
)
