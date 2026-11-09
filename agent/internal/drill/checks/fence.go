package checks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// NetFence is the drill's network fence (drill.Fence) over a netx.Fence: Enable holds the fence, so
// the agent's shared transport refuses every non-loopback dial and counts it, and Disable releases it
// and reports what was refused. It is reference counted underneath, so a nested hold elsewhere keeps the
// fence on, and an Enable with no Disable (a panic that skipped it) is undone by the next Disable.
type NetFence struct {
	// Fence defaults to netx.Default, the fence of the agent process.
	Fence *netx.Fence

	mu     sync.Mutex
	leases []*netx.Lease
}

var _ drill.Fence = (*NetFence)(nil)

func (n *NetFence) fence() *netx.Fence {
	if n.Fence != nil {
		return n.Fence
	}
	return netx.Default
}

// Enable implements drill.Fence.
func (n *NetFence) Enable() {
	l := n.fence().Acquire()
	n.mu.Lock()
	n.leases = append(n.leases, l)
	n.mu.Unlock()
}

// Disable implements drill.Fence: it releases the most recent hold and returns what the fence refused
// since it. The callers are "host:port (caller)" strings, "x3" appended when a destination was refused
// more than once, in the order they were first seen.
func (n *NetFence) Disable() drill.FenceReport {
	n.mu.Lock()
	var l *netx.Lease
	if k := len(n.leases); k > 0 {
		l, n.leases = n.leases[k-1], n.leases[:k-1]
	}
	n.mu.Unlock()
	if l == nil {
		return drill.FenceReport{}
	}
	return ReportFrom(l.Release())
}

// ReportFrom converts what a netx fence saw into the drill's FenceReport.
func ReportFrom(r netx.Report) drill.FenceReport {
	out := drill.FenceReport{BlockedDials: r.Blocked}
	for _, d := range r.Dials {
		s := d.Addr + " (" + d.Caller + ")"
		if d.Count > 1 {
			s += " x" + strconv.Itoa(d.Count)
		}
		out.Callers = append(out.Callers, s)
	}
	return out
}

// FenceTarget is the address check 1 dials: the host the node must not reach during a drill (drill.md
// section 4.2).
const FenceTarget = "huggingface.co:443"

// fenceProbeTimeout bounds the probe request when the fence is broken and the dial really goes out.
const fenceProbeTimeout = 10 * time.Second

// FenceConfig configures check 1. The zero value is the production check.
type FenceConfig struct {
	// Active reports whether a fence is on; default netx.Default.Active.
	Active func() bool
	// Transport is the transport the probe goes through, "the agent transport" of the spec; default
	// netx.Shared(). A test passes a fake that succeeds to prove a broken fence fails the check.
	Transport http.RoundTripper
	// Target is the host:port to dial; default FenceTarget.
	Target string
}

// Fence returns check 1, drl.fence: with the fence on, dial huggingface.co:443 through the agent's
// transport. Refused by the fence is a pass. Anything else (the request got through, or failed some other
// way, or no fence is on) means the fence does not protect the run, so the check fails and the run stops:
// every later result would be a claim the drill cannot back. The probe is refused without being counted
// (netx.WithProbe), so a healthy fence leaves blocked_dials at 0.
func Fence(cfg FenceConfig) drill.Check {
	return drill.Check{
		ID: drill.FenceCheckID, Area: "network", Order: 1, FailCode: doctor.DRL091,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runFence(ctx, cfg) },
	}
}

func runFence(ctx context.Context, cfg FenceConfig) drill.Outcome {
	active, rt, target := cfg.Active, cfg.Transport, cfg.Target
	if active == nil {
		active = netx.Default.Active
	}
	if rt == nil {
		rt = netx.Shared()
	}
	if target == "" {
		target = FenceTarget
	}
	broken := func(reason string, extra map[string]any) drill.Outcome {
		d := map[string]any{"target": target, "reason": reason}
		for k, v := range extra {
			d[k] = v
		}
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL091, Details: d, StopRun: true}
	}
	if !active() {
		// Nothing to test, and a dial now would really leave the machine: say so without trying.
		return broken("fence_inactive", nil)
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return broken("bad_target", nil)
	}

	ctx, cancel := context.WithTimeout(netx.WithProbe(netx.WithCaller(ctx, "drill.fence")), fenceProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+target+"/", nil)
	if err != nil {
		return broken("bad_target", nil)
	}
	resp, err := (&http.Client{Transport: rt}).Do(req)
	switch {
	case err == nil:
		_ = resp.Body.Close()
		return broken("dial_succeeded", map[string]any{"status": resp.StatusCode})
	case errors.Is(err, netx.ErrFenced):
		return drill.Outcome{Result: drill.ResultPass, Details: map[string]any{"target": target, "refused": true}}
	default:
		// The request failed, but not because the fence refused it: it cannot be said that the fence is
		// what stopped the dial.
		return broken("not_refused_by_the_fence", map[string]any{"error": clipText(err.Error(), 200)})
	}
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
