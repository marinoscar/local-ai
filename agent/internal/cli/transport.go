package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// Transport defaults and limits (aictl.md section 3.2, agent.md section 3.7).
const (
	// DefaultSocket is the agent's unix socket (root:localai-operators 0660).
	DefaultSocket = "/run/localai/agent.sock"
	// DefaultURL is the TCP fallback base URL.
	DefaultURL = "http://127.0.0.1:7070"
	// DefaultTokenFile holds the agent token used over TCP.
	DefaultTokenFile = "/etc/localai/agent-token"
	// APIPrefix is the base path of every API route.
	APIPrefix = api.BasePath
	// DefaultRetryInterval and DefaultRetryWindow bound the retry while the agent starts: every
	// second for up to ten seconds, then exit 3.
	DefaultRetryInterval = time.Second
	DefaultRetryWindow   = 10 * time.Second

	// socketHost is the URL host of socket requests; the agent skips its Host check on the socket.
	socketHost = "localai"
	// maxResponseBody bounds what Do reads into memory.
	maxResponseBody = 32 << 20
)

// transportKind is how the client reaches the agent.
type transportKind int

const (
	viaNone transportKind = iota
	viaSocket
	viaTCP
)

func (k transportKind) String() string {
	switch k {
	case viaSocket:
		return "socket"
	case viaTCP:
		return "tcp"
	}
	return "none"
}

// parseAgentURL validates the TCP fallback URL. The token goes over it, so it must stay on this
// machine: http, a loopback host, no credentials, no path.
func parseAgentURL(raw string, lang Lang) (*url.URL, error) {
	bad := func() error {
		return NewCLIError(lang, CodeUsage, pick(lang,
			"The --url option must point to this computer (127.0.0.1, localhost or [::1]).",
			"La opción --url debe apuntar a este equipo (127.0.0.1, localhost o [::1]).")).
			WithDetails("url_option", "--url")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, bad()
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return u, nil
	}
	return nil, bad()
}

// pick returns the string for lang.
func pick(lang Lang, en, es string) string {
	if lang == LangES {
		return es
	}
	return en
}

// isDialError reports whether err is a failure to connect (as opposed to a failure on a connection
// that was made).
func isDialError(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "dial"
}

// isPermError reports whether err is the operating system refusing access.
func isPermError(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, os.ErrPermission)
}

// dialUnix is the default socket dialer.
func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// newSocketHTTP returns an HTTP client that sends every request to the unix socket. It never
// follows redirects and keeps no idle connections (the CLI is short-lived).
func (c *Client) newSocketHTTP() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.cfg.DialUnix(ctx, c.cfg.Socket)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// newTCPHTTP returns the client for the TCP fallback. Proxy is nil on purpose: the proxy
// environment must never see the token (aictl.md implementation note 3).
func newTCPHTTP() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// readToken reads the agent token (trimmed). It is called only when the socket did not work, so an
// operator without read access to the token file still succeeds over the socket.
func (c *Client) readToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	b, err := c.cfg.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", c.cfg.TokenFile)
	}
	c.token = tok
	return tok, nil
}

// unreachable builds the error for "neither transport worked". The socket error is the one that
// counts (it is the first and the most specific): EACCES there means the user may not talk to the
// agent (exit 4), anything else means the agent is not reachable (exit 3).
func (c *Client) unreachable(sockErr, other error) *CLIError {
	code := CodeUnreachable
	if isPermError(sockErr) {
		code = CodePermission
	}
	e := NewCLIError(c.cfg.Lang, code, "").WithCause(sockErr).WithDetails("socket", c.cfg.Socket, "socket_error", sockErr.Error())
	if other != nil {
		e.WithDetails("tcp_error", other.Error())
	}
	return e
}

// classify turns a transport error on a connection that was made into a CLIError: an expired
// deadline is LAI-CLI-004 (exit 7), Ctrl-C is LAI-CLI-010 (130), anything else means the agent went
// away (exit 3).
func (c *Client) classify(ctx context.Context, err error) *CLIError {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return NewCLIError(c.cfg.Lang, CodeTimeout, "").WithCause(context.DeadlineExceeded)
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return NewCLIError(c.cfg.Lang, CodeInterrupted, "").WithCause(context.Canceled)
	}
	return NewCLIError(c.cfg.Lang, CodeUnreachable, "").WithCause(err).WithDetails("error", err.Error())
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
