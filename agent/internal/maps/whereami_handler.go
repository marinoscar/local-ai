package maps

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// RouteTimeout is the handler deadline of the whereami routes.
const RouteTimeout = 5 * time.Second

// DesktopOrigin is the origin of ai-desktop's own pages.
const DesktopOrigin = "localai://app"

// SameOrigins lists the Origin values PUT and DELETE accept for an agent on port: the loopback
// names and the desktop shell (companion-services.md section 6).
func SameOrigins(port int) []string {
	p := strconv.Itoa(port)
	return []string{"http://127.0.0.1:" + p, "http://localhost:" + p, DesktopOrigin}
}

// Handler serves /maps/whereami. These routes are outside /api/v1, in the unauthenticated survival
// set, loopback only; PUT and DELETE additionally need a same-origin Origin header (CSRF defence:
// a missing or foreign Origin is 403).
type Handler struct {
	Service *Service
	// Origins returns the accepted Origin values (SameOrigins of the agent port).
	Origins func() []string
}

// Routes returns the three routes.
func (h *Handler) Routes() []api.Route {
	none := []api.Level{api.AuthNone}
	return []api.Route{
		{Method: http.MethodGet, Pattern: "/maps/whereami", Absolute: true, Auth: none, Timeout: RouteTimeout, Handler: api.HandlerFunc(h.get)},
		{Method: http.MethodPut, Pattern: "/maps/whereami", Absolute: true, Auth: none, Timeout: RouteTimeout, Handler: api.HandlerFunc(h.put)},
		{Method: http.MethodDelete, Pattern: "/maps/whereami", Absolute: true, Auth: none, Timeout: RouteTimeout, Handler: api.HandlerFunc(h.del)},
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, r, http.StatusOK, h.Service.Position())
	return nil
}

// putBody is the PUT body. Text ("9.93, -84.08" or "9°55'48\"N 84°4'48\"W") may replace lat and lon,
// so a person can type coordinates as they are.
type putBody struct {
	Lat   *float64 `json:"lat"`
	Lon   *float64 `json:"lon"`
	Text  *string  `json:"text"`
	Label *string  `json:"label"`
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) error {
	if err := h.checkOrigin(r); err != nil {
		return err
	}
	var b putBody
	if err := api.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	var lat, lon float64
	switch {
	case b.Text != nil && (b.Lat != nil || b.Lon != nil):
		return invalid("text", "give either text or lat and lon, not both")
	case b.Text != nil:
		var err error
		if lat, lon, err = ParseCoordinates(*b.Text); err != nil {
			return invalid("text", err.Error())
		}
	case b.Lat == nil:
		return invalid("lat", "required")
	case b.Lon == nil:
		return invalid("lon", "required")
	default:
		lat, lon = *b.Lat, *b.Lon
		if err := validate(lat, lon); err != nil {
			return invalid("lat", err.Error())
		}
	}
	label := ""
	if b.Label != nil {
		label = strings.TrimSpace(*b.Label)
	}
	if utf8.RuneCountInString(label) > MaxLabel {
		return invalid("label", "at most 80 characters")
	}
	pos, err := h.Service.Set(lat, lon, label)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, r, http.StatusOK, pos)
	return nil
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) error {
	if err := h.checkOrigin(r); err != nil {
		return err
	}
	if err := h.Service.Clear(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) checkOrigin(r *http.Request) *api.Error {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return api.NewError(api.CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_required"})
	}
	if len(origins) == 1 {
		for _, ok := range h.Origins() {
			if origins[0] == ok {
				return nil
			}
		}
	}
	return api.NewError(api.CodeBadOrigin, http.StatusForbidden, map[string]any{"reason": "origin_not_allowed"})
}

func invalid(field, reason string) *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}
