package app

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Serve is `aiagent serve`: it builds the Daemon over env, runs the startup sequence of agent.md
// section 4.1, waits until ctx ends (SIGTERM or SIGINT, see SignalContext) and runs the graceful
// shutdown of section 4.12. It returns a *FatalError for the two fatal startup failures (not root,
// both listeners failed) and nil after a clean shutdown; the caller exits 1 on an error.
func Serve(ctx context.Context, env Env) error {
	d := NewDaemon(env)
	if err := d.Start(ctx); err != nil {
		d.Shutdown() // flush what was opened; harmless for what never was
		return err
	}
	<-ctx.Done()
	d.Shutdown()
	return nil
}

// RunServe is the entry point cmd/aiagent calls for `aiagent serve`: Serve over the real machine,
// with the error reported on stderr. It returns the process exit code.
func RunServe(ctx context.Context, stderr io.Writer) int {
	if err := Serve(ctx, Env{}); err != nil {
		var fe *FatalError
		if errors.As(err, &fe) {
			fmt.Fprintf(stderr, "aiagent: %s: %v\n", fe.Code, fe.Err)
		} else {
			fmt.Fprintf(stderr, "aiagent: %v\n", err)
		}
		return 1
	}
	return 0
}
