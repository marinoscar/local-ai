package resolve

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// pmEnv is a directory with the fake pmtiles tool, the tiny map it "extracts" and the files that steer
// it (mode) and record its calls (calls.log).
type pmEnv struct {
	t    *testing.T
	dir  string
	tool string
}

func newPMEnv(t *testing.T, mode string) *pmEnv {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []struct {
		src, dst string
		perm     os.FileMode
	}{{fakePMTilesPath, "pmtiles", 0o755}, {tinyPMTilesPath, "tiny.pmtiles", 0o644}} {
		if err := os.WriteFile(filepath.Join(dir, f.dst), readFixture(t, f.src), f.perm); err != nil {
			t.Fatal(err)
		}
	}
	e := &pmEnv{t: t, dir: dir, tool: filepath.Join(dir, "pmtiles")}
	e.mode(mode)
	return e
}

func (e *pmEnv) mode(m string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "mode"), []byte(m+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// calls is the recorded argv of every call, one string per call.
func (e *pmEnv) calls() []string {
	b, err := os.ReadFile(filepath.Join(e.dir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// fakePlanet is a fake planet host: it answers 200 to HEAD for the dates in have and 404 for the rest.
type fakePlanet struct {
	srv  *httptest.Server
	mu   sync.Mutex
	have map[string]bool
	hits []string
	// status, when set, answers every request with it.
	status int
}

func newFakePlanet(t *testing.T, dates ...string) *fakePlanet {
	t.Helper()
	p := &fakePlanet{have: map[string]bool{}}
	for _, d := range dates {
		p.have[d] = true
	}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".pmtiles")
		p.mu.Lock()
		p.hits = append(p.hits, r.Method+" "+date)
		ok, st := p.have[date], p.status
		p.mu.Unlock()
		switch {
		case st != 0:
			w.WriteHeader(st)
		case ok && r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePlanet) pattern() string { return p.srv.URL + "/{date}.pmtiles" }
func (p *fakePlanet) host() string    { return strings.TrimPrefix(strings.Split(p.srv.URL, "//")[1], "") }
func (p *fakePlanet) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hits...)
}

func pmCatalog(pattern string) plan.CatalogItem {
	zoom := 15
	it := contentItem("osm-central-america", "maps", catalog.ContentSource{
		Type: "pmtiles_extract", PlanetURLPattern: pattern, BBox: append([]float64(nil), tinyBBox...), MaxZoom: &zoom, Checksum: "computed",
	})
	it.Content.ExpectedSizeBytes = 123456789
	return it
}

// newPM builds a PMTiles over the fake tool and planet. The clock is fixedNow (2026-11-02 UTC).
func newPM(t *testing.T, e *pmEnv, p *fakePlanet) *PMTiles {
	t.Helper()
	c := *p.srv.Client()
	c.CheckRedirect = originCheckRedirect
	return &PMTiles{
		Env:  &Env{Online: func() bool { return true }, Now: func() time.Time { return fixedNow }, SyncHome: t.TempDir()},
		HTTP: &c, Tool: e.tool, RetryDelay: time.Millisecond,
	}
}

func TestPMTilesPicksTheNewestPlanetBuild(t *testing.T) {
	// today is 20261102; the build of Oct 30 is the first that exists: k = 3.
	p := newFakePlanet(t, "20261030", "20261020")
	e := newPMEnv(t, "ok")
	pm := newPM(t, e, p)
	got, err := pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "20261030" || got.SortKey != "20261030" {
		t.Fatalf("%+v", got)
	}
	want := []string{"HEAD 20261102", "HEAD 20261101", "HEAD 20261031", "HEAD 20261030"}
	if reqs := p.requests(); strings.Join(reqs, ",") != strings.Join(want, ",") {
		t.Fatalf("probes %v, want %v", reqs, want)
	}
}

func TestPMTilesTodaysBuildIsTakenAtOnce(t *testing.T) {
	p := newFakePlanet(t, "20261102", "20261101")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	got, err := pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if err != nil || got.Version != "20261102" || len(p.requests()) != 1 {
		t.Fatalf("%+v %v %v", got, err, p.requests())
	}
}

func TestPMTilesNoBuildInThirtyOneDaysIsLAISYN033(t *testing.T) {
	p := newFakePlanet(t, "20260101")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	_, err := pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if CodeOf(err) != doctor.SYN033 {
		t.Fatalf("%v", err)
	}
	if n := len(p.requests()); n != 31 {
		t.Fatalf("%d probes, want 31 (k = 0..30)", n)
	}
	// the build exactly 30 days ago still counts
	p2 := newFakePlanet(t, "20261003")
	if got, err := newPM(t, newPMEnv(t, "ok"), p2).Resolve(context.Background(), pmCatalog(p2.pattern())); err != nil || got.Version != "20261003" {
		t.Fatalf("k = 30: %+v %v", got, err)
	}
	p3 := newFakePlanet(t, "20261002")
	if _, err := newPM(t, newPMEnv(t, "ok"), p3).Resolve(context.Background(), pmCatalog(p3.pattern())); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("k = 31 must not be found: %v", err)
	}
}

func TestPMTilesServerErrorsMoveToTheNextDayButANetworkErrorStops(t *testing.T) {
	p := newFakePlanet(t, "20261031")
	p.status = http.StatusServiceUnavailable
	pm := newPM(t, newPMEnv(t, "ok"), p)
	if _, err := pm.Resolve(context.Background(), pmCatalog(p.pattern())); CodeOf(err) != doctor.SYN033 || len(p.requests()) != 31 {
		t.Fatalf("503s: %v after %d probes", err, len(p.requests()))
	}
	// a dead host: one attempt, then 033
	dead := newFakePlanet(t, "20261102")
	pattern := dead.pattern()
	dead.srv.Close()
	pm = newPM(t, newPMEnv(t, "ok"), dead)
	if _, err := pm.Resolve(context.Background(), pmCatalog(pattern)); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("dead host: %v", err)
	}
}

func TestParsePMTilesDryRun(t *testing.T) {
	cases := []struct {
		out  string
		want int64
		ok   bool
	}{
		{"Region tiles 5, result tile entries 5\nfetching 3 dirs, 17 chunks, 17 requests\ntiles to extract: 4.2 MB\n", 4_200_000, true},
		{"Tiles To Extract 1,234 bytes", 1234, true},
		{"tiles to extract: 3.9 MiB", 4089446, true},
		{"fetching 1 dirs\ntotal size 12 GB\n", 12_000_000_000, true},
		{"tiles to extract: 2 GiB", 2 << 30, true},
		{"tiles to extract: 900 kB", 900_000, true},
		{"tiles to extract: 17 B", 17, true},
		{"total size 5 MB\ntiles to extract: 1 MB", 1_000_000, true}, // "tiles to extract" wins
		{"nothing to see", 0, false},
		{"tiles to extract: many", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parsePMTilesDryRun(c.out)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %d %v, want %d %v", c.out, got, ok, c.want, c.ok)
		}
	}
}

func TestPMTilesPlansTheDryRunSize(t *testing.T) {
	p := newFakePlanet(t, "20261102")
	e := newPMEnv(t, "ok")
	pm := newPM(t, e, p)
	got, err := pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if err != nil || got.SizeBytes != 4_200_000 {
		t.Fatalf("%+v %v", got, err)
	}
	calls := e.calls()
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	wantArgs := "argv: [extract] [" + strings.Replace(p.pattern(), "{date}", "20261102", 1) + "] [/dev/null] [--bbox=-92.5,7,-77,18.6] [--maxzoom=15] [--dry-run]"
	if !strings.HasPrefix(calls[0], wantArgs) || !strings.Contains(calls[0], "home=["+pm.Env.SyncHome+"]") || !strings.Contains(calls[0], "proxy=[]") {
		t.Fatalf("call %q\nwant prefix %q", calls[0], wantArgs)
	}

	e.mode("dry_unparseable")
	got, err = pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if err != nil || got.SizeBytes != 123456789 {
		t.Fatalf("fallback to expected_size_bytes: %+v %v", got, err)
	}
	e.mode("extract_fail") // a dry run is only called "extract" with --dry-run; the mode applies to real runs
	if got, err = pm.Resolve(context.Background(), pmCatalog(p.pattern())); err != nil || got.SizeBytes != 4_200_000 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPMTilesMissingToolIsLAISYN061(t *testing.T) {
	p := newFakePlanet(t, "20261102")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	for name, tool := range map[string]string{
		"absent":         filepath.Join(t.TempDir(), "no-such-tool"),
		"not executable": filepath.Join(t.TempDir(), "plain"),
		"a directory":    t.TempDir(),
	} {
		if name == "not executable" {
			if err := os.WriteFile(tool, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		pm.Tool = tool
		_, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
		if re := asResolveErr(t, err); re.Code != doctor.SYN061 || !re.Permanent {
			t.Errorf("%s (engine): %+v", name, re)
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("%d requests with no tool installed", n)
	}
	// The planner still lists the item (the date needs no tool, the size is the catalog's), so the run
	// fails it with the code that says what to do rather than leaving it unresolved.
	pm.Tool = filepath.Join(t.TempDir(), "no-such-tool")
	got, err := pm.Resolve(context.Background(), pmCatalog(p.pattern()))
	if err != nil || got.Version != "20261102" || got.SizeBytes != 123456789 {
		t.Fatalf("planner: %+v %v", got, err)
	}
}

func TestPMTilesRefusesWhenOfflineOrWithAnUnusableEntry(t *testing.T) {
	p := newFakePlanet(t, "20261102")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	pm.Env.Online = func() bool { return false }
	_, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if re := asResolveErr(t, err); re.Code != doctor.SYN002 {
		t.Fatalf("%+v", re)
	}
	pm.Env.Online = func() bool { return true }
	mod := func(fn func(s *catalog.ContentSource)) plan.CatalogItem {
		it := pmCatalog(p.pattern())
		fn(&it.Content.Source)
		return it
	}
	zoom := 30
	cases := map[string]plan.CatalogItem{
		"no date": mod(func(s *catalog.ContentSource) { s.PlanetURLPattern = "https://planet.example.test/latest.pmtiles" }),
		"two dates": mod(func(s *catalog.ContentSource) {
			s.PlanetURLPattern = "https://planet.example.test/{date}/{date}.pmtiles"
		}),
		"http": mod(func(s *catalog.ContentSource) { s.PlanetURLPattern = "http://planet.example.test/{date}.pmtiles" }),
		"credentials": mod(func(s *catalog.ContentSource) {
			s.PlanetURLPattern = "https://" + url.UserPassword("a", "b").String() + "@planet.example.test/{date}.pmtiles"
		}),
		"query":       mod(func(s *catalog.ContentSource) { s.PlanetURLPattern = "https://planet.example.test/{date}.pmtiles?x=1" }),
		"short bbox":  mod(func(s *catalog.ContentSource) { s.BBox = []float64{1, 2, 3} }),
		"inverted":    mod(func(s *catalog.ContentSource) { s.BBox = []float64{10, 0, -10, 5} }),
		"off world":   mod(func(s *catalog.ContentSource) { s.BBox = []float64{-200, 0, 10, 5} }),
		"no maxzoom":  mod(func(s *catalog.ContentSource) { s.MaxZoom = nil }),
		"big maxzoom": mod(func(s *catalog.ContentSource) { s.MaxZoom = &zoom }),
		"wrong type":  mod(func(s *catalog.ContentSource) { s.Type = "url" }),
	}
	for name, it := range cases {
		_, err := pm.ResolveItem(context.Background(), it, plan.ItemPlan{})
		if re := asResolveErr(t, err); re.Code != doctor.SYN030 || !re.Permanent {
			t.Errorf("%s: %+v", name, re)
		}
	}
	bad := pmCatalog(p.pattern())
	bad.ID = "-rf"
	if _, err := pm.ResolveItem(context.Background(), bad, plan.ItemPlan{}); CodeOf(err) != doctor.SYN030 && asResolveErr(t, err).Code != doctor.SYN030 {
		t.Errorf("an id that looks like a flag: %v", err)
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestPMTilesResolveItemIsADirectoryBuildUnderTheExclusiveResource(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dir || res.Build == nil || res.Produce != nil || len(res.Files) != 0 || res.Exclusive != "pmtiles" ||
		res.Trust != manifest.TrustComputed || res.Version != "20261030" || res.SortKey != "20261030" || res.Resolver != "pmtiles_extract" {
		t.Fatalf("%+v", res)
	}
	if res.Source.Type != "pmtiles_extract" || res.Source.String("build_date") != "20261030" || res.Source.String("bbox") != "-92.5,7,-77,18.6" ||
		res.Source.String("planet_url") != strings.Replace(p.pattern(), "{date}", "20261030", 1) {
		t.Fatalf("source %+v", res.Source)
	}
}

func pmBuildTree(t *testing.T, pm *PMTiles, p *fakePlanet) (item.Resolution, string, error) {
	t.Helper()
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	return res, tree, res.Build(context.Background(), tree)
}

func TestPMTilesBuildExtractsAndNamesTheFileOnlyWhenDone(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	e := newPMEnv(t, "ok")
	pm := newPM(t, e, p)
	// leftovers of an earlier, killed run are overwritten (the CLI cannot resume)
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"osm-central-america.pmtiles.part", "osm-central-america.pmtiles"} {
		if err := os.WriteFile(filepath.Join(tree, f), []byte("junk of an earlier run"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := res.Build(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(tree, "osm-central-america.pmtiles"))
	if err != nil || !bytes.Equal(got, readFixture(t, tinyPMTilesPath)) {
		t.Fatalf("the extract is not in place: %v", err)
	}
	if ents, _ := os.ReadDir(tree); len(ents) != 1 {
		t.Fatalf("the tree holds more than the map: %v", ents)
	}
	calls := e.calls()
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	wantArgs := "argv: [extract] [" + strings.Replace(p.pattern(), "{date}", "20261030", 1) + "] [" + filepath.Join(tree, "osm-central-america.pmtiles.part") +
		"] [--bbox=-92.5,7,-77,18.6] [--maxzoom=15] [--download-threads=4] [--overfetch=0.05]"
	if !strings.HasPrefix(calls[0], wantArgs) || !strings.Contains(calls[0], "home=["+pm.Env.SyncHome+"]") || !strings.Contains(calls[0], "proxy=[]") {
		t.Fatalf("call %q\nwant prefix %q", calls[0], wantArgs)
	}
}

func TestPMTilesBuildCannotPassAnEarlierTrysBytesOff(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	pm := newPM(t, newPMEnv(t, "no_output"), p) // exits 0 and writes nothing
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	stale := filepath.Join(tree, "osm-central-america.pmtiles.part")
	if err := os.WriteFile(stale, []byte("bytes of a killed earlier try"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := res.Build(context.Background(), tree); asResolveErr(t, err).Code != doctor.SYN031 {
		t.Fatalf("a stale .part must not be taken for this try's output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "osm-central-america.pmtiles")); err == nil {
		t.Fatal("the stale bytes were named as the map")
	}
	if st, err := os.Stat(stale); err != nil || st.Size() != 0 {
		t.Fatalf("the stale .part is emptied, not deleted: %v %v", st, err)
	}
}

func TestPMTilesBuildThrottlesThroughTheProxyForThePlanetHostOnly(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	e := newPMEnv(t, "ok")
	pm := newPM(t, e, p)
	var hosts []string
	stopped := false
	pm.Env.Bucket = throttle.NewBucket(1<<20, nil)
	pm.Env.StartProxy = func(ctx context.Context, b throttle.Bucket, allow []string) (string, func(), error) {
		hosts = allow
		return "127.0.0.1:43210", func() { stopped = true }, nil
	}
	if _, _, err := pmBuildTree(t, pm, p); err != nil {
		t.Fatal(err)
	}
	if want := strings.Split(p.srv.URL, "//")[1]; len(hosts) != 1 || !strings.HasPrefix(want, hosts[0]) {
		t.Fatalf("allowlist %v, want the planet host %s", hosts, want)
	}
	if !stopped {
		t.Fatal("the proxy was left running")
	}
	if calls := e.calls(); len(calls) != 1 || !strings.Contains(calls[0], "proxy=[http://127.0.0.1:43210]") {
		t.Fatalf("calls %v", calls)
	}
}

func TestPMTilesBuildRetriesOnceThenFailsWithLAISYN031(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	e := newPMEnv(t, "extract_fail_once")
	pm := newPM(t, e, p)
	_, tree, err := pmBuildTree(t, pm, p)
	if err != nil {
		t.Fatalf("the second attempt works: %v", err)
	}
	if n := len(e.calls()); n != 2 {
		t.Fatalf("%d extract calls, want 2", n)
	}
	if _, err := os.Stat(filepath.Join(tree, "osm-central-america.pmtiles")); err != nil {
		t.Fatal(err)
	}

	e2 := newPMEnv(t, "extract_fail")
	_, tree, err = pmBuildTree(t, newPM(t, e2, p), p)
	re := asResolveErr(t, err)
	if re.Code != doctor.SYN031 || re.Permanent || !strings.Contains(re.Error(), "exited 1") || !strings.Contains(re.Error(), "could not fetch the planet header") {
		t.Fatalf("%v", err)
	}
	if n := len(e2.calls()); n != 2 {
		t.Fatalf("%d extract calls, want at most 2 per run", n)
	}
	if ents, _ := os.ReadDir(tree); len(ents) > 1 {
		t.Fatalf("a failed extract left files: %v", ents)
	}

	// exit 0 with no file is a failure too
	_, _, err = pmBuildTree(t, newPM(t, newPMEnv(t, "no_output"), p), p)
	if re := asResolveErr(t, err); re.Code != doctor.SYN031 {
		t.Fatalf("%v", err)
	}
}

func TestPMTilesBuildTimeoutAndCancel(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	pm := newPM(t, newPMEnv(t, "slow"), p)
	pm.ExtractTimeout = 400 * time.Millisecond
	start := time.Now()
	_, _, err := pmBuildTree(t, pm, p)
	if re := asResolveErr(t, err); re.Code != doctor.SYN031 || !strings.Contains(re.Error(), "took longer") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("the timeout took %s", time.Since(start))
	}

	e := newPMEnv(t, "slow")
	pm = newPM(t, e, p)
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start = time.Now()
	err = res.Build(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancel must come back as the context's error, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("the cancel took %s", time.Since(start))
	}
	if n := len(e.calls()); n != 1 {
		t.Fatalf("a cancel must not start another attempt: %d calls", n)
	}
}

// pmRunnerFunc adapts a function to execx.Runner.
type pmRunnerFunc func(ctx context.Context, c execx.Cmd) (execx.Result, error)

func (f pmRunnerFunc) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) { return f(ctx, c) }

func TestPMTilesLendsTheStagingTreeToTheServiceUserAndTakesItBack(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	e := newPMEnv(t, "ok")
	pm := newPM(t, e, p)

	root := t.TempDir()
	staging := filepath.Join(root, ".staging")
	tree := filepath.Join(staging, "osm-central-america", "20261030", "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{staging, filepath.Join(staging, "osm-central-america"), filepath.Join(staging, "osm-central-america", "20261030")} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var chowns []string
	var mu sync.Mutex
	pm.Credential = &execx.Credential{UID: 1234, GID: 5678}
	pm.Euid, pm.Egid = func() int { return 0 }, func() int { return 0 }
	pm.Chown = func(path string, uid, gid int) error {
		mu.Lock()
		defer mu.Unlock()
		chowns = append(chowns, strings.TrimPrefix(path, root)+"="+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		return nil
	}
	var during struct {
		cred       *execx.Credential
		stagingX   bool
		treeMode   os.FileMode
		proxyEmpty bool
	}
	pm.Env.Runner = pmRunnerFunc(func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		during.cred = c.Credential
		st, _ := os.Stat(staging)
		during.stagingX = st.Mode().Perm()&0o001 != 0
		ts, _ := os.Stat(tree)
		during.treeMode = ts.Mode().Perm()
		// write the part file like the tool would
		part := c.Args[2]
		if err := os.WriteFile(part, readFixture(t, tinyPMTilesPath), 0o644); err != nil {
			return execx.Result{}, err
		}
		return execx.Result{}, nil
	})
	res, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Build(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if during.cred == nil || *during.cred != *pm.Credential {
		t.Fatalf("the tool did not run as the service user: %+v", during.cred)
	}
	if !during.stagingX || during.treeMode != 0o755 {
		t.Fatalf("while the tool ran: .staging searchable=%v tree=%v", during.stagingX, during.treeMode)
	}
	for _, d := range []string{staging, filepath.Join(staging, "osm-central-america"), filepath.Join(staging, "osm-central-america", "20261030")} {
		if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v after the extract, want 0700 again", strings.TrimPrefix(d, root), st.Mode().Perm())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	wantFirst := "/.staging/osm-central-america/20261030/tree=1234:5678"
	if len(chowns) < 3 || chowns[0] != wantFirst || chowns[len(chowns)-1] != "/.staging/osm-central-america/20261030/tree=0:0" {
		t.Fatalf("chowns %v", chowns)
	}
	if !pmContains(chowns, "/.staging/osm-central-america/20261030/tree/osm-central-america.pmtiles=0:0") {
		t.Fatalf("the finished map was not handed back: %v", chowns)
	}
}

func TestPMTilesDoesNotTouchOwnershipWithoutACredential(t *testing.T) {
	p := newFakePlanet(t, "20261030")
	pm := newPM(t, newPMEnv(t, "ok"), p)
	pm.Chown = func(string, int, int) error { t.Error("chown called"); return nil }
	pm.Chmod = func(string, os.FileMode) error { t.Error("chmod called"); return nil }
	if _, _, err := pmBuildTree(t, pm, p); err != nil {
		t.Fatal(err)
	}
}

func pmContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- the validator ------------------------------------------------------------------------------------

// stagedMap puts the tiny map in a directory under the name the resolver gave it.
func stagedMap(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "osm-central-america.pmtiles"), readFixture(t, tinyPMTilesPath), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pmValidatorSetup(t *testing.T, mode string) (*item.Validators, item.Target, *pmEnv) {
	t.Helper()
	p := newFakePlanet(t, "20261030")
	e := newPMEnv(t, mode)
	pm := newPM(t, e, p)
	vals := item.NewValidators()
	if err := pm.RegisterValidator(vals); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.ResolveItem(context.Background(), pmCatalog(p.pattern()), plan.ItemPlan{}); err != nil {
		t.Fatal(err)
	}
	return vals, item.Target{
		Meta:      item.Meta{ItemID: "osm-central-america", Kind: "maps", SourceType: "pmtiles_extract"},
		StagedDir: stagedMap(t), Dir: true,
	}, e
}

func TestPMTilesValidatorAcceptsAGoodExtract(t *testing.T) {
	vals, target, e := pmValidatorSetup(t, "ok")
	if err := vals.Run(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	calls := e.calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "argv: [verify] ["+filepath.Join(target.StagedDir, "osm-central-america.pmtiles")+"]") ||
		!strings.HasPrefix(calls[1], "argv: [show] ["+filepath.Join(target.StagedDir, "osm-central-america.pmtiles")+"] [--header-json]") {
		t.Fatalf("calls %v", calls)
	}
}

func TestPMTilesValidatorRejectsWithLAISYN023(t *testing.T) {
	cases := map[string]string{
		"verify fails":    "verify_fail",
		"wrong tile type": "wrong_type",
		"bbox too small":  "small_bbox",
	}
	for name, mode := range cases {
		t.Run(name, func(t *testing.T) {
			vals, target, _ := pmValidatorSetup(t, mode)
			err := vals.Run(context.Background(), target)
			var ve *item.ValidationError
			if !errors.As(err, &ve) || ve.Code != doctor.SYN023 || ve.Validator != PMTilesValidatorName {
				t.Fatalf("want a validation error LAI-SYN-023, got %v", err)
			}
		})
	}
}

func TestPMTilesValidatorChecksTheFilesAndOnlyWhatItBuilt(t *testing.T) {
	vals, target, e := pmValidatorSetup(t, "ok")
	// no map, two maps, another name: all rejected without running the tool
	for name, setup := range map[string]func(dir string){
		"no map":     func(dir string) { _ = os.Remove(filepath.Join(dir, "osm-central-america.pmtiles")) },
		"second map": func(dir string) { _ = os.WriteFile(filepath.Join(dir, "other.pmtiles"), []byte("x"), 0o644) },
		"renamed": func(dir string) {
			_ = os.Rename(filepath.Join(dir, "osm-central-america.pmtiles"), filepath.Join(dir, "x.pmtiles"))
		},
	} {
		tg := target
		tg.StagedDir = stagedMap(t)
		setup(tg.StagedDir)
		var ve *item.ValidationError
		if err := vals.Run(context.Background(), tg); !errors.As(err, &ve) || ve.Code != doctor.SYN023 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(e.calls()) != 0 {
		t.Fatalf("the tool ran for a directory that was already wrong: %v", e.calls())
	}
	// an item this process did not build (a copy from a drive) is not checked, and neither is another type
	other := target
	other.ItemID = "from-a-drive"
	if err := vals.Run(context.Background(), other); err != nil {
		t.Fatalf("unbuilt item: %v", err)
	}
	notMap := target
	notMap.Meta.SourceType = "url"
	if err := vals.Run(context.Background(), notMap); err != nil {
		t.Fatalf("other source type: %v", err)
	}
	if len(e.calls()) != 0 {
		t.Fatalf("the tool ran: %v", e.calls())
	}
}

func TestCheckPMTilesHeader(t *testing.T) {
	want := [4]float64{-92.5, 7.0, -77.0, 18.6}
	hdr := func(typ string, minLon, minLat, maxLon, maxLat string) string {
		return `{"tile_type":` + typ + `,"min_lon_e7":` + minLon + `,"min_lat_e7":` + minLat + `,"max_lon_e7":` + maxLon + `,"max_lat_e7":` + maxLat + `}`
	}
	ok := []string{
		hdr(`"mvt"`, "-925000000", "70000000", "-770000000", "186000000"),
		hdr(`"MVT"`, "-925000000", "70000000", "-770000000", "186000000"),
		hdr(`1`, "-925000000", "70000000", "-770000000", "186000000"),
		// larger than asked
		hdr(`"mvt"`, "-930000000", "60000000", "-760000000", "190000000"),
		// short by less than 0.01 degree on every edge
		hdr(`"mvt"`, "-924900001", "70099999", "-770099999", "185900001"),
		// degrees instead of E7
		`{"tile_type":"mvt","min_lon":-92.5,"min_lat":7,"max_lon":-77,"max_lat":18.6}`,
	}
	for _, h := range ok {
		if err := checkPMTilesHeader([]byte(h), want); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	bad := []string{
		hdr(`"png"`, "-925000000", "70000000", "-770000000", "186000000"),
		hdr(`2`, "-925000000", "70000000", "-770000000", "186000000"),
		`{"min_lon_e7":-925000000,"min_lat_e7":70000000,"max_lon_e7":-770000000,"max_lat_e7":186000000}`,
		// short by more than 0.01 degree: west, south, east, north
		hdr(`"mvt"`, "-924800000", "70000000", "-770000000", "186000000"),
		hdr(`"mvt"`, "-925000000", "70200000", "-770000000", "186000000"),
		hdr(`"mvt"`, "-925000000", "70000000", "-770200000", "186000000"),
		hdr(`"mvt"`, "-925000000", "70000000", "-770000000", "185800000"),
		`{"tile_type":"mvt"}`,
		`not json`,
		``,
	}
	for _, h := range bad {
		err := checkPMTilesHeader([]byte(h), want)
		var pe *PMTilesError
		if !errors.As(err, &pe) || pe.DoctorCode() != doctor.SYN023 {
			t.Errorf("%q: want LAI-SYN-023, got %v", h, err)
		}
	}
}
