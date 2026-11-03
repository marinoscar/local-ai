package health

import (
	"context"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// StatusView is a source of service rows: *services.Watcher (llama, and kiwix and whisper when it is
// not limited) and *Supervisor (kiwix, whisper, piper, maps) both are one. It is the shape of
// api.ServiceStatusSource and statusapi.ServiceView.
type StatusView interface {
	Snapshot(ctx context.Context) []services.Status
}

type combined []StatusView

// Combine returns a view that merges the rows of several views by service name: a name in a later view
// replaces the row of an earlier one (so the supervisor's row wins over a watcher that still lists
// the service), and the result is in the canonical order llama, kiwix, whisper, piper, maps. A nil
// view is skipped.
func Combine(views ...StatusView) StatusView {
	var vs combined
	for _, v := range views {
		if v != nil {
			vs = append(vs, v)
		}
	}
	return vs
}

// Snapshot implements StatusView.
func (c combined) Snapshot(ctx context.Context) []services.Status {
	byName := map[string]services.Status{}
	var names []string
	for _, v := range c {
		for _, st := range v.Snapshot(ctx) {
			if _, ok := byName[st.Name]; !ok {
				names = append(names, st.Name)
			}
			byName[st.Name] = st
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return Order(names[i]) < Order(names[j]) })
	out := make([]services.Status, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out
}
