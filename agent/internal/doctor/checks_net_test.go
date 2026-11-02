package doctor_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

type fakeServe struct{ rs []doctor.Result }

func (f *fakeServe) ServeResults() []doctor.Result { return f.rs }

type fakeReach struct {
	rs  []doctor.Result
	ran int
}

func (f *fakeReach) CheckServeReachable(context.Context) []doctor.Result {
	f.ran++
	return f.rs
}

type fakeConn struct{ rs []doctor.Result }

func (f *fakeConn) ConnectivityResults() []doctor.Result { return f.rs }

func TestNetChecksShapeAndSkipMissingSources(t *testing.T) {
	all := doctor.NetChecks(doctor.NetSources{Serve: &fakeServe{}, Reachable: &fakeReach{}, Connectivity: &fakeConn{}})
	got := map[string]doctor.Check{}
	for _, c := range all {
		got[c.ID] = c
		if c.Area != "NET" {
			t.Errorf("%s: area %s", c.ID, c.Area)
		}
	}
	for id, fast := range map[string]bool{doctor.CheckNetServeState: true, doctor.CheckNetServeReachable: false, doctor.CheckNetConnectivity: true} {
		c, ok := got[id]
		if !ok || c.Fast != fast {
			t.Errorf("%s: present=%v fast=%v, want fast=%v", id, ok, c.Fast, fast)
		}
	}
	if c := got[doctor.CheckNetServeReachable]; c.Timeout < doctor.ServeReachableTimeout+time.Second {
		t.Errorf("serve_reachable timeout %v must exceed the 30 s the request is given", c.Timeout)
	}
	if n := len(doctor.NetChecks(doctor.NetSources{})); n != 0 {
		t.Errorf("no sources gave %d checks", n)
	}
	if n := len(doctor.NetChecks(doctor.NetSources{Connectivity: &fakeConn{}})); n != 1 {
		t.Errorf("one source gave %d checks", n)
	}
}

func TestNetChecksRunThroughTheRegistry(t *testing.T) {
	reach := &fakeReach{rs: []doctor.Result{{Code: doctor.NET008}}}
	src := doctor.NetSources{
		Serve:        &fakeServe{rs: []doctor.Result{{Code: doctor.NET005, Details: map[string]any{"command": "sudo tailscale serve --tcp=18080 off"}}}},
		Reachable:    reach,
		Connectivity: &fakeConn{rs: []doctor.Result{{Code: doctor.NET011}, {Code: doctor.NET013}}},
	}
	reg := doctor.NewRegistry()
	if err := doctor.RegisterNetChecks(reg, src); err != nil {
		t.Fatal(err)
	}
	if err := doctor.RegisterNetChecks(reg, src); err == nil {
		t.Error("registering twice must fail")
	}

	// The fast run (the alert source, every 60 s) never runs the slow check.
	rep, err := reg.Run(context.Background(), doctor.RunOptions{Areas: []string{"NET"}, FastOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range rep.Results {
		codes = append(codes, r.Code)
		if r.Code == doctor.NET005 && r.Check != doctor.CheckNetServeState {
			t.Errorf("NET-005 came from %s", r.Check)
		}
	}
	if want := []string{doctor.NET005, doctor.NET011, doctor.NET013}; !reflect.DeepEqual(sorted(codes), want) {
		t.Errorf("fast codes = %v, want %v", codes, want)
	}
	if reach.ran != 0 {
		t.Error("the slow reachability check ran in a fast pass")
	}

	// An explicit run includes it.
	rep, err = reg.Run(context.Background(), doctor.RunOptions{Areas: []string{"NET"}})
	if err != nil {
		t.Fatal(err)
	}
	var sawReach bool
	for _, r := range rep.Results {
		sawReach = sawReach || (r.Code == doctor.NET008 && r.Check == doctor.CheckNetServeReachable)
	}
	if !sawReach || reach.ran != 1 {
		t.Errorf("explicit run: reach=%v ran=%d", sawReach, reach.ran)
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
