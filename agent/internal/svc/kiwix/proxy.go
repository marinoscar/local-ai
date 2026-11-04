package kiwix

import (
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/proxy"
)

// LibraryPath is where the library is mounted on the agent. kiwix-serve runs with
// --urlRootLocation=/library, so the path passes through unchanged (companion-services.md section 3.5).
const LibraryPath = "/library/"

// LibraryCSP is set on every proxied HTML page: a ZIM page can never fetch anything that is not the
// library itself, even when the machine is online (companion-services.md section 6).
const LibraryCSP = "default-src 'self' 'unsafe-inline' data:; connect-src 'self'"

// LanguageHeader lets the Portal and the desktop say which language the error page should use.
const LanguageHeader = "X-LocalAI-UI-Language"

// ProxyOptions configures the /library/ mount.
type ProxyOptions struct {
	// Port is the kiwix-serve port (ports.kiwix); required.
	Port int
	// Logger receives the proxy warnings; nil means slog.Default.
	Logger *slog.Logger
	// Timeout is the response timeout; zero means 600 s.
	Timeout time.Duration
}

// NewProxy returns the handler of /library/: a reverse proxy to kiwix-serve on loopback with the path
// preserved, GET and HEAD only, no X-Forwarded-* added, the CSP on HTML responses and a bilingual 503
// page with LAI-SVC-002 when kiwix-serve does not answer. The language of that page is the
// X-LocalAI-UI-Language header, else Accept-Language, else what the server resolved from
// portal.language.
func NewProxy(o ProxyOptions) http.Handler {
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(o.Port)}
	h := proxy.New(proxy.Options{
		Target:          target,
		MethodPolicy:    proxy.ReadOnly,
		DownStatus:      http.StatusServiceUnavailable,
		DownCode:        api.CodeLibraryDown,
		ResponseTimeout: o.Timeout,
		ResponseHook:    addCSP,
		Logger:          o.Logger,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lang := requestLanguage(r); lang != "" {
			api.InfoFrom(r.Context()).Lang = lang
		}
		h.ServeHTTP(w, r)
	})
}

// Routes returns the route of the mount: GET only (a POST is 405 from the server, never forwarded),
// level none (the survival set), no route deadline (the proxy owns it).
func Routes(h http.Handler) []api.Route {
	return []api.Route{{Method: http.MethodGet, Pattern: LibraryPath, Auth: []api.Level{api.AuthNone}, Absolute: true, Handler: h}}
}

// addCSP sets the policy on HTML only; other types (images, scripts of the ZIM) are same-origin and
// stay as they are.
func addCSP(resp *http.Response) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))), "text/html") {
		resp.Header.Set("Content-Security-Policy", LibraryCSP)
		resp.Header.Set("X-Content-Type-Options", "nosniff")
	}
}

// requestLanguage returns "en" or "es" from the UI language header, else from Accept-Language (by
// q-value, ties by order), else "" (the caller keeps what the server resolved).
func requestLanguage(r *http.Request) string {
	if v := strings.ToLower(strings.TrimSpace(r.Header.Get(LanguageHeader))); v == "en" || v == "es" {
		return v
	}
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cs []cand
	for i, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if primary != "en" && primary != "es" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					f = 0
				}
				q = f
			}
		}
		if q > 0 {
			cs = append(cs, cand{primary, q, i})
		}
	}
	if len(cs) == 0 {
		return ""
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].q != cs[j].q {
			return cs[i].q > cs[j].q
		}
		return cs[i].pos < cs[j].pos
	})
	return cs[0].lang
}
