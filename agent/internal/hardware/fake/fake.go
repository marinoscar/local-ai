// Package fake is a hardware.Sampler for tests of the packages that consume it (router fit rules,
// status, cloud vitals, bench): it samples nothing, serves the Snapshot a test sets and records
// how often it was asked.
package fake

import (
	"context"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// Sampler implements hardware.Sampler.
type Sampler struct {
	mu        sync.Mutex
	snap      hardware.Snapshot
	next      []hardware.Snapshot
	refreshes int
	gpuReads  [][]hardware.GPU
	gpuErr    error
	gpuCalls  int
}

var _ hardware.Sampler = (*Sampler)(nil)

// New returns a Sampler serving snap.
func New(snap hardware.Snapshot) *Sampler { return &Sampler{snap: snap} }

// Set replaces the snapshot Snapshot returns.
func (s *Sampler) Set(snap hardware.Snapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// QueueRefresh scripts the snapshots successive Refresh calls produce; after the queue is empty,
// Refresh returns the current snapshot unchanged.
func (s *Sampler) QueueRefresh(snaps ...hardware.Snapshot) {
	s.mu.Lock()
	s.next = append(s.next, snaps...)
	s.mu.Unlock()
}

// QueueGPUReads scripts the answers of successive SampleGPUs calls (the last one repeats), for
// bench tests that need a memory curve.
func (s *Sampler) QueueGPUReads(reads ...[]hardware.GPU) {
	s.mu.Lock()
	s.gpuReads = append(s.gpuReads, reads...)
	s.mu.Unlock()
}

// FailGPUReads makes SampleGPUs return err.
func (s *Sampler) FailGPUReads(err error) {
	s.mu.Lock()
	s.gpuErr = err
	s.mu.Unlock()
}

// Refreshes is how many times Refresh was called.
func (s *Sampler) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// Snapshot implements hardware.Sampler.
func (s *Sampler) Snapshot() hardware.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// Refresh implements hardware.Sampler.
func (s *Sampler) Refresh(context.Context) hardware.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	if len(s.next) > 0 {
		s.snap, s.next = s.next[0], s.next[1:]
	}
	return s.snap
}

// SampleGPUs implements hardware.Sampler.
func (s *Sampler) SampleGPUs(context.Context) ([]hardware.GPU, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.gpuCalls
	s.gpuCalls++
	if s.gpuErr != nil {
		return nil, s.gpuErr
	}
	if len(s.gpuReads) == 0 {
		return append([]hardware.GPU(nil), s.snap.GPUs...), nil
	}
	if i >= len(s.gpuReads) {
		i = len(s.gpuReads) - 1
	}
	return append([]hardware.GPU(nil), s.gpuReads[i]...), nil
}
