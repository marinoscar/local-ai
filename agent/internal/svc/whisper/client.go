package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"
)

// InferenceTimeout is how long whisper-server may take for one clip (companion-services.md 4.2.5).
const InferenceTimeout = 60 * time.Second

// Failures of the whisper-server call.
var (
	// ErrServerDown: the server is not reachable or answered with a server error (LAI-SVC-011).
	ErrServerDown = errors.New("whisper-server is not available")
	// ErrInferenceTimeout: it took longer than the timeout (LAI-SVC-016).
	ErrInferenceTimeout = errors.New("whisper-server took too long")
)

// Result is what the agent uses of a verbose_json answer.
type Result struct {
	Text string
	// Language is the language whisper used or detected.
	Language string
	// LanguageProbability is nil when whisper did not send one.
	LanguageProbability *float64
	// Duration is whisper's own measure of the clip, in seconds.
	Duration float64
}

// Client calls whisper-server (companion-services.md section 4.2.4).
type Client struct {
	// BaseURL is like http://127.0.0.1:8081; it is read on every call so a port change applies.
	BaseURL func() string
	// HTTP is the transport; default http.DefaultClient.
	HTTP *http.Client
	// Timeout bounds one call; default InferenceTimeout.
	Timeout time.Duration
}

// verboseJSON is the answer of POST /inference with response_format=verbose_json.
type verboseJSON struct {
	Task     string   `json:"task"`
	Language string   `json:"language"`
	Duration float64  `json:"duration"`
	Text     string   `json:"text"`
	Prob     *float64 `json:"detected_language_probability"`
}

// ParseVerboseJSON decodes an /inference answer.
func ParseVerboseJSON(b []byte) (Result, error) {
	var v verboseJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return Result{}, fmt.Errorf("whisper answer is not JSON: %w", err)
	}
	r := Result{Text: strings.TrimSpace(v.Text), Language: v.Language, Duration: v.Duration}
	if v.Prob != nil && *v.Prob >= 0 && *v.Prob <= 1 {
		r.LanguageProbability = v.Prob
	}
	return r, nil
}

// Transcribe sends a 16 kHz mono PCM16 WAV. language is "auto" or a code; prompt may be empty.
func (c *Client) Transcribe(ctx context.Context, wav []byte, language, prompt string) (Result, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return Result{}, err
	}
	_, _ = part.Write(wav)
	fields := [][2]string{
		{"response_format", "verbose_json"}, {"language", language}, {"temperature", "0.0"},
		{"temperature_inc", "0.2"}, {"no_timestamps", "true"},
	}
	if prompt != "" {
		fields = append(fields, [2]string{"prompt", prompt})
	}
	for _, f := range fields {
		_ = mw.WriteField(f[0], f[1])
	}
	if err := mw.Close(); err != nil {
		return Result{}, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = InferenceTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.BaseURL()+"/inference", &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, classify(ctx, cctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, classify(ctx, cctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%w: status %d", ErrServerDown, resp.StatusCode)
	}
	res, err := ParseVerboseJSON(raw)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrServerDown, err)
	}
	return res, nil
}

// classify maps a transport error: the caller leaving stays a context error, the deadline of the call
// is a timeout, anything else (refused, reset) means the server is down.
func classify(parent, call context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	var ne net.Error
	if call.Err() != nil || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrInferenceTimeout
	}
	return fmt.Errorf("%w: %v", ErrServerDown, err)
}
