// Package fake is a test double of the music engine's shim (music.Shim): no Python, no GPU. A task
// stays queued until the test moves it (Run, Progress, Complete, Fail) or, with AutoComplete, is done
// at the first poll with one short WAV per variation.
package fake

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/music"
)

// Shim is the fake.
type Shim struct {
	mu sync.Mutex
	// Status is the /health status: ready (default), loading or error.
	Status  string
	ModelID string
	// MaxQueue makes Submit answer ErrQueueFull while that many tasks are queued (0: unbounded).
	MaxQueue int
	// AutoComplete finishes a task at its first poll.
	AutoComplete bool
	// Down makes every call fail with ErrUnreachable (the unit stopped or crashed).
	Down bool
	// FailOnPoll makes the first poll report failed.
	FailOnPoll bool
	// WAVSeconds is the length of the files written (default 0.1 s).
	WAVSeconds float64
	// Outputs is how many files a task writes (0: one per variation).
	Outputs int
	// Reject makes Submit answer 400 with this reason.
	Reject string

	tasks     map[string]*task
	order     []string
	Requests  []music.TaskRequest
	Cancelled []string
}

type task struct {
	req    music.TaskRequest
	status music.TaskStatus
}

// New returns a ready fake.
func New() *Shim {
	return &Shim{Status: music.HealthReady, ModelID: "music-example", tasks: map[string]*task{}}
}

var _ music.Shim = (*Shim)(nil)

// Kill makes the engine unreachable from now on.
func (f *Shim) Kill() {
	f.mu.Lock()
	f.Down = true
	f.mu.Unlock()
}

// Health implements music.Shim.
func (f *Shim) Health(context.Context) (music.Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return music.Health{}, music.ErrUnreachable
	}
	return music.Health{Status: f.Status, ModelID: f.ModelID, Device: "cuda", DType: "bf16"}, nil
}

// Submit implements music.Shim.
func (f *Shim) Submit(_ context.Context, req music.TaskRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return "", music.ErrUnreachable
	}
	if f.Status != music.HealthReady {
		return "", music.ErrNotReady
	}
	if f.Reject != "" {
		return "", &music.RejectedError{Reason: f.Reject}
	}
	queued := 0
	for _, t := range f.tasks {
		if t.status.State == music.TaskQueued {
			queued++
		}
	}
	if f.MaxQueue > 0 && queued >= f.MaxQueue {
		return "", music.ErrQueueFull
	}
	id := fmt.Sprintf("tsk_%012x", len(f.order)+1)
	f.tasks[id] = &task{req: req, status: music.TaskStatus{State: music.TaskQueued}}
	f.order = append(f.order, id)
	f.Requests = append(f.Requests, req)
	return id, nil
}

// Task implements music.Shim.
func (f *Shim) Task(_ context.Context, id string) (music.TaskStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return music.TaskStatus{}, music.ErrUnreachable
	}
	t, ok := f.tasks[id]
	if !ok {
		return music.TaskStatus{}, music.ErrTaskNotFound
	}
	if f.FailOnPoll && t.status.State == music.TaskQueued {
		msg := "the fake engine failed"
		t.status = music.TaskStatus{State: music.TaskFailed, Error: &msg}
	}
	if f.AutoComplete && (t.status.State == music.TaskQueued || t.status.State == music.TaskRunning) {
		if err := f.completeLocked(t); err != nil {
			return music.TaskStatus{}, err
		}
	}
	return clone(t.status), nil
}

// Cancel implements music.Shim.
func (f *Shim) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return music.ErrUnreachable
	}
	t, ok := f.tasks[id]
	if !ok {
		return music.ErrTaskNotFound
	}
	f.Cancelled = append(f.Cancelled, id)
	if t.status.State == music.TaskQueued || t.status.State == music.TaskRunning {
		_ = os.RemoveAll(t.req.OutputDir)
		t.status = music.TaskStatus{State: music.TaskCancelled}
	}
	return nil
}

// IDs returns the task ids, oldest first.
func (f *Shim) IDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.order...)
}

// Last returns the id of the newest task ("" when none).
func (f *Shim) Last() string {
	ids := f.IDs()
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

// Progress moves a task to running with the given progress.
func (f *Shim) Progress(id string, p float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[id]; ok {
		t.status.State = music.TaskRunning
		t.status.Progress = &p
	}
}

// Complete writes the outputs of a task and marks it done.
func (f *Shim) Complete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return music.ErrTaskNotFound
	}
	return f.completeLocked(t)
}

// Fail marks a task failed.
func (f *Shim) Fail(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[id]; ok {
		msg := "the fake engine failed"
		t.status = music.TaskStatus{State: music.TaskFailed, Error: &msg}
	}
}

func (f *Shim) completeLocked(t *task) error {
	if err := os.MkdirAll(t.req.OutputDir, 0o750); err != nil {
		return err
	}
	secs := f.WAVSeconds
	if secs <= 0 {
		secs = 0.1
	}
	var outs []music.TaskOutput
	n := t.req.Variations
	if f.Outputs > 0 {
		n = f.Outputs
	}
	for v := 0; v < n; v++ {
		p := filepath.Join(t.req.OutputDir, fmt.Sprintf("out-%d.wav", v))
		if err := os.WriteFile(p, WAV(secs, 8000), 0o640); err != nil {
			return err
		}
		outs = append(outs, music.TaskOutput{Path: p, DurationS: secs, SampleRate: 8000, Mime: "audio/wav"})
	}
	one := 1.0
	t.status = music.TaskStatus{State: music.TaskDone, Progress: &one, Outputs: outs}
	return nil
}

func clone(s music.TaskStatus) music.TaskStatus {
	c := s
	c.Outputs = append([]music.TaskOutput{}, s.Outputs...)
	return c
}

// WAV returns a silent mono 16-bit PCM WAV of the given length.
func WAV(seconds float64, rate int) []byte {
	n := int(seconds * float64(rate))
	data := make([]byte, n*2)
	b := make([]byte, 0, 44+len(data))
	le := binary.LittleEndian
	b = append(b, "RIFF"...)
	b = le.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVEfmt "...)
	b = le.AppendUint32(b, 16)
	b = le.AppendUint16(b, 1) // PCM
	b = le.AppendUint16(b, 1) // mono
	b = le.AppendUint32(b, uint32(rate))
	b = le.AppendUint32(b, uint32(rate*2))
	b = le.AppendUint16(b, 2)
	b = le.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = le.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}
