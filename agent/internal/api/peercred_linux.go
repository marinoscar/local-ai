//go:build linux

package api

import (
	"errors"
	"net"
	"syscall"
)

// peerCred reads the credentials of the process on the other end of a unix connection
// (SO_PEERCRED): the kernel fills them in at connect time, so the peer cannot forge them.
func peerCred(c net.Conn) (PeerCred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return PeerCred{}, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerCred{}, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return PeerCred{}, err
	}
	if credErr != nil {
		return PeerCred{}, credErr
	}
	return PeerCred{UID: int(cred.Uid), GID: int(cred.Gid), PID: int(cred.Pid)}, nil
}
