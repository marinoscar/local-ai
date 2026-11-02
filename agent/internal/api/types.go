package api

// Wire types (agent.md section 4.14 step 2).
//
// The request and response bodies of the routes that are implemented are hand-written here, with
// `json` tags that mirror the property names of the schema of the same name in agent/api/openapi.yaml.
// TestTypesMirrorSchemas fails when a field exists on one side only. A route's own issue adds its
// types (and its openapi.yaml entry, and removes its x-localai-status: planned marker) in the same PR.
//
// Implemented so far: GET /health (AGENT-05) and the error envelope of every non-2xx response.

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
