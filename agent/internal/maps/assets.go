package maps

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/static"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Style assets the viewer asks for (companion-services.md section 4.4.3).
const (
	GlyphsTemplate = "/maps/assets/fonts/{fontstack}/{range}.pbf"
	SpritePath     = "/maps/assets/sprites/v4/light"
)

// FontStacks are the font stacks the basemap style uses; readiness needs each one's first range.
var FontStacks = []string{"Noto Sans Regular", "Noto Sans Medium", "Noto Sans Italic"}

// assetRoot is the directory of the best copy of the map-assets item, "" when there is none.
func (s *Maps) assetRoot() string {
	cp, err := s.copies.BestCopy(ark.MapAssetsID)
	if err != nil || cp.Dir == "" {
		return ""
	}
	return cp.Dir
}

// assetsPresent reports whether the labels can be drawn: the first glyph range of every font stack
// and the sprite json exist as regular files below the asset root.
func (s *Maps) assetsPresent() bool {
	root := s.assetRoot()
	if root == "" {
		return false
	}
	need := []string{"sprites/v4/light.json"}
	for _, f := range FontStacks {
		need = append(need, "fonts/"+f+"/0-255.pbf")
	}
	for _, rel := range need {
		p, err := static.SafeJoin(root, rel)
		if err != nil {
			return false
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// serveAsset is GET|HEAD /maps/assets/<path>: a file of the best copy of map-assets. The path rules
// are the agent's static ones (no traversal, no dotfile, a symbolic link only inside the root).
func (s *Maps) serveAsset(w http.ResponseWriter, r *http.Request) error {
	root := s.assetRoot()
	if root == "" {
		return notFound(doctor.SVC031, "not_installed")
	}
	rel := strings.TrimPrefix(r.URL.Path, AssetsPrefix)
	p, err := static.SafeJoin(root, rel)
	if err != nil {
		return api.StaticError(err)
	}
	if strings.EqualFold(filepath.Ext(p), ".pbf") {
		w.Header().Set("Content-Type", "application/x-protobuf")
	}
	if err := static.ServeFile(w, r, p); err != nil {
		return api.StaticError(err)
	}
	return nil
}
