package gps

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// GPSDAddr is where gpsd listens (overview.md section 3.4). It is loopback only.
const GPSDAddr = "127.0.0.1:2947"

// watchCommand turns on the JSON stream.
const watchCommand = "?WATCH={\"enable\":true,\"json\":true};\n"

// tpv is the part of a gpsd TPV report the agent reads.
type tpv struct {
	Class string   `json:"class"`
	Mode  int      `json:"mode"`
	Lat   *float64 `json:"lat"`
	Lon   *float64 `json:"lon"`
	Epx   float64  `json:"epx"`
	Epy   float64  `json:"epy"`
	Eph   float64  `json:"eph"`
}

// ParseTPV reads one line of gpsd's JSON stream. Only a TPV with mode 2 or 3 and a position gives a
// fix; mode 0 or 1 ("no fix"), other classes and malformed lines return ok=false.
func ParseTPV(line []byte, now time.Time) (Fix, bool) {
	var t tpv
	if json.Unmarshal(line, &t) != nil || t.Class != "TPV" || t.Mode < 2 || t.Lat == nil || t.Lon == nil {
		return Fix{}, false
	}
	if math.IsNaN(*t.Lat) || math.IsNaN(*t.Lon) || !inRange(*t.Lat, *t.Lon) {
		return Fix{}, false
	}
	acc := t.Eph
	if acc == 0 {
		acc = math.Max(t.Epx, t.Epy)
	}
	mode := t.Mode
	if mode > 3 {
		mode = 3
	}
	return Fix{Lat: *t.Lat, Lon: *t.Lon, AccuracyM: math.Round(acc), Mode: mode, At: now}, true
}

// Dialer opens the connection to gpsd; tests pass a net.Pipe.
type Dialer func(ctx context.Context, addr string) (net.Conn, error)

// NetDialer is the real dialer.
func NetDialer(ctx context.Context, addr string) (net.Conn, error) {
	return netx.DialFor("maps.gps", 3*time.Second)(ctx, "tcp", addr)
}

// Backoff of the reconnection to gpsd and of the serial reopening (companion-services.md section
// 4.4.5): 5 s, doubling, up to 60 s.
const (
	BackoffMin = 5 * time.Second
	BackoffMax = 60 * time.Second
)

// Sleeper waits d or until ctx ends; it reports false when ctx ended. Tests pass a fake.
type Sleeper func(ctx context.Context, d time.Duration) bool

// RealSleep is the Sleeper over a timer.
func RealSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// NextBackoff doubles cur within [BackoffMin, BackoffMax].
func NextBackoff(cur time.Duration) time.Duration {
	if cur < BackoffMin {
		return BackoffMin
	}
	if cur*2 > BackoffMax {
		return BackoffMax
	}
	return cur * 2
}

// GPSD is the gpsd client.
type GPSD struct {
	Addr  string
	Dial  Dialer
	Sleep Sleeper
	Clock func() time.Time
	// OnState is told when the connection comes up (true) and goes down (false).
	OnState func(connected bool)
	// OnFix receives every fix.
	OnFix func(Fix)
}

func (g *GPSD) defaults() (addr string, dial Dialer, clock func() time.Time) {
	addr, dial, clock = g.Addr, g.Dial, g.Clock
	if addr == "" {
		addr = GPSDAddr
	}
	if dial == nil {
		dial = NetDialer
	}
	if clock == nil {
		clock = time.Now
	}
	return addr, dial, clock
}

// Once connects and streams until the connection ends. dialed is false when nothing accepted the
// connection (gpsd is not running); got says whether any line arrived.
func (g *GPSD) Once(ctx context.Context) (dialed, got bool) {
	addr, dial, clock := g.defaults()
	conn, err := dial(ctx, addr)
	if err != nil {
		return false, false
	}
	return true, g.serve(ctx, conn, clock)
}

// Run connects, streams and reconnects with backoff until ctx ends. A connection that delivered at
// least one line resets the backoff.
func (g *GPSD) Run(ctx context.Context) {
	sleep := g.Sleep
	if sleep == nil {
		sleep = RealSleep
	}
	wait := time.Duration(0)
	for ctx.Err() == nil {
		if _, got := g.Once(ctx); got {
			wait = 0
		}
		wait = NextBackoff(wait)
		if !sleep(ctx, wait) {
			return
		}
	}
}

// serve streams one connection; it reports whether any line arrived.
func (g *GPSD) serve(ctx context.Context, conn net.Conn, clock func() time.Time) (got bool) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, watchCommand); err != nil {
		return false
	}
	if g.OnState != nil {
		g.OnState(true)
		defer g.OnState(false)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		got = true
		if fix, ok := ParseTPV(sc.Bytes(), clock()); ok && g.OnFix != nil {
			g.OnFix(fix)
		}
	}
	return got
}
