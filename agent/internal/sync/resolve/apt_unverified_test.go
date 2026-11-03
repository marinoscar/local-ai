package resolve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// These tests cover sync.apt_unverified_repos at the resolver (issue #274): an extra apt repository
// with no keyring is refused (LAI-SYN-060) or taken without checking the vendor signature, in the
// narrowest way apt allows. apt itself is a scripted fake, so nothing dials and nothing needs root.

const (
	unvTailscale = "tailscale-archive-keyring.gpg"
	unvNvidia    = "nvidia-cuda-archive-keyring.gpg"
	unvID        = "kit-debs-test-amd64"
)

// unvRepos are the two extra repositories of the real kit-debs items (invented hosts): one ordinary,
// one flat (suite "/", no components), both over https.
func unvRepos() []catalog.ExtraRepo {
	return []catalog.ExtraRepo{
		{Name: "tailscale", URI: "https://pkgs.example.test/stable/ubuntu", Suite: "test", Components: []string{"main"}, Key: unvTailscale},
		{Name: "nvidia-cuda", URI: "https://dl.example.test/compute/cuda/repos/x86_64/", Suite: "/", Key: unvNvidia},
	}
}

func unvItem(repos ...catalog.ExtraRepo) plan.CatalogItem {
	it := aptItem(unvID, "test", "amd64", "pkg-c")
	it.Content.Source.ExtraRepos = repos
	return it
}

type unvRig struct {
	a    *AptClosure
	run  *fake.Runner
	tree string
}

// newUnvRig builds an AptClosure over a scripted apt-get. have lists the keyring files that exist; the
// Ubuntu archive keyring always does. allow is the policy function (nil: the zero value).
func newUnvRig(t *testing.T, allow func() bool, online bool, have ...string) *unvRig {
	t.Helper()
	const bin = "/usr/bin/apt-get"
	sync := t.TempDir()
	tree := filepath.Join(sync, "apt", unvID)
	opts := aptOptions(tree, "amd64", "localai", "")
	line := uriLine("https://archive.ubuntu.com/ubuntu/pool/main/p/pkg-c/pkg-c_1.0_amd64.deb", "pkg-c_1.0_amd64.deb", 10, "SHA256", "pkg-c")
	run := fake.New()
	run.Expect(fake.Key(bin, append(append([]string{}, opts...), "update")...), execx.Result{}, nil)
	run.Expect(fake.Key(bin, append(append([]string{}, opts...), "--print-uris", "--yes", "install", "pkg-c")...), execx.Result{Stdout: []byte(line + "\n")}, nil)
	a := &AptClosure{
		Env: &Env{
			Runner: run, Lookup: func(string) (string, error) { return bin, nil }, Online: func() bool { return online }, SyncHome: sync, Now: func() time.Time { return fixedNow },
			StartProxy: func(context.Context, throttle.Bucket, []string) (string, func(), error) { return "", func() {}, nil },
		},
		Keyrings: func(name string) ([]byte, error) {
			if name == ubuntuKeyring {
				return []byte("ubuntu-keyring"), nil
			}
			for _, h := range have {
				if h == name {
					return []byte("vendor-keyring"), nil
				}
			}
			return nil, errors.New("not embedded")
		},
		Credential:           &execx.Credential{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
		User:                 "localai",
		Euid:                 func() int { return 1000 },
		AllowUnverifiedRepos: allow,
	}
	return &unvRig{a: a, run: run, tree: tree}
}

func (r *unvRig) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.tree, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func yes() bool { return true }
func no() bool  { return false }

func TestAptUnverifiedReposRefuseKeepsFailingClosed(t *testing.T) {
	for name, allow := range map[string]func() bool{"refuse": no, "a nil policy (the zero value)": nil} {
		t.Run(name, func(t *testing.T) {
			r := newUnvRig(t, allow, true)
			_, err := r.a.Resolve(context.Background(), unvItem(unvRepos()...))
			if CodeOf(err) != doctor.SYN060 || !strings.Contains(err.Error(), "keyring "+unvTailscale+" is not available") {
				t.Fatalf("err = %v (code %q), want LAI-SYN-060 about the missing keyring", err, CodeOf(err))
			}
			if n := len(r.run.Calls()); n != 0 {
				t.Errorf("%d commands ran before the refusal", n)
			}
			if _, statErr := os.Stat(r.tree); statErr == nil {
				t.Errorf("a refused item must not leave an apt tree behind")
			}
		})
	}
	// An item with no extra repositories is not affected by the setting, whatever it says.
	r := newUnvRig(t, no, true)
	c, err := r.a.Closure(context.Background(), unvItem())
	if err != nil || len(c.Unverified) != 0 {
		t.Fatalf("no extra repos: (%+v, %v)", c, err)
	}
}

func TestAptUnverifiedReposAllowTrustsOnlyTheExtraRepositories(t *testing.T) {
	r := newUnvRig(t, yes, true)
	c, err := r.a.Closure(context.Background(), unvItem(unvRepos()...))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nvidia-cuda", "tailscale"}; !reflect.DeepEqual(c.Unverified, want) {
		t.Fatalf("Unverified = %v, want %v", c.Unverified, want)
	}
	if n := len(r.run.Calls()); n != 2 {
		t.Errorf("%d apt calls, want update and print-uris", n)
	}

	extra := r.read(t, "etc/apt/sources.list.d/extra.sources")
	want := "Types: deb\nURIs: https://pkgs.example.test/stable/ubuntu\nSuites: test\nComponents: main\nArchitectures: amd64\nTrusted: yes\n\n" +
		"Types: deb\nURIs: https://dl.example.test/compute/cuda/repos/x86_64/\nSuites: /\nArchitectures: amd64\nTrusted: yes\n"
	if extra != want {
		t.Errorf("extra.sources:\n%s\nwant:\n%s", extra, want)
	}
	if strings.Contains(extra, "Signed-By") {
		t.Error("a repository taken without a keyring must not name a Signed-By")
	}

	// The Ubuntu archive is still verified with its keyring and never trusted blindly.
	ubuntu := r.read(t, "etc/apt/sources.list.d/ubuntu.sources")
	if !strings.Contains(ubuntu, "Signed-By: "+filepath.Join(r.tree, "etc/apt/trusted.gpg.d", ubuntuKeyring)) || strings.Contains(ubuntu, "Trusted") {
		t.Errorf("ubuntu.sources:\n%s", ubuntu)
	}
	// No global switch: apt is not told to accept unauthenticated packages anywhere.
	for i, call := range r.run.Calls() {
		joined := strings.ToLower(strings.Join(call.Args, " "))
		for _, bad := range []string{"unauth", "insecure", "allowdowngrade", "::trusted ", "=trusted", "trusted=yes"} {
			if strings.Contains(joined, bad) {
				t.Errorf("call %d carries a global trust option (%s): %s", i, bad, joined)
			}
		}
	}
	// Only the Ubuntu keyring was written; there is nothing for the vendor repositories.
	ents, err := os.ReadDir(filepath.Join(r.tree, "etc/apt/trusted.gpg.d"))
	if err != nil || len(ents) != 1 || ents[0].Name() != ubuntuKeyring {
		t.Errorf("trusted.gpg.d = %v (%v)", ents, err)
	}
	conf := r.read(t, "etc/apt/apt.conf")
	if strings.Contains(strings.ToLower(conf), "unauth") {
		t.Errorf("apt.conf = %q", conf)
	}
}

func TestAptUnverifiedReposOnlyTheMissingKeyringIsUnverified(t *testing.T) {
	r := newUnvRig(t, yes, true, unvTailscale)
	c, err := r.a.Closure(context.Background(), unvItem(unvRepos()...))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nvidia-cuda"}; !reflect.DeepEqual(c.Unverified, want) {
		t.Fatalf("Unverified = %v, want %v", c.Unverified, want)
	}
	extra := r.read(t, "etc/apt/sources.list.d/extra.sources")
	if !strings.Contains(extra, "Signed-By: "+filepath.Join(r.tree, "etc/apt/trusted.gpg.d", unvTailscale)+"\n") || strings.Count(extra, "Trusted: yes") != 1 {
		t.Errorf("a repository that has its keyring stays verified:\n%s", extra)
	}
	if _, err := os.Stat(filepath.Join(r.tree, "etc/apt/trusted.gpg.d", unvTailscale)); err != nil {
		t.Errorf("the available keyring was not written: %v", err)
	}
}

func TestAptUnverifiedReposNeedHTTPS(t *testing.T) {
	repos := unvRepos()
	repos[0].URI = "http://pkgs.example.test/stable/ubuntu"
	r := newUnvRig(t, yes, true)
	_, err := r.a.Resolve(context.Background(), unvItem(repos...))
	if CodeOf(err) != doctor.SYN060 || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("err = %v (code %q), want LAI-SYN-060 about https", err, CodeOf(err))
	}
	if n := len(r.run.Calls()); n != 0 {
		t.Errorf("%d commands ran", n)
	}
	// With its keyring the same http repository is not touched by this rule (it is verified).
	r = newUnvRig(t, yes, true, unvTailscale, unvNvidia)
	if c, err := r.a.Closure(context.Background(), unvItem(repos...)); err != nil || len(c.Unverified) != 0 {
		t.Fatalf("verified http repository: (%+v, %v)", c, err)
	}
}

func TestAptUnverifiedReposOfflineMakesNoCall(t *testing.T) {
	for name, allow := range map[string]func() bool{"allow": yes, "refuse": no} {
		t.Run(name, func(t *testing.T) {
			r := newUnvRig(t, allow, false)
			item := unvItem(unvRepos()...)
			if _, err := r.a.Resolve(context.Background(), item); !errors.Is(err, ErrOffline) {
				t.Fatalf("Resolve offline: %v", err)
			}
			if _, err := r.a.Produce(context.Background(), Request{Item: item, Root: t.TempDir(), Version: "v"}); !errors.Is(err, ErrOffline) {
				t.Fatalf("Produce offline: %v", err)
			}
			if n := len(r.run.Calls()); n != 0 {
				t.Errorf("%d commands ran while offline", n)
			}
			if _, statErr := os.Stat(r.tree); statErr == nil {
				t.Error("an offline refusal must not create the apt tree")
			}
		})
	}
}

func TestAptUnverifiedReposAreRecordedInTheProduct(t *testing.T) {
	root := t.TempDir()
	cases := map[string]struct {
		have   []string
		wantIn []string // source.unverified_repos
	}{
		"both repositories unverified": {nil, []string{"nvidia-cuda", "tailscale"}},
		"one unverified":               {[]string{unvTailscale}, []string{"nvidia-cuda"}},
		"all verified":                 {[]string{unvTailscale, unvNvidia}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newUnvRig(t, yes, true, tc.have...)
			item := unvItem(unvRepos()...)
			planned, err := r.a.Resolve(context.Background(), item)
			if err != nil {
				t.Fatal(err)
			}
			// The present version is the one wanted, so Produce stages nothing (Keep) and still reports
			// the facts the manifest records.
			prod, err := r.a.Produce(context.Background(), Request{Item: item, Root: root, RootID: "internal", Version: planned.Version, PresentDir: t.TempDir(), PresentVersion: planned.Version})
			if err != nil || !prod.Keep {
				t.Fatalf("produce: (%+v, %v)", prod, err)
			}
			got, has := prod.Source["unverified_repos"]
			if tc.wantIn == nil {
				if has || len(prod.Notices) != 0 {
					t.Fatalf("a verified kit must carry no warning: %+v", prod)
				}
				return
			}
			if !has || !reflect.DeepEqual(got, tc.wantIn) {
				t.Fatalf("source.unverified_repos = %v, want %v", got, tc.wantIn)
			}
			if len(prod.Notices) != 1 || prod.Notices[0].Code != doctor.SYN063 || !strings.Contains(prod.Notices[0].Message, tc.wantIn[0]) {
				t.Fatalf("notices = %+v, want exactly one LAI-SYN-063", prod.Notices)
			}
		})
	}
}

func TestAptUnverifiedReposSettingIsReadOnEveryClosure(t *testing.T) {
	allow := true
	r := newUnvRig(t, func() bool { return allow }, true)
	item := unvItem(unvRepos()...)
	first, err := r.a.Closure(context.Background(), item)
	if err != nil || len(first.Unverified) != 2 {
		t.Fatalf("allow: (%+v, %v)", first, err)
	}
	// Switching to refuse takes effect at once, even inside the closure cache's lifetime.
	allow = false
	if _, err := r.a.Resolve(context.Background(), item); CodeOf(err) != doctor.SYN060 {
		t.Fatalf("after refuse: code %q (err %v)", CodeOf(err), err)
	}
	// And back to allow: the cached closure of the allow policy is still valid.
	allow = true
	again, err := r.a.Closure(context.Background(), item)
	if err != nil || again != first {
		t.Fatalf("back to allow: (%v, %v)", again, err)
	}
}

func TestAptFlatRepositoryRules(t *testing.T) {
	ok := catalog.ExtraRepo{Name: "cuda", URI: "https://dl.example.test/cuda/", Suite: "/", Key: unvNvidia}
	mod := func(f func(*catalog.ExtraRepo)) aptSource {
		r := ok
		f(&r)
		return aptSource{Codename: "resolute", Arch: "amd64", Extra: []catalog.ExtraRepo{r}}
	}
	for name, tc := range map[string]struct {
		src aptSource
		ok  bool
	}{
		"the NVIDIA shape: suite / and no components": {mod(func(*catalog.ExtraRepo) {}), true},
		"a flat suite below a path":                   {mod(func(r *catalog.ExtraRepo) { r.Suite = "debs/stable/" }), true},
		"a flat suite with components":                {mod(func(r *catalog.ExtraRepo) { r.Components = []string{"main"} }), false},
		"a flat suite that climbs":                    {mod(func(r *catalog.ExtraRepo) { r.Suite = "../x/" }), false},
		"a flat suite with a space":                   {mod(func(r *catalog.ExtraRepo) { r.Suite = "a b/" }), false},
		"a normal suite without components":           {mod(func(r *catalog.ExtraRepo) { r.Suite = "stable" }), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.src.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && CodeOf(err) != doctor.SYN060 {
				t.Errorf("code = %q", CodeOf(err))
			}
		})
	}
	flat := aptSource{Codename: "resolute", Arch: "amd64", Extra: []catalog.ExtraRepo{ok}}
	if got, want := flat.renderExtraSources("/k"), "Types: deb\nURIs: https://dl.example.test/cuda/\nSuites: /\nArchitectures: amd64\nSigned-By: /k/"+unvNvidia+"\n"; got != want {
		t.Errorf("flat stanza:\n%s\nwant:\n%s", got, want)
	}
}
