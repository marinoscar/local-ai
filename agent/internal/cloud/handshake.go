package cloud

import (
	"context"
	"slices"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The two command planes (agent.md section 4.8.6).
const (
	PlaneLocalai = "localai"
	PlaneEAB     = "eab"
)

// The features a handshake can advertise (cloud-app.md section 3.6).
const (
	FeatureState    = "state.v1"
	FeatureCommands = "commands.v1"
	FeatureCatalog  = "catalog.v1"
	FeatureKey      = "key.v1"
	// FeatureTailnet: POST .../localai/tailnet exists (the node reports its own tailnet address, issue #437).
	FeatureTailnet = "tailnet.v1"
	// FeatureHostVitals is EAB's serverFeatures entry that lets heartbeats carry host, power and events.
	FeatureHostVitals = "node_host_vitals"
)

// Handshake is the answer of GET /api/nodes/{id}/localai/config, or what an older cloud without that
// route implies (Legacy).
type Handshake struct {
	NodeID             string
	APIVersion         int
	Features           []string
	CommandPlane       string
	HeartbeatIntervalS int
	StateMinIntervalS  int
	StateMaxIntervalS  int
	ClaimWaitMaxMs     int
	KeyReported        bool
	// Legacy: the cloud answered 404 to the handshake itself (an older cloud app).
	Legacy    bool
	CheckedAt time.Time
}

type handshakeReply struct {
	NodeID             string   `json:"nodeId"`
	APIVersion         int      `json:"apiVersion"`
	Features           []string `json:"features"`
	CommandPlane       string   `json:"commandPlane"`
	HeartbeatIntervalS int      `json:"heartbeatIntervalS"`
	StateMinIntervalS  int      `json:"stateMinIntervalS"`
	StateMaxIntervalS  int      `json:"stateMaxIntervalS"`
	ClaimWaitMaxMs     int      `json:"claimWaitMaxMs"`
	KeyReported        *bool    `json:"keyReported"`
	ServerTime         string   `json:"serverTime"`
}

func (h Handshake) record() HandshakeRecord {
	return HandshakeRecord{
		Features: append([]string{}, h.Features...), CommandPlane: h.CommandPlane, HeartbeatIntervalS: h.HeartbeatIntervalS,
		StateMinIntervalS: h.StateMinIntervalS, StateMaxIntervalS: h.StateMaxIntervalS, ClaimWaitMaxMs: h.ClaimWaitMaxMs,
		CheckedAt: h.CheckedAt,
	}
}

func handshakeFromRecord(r HandshakeRecord) Handshake {
	return Handshake{
		Features: r.Features, CommandPlane: r.CommandPlane, HeartbeatIntervalS: r.HeartbeatIntervalS,
		StateMinIntervalS: r.StateMinIntervalS, StateMaxIntervalS: r.StateMaxIntervalS, ClaimWaitMaxMs: r.ClaimWaitMaxMs,
		KeyReported: true, Legacy: r.CommandPlane == PlaneEAB && !slices.Contains(r.Features, FeatureState), CheckedAt: r.CheckedAt,
	}
}

// handshake runs the handshake and stores its answer in memory and in link.json. A 404 is not an
// error: it means an older cloud, and the link falls back to the EAB command plane and to the state
// carried in the capabilities (LAI-CLD-056). Any other failure is returned for the caller to classify.
func (l *Link) handshake(ctx context.Context) error {
	path, ok := l.serverPath("/localai/config")
	if !ok {
		return ErrNotLinked
	}
	now := l.clk.Now()
	reply, err := l.client().Call(ctx, "GET", path, nil, callTimeout)
	if err != nil {
		if ae := asAPIError(err); ae != nil && ae.Status == 404 {
			l.mu.Lock()
			l.st.hs, l.st.haveHS = Handshake{CommandPlane: PlaneEAB, Legacy: true, CheckedAt: now}, true
			l.st.stateFallback = true
			l.mu.Unlock()
			l.raise(doctor.CLD056, nil)
			l.clear(doctor.AGT012)
			l.saveLink()
			return nil
		}
		return err
	}
	var hr handshakeReply
	if err := reply.Decode(&hr); err != nil {
		return err
	}
	hs := Handshake{
		NodeID: hr.NodeID, APIVersion: hr.APIVersion, Features: append([]string{}, hr.Features...), CommandPlane: hr.CommandPlane,
		HeartbeatIntervalS: boundedS(hr.HeartbeatIntervalS, 5, 60), StateMinIntervalS: boundedS(hr.StateMinIntervalS, 1, 600),
		StateMaxIntervalS: boundedS(hr.StateMaxIntervalS, 5, 3600), ClaimWaitMaxMs: clampInt(hr.ClaimWaitMaxMs, 0, 25000),
		KeyReported: hr.KeyReported == nil || *hr.KeyReported, CheckedAt: now,
	}
	if hs.CommandPlane != PlaneEAB {
		hs.CommandPlane = PlaneLocalai
	}
	if hs.StateMaxIntervalS != 0 && hs.StateMinIntervalS > hs.StateMaxIntervalS {
		hs.StateMaxIntervalS = hs.StateMinIntervalS
	}
	hasState := slices.Contains(hs.Features, FeatureState)
	l.mu.Lock()
	l.st.hs, l.st.haveHS = hs, true
	l.st.stateFallback = !hasState
	l.mu.Unlock()
	if hasState {
		l.clear(doctor.CLD056)
	} else {
		l.raise(doctor.CLD056, nil)
	}
	l.checkSkew(hr.ServerTime, now)
	if slices.Contains(hs.Features, FeatureTailnet) {
		l.kick(l.tailnetWake)
	}
	if !hs.KeyReported {
		// The cloud lost the key (or never had it): push it again.
		_ = l.d.State.Mutate(func(s *state.State) error { s.Key.CloudSynced = false; return nil })
		l.kick(l.keyWake)
	}
	l.saveLink()
	return nil
}

// boundedS keeps an interval the cloud suggested inside [lo, hi] seconds; 0 (absent) stays 0, which
// means "use node.yaml".
func boundedS(v, lo, hi int) int {
	if v <= 0 {
		return 0
	}
	return clampInt(v, lo, hi)
}

// checkSkew compares the cloud's clock with ours: more than five minutes apart raises LAI-AGT-012.
func (l *Link) checkSkew(serverTime string, now time.Time) {
	t, err := time.Parse(time.RFC3339, serverTime)
	if err != nil {
		return
	}
	skew := now.Sub(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > clockSkewLimit {
		l.raise(doctor.AGT012, map[string]any{"skew_seconds": int64(skew / time.Second)})
		return
	}
	l.clear(doctor.AGT012)
}

// hasFeature reports whether the handshake advertised f; false before any handshake and for an
// older cloud.
func (l *Link) hasFeature(f string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.haveHS && !l.st.hs.Legacy && slices.Contains(l.st.hs.Features, f)
}

// hasStateEndpoint reports whether snapshots go to POST .../localai/state.
func (l *Link) hasStateEndpoint() bool {
	l.mu.Lock()
	ok := l.st.haveHS && !l.st.stateFallback && !l.st.hs.Legacy
	l.mu.Unlock()
	return ok && l.callable()
}

// hostVitalsOn reports whether the server advertised node_host_vitals.
func (l *Link) hostVitalsOn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.st.features, FeatureHostVitals)
}

// heartbeatInterval is the current heartbeat period: 300 s while the node is disabled, else the
// cloud's suggestion, else node.yaml's cloud.poll.heartbeat_seconds.
func (l *Link) heartbeatInterval() time.Duration {
	l.mu.Lock()
	state, hs, have := l.st.state, l.st.hs, l.st.haveHS
	l.mu.Unlock()
	if state == StateDisabled {
		return disabledInterval
	}
	if have && hs.HeartbeatIntervalS > 0 {
		return time.Duration(hs.HeartbeatIntervalS) * time.Second
	}
	if s := l.cfg().Cloud.Poll.HeartbeatSeconds; s > 0 {
		return time.Duration(s) * time.Second
	}
	return 15 * time.Second
}

// stateIntervals are the debounce and the maximum period of the state push.
func (l *Link) stateIntervals() (min, max time.Duration) {
	l.mu.Lock()
	hs, have := l.st.hs, l.st.haveHS
	l.mu.Unlock()
	min, max = 5*time.Second, time.Duration(l.cfg().Cloud.Poll.StateSeconds)*time.Second
	if have {
		if hs.StateMinIntervalS > 0 {
			min = time.Duration(hs.StateMinIntervalS) * time.Second
		}
		if hs.StateMaxIntervalS > 0 {
			max = time.Duration(hs.StateMaxIntervalS) * time.Second
		}
	}
	if max < min {
		max = min
	}
	if max <= 0 {
		max = 60 * time.Second
	}
	return min, max
}
