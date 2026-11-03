package app

import (
	"sort"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/api"
)

// notWiredYet lists the implemented operations the daemon does not serve yet, each with the reason. An
// entry leaves this list in the PR of the item that wires it (with app.RegisterRoutes, or by an edit of
// build.go); the test fails on an entry that is served after all, so the list only shrinks.
var notWiredYet = map[string]string{
	"POST /ark/sync":     "the dry-run planner (SYNC-05) needs the sync engine's roots and run lock; the run engine (SYNC-07) registers it",
	"POST /speech/tts":   "Piper (SVC-08) registers its routes, its health probe and its voice index with the other companion services",
	"GET /speech/voices": "Piper (SVC-08), as above",
}

// The daemon serves exactly the operations openapi.yaml says are implemented (agent.md section 4.14):
// a route that exists but is not wired here, or that is wired but marked planned, fails this test, so
// the wiring cannot fall behind the packages. It is the third side of the route bijection, next to the
// document against the route constructors (internal/api) and against the handlers' behaviour. The
// routes outside the API prefix (the rendered manuals under /docs/) are not operations of the document.
func TestDaemonServesEveryImplementedOperation(t *testing.T) {
	spec, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	d := r.start()
	r.waitStarted()

	served := map[string]api.Route{}
	for _, rt := range d.Components().API.Routes() {
		if !rt.Absolute {
			served[rt.Method+" "+rt.Pattern] = rt
		}
	}
	documented := map[string]bool{}
	var problems []string
	for _, op := range spec.Operations() {
		documented[op.Key()] = true
		_, ok := served[op.Key()]
		switch {
		case !op.Planned() && !ok && notWiredYet[op.Key()] != "":
			// Implemented by its package, wired by the slice's own item through app.RegisterRoutes.
		case !op.Planned() && !ok:
			problems = append(problems, op.Key()+" is documented as implemented but the daemon does not serve it")
		case op.Planned() && ok:
			problems = append(problems, op.Key()+" is served but still marked planned")
		}
	}
	for key := range served {
		if !documented[key] {
			problems = append(problems, key+" is served but not in openapi.yaml")
		}
		if notWiredYet[key] != "" {
			problems = append(problems, key+" is served: remove it from notWiredYet")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	// The daemon registers into its own registry, never the package-level one.
	if got := len(api.DefaultRegistry.Routes()); got != 0 {
		t.Errorf("%d routes were added to api.DefaultRegistry", got)
	}
	// The gate rule is the same for every route the spec calls mutating: only POST /mode/survival is exempt.
	for key, rt := range served {
		if rt.GateExempt && key != "POST /mode/survival" {
			t.Errorf("%s is exempt from the startup gate", key)
		}
	}
}
