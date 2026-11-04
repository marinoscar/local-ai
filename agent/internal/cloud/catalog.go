package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Errors of the catalog fetch.
var (
	// ErrNoApprovedCatalog: the cloud has no approved catalog (404 LAI-CLD-013); the caller falls back
	// to the next source (GitHub).
	ErrNoApprovedCatalog = errors.New("cloud: the cloud has no approved catalog")
	// ErrCatalogNotModified: the cloud answered 304 to the conditional fetch.
	ErrCatalogNotModified = errors.New("cloud: the catalog has not changed")
)

const catalogTimeout = 60 * time.Second

// CatalogBundle is the answer of GET /api/nodes/{id}/localai/catalog (cloud-app.md section 3.6).
type catalogReply struct {
	Version      string               `json:"version"`
	BundleSHA256 string               `json:"bundleSha256"`
	ApprovedAt   string               `json:"approvedAt"`
	Files        []catalog.BundleFile `json:"files"`
}

// FetchCatalog implements catalog.CloudSource (priority 1 of catalog.md section 1): it fetches the
// approved catalog of the cloud and verifies the sha256 of every file against its content. It always
// asks for the full bundle: a manual refresh must see the cloud's truth, not a cached answer. The
// agent installs the bundle after its own lint (catalog.Fetcher).
func (l *Link) FetchCatalog(ctx context.Context) (*catalog.Bundle, error) {
	b, err := l.fetchCatalog(ctx, "")
	return b, err
}

// FetchCatalogIfChanged is FetchCatalog with If-None-Match set to etag (the bundleSha256 of the
// catalog the agent runs): it returns ErrCatalogNotModified on a 304.
func (l *Link) FetchCatalogIfChanged(ctx context.Context, etag string) (*catalog.Bundle, error) {
	return l.fetchCatalog(ctx, etag)
}

func (l *Link) fetchCatalog(ctx context.Context, etag string) (*catalog.Bundle, error) {
	if !l.callable() || !l.active() {
		return nil, ErrNotLinked
	}
	path, ok := l.serverPath("/localai/catalog")
	if !ok {
		return nil, ErrNotLinked
	}
	var opts []RequestOption
	if etag != "" {
		opts = append(opts, WithHeader("If-None-Match", `"`+etag+`"`))
	}
	reply, err := l.client().Call(ctx, "GET", path, nil, catalogTimeout, opts...)
	if err != nil {
		l.noteAuth(err)
		if ae := asAPIError(err); ae != nil {
			if ae.Status == 404 && (ae.Code == doctor.CLD013 || ae.Reason == "no_approved_catalog") {
				return nil, ErrNoApprovedCatalog
			}
			if ae.Status == 404 {
				l.recheckHandshake(ctx)
			}
		}
		return nil, err
	}
	if reply.Status == 304 {
		return nil, ErrCatalogNotModified
	}
	var cr catalogReply
	if err := reply.Decode(&cr); err != nil {
		return nil, fmt.Errorf("cloud: the catalog answer is not valid: %w", err)
	}
	if len(cr.Files) == 0 {
		return nil, errors.New("cloud: the catalog answer has no files")
	}
	for _, f := range cr.Files {
		sum := sha256.Sum256([]byte(f.Content))
		if f.SHA256 == "" || hex.EncodeToString(sum[:]) != strings.ToLower(f.SHA256) {
			return nil, fmt.Errorf("cloud: catalog file %s does not match its sha256", cut(f.Path, 60))
		}
	}
	return &catalog.Bundle{Version: cr.Version, BundleSHA256: cr.BundleSHA256, Files: cr.Files}, nil
}
