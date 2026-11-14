package cloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestHandshakeBothPlanes(t *testing.T) {
	for name, tc := range map[string]struct {
		plane string
		want  string
	}{"localai plane": {"localai", PlaneLocalai}, "eab plane": {"eab", PlaneEAB}, "unknown plane is localai": {"", PlaneLocalai}} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.srv.Handshake.CommandPlane = tc.plane
			r.srv.Handshake.ClaimWaitMaxMs = 12000
			r.enroll()
			s, ok := r.link.Session()
			if !ok || s.Plane != tc.want || s.ClaimWaitMaxMs != 12000 || s.ServerNodeID == "" || len(s.Allow) != 17 || s.Concurrency != 1 {
				t.Fatalf("session %+v ok=%v", s, ok)
			}
			if r.link.CloudStatus().StateTransport != "endpoint" {
				t.Error("state_transport")
			}
		})
	}
}

func TestHandshake404MeansEABPlaneAndTheCapabilitiesFallback(t *testing.T) {
	r := newRig(t)
	r.srv.Handshake.Missing = true
	r.enroll()
	s, ok := r.link.Session()
	if !ok || s.Plane != PlaneEAB {
		t.Fatalf("session %+v", s)
	}
	if !r.alertActive(doctor.CLD056) {
		t.Error("LAI-CLD-056 not raised")
	}
	if r.link.hasFeature(FeatureState) || r.link.hasFeature(FeatureKey) {
		t.Error("an older cloud must not be credited with state.v1 or key.v1")
	}
	st := r.link.CloudStatus()
	if st.StateTransport != "capabilities" {
		t.Errorf("state_transport = %s", st.StateTransport)
	}
	// link.json remembers it.
	data, _ := os.ReadFile(r.linkPath)
	if !strings.Contains(string(data), `"command_plane": "eab"`) {
		t.Errorf("link.json: %s", data)
	}
	r.waitCount(fake.RouteHeartbeat, 1)
	// Re-checked every hour.
	r.waitTimers(2)
	n := r.srv.Count(fake.RouteConfig)
	r.step(time.Hour)
	r.waitCount(fake.RouteConfig, n+1)
}

func TestSessionIsNotAvailableUnlessLinked(t *testing.T) {
	r := newRig(t)
	if _, ok := r.link.Session(); ok {
		t.Fatal("a session before enroll")
	}
	r.enroll()
	if _, ok := r.link.Session(); !ok {
		t.Fatal("no session while linked")
	}
	r.stop()
	if _, ok := r.link.Session(); ok {
		t.Fatal("a session while stopped")
	}
}

func TestClaimResultFollowsTheStateTable(t *testing.T) {
	r := newRig(t)
	r.enroll()
	// The first heartbeat's answer must be applied before the claim fails: an answer applied after the
	// claim failure is a successful call and returns the link to linked (the test below pins that).
	r.waitHeartbeatApplied(1)
	// A claim failure moves linked to backoff; the next success returns to linked.
	r.link.ClaimResult(errors.New("connection reset"))
	r.waitState(StateBackoff)
	r.link.ClaimResult(nil)
	r.waitState(StateLinked)
	if r.link.CloudStatus().LastClaimAt == nil {
		t.Error("last_claim_at not set by a successful claim")
	}
	// A 403 disables the node: LAI-CLD-052, heartbeat every 300 s.
	r.link.ClaimDisabled()
	r.waitState(StateDisabled)
	r.waitAlert(doctor.CLD052, true)
	if !r.link.Disabled() {
		t.Error("Disabled() is false")
	}
	if got := r.link.heartbeatInterval(); got != 300*time.Second {
		t.Errorf("disabled heartbeat interval %v", got)
	}
	// A claim answering 200 again returns to linked and clears the alert.
	r.link.ClaimResult(nil)
	r.waitState(StateLinked)
	r.waitAlert(doctor.CLD052, false)
	if got := r.link.heartbeatInterval(); got != 15*time.Second {
		t.Errorf("interval after recovery %v", got)
	}
}

// A heartbeat answer is a successful call, so one applied after a claim failure returns backoff to
// linked (agent.md section 4.8.1: "next successful call"). The two loops are independent; whichever
// result is applied last wins. The fake holds the answer so the order is fixed instead of left to the
// scheduler, which is what made TestClaimResultFollowsTheStateTable flaky.
func TestHeartbeatAnswerAppliedAfterAClaimFailureReturnsToLinked(t *testing.T) {
	r := newRig(t)
	release := r.srv.Hold(fake.RouteHeartbeat)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1) // received by the cloud, not yet applied by the client
	r.link.ClaimResult(errors.New("connection reset"))
	if got := r.link.State(); got != StateBackoff {
		t.Fatalf("state %s after a claim failure, want backoff", got)
	}
	release()
	r.waitState(StateLinked)
	r.waitHeartbeatApplied(1)
	if got := r.link.State(); got != StateLinked {
		t.Errorf("state %s once the heartbeat answer was applied, want linked", got)
	}
}

func TestClaim401RevokesAndClaim404RegistersAgain(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitHeartbeatApplied(1)
	r.link.ClaimResult(&APIError{Status: 404})
	r.waitCount(fake.RouteRegister, 2)
	r.waitState(StateLinked)
	r.waitHeartbeatApplied(2) // the new link cycle's first heartbeat
	r.link.ClaimResult(&APIError{Status: 401})
	r.waitState(StateRevoked)
}

// A disabled node probes the claim on each heartbeat through the optional Probe method of the claim loop.
type probeLoop struct {
	started, stopped int
	startedCh        chan struct{} // receives one value per Start
	probed           chan struct{}
	mu               sync.Mutex
}

func (p *probeLoop) Start(context.Context) {
	p.mu.Lock()
	p.started++
	p.mu.Unlock()
	p.startedCh <- struct{}{}
}
func (p *probeLoop) Stop(context.Context) error {
	p.mu.Lock()
	p.stopped++
	p.mu.Unlock()
	return nil
}
func (p *probeLoop) Probe(context.Context) { p.probed <- struct{}{} }

func TestClaimLoopIsStartedWhenLinkedAndStoppedOnStop(t *testing.T) {
	pl := &probeLoop{startedCh: make(chan struct{}, 8), probed: make(chan struct{}, 8)}
	r := newRig(t, withDeps(func(d *Deps) { d.Claim = pl }))
	r.enroll()
	select {
	case <-pl.startedCh: // the supervisor starts the claim loop beside the heartbeat loop, in no fixed order
	case <-r.ctx.Done():
		t.Fatal("the claim loop was not started")
	}
	// The first heartbeat's answer is applied before the claim is disabled; applied afterwards it would
	// probe the claim, and the wait below could pass on that probe instead of on the 300 s heartbeat.
	r.waitHeartbeatApplied(1)
	pl.mu.Lock()
	started := pl.started
	pl.mu.Unlock()
	if started != 1 {
		t.Fatalf("claim loop started %d times", started)
	}
	r.link.ClaimDisabled()
	r.waitState(StateDisabled)
	r.waitTimers(2)
	r.step(300 * time.Second)
	select {
	case <-pl.probed: // the claim is re-checked on each heartbeat while disabled
	case <-r.ctx.Done():
		t.Fatal("the claim was not re-checked on a heartbeat while disabled")
	}
	r.stop()
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.stopped == 0 {
		t.Error("the claim loop was not stopped")
	}
}

func TestCatalogFetchVerifiesFilesAndSupportsConditionalRequests(t *testing.T) {
	r := newRig(t)
	r.srv.Catalog = &fake.Bundle{Version: "2026.11.02-1", ApprovedAt: "2026-11-02T14:00:00Z", Files: []fake.BundleFile{
		{Path: "CATALOG_VERSION", Content: "2026.11.02-1\n"}, {Path: "models.yaml", Content: "schema_version: 1\nmodels: []\n"},
	}}
	if _, err := r.link.FetchCatalog(r.ctx); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("not linked: %v", err)
	}
	r.enroll()
	b, err := r.link.FetchCatalog(r.ctx)
	if err != nil || b.Version != "2026.11.02-1" || len(b.Files) != 2 || b.BundleSHA256 != r.srv.Catalog.BundleSHA256() {
		t.Fatalf("bundle %+v %v", b, err)
	}
	if h := r.srv.RequestsFor(fake.RouteCatalog)[0].Header.Get("If-None-Match"); h != "" {
		t.Errorf("an unconditional fetch sent If-None-Match %q", h)
	}
	// Conditional: a 304 is ErrCatalogNotModified, with the sha as a strong ETag.
	_, err = r.link.FetchCatalogIfChanged(r.ctx, b.BundleSHA256)
	if !errors.Is(err, ErrCatalogNotModified) {
		t.Fatalf("conditional: %v", err)
	}
	if h := r.srv.RequestsFor(fake.RouteCatalog)[1].Header.Get("If-None-Match"); h != `"`+b.BundleSHA256+`"` {
		t.Errorf("If-None-Match = %q", h)
	}
	// A different etag gets the bundle.
	if b2, err := r.link.FetchCatalogIfChanged(r.ctx, strings.Repeat("0", 8)); err != nil || b2 == nil {
		t.Errorf("changed: %v", err)
	}
}

func TestCatalogFetchFailureModes(t *testing.T) {
	r := newRig(t)
	r.enroll()
	// 404 LAI-CLD-013: no approved catalog, the caller falls back to GitHub.
	if _, err := r.link.FetchCatalog(r.ctx); !errors.Is(err, ErrNoApprovedCatalog) {
		t.Fatalf("no approved catalog: %v", err)
	}
	// A file whose sha256 does not match its content is refused.
	r.srv.Catalog = &fake.Bundle{Version: "2026.11.02-1", Files: []fake.BundleFile{{Path: "models.yaml", Content: "x", SHA256: strings.Repeat("0", 32) + strings.Repeat("1", 32)}}}
	if _, err := r.link.FetchCatalog(r.ctx); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered file: %v", err)
	}
	// An answer without files is refused.
	r.srv.Catalog = &fake.Bundle{Version: "2026.11.02-1"}
	if _, err := r.link.FetchCatalog(r.ctx); err == nil {
		t.Fatal("empty bundle accepted")
	}
	// 5xx is an ordinary error.
	r.srv.Script(fake.RouteCatalog, fake.Reply{Status: 503})
	var ae *APIError
	if _, err := r.link.FetchCatalog(r.ctx); !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("503: %v", err)
	}
	// 401 revokes the link.
	r.srv.Script(fake.RouteCatalog, fake.Reply{Status: 401})
	_, _ = r.link.FetchCatalog(r.ctx)
	r.waitState(StateRevoked)
}

var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// validBundle reads the valid catalog of internal/catalog (its sha256 values are written as @sha:N@ so
// that no 64 hex digits sit in the repository) as the cloud would serve it.
func validBundle(t *testing.T) *fake.Bundle {
	t.Helper()
	src := filepath.Join("..", "catalog", "testdata", "valid")
	b := &fake.Bundle{Version: "2026.11.02-1", ApprovedAt: "2026-11-02T14:00:00Z"}
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		content := shaToken.ReplaceAllStringFunc(string(data), func(m string) string {
			return strings.Repeat(shaToken.FindStringSubmatch(m)[1], 64)
		})
		b.Files = append(b.Files, fake.BundleFile{Path: filepath.ToSlash(rel), Content: content})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The link is the catalog fetcher's cloud source: POST /catalog/refresh installs what the cloud approved.
func TestCatalogRefreshThroughTheLinkInstallsTheApprovedCatalog(t *testing.T) {
	r := newRig(t)
	bundle := validBundle(t)
	r.srv.Catalog = bundle
	r.enroll()
	dir := filepath.Join(t.TempDir(), "catalog")
	f := &catalog.Fetcher{Dir: dir, Cloud: r.link, TempDir: t.TempDir()}
	res, err := f.Refresh(r.ctx)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Source != catalog.SourceCloud || res.Catalog == nil || !res.Changed || res.Catalog.Version == "" {
		t.Fatalf("result %+v", res)
	}
	var want string
	for _, f := range bundle.Files {
		if f.Path == "CATALOG_VERSION" {
			want = f.Content
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "CATALOG_VERSION"))
	if err != nil || string(data) != want || want == "" {
		t.Errorf("installed version file: %q (want %q) %v", data, want, err)
	}
}
