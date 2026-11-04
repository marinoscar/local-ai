package kiwix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// BookRef is one book of the merged library: the catalog content id, the book name and the root the
// chosen copy is on. Name is the MANIFEST source.book (companion-services.md section 4.1.2).
type BookRef struct {
	ContentID string
	Name      string
	Root      string
}

// Status is what the last good regeneration found. The zero value knows nothing; a nil *Status is
// usable and knows nothing. Safe for concurrent use.
type Status struct {
	mu    sync.RWMutex
	known bool
	books []BookRef
}

// NewStatus returns an empty Status.
func NewStatus() *Status { return &Status{} }

// Set records the books of a good merged run (an empty list is a known, empty library).
func (s *Status) Set(books []BookRef) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.known, s.books = true, append([]BookRef(nil), books...)
	s.mu.Unlock()
}

// Snapshot returns a copy of the books and whether a run has happened since the agent started.
func (s *Status) Snapshot() (books []BookRef, known bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]BookRef(nil), s.books...), s.known
}

// Content is the health.Probe content check: a library known to be empty has nothing to serve
// (missing_content, LAI-SVC-001). Before the first run nothing is claimed.
func (s *Status) Content(context.Context) health.Content {
	if books, known := s.Snapshot(); known && len(books) == 0 {
		return health.Content{Missing: true, Code: doctor.SVC001}
	}
	return health.Content{}
}

// Fetcher gets a loopback URL and returns the body (a fake in tests).
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// HTTPFetcher is the real Fetcher: plain GET, no proxy, no redirect, 1 MiB cap.
type HTTPFetcher struct{ Client *http.Client }

// Get implements Fetcher.
func (f HTTPFetcher) Get(ctx context.Context, url string) ([]byte, error) {
	c := f.Client
	if c == nil {
		c = &http.Client{
			Timeout:       3 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

var totalResultsRe = regexp.MustCompile(`<(?:[A-Za-z0-9_]+:)?totalResults>\s*(\d+)\s*</`)

// ParseTotalResults reads <opensearch:totalResults> from an OPDS catalog page.
func ParseTotalResults(body []byte) (int, error) {
	m := totalResultsRe.FindSubmatch(body)
	if m == nil {
		return 0, errors.New("no totalResults in the catalog page")
	}
	return strconv.Atoi(string(m[1]))
}

// CatalogURL is the liveness URL whose totalResults is compared with the merged library
// (companion-services.md section 4.6).
func CatalogURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/library/catalog/v2/entries?count=1"
}

// Attach plugs the library checks into the kiwix probe: Content (nothing to serve) and Readiness
// (the book list and the totalResults comparison, LAI-SVC-004 when kiwix-serve skipped a ZIM). port
// gives the kiwix-serve port.
func (s *Status) Attach(p *health.FuncProbe, f Fetcher, port func() int) {
	p.ContentFn = s.Content
	p.ReadinessFn = func(ctx context.Context) health.Readiness {
		books, _ := s.Snapshot()
		rd := health.Readiness{Detail: plural(len(books), "book"), Books: BookMap(books)}
		if f != nil && port != nil {
			if body, err := f.Get(ctx, CatalogURL(port())); err == nil {
				if n, perr := ParseTotalResults(body); perr == nil && n != len(books) {
					rd.Problems = append(rd.Problems, health.Problem{Code: doctor.SVC004, Severity: doctor.Warning})
				}
			}
		}
		return rd
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
