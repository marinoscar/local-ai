package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// promptSentence repeated promptRepeats times is a prompt of about 512 tokens (llama-router.md
// section 4.13 step 3): about 16 tokens a sentence. It is deterministic so two runs are comparable.
const (
	promptSentence = "The quick brown fox jumps over the lazy dog near the quiet river bank. "
	promptRepeats  = 32
	promptClosing  = "Summarize the text above in one sentence."
)

// Prompt is the prompt of the measured request: about 512 tokens, to force the KV cache and the
// compute buffers to be allocated.
func Prompt() string {
	return strings.Repeat(promptSentence, promptRepeats) + promptClosing
}

// round rounds v to the given number of decimals.
func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

// median returns the median of vals (the mean of the middle two for an even count). vals is not
// modified.
func median(vals []int64) int64 {
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2] + 1) / 2
}

// detectBackend is hardware.BackendFor over the vendors in a sample.
func detectBackend(gpus []hardware.GPU) string {
	var nvidia, amd bool
	for _, g := range gpus {
		switch g.Vendor {
		case hardware.VendorNVIDIA:
			nvidia = true
		case hardware.VendorAMD:
			amd = true
		}
	}
	return hardware.BackendFor(nvidia, amd)
}

// minBaselineGood is how many of the BaselineSamples must have been read for U0 to count.
const minBaselineGood = 3

// baselineVRAM is U0: the median of BaselineSamples samples of the graphics memory in use,
// BaselineInterval apart. first is the sample the caller already took.
func (r *run) baselineVRAM(ctx context.Context, backend string, first []hardware.GPU, firstErr error) (int64, error) {
	var vals []int64
	add := func(g []hardware.GPU, err error) {
		if err == nil {
			vals = append(vals, hardware.Snapshot{GPUs: g}.FitUsedMiB(backend))
		}
	}
	if len(hardware.Snapshot{GPUs: first}.FitGPUs(backend)) == 0 {
		return 0, fmt.Errorf("no GPU found for the %s backend", backend)
	}
	add(first, firstErr)
	var lastErr = firstErr
	for i := 1; i < BaselineSamples; i++ {
		if err := r.b.o.Sleep(ctx, BaselineInterval); err != nil {
			return 0, err
		}
		g, err := r.b.d.GPU.SampleGPUs(ctx)
		if err != nil {
			lastErr = err
		}
		add(g, err)
	}
	if len(vals) < minBaselineGood {
		return 0, fmt.Errorf("only %d of %d baseline samples could be read: %w", len(vals), BaselineSamples, lastErr)
	}
	return median(vals), nil
}

// sampleDuring runs req while sampling read every SampleInterval, and returns the peak. A sample
// that fails is skipped; the run fails when none succeeded. One last sample is taken after the
// request returned, so a buffer freed at the very end cannot hide the peak. req must honour ctx.
func (r *run) sampleDuring(ctx context.Context, req func() error, read func(context.Context) (int64, error)) (int64, error) {
	done := make(chan error, 1)
	go func() { done <- req() }()

	var peak int64
	good := 0
	var lastErr error
	take := func() {
		v, err := read(ctx)
		if err != nil {
			lastErr = err
			return
		}
		good++
		if v > peak {
			peak = v
		}
	}
	for {
		take()
		select {
		case err := <-done:
			if err != nil {
				return 0, fmt.Errorf("the chat request failed: %w", err)
			}
			take()
			if good == 0 {
				return 0, fmt.Errorf("no memory sample could be read: %w", lastErr)
			}
			return peak, nil
		default:
		}
		if err := r.b.o.Sleep(ctx, SampleInterval); err != nil {
			<-done // the request ends with the same cancellation
			return 0, err
		}
	}
}

// llamaBench is step 4: run llama-bench on the weights and read prompt (pp512) and generation
// (tg128) tokens per second.
func (b *Bench) llamaBench(ctx context.Context, ps Preset) (pp, tg float64, err error) {
	res, err := b.d.Exec.Run(ctx, execx.Cmd{
		Path:    b.o.BenchBinary,
		Args:    ps.BenchArgs(),
		Timeout: b.o.BenchTimeout,
		MaxOut:  maxBenchOut,
		Env:     b.o.Env,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("llama-bench: %w", err)
	}
	if res.ExitCode != 0 {
		return 0, 0, fmt.Errorf("llama-bench exited with code %d: %s", res.ExitCode, lastLine(res.Stderr))
	}
	pp, tg, err = ParseBenchJSON(res.Stdout)
	if err != nil {
		return 0, 0, fmt.Errorf("llama-bench output: %w", err)
	}
	return pp, tg, nil
}

// lastLine is the last non-empty line of b, cut to 200 characters, for an error message.
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}

// benchRow is one entry of `llama-bench -o json`.
type benchRow struct {
	NPrompt int     `json:"n_prompt"`
	NGen    int     `json:"n_gen"`
	AvgTS   float64 `json:"avg_ts"`
}

// ParseBenchJSON reads the output of `llama-bench -p 512 -n 128 -o json`: a JSON array with one
// entry for the prompt test (n_prompt 512, n_gen 0) and one for the generation test (n_prompt 0,
// n_gen 128), each with avg_ts, the mean tokens per second. Text around the array (a banner, a
// trailing newline) is ignored.
func ParseBenchJSON(out []byte) (pp512, tg128 float64, err error) {
	s := string(out)
	i, j := strings.Index(s, "["), strings.LastIndex(s, "]")
	if i < 0 || j < i {
		return 0, 0, errors.New("no JSON array in the output")
	}
	var rows []benchRow
	if err := json.Unmarshal([]byte(s[i:j+1]), &rows); err != nil {
		return 0, 0, fmt.Errorf("not valid JSON: %w", err)
	}
	for _, r := range rows {
		switch {
		case r.NPrompt == 512 && r.NGen == 0:
			pp512 = r.AvgTS
		case r.NPrompt == 0 && r.NGen == 128:
			tg128 = r.AvgTS
		}
	}
	if pp512 <= 0 || tg128 <= 0 {
		return 0, 0, errors.New("the prompt or the generation result is missing")
	}
	return pp512, tg128, nil
}
