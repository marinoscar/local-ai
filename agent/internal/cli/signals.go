package cli

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Interrupts returns a channel that receives once for every Ctrl-C (SIGINT) or SIGTERM, and a stop
// function that unregisters it (idempotent).
//
// Main already cancels Call.Ctx on the first signal, which is all a plain wait needs: Follow turns
// that cancellation into "stop waiting, leave the operation running" (aictl.md section 3.3). A
// command that wants to keep asking after the first press, such as `ark sync` in the foreground
// ("Cancel the sync too? [y/N]"), takes the signals over with this channel and passes it as
// FollowSpec.Interrupt: the cancelled context is then ignored and each press reaches the hook.
func Interrupts() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 4)
	out := make(chan struct{}, 4)
	done := make(chan struct{})
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for {
			select {
			case <-sig:
				select {
				case out <- struct{}{}:
				default: // a press is already waiting to be read
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			signal.Stop(sig)
			close(done)
		})
	}
}
