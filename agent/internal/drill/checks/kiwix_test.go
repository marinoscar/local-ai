package checks

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

const rssOne = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"><channel>
<title>Search: water</title><opensearch:totalResults>16427</opensearch:totalResults>
<item><title>Water</title><link>/library/content/book/A/Water</link></item>
</channel></rss>`

const rssNone = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"><channel>
<title>Search: water</title><opensearch:totalResults>0</opensearch:totalResults></channel></rss>`

const htmlOne = `<html><body><div class="header">Results 1-1 of 3</div><div class="results"><ul><li><a href="/library/content/b/A/W">Water</a></li></ul></div></body></html>`
const htmlNone = `<html><body><div class="header">No results were found</div></body></html>`

func TestCountResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"rss with a result", rssOne, 1},
		{"rss without", rssNone, 0},
		{"html with a result", htmlOne, 1},
		{"html without", htmlNone, 0},
		{"damaged xml", "<?xml version=\"1.0\"?><rss><channel><item>", 0},
		{"not a search page", "<html>kiwix</html>", 0},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := countResults([]byte(tc.body)); got != tc.want {
				t.Errorf("countResults = %d, want %d", got, tc.want)
			}
		})
	}
}

func books(bs ...LibraryBook) func(context.Context) ([]LibraryBook, error) {
	return func(context.Context) ([]LibraryBook, error) { return bs, nil }
}

func kiwixAgent(answers map[string]http.HandlerFunc) *fakeAgent {
	a := newFakeAgent()
	a.handle("/library/search", func(w http.ResponseWriter, r *http.Request) {
		if h, ok := answers[r.URL.Query().Get("books.name")]; ok {
			h(w, r)
			return
		}
		text(http.StatusOK, "application/rss+xml", rssNone)(w, r)
	})
	return a
}

func TestKiwixSearch(t *testing.T) {
	t1a := LibraryBook{Name: "wiki_med_en", ItemID: "wikimed-en", Tier: 1}
	t1b := LibraryBook{Name: "wikipedia_es_nopic", ItemID: "wikipedia-es-nopic", Tier: 1}
	t2 := LibraryBook{Name: "wikibooks_en", ItemID: "wikibooks-en", Tier: 2}
	ok := text(200, "application/rss+xml", rssOne)
	cases := []struct {
		name    string
		answers map[string]http.HandlerFunc
		books   []LibraryBook
		down    bool
		result  drill.Result
		code    string
	}{
		{"every book answers", map[string]http.HandlerFunc{t1a.Name: ok, t1b.Name: ok, t2.Name: ok}, []LibraryBook{t1a, t1b, t2}, false, drill.ResultPass, ""},
		{"a tier-1 book has no result", map[string]http.HandlerFunc{t1a.Name: ok, t2.Name: ok}, []LibraryBook{t1a, t1b, t2}, false, drill.ResultFail, doctor.DRL030},
		{"a tier-1 book answers 500", map[string]http.HandlerFunc{t1a.Name: status(500), t1b.Name: ok}, []LibraryBook{t1a, t1b}, false, drill.ResultFail, doctor.DRL030},
		{"kiwix down, the proxy answers 503", map[string]http.HandlerFunc{t1a.Name: status(503), t1b.Name: status(503)}, []LibraryBook{t1a, t1b}, false, drill.ResultFail, doctor.DRL030},
		{"the proxy is not reachable", nil, []LibraryBook{t1a}, true, drill.ResultFail, doctor.DRL030},
		{"only a tier-2 book fails", map[string]http.HandlerFunc{t1a.Name: ok, t1b.Name: ok, t2.Name: status(500)}, []LibraryBook{t1a, t1b, t2}, false, drill.ResultWarn, doctor.DRL030},
		{"a tier-2 book with no result", map[string]http.HandlerFunc{t1a.Name: ok, t1b.Name: ok}, []LibraryBook{t1a, t1b, t2}, false, drill.ResultWarn, doctor.DRL030},
		{"tier 1 and tier 2 both fail: fail", map[string]http.HandlerFunc{t1b.Name: ok}, []LibraryBook{t1a, t1b, t2}, false, drill.ResultFail, doctor.DRL030},
		{"no books at all", nil, nil, false, drill.ResultSkip, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := kiwixAgent(tc.answers)
			a.take("/library", tc.down)
			out := run(t, NewKiwixCheck(KiwixDeps{HTTP: a, Books: books(tc.books...)}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
		})
	}
}

func TestKiwixAsksTheProxyForWaterOneResultPerBook(t *testing.T) {
	a := kiwixAgent(map[string]http.HandlerFunc{"bk": text(200, "application/rss+xml", rssOne)})
	run(t, NewKiwixCheck(KiwixDeps{HTTP: a, Books: books(LibraryBook{Name: "bk", Tier: 1})}))
	reqs := a.requests()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0], "GET /library/search?") {
		t.Fatalf("requests = %v", reqs)
	}
	u, err := url.Parse("http://x" + strings.TrimPrefix(reqs[0], "GET "))
	must(t, err)
	q := u.Query()
	if q.Get("books.name") != "bk" || q.Get("pattern") != "water" || q.Get("pageLength") != "1" {
		t.Errorf("query = %v", q)
	}
}

func TestKiwixLibraryUnknownAndWiring(t *testing.T) {
	c := NewKiwixCheck(KiwixDeps{HTTP: newFakeAgent(), Books: func(context.Context) ([]LibraryBook, error) { return nil, errors.New("no run yet") }})
	if out := run(t, c); out.Result != drill.ResultFail || out.Code != doctor.DRL030 {
		t.Errorf("library unknown: %+v", out)
	}
	if out := run(t, NewKiwixCheck(KiwixDeps{})); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
	if c.ID != "drl.kiwix.search" || !c.Critical || c.Order != 8 || c.FailCode != doctor.DRL030 {
		t.Errorf("check = %+v", c)
	}
}
