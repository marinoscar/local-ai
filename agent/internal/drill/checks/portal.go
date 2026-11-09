package checks

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// CodePortalFailed is LAI-DRL-050: the Portal or the chat page does not work.
const CodePortalFailed = doctor.DRL050

// PortalDeps are what check 15 needs.
type PortalDeps struct {
	// HTTP reaches the agent as the internal admin. The node is not in survival mode during a drill, so
	// the chat page (which the agent opens to anonymous callers only in survival) is asked for with an
	// admin context, and no Portal session is created.
	HTTP AgentHTTP
}

// NewPortalCheck is check 15, drl.portal (critical): the pages a person with a phone needs.
//
//	GET /portal                   200 with a Content-Security-Policy header
//	GET /api/v1/portal/status     200 and a valid status document
//	GET /chat/                    200 and the llama WebUI (an HTML page)
//	GET /chat/props               200
//
// pass: all four. fail: any of them does not (LAI-DRL-050); the details name which.
func NewPortalCheck(d PortalDeps) drill.Check {
	return drill.Check{
		ID: "drl.portal", Area: "portal", Critical: true, Order: orderPortal, FailCode: CodePortalFailed,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runPortal(ctx, d) },
	}
}

func runPortal(ctx context.Context, d PortalDeps) drill.Outcome {
	if d.HTTP == nil {
		return contentSkip("not_available", nil)
	}
	failed := map[string]any{}
	note := func(part, reason string) { failed[part] = reason }

	// The Portal page and its policy.
	switch rp, err := getAgent(ctx, d.HTTP, "/portal", maxReply); {
	case err != nil:
		note("portal", "request_failed")
	case rp.Status != http.StatusOK:
		note("portal", statusReason(rp.Status))
	case rp.Header.Get("Content-Security-Policy") == "":
		note("portal", "no_csp")
	case !looksLikeHTML(rp.Body):
		note("portal", "not_html")
	}

	// The status document the Portal draws from.
	switch st, rp, err := fetchPortalStatus(ctx, d.HTTP); {
	case err != nil && rp.Status == http.StatusOK:
		note("status", "damaged")
	case err != nil:
		note("status", "request_failed")
	case rp.Status != http.StatusOK:
		note("status", statusReason(rp.Status))
	default:
		if why := statusInvalid(st); why != "" {
			note("status", why)
		}
	}

	// The chat page and the props call the WebUI makes first.
	switch rp, err := getAgent(ctx, d.HTTP, "/chat/", maxReply); {
	case err != nil:
		note("chat", "request_failed")
	case rp.Status != http.StatusOK:
		note("chat", statusReason(rp.Status))
	case !looksLikeHTML(rp.Body):
		note("chat", "not_html")
	}
	switch rp, err := getAgent(ctx, d.HTTP, "/chat/props", 1<<20); {
	case err != nil:
		note("chat_props", "request_failed")
	case rp.Status != http.StatusOK:
		note("chat_props", statusReason(rp.Status))
	}

	if len(failed) > 0 {
		return contentFail(CodePortalFailed, map[string]any{"failed": failed})
	}
	return contentPass(map[string]any{"pages": 4})
}

// looksLikeHTML is true for a page that starts like one; the SPA shells and the llama WebUI do.
func looksLikeHTML(b []byte) bool {
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}

// statusInvalid says what is wrong with a Portal status document, or "" when it is valid: the schema
// version this agent writes, a node, a mode and a generation time.
func statusInvalid(st portal.Status) string {
	switch {
	case st.SchemaVersion != portal.SchemaVersion:
		return "schema_version"
	case st.NodeID == "":
		return "no_node_id"
	case st.Mode == "":
		return "no_mode"
	}
	if _, err := time.Parse(time.RFC3339, st.GeneratedAt); err != nil {
		return "generated_at"
	}
	return ""
}
