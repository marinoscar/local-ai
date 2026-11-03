package fake

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Outcome says how a scripted streaming process ends once its chunks are delivered.
type Outcome int

const (
	// Exit: stdout ends and Wait reports ExitCode and Stderr.
	Exit Outcome = iota
	// Timeout: the process "runs past its timeout": it is killed, stdout ends and Wait returns
	// execx.ErrTimeout. Nothing sleeps: the timeout is declared, not waited for.
	Timeout
	// Hang: the process produces its chunks and then runs on until Kill or the end of the context
	// given to Start; Wait then returns the context's error (or nil after a plain Kill).
	Hang
)

// Script is what one fake streaming process does.
type Script struct {
	// StartErr, when set, makes Start fail with it (a missing binary, a refused rlimit).
	StartErr error
	// Release, when set, holds the process back until it is closed: it produces nothing and does not
	// read its stdin before. Concurrency tests close it to let the process go on.
	Release <-chan struct{}
	// Chunks are written to stdout one by one; each Write blocks until the reader took it, so a
	// reader that stops reading (a client that left) stops the process too.
	Chunks [][]byte
	// ExitCode and Stderr are reported by Wait for Outcome Exit.
	ExitCode int
	Stderr   string
	Outcome  Outcome
}

// Streamer implements execx.Streamer: it runs nothing, records every call and plays the next
// Script. Start fails loudly when no script is left.
type Streamer struct {
	mu      sync.Mutex
	scripts []Script
	procs   []*Proc
	started chan *Proc
}

// NewStreamer returns a Streamer that plays the scripts in order.
func NewStreamer(scripts ...Script) *Streamer {
	return &Streamer{scripts: scripts, started: make(chan *Proc, 256)}
}

// Add queues more scripts.
func (s *Streamer) Add(scripts ...Script) {
	s.mu.Lock()
	s.scripts = append(s.scripts, scripts...)
	s.mu.Unlock()
}

// Started delivers every process once it was started (after Start validated and read the command).
// A test waits on it to know the code under test has really launched the program.
func (s *Streamer) Started() <-chan *Proc { return s.started }

// Procs returns the processes started so far.
func (s *Streamer) Procs() []*Proc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Proc(nil), s.procs...)
}

// Calls returns the commands started so far, in order.
func (s *Streamer) Calls() []execx.StreamCmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]execx.StreamCmd, len(s.procs))
	for i, p := range s.procs {
		out[i] = p.Cmd
	}
	return out
}

// Start implements execx.Streamer.
func (s *Streamer) Start(ctx context.Context, c execx.StreamCmd) (execx.Process, error) {
	s.mu.Lock()
	if len(s.scripts) == 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("fake: unexpected streaming command: %s", c.Path)
	}
	sc := s.scripts[0]
	s.scripts = s.scripts[1:]
	s.mu.Unlock()
	if sc.StartErr != nil {
		return nil, sc.StartErr
	}
	pr, pw := io.Pipe()
	p := &Proc{Cmd: c, sc: sc, ctx: ctx, pr: pr, pw: pw, done: make(chan struct{}), killCh: make(chan struct{})}
	s.mu.Lock()
	s.procs = append(s.procs, p)
	s.mu.Unlock()
	go p.run()
	s.started <- p
	return p, nil
}

// Proc is a fake running process; it implements execx.Process.
type Proc struct {
	// Cmd is the command Start received.
	Cmd execx.StreamCmd

	sc  Script
	ctx context.Context
	pr  *io.PipeReader
	pw  *io.PipeWriter

	mu       sync.Mutex
	stdin    []byte
	killed   bool
	waited   bool
	timedOut bool
	done     chan struct{}
	killCh   chan struct{}
	killOnce sync.Once
}

// Stdin returns what the program read from its stdin (available once Done is closed or the
// process produced its first chunk).
func (p *Proc) Stdin() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.stdin...)
}

// Killed reports whether the process was killed (Kill, the context, or a scripted timeout).
func (p *Proc) Killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// Waited reports whether Wait was called.
func (p *Proc) Waited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waited
}

// Done is closed when the fake process finished producing output.
func (p *Proc) Done() <-chan struct{} { return p.done }

func (p *Proc) run() {
	defer close(p.done)
	if p.sc.Release != nil {
		select {
		case <-p.sc.Release:
		case <-p.killCh:
			_ = p.pw.Close()
			return
		case <-p.ctx.Done():
			p.kill()
			_ = p.pw.Close()
			return
		}
	}
	if p.Cmd.Stdin != nil {
		b, _ := io.ReadAll(p.Cmd.Stdin)
		p.mu.Lock()
		p.stdin = b
		p.mu.Unlock()
	}
	for _, ch := range p.sc.Chunks {
		if !p.write(ch) {
			return
		}
	}
	switch p.sc.Outcome {
	case Timeout:
		p.mu.Lock()
		p.timedOut = true
		p.mu.Unlock()
		p.kill()
	case Hang:
		select {
		case <-p.killCh:
		case <-p.ctx.Done():
			p.kill()
		}
	}
	_ = p.pw.Close()
}

// write delivers one chunk, giving up when the process is killed or the context ends.
func (p *Proc) write(b []byte) bool {
	errc := make(chan error, 1)
	go func() { _, err := p.pw.Write(b); errc <- err }()
	select {
	case err := <-errc:
		return err == nil
	case <-p.killCh:
		_ = p.pw.CloseWithError(io.EOF)
		return false
	case <-p.ctx.Done():
		p.kill()
		_ = p.pw.CloseWithError(io.EOF)
		return false
	}
}

func (p *Proc) kill() {
	p.killOnce.Do(func() {
		p.mu.Lock()
		p.killed = true
		p.mu.Unlock()
		close(p.killCh)
	})
}

// Stdout implements execx.Process.
func (p *Proc) Stdout() io.Reader { return p.pr }

// Kill implements execx.Process.
func (p *Proc) Kill() {
	p.kill()
	// A reader blocked on stdout must see the end of the stream.
	_ = p.pw.CloseWithError(io.EOF)
}

// Wait implements execx.Process.
func (p *Proc) Wait() (execx.StreamResult, error) {
	<-p.done
	p.mu.Lock()
	p.waited = true
	timedOut, killed := p.timedOut, p.killed
	p.mu.Unlock()
	res := execx.StreamResult{ExitCode: p.sc.ExitCode, Stderr: []byte(p.sc.Stderr)}
	switch {
	case timedOut:
		res.ExitCode = -1
		return res, execx.ErrTimeout
	case killed && p.ctx.Err() != nil:
		res.ExitCode = -1
		return res, p.ctx.Err()
	case killed:
		res.ExitCode = -1
	}
	return res, nil
}

var (
	_ execx.Streamer = (*Streamer)(nil)
	_ execx.Process  = (*Proc)(nil)
)
