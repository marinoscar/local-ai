package app

import (
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
)

// This file wires the web source types of ark sync (ark-sync.md sections 4.9.1, 4.9.4 and 4.9.5):
// huggingface, github_release and url. They are plain downloads (the byte transfer is the
// downloader's), so each registers its engine half; the one planner half is url's, because only a url
// item can follow "latest" or a sidecar, which the catalog cannot pin (a huggingface, github_release or
// pinned url item is planned from what the catalog pins). The resolvers share one resolve.Env with the
// kit producers (the second online lock, the clock, the logger) and one transport with the downloads,
// so a test that points SyncHTTP at a local server points the metadata requests there too.

// arkWebSources are the three resolvers, kept on the stack so a test can reach them.
type arkWebSources struct {
	web *resolve.Web
	hf  *resolve.HuggingFace
	gh  *resolve.GitHubRelease
	url *resolve.URLSource
}

// registerWebSources builds the web resolvers and registers them on the engine's registry (and url on
// the planner's). dl is the sync downloader and httpc its HTTP client.
func (s *arkStack) registerWebSources(dl *download.Downloader, httpc *download.HTTPClient) error {
	d := s.d
	web := &resolve.Web{
		Env:            s.sources.env,
		Downloader:     dl,
		HFToken:        d.hfLogin,
		UserAgent:      download.UserAgent(d.env.Version),
		CatalogVersion: func() string { return d.c.Catalog.Get().Catalog.Version },
	}
	if httpc != nil {
		web.HTTP = &http.Client{Transport: httpc.Transport()}
	}
	w := &arkWebSources{
		web: web,
		hf:  &resolve.HuggingFace{W: web},
		gh:  &resolve.GitHubRelease{W: web},
		url: &resolve.URLSource{W: web},
	}
	if err := resolve.Register(s.resolvers, w.url); err != nil {
		return err
	}
	for _, r := range []struct {
		sourceType string
		resolver   resolve.WebItemResolver
	}{{w.hf.SourceType(), w.hf.Engine()}, {w.gh.SourceType(), w.gh.Engine()}, {w.url.SourceType(), w.url.Engine()}} {
		if err := s.itemResolvers.Register(r.sourceType, r.resolver); err != nil {
			return err
		}
	}
	s.webSources = w
	return nil
}
