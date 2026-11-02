package fake

import (
	"context"
	"io"
	"sync"
)

// Input is one thing the person at the fake terminal does: type Text and press Enter, or make the
// read fail with Err (io.EOF for Ctrl-D, context.Canceled for Ctrl-C).
type Input struct {
	Text string
	Err  error
}

// Terminal is a scripted person at the keyboard. It implements cli.Terminal (the method set is the
// contract; this package must not import cli) so confirmation and PIN tests need no pseudo-terminal.
// Each read consumes the next Input; when the script is used up a read returns io.EOF, like a closed
// stdin.
type Terminal struct {
	// TTY is what Interactive reports.
	TTY bool
	// Script is what the person does, in order.
	Script []Input

	mu    sync.Mutex
	pos   int
	kinds []string
}

// Typed returns a terminal (interactive when tty) whose person types the given lines in order.
func Typed(tty bool, lines ...string) *Terminal {
	t := &Terminal{TTY: tty}
	for _, l := range lines {
		t.Script = append(t.Script, Input{Text: l})
	}
	return t
}

// Interactive implements cli.Terminal.
func (t *Terminal) Interactive() bool { return t.TTY }

func (t *Terminal) next(ctx context.Context, kind string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kinds = append(t.kinds, kind)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.pos >= len(t.Script) {
		return "", io.EOF
	}
	in := t.Script[t.pos]
	t.pos++
	return in.Text, in.Err
}

// ReadLine implements cli.Terminal.
func (t *Terminal) ReadLine(ctx context.Context) (string, error) { return t.next(ctx, "line") }

// ReadSecret implements cli.Terminal. Kinds records that the read was a secret one (no echo).
func (t *Terminal) ReadSecret(ctx context.Context) (string, error) { return t.next(ctx, "secret") }

// Kinds returns how each read was made, in order: "line" or "secret".
func (t *Terminal) Kinds() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.kinds...)
}

// Reads returns how many reads were made.
func (t *Terminal) Reads() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.kinds)
}
