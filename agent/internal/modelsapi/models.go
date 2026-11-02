package modelsapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// engines are the catalog engines GET /models?engine= accepts (catalog.md section 3).
var engines = []string{"llama", "whisper", "piper"}

// ListOptions are the filters of GET /models.
type ListOptions struct {
	// Loadable keeps the models with loadable true.
	Loadable bool
	// Engine keeps one engine (llama, whisper or piper); "" keeps all.
	Engine string
}

// List is GET /models: every catalog model merged with its local state, from cached values only (the
// merge never calls the router).
func (s *Service) List(ctx context.Context, opts ListOptions) ModelList {
	view := s.d.Models.Models(lang(ctx))
	out := ModelList{ModelsMax: view.ModelsMax, Models: []statusapi.ModelRow{}}
	if view.Default != "" {
		def := view.Default
		out.Default = &def
	}
	for _, row := range view.Rows {
		if opts.Loadable && !row.Loadable {
			continue
		}
		if opts.Engine != "" && row.Engine != opts.Engine {
			continue
		}
		out.Models = append(out.Models, row)
	}
	return out
}

// Get is GET /models/{id}: the row of List plus the model's files and its router preset. An id that
// is not in the catalog is 404 LAI-AGT-021.
func (s *Service) Get(ctx context.Context, id string) (ModelDetail, error) {
	cm, ok := s.d.Catalog.Get().Catalog.Model(id)
	if !ok {
		return ModelDetail{}, errUnknownModel(id)
	}
	view := s.d.Models.Models(lang(ctx))
	idx := slices.IndexFunc(view.Rows, func(r statusapi.ModelRow) bool { return r.ID == id })
	if idx < 0 {
		// The catalog changed between the two reads: the model is gone from the view too.
		return ModelDetail{}, errUnknownModel(id)
	}
	row := view.Rows[idx]

	// Where the files are: the first root with a complete copy.
	copyRoot := ""
	if len(row.Locations) > 0 {
		copyRoot = row.Locations[0].Root
	}
	det := ModelDetail{ModelRow: row, Files: make([]ModelFile, 0, len(cm.Files))}
	for _, f := range cm.Files {
		mf := ModelFile{Kind: f.Kind, Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256}
		if copyRoot != "" {
			if p, err := router.ModelFilePath(copyRoot, id, f.Name); err == nil {
				mf.Path = p
			}
		}
		det.Files = append(det.Files, mf)
	}
	if hasPreset(row) && copyRoot != "" {
		if p, err := router.BuildPreset(cm, copyRoot, row.Aliases); err == nil {
			det.Preset = router.RenderSection(p, row.Default)
		}
	}
	return det, nil
}

// hasPreset says whether the router has a section for the model: a complete copy of a llama model
// that fits this node (a model that would load if the router answered counts).
func hasPreset(row statusapi.ModelRow) bool {
	if !row.Present || row.Engine != "llama" {
		return false
	}
	return row.Loadable || (row.LoadableReason != nil && row.LoadableReason.Code == doctor.LLM004)
}

func errUnknownModel(id string) *api.Error {
	return api.NewError(api.CodeNotFound, http.StatusNotFound, map[string]any{"model": id, "kind": "model"})
}

// findModel returns the catalog entry of id, or the 404 of an unknown id.
func (s *Service) findModel(id string) (*catalog.Model, error) {
	cm, ok := s.d.Catalog.Get().Catalog.Model(id)
	if !ok {
		return nil, errUnknownModel(id)
	}
	return cm, nil
}

// flag reads a boolean query parameter: 1, true, 0 or false, or absent (def).
func flag(r *http.Request, name string, def bool) (bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": name, "reason": "invalid_value", "allowed": []string{"0", "1"},
		})
	}
	return b, nil
}

func (s *Service) listModels(w http.ResponseWriter, r *http.Request) error {
	loadable, err := flag(r, "loadable", false)
	if err != nil {
		return err
	}
	engine := r.URL.Query().Get("engine")
	if engine != "" && !slices.Contains(engines, engine) {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{
			"field": "engine", "reason": "invalid_value", "allowed": engines,
		})
	}
	api.WriteJSON(w, r, http.StatusOK, s.List(r.Context(), ListOptions{Loadable: loadable, Engine: engine}))
	return nil
}

func (s *Service) getModel(w http.ResponseWriter, r *http.Request) error {
	det, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusOK, det)
	return nil
}
