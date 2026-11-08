// Package checks holds the concrete checks of the drill (drill.md section 4.2). Each file is one
// area: llm.go, vlm.go, fit.go and restorecheck.go here (DRILL-03); the others are other issues'. A
// check only sees small interfaces (the model layer, the chat endpoint, the node's facts), never the
// router, systemd or the network, so every pass, warn and fail path runs against fakes.
package checks

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

// Timeouts of the model checks (drill.md section 4.2: a model load 300 s, a completion 120 s).
const (
	// LoadTimeout is the model load timeout when node.yaml does not say; the router doubles it for
	// weights on an ark drive.
	LoadTimeout = 300 * time.Second
	// CompletionTimeout bounds one chat completion.
	CompletionTimeout = 120 * time.Second
)

// The checks' numbers in drill.md section 4.2.
const (
	orderAnswer  = 5
	orderPhoto   = 6
	orderFit     = 7
	orderRestore = 22
)

// Speed floors of check 5 (drill.md section 4.2): tokens per second of generation.
const (
	// FloorCPU is the floor of a cpu-only node.
	FloorCPU = 2.0
	// FloorGPU is the floor of a node with a GPU.
	FloorGPU = 8.0
)

// Chatter sends one chat completion; *router.Client implements it. The model must already be loaded.
type Chatter interface {
	Complete(ctx context.Context, req router.CompletionRequest) (router.Completion, error)
}

// ModelDeps is what the model checks need.
type ModelDeps struct {
	// Models loads, unloads and lists models through the orchestrator (the one path users use).
	Models drill.ModelOps
	// Chat asks a loaded model a question.
	Chat Chatter
	// Inputs returns the facts a decision is made from: the catalog, the profile, the roots, the fit
	// environment (a fresh hardware snapshot). It is called once per check.
	Inputs func() router.Inputs
	// Presence says whether a model's verified files are on a root (router.ManifestPresence).
	Presence router.Presence
	// Photo is seven.png (media.SevenPNG).
	Photo []byte
}

func (d ModelDeps) validate() error {
	switch {
	case d.Models == nil:
		return errNeeds("the model layer")
	case d.Chat == nil:
		return errNeeds("the chat endpoint")
	case d.Inputs == nil:
		return errNeeds("the node inputs")
	case d.Presence == nil:
		return errNeeds("the presence rule")
	case len(d.Photo) == 0:
		return errNeeds("the test photo")
	}
	return nil
}

type needsError string

func (e needsError) Error() string { return "drill checks: the model checks need " + string(e) }

func errNeeds(what string) error { return needsError(what) }

// survivalModel is one entry of profile.survival.models as the checks see it.
type survivalModel struct {
	ID        string
	Model     *catalog.Model // nil when the profile names a model the catalog does not have
	Present   bool
	Loadable  bool
	Reason    *router.Reason // why it is not loadable
	Vision    bool
	Reasoning bool
}

// survivalModels lists profile.survival.models in the profile's order, without duplicates, with what
// the node can say about each one. A node without a profile has none.
func survivalModels(in router.Inputs, pres router.Presence) []survivalModel {
	if in.Profile == nil || in.Catalog == nil {
		return nil
	}
	var out []survivalModel
	seen := map[string]bool{}
	for _, id := range in.Profile.Survival.Models {
		if seen[id] {
			continue
		}
		seen[id] = true
		sm := survivalModel{ID: id}
		m, ok := in.Catalog.Model(id)
		if !ok {
			why := router.Reason{Message: "the model is not in the catalog"}
			sm.Reason = &why
			out = append(out, sm)
			continue
		}
		sm.Model = m
		for _, root := range in.Roots.Ordered() {
			if pres != nil && pres.Present(root, m) {
				sm.Present = true
				break
			}
		}
		sm.Loadable, sm.Reason = in.Fit.Loadable(m, sm.Present)
		sm.Vision = hasRole(m, "vision")
		sm.Reasoning = m.ClientHints != nil && m.ClientHints.Reasoning
		out = append(out, sm)
	}
	return out
}

func hasRole(m *catalog.Model, role string) bool {
	for _, r := range m.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// defaultModel is the survival default (the model --quick tests).
func defaultModel(in router.Inputs) string {
	if in.Profile == nil {
		return ""
	}
	return in.Profile.Survival.DefaultModel
}

// loadTimeout is the load timeout the node uses (llama.load_timeout_seconds, 300 s by default), doubled
// for weights on an ark drive: the longest a single model load can take.
func loadTimeout(in router.Inputs) time.Duration {
	t := LoadTimeout
	if s := in.Llama.LoadTimeoutSeconds; s > 0 {
		t = time.Duration(s) * time.Second
	}
	return 2 * t
}

// ModelError details: the code of the failed operation, when it had one.
func loadFailureDetails(id string, err error) map[string]any {
	d := map[string]any{"model": id, "reason": "load_failed", "error": clip(err.Error(), 300)}
	if me, ok := asModelError(err); ok && me.Code != "" {
		d["load_code"] = me.Code
	}
	return d
}

func asModelError(err error) (*drill.ModelError, bool) {
	var me *drill.ModelError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// clip cuts s to n bytes (the cloud accepts detail strings of up to 500 characters).
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func join(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return strings.Join(ids, ", ")
}

func sorted(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// Register adds the model checks to a registry: check 5 and 6 per survival model (as groups, since
// the models depend on the node), check 7 and check 22.
func Register(reg *drill.Registry, d ModelDeps) error {
	if err := d.validate(); err != nil {
		return err
	}
	if err := reg.AddGroup("llm.answer", LLMAnswerChecks(d)); err != nil {
		return err
	}
	if err := reg.AddGroup("vlm.photo", VLMPhotoChecks(d)); err != nil {
		return err
	}
	if err := reg.Add(FitCheck(d)); err != nil {
		return err
	}
	return reg.Add(RestoreCheck(d))
}
