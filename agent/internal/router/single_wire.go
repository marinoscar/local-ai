package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// This file is the wire side of single mode (llama-router.md section 4.8): a one-model llama-server
// has no router status, so what is loaded is read from GET /v1/models, which lists the one model with
// its aliases and carries no status.

// SingleLister is the part of the client the single-mode orchestration reads. *Client implements it.
type SingleLister interface {
	// V1Models calls GET /v1/models of a one-model llama-server. Every entry is reported with the
	// status "loaded": the server only answers when its model is loaded, and the caller checks
	// /health too.
	V1Models(ctx context.Context) ([]ModelStatus, error)
}

// ServerProber tells which kind of llama-server answers. *Client implements it.
type ServerProber interface {
	// ServerMode returns LlamaModeRouter or LlamaModeSingle. An error means the server did not
	// answer or answered with something that is neither.
	ServerMode(ctx context.Context) (string, error)
}

var (
	_ SingleLister = (*Client)(nil)
	_ ServerProber = (*Client)(nil)
)

// V1Models implements SingleLister.
func (c *Client) V1Models(ctx context.Context) ([]ModelStatus, error) {
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/v1/models", maxBody: maxModelsBody})
	if err != nil {
		return nil, err
	}
	return ParseV1Models(rep.body)
}

// ServerMode implements ServerProber. A router answers GET /models with a status on every entry; a
// one-model server answers the same list without status (or, on older builds, not at all).
func (c *Client) ServerMode(ctx context.Context) (string, error) {
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/models", maxBody: maxModelsBody})
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return LlamaModeSingle, nil
		}
		return "", err
	}
	if _, perr := ParseModels(rep.body); perr == nil {
		return LlamaModeRouter, nil
	}
	if _, perr := ParseV1Models(rep.body); perr == nil {
		return LlamaModeSingle, nil
	}
	return "", malformed("neither a router nor a one-model server list")
}

// ParseV1Models decodes the body of GET /v1/models of a one-model llama-server: an object with a data
// array whose entries carry an id and, on current builds, aliases and tags. There is no status, so
// every entry is returned as loaded. Unknown fields are ignored, as llama-server adds some between
// releases.
func ParseV1Models(body []byte) ([]ModelStatus, error) {
	var top struct {
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&top); err != nil {
		return nil, malformed("models body is not a JSON object: %v", err)
	}
	if err := trailing(dec); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(top.Data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, malformed("models body has no data array")
	}
	var raws []struct {
		ID      *string  `json:"id"`
		Aliases []string `json:"aliases"`
		Tags    []string `json:"tags"`
		// Status is absent on a one-model server. A router answers /v1/models with the status of
		// every preset (the same handler as /models), and while llama switches mode a poll can see
		// that, so it is honoured when it is there.
		Status *struct {
			Value string `json:"value"`
		} `json:"status"`
	}
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil, malformed("models data: %v", err)
	}
	out := make([]ModelStatus, 0, len(raws))
	seen := map[string]bool{}
	for i, r := range raws {
		if r.ID == nil || !validID(*r.ID) {
			return nil, malformed("models data[%d] has no usable id", i)
		}
		if seen[*r.ID] {
			return nil, malformed("model %q listed twice", *r.ID)
		}
		seen[*r.ID] = true
		var aliases []string
		for _, a := range r.Aliases {
			if a != *r.ID && validID(a) && !contains1(aliases, a) {
				aliases = append(aliases, a)
			}
		}
		status := StatusLoaded
		if r.Status != nil && r.Status.Value != "" {
			status = Status(r.Status.Value)
		}
		out = append(out, ModelStatus{ID: *r.ID, Aliases: aliases, Tags: r.Tags, Source: "single", Status: status})
	}
	return out, nil
}

// serves reports whether a one-model server's entry answers to a catalog id, as its id or an alias.
func (m ModelStatus) serves(id string) bool { return m.ID == id || contains1(m.Aliases, id) }
