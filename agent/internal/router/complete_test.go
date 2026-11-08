package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// completionServer answers POST /v1/chat/completions with reply and records the request.
func completionServer(t *testing.T, status int, reply string, got *map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		if got != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Options{BaseURL: srv.URL, Keys: StaticKey(secret(3)), Timeout: time.Second, ChatTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCompleteTextAndTimings(t *testing.T) {
	var got map[string]any
	c := completionServer(t, 200, `{"choices":[{"message":{"role":"assistant","content":"READY","reasoning_content":"hmm"}}],
		"usage":{"completion_tokens":3},"timings":{"predicted_n":3,"predicted_ms":250,"predicted_per_second":12}}`, &got)
	res, err := c.Complete(context.Background(), CompletionRequest{Model: "model-a-q4km", Prompt: "Reply with exactly one word: READY", MaxTokens: 16})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "READY" || res.CompletionTokens != 3 || !res.HasTimings || res.TokensPerSecond != 12 {
		t.Fatalf("completion = %+v", res)
	}
	if got["model"] != "model-a-q4km" || got["max_tokens"] != float64(16) || got["temperature"] != float64(0) || got["stream"] != false {
		t.Fatalf("request = %v", got)
	}
	if _, has := got["chat_template_kwargs"]; has {
		t.Error("thinking is only disabled when asked")
	}
	msgs := got["messages"].([]any)
	if m := msgs[0].(map[string]any); m["role"] != "user" || m["content"] != "Reply with exactly one word: READY" {
		t.Fatalf("message = %v", msgs[0])
	}
}

func TestCompleteSpeedFromPredictedNAndMS(t *testing.T) {
	c := completionServer(t, 200, `{"choices":[{"message":{"content":"ok"}}],"timings":{"predicted_n":20,"predicted_ms":4000}}`, nil)
	res, err := c.Complete(context.Background(), CompletionRequest{Model: "m", Prompt: "p"})
	if err != nil || !res.HasTimings || res.TokensPerSecond != 5 || res.CompletionTokens != 20 {
		t.Fatalf("completion = %+v, %v", res, err)
	}
}

func TestCompleteWithoutTimings(t *testing.T) {
	c := completionServer(t, 200, `{"choices":[{"message":{"content":[{"type":"text","text":"RE"},{"type":"text","text":"ADY"}]}}]}`, nil)
	res, err := c.Complete(context.Background(), CompletionRequest{Model: "m", Prompt: "p"})
	if err != nil || res.HasTimings || res.TokensPerSecond != 0 || res.Text != "READY" {
		t.Fatalf("completion = %+v, %v", res, err)
	}
}

func TestCompleteSendsTheImageAndDisablesThinking(t *testing.T) {
	var got map[string]any
	c := completionServer(t, 200, `{"choices":[{"message":{"content":"7"}}]}`, &got)
	if _, err := c.Complete(context.Background(), CompletionRequest{
		Model: "m", Prompt: "What digit?", Image: []byte{0x89, 'P', 'N', 'G'}, MaxTokens: 8, DisableThinking: true,
	}); err != nil {
		t.Fatal(err)
	}
	kw, _ := got["chat_template_kwargs"].(map[string]any)
	if kw["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs = %v", got["chat_template_kwargs"])
	}
	parts := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["text"] != "What digit?" {
		t.Fatalf("content parts = %v", parts)
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image url = %q", url)
	}
}

func TestCompleteErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := completionServer(t, 200, `{}`, nil).Complete(ctx, CompletionRequest{Model: "", Prompt: "p"}); !errors.Is(err, ErrBadModelID) {
		t.Errorf("empty model = %v", err)
	}
	if _, err := completionServer(t, 200, `{"choices":[]}`, nil).Complete(ctx, CompletionRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Error("an answer without choices is malformed")
	}
	if _, err := completionServer(t, 400, `{"error":{"message":"model is not loaded","type":"invalid_request_error"}}`, nil).Complete(ctx, CompletionRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Error("an HTTP 400 is an error")
	}
}
