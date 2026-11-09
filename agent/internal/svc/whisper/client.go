package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// The whisper-server client (companion-services.md section 4.2.4): POST /inference with a 16 kHz mono
// PCM16 WAV, answered in verbose_json. GET / is the liveness check; POST /load is not used.

// ClientTimeout is the time whisper-server gets for one transcription.
const ClientTimeout = 60 * time.Second

// maxReplyBytes bounds the answer that is read.
const maxReplyBytes = 4 << 20

// Doer is the part of *http.Client the client uses.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Kinds of a ClientError.
const (
	// KindDown: whisper-server could not be reached or answered with an error (LAI-SVC-011).
	KindDown = "down"
	// KindTimeout: it did not answer within the timeout (LAI-SVC-016).
	KindTimeout = "timeout"
)

// ClientError is a failed call to whisper-server.
type ClientError struct {
	Kind   string
	Reason string // connect, status, bad_reply, error_field or timeout
	Status int    // the HTTP status when there was one
	Err    error
}

// Error implements error. The text never holds a transcript.
func (e *ClientError) Error() string {
	s := "whisper-server: " + e.Kind + " (" + e.Reason + ")"
	if e.Status != 0 {
		s += fmt.Sprintf(" status %d", e.Status)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap implements errors.Unwrap.
func (e *ClientError) Unwrap() error { return e.Err }

// Result is what a transcription yields.
type Result struct {
	Text string
	// Language is the ISO 639-1 code (whisper-server answers with the language's English name).
	Language string
	// Probability is detected_language_probability when the server sent it.
	Probability *float64
	// Seconds is the audio length the server reports (0 when it did not say).
	Seconds float64
}

// Client talks to whisper-server.
type Client struct {
	// BaseURL returns the server's base URL ("http://127.0.0.1:8081"); read on every call so a
	// changed port takes effect.
	BaseURL func() string
	// HTTP defaults to a plain http.Client (no proxy: the server is on loopback).
	HTTP Doer
	// Timeout defaults to ClientTimeout.
	Timeout time.Duration
}

// verboseReply is the part of the verbose_json answer the agent reads. language is the English name of
// the language ("english"); detected_language_probability is present on newer builds only.
type verboseReply struct {
	Text             string   `json:"text"`
	Language         string   `json:"language"`
	DetectedLanguage string   `json:"detected_language"`
	Duration         float64  `json:"duration"`
	Probability      *float64 `json:"detected_language_probability"`
	Error            string   `json:"error"`
}

// Transcribe sends wav (16 kHz mono PCM16) with the language ("auto" or a code) and the optional prompt.
// A ctx that ends first (the caller left) returns its own error, not a ClientError.
func (c *Client) Transcribe(ctx context.Context, wav []byte, language, prompt string) (Result, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ClientTimeout
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="audio.wav"`)
	h.Set("Content-Type", "audio/wav")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(wav)
	fields := [][2]string{
		{"response_format", "verbose_json"}, {"language", language},
		{"temperature", "0.0"}, {"temperature_inc", "0.2"}, {"no_timestamps", "true"},
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

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(c.BaseURL(), "/")+"/inference", &body)
	if err != nil {
		return Result{}, &ClientError{Kind: KindDown, Reason: "connect", Err: err}
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	doer := c.HTTP
	if doer == nil {
		doer = netx.Client("svc.whisper")
	}
	resp, err := doer.Do(req)
	if err != nil {
		return Result{}, c.classify(ctx, callCtx, "connect", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if err != nil {
		return Result{}, c.classify(ctx, callCtx, "bad_reply", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, &ClientError{Kind: KindDown, Reason: "status", Status: resp.StatusCode}
	}
	if len(raw) > maxReplyBytes {
		return Result{}, &ClientError{Kind: KindDown, Reason: "bad_reply", Err: errors.New("the answer is too large")}
	}
	var rep verboseReply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return Result{}, &ClientError{Kind: KindDown, Reason: "bad_reply", Err: errors.New("the answer is not JSON")}
	}
	if rep.Error != "" {
		// whisper-server reports a failed run as 200 {"error": "..."} on some builds.
		return Result{}, &ClientError{Kind: KindDown, Reason: "error_field"}
	}
	lang := rep.Language
	if lang == "" {
		lang = rep.DetectedLanguage
	}
	res := Result{Text: strings.TrimSpace(rep.Text), Language: LanguageCode(lang), Seconds: rep.Duration}
	if p := rep.Probability; p != nil && *p >= 0 && *p <= 1 {
		res.Probability = p
	}
	return res, nil
}

// classify turns a transport failure into the caller's error, a timeout, or "down".
func (c *Client) classify(parent, call context.Context, reason string, err error) error {
	if perr := parent.Err(); perr != nil {
		return perr
	}
	if errors.Is(call.Err(), context.DeadlineExceeded) {
		return &ClientError{Kind: KindTimeout, Reason: "timeout", Err: err}
	}
	return &ClientError{Kind: KindDown, Reason: reason, Err: err}
}

// Alive reports whether GET / answers 200 (the liveness check; the route does not need it, the health
// probe may).
func (c *Client) Alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL(), "/")+"/", nil)
	if err != nil {
		return false
	}
	doer := c.HTTP
	if doer == nil {
		doer = netx.Client("svc.whisper")
	}
	resp, err := doer.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK
}
