package app

import "testing"

// The web source types are registered by the real daemon: the engine half for all three, the planner
// half for url only (the other two are planned from what the catalog pins), over the transport and the
// Hugging Face login of the sync downloader.
func TestWebSourcesAreWiredToTheNode(t *testing.T) {
	sr := newSrcRig(t, "online", true)
	d := sr.start()
	sr.waitStarted()
	c := d.Components()
	for _, typ := range []string{"huggingface", "github_release", "url"} {
		if _, ok := c.SyncItemResolvers.For(typ); !ok {
			t.Errorf("no engine resolver for %s", typ)
		}
	}
	if _, ok := c.SyncResolvers.For("url"); !ok {
		t.Error("no planner resolver for url")
	}
	for _, typ := range []string{"huggingface", "github_release"} {
		if _, ok := c.SyncResolvers.For(typ); ok {
			t.Errorf("%s has a planner resolver: the catalog pins its version and size", typ)
		}
	}
	if d.ark.webSources == nil || d.ark.webSources.web.HTTP == nil || d.ark.webSources.web.Downloader == nil {
		t.Fatalf("the web resolvers are not bound to the node: %+v", d.ark.webSources)
	}
}
