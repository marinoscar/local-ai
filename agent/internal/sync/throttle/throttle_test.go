package throttle

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// epoch is read once and shared between what the tests feed in and what they expect.
var epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

// vclock is a virtual clock: After(d) moves the time to now+d at once and returns a fired channel,
// so a "sleep" costs no real time. It is used from one goroutine or from several: the bucket calls
// After under its own lock, so the instants line up (see TokenBucket.waitPiece).
type vclock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newVClock() *vclock { return &vclock{now: epoch} }

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *vclock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	if d > 0 {
		c.now = c.now.Add(d)
	}
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *vclock) elapsed() time.Duration { return c.Now().Sub(epoch) }

func TestBucketTable(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name      string
		rate      int64
		take      []int
		wantSleep []time.Duration
	}{
		{"burst is free", mib, []int{mib}, nil},
		{"second megabyte waits one second", mib, []int{mib, mib}, []time.Duration{time.Second}},
		{"half a megabyte per second", mib / 2, []int{mib, mib / 2}, []time.Duration{time.Second}},
		{"unlimited never waits", 0, []int{10 * mib, 10 * mib}, nil},
		{"larger than the burst is taken in pieces", mib, []int{3 * mib}, []time.Duration{time.Second, time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newVClock()
			b := NewBucket(tc.rate, clk)
			for _, n := range tc.take {
				if err := b.Wait(context.Background(), n); err != nil {
					t.Fatal(err)
				}
			}
			if len(clk.sleeps) != len(tc.wantSleep) {
				t.Fatalf("sleeps = %v, want %v", clk.sleeps, tc.wantSleep)
			}
			for i, d := range tc.wantSleep {
				if clk.sleeps[i] != d {
					t.Errorf("sleep %d = %v, want %v", i, clk.sleeps[i], d)
				}
			}
		})
	}
}

func TestBucketRefillsWhileIdle(t *testing.T) {
	clk := newVClock()
	b := NewBucket(1<<20, clk)
	_ = b.Wait(context.Background(), 1<<20) // drains the burst
	clk.mu.Lock()
	clk.now = clk.now.Add(10 * time.Second) // idle: refills, capped at the burst
	clk.mu.Unlock()
	_ = b.Wait(context.Background(), 1<<20)
	if len(clk.sleeps) != 0 {
		t.Errorf("slept %v after an idle period that refills the burst", clk.sleeps)
	}
	_ = b.Wait(context.Background(), 1<<20)
	if len(clk.sleeps) != 1 {
		t.Errorf("sleeps = %v, want exactly one (the burst is capped, not banked)", clk.sleeps)
	}
}

func TestBucketSetRate(t *testing.T) {
	clk := newVClock()
	b := NewBucket(0, clk)
	if !b.Unlimited() || b.Rate() != 0 {
		t.Fatal("rate 0 must be unlimited")
	}
	b.SetRate(1 << 20)
	if b.Unlimited() || b.Rate() != 1<<20 {
		t.Fatalf("rate = %d", b.Rate())
	}
	_ = b.Wait(context.Background(), 1<<20)
	_ = b.Wait(context.Background(), 1<<20)
	if len(clk.sleeps) != 1 {
		t.Fatalf("sleeps = %v", clk.sleeps)
	}
	b.SetRate(-5)
	if !b.Unlimited() {
		t.Error("a negative rate is unlimited")
	}
}

// A waiter that gives up returns what it reserved, so the others are not slowed by a ghost.
func TestBucketCancelRefunds(t *testing.T) {
	clk := newManualClock()
	b := NewBucket(1<<20, clk)
	_ = b.Wait(context.Background(), 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Wait(ctx, 1<<20) }()
	clk.waitForTimer(t)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// With the ghost reservation refunded, a full second of refill pays for a full megabyte.
	clk.advance(time.Second)
	go func() { done <- b.Wait(context.Background(), 1<<20) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second waiter is still blocked: the cancelled reservation was not returned")
	}
}

func TestBucketAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewBucket(1<<20, newVClock()).Wait(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if err := (Unlimited{}).Wait(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("unlimited err = %v", err)
	}
}

// manualClock fires After channels only when advance moves the time past them.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*mtimer
	added  chan struct{}
}

type mtimer struct {
	at time.Time
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: epoch, added: make(chan struct{}, 16)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &mtimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, tm)
	c.added <- struct{}{}
	return tm.ch
}

func (c *manualClock) waitForTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.added:
	case <-time.After(10 * time.Second):
		t.Fatal("no timer was registered")
	}
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, tm := range c.timers {
		if !tm.at.After(c.now) {
			select {
			case tm.ch <- c.now:
			default:
			}
		}
	}
}

// Two downloads share one bucket: together they stay within 10 % of the limit over 10 s of virtual
// time (acceptance criterion of the issue).
func TestTwoParallelDownloadsShareTheLimit(t *testing.T) {
	const (
		rate  = 10 << 20
		chunk = 64 << 10
		span  = 10 * time.Second
	)
	clk := newVClock()
	b := NewBucket(rate, clk)
	var mu sync.Mutex
	var total [2]int64
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for clk.elapsed() < span {
				if err := b.Wait(context.Background(), chunk); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				total[i] += chunk
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sum := total[0] + total[1]
	want := float64(rate) * clk.elapsed().Seconds()
	if got := float64(sum); math.Abs(got-want)/want > 0.10 {
		t.Errorf("transferred %d bytes in %v, want %.0f within 10 %%", sum, clk.elapsed(), want)
	}
	if sum < int64(float64(rate)*span.Seconds()*0.9) {
		t.Errorf("transferred %d bytes, below 90 %% of the limit over %v", sum, span)
	}
}

func TestParseLimit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		bad  bool
	}{
		{"0", 0, false},
		{" 0 ", 0, false},
		{"20MiB/s", 20 << 20, false},
		{"500KB/s", 500_000, false},
		{"500kB/s", 500_000, false},
		{"100Mbit/s", 12_500_000, false},
		{"8kbit/s", 1000, false},
		{"1.5MB/s", 1_500_000, false},
		{"1GiB/s", 1 << 30, false},
		{"2 MiB/s", 2 << 20, false},
		{"64B/s", 64, false},
		{"8bit/s", 1, false},
		{"", 0, true},
		{"20", 0, true},
		{"20MiB", 0, true},
		{"20mb/s", 0, true}, // ambiguous: megabit or megabyte
		{"20PiB/s", 0, true},
		{"-5MB/s", 0, true},
		{"fast", 0, true},
		{"0MB/s", 0, true},
		{"0.5bit/s", 0, true},
		{"99999999TB/s", 0, true},
		{"2000000GiB/s", 0, true},
	} {
		got, err := ParseLimit(tc.in)
		if tc.bad {
			if !errors.Is(err, ErrBadLimit) {
				t.Errorf("ParseLimit(%q) = %d, %v; want ErrBadLimit", tc.in, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseLimit(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

// recBucket counts what the proxy draws.
type recBucket struct {
	mu sync.Mutex
	n  int
}

func (r *recBucket) Wait(ctx context.Context, n int) error {
	r.mu.Lock()
	r.n += n
	r.mu.Unlock()
	return ctx.Err()
}

func (r *recBucket) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// startTarget serves a TCP "server" that sends payload and closes. The proxy's Dial reaches it for
// any host:443.
func startTarget(t *testing.T, payload []byte) (dial func(context.Context, string, string) (net.Conn, error), dialed chan string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write(payload)
			}()
		}
	}()
	dialed = make(chan string, 8)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed <- addr
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}, dialed
}

func connectReq(t *testing.T, proxyAddr, target string) (*http.Response, *bufio.Reader, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", target, err)
	}
	return resp, br, c
}

func TestProxyTunnelsAllowlistedHostThroughTheBucket(t *testing.T) {
	payload := make([]byte, 300<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	dial, dialed := startTarget(t, payload)
	bucket := &recBucket{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, stop, err := StartProxyWith(ctx, bucket, []string{"Files.Example.Test", "other.example.test:443"}, ProxyOptions{Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	host, _, _ := net.SplitHostPort(addr)
	if host != "127.0.0.1" {
		t.Fatalf("listens on %q, want loopback", host)
	}
	resp, br, _ := connectReq(t, addr, "files.example.test:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("relayed %d bytes, want %d", len(got), len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
	if bucket.total() != len(payload) {
		t.Errorf("bucket drew %d bytes, want %d", bucket.total(), len(payload))
	}
	if d := <-dialed; d != "files.example.test:443" {
		t.Errorf("dialed %q", d)
	}
	resp2, _, _ := connectReq(t, addr, "other.example.test:443")
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("host listed with :443 got %d", resp2.StatusCode)
	}
}

func TestProxyRefusals(t *testing.T) {
	dial, dialed := startTarget(t, []byte("x"))
	addr, stop, err := StartProxyWith(context.Background(), &recBucket{}, []string{"ok.example.test"}, ProxyOptions{Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, tc := range []struct{ name, req string }{
		{"host not on the list", "CONNECT evil.example.test:443 HTTP/1.1\r\nHost: evil.example.test:443\r\n\r\n"},
		{"port 80", "CONNECT ok.example.test:80 HTTP/1.1\r\nHost: ok.example.test:80\r\n\r\n"},
		{"no port", "CONNECT ok.example.test HTTP/1.1\r\nHost: ok.example.test\r\n\r\n"},
		{"GET", "GET http://ok.example.test/ HTTP/1.1\r\nHost: ok.example.test\r\n\r\n"},
		{"POST", "POST / HTTP/1.1\r\nHost: ok.example.test\r\nContent-Length: 0\r\n\r\n"},
		{"loopback address", "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, _ = io.WriteString(c, tc.req)
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status %d, want 403", resp.StatusCode)
			}
		})
	}
	select {
	case d := <-dialed:
		t.Errorf("the proxy dialed %q for a refused request", d)
	default:
	}
}

func TestProxyUnlimitedIsNotStarted(t *testing.T) {
	for name, b := range map[string]Bucket{"nil": nil, "Unlimited": Unlimited{}, "rate 0": NewBucket(0, nil)} {
		addr, stop, err := StartProxy(context.Background(), b, []string{"ok.example.test"})
		if err != nil || addr != "" {
			t.Errorf("%s: addr %q, err %v; want no proxy", name, addr, err)
		}
		stop()
	}
	addr, stop, err := StartProxy(context.Background(), NewBucket(1<<20, nil), nil)
	if err != nil || addr == "" {
		t.Fatalf("limited bucket: addr %q, err %v", addr, err)
	}
	stop()
	stop() // idempotent
}

func TestProxyDiesWithItsContext(t *testing.T) {
	dial, _ := startTarget(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	addr, stop, err := StartProxyWith(ctx, &recBucket{}, []string{"ok.example.test"}, ProxyOptions{Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the proxy still accepts connections after its context was cancelled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProxyStopClosesOpenTunnels(t *testing.T) {
	hold, release := net.Pipe()
	defer release.Close()
	dial := func(context.Context, string, string) (net.Conn, error) { return hold, nil }
	addr, stop, err := StartProxyWith(context.Background(), &recBucket{}, []string{"ok.example.test"}, ProxyOptions{Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	resp, _, c := connectReq(t, addr, "ok.example.test:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	stop()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("the tunnel is still open after stop")
	}
}
