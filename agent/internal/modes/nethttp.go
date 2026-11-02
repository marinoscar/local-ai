package modes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// HTTPDoer is the HTTP client behind an interface, for the prober and the reachability check.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newProbeClient is the dedicated client of the prober: no proxy env, no keep-alive, every phase
// bounded by timeout, redirects reported rather than followed.
func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}
