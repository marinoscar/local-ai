package ensure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

func execOf(r *rig) *Executor {
	return &Executor{Installer: r.inst, Kit: r.kit, Audit: r.audit, Clock: r.clock, WhisperWant: func() WhisperWant { return r.wants.WhisperWant }}
}

func TestAllowlistRefusesWhatItDoesNotList(t *testing.T) {
	ok := Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix}
	cases := []struct {
		name string
		step Step
		ok   bool
	}{
		{"kiwix-tools", ok, true},
		{"packages", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"cmake", "libvulkan-dev"}}, true},
		{"the whisper restore", Step{ID: IDWhisperRestore, Kind: KindWhisperRestore, Target: "whisper"}, true},
		{"the whisper build, flagged as needing a confirmation", Step{ID: IDWhisperBuild, Kind: KindWhisperBuild, Target: "whisper", NeedsConfirmation: true}, true},
		{"the kiwix restart, flagged", Step{ID: IDRestartKiwix, Kind: KindRestart, Target: "kiwix", NeedsConfirmation: true}, true},

		{"the agent binary is no tool step", Step{ID: "tool.agent", Kind: KindTool, Target: kit.ToolAgent}, false},
		{"an id nobody listed", Step{ID: "shell.run", Kind: KindTool, Target: "sh"}, false},
		{"a known id with another target", Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolAgent}, false},
		{"a known id with another kind", Step{ID: IDKiwix, Kind: KindPackages, Target: kit.ToolKiwix}, false},
		{"the build without the confirmation flag", Step{ID: IDWhisperBuild, Kind: KindWhisperBuild, Target: "whisper"}, false},
		{"the restart without the confirmation flag", Step{ID: IDRestartKiwix, Kind: KindRestart, Target: "kiwix"}, false},
		{"a tool step claiming to need a confirmation", Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix, NeedsConfirmation: true}, false},
		{"a restart of llama", Step{ID: "restart.llama", Kind: KindRestart, Target: "llama", NeedsConfirmation: true}, false},
		{"packages with nothing in them", Step{ID: IDPackages, Kind: KindPackages, Target: "packages"}, false},
		{"a package name apt would read as an option", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"--allow-unauthenticated"}}, false},
		{"a package name with a version", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"cmake=3.1"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Allowed(tc.step)
			if tc.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("want ErrNotAllowed, got %v", err)
			}
		})
	}
	t.Run("every id of StepIDs is on the list, and only those", func(t *testing.T) {
		if len(StepIDs) != len(allowlist) {
			t.Fatalf("%d ids, %d rows", len(StepIDs), len(allowlist))
		}
		for _, id := range StepIDs {
			if !KnownStep(id) {
				t.Errorf("%s is not on the allowlist", id)
			}
		}
	})
}

func TestExecutorRefusesAndTouchesNothing(t *testing.T) {
	r := newRig(t).build()
	ex := execOf(r)
	for _, st := range []Step{
		{ID: "tool.agent", Kind: KindTool, Target: kit.ToolAgent, Status: StatusPending},
		{ID: IDWhisperBuild, Kind: KindWhisperBuild, Target: "whisper", Status: StatusPending}, // no confirmation flag
	} {
		res := ex.Run(context.Background(), st, audit.System(), "run-1")
		if !errors.Is(res.Err, ErrNotAllowed) || res.Step.Status != StatusFailed {
			t.Errorf("%s: %+v", st.ID, res)
		}
	}
	if calls := r.inst.Calls(); len(calls) != 0 {
		t.Errorf("the installer was called: %v", calls)
	}
}

func TestExecutorRunsOneActionWithTheExactArguments(t *testing.T) {
	cases := []struct {
		name string
		step Step
		set  func(r *rig)
		want string
	}{
		{"a tool is installed from the kit folder that verified now", Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix, Status: StatusPending},
			nil, "tool:kiwix-tools:/srv/localai/kit"},
		{"packages: only those the kit still carries, sorted", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"git", "gone", "cmake"}, Status: StatusPending},
			nil, "packages:/srv/localai/kit:cmake,git"},
		{"the restore uses the file the kit offers now", Step{ID: IDWhisperRestore, Kind: KindWhisperRestore, Target: "whisper", Status: StatusPending},
			nil, "restore:/srv/localai/kit/prebuilt/whisper.tar.zst"},
		{"the build", Step{ID: IDWhisperBuild, Kind: KindWhisperBuild, Target: "whisper", NeedsConfirmation: true, Status: StatusNeedsConfirm}, nil, "build"},
		{"the restart", Step{ID: IDRestartKiwix, Kind: KindRestart, Target: "kiwix", NeedsConfirmation: true, Status: StatusNeedsConfirm}, nil, "restart:kiwix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t).build()
			if tc.set != nil {
				tc.set(r)
			}
			res := execOf(r).Run(context.Background(), tc.step, audit.System(), "run-1")
			if res.Err != nil || res.Step.Status != StatusDone {
				t.Fatalf("%+v", res)
			}
			if calls := r.inst.Calls(); len(calls) != 1 || calls[0] != tc.want {
				t.Errorf("calls %v, want [%s]", calls, tc.want)
			}
		})
	}
}

func TestNothingRunsFromAKitItemThatDoesNotVerifyNow(t *testing.T) {
	cases := []struct {
		name string
		step Step
		set  func(r *rig)
		code string
	}{
		{"a tool whose item went missing after the plan", Step{ID: IDPmtiles, Kind: KindTool, Target: kit.ToolPmtiles},
			func(r *rig) { r.kit.tools[kit.ToolPmtiles] = toolOffer{err: ErrKitMissing} }, doctor.KIT040},
		{"a tool with a bad checksum", Step{ID: IDPmtiles, Kind: KindTool, Target: kit.ToolPmtiles},
			func(r *rig) { r.kit.tools[kit.ToolPmtiles] = toolOffer{err: ErrKitDamaged} }, doctor.KIT001},
		{"packages whose repository went away", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"cmake"}},
			func(r *rig) { r.kit.pkgErr = ErrKitMissing }, doctor.KIT040},
		{"a damaged package repository", Step{ID: IDPackages, Kind: KindPackages, Target: "packages", Packages: []string{"cmake"}},
			func(r *rig) { r.kit.pkgErr = ErrKitDamaged }, doctor.KIT001},
		{"a ready-made build that is no longer there", Step{ID: IDWhisperRestore, Kind: KindWhisperRestore, Target: "whisper"},
			func(r *rig) { r.kit.prebuiltErr = ErrKitMissing }, doctor.KIT040},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t).build()
			tc.set(r)
			res := execOf(r).Run(context.Background(), tc.step, audit.System(), "run-1")
			if res.Step.Status != StatusFailed || res.Step.Code != tc.code {
				t.Fatalf("%+v", res.Step)
			}
			if calls := r.inst.Calls(); len(calls) != 0 {
				t.Errorf("installed anyway: %v", calls)
			}
		})
	}
}

type codedErr struct{ code string }

func (c codedErr) Error() string { return "boom " + c.code }
func (c codedErr) Code() string  { return c.code }

func TestFailuresCarryTheCodeOfTheInstaller(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"a code of its own (a bad signature)", codedErr{doctor.KIT044}, doctor.KIT044},
		{"the library installer's code", codedErr{doctor.SVC003}, doctor.SVC003},
		{"a checksum that does not match", fmt.Errorf("x: %w", ErrKitDamaged), doctor.KIT001},
		{"anything else is LAI-KIT-047", errors.New("apt exploded"), doctor.KIT047},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t).build()
			r.inst.fail["tool:kiwix-tools"] = tc.err
			res := execOf(r).Run(context.Background(), Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix}, audit.System(), "run-1")
			if res.Step.Status != StatusFailed || res.Step.Code != tc.code || res.Step.Error == "" {
				t.Fatalf("%+v", res.Step)
			}
		})
	}
}

func TestEveryStepIsAudited(t *testing.T) {
	r := newRig(t).build()
	r.inst.fail["tool:pmtiles"] = errors.New("nope")
	ex := execOf(r)
	ex.Run(context.Background(), Step{ID: IDKiwix, Kind: KindTool, Target: kit.ToolKiwix, Item: "kit-kiwix-tools"}, audit.System(), "run-7")
	ex.Run(context.Background(), Step{ID: IDPmtiles, Kind: KindTool, Target: kit.ToolPmtiles}, audit.System(), "run-7")
	ex.Run(context.Background(), Step{ID: "tool.agent", Kind: KindTool, Target: "agent"}, audit.System(), "run-7")
	evs := r.audit.Events()
	if len(evs) != 3 {
		t.Fatalf("%d events", len(evs))
	}
	if e := evs[0]; e.Action != ActionStep || e.Result != audit.ResultOK || e.Target["step"] != IDKiwix || e.Target["run_id"] != "run-7" || e.Target["version"] != "3.7.0" || e.Target["item"] != "kit-kiwix-tools" || e.Actor.Kind != audit.KindSystem {
		t.Errorf("ok event %+v", e)
	}
	if e := evs[1]; e.Result != audit.ResultFail || e.Code != doctor.KIT047 {
		t.Errorf("failed event %+v", e)
	}
	if e := evs[2]; e.Result != audit.ResultFail || e.Target["step"] != "tool.agent" {
		t.Errorf("refused event %+v", e)
	}
	for _, e := range evs {
		for k, v := range e.Target {
			if strings.Contains(strings.ToLower(k+v), "token") {
				t.Errorf("secret-looking audit field %s=%s", k, v)
			}
		}
	}
}
