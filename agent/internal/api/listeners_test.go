package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortTempDir returns a temp directory with a short path: unix socket paths are limited to about
// 100 bytes and t.TempDir() names include the test name.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type chownCall struct {
	path     string
	uid, gid int
}

// chownRecorder is the fake chown hook: it records instead of changing the owner (the tests do not
// run as root).
type chownRecorder struct {
	mu    sync.Mutex
	calls []chownCall
	err   error
}

func (c *chownRecorder) chown(path string, uid, gid int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, chownCall{path, uid, gid})
	return c.err
}

const testGID = 4242

// listenServer starts a server on an ephemeral loopback port and a socket in a temp dir.
func listenServer(t *testing.T, mod ...func(*Options)) (*Server, *chownRecorder, *syncBuf) {
	t.Helper()
	rec := &chownRecorder{}
	dir := shortTempDir(t)
	all := append([]func(*Options){func(o *Options) {
		o.TCPAddr = "127.0.0.1:0"
		o.SocketPath = filepath.Join(dir, "run", "agent.sock")
		o.Hooks = Hooks{
			LookupGID: func(name string) (int, error) {
				if name != DefaultSocketGroup {
					return 0, errors.New("unknown group " + name)
				}
				return testGID, nil
			},
			Chown: rec.chown,
		}
	}}, mod...)
	s, logs := newTestServer(t, all...)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s, rec, logs
}

func socketClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		DisableKeepAlives: true,
	}, Timeout: 5 * time.Second}
}

func get(t *testing.T, c *http.Client, url string, hdr map[string]string, host string) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if host != "" {
		r.Host = host
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestServesOnBothListeners(t *testing.T) {
	s, _, _ := listenServer(t)
	tcpPort := s.TCPAddr().(*net.TCPAddr).Port
	base := "http://127.0.0.1:" + itoa(tcpPort)
	tcp := &http.Client{Timeout: 5 * time.Second}

	t.Run("TCP", func(t *testing.T) {
		resp, body := get(t, tcp, base+"/api/v1/health", nil, "")
		if resp.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if !ValidRequestID(resp.Header.Get("X-Request-Id")) {
			t.Fatal("no request id")
		}
		_, body = get(t, tcp, base+"/api/v1/info", nil, "")
		if !strings.Contains(body, `"transport":"tcp"`) || !strings.Contains(body, `"peer":""`) {
			t.Fatalf("TCP request info: %s", body)
		}
	})
	t.Run("TCP refuses a bad Host and a bad Origin on the wire", func(t *testing.T) {
		resp, body := get(t, tcp, base+"/api/v1/health", nil, "evil.example:"+itoa(tcpPort))
		if resp.StatusCode != 403 || !strings.Contains(body, CodeBadHost) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		resp, body = get(t, tcp, base+"/api/v1/health", map[string]string{"Origin": "https://evil.example"}, "")
		if resp.StatusCode != 403 || !strings.Contains(body, CodeBadOrigin) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		// The bound port, not the configured one, is what Host must name.
		resp, _ = get(t, tcp, base+"/api/v1/health", nil, "localhost:"+itoa(tcpPort))
		if resp.StatusCode != 200 {
			t.Fatalf("localhost host refused: %d", resp.StatusCode)
		}
	})
	t.Run("socket", func(t *testing.T) {
		c := socketClient(s.SocketPath())
		resp, body := get(t, c, "http://agent/api/v1/health", nil, "")
		if resp.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		// Host is ignored on the socket: even an arbitrary one is served.
		resp, _ = get(t, c, "http://agent/api/v1/health", nil, "evil.example")
		if resp.StatusCode != 200 {
			t.Fatalf("socket must ignore Host, got %d", resp.StatusCode)
		}
		// And so is Origin.
		resp, _ = get(t, c, "http://agent/api/v1/health", map[string]string{"Origin": "https://evil.example"}, "")
		if resp.StatusCode != 200 {
			t.Fatalf("socket must ignore Origin, got %d", resp.StatusCode)
		}
	})
	t.Run("the socket peer credentials reach the handler", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("SO_PEERCRED is Linux only")
		}
		_, body := get(t, socketClient(s.SocketPath()), "http://agent/api/v1/info", nil, "")
		if !strings.Contains(body, `"transport":"socket"`) || !strings.Contains(body, `"peer":"peer"`) {
			t.Fatalf("socket request info: %s", body)
		}
	})
	t.Run("peer credentials equal this process", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("SO_PEERCRED is Linux only")
		}
		var got *PeerCred
		var gotT Transport
		s2, _, _ := listenServer(t, func(o *Options) {
			o.Auth = AuthFunc(func(r *http.Request, rt *Route) (Principal, error) {
				info := InfoFrom(r.Context())
				got, gotT = info.Peer, info.Transport
				return Anonymous(), nil
			})
		})
		get(t, socketClient(s2.SocketPath()), "http://agent/api/v1/open", nil, "")
		if gotT != TransportSocket || got == nil || got.UID != os.Getuid() || got.GID != os.Getgid() || got.PID != os.Getpid() {
			t.Fatalf("transport %q peer %+v, want uid %d gid %d pid %d", gotT, got, os.Getuid(), os.Getgid(), os.Getpid())
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestSocketPermissions(t *testing.T) {
	s, rec, _ := listenServer(t)
	fi, err := os.Lstat(s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != os.ModeSocket || fi.Mode().Perm() != 0o660 {
		t.Fatalf("mode = %v, want a 0660 socket", fi.Mode())
	}
	if len(rec.calls) != 1 || rec.calls[0].uid != 0 || rec.calls[0].gid != testGID {
		t.Fatalf("chown calls = %+v, want one call to root:%d", rec.calls, testGID)
	}
	// The owner is set before the socket appears under its final name, and no helper directory is left.
	entries, _ := os.ReadDir(filepath.Dir(s.SocketPath()))
	if len(entries) != 1 || entries[0].Name() != "agent.sock" {
		t.Fatalf("leftovers next to the socket: %v", entries)
	}
	if strings.HasSuffix(rec.calls[0].path, "agent.sock") {
		t.Fatalf("chown ran on the final path %s, after the socket was reachable", rec.calls[0].path)
	}
}

func TestSocketSetupFailures(t *testing.T) {
	hooks := func(chown func(string, int, int) error, lookup func(string) (int, error)) Hooks {
		if lookup == nil {
			lookup = func(string) (int, error) { return testGID, nil }
		}
		return Hooks{LookupGID: lookup, Chown: chown}
	}
	t.Run("group missing", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		_, berr := listenSocket(path, "nogroup-x", hooks(nil, func(string) (int, error) { return 0, errors.New("no such group") }))
		if berr == nil || berr.Code != CodeSocketBind || !strings.Contains(berr.Error(), "nogroup-x") {
			t.Fatalf("berr = %v", berr)
		}
		if _, err := os.Lstat(path); err == nil {
			t.Fatal("a socket was left behind")
		}
	})
	t.Run("chown fails: nothing is left behind", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		_, berr := listenSocket(path, DefaultSocketGroup, hooks(func(string, int, int) error { return errors.New("operation not permitted") }, nil))
		if berr == nil || berr.Code != CodeSocketBind {
			t.Fatalf("berr = %v", berr)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("leftovers: %v", entries)
		}
	})
	t.Run("parent is a file", func(t *testing.T) {
		dir := shortTempDir(t)
		parent := filepath.Join(dir, "file")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, berr := listenSocket(filepath.Join(parent, "a.sock"), DefaultSocketGroup, hooks(nil, nil))
		if berr == nil || berr.Code != CodeSocketBind {
			t.Fatalf("berr = %v", berr)
		}
	})
	t.Run("a stale socket file is replaced", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		old.SetUnlinkOnClose(false)
		_ = old.Close() // the file stays, nobody listens: what a crash leaves
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("setup: stale socket missing")
		}
		ln, berr := listenSocket(path, DefaultSocketGroup, hooks(func(string, int, int) error { return nil }, nil))
		if berr != nil {
			t.Fatalf("stale socket not replaced: %v", berr)
		}
		defer ln.Close()
		if c, err := net.Dial("unix", path); err != nil {
			t.Fatalf("new socket does not accept: %v", err)
		} else {
			_ = c.Close()
		}
	})
	t.Run("a live socket is never taken over", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		live, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer live.Close()
		_, berr := listenSocket(path, DefaultSocketGroup, hooks(func(string, int, int) error { return nil }, nil))
		if berr == nil || !strings.Contains(berr.Error(), "in use") {
			t.Fatalf("berr = %v", berr)
		}
		if c, err := net.Dial("unix", path); err != nil {
			t.Fatalf("the live socket was disturbed: %v", err)
		} else {
			_ = c.Close()
		}
	})
	t.Run("a regular file is never removed", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, berr := listenSocket(path, DefaultSocketGroup, hooks(func(string, int, int) error { return nil }, nil))
		if berr == nil || !strings.Contains(berr.Error(), "not a socket") {
			t.Fatalf("berr = %v", berr)
		}
		if b, _ := os.ReadFile(path); string(b) != "precious" {
			t.Fatal("the file was touched")
		}
	})
	t.Run("close removes only our own file", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "a.sock")
		ln, berr := listenSocket(path, DefaultSocketGroup, hooks(func(string, int, int) error { return nil }, nil))
		if berr != nil {
			t.Fatal(berr)
		}
		// Someone replaces the file after us: Close must leave theirs.
		_ = os.Remove(path)
		other, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		_ = ln.Close()
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("Close removed a socket that is not ours")
		}
	})
}

func TestTCPBind(t *testing.T) {
	t.Run("only loopback", func(t *testing.T) {
		for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0", "localhost:0", "[::]:0", "nonsense"} {
			if ln, berr := listenTCP(addr); berr == nil {
				_ = ln.Close()
				t.Errorf("%s was accepted", addr)
			} else if berr.Code != CodeTCPBind {
				t.Errorf("%s: code %s", addr, berr.Code)
			}
		}
		ln, berr := listenTCP("127.0.0.1:0")
		if berr != nil {
			t.Fatal(berr)
		}
		_ = ln.Close()
		ln, berr = listenTCP("[::1]:0")
		if berr == nil {
			_ = ln.Close()
		}
	})
}

func TestPortInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	s, _, logs := listenServer(t, func(o *Options) { o.TCPAddr = taken.Addr().String() })

	tcpErr, sockErr := s.BindErrors()
	if tcpErr == nil || tcpErr.Code != CodeTCPBind || tcpErr.Transport != TransportTCP {
		t.Fatalf("tcp bind error = %v, want %s", tcpErr, CodeTCPBind)
	}
	if sockErr != nil {
		t.Fatalf("socket error = %v", sockErr)
	}
	if s.TCPAddr() != nil {
		t.Fatal("TCPAddr must be nil when the bind failed")
	}
	// The socket still serves, so aictl keeps working (agent.md section 5).
	resp, body := get(t, socketClient(s.SocketPath()), "http://agent/api/v1/health", nil, "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(logs.String(), CodeTCPBind) {
		t.Fatalf("the bind failure was not logged with its code:\n%s", logs.String())
	}
	// The foreign listener is untouched.
	if c, err := net.Dial("tcp", taken.Addr().String()); err != nil {
		t.Fatal(err)
	} else {
		_ = c.Close()
	}
}

func TestSocketFailureLeavesTCP(t *testing.T) {
	dir := shortTempDir(t)
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, _ := listenServer(t, func(o *Options) { o.SocketPath = filepath.Join(blocker, "agent.sock") })
	tcpErr, sockErr := s.BindErrors()
	if tcpErr != nil || sockErr == nil || sockErr.Code != CodeSocketBind {
		t.Fatalf("errors = %v, %v", tcpErr, sockErr)
	}
	resp, _ := get(t, &http.Client{Timeout: 5 * time.Second}, "http://127.0.0.1:"+itoa(s.TCPAddr().(*net.TCPAddr).Port)+"/api/v1/health", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("TCP does not serve: %d", resp.StatusCode)
	}
}

func TestBothListenersFail(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	dir := shortTempDir(t)
	blocker := filepath.Join(dir, "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	s, _ := newTestServer(t, func(o *Options) {
		o.TCPAddr = taken.Addr().String()
		o.SocketPath = filepath.Join(blocker, "agent.sock")
		o.Hooks = Hooks{LookupGID: func(string) (int, error) { return testGID, nil }}
	})
	err = s.Start(context.Background())
	var le *ListenError
	if !errors.As(err, &le) {
		t.Fatalf("Start error = %v, want *ListenError", err)
	}
	var be *BindError
	if !errors.As(err, &be) || (be.Code != CodeTCPBind && be.Code != CodeSocketBind) {
		t.Fatalf("errors.As(*BindError) failed on %v", err)
	}
	if le.TCP.Code != CodeTCPBind || le.Socket.Code != CodeSocketBind {
		t.Fatalf("codes %s, %s", le.TCP.Code, le.Socket.Code)
	}
	if !strings.Contains(err.Error(), CodeTCPBind) || !strings.Contains(err.Error(), CodeSocketBind) {
		t.Fatalf("message lacks the codes: %v", err)
	}
	_ = s.Stop(context.Background())
}

func TestStartTwiceAndStop(t *testing.T) {
	s, _, _ := listenServer(t)
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("second Start accepted")
	}
	addr := s.TCPAddr().String()
	path := s.SocketPath()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("the socket file survived Stop")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the port is still open after Stop")
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStopWaitsForInFlightRequests(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	reg := NewRegistry()
	_ = reg.Add(Route{Method: "GET", Pattern: "/hold", Auth: []Level{AuthNone}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})})
	s, _, _ := listenServer(t, func(o *Options) { o.Registry = reg })
	c := socketClient(s.SocketPath())

	result := make(chan string, 1)
	go func() {
		_, body := get(t, c, "http://agent/api/v1/hold", nil, "")
		result <- body
	}()
	<-started
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- s.Stop(ctx)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a request was in flight")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if body := <-result; body != "done" {
		t.Fatalf("in-flight request got %q", body)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestStopCutsStragglersWhenTheBudgetIsSpent(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	reg := NewRegistry()
	_ = reg.Add(Route{Method: "GET", Pattern: "/hold", Auth: []Level{AuthNone}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})})
	s, _, _ := listenServer(t, func(o *Options) { o.Registry = reg })
	go func() {
		c := socketClient(s.SocketPath())
		resp, err := c.Get("http://agent/api/v1/hold")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want the exceeded deadline", err)
	}
	if _, err := os.Lstat(s.SocketPath()); err == nil {
		t.Fatal("the socket file survived")
	}
}

func TestDeadlineOnAKeptAliveConnection(t *testing.T) {
	// A route deadline sets connection deadlines; a stale one must not break the next request on
	// the same connection.
	s, _, _ := listenServer(t)
	base := "http://127.0.0.1:" + itoa(s.TCPAddr().(*net.TCPAddr).Port)
	c := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 3; i++ {
		resp, _ := get(t, c, base+"/api/v1/health", nil, "")
		if resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealPostThroughTheWire(t *testing.T) {
	s, _, _ := listenServer(t)
	base := "http://127.0.0.1:" + itoa(s.TCPAddr().(*net.TCPAddr).Port)
	post := func(ct, body string) (*http.Response, string) {
		r, _ := http.NewRequest("POST", base+"/api/v1/echo", strings.NewReader(body))
		r.Header.Set("X-Test-Principal", "bearer")
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	if resp, body := post("application/json", `{"name":"x"}`); resp.StatusCode != 200 || !strings.Contains(body, `"name":"x"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, body := post("text/plain", `{"name":"x"}`); resp.StatusCode != 415 || !strings.Contains(body, CodeInvalidRequest) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, body := post("application/json", `{"name":"`+strings.Repeat("a", int(DefaultMaxBody))+`"}`); resp.StatusCode != 413 || !strings.Contains(body, CodeTooLarge) {
		t.Fatalf("%d %.100s", resp.StatusCode, body)
	}
}

func TestWorkerName(t *testing.T) {
	s, _ := newTestServer(t)
	if s.Name() != "api" {
		t.Fatalf("Name() = %q", s.Name())
	}
}
