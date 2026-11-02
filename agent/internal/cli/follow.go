package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// FollowSpec is what a long-running command gives Call.Follow (aictl.md section 3.3, "Progress").
// Which events a command watches and how it words them is the command's business; the waiting, the
// drawing, Ctrl-C, resync and reconnects are Follow's.
type FollowSpec struct {
	// Progress says which events to watch, how to match them to this operation, how to turn them
	// into progress and a result, and how to re-read the real state (see progress.Spec).
	Progress progress.Spec
	// Request is the mutating request that starts the operation, for example POST /models/{id}/load
	// with wait=false. Follow opens the event stream first, so no event is missed, then calls it
	// with the command's context. The key it returns (op_id from the response or X-LocalAI-Op-Id,
	// run_id) is given to Progress.Match. Nil follows an operation that is already running.
	//
	// It must return quickly (the request that answers 202): Ctrl-C ends the waiting, never the
	// operation, and a request that blocks until the operation is done cannot be left behind.
	Request func(ctx context.Context) (key string, err error)

	// OnInterrupt, when set, replaces "leave at once" on Ctrl-C (`ark sync` foreground asks "Cancel
	// the sync too? [y/N]"). Follow then takes over the signals (Interrupts) so every press reaches
	// it. Without it the first Ctrl-C prints "Still running in the background" and exits 130, and
	// no cancel request is ever sent.
	OnInterrupt func(ctx context.Context) progress.InterruptAction
	// Interrupt overrides the signal source (tests, or a command that already called Interrupts).
	Interrupt <-chan struct{}

	// Clock and TTY are test hooks: a manual clock, and whether stderr is a terminal (default: it
	// is a character device and TERM is not "dumb").
	Clock config.Clock
	TTY   *bool
}

// Follow waits for an operation by following the agent's events and returns what the command's
// Progress.OnEvent / Reread produced. It is the one place the progress rules live:
//
//   - human mode on a terminal draws one updating line on stderr; off a terminal it prints one line
//     per state change; --quiet draws nothing; --json draws nothing and the command prints the
//     final document afterwards (the waiting still respects --timeout);
//   - Ctrl-C stops the waiting, prints `i Still running in the background. Check with `aictl status`.`
//     and exits 130, and sends nothing to the agent (with --json the CodeInterrupted envelope is the
//     output);
//   - a resync, a shutdown or a lost connection reconnects with Last-Event-ID and re-reads the real
//     state; an expired deadline is exit 7.
//
// A command returns the error as it is: ExitFor and the reporter do the rest.
func (c *Call) Follow(fs FollowSpec) (progress.Result, error) {
	cl, err := c.Client()
	if err != nil {
		return progress.Result{}, err
	}
	tty := c.stderrIsTTY()
	if fs.TTY != nil {
		tty = *fs.TTY
	}
	rend := progress.NewRenderer(progress.RenderOptions{
		Out:    c.Stderr,
		TTY:    tty,
		Silent: c.Global.Quiet || c.Global.JSON,
		JSON:   c.Global.JSON,
		Color:  tty && !c.Global.NoColor,
		UTF8:   out.UTF8Enabled(c.env.Getenv),
		Lang:   string(c.Lang),
		Clock:  fs.Clock,
	})

	spec := fs.Progress
	if spec.Reread != nil {
		reread := spec.Reread
		spec.Reread = func(ctx context.Context) (progress.Step, error) {
			step, err := reread(ctx)
			return step, markFatal(err)
		}
	}

	opts := progress.Options{
		Open:        c.eventsOpener(cl),
		Renderer:    rend,
		Clock:       fs.Clock,
		Timeout:     c.Timeout,
		Lang:        string(c.Lang),
		Interrupt:   fs.Interrupt,
		OnInterrupt: fs.OnInterrupt,
	}
	if opts.OnInterrupt != nil && opts.Interrupt == nil {
		ch, stop := Interrupts()
		defer stop()
		opts.Interrupt = ch
	}

	w, err := progress.Open(c.Ctx, spec, opts)
	if err != nil {
		return progress.Result{}, c.followErr(err)
	}
	if fs.Request != nil {
		key, rerr := fs.Request(c.Ctx)
		if rerr != nil {
			w.Close()
			return progress.Result{}, rerr
		}
		w.Correlate(key)
	}
	res, err := w.Wait()
	if err != nil {
		return res, c.followErr(err)
	}
	return res, nil
}

// followErr maps the end of a wait that did not succeed. After Ctrl-C the renderer has already
// printed the "Still running" line, so a human run only needs the exit code; --json gets the
// CodeInterrupted envelope on stdout like any other failure.
func (c *Call) followErr(err error) error {
	if errors.Is(err, progress.ErrInterrupted) {
		if c.Global.JSON {
			return c.Err(CodeInterrupted, "").WithCause(context.Canceled)
		}
		return ExitWith(ExitInterrupted)
	}
	return err
}

// eventsOpener binds GET /events to the client: same transport, same startup retry, and the
// Last-Event-ID header on a reconnect.
func (c *Call) eventsOpener(cl *Client) events.OpenFunc {
	return func(ctx context.Context, q events.Query) (io.ReadCloser, error) {
		path := "/events"
		if len(q.Types) > 0 {
			// The patterns hold only a-z, 0-9, "_", "-", "." and a final "*": nothing to escape.
			path += "?types=" + strings.Join(q.Types, ",")
		}
		var hdr http.Header
		if q.LastEventID != "" {
			hdr = http.Header{"Last-Event-ID": {q.LastEventID}}
		}
		resp, err := cl.StreamWith(ctx, http.MethodGet, path, nil, hdr)
		if err != nil {
			return nil, markFatal(err)
		}
		return resp.Body, nil
	}
}

// markFatal tells the waiter which failures are worth retrying: only "the agent is not reachable"
// (it is starting or restarting). An answer from the agent (404, 403, 500) or a deadline is final.
func markFatal(err error) error {
	if err == nil {
		return nil
	}
	var ce *CLIError
	if errors.As(err, &ce) && ce.Code == CodeUnreachable {
		return err
	}
	var ae *APIError
	if errors.As(err, &ae) || ce != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return progress.Fatal(err)
	}
	return err
}

// stderrIsTTY reports whether progress may redraw a line: stderr is a terminal that is not "dumb".
func (c *Call) stderrIsTTY() bool {
	if c.env.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := c.Stderr.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
