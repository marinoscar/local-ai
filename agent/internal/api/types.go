package api

// Wire types (agent.md section 4.14 step 2).
//
// The request and response bodies of the routes that are implemented are hand-written here, with
// `json` tags that mirror the property names of the schema of the same name in agent/api/openapi.yaml.
// TestTypesMirrorSchemas fails when a field exists on one side only. A route's own issue adds its
// types (and its openapi.yaml entry, and removes its x-localai-status: planned marker) in the same PR.
//
// Implemented so far: GET /health (AGENT-05), the error envelope of every non-2xx response and the /cloud
// routes (AGENT-17).

// Health is the body of GET /health (agent.md section 3.8.1; schema Health).
type Health struct {
	// OK is true whenever the agent answers: a damaged or unwritable state degrades into memory and
	// is reported as an alert, never by refusing to serve. The key stays in the body so that
	// clients keep one shape.
	OK bool `json:"ok"`
	// Version is the agent version (aiagent version).
	Version string `json:"version"`
	// Mode is the current mode: online, offline or survival.
	Mode string `json:"mode"`
	// Starting is true until startup step 17 completes (Server.SetStarting).
	Starting bool `json:"starting"`
}

// ErrorEnvelope is the JSON body of every non-2xx response (agent.md section 3.7; schema ErrorEnvelope).
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody is the object inside ErrorEnvelope. Code is always a doctor code, Fix one plain-language
// sentence, Details an object (empty when the route documents no keys).
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Fix     string         `json:"fix"`
	Details map[string]any `json:"details"`
}

// Cloud is the body of GET /cloud (agent.md section 3.8.26; schema Cloud). Only enrolled is
// guaranteed by the schema; this agent always sends every member, null or empty when not known.
type Cloud struct {
	Enrolled        bool    `json:"enrolled"`
	URL             *string `json:"url"`
	State           string  `json:"state"`
	ServerNodeID    *string `json:"server_node_id"`
	Name            *string `json:"name"`
	Since           *string `json:"since"`
	LastHeartbeatAt *string `json:"last_heartbeat_at"`
	LastClaimAt     *string `json:"last_claim_at"`
	LastError       *string `json:"last_error"`
	// Credential is the node credential masked as nod_...<last four>; the credential is never returned.
	Credential     *string        `json:"credential"`
	EligibleTypes  []string       `json:"eligible_types"`
	ServerFeatures []string       `json:"server_features"`
	StateTransport string         `json:"state_transport"`
	Inflight       map[string]any `json:"inflight"`
	KeySynced      bool           `json:"key_synced"`
	// Paused: a drill runs and the link sends nothing (agent.md section 4.8.10).
	Paused bool `json:"paused"`
}

// CloudEnrollRequest is the body of POST /cloud/enroll (schema CloudEnrollRequest).
type CloudEnrollRequest struct {
	URL        string `json:"url"`
	Credential string `json:"credential"`
}

// CloudEnrolled is the answer of POST /cloud/enroll (schema CloudEnrolled).
type CloudEnrolled struct {
	State        string `json:"state"`
	ServerNodeID string `json:"server_node_id,omitempty"`
	Reattached   bool   `json:"reattached,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

// CloudUnenrolled is the answer of DELETE /cloud (schema CloudUnenrolled).
type CloudUnenrolled struct {
	Enrolled bool `json:"enrolled"`
}

// ResendKeyResult is the answer of POST /cloud/resend-key (schema ResendKeyResult).
type ResendKeyResult struct {
	KeySynced   bool   `json:"key_synced"`
	Fingerprint string `json:"fingerprint"`
}
