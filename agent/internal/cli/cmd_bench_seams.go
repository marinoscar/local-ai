package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/router/bench"
)

// The seams of the bench engine (internal/router/bench, llama-router.md section 4.13.1) over what
// aictl has: the agent client for models and events, the node's key for the one chat request, and the
// machine itself for the memory samples, the process list and llama-bench.

// benchHost is where `aictl bench` touches the machine. Tests replace benchHostFn with temporary
// directories and fakes; nothing else in the command knows a path.
type benchHost struct {
	// GPU reads the graphics memory; nil is the real sampler (the same probes as the agent).
	GPU bench.GPUSampler
	// Procs lists the router's processes (CPU-only nodes); nil reads /proc and the cgroup.
	Procs bench.Procs
	// Exec runs llama-bench; nil is execx.Exec.
	Exec execx.Runner
	// Dir, BenchBinary and BuildLink are the engine's locations ("" is its default).
	Dir, BenchBinary, BuildLink string
	// NodeYAML and DropIn are the node's configuration and the llama environment drop-in.
	NodeYAML, DropIn string
	// Now and Sleep are the engine's clock and wait (nil is the real thing).
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// OnEvent, when set, is called with each model event after it was handed to the engine. It is a
	// test hook: it lets a test know that an event has been taken in without waiting for a while.
	OnEvent func(agentevents.Event)
}

// benchHostFn returns the host of a run.
var benchHostFn = func() benchHost {
	return benchHost{
		GPU:      hardware.New(hardware.Options{}),
		Procs:    bench.NewProcFS(os.DirFS("/"), ""),
		NodeYAML: "/etc/localai/node.yaml",
		DropIn:   "/etc/systemd/system/localai-llama.service.d/50-env.conf",
	}
}

// benchNode is what bench reads from the node's configuration.
type benchNode struct {
	ID      string
	Backend string
	// Env is the environment of llama-bench: the merged llama environment of the node, so the
	// benchmark sees the GPU the way llama-server does. nil when there is none.
	Env []string
}

// readBenchNode reads node.yaml (leniently: a damaged file only means less detail) and the llama
// environment drop-in.
func readBenchNode(h benchHost) benchNode {
	var n benchNode
	env := map[string]string{}
	if data, err := os.ReadFile(h.DropIn); err == nil && h.DropIn != "" {
		for k, v := range parseDropIn(data) {
			env[k] = v
		}
	}
	if data, err := os.ReadFile(h.NodeYAML); err == nil && h.NodeYAML != "" && len(data) <= 1<<20 {
		cfg, _ := config.Parse(data, nil)
		n.ID, n.Backend = cfg.Node.ID, cfg.Llama.Backend
		for k, v := range cfg.Llama.ExtraEnv {
			env[k] = v // the node's value wins, as in the drop-in
		}
	}
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		n.Env = []string{"PATH=" + execx.FixedPath}
		for _, k := range keys {
			n.Env = append(n.Env, k+"="+env[k])
		}
	}
	return n
}

// parseDropIn reads the Environment= lines of the generated drop-in (llama-router.md section 3.3):
// NAME=value, or "NAME=value" in double quotes with \\ and \" escaped, and %% for a percent sign.
func parseDropIn(data []byte) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, `"`) && strings.HasSuffix(rest, `"`) && len(rest) >= 2 {
			rest = rest[1 : len(rest)-1]
			rest = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(rest)
		}
		rest = strings.ReplaceAll(rest, "%%", "%")
		if k, v, ok := strings.Cut(rest, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}

// benchAgent is bench.Agent over the agent client. It also says what is going on, because the engine
// has no progress hook of its own: the loads and unloads it asks for are its steps.
type benchAgent struct {
	c       *Call
	cl      *Client
	target  string
	names   modelNameMap
	say     func(string)
	cpu     bool
	onEvent func(agentevents.Event)

	mu           sync.Mutex
	ctxTokens    int64
	measureShown bool
	speedShown   bool
}

var _ bench.Agent = (*benchAgent)(nil)

func (a *benchAgent) get(ctx context.Context, path string, v any) error {
	_, _, err := a.c.callWith(a.cl, ctx, http.MethodGet, path, nil, v)
	return err
}

// Models implements bench.Agent: GET /models.
func (a *benchAgent) Models(ctx context.Context) ([]bench.ModelInfo, error) {
	var ms wireModels
	if err := a.get(ctx, "/models", &ms); err != nil {
		return nil, err
	}
	out := make([]bench.ModelInfo, 0, len(ms.Models))
	for _, m := range ms.Models {
		out = append(out, bench.ModelInfo{ID: m.ID, Engine: m.Engine, Present: m.Present, Verified: m.Verified, Status: m.Status})
	}
	return out, nil
}

// Model implements bench.Agent: GET /models/{id} with the router preset.
func (a *benchAgent) Model(ctx context.Context, id string) (bench.ModelInfo, error) {
	var m struct {
		wireModel
		Preset string `json:"preset"`
	}
	if err := a.get(ctx, modelPath(id, ""), &m); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == doctor.AGT021 {
			return bench.ModelInfo{}, fmt.Errorf("%w: %s", bench.ErrUnknownModel, id)
		}
		return bench.ModelInfo{}, err
	}
	if id == a.target {
		if ps, err := bench.ParsePreset(m.Preset); err == nil {
			a.mu.Lock()
			a.ctxTokens = ps.Ctx
			a.mu.Unlock()
		}
	}
	return bench.ModelInfo{ID: m.ID, Engine: m.Engine, Present: m.Present, Verified: m.Verified, Status: m.Status, Preset: m.Preset}, nil
}

// Load implements bench.Agent: POST /models/{id}/load, waiting, evicting nothing.
func (a *benchAgent) Load(ctx context.Context, id string) (bench.Op, error) {
	a.announceLoad(id)
	var ans modelLoadAnswer
	_, _, err := a.c.callWith(a.cl, ctx, http.MethodPost, modelPath(id, "/load"),
		map[string]any{"wait": true, "unload_others": false}, &ans)
	if err != nil {
		return bench.Op{}, busyFrom(err)
	}
	return bench.Op{ID: ans.OpID}, nil
}

// Unload implements bench.Agent: POST /models/{id}/unload, never forced.
func (a *benchAgent) Unload(ctx context.Context, id string) (bench.Op, error) {
	a.announceUnload(id)
	var ans modelUnloadAnswer
	_, _, err := a.c.callWith(a.cl, ctx, http.MethodPost, modelPath(id, "/unload"), map[string]any{}, &ans)
	if err != nil {
		return bench.Op{}, busyFrom(err)
	}
	return bench.Op{ID: ans.OpID}, nil
}

// Events implements bench.Agent: GET /events?types=model.*, live events only.
func (a *benchAgent) Events(ctx context.Context) (<-chan agentevents.Event, error) {
	st, err := events.Open(ctx, a.c.eventsOpener(a.cl), events.Query{Types: []string{"model.*"}})
	if err != nil {
		return nil, err
	}
	ch := make(chan agentevents.Event, 64)
	go func() {
		defer close(ch)
		defer st.Close()
		for {
			it, err := st.Next()
			if err != nil {
				return
			}
			if it.Ping {
				continue
			}
			select {
			case ch <- it.Event:
				if a.onEvent != nil {
					a.onEvent(it.Event)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// busyFrom turns the agent's "another model operation is running" (409 LAI-AGT-022) into the
// engine's *BusyError, which the engine and the command map to exit 6.
func busyFrom(err error) error {
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == doctor.AGT022 {
		op, _ := ae.Details["op_id"].(string)
		return &bench.BusyError{OpID: op}
	}
	return err
}

// announceLoad says what a load the engine asked for means: the model being measured, or a model
// that was loaded before coming back.
func (a *benchAgent) announceLoad(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lang := a.c.Lang
	if id == a.target && !a.measureShown {
		a.measureShown = true
		mem := pick(lang, "graphics memory", "la memoria gráfica")
		if a.cpu {
			mem = pick(lang, "memory", "la memoria")
		}
		ctxText := ""
		if a.ctxTokens > 0 {
			ctxText = fmt.Sprintf(" (%d tokens)", a.ctxTokens)
		}
		a.say(pick(lang,
			"Measuring "+mem+" at full context"+ctxText,
			"Midiendo "+mem+" con el contexto completo"+ctxText))
		return
	}
	if id != a.target {
		a.say(pick(lang, "Loading "+a.names.of(id)+" again", "Cargando "+a.names.of(id)+" de nuevo"))
	}
}

// announceUnload is announceLoad for an unload: the other models stepping aside, or the measured
// model leaving for the speed test.
func (a *benchAgent) announceUnload(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lang := a.c.Lang
	switch {
	case id == a.target && a.measureShown && !a.speedShown:
		a.speedShown = true
		a.say(pick(lang, "Measuring speed with llama-bench", "Midiendo la velocidad con llama-bench"))
	case id != a.target && !a.measureShown:
		a.say(pick(lang, "Unloading "+a.names.of(id)+" so the test has the memory to itself",
			"Descargando "+a.names.of(id)+" para que la prueba tenga la memoria para ella sola"))
	}
}

// benchChat is bench.Chatter: the one chat completion that makes the router allocate its context
// buffers. It holds the node's key from GET /key and sends it only to an address on this computer.
type benchChat struct {
	base string
	key  string
	hc   *http.Client
}

var _ bench.Chatter = (*benchChat)(nil)

// newBenchChat reads the key and the local address from the agent. The key stays in the returned
// value; it is never printed, logged or put in an error.
func (c *Call) newBenchChat(cl *Client) (*benchChat, error) {
	var k keyInfo
	if _, _, err := c.callWith(cl, c.Ctx, http.MethodGet, "/key", nil, &k); err != nil {
		return nil, err
	}
	if k.Key == "" || k.BaseURLs.Local == "" {
		return nil, c.Err(CodeBadResponse, "").WithDetails("reason", "no key or local address in the answer")
	}
	if !isLoopbackURL(k.BaseURLs.Local) {
		return nil, c.codeError(doctor.LLM025, ExitFailed,
			pick(c.Lang, "The model service address is not on this computer, so the key is not sent.",
				"La dirección del servicio de modelos no está en este equipo, así que no se envía la clave."))
	}
	return &benchChat{
		base: strings.TrimRight(k.BaseURLs.Local, "/"),
		key:  k.Key,
		hc: &http.Client{
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// isLoopbackURL reports whether u points at this computer (localhost or a loopback address).
func isLoopbackURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return false
	}
	host := p.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Chat implements bench.Chatter.
func (b *benchChat) Chat(ctx context.Context, req bench.ChatRequest) (bench.ChatResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":       req.Model,
		"messages":    []map[string]string{{"role": "user", "content": req.Prompt}},
		"max_tokens":  req.MaxTokens,
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		return bench.ChatResult{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return bench.ChatResult{}, errors.New("could not build the chat request")
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+b.key)
	resp, err := b.hc.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return bench.ChatResult{}, ctx.Err()
		}
		// The transport's error text names the address, never the key (it is only in a header).
		return bench.ChatResult{}, fmt.Errorf("the model service did not answer the chat request: %w", unwrapURLError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return bench.ChatResult{}, fmt.Errorf("the model service answered the chat request with HTTP %d", resp.StatusCode)
	}
	var out struct {
		Timings struct {
			PredictedPerSecond float64 `json:"predicted_per_second"`
			DraftN             int64   `json:"draft_n"`
			DraftNAccepted     int64   `json:"draft_n_accepted"`
		} `json:"timings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return bench.ChatResult{}, errors.New("the model service sent a chat answer that is not JSON")
	}
	return bench.ChatResult{
		PredictedPerSecond: out.Timings.PredictedPerSecond,
		DraftN:             out.Timings.DraftN,
		DraftAccepted:      out.Timings.DraftNAccepted,
	}, nil
}

// unwrapURLError drops the *url.Error wrapper (it repeats the method and the address) and keeps the
// cause.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
