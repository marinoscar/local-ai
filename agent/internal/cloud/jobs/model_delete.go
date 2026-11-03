package jobs

import (
	"context"
	"errors"
	"slices"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

// KeptRoot is a root a delete left alone and why: last_verified_copy, loaded or not_present.
type KeptRoot struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
}

// ModelDeleteResult is the result of model.delete.
type ModelDeleteResult struct {
	ModelID      string     `json:"model_id"`
	DeletedRoots []string   `json:"deleted_roots"`
	KeptRoots    []KeptRoot `json:"kept_roots"`
	FreedBytes   int64      `json:"freed_bytes"`
}

// keptLoaded is the reason of a root left alone because the model is in memory.
const keptLoaded = "loaded"

// newModelDelete is DELETE /models/{id} once per named root, always with keep-last: the sync engine
// refuses to remove the last verified copy, and that root is reported as kept (last_verified_copy)
// instead of failing the command. A model that is loaded is kept too (loaded). A model operation
// that holds modelOpsMu is waited for up to 60 s. Any other error ends the command with its code.
// The decoder has already checked that confirm equals model_id; the agent never takes the cloud's
// word for it.
func newModelDelete(d Deps) cloud.Handler {
	return typed[ModelDeleteParams]{
		name: cloud.TypeModelDelete, decode: decodeModelDelete,
		run: func(ctx context.Context, job cloud.Job, p ModelDeleteParams) (cloud.Outcome, error) {
			out := ModelDeleteResult{ModelID: p.ModelID, DeletedRoots: []string{}, KeptRoots: []KeptRoot{}}
			for _, root := range p.Roots {
				res, err := retryBusy(ctx, d, func() (modelsapi.DeleteResult, error) {
					return d.Models.Delete(ctx, modelsapi.DeleteRequest{ID: p.ModelID, Root: root, KeepLast: true, Actor: job.Actor})
				})
				if reason, kept := keptReason(err); kept {
					out.KeptRoots = append(out.KeptRoots, KeptRoot{Root: root, Reason: reason})
					continue
				}
				if err != nil {
					return cloud.Outcome{}, err
				}
				out.merge(root, res)
			}
			return cloud.Outcome{Result: out}, nil
		},
	}
}

// keptReason maps the refusals that mean "this root stays" to the reason of kept_roots.
func keptReason(err error) (string, bool) {
	var ae *api.Error
	if err == nil || !errors.As(err, &ae) {
		return "", false
	}
	switch {
	case ae.Code == doctor.SYN044:
		return modelsapi.KeptLastVerifiedCopy, true
	case ae.Code == doctor.AGT022 && ae.Details["reason"] == keptLoaded:
		return keptLoaded, true
	}
	return "", false
}

// merge adds the outcome of one root's delete.
func (r *ModelDeleteResult) merge(root string, res modelsapi.DeleteResult) {
	// The result schema takes root ids only: a copy the engine names otherwise is the requested root.
	id := func(s string) string {
		if rootIDRE.MatchString(s) {
			return s
		}
		return root
	}
	for _, c := range res.Deleted {
		if r1 := id(c.Root); !slices.Contains(r.DeletedRoots, r1) {
			r.DeletedRoots = append(r.DeletedRoots, r1)
		}
		r.FreedBytes += max(c.Bytes, 0)
	}
	for _, k := range res.Kept {
		r1 := id(k.Root)
		if !slices.ContainsFunc(r.KeptRoots, func(e KeptRoot) bool { return e.Root == r1 }) {
			r.KeptRoots = append(r.KeptRoots, KeptRoot{Root: r1, Reason: k.Reason})
		}
	}
	// A root that came back neither deleted nor kept had no copy.
	if !slices.Contains(r.DeletedRoots, root) && !slices.ContainsFunc(r.KeptRoots, func(e KeptRoot) bool { return e.Root == root }) {
		r.KeptRoots = append(r.KeptRoots, KeptRoot{Root: root, Reason: modelsapi.KeptNotPresent})
	}
}
