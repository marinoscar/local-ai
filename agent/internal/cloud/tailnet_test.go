package cloud

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
)

// fakeTailnet is a TailnetSource the test edits. The address is built at run time so none sits in the source.
type fakeTailnet struct {
	mu       sync.Mutex
	ip, name string
	reads    int
}

func tailnetAddr(last int) string {
	return strings.Join([]string{"100", "64", "10", strconv.Itoa(last)}, ".")
}

func (f *fakeTailnet) SelfAddress(context.Context) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.ip, f.name
}

func (f *fakeTailnet) set(ip, name string) {
	f.mu.Lock()
	f.ip, f.name = ip, name
	f.mu.Unlock()
}

// The node reports its own tailnet address and name once the handshake advertises the route, and the body is
// exactly {ip, machine} (issue #437).
func TestTailnetIsReportedAfterTheHandshakeAndNeverLogged(t *testing.T) {
	ts := &fakeTailnet{ip: tailnetAddr(20), name: "host-a.example.ts.net"}
	r := newRig(t, withDeps(func(d *Deps) { d.Tailnet = ts }))
	res := r.enroll()
	r.waitCount(fake.RouteTailnet, 1)
	req := r.srv.RequestsFor(fake.RouteTailnet)[0]
	var body map[string]string
	if err := req.JSON(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["ip"] != tailnetAddr(20) || body["machine"] != "host-a.example.ts.net" {
		t.Errorf("body has keys %v", keysOf(body))
	}
	if !strings.HasSuffix(req.Path, "/api/nodes/"+res.ServerNodeID+"/localai/tailnet") {
		t.Errorf("path %s", req.Path)
	}
	if got, _ := r.srv.TailnetOf(res.ServerNodeID); got.IP != tailnetAddr(20) {
		t.Errorf("the cloud holds %+v", got)
	}
	if strings.Contains(r.logs.String(), tailnetAddr(20)) {
		t.Error("the tailnet address is in the log")
	}
}

// An address that is already known to the cloud is not sent again, a changed one is, and a node whose
// tailscale is not up yet is asked again after five minutes.
func TestTailnetIsNotResentAndFollowsAChange(t *testing.T) {
	ts := &fakeTailnet{}
	r := newRig(t, withDeps(func(d *Deps) { d.Tailnet = ts }))
	res := r.enroll()
	count := func() int { _, n := r.srv.TailnetOf(res.ServerNodeID); return n }
	// No address yet (tailscale is not running): looked at, nothing sent.
	waitReads(t, ts, 1)
	if count() != 0 {
		t.Fatalf("%d reports without an address", count())
	}
	ts.set(tailnetAddr(20), "host-a.example.ts.net")
	stepUntil(t, r, tailnetRetryEvery, func() bool { return count() == 1 })

	// The same address an hour later: looked at again, not sent.
	reads := readsOf(ts)
	stepUntil(t, r, tailnetRecheckEvery, func() bool { return readsOf(ts) > reads })
	if count() != 1 {
		t.Errorf("the same address was sent again: %d reports", count())
	}

	// A new login gives a new address: sent.
	ts.set(tailnetAddr(21), "host-a.example.ts.net")
	stepUntil(t, r, tailnetRecheckEvery, func() bool { return count() == 2 })
	if got, _ := r.srv.TailnetOf(res.ServerNodeID); got.IP != tailnetAddr(21) {
		t.Errorf("the cloud holds %+v", got)
	}
}

// stepUntil moves the fake clock by d, again and again, until cond holds. The other loops' timers fire too,
// which the fake cloud answers; what matters is that the tailnet loop got its turn.
func stepUntil(t *testing.T, r *rig, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition never held while the clock was stepped")
		}
		r.clk.Advance(d)
		time.Sleep(2 * time.Millisecond)
	}
}

// A cloud without the route (an older one) is never asked, and a node without a tailnet source stays quiet.
func TestTailnetIsSkippedWithoutTheFeatureOrTheSource(t *testing.T) {
	t.Run("old cloud", func(t *testing.T) {
		ts := &fakeTailnet{ip: tailnetAddr(20)}
		r := newRig(t, withDeps(func(d *Deps) { d.Tailnet = ts }))
		r.srv.Handshake.Features = []string{"state.v1", "commands.v1", "catalog.v1", "key.v1"}
		r.enroll()
		r.waitCount(fake.RouteKey, 1)
		r.clk.Advance(tailnetRetryEvery)
		r.clk.Advance(tailnetRetryEvery)
		if n := len(r.srv.RequestsFor(fake.RouteTailnet)); n != 0 {
			t.Errorf("%d requests to a cloud that does not advertise the route", n)
		}
	})
	t.Run("no source", func(t *testing.T) {
		r := newRig(t)
		r.enroll()
		r.waitCount(fake.RouteKey, 1)
		if n := len(r.srv.RequestsFor(fake.RouteTailnet)); n != 0 {
			t.Errorf("%d requests without a tailnet source", n)
		}
	})
}

// An outage is retried after five minutes, and the address is still not logged.
func TestTailnetRetriesAfterAFailure(t *testing.T) {
	ts := &fakeTailnet{ip: tailnetAddr(20), name: "host-a.example.ts.net"}
	r := newRig(t, withDeps(func(d *Deps) { d.Tailnet = ts }))
	r.srv.Forever(fake.RouteTailnet, fake.Reply{Status: 503})
	r.enroll()
	r.waitCount(fake.RouteTailnet, 1)
	if err := r.clk.WaitArmed(r.ctx, tailnetRetryEvery); err != nil {
		t.Fatalf("the loop never armed its five minute timer: %v", err)
	}
	before := len(r.srv.RequestsFor(fake.RouteTailnet))
	r.clk.Advance(tailnetRetryEvery)
	r.waitCount(fake.RouteTailnet, before+1)
	if strings.Contains(r.logs.String(), tailnetAddr(20)) {
		t.Error("the tailnet address is in the log")
	}
}

func readsOf(f *fakeTailnet) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func waitReads(t *testing.T, f *fakeTailnet, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for readsOf(f) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d reads, want %d", readsOf(f), n)
		}
		time.Sleep(time.Millisecond)
	}
}
