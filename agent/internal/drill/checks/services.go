package checks

import (
	"context"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// ServiceHealth is the part of *health.Supervisor check 19 uses: the rows of GET /services and the deep
// checks of the services that are up (companion-services.md section 4.6).
type ServiceHealth interface {
	Records(ctx context.Context) []health.Record
	Deep(ctx context.Context) []health.DeepResult
}

// ServicesDeps are what check 19 needs.
type ServicesDeps struct {
	Health ServiceHealth
}

// NewServicesCheck is check 19, drl.services: the deep health checks of the companion services, the ones
// checks 8 to 14 do not already cover end to end.
//
//	pass: every service that should run is ok and its deep check finds nothing.
//	warn: a service is degraded or down, or a deep check finds a problem.
//
// A service that is disabled, has no content to serve (check 3 reports that), or is still starting is
// not a finding here.
func NewServicesCheck(d ServicesDeps) drill.Check {
	return drill.Check{
		ID: "drl.services", Area: "services", Order: orderServices,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			if d.Health == nil {
				return contentSkip("not_available", nil)
			}
			var findings []map[string]any
			code := ""
			note := func(c string) {
				if code == "" {
					code = c
				}
			}
			states := map[string]string{}
			for _, r := range d.Health.Records(ctx) {
				states[r.Name] = string(r.State)
				if r.State != health.StateDegraded && r.State != health.StateDown {
					continue
				}
				f := map[string]any{"service": r.Name, "state": string(r.State)}
				for _, p := range r.Problems {
					note(p.Code)
					f["code"] = p.Code
					break
				}
				findings = append(findings, f)
			}
			for _, dr := range d.Health.Deep(ctx) {
				for _, p := range dr.Problems {
					note(p.Code)
					findings = append(findings, map[string]any{"service": dr.Service, "state": "deep", "code": p.Code})
				}
			}
			details := map[string]any{"services": states}
			if len(findings) > 0 {
				sort.SliceStable(findings, func(i, j int) bool {
					return health.Order(findings[i]["service"].(string)) < health.Order(findings[j]["service"].(string))
				})
				details["findings"] = findings
				return contentWarn(code, details)
			}
			return contentPass(details)
		},
	}
}
