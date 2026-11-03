package cloud

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// The seams *Link fills for the other packages. A change to one of those interfaces fails the build
// here, next to the code that implements it.
var (
	_ modes.LinkController = (*Link)(nil)
	_ statusapi.CloudView  = (*Link)(nil)
	_ catalog.CloudSource  = (*Link)(nil)
	_ api.CloudLink        = (*Link)(nil)
	_ KeyPusher            = (*Link)(nil)
	_ StatusSource         = (*statusapi.StatusBuilder)(nil)
	_ CatalogSource        = (*statusapi.CatalogStore)(nil)
)

// KeyPusher is what the key routes (AGENT-15) call after a rotation, before the rotate result is
// reported: nil means the cloud now holds the key, an error means it does not (the link retries by
// itself every 5 minutes). *Link implements it.
type KeyPusher interface {
	Push(ctx context.Context) error
}
