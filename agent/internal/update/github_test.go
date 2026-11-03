package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func (r *rig) source() Source { return r.runner("0.0.1").Source }

func TestResolveNewestOfTheChannel(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	r.release("0.10.0")
	r.release("0.9.5")
	pre := r.release("1.0.0-rc.1")
	pre.Prerelease = true
	draft := r.release("2.0.0")
	draft.Draft = true

	src := r.source()
	rel, err := src.Resolve(context.Background(), "")
	if err != nil || rel.Version.String() != "0.10.0" || rel.Tag != "node-v0.10.0" {
		t.Fatalf("stable: got %+v, %v", rel, err)
	}
	if !rel.Has("SHA256SUMS") || !rel.Has("aiagent-linux-amd64") || rel.Has("nothing") {
		t.Fatalf("assets: %v", rel.Assets)
	}

	src.Channel = ChannelPrerelease
	rel, err = src.Resolve(context.Background(), "")
	if err != nil || rel.Version.String() != "1.0.0-rc.1" || !rel.Prerelease {
		t.Fatalf("prerelease channel: got %+v, %v", rel, err)
	}
}

func TestResolveExplicitVersion(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	pre := r.release("0.3.0-rc.1")
	pre.Prerelease = true
	draft := r.release("0.4.0")
	draft.Draft = true
	src := r.source()

	rel, err := src.Resolve(context.Background(), "0.2.0")
	if err != nil || rel.Version.String() != "0.2.0" {
		t.Fatalf("explicit: %+v, %v", rel, err)
	}
	// An explicit pre-release is allowed whatever the channel says: the person asked for it by name.
	if rel, err = src.Resolve(context.Background(), "0.3.0-rc.1"); err != nil || !rel.Prerelease {
		t.Fatalf("explicit prerelease: %+v, %v", rel, err)
	}
	for name, want := range map[string]string{
		"0.9.9":     doctor.AGT021, // never released
		"0.4.0":     doctor.AGT021, // a draft is not a release
		"v0.2.0":    doctor.AGT020, // not a version
		"../0.2.0":  doctor.AGT020,
		"0.2.0/../": doctor.AGT020,
	} {
		if _, err := src.Resolve(context.Background(), name); CodeOf(err) != want {
			t.Errorf("%q: want %s, got %v", name, want, err)
		}
	}
}

func TestResolveNoRelease(t *testing.T) {
	r := newRig(t, "0.1.0")
	if _, err := r.source().Resolve(context.Background(), ""); CodeOf(err) != doctor.AGT021 {
		t.Fatalf("want AGT021, got %v", err)
	}
}

func TestResolveDoesNotTrustTheTagOfTheAnswer(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	// The server answers the request for 0.2.0 with another release.
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if strings.HasSuffix(req.URL.Path, "/tags/node-v0.2.0") {
			_, _ = w.Write([]byte(`{"tag_name":"node-v0.1.5","assets":[]}`))
			return true
		}
		return false
	}
	if _, err := r.source().Resolve(context.Background(), "0.2.0"); CodeOf(err) != doctor.AGT021 {
		t.Fatalf("a mismatching tag must be refused, got %v", err)
	}
}

func TestSourceRefusesBadConfiguration(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	for name, mutate := range map[string]func(*Source){
		"repo with a path":    func(s *Source) { s.Repo = "owner/../other" },
		"repo with a scheme":  func(s *Source) { s.Repo = "https://evil/x" },
		"repo with one part":  func(s *Source) { s.Repo = "justone" },
		"plain http api":      func(s *Source) { s.APIBase = strings.Replace(r.srv.URL, "https:", "http:", 1) },
		"plain http download": func(s *Source) { s.DownloadBase = strings.Replace(r.srv.URL, "https:", "http:", 1) },
		"credentials in url":  func(s *Source) { s.APIBase = "https://user:pw@127.0.0.1" },
	} {
		src := r.source()
		mutate(&src)
		if _, err := src.Resolve(context.Background(), ""); err == nil {
			t.Errorf("%s: want a refusal", name)
		} else if code := CodeOf(err); code != doctor.AGT020 && code != doctor.AGT099 {
			t.Errorf("%s: unexpected code %s", name, code)
		}
	}
	if r.requests() != 0 {
		t.Fatalf("a refused configuration must not send a request, saw %d", r.requests())
	}
}

func TestRedirectsStayOnGitHubOverHTTPS(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) }))
	defer plain.Close()

	for name, target := range map[string]string{
		"to plain http":       plain.URL,
		"to an unlisted host": "https://localhost:1/",
	} {
		t.Run(name, func(t *testing.T) {
			r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
				http.Redirect(w, req, target, http.StatusFound)
				return true
			}
			// The redirect is refused before any connection to the target is made.
			_, err := r.source().Resolve(context.Background(), "")
			if err == nil {
				t.Fatal("a redirect off GitHub must be refused")
			}
			if CodeOf(err) != doctor.AGT009 {
				t.Fatalf("want AGT009, got %v", err)
			}
		})
	}
}

func TestRedirectToAllowedHostIsFollowed(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	moved := false
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if strings.HasSuffix(req.URL.Path, "/releases") && !moved {
			moved = true
			http.Redirect(w, req, r.srv.URL+req.URL.Path+"?per_page=30&again=1", http.StatusFound)
			return true
		}
		return false
	}
	if rel, err := r.source().Resolve(context.Background(), ""); err != nil || rel.Version.String() != "0.2.0" {
		t.Fatalf("an https redirect to an allowed host must work: %v", err)
	}
}

func TestResolveNetworkProblems(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	src := r.source()
	r.srv.Close() // nobody listens any more: the node has no route to GitHub
	if _, err := src.Resolve(context.Background(), ""); CodeOf(err) != doctor.AGT023 {
		t.Fatalf("no internet must be AGT023, got %v", err)
	}

	r2 := newRig(t, "0.1.0")
	r2.release("0.2.0")
	r2.srv.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "rate limited", http.StatusForbidden)
		return true
	}
	if _, err := r2.source().Resolve(context.Background(), ""); CodeOf(err) != doctor.AGT009 {
		t.Fatalf("an HTTP error is AGT009, got %v", err)
	}
	r2.srv.Hook = func(w http.ResponseWriter, _ *http.Request) bool {
		_, _ = w.Write([]byte("not json"))
		return true
	}
	if _, err := r2.source().Resolve(context.Background(), ""); CodeOf(err) != doctor.AGT009 {
		t.Fatalf("an unreadable answer is AGT009, got %v", err)
	}

	// A certificate the client does not trust is a failed update, never "offline".
	r3 := newRig(t, "0.1.0")
	r3.release("0.2.0")
	src3 := r3.source()
	src3.Client = &http.Client{}
	if _, err := src3.Resolve(context.Background(), ""); CodeOf(err) != doctor.AGT009 {
		t.Fatalf("an untrusted certificate must be AGT009, got %v", err)
	}
}

func TestResolveSendsNoCredentials(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	var seen http.Header
	r.srv.Hook = func(_ http.ResponseWriter, req *http.Request) bool {
		seen = req.Header.Clone()
		return false
	}
	src := r.source()
	src.UserAgent = "aiagent-test"
	if _, err := src.Resolve(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "" || seen.Get("Cookie") != "" || seen.Get("User-Agent") != "aiagent-test" {
		t.Fatalf("headers: %v", seen)
	}
}
