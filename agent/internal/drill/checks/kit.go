package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// The kit checks of the drill (drill.md section 4.2 checks 17 and 18, rebuild-kit.md section 4.4):
// whether this computer's kit is complete and whether llama.cpp and whisper.cpp could be rebuilt
// offline. Neither changes anything: the verifier is read-only and the preflights only look.

// KitSource is the kit verifier as the drill sees it; the daemon implements it over kitready.Source.
type KitSource interface {
	// Roots lists the mounted content roots: the internal root and the ark drives.
	Roots() []kit.StatusRoot
	// Verify runs rebuild-kit.md section 4.4 steps 1 to 5 on one root: nothing is re-hashed (check 4
	// of the drill is the hashing) and the build dry run is left to check 18.
	Verify(ctx context.Context, rootID string) (kit.VerifyReport, error)
}

// KitPresentTimeout bounds check 17: a quick verification reads every manifest of the kit and stats
// its files, which on a drive with a big kit takes longer than the 30 s of a plain check.
const KitPresentTimeout = 60 * time.Second

// KitPresent is check 17, drl.kit.present: the kit verification of rebuild-kit.md section 4.4,
// steps 1 to 5, on the internal root and on every mounted drive.
//
//   - fail (LAI-DRL-070): this computer's own parts are missing or inconsistent on the internal root
//     (LAI-KIT-040 and 042), a kit file is damaged or the signature of the agent binaries does not
//     verify (LAI-KIT-001, 044), or the internal root has no kit at all;
//   - warn: a drive is missing parts of other computers of the lab (LAI-KIT-041), a drive's kit has
//     any other problem (the finding's own code), or the internal root's CUDA toolkit is newer than the
//     driver (LAI-KIT-043);
//   - pass: everything is in place. A missing prebuilt build is optional and never a warning here.
//
// No drive mounted is not a problem: the lab-level part is skipped. A drive that holds no kit folder
// is skipped too, and a note says so.
func KitPresent(src KitSource) drill.Check {
	return drill.Check{
		ID: "drl.kit.present", Area: "kit", Critical: true, Order: 17, Timeout: KitPresentTimeout,
		FailCode: doctor.DRL070,
		Run:      func(ctx context.Context, _ *drill.Env) drill.Outcome { return kitPresent(ctx, src) },
	}
}

func kitPresent(ctx context.Context, src KitSource) drill.Outcome {
	roots := src.Roots()
	details := map[string]any{"roots_checked": 0}
	var internal *kit.StatusRoot
	for i := range roots {
		if roots[i].Internal {
			internal = &roots[i]
			break
		}
	}
	if internal == nil {
		details["reason"] = "no_internal_root"
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL070, Details: details}
	}

	var (
		failed, warned bool
		warnCode       string
		missing        []string
		codes          []string
		skipped        []string
		overall        = kit.Green
		checked        int
	)
	addCode := func(c string) {
		for _, x := range codes {
			if x == c {
				return
			}
		}
		codes = append(codes, c)
	}
	warn := func(code string) {
		warned = true
		if warnCode == "" {
			warnCode = code
		}
	}

	for _, r := range roots {
		rep, err := src.Verify(ctx, r.ID)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			details["reason"] = "interrupted"
			return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL070, Details: details}
		case errors.Is(err, kit.ErrNoKit) && !r.Internal:
			skipped = append(skipped, r.ID+": no kit folder")
			continue
		case errors.Is(err, kit.ErrNoKit):
			failed = true
			details["reason"] = "no_kit_on_internal_root"
			addCode(kit.CodeKitMissingNode)
			continue
		default:
			if r.Internal {
				failed = true
				details["reason"] = "cannot_verify"
				details["error"] = clipText(err.Error(), 300)
			} else {
				skipped = append(skipped, r.ID+": "+clipText(err.Error(), 100))
			}
			continue
		}
		checked++
		overall = worseColour(overall, rep.Overall)
		missing = append(missing, partProblems(rep, r.Internal)...)
		if r.Internal && rep.Node == nil {
			details["node_facts"] = "unknown"
		}

		node, lab, damaged := rep.NodeProblems(), rep.LabProblems(), rep.Damaged()
		for _, f := range rep.Findings {
			if !f.Optional {
				addCode(f.Code)
			}
		}
		if r.Internal {
			if len(node) > 0 || damaged {
				failed = true
				continue
			}
			if rep.HasCode(kit.CodeKitCudaTooNew) {
				warn(kit.CodeKitCudaTooNew)
			}
			continue
		}
		// A drive: the lab's gaps are the warning of the spec; its other problems are warnings too, with
		// their own code, since they are the drive's and not this computer's.
		switch {
		case len(lab) > 0:
			warn(kit.CodeKitMissingLab)
		case len(node) > 0:
			warn(node[0].Code)
		case damaged:
			warn(damagedCode(rep))
		case rep.HasCode(kit.CodeKitCudaTooNew):
			warn(kit.CodeKitCudaTooNew)
		}
	}

	details["roots_checked"] = checked
	details["kit_overall"] = overall
	if len(missing) > 0 {
		details["missing"] = clipText(strings.Join(missing, "; "), 450)
	}
	if len(codes) > 0 {
		details["codes"] = strings.Join(codes, ", ")
	}
	if len(skipped) > 0 {
		details["skipped"] = clipText(strings.Join(skipped, "; "), 300)
	}
	switch {
	case failed:
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL070, Details: details}
	case warned:
		return drill.Outcome{Result: drill.ResultWarn, Code: warnCode, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

func damagedCode(rep kit.VerifyReport) string {
	if rep.HasCode(kit.CodeKitBadSignature) {
		return kit.CodeKitBadSignature
	}
	return kit.CodeKitDamagedFile
}

// partProblems lists the parts of a report that are not ok as "part (CODE)", or "part (status)" when
// the part has no code of its own (a missing prebuilt is a warning of the part, not a finding of a
// colour). A drive's parts are prefixed with its id.
func partProblems(rep kit.VerifyReport, internal bool) []string {
	var out []string
	for _, p := range rep.Parts {
		if p.Status == kit.StatusOK {
			continue
		}
		why := p.Code
		if why == "" {
			why = p.Status
		}
		name := p.Part
		if !internal {
			name = rep.Root + "/" + name
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, why))
	}
	return out
}

func worseColour(a, b string) string {
	rank := map[string]int{kit.Green: 0, kit.Yellow: 1, kit.Red: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Preflight runs the offline preflight of one program's rebuild: what `aictl rebuild-llama --dry-run`
// or `aictl rebuild-whisper --dry-run` runs, in this process, without a subprocess of aictl. It never
// compiles, never writes and never uses the network (the request is offline and from the kit).
type Preflight func(ctx context.Context) build.Report

// BuildDeps are the two preflights of check 18. Whisper may be nil on a node without the speech
// service: it is then not part of the check.
type BuildDeps struct {
	Llama   Preflight
	Whisper Preflight
}

// KitRebuildDryRunTimeout bounds check 18: every preflight check runs a read-only program
// (cmake --version, apt-get -s install, nvidia-smi) that may take up to 30 s on its own.
const KitRebuildDryRunTimeout = 120 * time.Second

// KitRebuildDryRun is check 18, drl.kit.rebuild_dry_run: the dry run of rebuild-llama and of
// rebuild-whisper passes (rebuild-kit.md section 4.3.1 step 1, offline, from the kit). A failed
// preflight fails the check with LAI-DRL-071 and details.llama_failed (or whisper_failed) lists each
// failed preflight check as "LAI-KIT-0nn <what was found>".
func KitRebuildDryRun(d BuildDeps) drill.Check {
	return drill.Check{
		ID: "drl.kit.rebuild_dry_run", Area: "kit", Critical: true, Order: 18, Timeout: KitRebuildDryRunTimeout,
		FailCode: doctor.DRL071,
		Run:      func(ctx context.Context, _ *drill.Env) drill.Outcome { return d.rebuildDryRun(ctx) },
	}
}

func (d BuildDeps) rebuildDryRun(ctx context.Context) drill.Outcome {
	details := map[string]any{}
	ok := true
	for _, p := range []struct {
		name string
		run  Preflight
	}{{"llama", d.Llama}, {"whisper", d.Whisper}} {
		if p.run == nil {
			details[p.name+"_skipped"] = "not configured on this computer"
			continue
		}
		rep := p.run(ctx)
		good := rep.OK()
		details[p.name+"_ok"] = good
		if !good {
			ok = false
			details[p.name+"_failed"] = failedChecks(rep)
		}
	}
	if !ok {
		return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL071, Details: details}
	}
	return drill.Outcome{Result: drill.ResultPass, Details: details}
}

// failedChecks words the failed checks of a preflight: "LAI-KIT-010 cmake is not installed; ...".
func failedChecks(rep build.Report) string {
	var parts []string
	for _, c := range rep.Failed() {
		s := strings.TrimSpace(c.Code + " " + c.Detail)
		if s == "" {
			s = c.ID
		}
		parts = append(parts, clipText(s, 140))
	}
	return clipText(strings.Join(parts, "; "), 450)
}

// clipText cuts s to n bytes (the cloud accepts detail strings of up to 500 characters).
func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// RegisterKit adds the kit, rebuild dry-run, power and clock checks (17, 18, 20 and 21) to a registry.
// The kit source and the build preflights are required; the power check needs Hardening to do more
// than skip, and the clock check passes with a note when it has no content index to compare with.
func RegisterKit(reg *drill.Registry, k KitSource, b BuildDeps, p PowerDeps, newest NewestVerified) error {
	if k == nil || b.Llama == nil {
		return errors.New("drill checks: the kit checks need the kit verifier and the llama.cpp preflight")
	}
	for _, c := range []drill.Check{KitPresent(k), KitRebuildDryRun(b), Power(p), Clock(newest)} {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}
