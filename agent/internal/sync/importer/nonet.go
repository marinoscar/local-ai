package importer

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// ErrNetworkRefused is what an import's HTTP transport answers to every request: an import copies what
// is already on a drive and never uses the network, in any mode, including survival (ark-sync.md
// section 4.14).
var ErrNetworkRefused = errors.New("importer: an import never uses the network")

// NoNetwork is the http.RoundTripper of an import: it refuses every request. With Panic set it panics
// instead, which is what the tests use so that a code path that reaches for the network fails the
// build loudly instead of being swallowed as a retryable error.
type NoNetwork struct{ Panic bool }

// RoundTrip implements http.RoundTripper.
func (n NoNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	if n.Panic {
		panic("importer: an import tried to use the network: " + req.Method + " " + req.URL.Host)
	}
	return nil, ErrNetworkRefused
}

var _ http.RoundTripper = NoNetwork{}

// Client is an http.Client over NoNetwork.
func Client(panics bool) *http.Client { return &http.Client{Transport: NoNetwork{Panic: panics}} }

// RefuseDial is a DialContext (the shape of download.ClientConfig.DialContext) that never dials: it
// returns ErrNetworkRefused, or panics when panics is set. The downloader of an import is built over
// it, so even a code path that bypasses the transport cannot open a socket.
func RefuseDial(panics bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(_ context.Context, network, addr string) (net.Conn, error) {
		if panics {
			panic("importer: an import tried to dial " + network + " " + addr)
		}
		return nil, ErrNetworkRefused
	}
}
