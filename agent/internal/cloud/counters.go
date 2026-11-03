package cloud

import "sync/atomic"

// Counters are the heartbeat's vitals.counters (agent.md section 4.8.3). The link owns
// HeartbeatFailures; the claim loop (AGENT-18) increments the rest through Link.Counters().
type Counters struct {
	Claims             atomic.Int64
	EmptyPolls         atomic.Int64
	ClaimFailures      atomic.Int64
	Succeeded          atomic.Int64
	Failed             atomic.Int64
	RateLimited        atomic.Int64
	LeaseRenewals      atomic.Int64
	LeaseRenewFailures atomic.Int64
	HeartbeatFailures  atomic.Int64
	WatchdogTrips      atomic.Int64
}

// CountersWire is the wire form of Counters.
type CountersWire struct {
	Claims             int64 `json:"claims"`
	EmptyPolls         int64 `json:"emptyPolls"`
	ClaimFailures      int64 `json:"claimFailures"`
	Succeeded          int64 `json:"succeeded"`
	Failed             int64 `json:"failed"`
	RateLimited        int64 `json:"rateLimited"`
	LeaseRenewals      int64 `json:"leaseRenewals"`
	LeaseRenewFailures int64 `json:"leaseRenewFailures"`
	HeartbeatFailures  int64 `json:"heartbeatFailures"`
	WatchdogTrips      int64 `json:"watchdogTrips"`
}

// Wire returns the current values.
func (c *Counters) Wire() CountersWire {
	return CountersWire{
		Claims: c.Claims.Load(), EmptyPolls: c.EmptyPolls.Load(), ClaimFailures: c.ClaimFailures.Load(),
		Succeeded: c.Succeeded.Load(), Failed: c.Failed.Load(), RateLimited: c.RateLimited.Load(),
		LeaseRenewals: c.LeaseRenewals.Load(), LeaseRenewFailures: c.LeaseRenewFailures.Load(),
		HeartbeatFailures: c.HeartbeatFailures.Load(), WatchdogTrips: c.WatchdogTrips.Load(),
	}
}

// Map returns the values as the flat map /status?counters=1 shows (statusapi.CounterView), with the
// keys cloud_<name>.
func (c *Counters) Map() map[string]any {
	w := c.Wire()
	return map[string]any{
		"cloud_claims": w.Claims, "cloud_failures": w.ClaimFailures, "cloud_empty_polls": w.EmptyPolls,
		"cloud_succeeded": w.Succeeded, "cloud_failed": w.Failed, "cloud_rate_limited": w.RateLimited,
		"cloud_lease_renewals": w.LeaseRenewals, "cloud_lease_renew_failures": w.LeaseRenewFailures,
		"cloud_heartbeat_failures": w.HeartbeatFailures, "cloud_watchdog_trips": w.WatchdogTrips,
	}
}
