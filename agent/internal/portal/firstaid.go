package portal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// First-aid links (survival-portal.md sections 3.4 and 4.1.10). The catalog's portal_links are
// resolved to undated kiwix book aliases; an article is checked against kiwix-serve in the
// background (never per status request) and the result is cached.

const (
	// DefaultKiwixBase is where kiwix-serve listens (companion-services.md section 4.1.3).
	DefaultKiwixBase = "http://127.0.0.1:8888"
	// FirstAidRecheck is how often the cached availability is refreshed besides the triggers.
	FirstAidRecheck = 6 * time.Hour
	// firstAidRetry is the retry after a pass in which kiwix could not be asked.
	firstAidRetry    = 30 * time.Second
	probeTimeout     = 3 * time.Second
	probeConcurrency = 4
)

// ArticleProber asks kiwix-serve whether an article exists. found is false with a nil error for a
// missing article (404); an error means kiwix could not be asked.
type ArticleProber interface {
	Exists(ctx context.Context, path string) (found bool, err error)
}

// HTTPProber is the ArticleProber over kiwix-serve's HTTP port.
type HTTPProber struct {
	// Base is the kiwix-serve origin, default DefaultKiwixBase.
	Base string
	// Client defaults to a client with the 3 s probe timeout.
	Client *http.Client
}

// Exists implements ArticleProber with a HEAD request (a GET when HEAD is not supported).
func (p HTTPProber) Exists(ctx context.Context, path string) (bool, error) {
	base := p.Base
	if base == "" {
		base = DefaultKiwixBase
	}
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: probeTimeout}
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
		if err != nil {
			return false, err
		}
		res, err := c.Do(req)
		if err != nil {
			return false, err
		}
		_ = res.Body.Close()
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 400:
			return true, nil
		case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
			return false, nil
		case method == http.MethodHead && (res.StatusCode == http.StatusMethodNotAllowed || res.StatusCode == http.StatusNotImplemented):
			continue
		default:
			return false, fmt.Errorf("kiwix answered %d", res.StatusCode)
		}
	}
	return false, fmt.Errorf("kiwix did not answer")
}

// ArticlePath is /library/content/<undated book>/<article>, each path segment escaped.
func ArticlePath(book, article string) string {
	segs := strings.Split(article, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/library/content/" + url.PathEscape(book) + "/" + strings.Join(segs, "/")
}

// FirstAidAlerts is what the resolver needs of the doctor's alert manager; *doctor.Manager implements it.
type FirstAidAlerts interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
	Clear(code string)
}

// FirstAidConfig builds a FirstAidResolver.
type FirstAidConfig struct {
	Prober ArticleProber
	// Inputs returns the active catalog and kiwix's record (its book map). Called by the background pass.
	Inputs func() (*catalog.Catalog, health.Record)
	Alerts FirstAidAlerts
	// Bus, when set, triggers a recheck on drive and service events.
	Bus    *events.Bus
	Logger *slog.Logger
	// Recheck overrides FirstAidRecheck (tests).
	Recheck time.Duration
}

type probeState int

const (
	stateFound probeState = iota + 1
	stateMissing
	stateUnknown
)

// FirstAidResolver resolves portal_links and keeps their availability cached.
type FirstAidResolver struct {
	cfg FirstAidConfig
	log *slog.Logger

	mu    sync.Mutex
	cache map[string]probeState
	print string

	trigger chan struct{}

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewFirstAidResolver returns a resolver. It does nothing until Start (or Refresh).
func NewFirstAidResolver(cfg FirstAidConfig) *FirstAidResolver {
	r := &FirstAidResolver{cfg: cfg, log: cfg.Logger, cache: map[string]probeState{}, trigger: make(chan struct{}, 1)}
	if r.log == nil {
		r.log = slog.Default()
	}
	r.log = r.log.With("component", "first-aid")
	return r
}

// firstAidLink is one portal_links entry with its book names resolved.
type firstAidLink struct {
	key    string
	order  int
	titleE string
	titleS string
	// enBook and esBook are the undated kiwix names; "" when the item is not installed.
	enBook, esBook string
	enPath, esPath string
}

func bookOf(books []health.Book, contentID string) string {
	for _, b := range books {
		if b.ContentID == contentID {
			return b.Name
		}
	}
	return ""
}

// links collects every portal_links entry of the catalog, sorted by order then key; a key seen twice
// keeps its first entry (the lint forbids duplicates, this keeps the output stable anyway).
func links(cat *catalog.Catalog, books []health.Book) []firstAidLink {
	if cat == nil {
		return nil
	}
	var out []firstAidLink
	seen := map[string]bool{}
	for _, it := range cat.Content {
		if it.Kind != "kiwix" {
			continue
		}
		for _, pl := range it.PortalLinks {
			if pl.Key == "" || seen[pl.Key] {
				continue
			}
			seen[pl.Key] = true
			l := firstAidLink{key: pl.Key, order: pl.Order, titleE: pl.Title.EN, titleS: pl.Title.ES}
			if l.enBook = bookOf(books, it.ID); l.enBook != "" && pl.Article != "" {
				l.enPath = ArticlePath(l.enBook, pl.Article)
			}
			if pl.ESItem != "" && pl.ESArticle != "" {
				if l.esBook = bookOf(books, pl.ESItem); l.esBook != "" {
					l.esPath = ArticlePath(l.esBook, pl.ESArticle)
				}
			}
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return out[i].key < out[j].key
	})
	return out
}

// Resolve returns the first_aid entries for lang from the cache. It never asks kiwix: when the
// inputs differ from the last checked ones (a new catalog, another book set, a kiwix restart) it
// only schedules a background recheck.
func (r *FirstAidResolver) Resolve(cat *catalog.Catalog, kiwix health.Record, lang string) []FirstAid {
	out := []FirstAid{}
	ls := links(cat, kiwix.Books)
	if len(ls) == 0 {
		return out
	}
	fp := fingerprint(cat, kiwix)
	r.mu.Lock()
	changed := fp != r.print
	cache := r.cache
	r.mu.Unlock()
	if changed {
		r.Trigger()
	}
	es := lang == doctor.LangES
	for _, l := range ls {
		title := l.titleE
		if es && l.titleS != "" {
			title = l.titleS
		}
		e := FirstAid{Key: l.key, Title: title}
		switch {
		case es && l.esPath != "" && cache[l.esPath] == stateFound:
			e.URL, e.Available = l.esPath, true
		case l.enPath != "":
			e.URL, e.Available = l.enPath, cache[l.enPath] == stateFound
		}
		out = append(out, e)
	}
	return out
}

func fingerprint(cat *catalog.Catalog, k health.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%p|%s|%s|%d|%d|", cat, catVersion(cat), k.State, k.Restarts, k.Since.UnixNano())
	bs := make([]string, 0, len(k.Books))
	for _, bk := range k.Books {
		bs = append(bs, bk.ContentID+"="+bk.Name+"@"+bk.Root)
	}
	sort.Strings(bs)
	b.WriteString(strings.Join(bs, ","))
	return b.String()
}

func catVersion(c *catalog.Catalog) string {
	if c == nil {
		return ""
	}
	return c.Version
}

// Trigger asks for a recheck; calls that arrive while one is pending are merged.
func (r *FirstAidResolver) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Refresh checks every first-aid article now, replaces the cache and raises or clears LAI-SVC-109.
// It returns the number of probes that could not reach kiwix.
func (r *FirstAidResolver) Refresh(ctx context.Context) (unreachable int) {
	if r.cfg.Inputs == nil || r.cfg.Prober == nil {
		return 0
	}
	cat, rec := r.cfg.Inputs()
	ls := links(cat, rec.Books)
	paths := map[string]bool{}
	for _, l := range ls {
		for _, p := range []string{l.enPath, l.esPath} {
			if p != "" {
				paths[p] = true
			}
		}
	}
	list := make([]string, 0, len(paths))
	for p := range paths {
		list = append(list, p)
	}
	sort.Strings(list)

	next := make(map[string]probeState, len(list))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)
	for _, p := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			found, err := r.cfg.Prober.Exists(pctx, p)
			cancel()
			st := stateFound
			switch {
			case err != nil:
				st = stateUnknown
			case !found:
				st = stateMissing
			}
			mu.Lock()
			next[p] = st
			if st == stateUnknown {
				unreachable++
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return unreachable
	}

	// A broken link is a missing article of an installed book (an absent book is LAI-SVC-001's business).
	var broken []string
	for _, l := range ls {
		if l.enPath != "" && next[l.enPath] == stateMissing || l.esPath != "" && next[l.esPath] == stateMissing {
			broken = append(broken, l.key)
		}
	}

	r.mu.Lock()
	r.cache = next
	r.print = fingerprint(cat, rec)
	r.mu.Unlock()

	if a := r.cfg.Alerts; a != nil {
		if len(broken) > 0 {
			if err := a.Raise(doctor.SVC109, map[string]any{"key": broken[0], "keys": broken}); err != nil {
				r.log.Warn("could not raise the first-aid alert", "error", err.Error())
			}
		} else {
			a.Clear(doctor.SVC109)
		}
	}
	return unreachable
}

// Name implements the app Worker.
func (r *FirstAidResolver) Name() string { return "first-aid" }

// Start implements the app Worker: a pass now, then on every trigger and every FirstAidRecheck.
func (r *FirstAidResolver) Start(ctx context.Context) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.cancel != nil {
		return fmt.Errorf("first-aid: already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel, r.done = cancel, make(chan struct{})
	every := r.cfg.Recheck
	if every <= 0 {
		every = FirstAidRecheck
	}
	if r.cfg.Bus != nil {
		if f, err := events.ParseFilter("drive.*,service.state"); err == nil {
			if ch, err := r.cfg.Bus.Subscribe(loopCtx, f, 0); err == nil {
				go func() {
					for range ch {
						r.Trigger()
					}
				}()
			}
		}
	}
	go func(done chan struct{}) {
		defer close(done)
		tick := time.NewTicker(every)
		defer tick.Stop()
		var retry <-chan time.Time
		r.Trigger()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-tick.C:
			case <-r.trigger:
			case <-retry:
			}
			retry = nil
			if r.Refresh(loopCtx) > 0 {
				retry = time.After(firstAidRetry)
			}
		}
	}(r.done)
	return nil
}

// Stop implements the app Worker.
func (r *FirstAidResolver) Stop(ctx context.Context) error {
	r.runMu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil
	r.runMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
