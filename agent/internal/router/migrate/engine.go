package migrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// Timing holds every wait of the migration; a zero field takes the default. Tests inject a Clock whose
// Sleep advances a manual time, so nothing ever really waits.
type Timing struct {
	// StopWait is how long the old unit may take to become inactive (M6): 30 s.
	StopWait time.Duration
	// HealthWait is how long /health may take after the new unit started (M7): 60 s.
	HealthWait time.Duration
	// LoadWait is how long the model may take to load (M7); 0 takes node.yaml's load timeout.
	LoadWait time.Duration
	// RollbackWait bounds the wait of R5 for the old service to answer: 120 s.
	RollbackWait time.Duration
	// Poll is the pause between two polls: 1 s.
	Poll time.Duration
	// Cmd bounds one external program; VersionCmd the --version of the legacy binary.
	Cmd, VersionCmd time.Duration
}

func (t Timing) withDefaults() Timing {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.StopWait, 30*time.Second)
	def(&t.HealthWait, 60*time.Second)
	def(&t.RollbackWait, 120*time.Second)
	def(&t.Poll, time.Second)
	def(&t.Cmd, 30*time.Second)
	def(&t.VersionCmd, 20*time.Second)
	return t
}

// Options configures New. Every port is required except where noted.
type Options struct {
	Paths   Paths
	FS      FS
	Exec    execx.Runner
	Lookup  func(name string) (string, error)
	Systemd Systemd
	// Agent is localai-agent.service; nil means there is none to stop.
	Agent    AgentUnit
	Users    Users
	Probe    Probe
	Builder  Builder
	Config   NodeConfig
	State    State
	Renderer Renderer
	Catalog  *catalog.Catalog
	// Audit records migration.* lines; nil records nothing.
	Audit AuditFunc
	Clock Clock
	// Out receives the progress lines and the report for people; nil discards them. It never receives
	// the key.
	Out    io.Writer
	Logger *slog.Logger
	Timing Timing
	// AcceptCatalogFlags is --accept-catalog-flags; RestoreFiles is --restore-files.
	AcceptCatalogFlags bool
	RestoreFiles       bool
	// RootUID and RootGID own the files the migration writes (0 and 0).
	RootUID, RootGID int
	// Version is the agent version, written into the manifests the migration creates.
	Version string
}

// Engine runs the migration. It is used by one goroutine at a time.
type Engine struct {
	paths   Paths
	fs      FS
	exec    execx.Runner
	lookup  func(string) (string, error)
	sd      Systemd
	agent   AgentUnit
	users   Users
	probe   Probe
	builder Builder
	config  NodeConfig
	state   State
	render  Renderer
	cat     *catalog.Catalog
	audit   AuditFunc
	clock   Clock
	out     io.Writer
	log     *slog.Logger
	timing  Timing

	acceptFlags  bool
	restoreFiles bool
	rootUID      int
	rootGID      int
	version      string

	prog Progress
}

// New returns an Engine.
func New(o Options) *Engine {
	e := &Engine{
		paths: o.Paths, fs: o.FS, exec: o.Exec, lookup: o.Lookup, sd: o.Systemd, agent: o.Agent, users: o.Users,
		probe: o.Probe, builder: o.Builder, config: o.Config, state: o.State, render: o.Renderer, cat: o.Catalog,
		audit: o.Audit, clock: o.Clock, out: o.Out, log: o.Logger, timing: o.Timing.withDefaults(),
		acceptFlags: o.AcceptCatalogFlags, restoreFiles: o.RestoreFiles, rootUID: o.RootUID, rootGID: o.RootGID,
		version: o.Version,
	}
	if e.lookup == nil {
		e.lookup = execx.Lookup
	}
	if e.out == nil {
		e.out = io.Discard
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	e.log = e.log.With("component", "migrate")
	return e
}

// ---- output ---------------------------------------------------------------------------------------

func (e *Engine) say(format string, a ...any) {
	fmt.Fprintf(e.out, format+"\n", a...)
}

func (e *Engine) warnf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	e.log.Warn(msg)
	fmt.Fprintf(e.out, "warning: %s\n", msg)
}

func (e *Engine) auditLine(action string, res audit.Result, code string, target map[string]string) {
	if e.audit == nil {
		return
	}
	if err := e.audit(action, res, code, target); err != nil {
		e.warnf("the audit log could not be written (%v)", err)
	}
}

// ---- time -----------------------------------------------------------------------------------------

// until polls cond every Poll until it returns true, an error, or max passes. It returns the last
// condition error; timedOut is true when the time ran out.
func (e *Engine) until(ctx context.Context, max time.Duration, cond func(context.Context) (bool, error)) (timedOut bool, err error) {
	deadline := e.clock.Now().Add(max)
	for {
		ok, cerr := cond(ctx)
		if ok {
			return false, nil
		}
		err = cerr
		if !e.clock.Now().Before(deadline) {
			return true, err
		}
		if serr := e.clock.Sleep(ctx, e.timing.Poll); serr != nil {
			return false, serr
		}
	}
}

// ---- programs -------------------------------------------------------------------------------------

// run runs a program with a fixed argv. A missing program is an error; a non-zero exit is in the result.
func (e *Engine) run(ctx context.Context, timeout time.Duration, path string, args ...string) (execx.Result, error) {
	if timeout <= 0 {
		timeout = e.timing.Cmd
	}
	return e.exec.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: timeout, MaxOut: 1 << 20})
}

// runNamed looks the program up in the fixed PATH and runs it.
func (e *Engine) runNamed(ctx context.Context, name string, args ...string) (execx.Result, error) {
	p, err := e.lookup(name)
	if err != nil {
		return execx.Result{}, err
	}
	return e.run(ctx, 0, p, args...)
}

// ---- files ----------------------------------------------------------------------------------------

func exists(fsys FS, p string) bool {
	_, err := fsys.Lstat(p)
	return err == nil
}

func isLink(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// nearestExisting returns p or its closest ancestor that exists.
func nearestExisting(fsys FS, p string) string {
	for {
		if exists(fsys, p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// ctxReader makes a long read stop when the context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sha256Of hashes a file (following a symbolic link).
func (e *Engine) sha256Of(ctx context.Context, path string) (string, error) {
	r, err := e.fs.OpenRead(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, r}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies src to dst (created exclusively, fsynced), hashing nothing: the caller verifies by
// reading dst again.
func (e *Engine) copyFile(ctx context.Context, src, dst string, mode os.FileMode) (err error) {
	in, err := e.fs.OpenRead(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := e.fs.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = e.fs.Remove(dst)
		}
	}()
	if _, err = io.Copy(out, ctxReader{ctx, in}); err != nil {
		return err
	}
	return out.Sync()
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readKey reads a key file and checks its shape; the bytes are returned as written (a byte copy keeps
// them). The error never contains the key.
func (e *Engine) readKey(path string) ([]byte, error) {
	data, err := e.fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !hex64.Match(bytes.TrimSpace(data)) {
		return nil, errBadKeyShape
	}
	return data, nil
}

var errBadKeyShape = errors.New("the key is not 64 lowercase hex digits")

// keyFingerprint is how the key is shown: first and last four characters.
func keyFingerprint(key []byte) string { return router.KeyFingerprint(string(bytes.TrimSpace(key))) }

// unitState reads a unit.
func (e *Engine) legacyActive(ctx context.Context) (bool, error) {
	st, err := e.sd.State(ctx, LegacyUnitName)
	if err != nil {
		return false, err
	}
	return st.ActiveState == "active" || st.ActiveState == "activating" || st.ActiveState == "reloading", nil
}

// isNotExist is true for the error of a missing file on any FS.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func trimNL(s string) string { return strings.TrimRight(s, "\r\n") }
