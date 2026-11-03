package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
)

// Probe is migrate.Probe over HTTP: the router client of internal/router for what it already does
// (health, the router's own model list, the context size) and plain requests for the three things the
// contract check needs that it does not: /v1/models with and without the key, a chat completion under
// the legacy name, and the public URL. It is the only code of the migration that speaks HTTP; the key is
// read from the key files for every call and goes into the Authorization header and nowhere else.
type Probe struct {
	// Port is the router's port (8080).
	Port int
	// KeyFiles are tried in order: the new key file, then the old one (a rollback before the key was
	// copied still has to ask the old service).
	KeyFiles []string
	// FS reads them; default config.OSFS.
	FS config.FS
	// Timeout bounds one loopback call; ChatTimeout the completion (a CPU-only node is slow).
	Timeout, ChatTimeout time.Duration
	// BaseURL is the router's address; default http://127.0.0.1:<Port>. Tests point it at httptest.
	BaseURL string
	// PublicURL builds the public URL of a host; default https://<host>/v1/models. Public is asked
	// through HTTP, a client that follows no redirect, whatever the proxy environment says.
	PublicURL func(host string) string
	HTTP      *http.Client

	rc *router.Client
}

var _ migrate.Probe = (*Probe)(nil)

var errNoKey = errors.New("the API key cannot be read")

// keyChain is the router.KeySource over the key files.
type keyChain struct{ p *Probe }

func (k keyChain) Key() (string, error) {
	fsys := k.p.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	for _, path := range k.p.KeyFiles {
		data, err := fsys.ReadFile(path)
		if err != nil {
			continue
		}
		if key := strings.TrimSpace(string(data)); key != "" && len(key) <= 512 {
			return key, nil
		}
	}
	return "", errNoKey
}

func (p *Probe) base() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	port := p.Port
	if port == 0 {
		port = router.DefaultPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

func (p *Probe) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 10 * time.Second
}

func (p *Probe) client() (*router.Client, error) {
	if p.rc != nil {
		return p.rc, nil
	}
	c, err := router.NewClient(router.Options{BaseURL: p.base(), Keys: keyChain{p}, Timeout: p.timeout(), ChatTimeout: p.chatTimeout()})
	if err != nil {
		return nil, err
	}
	p.rc = c
	return c, nil
}

func (p *Probe) chatTimeout() time.Duration {
	if p.ChatTimeout > 0 {
		return p.ChatTimeout
	}
	return 3 * time.Minute
}

// Health implements migrate.Probe.
func (p *Probe) Health(ctx context.Context) error {
	c, err := p.client()
	if err != nil {
		return err
	}
	return c.Health(ctx)
}

// Models implements migrate.Probe.
func (p *Probe) Models(ctx context.Context) ([]migrate.ModelState, error) {
	c, err := p.client()
	if err != nil {
		return nil, err
	}
	ms, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]migrate.ModelState, 0, len(ms))
	for _, m := range ms {
		out = append(out, migrate.ModelState{ID: m.ID, Status: string(m.Status), Failed: m.Failed})
	}
	return out, nil
}

// NCtx implements migrate.Probe.
func (p *Probe) NCtx(ctx context.Context, id string) (int, error) {
	c, err := p.client()
	if err != nil {
		return 0, err
	}
	props, err := c.Props(ctx, id)
	if err != nil {
		return 0, err
	}
	return props.NCtx, nil
}

// loopbackClient never uses a proxy, never follows a redirect and dials loopback addresses only, so the
// key cannot travel anywhere else.
func (p *Probe) loopbackClient(timeout time.Duration) *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:              nil,
			DisableCompression: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
					return nil, fmt.Errorf("host: %s is not a loopback address", host)
				}
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (p *Probe) do(ctx context.Context, hc *http.Client, method, url string, body []byte, withKey bool) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withKey {
		key, err := keyChain{p}.Key()
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, scrub(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, data, err
}

// scrub keeps a transport error from carrying the request's headers anywhere (it never does, but the
// guarantee is cheap): only the text of the cause remains.
func scrub(err error) error { return errors.New(err.Error()) }

// V1Models implements migrate.Probe.
func (p *Probe) V1Models(ctx context.Context, withKey bool) (int, []migrate.V1Model, error) {
	st, data, err := p.do(ctx, p.loopbackClient(p.timeout()), http.MethodGet, p.base()+"/v1/models", nil, withKey)
	if err != nil || st != http.StatusOK {
		return st, nil, err
	}
	var doc struct {
		Data []struct {
			ID      string   `json:"id"`
			Aliases []string `json:"aliases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return st, nil, fmt.Errorf("GET /v1/models: the answer is not the expected JSON")
	}
	out := make([]migrate.V1Model, 0, len(doc.Data))
	for _, d := range doc.Data {
		out = append(out, migrate.V1Model{ID: d.ID, Aliases: d.Aliases})
	}
	return st, out, nil
}

// Chat implements migrate.Probe.
func (p *Probe) Chat(ctx context.Context, model string, maxTokens int) (int, error) {
	body, err := json.Marshal(map[string]any{
		"model": model, "max_tokens": maxTokens, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "Say hello."}},
	})
	if err != nil {
		return 0, err
	}
	st, _, err := p.do(ctx, p.loopbackClient(p.chatTimeout()), http.MethodPost, p.base()+"/v1/chat/completions", body, true)
	return st, err
}

// Public implements migrate.Probe.
func (p *Probe) Public(ctx context.Context, host string) (int, error) {
	url := "https://" + host + "/v1/models"
	if p.PublicURL != nil {
		url = p.PublicURL(host)
	}
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	st, _, err := p.do(ctx, hc, http.MethodGet, url, nil, true)
	return st, err
}
