package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sd_notify (agent.md section 4.1 steps 11, section 4.12 step 1, issue AGENT-20): the agent tells
// systemd it is ready, that it is alive and that it is stopping over the datagram socket named by
// $NOTIFY_SOCKET. The protocol is a few lines of text, so it is implemented here in pure Go instead
// of pulling in a dependency (the static binary must rebuild offline, ADR-0001).

// EnvNotifySocket is the variable systemd sets for a Type=notify service.
const EnvNotifySocket = "NOTIFY_SOCKET"

// EnvWatchdogUsec is the variable systemd sets when WatchdogSec= is configured: the timeout in
// microseconds.
const EnvWatchdogUsec = "WATCHDOG_USEC"

// DefaultWatchdogEvery is how often WATCHDOG=1 is sent while the main loop is healthy (agent.md
// section 3.1). WatchdogSec=60 in the unit leaves two missed pings of slack.
const DefaultWatchdogEvery = 20 * time.Second

// The sd_notify messages the agent sends.
const (
	MsgReady    = "READY=1"
	MsgWatchdog = "WATCHDOG=1"
	MsgStopping = "STOPPING=1"
)

// Notifier sends sd_notify messages. The zero value and a Notifier made with an empty socket name
// are no-ops, which is what the agent is when it runs outside systemd (a development machine, a
// test, `aiagent serve` by hand).
type Notifier struct {
	socket string

	mu   sync.Mutex
	sent []string // what was sent, newest last, for the tests and the shutdown log
}

// NewNotifier returns a Notifier for the socket named in $NOTIFY_SOCKET. An empty name is a no-op
// Notifier. A name starting with "@" is an abstract socket (the leading "@" stands for a NUL byte).
func NewNotifier(socket string) *Notifier { return &Notifier{socket: socket} }

// Enabled reports whether messages go anywhere.
func (n *Notifier) Enabled() bool { return n != nil && n.socket != "" }

// Send writes one datagram. With no socket it does nothing and returns nil. The error of a socket
// that cannot be reached is returned for the log, and never makes the agent stop: systemd's own
// timeouts are the safety net.
func (n *Notifier) Send(msg string) error {
	if !n.Enabled() {
		return nil
	}
	n.mu.Lock()
	n.sent = append(n.sent, msg)
	n.mu.Unlock()
	addr, err := notifyAddr(n.socket)
	if err != nil {
		return err
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("sd_notify: %w", err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("sd_notify: %w", err)
	}
	return nil
}

// Ready sends READY=1 (startup step 11).
func (n *Notifier) Ready() error { return n.Send(MsgReady) }

// Watchdog sends WATCHDOG=1.
func (n *Notifier) Watchdog() error { return n.Send(MsgWatchdog) }

// Stopping sends STOPPING=1 (shutdown step 1).
func (n *Notifier) Stopping() error { return n.Send(MsgStopping) }

// Status sends STATUS=<text>, the line `systemctl status` shows. Newlines are not allowed in the
// protocol and are replaced by spaces.
func (n *Notifier) Status(text string) error {
	return n.Send("STATUS=" + strings.NewReplacer("\n", " ", "\r", " ").Replace(text))
}

// Sent returns the messages sent so far, oldest first (tests; a no-op Notifier records nothing).
func (n *Notifier) Sent() []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.sent...)
}

func notifyAddr(socket string) (*net.UnixAddr, error) {
	switch {
	case strings.HasPrefix(socket, "@"):
		return &net.UnixAddr{Name: "\x00" + socket[1:], Net: "unixgram"}, nil
	case strings.HasPrefix(socket, "/"):
		return &net.UnixAddr{Name: socket, Net: "unixgram"}, nil
	}
	return nil, fmt.Errorf("sd_notify: %s=%q is neither an absolute path nor an abstract name", EnvNotifySocket, socket)
}

// WatchdogInterval is how often the pinger runs: DefaultWatchdogEvery, or a third of the timeout
// systemd announced in $WATCHDOG_USEC when that is shorter (systemd advises pinging at least twice
// per timeout). An unparsable value is ignored.
func WatchdogInterval(usec string) time.Duration {
	every := DefaultWatchdogEvery
	if usec == "" {
		return every
	}
	n, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || n <= 0 {
		return every
	}
	if third := time.Duration(n) * time.Microsecond / 3; third > 0 && third < every {
		return third
	}
	return every
}
