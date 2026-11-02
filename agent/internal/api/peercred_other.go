//go:build !linux

package api

import (
	"errors"
	"net"
)

// peerCred is unavailable off Linux (the agent only runs on Linux; this keeps the package building
// on a developer's machine). The socket request then has no peer, and the auth hook refuses it.
func peerCred(net.Conn) (PeerCred, error) {
	return PeerCred{}, errors.New("peer credentials are only available on linux")
}
