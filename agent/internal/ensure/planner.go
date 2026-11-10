package ensure

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// Facts are the node facts the wants depend on (node.yaml, os-release, the hardware).
type Facts struct {
	Codename, Arch string
	// Backend is the llama backend (cuda, vulkan, cpu); anything else counts as cpu.
	Backend string
	// KiwixOff, PiperOff and WhisperOff say that node.yaml switches the service off explicitly (enabled:
	// false). A missing block is not off.
	KiwixOff, PiperOff, WhisperOff bool
	// WhisperCommit and WhisperBackend are whisper.commit and whisper.backend ("auto" or "" follow llama).
	WhisperCommit, WhisperBackend string
}

// Wants is what the profile and node.yaml want installed.
type Wants struct {
	// Tools are the tarball tools wanted, by name.
	Tools map[string]bool
	// Packages is the package set wanted (empty: none); Essential the part of it a node cannot do without
	// (the build tools and the backend's own packages): a missing essential one the kit cannot supply is
	// reported, a missing optional one is not.
	Packages, Essential []string
	// Whisper is true when the speech program is wanted.
	Whisper     bool
	WhisperWant WhisperWant
}

// WantsFor turns the kit items of the profile (catalog ids) and the node facts into Wants. The profile
// decides what the node should have; node.yaml can only switch a service off.
func WantsFor(profileItems []string, f Facts) Wants {
	has := map[string]bool{}
	for _, id := range profileItems {
		has[id] = true
	}
	w := Wants{Tools: map[string]bool{}}
	w.Tools[kit.ToolKiwix] = has["kit-kiwix-tools"] && !f.KiwixOff
	w.Tools[kit.ToolPmtiles] = has["kit-pmtiles"]
	w.Tools[kit.ToolPiper] = has["kit-piper"] && !f.PiperOff
	if f.Codename != "" && f.Arch != "" && has["kit-debs-"+f.Codename+"-"+f.Arch] {
		w.Packages = kit.DefaultPackages(f.Backend)
		w.Essential = append([]string(nil), kit.BuildPackages...)
		switch f.Backend {
		case "vulkan":
			w.Essential = append(w.Essential, kit.VulkanPackages...)
		case "cuda":
			w.Essential = append(w.Essential, kit.CudaPackages...)
		}
	}
	if has["kit-whisper-cpp"] && !f.WhisperOff {
		w.Whisper = true
		backend := f.WhisperBackend
		if backend == "" || backend == "auto" {
			backend = f.Backend
		}
		if backend == "" {
			backend = "cpu"
		}
		w.WhisperWant = WhisperWant{Commit: f.WhisperCommit, Backend: backend}
	}
	return w
}

// Planner decides the steps. It reads and never changes anything.
type Planner struct {
	Machine Machine
	Kit     Kit
	Clock   Clock
}

// PlanInput is what one planning needs besides the ports.
type PlanInput struct {
	Auto  Auto
	Wants Wants
	// PendingRestart lists the services whose binary was replaced while they ran (the journal remembers).
	PendingRestart []string
}

var toolSteps = []struct {
	id, tool, item string
}{
	{IDKiwix, kit.ToolKiwix, "kit-kiwix-tools"},
	{IDPmtiles, kit.ToolPmtiles, "kit-pmtiles"},
	{IDPiper, kit.ToolPiper, "kit-piper"},
}

// Plan lists the steps this computer needs. A step whose work is done is StatusOK. A step the kit cannot
// supply is StatusBlocked with the reason. A step that only waits for the setting is StatusWaiting.
func (p *Planner) Plan(ctx context.Context, in PlanInput) (Plan, error) {
	plan := Plan{At: p.Clock.Now().UTC()}
	add := func(s Step) {
		if s.Status == StatusPending && in.Auto != AutoOn {
			s.Status = StatusWaiting
		}
		plan.Steps = append(plan.Steps, s)
	}
	if len(in.Wants.Packages) > 0 {
		s, err := p.packagesStep(ctx, in.Wants)
		if err != nil {
			return plan, err
		}
		add(s)
	}
	for _, t := range toolSteps {
		if !in.Wants.Tools[t.tool] {
			continue
		}
		s, err := p.toolStep(ctx, t.id, t.tool, t.item)
		if err != nil {
			return plan, err
		}
		add(s)
	}
	if in.Wants.Whisper {
		steps, err := p.whisperSteps(ctx, in.Wants.WhisperWant)
		if err != nil {
			return plan, err
		}
		for _, s := range steps {
			add(s)
		}
	}
	for _, svc := range in.PendingRestart {
		if svc != "kiwix" {
			continue
		}
		if p.Machine.ServiceActive(ctx, svc) {
			add(Step{ID: IDRestartKiwix, Kind: KindRestart, Target: svc, Status: StatusNeedsConfirm, NeedsConfirmation: true})
		}
	}
	return plan, nil
}

// kitFailure maps a Kit error to the status, reason and doctor code of a blocked step; an error that is
// not a kit answer is the planner's own failure.
func kitFailure(err error) (reason, code string, ok bool) {
	switch {
	case errors.Is(err, ErrKitMissing):
		return ReasonKitMissing, kit.CodeKitMissingNode, true
	case errors.Is(err, ErrKitDamaged):
		return ReasonKitDamaged, kit.CodeKitDamagedFile, true
	}
	return "", "", false
}

func (p *Planner) toolStep(ctx context.Context, id, tool, item string) (Step, error) {
	st := Step{ID: id, Kind: KindTool, Target: tool, Item: item}
	installed := p.Machine.ToolVersion(tool)
	src, version, err := p.Kit.Tool(ctx, tool)
	if err != nil {
		reason, code, ok := kitFailure(err)
		if !ok {
			return st, fmt.Errorf("ensure: the kit check of %s failed: %w", tool, err)
		}
		if installed != "" {
			st.Status, st.Version = StatusOK, installed
			return st, nil
		}
		st.Status, st.Reason, st.Code = StatusBlocked, reason, code
		return st, nil
	}
	st.Source, st.Version, st.From = src.KitDir, version, installed
	if installed == version {
		st.Status, st.From = StatusOK, ""
		return st, nil
	}
	st.Status = StatusPending
	return st, nil
}

func (p *Planner) packagesStep(ctx context.Context, w Wants) (Step, error) {
	st := Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Item: "kit-debs"}
	missing, err := p.Machine.MissingPackages(ctx, w.Packages)
	if err != nil {
		return st, fmt.Errorf("ensure: could not ask dpkg: %w", err)
	}
	if len(missing) == 0 {
		st.Status = StatusOK
		return st, nil
	}
	src, have, err := p.Kit.Packages(ctx)
	if err != nil {
		reason, code, ok := kitFailure(err)
		if !ok {
			return st, fmt.Errorf("ensure: the kit check of the packages failed: %w", err)
		}
		if !anyOf(missing, w.Essential) {
			st.Status = StatusOK // only optional ones are missing and the kit cannot supply them
			return st, nil
		}
		st.Status, st.Reason, st.Code, st.Packages = StatusBlocked, reason, code, missing
		return st, nil
	}
	var installable, unavailable []string
	for _, n := range missing {
		if have[n] {
			installable = append(installable, n)
		} else {
			unavailable = append(unavailable, n)
		}
	}
	sort.Strings(installable)
	switch {
	case len(installable) > 0:
		st.Status, st.Packages, st.Source = StatusPending, installable, src.KitDir
	case anyOf(unavailable, w.Essential):
		st.Status, st.Reason, st.Code, st.Packages = StatusBlocked, ReasonNotInKit, kit.CodeKitMissingNode, essentialOf(unavailable, w.Essential)
	default:
		st.Status = StatusOK
	}
	return st, nil
}

func (p *Planner) whisperSteps(ctx context.Context, w WhisperWant) ([]Step, error) {
	restore := Step{ID: IDWhisperRestore, Kind: KindWhisperRestore, Target: "whisper", Item: "kit-whisper-cpp"}
	if p.Machine.WhisperInstalled() {
		restore.Status = StatusOK
		return []Step{restore}, nil
	}
	file, err := p.Kit.WhisperPrebuilt(ctx, w)
	switch {
	case err == nil:
		restore.Status, restore.File = StatusPending, file
		return []Step{restore}, nil
	case errors.Is(err, ErrKitMissing), errors.Is(err, ErrKitDamaged):
		// No ready-made build: a build from the kit's source is possible, and never automatic.
	default:
		return nil, fmt.Errorf("ensure: the kit check of the speech build failed: %w", err)
	}
	build := Step{ID: IDWhisperBuild, Kind: KindWhisperBuild, Target: "whisper", Item: "kit-whisper-cpp"}
	ok, err := p.Kit.WhisperSource(ctx, w)
	switch {
	case err == nil && ok:
		build.Status, build.NeedsConfirmation = StatusNeedsConfirm, true
	case err == nil || errors.Is(err, ErrKitMissing) || errors.Is(err, ErrKitDamaged):
		reason, code, _ := kitFailure(err)
		if err == nil {
			reason, code = ReasonKitMissing, kit.CodeKitMissingNode
		}
		build.Status, build.Reason, build.Code = StatusBlocked, reason, code
	default:
		return nil, fmt.Errorf("ensure: the kit check of the whisper source failed: %w", err)
	}
	return []Step{build}, nil
}

func anyOf(names, set []string) bool { return len(essentialOf(names, set)) > 0 }

func essentialOf(names, set []string) []string {
	in := map[string]bool{}
	for _, s := range set {
		in[s] = true
	}
	var out []string
	for _, n := range names {
		if in[n] {
			out = append(out, n)
		}
	}
	return out
}

// Describe is a one-line English description of a step, for logs and the journal.
func (s Step) Describe() string {
	parts := []string{s.ID, string(s.Status)}
	if s.Version != "" {
		parts = append(parts, s.Version)
	}
	if len(s.Packages) > 0 {
		parts = append(parts, strings.Join(s.Packages, ","))
	}
	return strings.Join(parts, " ")
}
