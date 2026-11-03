package jobs

import (
	"context"
	"errors"
	"regexp"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// CatalogRefreshResult is the result of catalog.refresh; RejectedCode is null unless the new catalog
// failed its lint (LAI-CAT-002) and the node kept the one it has.
type CatalogRefreshResult struct {
	CatalogVersion string  `json:"catalog_version"`
	Changed        bool    `json:"changed"`
	Accepted       bool    `json:"accepted"`
	RejectedCode   *string `json:"rejected_code"`
}

var catalogVersionRE = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}-\d+$`)

// newCatalogRefresh is POST /catalog/refresh (the node fetches the approved catalog from the cloud,
// lints it and installs it). A catalog that fails the lint is not an error of the command: the answer
// is accepted: false with rejected_code, and catalog_version is the version the node still runs (the
// rejected one when the node has none). Every other refusal ends the command with its code, for
// example LAI-AGT-023 outside online mode.
func newCatalogRefresh(d Deps) cloud.Handler {
	return typed[noParams]{
		name: cloud.TypeCatalogRefresh, decode: decodeNone,
		run: func(ctx context.Context, job cloud.Job, _ noParams) (cloud.Outcome, error) {
			body, err := d.Catalog.RefreshCatalog(ctx, job.Actor)
			var ae *api.Error
			if errors.As(err, &ae) && ae.Code == doctor.CAT002 {
				version := d.Status.Build(ctx, true, statusOptions()).Node.CatalogVersion
				v := ""
				if version != nil {
					v = *version
				}
				if !catalogVersionRE.MatchString(v) {
					v, _ = ae.Details["version"].(string)
				}
				if !catalogVersionRE.MatchString(v) {
					return cloud.Outcome{}, err
				}
				code := doctor.CAT002
				return cloud.Outcome{Result: CatalogRefreshResult{CatalogVersion: v, RejectedCode: &code}}, nil
			}
			if err != nil {
				return cloud.Outcome{}, err
			}
			if !catalogVersionRE.MatchString(body.Version) {
				return cloud.Outcome{}, jobErr(doctor.AGT099, "The catalog version is not in the form the cloud takes.")
			}
			return cloud.Outcome{Result: CatalogRefreshResult{CatalogVersion: body.Version, Changed: body.Changed, Accepted: true}}, nil
		},
	}
}
