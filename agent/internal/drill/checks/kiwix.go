package checks

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// CodeLibrarySearch is LAI-DRL-030: the library search does not work.
const CodeLibrarySearch = doctor.DRL030

// LibraryBook is one book of the library with its survival tier.
type LibraryBook struct {
	// Name is the book name kiwix-serve serves it under, without a date ("wikipedia_en_all_nopic"): the
	// value of the books.name query parameter.
	Name string
	// ItemID is the catalog content id, for the document.
	ItemID string
	// Tier is the survival tier of the item (1 to 3).
	Tier int
}

// KiwixDeps are what check 8 needs.
type KiwixDeps struct {
	HTTP AgentHTTP
	// Books lists the books the library serves now with their tiers. An error means the library is not
	// known (kiwix never produced one).
	Books func(ctx context.Context) ([]LibraryBook, error)
	// PerBook bounds one search; zero means 20 s.
	PerBook time.Duration
}

// NewKiwixCheck is check 8, drl.kiwix.search (critical): a full-text search of the word "water" in
// every book of the library, through the agent's /library/ proxy, the same path the Portal's search
// takes.
//
//	pass: every tier-1 book answers with at least one result.
//	warn: a tier-2 or tier-3 book fails (LAI-DRL-030).
//	fail: a tier-1 book fails, or kiwix is down (LAI-DRL-030).
//	skip: the library has no books (check 3 reports what is missing).
func NewKiwixCheck(d KiwixDeps) drill.Check {
	return drill.Check{
		ID: "drl.kiwix.search", Area: "library", Critical: true, Order: orderKiwix,
		Timeout: kiwixTimeout, FailCode: CodeLibrarySearch,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runKiwix(ctx, d) },
	}
}

func runKiwix(ctx context.Context, d KiwixDeps) drill.Outcome {
	if d.HTTP == nil || d.Books == nil {
		return contentSkip("not_available", nil)
	}
	books, err := d.Books(ctx)
	if err != nil {
		return contentFail(CodeLibrarySearch, map[string]any{"reason": "library_unknown", "error": reasonOf(err)})
	}
	if len(books) == 0 {
		return contentSkip("no_books", nil)
	}
	per := d.PerBook
	if per <= 0 {
		per = 20 * time.Second
	}
	var tier1Failed, otherFailed []map[string]any
	ok := 0
	for _, b := range books {
		if ctx.Err() != nil {
			break
		}
		n, reason := searchBook(ctx, d.HTTP, b.Name, per)
		if n >= 1 {
			ok++
			continue
		}
		f := map[string]any{"book": b.Name, "tier": b.Tier, "reason": reason}
		if b.Tier == 1 {
			tier1Failed = append(tier1Failed, f)
		} else {
			otherFailed = append(otherFailed, f)
		}
	}
	details := map[string]any{"books": len(books), "answered": ok}
	switch {
	case len(tier1Failed) > 0:
		details["failed"] = append(tier1Failed, otherFailed...)
		return contentFail(CodeLibrarySearch, details)
	case len(otherFailed) > 0:
		details["failed"] = otherFailed
		return contentWarn(CodeLibrarySearch, details)
	}
	return contentPass(details)
}

// searchBook runs the search of one book and returns how many results it found and, when none, why.
func searchBook(ctx context.Context, c AgentHTTP, name string, timeout time.Duration) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	q := url.Values{}
	q.Set("books.name", name)
	q.Set("pattern", "water")
	q.Set("pageLength", "1")
	q.Set("format", "xml")
	rp, err := getAgent(ctx, c, "/library/search?"+q.Encode(), 1<<20)
	switch {
	case err != nil:
		return 0, "request_failed"
	case rp.Status != http.StatusOK:
		return 0, statusReason(rp.Status)
	}
	n := countResults(rp.Body)
	if n == 0 {
		return 0, "no_results"
	}
	return n, ""
}

var (
	htmlResultRE = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*\bresults\b[^"]*"[^>]*>(.*?)</div>`)
	liRE         = regexp.MustCompile(`(?i)<li[\s>]`)
)

// countResults counts the results of a kiwix-serve search answer. The OpenSearch RSS ("format=xml")
// is read with encoding/xml: the number of <item> elements of the page. A
// page of HTML is read too, for a kiwix-serve that ignores the format: its list items inside the
// results block. Anything else is no result.
func countResults(body []byte) int {
	trim := bytes.TrimSpace(body)
	if bytes.HasPrefix(trim, []byte("<?xml")) || bytes.HasPrefix(trim, []byte("<rss")) {
		var doc struct {
			Channel struct {
				Items []struct {
					Title string `xml:"title"`
				} `xml:"item"`
			} `xml:"channel"`
		}
		if err := xml.NewDecoder(bytes.NewReader(trim)).Decode(&doc); err != nil {
			return 0
		}
		return len(doc.Channel.Items)
	}
	if m := htmlResultRE.FindSubmatch(body); m != nil {
		return len(liRE.FindAll(m[1], -1))
	}
	return 0
}
