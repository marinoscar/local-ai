package checks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// CodeFirstAidMissing is LAI-SVC-109: a first-aid link of the catalog does not resolve.
const CodeFirstAidMissing = doctor.SVC109

// portalStatusPath is GET /api/v1/portal/status, in English so the document does not depend on the
// Portal's language setting.
const portalStatusPath = APIBase + "/portal/status?lang=en"

// fetchPortalStatus asks the agent for the Portal status as the internal admin (the full session).
func fetchPortalStatus(ctx context.Context, c AgentHTTP) (portal.Status, reply, error) {
	rp, err := getAgent(ctx, c, portalStatusPath, 1<<20)
	if err != nil {
		return portal.Status{}, rp, err
	}
	var st portal.Status
	if rp.Status == http.StatusOK {
		if err := json.Unmarshal(rp.Body, &st); err != nil {
			return portal.Status{}, rp, err
		}
	}
	return st, rp, nil
}

// FirstAidDeps are what check 9 needs.
type FirstAidDeps struct {
	HTTP AgentHTTP
}

// NewFirstAidCheck is check 9, drl.portal.firstaid: every first-aid link the Portal offers (the
// catalog's portal_links, resolved to books that are installed) returns 200 from the library.
//
//	pass: every link of the catalog is available and answers 200.
//	warn: a link is missing, or answers something else (LAI-SVC-109).
//	skip: the catalog has no first-aid links.
func NewFirstAidCheck(d FirstAidDeps) drill.Check {
	return drill.Check{
		ID: "drl.portal.firstaid", Area: "portal", Order: orderFirstAid, Timeout: kiwixTimeout,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runFirstAid(ctx, d) },
	}
}

func runFirstAid(ctx context.Context, d FirstAidDeps) drill.Outcome {
	if d.HTTP == nil {
		return contentSkip("not_available", nil)
	}
	st, rp, err := fetchPortalStatus(ctx, d.HTTP)
	switch {
	case err != nil:
		return contentWarn(CodeFirstAidMissing, map[string]any{"reason": "status_unavailable", "error": reasonOf(err)})
	case rp.Status != http.StatusOK:
		return contentWarn(CodeFirstAidMissing, map[string]any{"reason": "status_unavailable", "status": rp.Status})
	case len(st.FirstAid) == 0:
		return contentSkip("no_links", nil)
	}
	var missing []map[string]any
	for _, l := range st.FirstAid {
		if !l.Available || l.URL == "" {
			missing = append(missing, map[string]any{"key": l.Key, "reason": "not_available"})
			continue
		}
		page, err := getAgent(ctx, d.HTTP, l.URL, 64<<10)
		switch {
		case err != nil:
			missing = append(missing, map[string]any{"key": l.Key, "reason": "request_failed"})
		case page.Status != http.StatusOK:
			missing = append(missing, map[string]any{"key": l.Key, "reason": statusReason(page.Status)})
		}
	}
	details := map[string]any{"links": len(st.FirstAid), "answered": len(st.FirstAid) - len(missing)}
	if len(missing) > 0 {
		details["missing"] = missing
		return contentWarn(CodeFirstAidMissing, details)
	}
	return contentPass(details)
}
