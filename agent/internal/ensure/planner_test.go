package ensure

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

func TestWantsFor(t *testing.T) {
	all := []string{"kit-catalog", "kit-kiwix-tools", "kit-pmtiles", "kit-piper", "kit-debs-resolute-amd64", "kit-whisper-cpp"}
	cases := []struct {
		name     string
		items    []string
		facts    Facts
		tools    map[string]bool
		packages bool
		whisper  bool
		backend  string
	}{
		{"everything in the profile", all, Facts{Codename: "resolute", Arch: "amd64", Backend: "cuda"},
			map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: true, kit.ToolPiper: true}, true, true, "cuda"},
		{"kiwix switched off in node.yaml", all, Facts{Codename: "resolute", Arch: "amd64", Backend: "cpu", KiwixOff: true},
			map[string]bool{kit.ToolKiwix: false, kit.ToolPmtiles: true, kit.ToolPiper: true}, true, true, "cpu"},
		{"piper and whisper switched off", all, Facts{Codename: "resolute", Arch: "amd64", PiperOff: true, WhisperOff: true},
			map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: true, kit.ToolPiper: false}, true, false, ""},
		{"the profile has no debs item for this Ubuntu", []string{"kit-kiwix-tools"}, Facts{Codename: "noble", Arch: "amd64"},
			map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: false, kit.ToolPiper: false}, false, false, ""},
		{"an unknown release wants no packages", all, Facts{},
			map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: true, kit.ToolPiper: true}, false, true, "cpu"},
		{"whisper backend auto follows llama", all, Facts{Codename: "resolute", Arch: "amd64", Backend: "vulkan", WhisperBackend: "auto"},
			map[string]bool{kit.ToolKiwix: true, kit.ToolPmtiles: true, kit.ToolPiper: true}, true, true, "vulkan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := WantsFor(tc.items, tc.facts)
			for tool, want := range tc.tools {
				if w.Tools[tool] != want {
					t.Errorf("tool %s wanted=%v, want %v", tool, w.Tools[tool], want)
				}
			}
			if (len(w.Packages) > 0) != tc.packages {
				t.Errorf("packages wanted=%v (%v)", len(w.Packages) > 0, w.Packages)
			}
			if w.Whisper != tc.whisper || (tc.whisper && w.WhisperWant.Backend != tc.backend) {
				t.Errorf("whisper %v/%q, want %v/%q", w.Whisper, w.WhisperWant.Backend, tc.whisper, tc.backend)
			}
		})
	}
	t.Run("the essential packages are the build tools and the backend's own", func(t *testing.T) {
		w := WantsFor(all, Facts{Codename: "resolute", Arch: "amd64", Backend: "cuda"})
		if !contains(w.Essential, "cmake") || !contains(w.Essential, "nvidia-cuda-toolkit") || contains(w.Essential, "openssh-server") {
			t.Errorf("essential %v", w.Essential)
		}
	})
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestPlanTools(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		offer     toolOffer
		absent    bool
		auto      Auto
		status    Status
		reason    string
		code      string
	}{
		{"missing and the kit has it", "", toolOffer{version: "3.7.0"}, false, AutoOn, StatusPending, "", ""},
		{"outdated: the kit has another version", "3.6.0", toolOffer{version: "3.7.0"}, false, AutoOn, StatusPending, "", ""},
		{"present at the kit's version", "3.7.0", toolOffer{version: "3.7.0"}, false, AutoOn, StatusOK, "", ""},
		{"missing and ensure.auto is ask", "", toolOffer{version: "3.7.0"}, false, AutoAsk, StatusWaiting, "", ""},
		{"missing and ensure.auto is off", "", toolOffer{version: "3.7.0"}, false, AutoOff, StatusWaiting, "", ""},
		{"missing and the kit item did not verify", "", toolOffer{err: ErrKitMissing}, false, AutoOn, StatusBlocked, ReasonKitMissing, doctor.KIT040},
		{"missing and the kit item is damaged", "", toolOffer{err: ErrKitDamaged}, false, AutoOn, StatusBlocked, ReasonKitDamaged, doctor.KIT001},
		{"missing and the kit has no item at all", "", toolOffer{}, true, AutoOn, StatusBlocked, ReasonKitMissing, doctor.KIT040},
		{"present and the kit item is missing: nothing to do", "3.6.0", toolOffer{err: ErrKitMissing}, false, AutoOn, StatusOK, "", ""},
		{"present and the kit item is damaged: the node is fine, READY says the kit is not", "3.6.0", toolOffer{err: ErrKitDamaged}, false, AutoOn, StatusOK, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.wants = Wants{Tools: map[string]bool{kit.ToolKiwix: true}}
			if tc.installed != "" {
				r.machine.tools[kit.ToolKiwix] = tc.installed
			}
			if tc.absent {
				delete(r.kit.tools, kit.ToolKiwix)
			} else {
				r.kit.tools[kit.ToolKiwix] = tc.offer
			}
			p, err := r.planner().Plan(context.Background(), PlanInput{Auto: tc.auto, Wants: r.wants})
			if err != nil {
				t.Fatal(err)
			}
			s := stepOf(t, p, IDKiwix)
			if s.Status != tc.status || s.Reason != tc.reason || s.Code != tc.code {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", s.Status, s.Reason, s.Code, tc.status, tc.reason, tc.code)
			}
			if tc.status == StatusPending && tc.installed != "" && s.From != tc.installed {
				t.Errorf("an update names the version it replaces, got %q", s.From)
			}
		})
	}
	t.Run("a tool the profile does not want has no step", func(t *testing.T) {
		r := newRig(t)
		r.wants = Wants{Tools: map[string]bool{kit.ToolKiwix: false, kit.ToolPmtiles: true}}
		p, _ := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants})
		if _, ok := p.Step(IDKiwix); ok || len(p.Steps) != 1 {
			t.Errorf("steps %v", ids(p))
		}
	})
	t.Run("an unexpected failure of the kit check is an error, not a step", func(t *testing.T) {
		r := newRig(t)
		r.kit.tools[kit.ToolKiwix] = toolOffer{err: errors.New("disk on fire")}
		if _, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants}); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestPlanPackages(t *testing.T) {
	cases := []struct {
		name    string
		missing []string
		pkgs    map[string]bool
		pkgErr  error
		status  Status
		reason  string
		listed  string
	}{
		{"nothing missing", nil, map[string]bool{"cmake": true}, nil, StatusOK, "", ""},
		{"missing ones the kit carries", []string{"cmake", "git"}, map[string]bool{"cmake": true, "git": true}, nil, StatusPending, "", "cmake,git"},
		{"only the part the kit carries is listed", []string{"cmake", "curl"}, map[string]bool{"cmake": true}, nil, StatusPending, "", "cmake"},
		{"an essential one the kit does not carry", []string{"cmake"}, map[string]bool{"git": true}, nil, StatusBlocked, ReasonNotInKit, "cmake"},
		{"an optional one the kit does not carry is not a problem", []string{"curl"}, map[string]bool{"cmake": true}, nil, StatusOK, "", ""},
		{"the kit has no verified repository and an essential one is missing", []string{"cmake"}, nil, ErrKitMissing, StatusBlocked, ReasonKitMissing, "cmake"},
		{"the kit repository is damaged", []string{"cmake"}, nil, ErrKitDamaged, StatusBlocked, ReasonKitDamaged, "cmake"},
		{"no kit and only optional ones missing", []string{"curl"}, nil, ErrKitMissing, StatusOK, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.wants = Wants{Packages: []string{"cmake", "git", "curl"}, Essential: []string{"cmake", "git"}}
			r.machine.missing = tc.missing
			r.kit.pkgs, r.kit.pkgErr = tc.pkgs, tc.pkgErr
			p, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants})
			if err != nil {
				t.Fatal(err)
			}
			s := stepOf(t, p, IDPackages)
			if s.Status != tc.status || s.Reason != tc.reason || strings.Join(s.Packages, ",") != tc.listed {
				t.Errorf("got %s/%s/%v, want %s/%s/%s", s.Status, s.Reason, s.Packages, tc.status, tc.reason, tc.listed)
			}
		})
	}
	t.Run("dpkg failing is an error", func(t *testing.T) {
		r := newRig(t)
		r.machine.pkgErr = errors.New("no dpkg-query")
		if _, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants}); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestPlanWhisper(t *testing.T) {
	cases := []struct {
		name      string
		installed bool
		prebuilt  string
		pre       error
		src       bool
		srcErr    error
		wantIDs   []string
		status    Status
	}{
		{"a build is in use", true, "", ErrKitMissing, false, nil, []string{IDWhisperRestore}, StatusOK},
		{"none in use and a ready-made one fits: restored by itself", false, "/kit/prebuilt/w.tar.zst", nil, false, nil, []string{IDWhisperRestore}, StatusPending},
		{"none in use, none fits, the source is there: a build needs a confirmation", false, "", ErrKitMissing, true, nil, []string{IDWhisperBuild}, StatusNeedsConfirm},
		{"none in use, none fits, no source", false, "", ErrKitMissing, false, nil, []string{IDWhisperBuild}, StatusBlocked},
		{"none in use, source damaged", false, "", ErrKitMissing, false, ErrKitDamaged, []string{IDWhisperBuild}, StatusBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.wants = Wants{Whisper: true}
			r.machine.whisper = tc.installed
			r.kit.prebuilt, r.kit.prebuiltErr, r.kit.src, r.kit.srcErr = tc.prebuilt, tc.pre, tc.src, tc.srcErr
			p, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Steps) != len(tc.wantIDs) || p.Steps[0].ID != tc.wantIDs[0] || p.Steps[0].Status != tc.status {
				t.Fatalf("got %v, want %v/%s", ids(p), tc.wantIDs, tc.status)
			}
			if tc.status == StatusPending && p.Steps[0].File != tc.prebuilt {
				t.Errorf("file %q", p.Steps[0].File)
			}
			if tc.status == StatusNeedsConfirm && !p.Steps[0].NeedsConfirmation {
				t.Error("a build must say it needs a confirmation")
			}
		})
	}
	t.Run("compiling is needs_confirm even when auto is on, and also when it is ask", func(t *testing.T) {
		for _, auto := range []Auto{AutoOn, AutoAsk, AutoOff} {
			r := newRig(t)
			r.wants = Wants{Whisper: true}
			r.kit.prebuiltErr, r.kit.src = ErrKitMissing, true
			p, _ := r.planner().Plan(context.Background(), PlanInput{Auto: auto, Wants: r.wants})
			if s := stepOf(t, p, IDWhisperBuild); s.Status != StatusNeedsConfirm {
				t.Errorf("auto=%s: %s", auto, s.Status)
			}
		}
	})
}

func TestPlanRestartOfKiwix(t *testing.T) {
	cases := []struct {
		name    string
		pending []string
		active  bool
		want    bool
	}{
		{"kiwix was replaced and is running: a restart needs a confirmation", []string{"kiwix"}, true, true},
		{"kiwix was replaced and is not running: nothing to restart", []string{"kiwix"}, false, false},
		{"nothing pending", nil, true, false},
		{"another service is never planned", []string{"llama"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.wants = Wants{}
			r.machine.active["kiwix"] = tc.active
			p, _ := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants, PendingRestart: tc.pending})
			s, ok := p.Step(IDRestartKiwix)
			if ok != tc.want || (ok && (s.Status != StatusNeedsConfirm || !s.NeedsConfirmation)) {
				t.Errorf("steps %v", ids(p))
			}
		})
	}
}

func TestPlanOfAWholeNode(t *testing.T) {
	r := newRig(t)
	p, err := r.planner().Plan(context.Background(), PlanInput{Auto: AutoOn, Wants: r.wants})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ids(p), " ")
	want := "apt.packages=pending tool.kiwix-tools=pending tool.pmtiles=pending tool.piper=pending whisper.restore=pending"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if p.Work() != 5 {
		t.Errorf("work %d", p.Work())
	}
}
