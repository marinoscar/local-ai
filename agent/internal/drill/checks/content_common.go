package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// AgentOrigin is the origin the checks address their requests to: the agent's own listener. The
// request goes through AgentHTTP, which the daemon backs with the agent's own handler (no socket, no
// network), so the origin only names the host the middleware sees.
const AgentOrigin = "http://127.0.0.1:7070"

// APIBase is the prefix of the REST routes (agent.md section 3.7); routes such as /library/, /maps/,
// /portal and /chat/ are absolute and have no prefix.
const APIBase = "/api/v1"

// AgentHTTP sends one request to the agent as the internal admin context (drill.md section 6): the
// drill needs no token, and no session is created or left behind. The daemon supplies a Doer that runs
// the agent's handler in process; a test supplies an httptest server or a fake. It never follows a
// redirect: a 301 or 302 is the answer.
type AgentHTTP interface {
	Do(req *http.Request) (*http.Response, error)
}

// reply is a fully read response, bounded in size.
type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

// maxReply bounds what a check reads from one response. The largest legitimate body is the Portal page
// or a tile; a library search page or a speech answer is far smaller.
const maxReply = 4 << 20

// callAgent sends a request to the agent and reads at most limit bytes of the answer (maxReply when 0).
func callAgent(ctx context.Context, c AgentHTTP, method, path string, hdr http.Header, body io.Reader, limit int64) (reply, error) {
	if c == nil {
		return reply{}, errors.New("no way to reach the agent")
	}
	if limit <= 0 {
		limit = maxReply
	}
	req, err := http.NewRequestWithContext(ctx, method, AgentOrigin+path, body)
	if err != nil {
		return reply{}, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return reply{Status: resp.StatusCode, Header: resp.Header}, err
	}
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// getAgent is callAgent for a GET.
func getAgent(ctx context.Context, c AgentHTTP, path string, limit int64) (reply, error) {
	return callAgent(ctx, c, http.MethodGet, path, nil, nil, limit)
}

// escapePath percent-encodes the segments of a path that may hold spaces or non-ASCII letters (a font
// stack, "Noto Sans Regular") and keeps the slashes.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ---- outcomes --------------------------------------------------------------------------------------

func contentPass(details map[string]any) drill.Outcome {
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

func contentWarn(code string, details map[string]any) drill.Outcome {
	return drill.Outcome{Result: drill.ResultWarn, Code: code, Details: details}
}

func contentFail(code string, details map[string]any) drill.Outcome {
	return drill.Outcome{Result: drill.ResultFail, Code: code, Details: details}
}

func contentSkip(reason string, details map[string]any) drill.Outcome {
	if details == nil {
		details = map[string]any{}
	}
	details["reason"] = reason
	return drill.Outcome{Result: drill.ResultSkip, Details: details}
}

// reasonOf is a short, secret-free description of an error for a details map. A request error carries
// the URL of the agent's own route and nothing else of interest; clip keeps a document small.
func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// statusReason is the details text of an unexpected HTTP status.
func statusReason(status int) string { return fmt.Sprintf("http_%d", status) }

// ---- timeouts --------------------------------------------------------------------------------------

// The timeouts of the checks of this package (drill.md section 4.2: completions 120 s, others 30 s).
const (
	// speechTimeout bounds a check that transcribes or synthesises: the routes allow 120 s.
	speechTimeout = 120 * time.Second
	// kiwixTimeout bounds the search of every tier-1 book; a full-text search of a large ZIM on a slow
	// disk takes seconds, and a tier-1 library has about a dozen books.
	kiwixTimeout = 120 * time.Second
	// hashBudget is the time check 4 may spend hashing (drill.md section 4.2); its timeout is the
	// budget plus a margin for the file in flight when the budget ends.
	hashBudget  = 90 * time.Minute
	hashTimeout = hashBudget + 10*time.Minute
)

// ---- orders ----------------------------------------------------------------------------------------

// The number of each check in drill.md section 4.2.
const (
	orderManifest   = 3
	orderHash       = 4
	orderKiwix      = 8
	orderFirstAid   = 9
	orderSTT        = 10
	orderTTS        = 11
	orderRoundtrip  = 12
	orderMapsTile   = 13
	orderMapsSearch = 14
	orderPortal     = 15
	orderDocs       = 16
	orderServices   = 19
)
