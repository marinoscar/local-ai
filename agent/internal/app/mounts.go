package app

import (
	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// chatRoutes is the /chat/ mount (agent.md section 3.11.1): the llama WebUI with the API key
// injected from /etc/localai/api-key. The key is held by a FileKey that rereads the file when it
// changes (key rotate), and it only ever goes onto the upstream request.
func (d *Daemon) chatRoutes() []api.Route {
	c := d.c
	return portal.ChatRoutes(portal.ChatOptions{
		Port:   c.Config.Get().Ports.Llama,
		Key:    router.NewFileKey(config.OSFS{}, c.Paths.APIKey()).Key,
		Mode:   d.modeName,
		Ops:    router.NewHTTPOps(c.Orchestrator),
		Logger: d.log,
	})
}

// docsRoutes is /docs/ and GET /api/v1/docs/index (agent.md section 3.11.3): the rendered manuals
// of the first content root that has docs/index.json, internal root first. The roots are read per
// request, so an ark drive that is mounted or ejected is noticed without a restart.
func (d *Daemon) docsRoutes() []api.Route {
	internal := d.c.Paths.InternalRoot()
	return api.DocsRoutes(api.DocsOptions{
		Roots: func() []string {
			if d.ark == nil || d.ark.roots == nil {
				return []string{internal}
			}
			var out []string
			for _, r := range d.ark.roots.List() {
				out = append(out, r.Path)
			}
			return out
		},
		Logger: d.log,
	})
}
