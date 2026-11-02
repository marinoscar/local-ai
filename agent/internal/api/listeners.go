package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

// Defaults of the unix socket (agent.md section 4.1 step 10).
const (
	DefaultSocketPath  = "/run/localai/agent.sock"
	DefaultSocketGroup = "localai-operators"
	socketMode         = 0o660
)

// BindError is a listener that could not start. Code is CodeTCPBind (LAI-AGT-006) for the TCP
// port and CodeSocketBind (LAI-AGT-005) for the unix socket. Whether it is fatal is the caller's
// decision: the agent survives either one and dies only when both fail.
type BindError struct {
	Code      string
	Transport Transport
	Addr      string
	Err       error
}

// Error describes the failure; the code leads so a log line is searchable.
func (e *BindError) Error() string {
	return fmt.Sprintf("%s: cannot listen on %s %s: %v", e.Code, e.Transport, e.Addr, e.Err)
}

// Unwrap returns the underlying error.
func (e *BindError) Unwrap() error { return e.Err }

// ListenError is returned by Start when neither listener could start.
type ListenError struct{ TCP, Socket *BindError }

// Error joins the two bind errors.
func (e *ListenError) Error() string {
	return "api: no listener could start: " + e.TCP.Error() + "; " + e.Socket.Error()
}

// Unwrap returns both bind errors (for errors.Is and errors.As).
func (e *ListenError) Unwrap() []error { return []error{e.TCP, e.Socket} }

// Hooks are the operating-system seams of the socket setup, replaced in tests.
type Hooks struct {
	// LookupGID resolves a group name to its gid. Default: os/user.LookupGroup.
	LookupGID func(name string) (int, error)
	// Chown sets the owner of path. Default: os.Chown.
	Chown func(path string, uid, gid int) error
}

func (h Hooks) lookupGID(name string) (int, error) {
	if h.LookupGID != nil {
		return h.LookupGID(name)
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

func (h Hooks) chown(path string, uid, gid int) error {
	if h.Chown != nil {
		return h.Chown(path, uid, gid)
	}
	return os.Chown(path, uid, gid)
}

// listenTCP binds addr, which must be a loopback address: the agent never listens beyond 127.0.0.1.
func listenTCP(addr string) (net.Listener, *BindError) {
	fail := func(err error) (net.Listener, *BindError) {
		return nil, &BindError{Code: CodeTCPBind, Transport: TransportTCP, Addr: addr, Err: err}
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fail(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fail(errors.New("refusing a non-loopback address"))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fail(err)
	}
	return ln, nil
}

// unixSocket is the bound socket and the identity of its file, so Close removes only the file this
// process created.
type unixSocket struct {
	*net.UnixListener
	path string
	info os.FileInfo
}

// Close stops listening and removes the socket file, unless another process has replaced it.
func (u *unixSocket) Close() error {
	err := u.UnixListener.Close()
	if fi, serr := os.Lstat(u.path); serr == nil && os.SameFile(fi, u.info) {
		_ = os.Remove(u.path)
	}
	return err
}

// listenSocket creates the agent socket: root:<group> 0660, replacing a stale file (agent.md
// section 4.1 step 10).
//
// The socket is bound inside a private 0700 directory next to the final path, given its owner and
// mode there, and renamed into place. There is no moment at which a socket with the default
// permissions exists where others can reach it, and a stale file is replaced atomically. A live
// socket (another agent answers on it) and a file that is not a socket are refused, never removed.
func listenSocket(path, group string, hooks Hooks) (*unixSocket, *BindError) {
	fail := func(err error) (*unixSocket, *BindError) {
		return nil, &BindError{Code: CodeSocketBind, Transport: TransportSocket, Addr: path, Err: err}
	}
	gid, err := hooks.lookupGID(group)
	if err != nil {
		return fail(fmt.Errorf("group %q: %w", group, err))
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(err)
	}
	if err := clearStale(path); err != nil {
		return fail(err)
	}
	tmp, err := os.MkdirTemp(dir, ".sock-")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	tmpPath := filepath.Join(tmp, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmpPath, Net: "unix"})
	if err != nil {
		return fail(err)
	}
	ln.SetUnlinkOnClose(false) // unixSocket.Close removes the final path instead
	if err := hooks.chown(tmpPath, 0, gid); err != nil {
		_ = ln.Close()
		return fail(fmt.Errorf("chown root:%s: %w", group, err))
	}
	if err := os.Chmod(tmpPath, socketMode); err != nil {
		_ = ln.Close()
		return fail(err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = ln.Close()
		return fail(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return fail(err)
	}
	return &unixSocket{UnixListener: ln, path: path, info: fi}, nil
}

// clearStale removes a leftover socket file at path. It refuses when the file is not a socket, or
// when something is still listening on it.
func clearStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return fmt.Errorf("%s is in use by another process", path)
	}
	return os.Remove(path)
}
