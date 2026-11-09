package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// CompletionRequest is one chat completion of Client.Complete: a single user message, optionally with
// one PNG image, no streaming. The drill's model checks use it (drill.md section 4.2 checks 5 and 6).
type CompletionRequest struct {
	// Model is the catalog model id; it must be loaded (the client never asks for an autoload).
	Model string
	// Prompt is the user's text.
	Prompt string
	// Image is a PNG sent with the prompt (a vision model); nil sends text only.
	Image []byte
	// MaxTokens bounds the answer; below 1 means 1.
	MaxTokens int
	// DisableThinking sends chat_template_kwargs.enable_thinking = false, for a reasoning model
	// (catalog client_hints.reasoning): the answer comes at once, without a thinking phase.
	DisableThinking bool
	// Timeout bounds the call; 0 means Options.ChatTimeout.
	Timeout time.Duration
}

// Completion is the answer of Client.Complete.
type Completion struct {
	// Text is the assistant message content (reasoning_content is never returned).
	Text string
	// CompletionTokens is usage.completion_tokens, or timings.predicted_n; 0 when neither was sent.
	CompletionTokens int
	// TokensPerSecond is the generation speed from the server's timings: generated tokens over
	// generation time only, not counting the prompt or a model load. HasTimings is false when the
	// server sent no usable timings, and TokensPerSecond is then 0.
	TokensPerSecond float64
	HasTimings      bool
}

// Complete sends one chat completion at temperature 0 and returns the answer with its speed. Like
// every call of the client it goes to the router only, with the key attached by the client and never
// part of an error.
func (c *Client) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	if !validID(req.Model) {
		return Completion{}, ErrBadModelID
	}
	max := req.MaxTokens
	if max < 1 {
		max = 1
	}
	var content any = req.Prompt
	if len(req.Image) > 0 {
		content = []map[string]any{
			{"type": "text", "text": req.Prompt},
			{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(req.Image)}},
		}
	}
	body := map[string]any{
		"model":       req.Model,
		"messages":    []map[string]any{{"role": "user", "content": content}},
		"max_tokens":  max,
		"temperature": 0,
		"stream":      false,
	}
	if req.DisableThinking {
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.chatTimeout
	}
	rep, err := c.do(ctx, call{method: http.MethodPost, path: "/v1/chat/completions", timeout: timeout, body: body})
	if err != nil {
		return Completion{}, err
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Timings struct {
			PredictedN         int     `json:"predicted_n"`
			PredictedMS        float64 `json:"predicted_ms"`
			PredictedPerSecond float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if err := json.Unmarshal(rep.body, &r); err != nil || len(r.Choices) == 0 {
		return Completion{}, malformed("chat completion without choices")
	}
	out := Completion{Text: contentText(r.Choices[0].Message.Content), CompletionTokens: r.Usage.CompletionTokens}
	if out.CompletionTokens == 0 {
		out.CompletionTokens = r.Timings.PredictedN
	}
	switch {
	case r.Timings.PredictedPerSecond > 0:
		out.TokensPerSecond, out.HasTimings = r.Timings.PredictedPerSecond, true
	case r.Timings.PredictedN > 0 && r.Timings.PredictedMS > 0:
		out.TokensPerSecond, out.HasTimings = float64(r.Timings.PredictedN)/(r.Timings.PredictedMS/1000), true
	}
	return out, nil
}

// contentText reads message.content, a string or (rarely) a list of {"type":"text","text":...} parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
