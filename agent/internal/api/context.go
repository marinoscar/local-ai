package api

import (
	"context"
	"net/http"
)

// Transport is the listener a request arrived on (agent.md section 3.7).
type Transport string

// The two transports. The same handler serves both.
const (
	TransportTCP    Transport = "tcp"
	TransportSocket Transport = "socket"
)

// PeerCred is the credentials of a unix-socket peer, read with SO_PEERCRED when the connection was
// accepted. The auth middleware (AGENT-06) maps the uid and its groups to a principal.
type PeerCred struct{ UID, GID, PID int }

type connKey struct{}

type connInfo struct {
	transport Transport
	peer      *PeerCred
}

// WithTransport returns ctx marked as belonging to a connection of transport t with the given peer
// credentials (nil for TCP, or when the socket peer could not be read). The listeners do this in
// ConnContext; tests use it to drive the handler as a socket request without a real socket. A request
// without the mark is treated as TCP, so every TCP-only check fails closed.
func WithTransport(ctx context.Context, t Transport, peer *PeerCred) context.Context {
	return context.WithValue(ctx, connKey{}, connInfo{transport: t, peer: peer})
}

type infoKey struct{}

// RequestInfo is what the middleware chain knows about one request. The first middleware creates it;
// handlers and the auth hook read it with InfoFrom. It is not safe for use by other goroutines.
type RequestInfo struct {
	// ID is the request id (X-Request-Id), req_ plus 12 base32 characters.
	ID string
	// Transport is the listener the request arrived on.
	Transport Transport
	// Peer is the socket peer's credentials: nil on TCP, and nil on a socket when SO_PEERCRED
	// failed (the auth hook must then refuse the request).
	Peer *PeerCred
	// Lang is the language of error messages: "en" or "es".
	Lang string
	// Route is the matched route, nil until step 4 and for an unmatched request.
	Route *Route
	// Principal is the authenticated principal, zero (anonymous) until the auth step has run.
	Principal Principal
	// MaxBody is the body limit that applies to this request, set before the handler runs.
	MaxBody int64
	// ErrCode is the doctor code of the error envelope written for this request, if any.
	ErrCode string
	// Err is the underlying error of a 5xx envelope, for the access log. It is never sent to the client.
	Err error
}

// InfoFrom returns the request's RequestInfo. A request that did not pass through the server's
// middleware (a handler unit test) gets a detached, TCP-transport value, never nil.
func InfoFrom(ctx context.Context) *RequestInfo {
	if info, ok := ctx.Value(infoKey{}).(*RequestInfo); ok {
		return info
	}
	return &RequestInfo{Transport: TransportTCP, Lang: langEN, MaxBody: DefaultMaxBody}
}

// RequestID returns the request id of r, or "" outside the middleware chain.
func RequestID(r *http.Request) string { return InfoFrom(r.Context()).ID }

func connFrom(ctx context.Context) connInfo {
	if c, ok := ctx.Value(connKey{}).(connInfo); ok {
		return c
	}
	return connInfo{transport: TransportTCP}
}
