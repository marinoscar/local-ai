package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Terminal is the person at the keyboard, as seen by prompts: the confirmation helpers and the PIN
// flow read through it and never touch os.Stdin, so tests drive them with a fake (cli/fake) and no
// pseudo-terminal is needed.
//
// Prompts are written to stderr by the helpers, not by the Terminal: stdout stays clean for
// results.
type Terminal interface {
	// Interactive reports whether stdin is a terminal a person can answer on. Without one (a pipe,
	// /dev/null, a closed stdin) no prompt is ever shown.
	Interactive() bool
	// ReadLine reads one line from stdin, echoed as usual, without its line ending. It returns
	// io.EOF when the input has ended with nothing read, and ctx.Err() when ctx ends first (Ctrl-C
	// cancels the context).
	ReadLine(ctx context.Context) (string, error)
	// ReadSecret reads one line without echoing it (the PIN). On a terminal it turns echo off and
	// restores it on every exit, including cancellation; on a pipe it is ReadLine. Errors are as for
	// ReadLine.
	ReadSecret(ctx context.Context) (string, error)
}

// maxLineBytes bounds what one prompt reads: a PIN or an answer is a few characters, and a
// runaway pipe must not fill memory.
const maxLineBytes = 4096

// readLine reads one line from r, dropping the line ending ("\n" or "\r\n"). A last line without a
// line ending is returned as a line; an empty stream is io.EOF. Bytes past maxLineBytes on one
// line are discarded.
func readLine(r *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				break
			}
			return "", err
		}
		if b == '\n' {
			break
		}
		if len(buf) < maxLineBytes {
			buf = append(buf, b)
		}
	}
	return strings.TrimSuffix(string(buf), "\r"), nil
}

// streamTerminal reads from any reader and is never interactive: what Run uses when the caller gives
// no Terminal (tests that script stdin, and any non-terminal stdin).
type streamTerminal struct {
	mu sync.Mutex
	r  *bufio.Reader
}

func newStreamTerminal(r io.Reader) *streamTerminal {
	return &streamTerminal{r: bufio.NewReader(r)}
}

func (t *streamTerminal) Interactive() bool { return false }

func (t *streamTerminal) ReadLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return readLine(t.r)
}

func (t *streamTerminal) ReadSecret(ctx context.Context) (string, error) { return t.ReadLine(ctx) }

// osTerminal is the real terminal on a file (os.Stdin).
type osTerminal struct {
	in  *os.File
	tty bool
	mu  sync.Mutex
	r   *bufio.Reader
}

// NewOSTerminal returns the Terminal for in, normally os.Stdin. Interactive is decided once, by
// golang.org/x/term.
func NewOSTerminal(in *os.File) Terminal {
	return &osTerminal{in: in, tty: term.IsTerminal(int(in.Fd())), r: bufio.NewReader(in)}
}

func (t *osTerminal) Interactive() bool { return t.tty }

type lineResult struct {
	s   string
	err error
}

// await runs read in a goroutine and returns its result, or ctx.Err() if ctx ends first (a blocked
// read of a terminal cannot be interrupted any other way). The abandoned read ends with the
// process, which is what a cancelled prompt leads to.
func await(ctx context.Context, read func() lineResult) (string, error) {
	ch := make(chan lineResult, 1)
	go func() { ch <- read() }()
	select {
	case res := <-ch:
		return res.s, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t *osTerminal) ReadLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return await(ctx, func() lineResult {
		t.mu.Lock()
		defer t.mu.Unlock()
		s, err := readLine(t.r)
		return lineResult{s, err}
	})
}

func (t *osTerminal) ReadSecret(ctx context.Context) (string, error) {
	if !t.tty {
		return t.ReadLine(ctx)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd := int(t.in.Fd())
	saved, _ := term.GetState(fd) // to put echo back if the read is abandoned
	s, err := await(ctx, func() lineResult {
		b, err := term.ReadPassword(fd) // io.EOF only when nothing was typed
		return lineResult{strings.TrimRight(string(b), "\r\n"), err}
	})
	if ctx.Err() != nil && saved != nil {
		_ = term.Restore(fd, saved)
	}
	return s, err
}
