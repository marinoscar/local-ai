package checks

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// fakeModels is a drill.ModelOps that records what it was asked and fails on demand.
type fakeModels struct {
	mu       sync.Mutex
	resident []string
	loads    []string
	evicts   []bool
	failLoad map[string]error
	loadInfo drill.LoadInfo
	readErr  error
}

func (f *fakeModels) Resident(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resident), f.readErr
}
func (f *fakeModels) Default() string { return "" }
func (f *fakeModels) Load(_ context.Context, id string, evict bool) (drill.LoadInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, id)
	f.evicts = append(f.evicts, evict)
	if err := f.failLoad[id]; err != nil {
		return drill.LoadInfo{}, err
	}
	return f.loadInfo, nil
}
func (f *fakeModels) Unload(context.Context, string) error     { return nil }
func (f *fakeModels) SetDefault(context.Context, string) error { return nil }

func (f *fakeModels) loaded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.loads)
}

// answer is one scripted reply of fakeChat.
type answer struct {
	text string
	tps  float64 // 0: no timings
	err  error
}

// fakeChat replays answers in order (the last one repeats) and records the requests.
type fakeChat struct {
	mu   sync.Mutex
	seq  []answer
	reqs []router.CompletionRequest
}

func (f *fakeChat) Complete(_ context.Context, req router.CompletionRequest) (router.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.reqs)
	f.reqs = append(f.reqs, req)
	if len(f.seq) == 0 {
		return router.Completion{}, errors.New("no scripted answer")
	}
	if i >= len(f.seq) {
		i = len(f.seq) - 1
	}
	a := f.seq[i]
	if a.err != nil {
		return router.Completion{}, a.err
	}
	return router.Completion{Text: a.text, TokensPerSecond: a.tps, HasTimings: a.tps > 0, CompletionTokens: 3}, nil
}

func (f *fakeChat) requests() []router.CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// presence says which model ids have verified files on the internal root.
type presence map[string]bool

func (p presence) Present(_ string, m *catalog.Model) bool { return p[m.ID] }

// node is the fixture of a node: its facts and the fakes behind the deps.
type mdlNode struct {
	in     router.Inputs
	pres   presence
	models *fakeModels
	chat   *fakeChat
}

func model(id string, vram, ram int64, roles ...string) catalog.Model {
	return catalog.Model{
		ID: id, DisplayName: id, Engine: "llama", Roles: roles,
		Requirements: catalog.Requirements{Backends: []string{"cuda", "vulkan", "cpu"}, MinVRAMMiB: vram, MinRAMMiB: ram},
	}
}

// newNode builds an 8 GB CUDA node with a profile whose survival list is the given models.
func newMdlNode(t *testing.T, modelsMax int, def string, survival []string, ms ...catalog.Model) *mdlNode {
	t.Helper()
	prof := catalog.Profile{
		ID: "test", Router: catalog.Router{ModelsMax: modelsMax}, DefaultModel: def,
		Survival: catalog.Survival{Models: survival, DefaultModel: def},
	}
	n := &mdlNode{
		pres:   presence{},
		models: &fakeModels{failLoad: map[string]error{}},
		chat:   &fakeChat{},
	}
	for _, m := range ms {
		n.pres[m.ID] = true
	}
	n.in = router.Inputs{
		Catalog: catalog.New("2026.11.02-1", ms, nil, []catalog.Profile{prof}),
		Profile: &prof,
		Roots:   router.Roots{Internal: "/srv/localai"},
		Fit: router.Env{
			Backend: "cuda", GTotalMiB: 8192, RTotalMiB: 16384, RAvailMiB: 12000, MarginMiB: 512, ModelsMax: modelsMax,
		},
	}
	return n
}

func (n *mdlNode) deps() ModelDeps {
	return ModelDeps{
		Models: n.models, Chat: n.chat, Inputs: func() router.Inputs { return n.in },
		Presence: n.pres, Photo: []byte("png-bytes"),
	}
}

// run executes a check against a fresh env.
func runChk(t *testing.T, c drill.Check, env *drill.Env) drill.Outcome {
	t.Helper()
	if env == nil {
		env = &drill.Env{RunID: "drl-test"}
	}
	return c.Run(context.Background(), env)
}

// expand runs an expander and returns its checks by id.
func expand(t *testing.T, x drill.Expander) []drill.Check {
	t.Helper()
	cs, err := x(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func find(t *testing.T, cs []drill.Check, id string) drill.Check {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %v", id, ids(cs))
	return drill.Check{}
}

func ids(cs []drill.Check) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
