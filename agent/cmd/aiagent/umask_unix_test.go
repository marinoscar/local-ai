//go:build unix

package main

import (
	"syscall"
	"testing"
)

func TestSetRootUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	setRootUmask(1000)
	if got := syscall.Umask(0o077); got != 0o077 {
		t.Fatalf("a normal user's umask changed to %03o", got)
	}
	setRootUmask(0)
	if got := syscall.Umask(0o077); got != 0o022 {
		t.Fatalf("root's umask is %03o, want 022", got)
	}
}
