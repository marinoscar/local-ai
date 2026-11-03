package app

import (
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// notifyListener is a fake $NOTIFY_SOCKET: a datagram socket that records what systemd would read.
type notifyListener struct {
	conn *net.UnixConn
	name string // the value of NOTIFY_SOCKET
	msgs chan string
}

func newNotifyListener(t *testing.T, abstract bool) *notifyListener {
	t.Helper()
	name := filepath.Join(t.TempDir(), "n.sock")
	addr := &net.UnixAddr{Name: name, Net: "unixgram"}
	display := name
	if abstract {
		if runtime.GOOS != "linux" {
			t.Skip("abstract sockets are Linux only")
		}
		raw := fmt.Sprintf("lai-test-%d-%d", time.Now().UnixNano(), len(t.Name()))
		addr = &net.UnixAddr{Name: "\x00" + raw, Net: "unixgram"}
		display = "@" + raw
	}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l := &notifyListener{conn: conn, name: display, msgs: make(chan string, 64)}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				return
			}
			l.msgs <- string(buf[:n])
		}
	}()
	return l
}

// next waits for one message. The wait ends when the datagram arrives, not after a fixed time.
func (l *notifyListener) next(t *testing.T) string {
	t.Helper()
	select {
	case m := <-l.msgs:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no sd_notify message arrived")
		return ""
	}
}

func TestNotifierSendsOverFilesystemAndAbstractSockets(t *testing.T) {
	for _, abstract := range []bool{false, true} {
		name := "filesystem"
		if abstract {
			name = "abstract"
		}
		t.Run(name, func(t *testing.T) {
			l := newNotifyListener(t, abstract)
			n := NewNotifier(l.name)
			if !n.Enabled() {
				t.Fatal("a notifier with a socket must be enabled")
			}
			steps := []struct {
				send func() error
				want string
			}{
				{n.Ready, "READY=1"},
				{n.Watchdog, "WATCHDOG=1"},
				{func() error { return n.Status("serving\nmodels") }, "STATUS=serving models"},
				{n.Stopping, "STOPPING=1"},
			}
			for _, s := range steps {
				if err := s.send(); err != nil {
					t.Fatalf("send %q: %v", s.want, err)
				}
				if got := l.next(t); got != s.want {
					t.Errorf("message = %q, want %q", got, s.want)
				}
			}
			if got := n.Sent(); len(got) != len(steps) {
				t.Errorf("Sent() = %v", got)
			}
		})
	}
}

func TestNotifierIsANoOpWithoutASocket(t *testing.T) {
	for name, n := range map[string]*Notifier{"empty": NewNotifier(""), "nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			if n.Enabled() {
				t.Error("must not be enabled")
			}
			if err := n.Ready(); err != nil {
				t.Errorf("Ready: %v", err)
			}
			if err := n.Watchdog(); err != nil {
				t.Errorf("Watchdog: %v", err)
			}
			if err := n.Stopping(); err != nil {
				t.Errorf("Stopping: %v", err)
			}
			if got := n.Sent(); len(got) != 0 {
				t.Errorf("a no-op notifier recorded %v", got)
			}
		})
	}
}

func TestNotifierErrors(t *testing.T) {
	tests := []struct{ name, socket string }{
		{"relative path", "run/notify"},
		{"no listener", filepath.Join(t.TempDir(), "gone.sock")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := NewNotifier(tc.socket).Ready(); err == nil {
				t.Error("want an error for a socket that cannot be reached")
			}
		})
	}
}

func TestWatchdogInterval(t *testing.T) {
	tests := []struct {
		usec string
		want time.Duration
	}{
		{"", DefaultWatchdogEvery},
		{"60000000", DefaultWatchdogEvery},  // 60 s / 3 = 20 s
		{"30000000", 10 * time.Second},      // shorter timeout: ping more often
		{"600000000", DefaultWatchdogEvery}, // longer timeout: never slower than 20 s
		{"garbage", DefaultWatchdogEvery},   // ignored
		{"-5", DefaultWatchdogEvery},        // ignored
		{"0", DefaultWatchdogEvery},         // ignored
	}
	for _, tc := range tests {
		got := WatchdogInterval(tc.usec)
		if got != tc.want {
			t.Errorf("WatchdogInterval(%q) = %v, want %v", tc.usec, got, tc.want)
		}
	}
}
