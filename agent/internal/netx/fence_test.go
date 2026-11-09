package netx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	cases := []struct {
		network, addr string
		want          bool
	}{
		{"tcp", "127.0.0.1:80", true},
		{"tcp4", "127.255.0.9:7070", true},
		{"tcp", "[::1]:80", true},
		{"tcp6", "[::ffff:127.0.0.1]:80", true},
		{"tcp", "localhost:8080", true},
		{"tcp", "LOCALHOST.:8080", true},
		{"unix", "/run/localai/agent.sock", true},
		{"unixgram", "/run/systemd/notify", true},
		{"unix", "anything", true},
		{"tcp", "8.8.8.8:53", false},
		{"tcp", "[2606:4700::1111]:443", false},
		{"tcp", "huggingface.co:443", false},
		{"tcp", "localhost.example.org:80", false},
		{"tcp", "127.0.0.1.example.org:80", false},
		{"tcp", "10.0.0.5:22", false},
		{"tcp", "198.51.100.7:80", false},
		{"tcp", "0.0.0.0:80", false},
		{"udp", "1.1.1.1:53", false},
		{"tcp", "127.0.0.1", false}, // no port: unreadable, refused
		{"tcp", "", false},
		{"ip4:icmp", "127.0.0.1", false},
	}
	for _, c := range cases {
		if got := Allowed(c.network, c.addr); got != c.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", c.network, c.addr, got, c.want)
		}
	}
}

// refuses a dial to inner that would fail the test if it ran: the fence must decide first.
func neverDial(t *testing.T) DialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		t.Helper()
		t.Error("the fence let a dial through to the inner dialer")
		return nil, errors.New("must not be called")
	}
}

func TestFenceRefusesBeforeDNSAndCounts(t *testing.T) {
	f := NewFence()
	dial := f.Wrap("tester", neverDial(t))
	lease := f.Acquire()
	defer lease.Release()

	ctx := WithCaller(context.Background(), "sync.resolver")
	for _, addr := range []string{"huggingface.co:443", "8.8.8.8:53", "[2606:4700::1111]:443", "cloud.example.test:443"} {
		_, err := dial(ctx, "tcp", addr)
		var fe *FencedError
		if !errors.Is(err, ErrFenced) || !errors.As(err, &fe) {
			t.Fatalf("dial %s: %v, want a fence refusal", addr, err)
		}
		if fe.Addr != addr || fe.Caller != "sync.resolver" {
			t.Errorf("error = %+v", fe)
		}
	}
	// The default caller applies when the context names none.
	if _, err := dial(context.Background(), "tcp", "huggingface.co:443"); !errors.Is(err, ErrFenced) {
		t.Fatal(err)
	}
	rep := lease.Release()
	if rep.Blocked != 5 {
		t.Errorf("blocked = %d, want 5", rep.Blocked)
	}
	want := []Blocked{
		{"huggingface.co:443", "sync.resolver", 1}, {"8.8.8.8:53", "sync.resolver", 1},
		{"[2606:4700::1111]:443", "sync.resolver", 1}, {"cloud.example.test:443", "sync.resolver", 1},
		{"huggingface.co:443", "tester", 1},
	}
	if len(rep.Dials) != len(want) {
		t.Fatalf("dials = %+v", rep.Dials)
	}
	for i, w := range want {
		if rep.Dials[i] != w {
			t.Errorf("dial %d = %+v, want %+v", i, rep.Dials[i], w)
		}
	}
	if f.Active() {
		t.Error("the fence is still on after the only holder released it")
	}
}

func TestFenceAllowsLoopbackAndUnix(t *testing.T) {
	f := NewFence()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ul, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	go func() {
		for {
			c, err := ul.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	lease := f.Acquire()
	defer lease.Release()
	dial := f.Wrap("tester", nil)
	c, err := dial(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("loopback dial refused: %v", err)
	}
	_ = c.Close()
	u, err := dial(context.Background(), "unix", sock)
	if err != nil {
		t.Fatalf("unix dial refused: %v", err)
	}
	_ = u.Close()
	if rep := lease.Release(); rep.Blocked != 0 {
		t.Errorf("blocked = %d for allowed dials", rep.Blocked)
	}
}

func TestFenceOffAllowsEverything(t *testing.T) {
	f := NewFence()
	called := false
	dial := f.Wrap("x", func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("inner reached")
	})
	if _, err := dial(context.Background(), "tcp", "huggingface.co:443"); err == nil || errors.Is(err, ErrFenced) || !called {
		t.Fatalf("with the fence off the inner dial must run: called=%v err=%v", called, err)
	}
}

func TestFenceNestingAndReports(t *testing.T) {
	f := NewFence()
	dial := f.Wrap("n", neverDial(t))
	try := func(addr string) { _, _ = dial(context.Background(), "tcp", addr) }

	outer := f.Acquire()
	try("a.example.test:443")
	inner := f.Acquire()
	try("b.example.test:443")
	try("b.example.test:443")
	if r := inner.Release(); r.Blocked != 2 || len(r.Dials) != 1 || r.Dials[0].Addr != "b.example.test:443" || r.Dials[0].Count != 2 {
		t.Errorf("inner report = %+v", r)
	}
	if !f.Active() {
		t.Fatal("releasing the inner holder turned the fence off for the outer one")
	}
	try("c.example.test:443")
	r := outer.Release()
	if r.Blocked != 4 || len(r.Dials) != 3 {
		t.Errorf("outer report = %+v", r)
	}
	if f.Active() {
		t.Error("the fence is on after both holders released")
	}
	// A second release returns the same report and does not underflow.
	if r2 := outer.Release(); r2.Blocked != 4 {
		t.Errorf("second release = %+v", r2)
	}
	if f.Active() {
		t.Error("a double release turned the fence on")
	}
	// A new run starts clean.
	again := f.Acquire()
	if r := again.Release(); r.Blocked != 0 {
		t.Errorf("a new hold starts with %d", r.Blocked)
	}
}

func TestFenceReleasedByDeferOnPanic(t *testing.T) {
	f := NewFence()
	func() {
		defer func() { _ = recover() }()
		l := f.Acquire()
		defer l.Release()
		panic("boom")
	}()
	if f.Active() {
		t.Fatal("a panic left the fence on")
	}
}

func TestProbeDialsAreRefusedButNotCounted(t *testing.T) {
	f := NewFence()
	dial := f.Wrap("drill.fence", neverDial(t))
	l := f.Acquire()
	_, err := dial(WithProbe(context.Background()), "tcp", "huggingface.co:443")
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("a probe dial must still be refused: %v", err)
	}
	if r := l.Release(); r.Blocked != 0 || len(r.Dials) != 0 {
		t.Errorf("a probe dial was counted: %+v", r)
	}
}

func TestFenceBoundsDistinctDestinations(t *testing.T) {
	f := NewFence()
	dial := f.Wrap("flood", neverDial(t))
	l := f.Acquire()
	for i := 0; i < maxDistinct+50; i++ {
		_, _ = dial(context.Background(), "tcp", "h"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Duration(i).String()+".example.test:443")
	}
	r := l.Release()
	if r.Blocked != maxDistinct+50 {
		t.Errorf("blocked = %d, want the exact count %d", r.Blocked, maxDistinct+50)
	}
	if len(r.Dials) > maxDistinct {
		t.Errorf("listed %d destinations, at most %d", len(r.Dials), maxDistinct)
	}
}

func TestFenceConcurrentUse(t *testing.T) {
	f := NewFence()
	dial := f.Wrap("c", neverDial(t))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l := f.Acquire()
				_, _ = dial(context.Background(), "tcp", "x.example.test:443")
				l.Release()
			}
		}()
	}
	wg.Wait()
	if f.Active() {
		t.Fatal("the fence is on after every holder released it")
	}
}

func TestCallerNamesAreCleaned(t *testing.T) {
	ctx := WithCaller(context.Background(), "sync; resolver\n")
	if got := CallerFrom(ctx); strings.ContainsAny(got, "; \n") {
		t.Errorf("caller = %q", got)
	}
	if got := CallerFrom(WithCaller(context.Background(), "")); got != "" {
		t.Errorf("an empty caller set %q", got)
	}
	if got := CallerFrom(WithCaller(context.Background(), strings.Repeat("a", 500))); len(got) != maxCallerLen {
		t.Errorf("caller length = %d", len(got))
	}
	inner := WithCaller(WithCaller(context.Background(), "outer"), "inner")
	if CallerFrom(inner) != "inner" {
		t.Error("the nearer caller must win")
	}
}

func TestResolvedNameOutsideLoopbackIsRefused(t *testing.T) {
	// Control sees the resolved address: a dial the fence's string check let through (a name taken for
	// localhost) still cannot leave the machine.
	f := NewFence()
	l := f.Acquire()
	defer l.Release()
	d := f.Dialer(time.Second)
	// 192.0.2.1 is TEST-NET-1; the address string is not loopback, so Control refuses it before connecting.
	_, err := d(context.Background(), "tcp", "192.0.2.1:9")
	if err == nil || !strings.Contains(err.Error(), "refused a name") {
		t.Fatalf("err = %v", err)
	}
	if r := l.Release(); r.Blocked != 1 {
		t.Errorf("blocked = %d", r.Blocked)
	}
}
