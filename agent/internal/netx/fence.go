package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"weak"
)

// ErrFenced is what a refused dial matches (errors.Is). The error text names the address and the
// caller.
var ErrFenced = errors.New("netx: the network fence refused a dial")

// FencedError is the error of a dial the fence refused.
type FencedError struct {
	// Addr is the host:port that was dialed.
	Addr string
	// Caller is who dialed (WithCaller).
	Caller string
}

func (e *FencedError) Error() string {
	return fmt.Sprintf("netx: the network fence refused a dial to %s (caller %s)", e.Addr, e.Caller)
}

// Is makes errors.Is(err, ErrFenced) true.
func (e *FencedError) Is(target error) bool { return target == ErrFenced }

// Limits of what a fence remembers. The count of refused dials is exact; the list of who tried is
// bounded.
const (
	maxDistinct = 128
	maxAddrLen  = 120
)

// Blocked is one distinct refused destination with its caller and how many dials were refused.
type Blocked struct {
	// Addr is host:port.
	Addr   string
	Caller string
	Count  int
}

// Report is what a fence saw between an Acquire and its Release. Blocked is the exact number of
// refused dials; Dials lists the distinct destinations in the order they were first seen (at most
// 128 of them: a flood of different hosts is counted but not all listed).
type Report struct {
	Blocked int
	Dials   []Blocked
}

type key struct{ addr, caller string }

// Fence is a network fence. The zero value is not usable; use NewFence or Default.
type Fence struct {
	active atomic.Bool

	mu    sync.Mutex
	depth int
	total int
	order []key
	count map[key]int

	// transports are the transports made for this fence, weakly held (a short-lived client makes its own).
	// Acquire closes their idle connections: a pooled connection opened before the drill would otherwise
	// carry a request to the internet without any dial for the fence to see.
	transports []weak.Pointer[http.Transport]
}

// NewFence returns an inactive fence. Tests make their own; the agent uses Default.
func NewFence() *Fence { return &Fence{count: map[key]int{}} }

// Default is the fence of the agent process. Shared, Client, DialContext and every transport made
// without an explicit fence use it.
var Default = NewFence()

// Active reports whether any holder has the fence on.
func (f *Fence) Active() bool { return f.active.Load() }

// Lease is one hold of a fence. Release it exactly once (more calls return the same report).
type Lease struct {
	f         *Fence
	baseTotal int
	base      map[key]int
	once      sync.Once
	rep       Report
}

// Acquire turns the fence on (or keeps it on for a nested holder) and returns the hold. Pair it with
// a deferred Release: the fence is reference counted, so it stays on until the last holder lets go.
func (f *Fence) Acquire() *Lease {
	f.mu.Lock()
	var idle []*http.Transport
	if f.depth == 0 {
		f.total, f.order, f.count = 0, nil, map[key]int{}
		live := f.transports[:0]
		for _, w := range f.transports {
			if tr := w.Value(); tr != nil {
				live = append(live, w)
				idle = append(idle, tr)
			}
		}
		f.transports = live
	}
	f.depth++
	f.active.Store(true)
	base := make(map[key]int, len(f.count))
	for k, v := range f.count {
		base[k] = v
	}
	l := &Lease{f: f, baseTotal: f.total, base: base}
	f.mu.Unlock()
	for _, tr := range idle {
		tr.CloseIdleConnections()
	}
	return l
}

// track remembers a transport so that Acquire can close its idle connections.
func (f *Fence) track(tr *http.Transport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.transports); n >= 256 && n&(n-1) == 0 { // at each doubling, drop the ones that were collected
		live := f.transports[:0]
		for _, w := range f.transports {
			if w.Value() != nil {
				live = append(live, w)
			}
		}
		f.transports = live
	}
	f.transports = append(f.transports, weak.Make(tr))
}

// Release gives the hold back and returns what the fence refused since Acquire. The fence turns off
// when the last holder releases. It is safe to call twice and from a panic's defer.
func (l *Lease) Release() Report {
	l.once.Do(func() {
		f := l.f
		f.mu.Lock()
		defer f.mu.Unlock()
		l.rep.Blocked = f.total - l.baseTotal
		for _, k := range f.order {
			if n := f.count[k] - l.base[k]; n > 0 {
				l.rep.Dials = append(l.rep.Dials, Blocked{Addr: k.addr, Caller: k.caller, Count: n})
			}
		}
		f.depth--
		if f.depth <= 0 {
			f.depth = 0
			f.active.Store(false)
		}
	})
	return l.rep
}

func (f *Fence) record(addr, caller string) {
	k := key{addr: clip(addr, maxAddrLen), caller: caller}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total++
	if _, ok := f.count[k]; !ok {
		if len(f.order) >= maxDistinct {
			return
		}
		f.order = append(f.order, k)
	}
	f.count[k]++
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Allowed reports whether a dial to addr on network is allowed while a fence is on: Unix sockets, and
// TCP or UDP to a loopback IP literal or to the name localhost. It looks at the address string only and
// never resolves a name, because resolving a name is itself a network action. An address that cannot be
// read is not allowed.
func Allowed(network, addr string) bool {
	switch {
	case strings.HasPrefix(network, "unix"):
		return true
	case strings.HasPrefix(network, "tcp"), strings.HasPrefix(network, "udp"), network == "":
	default:
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return loopbackHost(host)
}

func loopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if i := strings.IndexByte(host, '%'); i >= 0 { // a zone: fe80::1%lo
		host = host[:i]
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Check applies the fence to one dial: nil when it is off or the address is allowed, a *FencedError
// otherwise. A refused dial is counted with its caller unless the context is a probe (WithProbe).
// deflt names the caller when the context names none.
func (f *Fence) Check(ctx context.Context, deflt, network, addr string) error {
	if !f.Active() || Allowed(network, addr) {
		return nil
	}
	caller := CallerFrom(ctx)
	if caller == "" {
		caller = cleanCaller(deflt)
	}
	if caller == "" {
		caller = "unknown"
	}
	if !isProbe(ctx) {
		f.record(addr, caller)
	}
	return &FencedError{Addr: addr, Caller: caller}
}
