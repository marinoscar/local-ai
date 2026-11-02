package doctor

import (
	"context"
	"time"
)

// Ids of the built-in network checks (agent.md sections 4.5 and 4.7).
const (
	CheckNetServeState     = "net.serve_state"
	CheckNetServeReachable = "net.serve_reachable"
	CheckNetConnectivity   = "net.connectivity"
)

// ServeReachableTimeout is the time the slow net.serve_reachable check gives the first HTTPS request
// to the node's own tailnet name: Tailscale can need about 20 s to issue the certificate of
// pattern B (LAI-NET-008 on timeout).
const ServeReachableTimeout = 30 * time.Second

// NetSources are the network sources the built-in net.* checks read. Each is optional: a nil source
// skips the check that needs it. *modes.TailscaleManager provides Serve and Reachable,
// *modes.ConnectivityProber provides Connectivity (the packages are not imported here: modes imports
// this one for its codes, so the sources are plain interfaces over doctor.Result).
type NetSources struct {
	// Serve reports what the last Tailscale Serve pass (apply, remove or inspect) found. It never runs
	// a process: LAI-NET-001 to 007 and 009.
	Serve interface{ ServeResults() []Result }
	// Reachable makes one HTTPS request to the node's own tailnet name and reports LAI-NET-008 when it
	// times out. It is slow, so it runs only when asked for (not by `?fast=1`, not as an alert source).
	Reachable interface {
		CheckServeReachable(ctx context.Context) []Result
	}
	// Connectivity reports what the prober found: LAI-NET-011, 012 and 013. It never probes.
	Connectivity interface{ ConnectivityResults() []Result }
}

// NetChecks returns the built-in network checks for the sources given:
//
//	net.serve_state      fast; NET-001 to NET-007 and NET-009 from the last serve pass
//	net.serve_reachable  slow, explicit runs only; LAI-NET-008 after a 30 s timeout
//	net.connectivity     fast; LAI-NET-011, 012 and 013 from the prober
func NetChecks(src NetSources) []Check {
	var out []Check
	if src.Serve != nil {
		out = append(out, Check{ID: CheckNetServeState, Area: "NET", Fast: true, Run: func(context.Context) []Result {
			return src.Serve.ServeResults()
		}})
	}
	if src.Reachable != nil {
		out = append(out, Check{ID: CheckNetServeReachable, Area: "NET", Timeout: ServeReachableTimeout + 5*time.Second,
			Run: func(ctx context.Context) []Result { return src.Reachable.CheckServeReachable(ctx) }})
	}
	if src.Connectivity != nil {
		out = append(out, Check{ID: CheckNetConnectivity, Area: "NET", Fast: true, Run: func(context.Context) []Result {
			return src.Connectivity.ConnectivityResults()
		}})
	}
	return out
}

// RegisterNetChecks adds NetChecks(src) to reg (DefaultRegistry when nil).
func RegisterNetChecks(reg *Registry, src NetSources) error {
	if reg == nil {
		reg = DefaultRegistry
	}
	for _, c := range NetChecks(src) {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}
