package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// The client of the loopback shim (engines/acestep, E11 contract section 2): GET /health, POST
// /v1/tasks, GET /v1/tasks/{id} and POST /v1/tasks/{id}/cancel on http://127.0.0.1:<ports.music>.

// Shim is the music engine as the service sees it. HTTPShim talks to the real one; FakeShim is the
// test double.
type Shim interface {
	Health(ctx context.Context) (Health, error)
	Submit(ctx context.Context, req TaskRequest) (taskID string, err error)
	Task(ctx context.Context, taskID string) (TaskStatus, error)
	Cancel(ctx context.Context, taskID string) error
}

// Shim errors. ErrUnreachable wraps a connection failure (the unit is stopped or crashed).
var (
	ErrQueueFull    = errors.New("music: the engine's queue is full")
	ErrNotReady     = errors.New("music: the engine is not ready")
	ErrTaskNotFound = errors.New("music: the engine does not know the task")
	ErrUnreachable  = errors.New("music: the engine cannot be reached")
)

// Health states of GET /health.
const (
	HealthLoading = "loading"
	HealthReady   = "ready"
	HealthError   = "error"
)

// Health is GET /health.
type Health struct {
	Status  string  `json:"status"`
	ModelID string  `json:"model_id"`
	Device  string  `json:"device"`
	DType   string  `json:"dtype"`
	VRAMMiB *int64  `json:"vram_mib"`
	Error   *string `json:"error"`
}

// TaskRequest is the body of POST /v1/tasks.
type TaskRequest struct {
	Lyrics     string   `json:"lyrics"`
	Tags       string   `json:"tags"`
	DurationS  int      `json:"duration_s"`
	Language   string   `json:"language"`
	Seed       *int64   `json:"seed"`
	Variations int      `json:"variations"`
	SamplePath *string  `json:"sample_path"`
	SampleMode *string  `json:"sample_mode"`
	Strength   *float64 `json:"strength"`
	OutputDir  string   `json:"output_dir"`
}

// Task states of GET /v1/tasks/{id}.
const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// TaskOutput is one file the engine wrote.
type TaskOutput struct {
	Path       string  `json:"path"`
	DurationS  float64 `json:"duration_s"`
	SampleRate int     `json:"sample_rate"`
	Mime       string  `json:"mime"`
}

// TaskStatus is GET /v1/tasks/{id}.
type TaskStatus struct {
	State    string       `json:"state"`
	Progress *float64     `json:"progress"`
	Error    *string      `json:"error"`
	Outputs  []TaskOutput `json:"outputs"`
}

// Doer sends a request: *http.Client implements it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// DefaultPort is ports.music (overview section 3): the shim's loopback port.
const DefaultPort = 8082

// shimCallTimeout bounds one call to the shim; it answers from memory, so this is generous.
const shimCallTimeout = 10 * time.Second

// HTTPShim is the client of the real shim.
type HTTPShim struct {
	// BaseURL returns http://127.0.0.1:<port>; default DefaultPort.
	BaseURL func() string
	// HTTP is the client; default netx.Client("music"), the fenced shared transport.
	HTTP Doer
}

var _ Shim = (*HTTPShim)(nil)

func (c *HTTPShim) base() string {
	if c.BaseURL != nil {
		return c.BaseURL()
	}
	return fmt.Sprintf("http://127.0.0.1:%d", DefaultPort)
}

func (c *HTTPShim) doer() Doer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return netx.Client("music")
}

// call sends one request and decodes a JSON answer into out (nil skips it). It returns the status.
func (c *HTTPShim) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, shimCallTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.doer().Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, fmt.Errorf("%w: %s", ErrUnreachable, shortErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%w: reading the answer", ErrUnreachable)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil && resp.StatusCode < 300 {
			return resp.StatusCode, fmt.Errorf("music: the engine sent a bad answer to %s %s", method, path)
		}
	}
	return resp.StatusCode, nil
}

// shortErr keeps an error's text to its last part (no URL with query strings reaches a log).
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// RejectedError is a task the engine refused as invalid (400): music.md section 4.4 step 3 answers
// it LAI-SVC-154 with the reason in details.reason. The shim's reason names a field, never a value.
type RejectedError struct{ Reason string }

func (e *RejectedError) Error() string { return "music: the engine refused the task: " + e.Reason }

// reasonText keeps the shim's reason short and printable.
func reasonText(s string) string {
	b := make([]rune, 0, 120)
	for _, r := range s {
		if len(b) >= 120 {
			break
		}
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		b = append(b, r)
	}
	if len(b) == 0 {
		return "rejected"
	}
	return string(b)
}

// Health is GET /health.
func (c *HTTPShim) Health(ctx context.Context) (Health, error) {
	var h Health
	st, err := c.call(ctx, http.MethodGet, "/health", nil, &h)
	if err != nil {
		return Health{}, err
	}
	if st != http.StatusOK {
		return Health{}, fmt.Errorf("music: /health answered %d", st)
	}
	return h, nil
}

// Submit is POST /v1/tasks.
func (c *HTTPShim) Submit(ctx context.Context, req TaskRequest) (string, error) {
	var out struct {
		TaskID string `json:"task_id"`
		Error  string `json:"error"`
	}
	st, err := c.call(ctx, http.MethodPost, "/v1/tasks", req, &out)
	if err != nil {
		return "", err
	}
	switch {
	case st == http.StatusAccepted && out.TaskID != "":
		return out.TaskID, nil
	case st == http.StatusTooManyRequests:
		return "", ErrQueueFull
	case st == http.StatusServiceUnavailable:
		return "", ErrNotReady
	case st == http.StatusBadRequest:
		return "", &RejectedError{Reason: reasonText(out.Error)}
	}
	return "", fmt.Errorf("music: the engine refused the task (%d)", st)
}

// Task is GET /v1/tasks/{id}.
func (c *HTTPShim) Task(ctx context.Context, taskID string) (TaskStatus, error) {
	var out TaskStatus
	st, err := c.call(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil, &out)
	if err != nil {
		return TaskStatus{}, err
	}
	switch st {
	case http.StatusOK:
		return out, nil
	case http.StatusNotFound:
		return TaskStatus{}, ErrTaskNotFound
	}
	return TaskStatus{}, fmt.Errorf("music: the engine answered %d for a task", st)
}

// Cancel is POST /v1/tasks/{id}/cancel.
func (c *HTTPShim) Cancel(ctx context.Context, taskID string) error {
	st, err := c.call(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/cancel", struct{}{}, nil)
	if err != nil {
		return err
	}
	switch st {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrTaskNotFound
	}
	return fmt.Errorf("music: the engine answered %d to a cancel", st)
}
