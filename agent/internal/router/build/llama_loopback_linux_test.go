//go:build linux

package build

import (
	"fmt"
	"syscall"
	"unsafe"
)

// bringUpLoopback sets the loopback interface up. A new network namespace (`unshare -n`) starts with
// lo down, and llama-cli of current llama.cpp talks to the model server it starts itself over
// 127.0.0.1: without lo it waits for it for ever. Loopback is no network: the namespace still has no
// other interface and no route, which is what the offline build test asserts. It needs
// CAP_NET_ADMIN (root in the namespace); an error is returned for the caller to decide.
func bringUpLoopback() error {
	const (
		siocgifflags = 0x8913
		siocsifflags = 0x8914
		iffUp        = 0x1
	)
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	// struct ifreq: the name (16 bytes), then a union of which the flags are the first 2 bytes.
	var ifr struct {
		name  [16]byte
		flags uint16
		_     [22]byte
	}
	copy(ifr.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocgifflags, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return fmt.Errorf("SIOCGIFFLAGS lo: %w", e)
	}
	if ifr.flags&iffUp != 0 {
		return nil
	}
	ifr.flags |= iffUp
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocsifflags, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return fmt.Errorf("SIOCSIFFLAGS lo: %w", e)
	}
	return nil
}
