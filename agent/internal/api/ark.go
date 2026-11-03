package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Handler of GET /ark (agent.md section 3.8.17, ark-sync.md sections 3.5 and 4.12): the readiness
// report of every mounted content root, `{"roots": [<READY.json of each root>]}`. The route only
// routes: it asks a view for the reports and wraps them. internal/ark/ready implements the view
// (internal/api cannot import it, the same seam as the sync routes), and the report schema, the
// colours and the doctor codes are owned by ark-sync.md section 4.12. GET /ark/drives is another issue and
// another file.

// arkTimeout is the deadline of GET /ark (agent.md section 3.8): the reports are cached in memory,
// so it is only the room for a first computation after a restart.
const arkTimeout = 10 * time.Second

// ArkReadyView answers GET /ark.
type ArkReadyView interface {
	// Ready returns one report per mounted root, internal first, as a JSON-encodable list: the
	// READY.json of the root with the title of every problem in lang ("en" or "es"). A node with no
	// mounted root answers an empty list. An error is a bug or an I/O failure and answers 500; an
	// *Error keeps its status and code.
	Ready(ctx context.Context, lang string) ([]any, error)
}

// ArkDeps are what GET /ark needs.
type ArkDeps struct {
	Ready ArkReadyView
}

// ArkRoutes returns GET /ark (op, 10 s).
func ArkRoutes(d ArkDeps) []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/ark", Auth: []Level{AuthOp}, Timeout: arkTimeout, Handler: HandlerFunc(d.get)},
	}
}

// RegisterArk adds GET /ark to reg (DefaultRegistry when nil). The wiring in internal/app calls it;
// the package does not register anything at init time.
func RegisterArk(reg *Registry, d ArkDeps) error {
	if d.Ready == nil {
		return errors.New("api: GET /ark needs a readiness view")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkRoutes(d)...)
}

// arkRoots is the body of GET /ark.
type arkRoots struct {
	Roots []any `json:"roots"`
}

func (d ArkDeps) get(w http.ResponseWriter, r *http.Request) error {
	roots, err := d.Ready.Ready(r.Context(), InfoFrom(r.Context()).Lang)
	if err != nil {
		return err
	}
	if roots == nil {
		roots = []any{}
	}
	WriteJSON(w, r, http.StatusOK, arkRoots{Roots: roots})
	return nil
}
