package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fakegithub stands in for api.github.com: the release record of a tag, with scripted rate limits.
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	releases map[string][]map[string]any // by "repo@tag": the assets
	limited  int                         // answer a rate limit this many times first
	reset    func() time.Time
	status   int
	reqs     []string
	headers  []http.Header
}

func newFakeGitHub(r *webRig) *fakeGitHub {
	g := &fakeGitHub{releases: map[string][]map[string]any{}}
	g.srv = r.serve(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.reqs = append(g.reqs, req.Method+" "+req.URL.Path)
		g.headers = append(g.headers, req.Header.Clone())
		if g.limited > 0 {
			g.limited--
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(g.reset().Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if g.status != 0 {
			w.WriteHeader(g.status)
			return
		}
		// /repos/<owner>/<name>/releases/tags/<tag>
		parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/repos/"), "/")
		if len(parts) != 5 || parts[2] != "releases" || parts[3] != "tags" {
			http.NotFound(w, req)
			return
		}
		assets, ok := g.releases[parts[0]+"/"+parts[1]+"@"+parts[4]]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": parts[4], "assets": assets})
	}))
	return g
}

func (g *fakeGitHub) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reqs)
}

func ghItem(asset, sha string, extract *bool) plan.CatalogItem {
	return urlContent("map-assets", "maps", catalog.ContentSource{
		Type: "github_release", Repo: "org-alpha/map-assets", Tag: "v1.2.3", Asset: asset, SHA256: sha, Extract: extract,
	})
}

func ghSetup(t *testing.T) (*webRig, *fakeGitHub, *GitHubRelease) {
	r := newWebRig(t)
	g := newFakeGitHub(r)
	r.w.GitHubAPI = g.srv.URL
	return r, g, &GitHubRelease{W: r.w}
}

func fakeAsset(name, digest string, size int64) map[string]any {
	a := map[string]any{"name": name, "size": size, "state": "uploaded", "browser_download_url": "https://github.example.test/org-alpha/map-assets/releases/download/v1.2.3/" + name}
	if digest != "" {
		a["digest"] = digest
	}
	return a
}

// A published digest that matches the pin is cross-checked ("catalog+github_digest"); the size is the
// asset's own; the version is the tag and the download is the asset's URL. The release record is asked
// for with the GitHub media type and no credentials.
func TestGitHubAssetFound(t *testing.T) {
	_, g, gr := ghSetup(t)
	sha := strings.Repeat("ab", 32)
	g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("other.zip", "", 1), fakeAsset("assets.tar.gz", "sha256:"+strings.ToUpper(sha), 4321)}
	got, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, truePtr()))
	if err != nil {
		t.Fatal(err)
	}
	f := got.Files[0]
	if got.Version != "v1.2.3" || !got.Extract || got.Trust != manifest.TrustPinned || got.SortKey != "2026.11.02-1" {
		t.Errorf("result = %+v", got)
	}
	if f.Path != "assets.tar.gz" || f.Size != 4321 || f.SHA256 != sha || f.SHA256Source != "catalog+github_digest" || f.Auth ||
		f.URL != "https://github.example.test/org-alpha/map-assets/releases/download/v1.2.3/assets.tar.gz" {
		t.Errorf("file = %+v", f)
	}
	if got.Meta["repo"] != "org-alpha/map-assets" || got.Meta["tag"] != "v1.2.3" || got.Meta["asset"] != "assets.tar.gz" {
		t.Errorf("meta = %v", got.Meta)
	}
	h := g.headers[0]
	if h.Get("Accept") != "application/vnd.github+json" || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
}

// Without a digest in the record the pin stands alone ("catalog"); a digest of another algorithm is
// not a cross-check either.
func TestGitHubNoDigest(t *testing.T) {
	_, g, gr := ghSetup(t)
	sha := strings.Repeat("cd", 32)
	g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("a.zip", "", 10), fakeAsset("b.zip", "sha512:abcd", 10)}
	for _, name := range []string{"a.zip", "b.zip"} {
		got, err := gr.Lookup(context.Background(), ghItem(name, sha, nil))
		if err != nil {
			t.Fatal(err)
		}
		if got.Files[0].SHA256Source != "catalog" || got.Extract {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// An asset the release does not have is LAI-SYN-033 (transient: the maintainer may still upload it);
// a release the source does not have is LAI-SYN-030; a published digest that is not the pin is
// LAI-SYN-021 and permanent.
func TestGitHubFailures(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	t.Run("asset missing", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("other.zip", "", 1)}
		_, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
	})
	t.Run("asset still uploading", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		a := fakeAsset("assets.tar.gz", "", 1)
		a["state"] = "starter"
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{a}
		_, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
	})
	t.Run("no such release", func(t *testing.T) {
		_, _, gr := ghSetup(t)
		_, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN030, true)
	})
	t.Run("server error", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		g.status = http.StatusBadGateway
		_, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
	})
	t.Run("digest mismatch", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("assets.tar.gz", "sha256:"+strings.Repeat("ef", 32), 1)}
		got, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN021, true)
		if len(got.Files) != 0 {
			t.Errorf("a refused item lists files: %+v", got)
		}
	})
	t.Run("no usable download URL", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		a := fakeAsset("assets.tar.gz", "", 1)
		a["browser_download_url"] = "http://github.example.test/x"
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{a}
		_, err := gr.Lookup(context.Background(), ghItem("assets.tar.gz", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
	})
	t.Run("unpinned or unusable", func(t *testing.T) {
		_, g, gr := ghSetup(t)
		for name, it := range map[string]plan.CatalogItem{
			"no sha":    ghItem("a.zip", "", nil),
			"bad tag":   urlContent("x", "maps", catalog.ContentSource{Type: "github_release", Repo: "org-alpha/r", Tag: "../v", Asset: "a.zip", SHA256: sha}),
			"bad repo":  urlContent("x", "maps", catalog.ContentSource{Type: "github_release", Repo: "no slash", Tag: "v1", Asset: "a.zip", SHA256: sha}),
			"bad asset": ghItem("a/b.zip", sha, nil),
		} {
			_, err := gr.Lookup(context.Background(), it)
			if CodeOf(err) != doctor.SYN033 || !isPermanent(err) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if g.count() != 0 {
			t.Errorf("%d requests for unusable items", g.count())
		}
	})
}

// A 403 with X-RateLimit-Remaining: 0 is transient. A reset within 15 minutes is waited for and the
// request is made once more; a later reset, or a limit that is still there afterwards, fails the item
// for this run (LAI-SYN-033, not permanent) without sleeping 15 minutes first.
func TestGitHubRateLimit(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	t.Run("lifts in time", func(t *testing.T) {
		r, g, gr := ghSetup(t)
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("a.zip", "", 5)}
		g.limited, g.reset = 1, func() time.Time { return fixedNow.Add(90 * time.Second) }
		got, err := gr.Lookup(context.Background(), ghItem("a.zip", sha, nil))
		if err != nil || got.Files[0].Size != 5 {
			t.Fatalf("%+v, %v", got, err)
		}
		if s := r.slept(); len(s) != 1 || s[0] != 90*time.Second {
			t.Errorf("waited %v, want [90s]", s)
		}
		if g.count() != 2 {
			t.Errorf("requests = %d, want 2", g.count())
		}
	})
	t.Run("too far away", func(t *testing.T) {
		r, g, gr := ghSetup(t)
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("a.zip", "", 5)}
		g.limited, g.reset = 1, func() time.Time { return fixedNow.Add(time.Hour) }
		_, err := gr.Lookup(context.Background(), ghItem("a.zip", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
		if len(r.slept()) != 0 || g.count() != 1 {
			t.Errorf("waited %v, requests %d", r.slept(), g.count())
		}
	})
	t.Run("still limited", func(t *testing.T) {
		r, g, gr := ghSetup(t)
		g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("a.zip", "", 5)}
		g.limited, g.reset = 9, func() time.Time { return fixedNow.Add(time.Minute) }
		_, err := gr.Lookup(context.Background(), ghItem("a.zip", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
		if len(r.slept()) != 1 || g.count() != 2 {
			t.Errorf("waited %v, requests %d", r.slept(), g.count())
		}
	})
	t.Run("a plain 403 is not a rate limit", func(t *testing.T) {
		r, g, gr := ghSetup(t)
		g.status = http.StatusForbidden
		_, err := gr.Lookup(context.Background(), ghItem("a.zip", sha, nil))
		codeIs(t, err, doctor.SYN033, false)
		if len(r.slept()) != 0 {
			t.Errorf("waited %v", r.slept())
		}
	})
}

// One release record serves every asset of the release: two items cost one request.
func TestGitHubReleaseRecordIsCached(t *testing.T) {
	_, g, gr := ghSetup(t)
	g.releases["org-alpha/map-assets@v1.2.3"] = []map[string]any{fakeAsset("a.zip", "", 1), fakeAsset("b.zip", "", 2)}
	for _, n := range []string{"a.zip", "b.zip", "a.zip"} {
		if _, err := gr.Lookup(context.Background(), ghItem(n, strings.Repeat("ab", 32), nil)); err != nil {
			t.Fatal(err)
		}
	}
	if g.count() != 1 {
		t.Errorf("requests = %d, want 1", g.count())
	}
}
