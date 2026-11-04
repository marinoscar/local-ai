package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Handler of GET /ark/drives (agent.md section 3.8.17, ark-sync.md section 3.5): every AI-ARK drive
// the detector knows, mounted or not, with its state and its problems. The route only routes: it asks
// a view for the list and wraps it. internal/ark implements the view (internal/api cannot import it,
// the same seam as GET /ark), and the drive states, the doctor codes and the shape of a drive are
// owned by ark-sync.md section 3.5 and 4.1.2. The eject, init, mount and profile routes are another
// issue and another file.

// arkDrivesTimeout is the deadline of GET /ark/drives (agent.md section 3.8): the list is kept in
// memory by the detector, so it is only the room for a slow response writer.
const arkDrivesTimeout = 10 * time.Second

// ArkDrivesView answers GET /ark/drives.
type ArkDrivesView interface {
	// Drives returns the drives in the order of the detector (mount order, then the ones that are
	// not mounted) as a JSON-encodable list, with the title and the fix of every problem in lang
	// ("en" or "es"). A node with no AI-ARK drive answers an empty list. An error is a bug and
	// answers 500; an *Error keeps its status and code.
	Drives(ctx context.Context, lang string) ([]any, error)
}

// ArkDrivesDeps are what GET /ark/drives needs.
type ArkDrivesDeps struct {
	Drives ArkDrivesView
}

// ArkDrivesRoutes returns GET /ark/drives (op, 10 s).
func ArkDrivesRoutes(d ArkDrivesDeps) []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/ark/drives", Auth: []Level{AuthOp}, Timeout: arkDrivesTimeout, Handler: HandlerFunc(d.get)},
	}
}

// RegisterArkDrives adds GET /ark/drives to reg (DefaultRegistry when nil). The wiring in
// internal/app calls it; the package does not register anything at init time.
func RegisterArkDrives(reg *Registry, d ArkDrivesDeps) error {
	if d.Drives == nil {
		return errors.New("api: GET /ark/drives needs a drives view")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(ArkDrivesRoutes(d)...)
}

// arkDrives is the body of GET /ark/drives.
type arkDrives struct {
	Drives []any `json:"drives"`
}

func (d ArkDrivesDeps) get(w http.ResponseWriter, r *http.Request) error {
	drives, err := d.Drives.Drives(r.Context(), InfoFrom(r.Context()).Lang)
	if err != nil {
		return err
	}
	if drives == nil {
		drives = []any{}
	}
	WriteJSON(w, r, http.StatusOK, arkDrives{Drives: drives})
	return nil
}
