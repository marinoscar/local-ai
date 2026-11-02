package cli

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// sendSIGINT sends SIGINT to this test process. It is safe only while something has registered for
// the signal (Interrupts does), otherwise the default action would kill the test binary.
func sendSIGINT(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptsDeliversEveryPress(t *testing.T) {
	ch, stop := Interrupts()
	defer stop()
	for i := 0; i < 2; i++ {
		sendSIGINT(t)
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("press %d never arrived", i+1)
		}
	}
	stop()
	stop() // idempotent
}
