package netx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DialFunc is the shape of http.Transport.DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// DefaultDialTimeout and DefaultKeepAlive are the settings of the dialer netx uses when a caller
// brings none.
const (
	DefaultDialTimeout = 30 * time.Second
	DefaultKeepAlive   = 30 * time.Second
)

// Dialer returns a plain dialer that also refuses, while the fence is on, a connection whose resolved
// address is not loopback (the name localhost may resolve to anything /etc/hosts says). timeout 0 is
// DefaultDialTimeout. Its DialContext is not fenced by itself: Wrap puts the fence in front of it.
func (f *Fence) Dialer(timeout time.Duration) DialFunc {
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	d := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: DefaultKeepAlive,
		Control: func(network, address string, _ syscall.RawConn) error {
			if !f.Active() || Allowed(network, address) {
				return nil
			}
			f.record(address, "resolved")
			return errors.New("netx: the network fence refused a name that resolves outside this machine")
		},
	}
	return d.DialContext
}

// ErrNotLoopback is the error of LoopbackDial when it refuses an address.
var ErrNotLoopback = errors.New("netx: refusing to dial a non-loopback address")

// LoopbackDial returns a dial that connects to loopback addresses only, always (not only while a fence is
// on), for a client that must never talk to anything but a service on this machine and carries a key.
// It refuses by the address string and, for a name, again by the address the name resolved to. timeout 0
// is DefaultDialTimeout. Put it behind the fence with NewTransport or Wrap as any other dial.
func LoopbackDial(timeout time.Duration) DialFunc {
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	d := &net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			if !strings.HasPrefix(network, "unix") && !Allowed(network, address) {
				return ErrNotLoopback
			}
			return nil
		},
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !Allowed(network, addr) {
			return nil, ErrNotLoopback
		}
		return d.DialContext(ctx, network, addr)
	}
}

// Wrap returns inner behind the fence: while the fence is on, a dial to anything but loopback or a Unix
// socket is refused before inner runs (so before any name is resolved) and counted. caller names who
// dials when the context names none. A nil inner dials with a plain dialer of DefaultDialTimeout.
func (f *Fence) Wrap(caller string, inner DialFunc) DialFunc {
	if inner == nil {
		inner = f.Dialer(0)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := f.Check(ctx, caller, network, addr); err != nil {
			return nil, err
		}
		return inner(ctx, network, addr)
	}
}

// baseTransport is the configuration of every transport netx makes: the defaults of
// http.DefaultTransport except that the proxy environment is never read (the agent's unit sets none,
// and a proxy would be one more way to reach the internet unseen).
func (f *Fence) baseTransport(caller string, inner DialFunc) *http.Transport {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           f.Wrap(caller, inner),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	f.track(tr)
	return tr
}

// NewTransport returns a transport that dials through the fence and names caller when the request's
// context names none. inner is the real dial (nil: a plain dialer); pass your own for a dial that must
// be pinned or restricted further. Set the other fields (timeouts, TLS, keep-alives) after the call.
func (f *Fence) NewTransport(caller string, inner DialFunc) *http.Transport {
	return f.baseTransport(caller, inner)
}

// Dial and the package-level helpers below use the Default fence.

// Wrap is Default.Wrap.
func Wrap(caller string, inner DialFunc) DialFunc { return Default.Wrap(caller, inner) }

// NewTransport is Default.NewTransport.
func NewTransport(caller string, inner DialFunc) *http.Transport {
	return Default.NewTransport(caller, inner)
}

// DialContext dials like net.Dialer.DialContext, behind the Default fence. Use it for code that opens
// a connection without HTTP.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return Default.Wrap("", nil)(ctx, network, addr)
}

// DialFor returns a fenced dial for caller with the given dial timeout (0: DefaultDialTimeout).
func DialFor(caller string, timeout time.Duration) DialFunc {
	return Default.Wrap(caller, Default.Dialer(timeout))
}

var (
	sharedOnce sync.Once
	shared     *http.Transport
)

// Shared is the agent's single shared outbound transport: no proxy, HTTP/1.1 and HTTP/2, dialing
// through the Default fence. Clients with no special needs use it (through Client or Via, so that their
// requests name a caller). Do not change its fields; make a transport with NewTransport for that.
func Shared() *http.Transport {
	sharedOnce.Do(func() { shared = Default.NewTransport("shared", nil) })
	return shared
}

type callerTripper struct {
	rt     http.RoundTripper
	caller string
}

func (c callerTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if CallerFrom(req.Context()) == "" {
		req = req.WithContext(WithCaller(req.Context(), c.caller))
	}
	return c.rt.RoundTrip(req)
}

// Via wraps rt so that its requests name caller (unless the request's context names one already).
func Via(rt http.RoundTripper, caller string) http.RoundTripper {
	if c := cleanCaller(caller); c != "" {
		return callerTripper{rt: rt, caller: c}
	}
	return rt
}

// Client returns an http.Client over the Shared transport whose requests name caller. It has no
// timeout and follows redirects like any http.Client; set what you need on the result.
func Client(caller string) *http.Client { return &http.Client{Transport: Via(Shared(), caller)} }

// UnixTransport returns a transport that sends every request to the Unix socket at path, whatever the
// URL says. A Unix socket is never reachable from outside the machine, so the fence never refuses it.
func UnixTransport(path string) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
}
