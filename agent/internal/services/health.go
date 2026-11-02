package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Ports are the loopback ports of the three services (node.yaml ports.llama, ports.kiwix and
// ports.whisper; they are restart-only, so they are read once).
type Ports struct {
	Llama, Kiwix, Whisper int
}

// Probe is one health check: an HTTP GET on a loopback URL that must answer 200 within Timeout. It is
// repeated every Interval and on every state change (agent.md section 4.3.3).
type Probe struct {
	// Service is the short name: llama, kiwix or whisper.
	Service  string
	URL      string
	Timeout  time.Duration
	Interval time.Duration
}

// Probes returns the health checks of the three services. llama: GET /health within 2 s, every 15 s.
// kiwix and whisper follow companion-services.md section 4.6, which is authoritative: kiwix
// GET /library/catalog/v2/entries?count=1 and whisper GET / , each 200 within 3 s, every 30 s.
func Probes(p Ports) []Probe {
	u := func(port int, path string) string { return "http://127.0.0.1:" + strconv.Itoa(port) + path }
	return []Probe{
		{Service: "llama", URL: u(p.Llama, "/health"), Timeout: 2 * time.Second, Interval: 15 * time.Second},
		{Service: "kiwix", URL: u(p.Kiwix, "/library/catalog/v2/entries?count=1"), Timeout: 3 * time.Second, Interval: 30 * time.Second},
		{Service: "whisper", URL: u(p.Whisper, "/"), Timeout: 3 * time.Second, Interval: 30 * time.Second},
	}
}

// Prober runs a Probe. HTTPProber is the real one; tests use a fake.
type Prober interface {
	// Check returns nil when the service answered as a healthy one does, and an error that says why not.
	Check(ctx context.Context, p Probe) error
}

// HTTPDoer is the part of *http.Client the prober uses.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPProber checks a Probe with an HTTP GET.
type HTTPProber struct {
	Client HTTPDoer
}

// NewHTTPProber returns an HTTPProber with a client that never follows a redirect (a health answer is
// the first answer), never reuses a connection (a probe must not ride on a dead socket of the service
// it just restarted), and uses no proxy.
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{Client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableKeepAlives: true, Proxy: nil},
	}}
}

// Check implements Prober: 200 within the probe's timeout is healthy, anything else is not.
func (h *HTTPProber) Check(ctx context.Context, p Probe) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return fmt.Errorf("services: health request for %s: %w", p.Service, err)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("services: %s does not answer: %w", p.Service, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("services: %s answered %d", p.Service, resp.StatusCode)
	}
	return nil
}

var _ Prober = (*HTTPProber)(nil)
