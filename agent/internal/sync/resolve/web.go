package resolve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The web resolvers: huggingface, github_release and url (ark-sync.md sections 4.9.1, 4.9.4 and
// 4.9.5). Each is a pure network reader: it turns a catalog item into the exact files to download
// (URL, size, sha256 and where the sha256 came from) after cross-checking the catalog pin against what
// the source itself publishes, and it never writes into a root. Only an `extract: true` item is made
// by a producer (Resolution.Produce), because the verified archive must be unpacked before the swap.
//
// Like the kit resolvers, each type has two halves:
//
//   - the planner's half (plan.Resolver): only `url` has one, because only a `url` item can follow
//     "latest" or a sidecar, which the catalog cannot pin. A huggingface, github_release or pinned
//     url item is planned from what the catalog pins (plan.FromCatalog), which is exactly its version
//     and size, so the dry run makes no request for it;
//   - the engine's half (run.ItemResolver, through WebItemResolver), called once per item in the
//     resolving state: this is where the checks happen and where LAI-SYN-021 stops a bad pin before a
//     single byte is downloaded.
//
// Rules this file keeps (AGENTS.md rules 2 and 3, ark-sync.md section 6):
//
//   - the network is touched only when Env.Online says so (the second lock behind the planner and the
//     engine), over https, with a hardened client (TLS 1.2 or later, no proxy, bounded time);
//   - the Hugging Face token goes only to the Hugging Face host, is read at request time, is never
//     kept, logged or put in an error, and is dropped on any redirect to another host (the port
//     counts: Go itself would keep it for the same host name on another port);
//   - a checksum that disagrees with the catalog is LAI-SYN-021 and the item is not downloaded;
//   - metadata answers are bounded in size, and URLs in errors and logs are reduced to host and path.

// The doctor codes of the web resolvers, aliased from the single table (doctor/codes.go).
const (
	// CodeSourceDisagrees: the source's checksum or size disagrees with the catalog (LAI-SYN-021).
	CodeSourceDisagrees = doctor.SYN021
	// CodeNoNewest: the version or the asset could not be found at the source (LAI-SYN-033).
	CodeNoNewest = doctor.SYN033
	// CodeHFLogin: Hugging Face needs a login or is rate-limiting (LAI-SYN-034).
	CodeHFLogin = doctor.SYN034
)

// Defaults of Web.
const (
	// HFBase is the Hugging Face site; the token is sent to its host only.
	HFBase = "https://huggingface.co"
	// GitHubAPIBase is the GitHub REST API.
	GitHubAPIBase = "https://api.github.com"
	// MaxRateWait caps how long a resolver waits for a rate limit to lift (ark-sync.md section 4.9.4).
	MaxRateWait = 15 * time.Minute

	maxMetaBody   = 16 << 20 // a release or tree listing
	maxSidecar    = 1 << 20  // a SHA256SUMS file
	metaTimeout   = 2 * time.Minute
	maxMetaRedirs = 10
	releaseTTL    = 10 * time.Minute
)

// Web is the shared environment of the three web resolvers. Env is required; every other field has
// a default, and most are seams for tests.
type Web struct {
	// Env supplies the online gate, the clock and the logger (and the tool lookup for xz).
	Env *Env
	// HTTP is the client of the metadata requests (HEAD, small JSON); nil builds a hardened one. Its
	// redirect policy is replaced per request.
	HTTP *http.Client
	// Downloader fetches the archive of an extract item; nil uses Env.Downloader.
	Downloader *download.Downloader
	// HFBase and GitHubAPI replace the two sites (tests); the Hugging Face token goes to the host of
	// HFBase only.
	HFBase, GitHubAPI string
	// HFToken returns the Hugging Face token ("" when there is none). It is called for every request
	// that may carry it and its result is never stored or logged.
	HFToken func() string
	// UserAgent is sent on every request; default download.UserAgent("dev").
	UserAgent string
	// CatalogVersion returns the version of the catalog being synced: the SortKey of a pinned item
	// (the catalog version in which its pin first reached this node). Empty means the sync time.
	CatalogVersion func() string
	// Sleep waits for a rate limit to lift; default a context-aware timer. Tests record the wait.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxRateWait caps that wait; default MaxRateWait (15 minutes).
	MaxRateWait time.Duration
	// Streamer starts the xz program for a .tar.xz; default execx.Exec{}.
	Streamer execx.Streamer
	// Limits bound an archive extraction; the zero value takes the defaults.
	Limits ExtractLimits

	mu       sync.Mutex
	releases map[string]cachedRelease
}

type cachedRelease struct {
	rel *ghRelease
	at  time.Time
}

func (w *Web) env() *Env {
	if w.Env != nil {
		return w.Env
	}
	return &Env{}
}

func (w *Web) now() time.Time { return w.env().now() }

func (w *Web) log() *redactLogger { return &redactLogger{w.env()} }

func (w *Web) ua() string {
	if w.UserAgent != "" {
		return w.UserAgent
	}
	return download.UserAgent("dev")
}

func (w *Web) hfBase() string {
	if w.HFBase != "" {
		return strings.TrimRight(w.HFBase, "/")
	}
	return HFBase
}

func (w *Web) githubAPI() string {
	if w.GitHubAPI != "" {
		return strings.TrimRight(w.GitHubAPI, "/")
	}
	return GitHubAPIBase
}

func (w *Web) maxRateWait() time.Duration {
	if w.MaxRateWait > 0 {
		return w.MaxRateWait
	}
	return MaxRateWait
}

func (w *Web) downloader() *download.Downloader {
	if w.Downloader != nil {
		return w.Downloader
	}
	return w.env().Downloader
}

func (w *Web) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// sortKey is the SortKey of a pinned item: the catalog version being synced, else the sync time.
func (w *Web) sortKey() string {
	if w.CatalogVersion != nil {
		if v := strings.TrimSpace(w.CatalogVersion()); v != "" && manifest.SafeName(v) {
			return v
		}
	}
	return w.syncTime()
}

// syncTime is the SortKey of an item that follows "latest": the moment it was resolved.
func (w *Web) syncTime() string { return w.now().UTC().Format(time.RFC3339) }

// requireOnline is the second lock in front of every request.
func (w *Web) requireOnline() error { return w.env().requireOnline() }

// hfHost is the host (with port) the Hugging Face token may go to.
func (w *Web) hfHost() string {
	u, err := url.Parse(w.hfBase())
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// client returns the metadata client with the redirect policy of one request.
func (w *Web) client(opt reqOpts) *http.Client {
	var c http.Client
	if w.HTTP != nil {
		c = *w.HTTP
	} else {
		c = http.Client{Transport: defaultMetaTransport()}
	}
	if c.Timeout == 0 {
		c.Timeout = metaTimeout
	}
	c.Jar = nil
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !opt.follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxMetaRedirs {
			return fmt.Errorf("stopped after %d redirects", maxMetaRedirs)
		}
		if req.URL.Scheme != "https" {
			return errRedirectHTTP
		}
		// The host with its port: Go keeps the Authorization header for the same host name on another
		// port, and a token must never leave the host it was meant for.
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			req.Header.Del("Authorization")
			if opt.sameHost {
				return errRedirectOrigin
			}
		}
		return nil
	}
	return &c
}

var (
	errRedirectHTTP   = errors.New("a redirect to a non-https URL was refused")
	errRedirectOrigin = errors.New("a redirect to another host was refused")
)

func defaultMetaTransport() http.RoundTripper {
	d := &net.Dialer{Timeout: download.DialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   download.TLSHandshakeTimeout,
		ResponseHeaderTimeout: download.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}

// reqOpts is how one metadata request is made.
type reqOpts struct {
	// follow follows redirects (https only, at most ten); false returns the first answer as it is.
	follow bool
	// sameHost refuses a redirect to another host (a checksum file must come from the origin).
	sameHost bool
	// auth sends the Hugging Face token, only to the Hugging Face host.
	auth bool
	// accept is the Accept header.
	accept string
}

// do makes one request. The caller closes the body. The error never carries a header or a query.
func (w *Web) do(ctx context.Context, method, rawURL string, opt reqOpts) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fail(CodeNoNewest, nil, "the URL is not usable")
	}
	if u.Scheme != "https" {
		return nil, fail(CodeNoNewest, nil, "%s is not an https URL", redactURL(rawURL))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fail(CodeNoNewest, nil, "the URL is not usable")
	}
	req.Header.Set("User-Agent", w.ua())
	req.Header.Set("Accept-Encoding", "identity")
	if opt.accept != "" {
		req.Header.Set("Accept", opt.accept)
	}
	if opt.auth && w.HFToken != nil && strings.EqualFold(u.Host, w.hfHost()) {
		if tok := strings.TrimSpace(w.HFToken()); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := w.client(opt).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail(CodeNoNewest, nil, "%s %s failed: %s", method, redactURL(rawURL), cleanNetErr(err))
	}
	return resp, nil
}

// cleanNetErr reduces a client error to a short sentence with no URL (the URL may carry a query).
func cleanNetErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return Clip([]byte(err.Error()), 160)
}

// redactURL reduces a URL to scheme, host and path.
func redactURL(raw string) string { return download.RedactURL(raw) }

// redactLogger logs with the component attribute and nothing secret.
type redactLogger struct{ e *Env }

func (l *redactLogger) info(msg string, args ...any) {
	l.e.log().Info(msg, append([]any{"component", "sync"}, args...)...)
}

// permanent marks an error the next run will meet again unless the catalog or the source changes: the
// engine reports it without a retry hint.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

func markPermanent(err error) error { return permanent{err} }

func isPermanent(err error) bool {
	var p permanent
	if errors.As(err, &p) {
		return true
	}
	switch CodeOf(err) {
	case doctor.SYN021, doctor.SYN030:
		return true
	}
	return false
}

// failPerm is fail, marked permanent.
func failPerm(code string, err error, format string, args ...any) error {
	return markPermanent(fail(code, err, format, args...))
}

// retryAfter reads Retry-After (seconds or an HTTP date); false when absent or unusable.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// rateLimited reports whether a 403 or 429 answer is a rate limit and how long to wait for it to lift.
// GitHub says so with X-RateLimit-Remaining: 0 and X-RateLimit-Reset (epoch seconds), or with
// Retry-After for its secondary limit; Hugging Face with a 429 and Retry-After.
func rateLimited(resp *http.Response, now time.Time) (wait time.Duration, ok bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	if d, has := retryAfter(resp.Header, now); has {
		return d, true
	}
	if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
		if n, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if d := time.Unix(n, 0).Sub(now); d > 0 {
				return d, true
			}
			return 0, true
		}
		return 0, true
	}
	return 0, resp.StatusCode == http.StatusTooManyRequests
}

// withRateLimit runs one request and, when the answer is a rate limit that lifts within the cap, waits
// and runs it once more. A limit that does not lift in time is a transient error with the given code.
func (w *Web) withRateLimit(ctx context.Context, code, what string, call func() (*http.Response, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := call()
		if err != nil {
			return nil, err
		}
		wait, limited := rateLimited(resp, w.now())
		if !limited {
			return resp, nil
		}
		_ = resp.Body.Close()
		if wait > w.maxRateWait() || attempt >= 1 {
			return nil, fail(code, nil, "%s is rate-limiting this node (try again in about %s)", what, wait.Round(time.Second))
		}
		w.log().info("a rate limit is waited for", "source", what, "wait", wait.Round(time.Second).String())
		if err := w.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// WebFile is one file of a web resolver's answer.
type WebFile struct {
	// Path is the file's path inside the item (a clean slash path).
	Path string
	// Kind is the manifest's file kind.
	Kind string
	// URL is the https URL to download.
	URL string
	// Size is the exact size in bytes, 0 when unknown (a computed checksum).
	Size int64
	// SHA256 is the lower-case hex digest the file must have, "" when it is to be computed.
	SHA256 string
	// SHA256Source says how the digest was established: "catalog+hf_lfs", "catalog+github_digest",
	// "catalog", "sidecar" or "computed_tofu".
	SHA256Source string
	// Auth: the download carries the Hugging Face token (only ever to its own host).
	Auth bool
}

// WebResolved is the rich answer of a web resolver (the issue's Resolved), before it is reduced to
// plan.Resolved for the planner or expanded to item.Resolution for the engine.
type WebResolved struct {
	// SourceType is the catalog source type that produced it.
	SourceType string
	// Version is short and plain; SortKey orders versions.
	Version, SortKey string
	// Files are the files to download, in catalog order.
	Files []WebFile
	// Trust says how the digests were established.
	Trust manifest.Trust
	// Extract: the single file is an archive to unpack into a directory item.
	Extract bool
	// Producer is "http" (every web resolver downloads over http; the archive is unpacked by the
	// producer of the engine half).
	Producer string
	// Meta is recorded in MANIFEST.source (never a secret).
	Meta map[string]string
}

// TotalBytes is the sum of the file sizes.
func (r WebResolved) TotalBytes() int64 {
	var n int64
	for _, f := range r.Files {
		n += f.Size
	}
	return n
}

// webSource is what the engine adapter needs of a resolver.
type webSource interface {
	SourceType() string
	Lookup(ctx context.Context, it plan.CatalogItem) (WebResolved, error)
}

// WebItemResolver is the engine half of a web resolver: a run.ItemResolver. Register it with
// ItemResolvers.Register(src.SourceType(), ...).
type WebItemResolver struct {
	W   *Web
	Src webSource
}

// Resolve implements run.ItemResolver.
func (a WebItemResolver) Resolve(ctx context.Context, it plan.CatalogItem, _ plan.ItemPlan) (item.Resolution, error) {
	wr, err := a.Src.Lookup(ctx, it)
	if err != nil {
		return item.Resolution{}, toResolveError(err)
	}
	res, err := a.W.resolution(it, wr)
	if err != nil {
		return item.Resolution{}, toResolveError(err)
	}
	return res, nil
}

// toResolveError gives a resolver's error the shape the machine reads. A cancelled context passes
// through unchanged.
func toResolveError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var re *item.ResolveError
	if errors.As(err, &re) {
		return err
	}
	var de *download.Error
	if errors.As(err, &de) {
		return err
	}
	code := CodeOf(err)
	if code == "" {
		code = CodeNoNewest
	}
	return &item.ResolveError{Code: code, Permanent: isPermanent(err), Err: err}
}

// resolution expands a WebResolved to what the machine downloads.
func (w *Web) resolution(it plan.CatalogItem, wr WebResolved) (item.Resolution, error) {
	src := manifest.Source{Type: wr.SourceType}
	keys := make([]string, 0, len(wr.Meta))
	for k := range wr.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := wr.Meta[k]; v != "" {
			var err error
			if src, err = src.With(k, v); err != nil {
				return item.Resolution{}, failPerm(CodeNoNewest, err, "the source field %s of %s cannot be recorded", k, it.ID)
			}
		}
	}
	res := item.Resolution{Version: wr.Version, SortKey: wr.SortKey, Source: src, Trust: wr.Trust, Resolver: wr.SourceType}
	if wr.Extract {
		if len(wr.Files) != 1 {
			return item.Resolution{}, failPerm(CodeNoNewest, nil, "extract: true needs exactly one archive, %s has %d files", it.ID, len(wr.Files))
		}
		res.Dir = true
		res.Produce = w.produceExtract(it, wr)
		return res, nil
	}
	for _, f := range wr.Files {
		res.Files = append(res.Files, item.File{
			Path: f.Path, Kind: f.Kind, URL: f.URL, Size: f.Size, SHA256: f.SHA256, SHA256Source: f.SHA256Source, Auth: f.Auth,
		})
	}
	return res, nil
}

// archiveDir is the staging directory (below <root>/.staging/<item>/<version>/) that holds the
// downloaded archive of an extract item.
const archiveDir = "archive"

// extractPrefix names the staging directories an archive is unpacked into: extract.1, extract.2, ...
const extractPrefix = "extract."

// produceExtract is Resolution.Produce of an extract item: download the archive (verified by the
// downloader against the pin), check it again on disk, unpack it into a fresh directory of the staging
// area, seal the tree (SHA256SUMS) and name that directory as the tree to swap in.
func (w *Web) produceExtract(it plan.CatalogItem, wr WebResolved) func(context.Context, item.ProduceEnv) (item.Produced, error) {
	f := wr.Files[0]
	return func(ctx context.Context, env item.ProduceEnv) (item.Produced, error) {
		p, err := w.extractItem(ctx, env, it, wr, f)
		if err != nil {
			return item.Produced{}, toResolveError(err)
		}
		return p, nil
	}
}

// extractItem is the body of Produce.
func (w *Web) extractItem(ctx context.Context, env item.ProduceEnv, it plan.CatalogItem, wr WebResolved, f WebFile) (item.Produced, error) {
	if err := w.requireOnline(); err != nil {
		return item.Produced{}, err
	}
	dl := w.downloader()
	if dl == nil {
		return item.Produced{}, markPermanent(fail(item.CodeInternal, nil, "no downloader is configured"))
	}
	job := download.FileJob{
		RootID: env.RootID, Root: env.Root, Item: it.ID, Version: wr.Version, Name: archiveDir + "/" + f.Path,
		URL: f.URL, ExpectedSize: f.Size, ExpectedSHA256: f.SHA256, Auth: f.Auth, MinFreeBytes: env.MinFree, Commit: true,
	}
	if job.StagingDir() != env.StagingDir {
		return item.Produced{}, markPermanent(fail(item.CodeInternal, nil, "the staging directory of %s is not where the producer writes", it.ID))
	}
	final := job.FinalPath()
	digest := f.SHA256
	if !w.haveArchive(final, f) {
		res, err := dl.Fetch(ctx, job, env.Checkpoints)
		if err != nil {
			return item.Produced{}, err
		}
		digest = res.SHA256
	}
	// The downloader hashed the stream; hash what is on the disk before anything is unpacked.
	sum, _, err := hashFile(final)
	if err != nil {
		return item.Produced{}, fail(doctor.SYN020, err, "cannot read back the archive %s", f.Path)
	}
	if f.SHA256 != "" && sum != f.SHA256 {
		return item.Produced{}, fail(doctor.SYN020, nil, "the archive %s on disk does not match its sha256", f.Path)
	}
	if digest == "" || f.SHA256 == "" {
		digest = sum
	}
	dest, dir, err := freshDir(env.StagingDir)
	if err != nil {
		return item.Produced{}, fail(item.CodeInternal, err, "cannot create the directory to unpack %s into", f.Path)
	}
	if _, err := w.ExtractArchive(ctx, final, dest); err != nil {
		return item.Produced{}, extractError(it, f, err)
	}
	part, err := sealTree("tree", dest, dir, "")
	if err != nil {
		return item.Produced{}, markPermanent(fail(doctor.SYN021, err, "the archive %s holds no usable tree", f.Path))
	}
	return item.Produced{
		TreeFrom: dir, TreeSHA256: part.TreeSHA256, Trust: wr.Trust,
		Source: map[string]any{"archive": f.Path, "archive_sha256": digest},
	}, nil
}

// haveArchive reports whether a verified archive of an earlier attempt is already in staging: the
// right size, and the right sha256 when the catalog pins one (an archive of a computed item is always
// fetched again).
func (w *Web) haveArchive(final string, f WebFile) bool {
	if f.SHA256 == "" {
		return false
	}
	sum, size, err := hashFile(final)
	return err == nil && sum == f.SHA256 && (f.Size == 0 || size == f.Size)
}

// freshDir creates the first unused extract.<n> directory below the staging directory.
func freshDir(staging string) (full, name string, err error) {
	for n := 1; n < 1000; n++ {
		name = extractPrefix + strconv.Itoa(n)
		full = filepath.Join(staging, name)
		if err = os.Mkdir(full, 0o755); err == nil {
			return full, name, nil
		}
		if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", errors.New("too many leftover extraction directories")
}

// extractError maps a failed extraction: a refused archive is the pin's fault (the bytes match it but
// hold something forbidden), a full disk is a skip, a missing tool is LAI-SYN-061.
func extractError(it plan.CatalogItem, f WebFile, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrNoXZ), CodeOf(err) == doctor.SYN061:
		return markPermanent(err)
	case isNoSpace(err):
		return &download.Error{Kind: download.KindDisk, Code: download.CodeDisk, Err: fmt.Errorf("download: no space left to unpack %s: %w", f.Path, err)}
	case errors.Is(err, ErrUnsafeArchive):
		return markPermanent(fail(CodeSourceDisagrees, err, "the archive %s of %s was refused", f.Path, it.ID))
	}
	return fail(CodeNoNewest, err, "the archive %s of %s could not be unpacked", f.Path, it.ID)
}

// fileBase is the last element of a URL's path, when it is a usable file name.
func fileBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	b := filepathBase(u.Path)
	if !manifest.SafeName(b) || !manifest.ValidFilePath(b) {
		return ""
	}
	return b
}

func filepathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
