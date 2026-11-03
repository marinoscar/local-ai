package resolve

import (
	"context"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func gitSource(url string) catalog.ContentSource {
	return catalog.ContentSource{Type: "git_mirror", URL: url}
}

// The three resolvers register under the catalog source types the planner looks them up by, and a
// resolver that cannot answer (offline, a tool missing) leaves the item unresolved: Resolve returns an
// error and no Resolved, which is the planner's rule for an item it cannot size.
func TestRegisterUnderTheCatalogSourceTypes(t *testing.T) {
	env := &Env{Runner: fake.New(), Online: func() bool { return false }, SyncHome: t.TempDir()}
	reg := plan.NewResolvers()
	rs := []NamedResolver{&GitMirror{Env: env}, &AptClosure{Env: env}, &RepoDocs{Env: env, CatalogVersion: func() string { return "2026.11.02-1" }}}
	if err := Register(reg, rs...); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"git_mirror", "apt_closure", "repo_docs"} {
		if _, ok := reg.For(typ); !ok {
			t.Errorf("no resolver registered for %s", typ)
		}
	}
	if err := Register(reg, rs[0]); err == nil {
		t.Error("a second registration of git_mirror was accepted")
	}
	// Offline, the two network resolvers answer with an error; the planner leaves those items unresolved.
	git := contentItem("kit-llama-cpp", "kit", gitSource("https://git.example.test/org/llama.cpp.git"))
	for typ, it := range map[string]plan.CatalogItem{"git_mirror": git, "apt_closure": aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")} {
		res, _ := reg.For(typ)
		if r, err := res.Resolve(context.Background(), it); err == nil || r.Version != "" {
			t.Errorf("%s resolved offline: (%+v, %v)", typ, r, err)
		}
	}
	// repo_docs needs no network to resolve: it follows the catalog.
	res, _ := reg.For("repo_docs")
	if r, err := res.Resolve(context.Background(), docsItem()); err != nil || r.Version != "catalog-2026.11.02-1" {
		t.Errorf("repo_docs: (%+v, %v)", r, err)
	}
}
