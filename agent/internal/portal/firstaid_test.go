package portal_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/portal"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// fakeKiwix answers HEAD for the paths in `have` with 200 and everything else with 404.
type fakeKiwix struct {
	mu    sync.Mutex
	have  map[string]bool
	down  bool
	calls atomic.Int64
}

func (f *fakeKiwix) Exists(_ context.Context, path string) (bool, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return false, http.ErrHandlerTimeout
	}
	return f.have[path], nil
}

type fakeAlertSink struct {
	mu      sync.Mutex
	raised  []map[string]any
	cleared int
}

func (f *fakeAlertSink) Raise(code string, d map[string]any, _ ...doctor.RaiseOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code != doctor.SVC109 {
		panic("unexpected code " + code)
	}
	f.raised = append(f.raised, d)
	return nil
}

func (f *fakeAlertSink) Clear(string) {
	f.mu.Lock()
	f.cleared++
	f.mu.Unlock()
}

func (f *fakeAlertSink) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.raised) == 0 {
		return nil
	}
	return f.raised[len(f.raised)-1]
}

func faCatalog(links ...catalog.PortalLink) *catalog.Catalog {
	c := catalog.Empty()
	c.Content = []catalog.ContentItem{
		{ID: "wikimed-en", Kind: "kiwix", PortalLinks: links},
		{ID: "wikimed-es", Kind: "kiwix"},
		{ID: "survival-en", Kind: "kiwix"},
	}
	return c
}

func link(key string, order int, article, esItem, esArticle string) catalog.PortalLink {
	return catalog.PortalLink{Key: key, Order: order, Article: article, ESItem: esItem, ESArticle: esArticle,
		Title: catalog.PortalTitle{EN: "EN " + key, ES: "ES " + key}}
}

func kiwixRec(books ...health.Book) health.Record {
	return health.Record{Name: "kiwix", State: "ok", Books: books}
}

var (
	bookEN = health.Book{ContentID: "wikimed-en", Name: "mdwiki_en_all", Root: "/r1"}
	bookES = health.Book{ContentID: "wikimed-es", Name: "mdwiki_es_all", Root: "/r1"}
)

type faRig struct {
	r   *portal.FirstAidResolver
	k   *fakeKiwix
	al  *fakeAlertSink
	cat *catalog.Catalog
	rec health.Record
	mu  sync.Mutex
}

func newFARig(cat *catalog.Catalog, rec health.Record, have ...string) *faRig {
	g := &faRig{k: &fakeKiwix{have: map[string]bool{}}, al: &fakeAlertSink{}, cat: cat, rec: rec}
	for _, h := range have {
		g.k.have[h] = true
	}
	g.r = portal.NewFirstAidResolver(portal.FirstAidConfig{
		Prober: g.k, Alerts: g.al,
		Inputs: func() (*catalog.Catalog, health.Record) {
			g.mu.Lock()
			defer g.mu.Unlock()
			return g.cat, g.rec
		},
	})
	return g
}

func TestFirstAidOrderingAndUndatedURLs(t *testing.T) {
	cat := faCatalog(
		link("burns", 30, "Burns (injury)", "", ""),
		link("cpr", 10, "CPR", "", ""),
		link("bleeding", 20, "Severe bleeding", "", ""),
	)
	g := newFARig(cat, kiwixRec(bookEN),
		"/library/content/mdwiki_en_all/CPR",
		"/library/content/mdwiki_en_all/Severe%20bleeding",
		"/library/content/mdwiki_en_all/Burns%20%28injury%29")
	g.r.Refresh(context.Background())
	got := g.r.Resolve(cat, g.rec, "en")
	want := []string{"cpr", "bleeding", "burns"}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for i, k := range want {
		if got[i].Key != k || !got[i].Available {
			t.Errorf("entry %d = %+v, want %s available", i, got[i], k)
		}
	}
	if got[2].URL != "/library/content/mdwiki_en_all/Burns%20%28injury%29" {
		t.Errorf("escaped url = %q", got[2].URL)
	}
	if got[0].Title != "EN cpr" {
		t.Errorf("title = %q", got[0].Title)
	}
}

func TestArticlePathEscaping(t *testing.T) {
	cases := map[string]string{
		"A b":       "/library/content/bk/A%20b",
		"X (y)":     "/library/content/bk/X%20%28y%29",
		"Dir/Sub b": "/library/content/bk/Dir/Sub%20b",
		"100%":      "/library/content/bk/100%25",
		"a?b#c":     "/library/content/bk/a%3Fb%23c",
		"Café":      "/library/content/bk/Caf%C3%A9",
	}
	for in, want := range cases {
		if got := portal.ArticlePath("bk", in); got != want {
			t.Errorf("ArticlePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstAidSpanishMatrix(t *testing.T) {
	enP := "/library/content/mdwiki_en_all/Bleeding"
	esP := "/library/content/mdwiki_es_all/Hemorragia"
	cat := faCatalog(link("bleeding", 10, "Bleeding", "wikimed-es", "Hemorragia"))
	cases := []struct {
		name      string
		books     []health.Book
		have      []string
		lang      string
		wantURL   string
		wantAvail bool
		wantTitle string
	}{
		{"es uses es_item", []health.Book{bookEN, bookES}, []string{enP, esP}, "es", esP, true, "ES bleeding"},
		{"es item book missing falls back to en", []health.Book{bookEN}, []string{enP}, "es", enP, true, "ES bleeding"},
		{"es article missing falls back to en", []health.Book{bookEN, bookES}, []string{enP}, "es", enP, true, "ES bleeding"},
		{"en ignores es_item", []health.Book{bookEN, bookES}, []string{enP, esP}, "en", enP, true, "EN bleeding"},
		{"es only es article present but en missing still uses es", []health.Book{bookEN, bookES}, []string{esP}, "es", esP, true, "ES bleeding"},
		{"both missing", []health.Book{bookEN, bookES}, nil, "es", enP, false, "ES bleeding"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newFARig(cat, kiwixRec(c.books...), c.have...)
			g.r.Refresh(context.Background())
			got := g.r.Resolve(cat, g.rec, c.lang)
			if len(got) != 1 || got[0].URL != c.wantURL || got[0].Available != c.wantAvail || got[0].Title != c.wantTitle {
				t.Errorf("got %+v, want url %s avail %v title %s", got, c.wantURL, c.wantAvail, c.wantTitle)
			}
		})
	}
}

func TestFirstAidBookMissingVersusArticleMissing(t *testing.T) {
	cat := faCatalog(link("cpr", 10, "CPR", "", ""), link("choking", 20, "Choking", "", ""))

	// Book not installed: unavailable, no alert, no probe.
	g := newFARig(cat, kiwixRec())
	g.r.Refresh(context.Background())
	got := g.r.Resolve(cat, g.rec, "en")
	if len(got) != 2 || got[0].Available || got[1].Available || got[0].URL != "" {
		t.Errorf("book missing: %+v", got)
	}
	if g.k.calls.Load() != 0 || g.al.last() != nil {
		t.Errorf("book missing must not probe (%d) or alert (%v)", g.k.calls.Load(), g.al.last())
	}

	// Book installed, one article gone: hidden and LAI-SVC-109 with the key.
	g = newFARig(cat, kiwixRec(bookEN), "/library/content/mdwiki_en_all/CPR")
	g.r.Refresh(context.Background())
	got = g.r.Resolve(cat, g.rec, "en")
	if !got[0].Available || got[1].Available {
		t.Errorf("article missing: %+v", got)
	}
	d := g.al.last()
	if d == nil || d["key"] != "choking" {
		t.Fatalf("alert details = %v", d)
	}

	// The article comes back: the alert is cleared.
	g.k.mu.Lock()
	g.k.have["/library/content/mdwiki_en_all/Choking"] = true
	g.k.mu.Unlock()
	g.r.Refresh(context.Background())
	if g.al.cleared == 0 {
		t.Error("alert not cleared")
	}
}

func TestFirstAidKiwixDownIsNotABrokenLink(t *testing.T) {
	cat := faCatalog(link("cpr", 10, "CPR", "", ""))
	g := newFARig(cat, kiwixRec(bookEN), "/library/content/mdwiki_en_all/CPR")
	g.k.down = true
	if n := g.r.Refresh(context.Background()); n != 1 {
		t.Errorf("unreachable = %d, want 1", n)
	}
	if g.al.last() != nil {
		t.Error("kiwix down must not raise LAI-SVC-109")
	}
	if got := g.r.Resolve(cat, g.rec, "en"); got[0].Available {
		t.Errorf("unknown state must be hidden: %+v", got)
	}
}

func TestFirstAidNeverProbesPerRequest(t *testing.T) {
	cat := faCatalog(link("cpr", 10, "CPR", "", ""), link("choking", 20, "Choking", "", ""))
	g := newFARig(cat, kiwixRec(bookEN), "/library/content/mdwiki_en_all/CPR", "/library/content/mdwiki_en_all/Choking")
	g.r.Refresh(context.Background())
	after := g.k.calls.Load()
	if after != 2 {
		t.Fatalf("probes = %d, want 2", after)
	}
	for i := 0; i < 50; i++ {
		g.r.Resolve(cat, g.rec, "en")
		g.r.Resolve(cat, g.rec, "es")
	}
	if g.k.calls.Load() != after {
		t.Errorf("Resolve probed: %d calls", g.k.calls.Load())
	}
}

// waitCalls waits until the probe counter reaches at least n.
func waitCalls(t *testing.T, k *fakeKiwix, n int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if k.calls.Load() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("probes = %d, want >= %d", k.calls.Load(), n)
}

func TestFirstAidRecheckTriggers(t *testing.T) {
	cat := faCatalog(link("cpr", 10, "CPR", "", ""))
	g := newFARig(cat, kiwixRec(bookEN), "/library/content/mdwiki_en_all/CPR", "/library/content/mdwiki_en_all/CPR2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := g.r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.r.Stop(context.Background()) }()
	waitCalls(t, g.k, 1) // the first pass at start
	time.Sleep(50 * time.Millisecond)
	base := g.k.calls.Load()
	g.r.Resolve(cat, g.rec, "en") // nothing changed: no new pass
	time.Sleep(50 * time.Millisecond)
	if g.k.calls.Load() != base {
		t.Fatalf("unchanged inputs rechecked: %d -> %d", base, g.k.calls.Load())
	}

	// Kiwix restart (Restarts and Since change).
	g.mu.Lock()
	g.rec.Restarts++
	g.rec.Since = time.Now()
	rec := g.rec
	g.mu.Unlock()
	g.r.Resolve(cat, rec, "en")
	waitCalls(t, g.k, base+1)
	base = g.k.calls.Load()
	time.Sleep(50 * time.Millisecond)

	// Drive mounted: another book appears.
	g.mu.Lock()
	g.rec.Books = append(append([]health.Book{}, g.rec.Books...), bookES)
	rec = g.rec
	g.mu.Unlock()
	g.r.Resolve(cat, rec, "en")
	waitCalls(t, g.k, base+1)
	base = g.k.calls.Load()
	time.Sleep(50 * time.Millisecond)

	// Catalog refresh: a new catalog value.
	cat2 := faCatalog(link("cpr", 10, "CPR", "", ""))
	g.mu.Lock()
	g.cat = cat2
	g.mu.Unlock()
	g.r.Resolve(cat2, rec, "en")
	waitCalls(t, g.k, base+1)

	// An explicit trigger (a drive event) also rechecks.
	base = g.k.calls.Load()
	time.Sleep(50 * time.Millisecond)
	g.r.Trigger()
	waitCalls(t, g.k, base+1)
}

func TestHTTPProber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/content/b/Yes":
			w.WriteHeader(200)
		case "/library/content/b/Nope":
			w.WriteHeader(404)
		case "/library/content/b/NoHead":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	p := portal.HTTPProber{Base: srv.URL}
	ctx := context.Background()
	if ok, err := p.Exists(ctx, "/library/content/b/Yes"); !ok || err != nil {
		t.Errorf("yes: %v %v", ok, err)
	}
	if ok, err := p.Exists(ctx, "/library/content/b/Nope"); ok || err != nil {
		t.Errorf("nope: %v %v", ok, err)
	}
	if ok, err := p.Exists(ctx, "/library/content/b/NoHead"); !ok || err != nil {
		t.Errorf("nohead: %v %v", ok, err)
	}
	if _, err := p.Exists(ctx, "/library/content/b/Boom"); err == nil {
		t.Error("500 must be an error, not a missing article")
	}
	srv.Close()
	if _, err := p.Exists(ctx, "/library/content/b/Yes"); err == nil {
		t.Error("closed server must be an error")
	}
}

type stubFirstAid struct{ lang string }

func (s *stubFirstAid) Resolve(_ *catalog.Catalog, _ health.Record, lang string) []portal.FirstAid {
	s.lang = lang
	return []portal.FirstAid{{Key: "cpr", Title: "RCP", URL: "/library/content/b/CPR", Available: true}}
}

func TestBuilderFillsFirstAid(t *testing.T) {
	stub := &stubFirstAid{}
	b := portal.NewBuilder(portal.Sources{FirstAid: stub})
	st, err := b.Build(context.Background(), portal.Request{Lang: "es", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if stub.lang != "es" || len(st.FirstAid) != 1 || st.FirstAid[0].Key != "cpr" {
		t.Errorf("lang %q first_aid %+v", stub.lang, st.FirstAid)
	}
}
