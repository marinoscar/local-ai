package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// This file is what happens after a build is smoke tested, for a fresh build and for a prebuilt one
// alike: put it in place, activate it (the post-switch check and rollback are switch.go, ROUTER-08),
// apply the retention, and record what changed (rebuild-kit.md section 4.3.1 steps 4 and 5). It also
// holds the restore of an exported build (--prebuilt, rebuild-kit.md section 4.2), which re-enters
// the same tail.

// NodeRecorder records node.yaml's llama.commit after a switch to another commit
// (rebuild-kit.md section 4.3.1 step 5). AICTL-09 implements it over the node configuration.
type NodeRecorder interface {
	RecordLlamaCommit(commit string) error
}

// PrebuiltExporter exports an activated build into kit/prebuilt on every writable root
// (rebuild-kit.md section 4.2). KIT-05 implements it (`aictl kit export-build`).
type PrebuiltExporter interface {
	// ExportBuild is called with the build id and the directory of the build that was just
	// activated.
	ExportBuild(ctx context.Context, buildID, buildDir string) error
}

// completeBuild is the tail of Build and of a prebuilt restore: the build in r.plan.OutDir is
// smoke tested and has its BUILD.json (info). It is renamed into place, activated unless
// Request.NoActivate, the retention runs, and, after a switch, node.yaml's commit is recorded and
// the prebuilt exported (each a warning when it fails: the build is already in use).
func (m *Manager) completeBuild(ctx context.Context, r *run, info BuildInfo, curTarget string, progress func(Stage, string)) (Result, error) {
	in, plan, req := r.in, r.plan, r.req
	r.closeLog()
	oldDir, e := r.place()
	if e != nil {
		return Result{}, r.abort(ctx, e)
	}

	res := Result{BuildID: plan.BuildID, BuildJSON: info, Path: plan.FinalDir, LogPath: filepath.Join(plan.FinalDir, buildLog),
		Warnings: append([]string(nil), r.warnings...)}
	protect := []string{plan.BuildID}
	if in.Activate {
		progress(StageSwitch, "Switching to the new build")
		ar, e := m.activate(ctx, activation{
			req: req, plan: plan, curTarget: curTarget, oldDir: oldDir, info: info,
		}, progress)
		res.Warnings = append(res.Warnings, ar.warnings...)
		if e != nil {
			return Result{}, e
		}
		res.Switched, res.PreviousBuild, res.DefaultModel = true, ar.previous, ar.defaultModel
		// current is the new build and the build it replaced is the previous one; the previous_build
		// of the state is not recorded yet, so it must not widen the keep set.
		protect = append(protect, buildName(curTarget), ar.previous)
		res.Warnings = append(res.Warnings, m.recordActivated(ctx, plan)...)
	} else {
		protect = append(protect, buildName(curTarget))
		if m.opt.PreviousBuild != nil {
			protect = append(protect, m.opt.PreviousBuild())
		}
	}

	progress(StageRetention, "Removing builds and sources that are no longer needed")
	removed, warns := m.retain(in.Keep, plan, protect)
	res.Removed = removed
	res.Warnings = append(res.Warnings, warns...)
	return res, nil
}

// recordActivated writes node.yaml's llama.commit when the activated build is of another commit and
// hands the build to the prebuilt exporter. It returns the warnings of what failed.
func (m *Manager) recordActivated(ctx context.Context, plan Plan) []string {
	var warns []string
	if m.opt.NodeConfig != nil {
		cur := ""
		if m.opt.Node != nil {
			cur = strings.ToLower(strings.TrimSpace(m.opt.Node().Commit))
		}
		if cur == "" || !strings.HasPrefix(plan.Commit, cur) {
			if err := m.opt.NodeConfig.RecordLlamaCommit(plan.Commit); err != nil {
				warns = append(warns, "node.yaml could not be updated with the new llama.commit: "+err.Error())
			}
		}
	}
	if m.opt.Exporter != nil {
		if err := m.opt.Exporter.ExportBuild(ctx, plan.BuildID, plan.FinalDir); err != nil {
			warns = append(warns, "The new build could not be exported to the kit: "+err.Error())
		}
	}
	return warns
}

// ---- --prebuilt -------------------------------------------------------------------------

// restorePrebuilt restores an exported build (Request.Prebuilt): the archive is unpacked into a
// scratch directory with the shared extraction, BUILD.json and the sha256 of every program are
// verified (a failure is LAI-KIT-001 and nothing is put in place), then the build goes through the
// same relocatable check, smoke test, placement, activation and retention as a fresh one. With
// Request.DryRun it stops after the verification.
func (m *Manager) restorePrebuilt(ctx context.Context, req Request, progress func(Stage, string)) (Result, error) {
	fsys := m.opt.FS
	builds := filepath.Join(m.root, buildsDir)
	fail := func(code, msg, cause string, err error, kv ...any) (Result, error) {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, newErr(code, StageSource, msg, err, append([]any{"cause", cause, "prebuilt", req.Prebuilt}, kv...)...)
	}
	if err := fsys.MkdirAll(builds, 0o755); err != nil {
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The builds folder could not be created.", err, "cause", "filesystem")
	}
	release, err := lockFile(filepath.Join(m.root, lockName))
	if err != nil {
		if errors.Is(err, errLockHeld) {
			return Result{}, newErr(CodeBuildFailed, StagePreflight, "Another llama.cpp build is already running on this computer.", err, "cause", "locked")
		}
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The build lock could not be taken.", err, "cause", "lock")
	}
	defer release()

	progress(StageSource, "Checking the prebuilt build "+req.Prebuilt)
	staging := filepath.Join(builds, ".prebuilt"+suffixTmp)
	_ = fsys.RemoveAll(staging)
	defer func() { _ = fsys.RemoveAll(staging) }() // renamed away on success: then this is a no-op
	if err := fsys.MkdirAll(staging, 0o755); err != nil {
		return fail(CodeBuildFailed, "The scratch folder for the prebuilt build could not be created.", "filesystem", err)
	}
	f, err := fsys.Open(req.Prebuilt)
	if err != nil {
		return fail(CodeBuildFailed, "The prebuilt file could not be opened: "+err.Error(), "prebuilt_unreadable", err)
	}
	h := sha256.New()
	xerr := extractTar(fsys, io.TeeReader(f, h), staging, 0)
	if xerr == nil {
		_, xerr = io.Copy(io.Discard, io.TeeReader(f, h))
	}
	_ = f.Close()
	if xerr != nil {
		return fail(CodeSourceDamaged, "The prebuilt build is damaged or not safe to unpack: "+xerr.Error(), "prebuilt_damaged", xerr)
	}
	if want := strings.ToLower(req.PrebuiltSHA256); want != "" && hex.EncodeToString(h.Sum(nil)) != want {
		return fail(CodeSourceDamaged, "The prebuilt file does not match the sha256 the kit recorded for it. Run `aictl kit verify --full`.", "prebuilt_digest", nil)
	}
	info, e := m.verifyPrebuilt(staging, m.opt.Recipe.Programs)
	if e != nil {
		return Result{}, e
	}
	if b := strings.TrimSpace(req.Backend); b != "" && b != info.Backend {
		return fail(CodeBuildFailed, fmt.Sprintf("The prebuilt build is for %s, not %s.", info.Backend, b), "prebuilt_mismatch", nil)
	}
	if c := strings.ToLower(strings.TrimSpace(req.Commit)); c != "" && !strings.HasPrefix(info.Commit, c) {
		return fail(CodeBuildFailed, "The prebuilt build is of another llama.cpp commit than "+c+".", "prebuilt_mismatch", nil)
	}

	in := Inputs{
		Commit: info.Commit, ShortCommit: info.Commit[:9], Backend: info.Backend, CudaArch: info.CudaArch,
		Activate: !req.NoActivate, Keep: req.Keep,
	}
	if in.Keep <= 0 {
		in.Keep = DefaultKeep
	}
	plan := NewPlan(m.root, in, info.Commit)
	if req.DryRun {
		return Result{BuildID: plan.BuildID, BuildJSON: info, DryRun: true, Restored: true}, nil
	}
	curTarget, err := m.currentTarget()
	if err != nil {
		return Result{}, newErr(CodeBuildFailed, StagePreflight, "The current link of the llama.cpp builds cannot be read.", err, "cause", "current_link")
	}
	if !in.Activate && curTarget != "" && buildName(curTarget) == plan.BuildID {
		return Result{}, newErr(CodeBuildFailed, StagePreflight,
			"Build "+plan.BuildID+" is the one in use now; restoring it without switching would replace it silently.",
			nil, "cause", "active_build", "build_id", plan.BuildID)
	}

	r := &run{m: m, req: req, in: in, plan: plan}
	if err := fsys.RemoveAll(plan.OutDir); err != nil {
		return fail(CodeBuildFailed, "The build folder could not be prepared.", "filesystem", err, "build_id", plan.BuildID)
	}
	if err := fsys.Rename(staging, plan.OutDir); err != nil {
		return fail(CodeBuildFailed, "The prebuilt build could not be moved into place.", "filesystem", err, "build_id", plan.BuildID)
	}
	w, err := fsys.Create(filepath.Join(plan.OutDir, buildLog), 0o644)
	if err != nil {
		_ = fsys.RemoveAll(plan.OutDir)
		return fail(CodeBuildFailed, "The build log could not be created.", "filesystem", err, "build_id", plan.BuildID)
	}
	r.logW, r.logOpen = w, true
	r.logf("restored from the prebuilt build %s\n", req.Prebuilt)

	if e := r.checkRunpath(); e != nil {
		return Result{}, r.abort(ctx, e)
	}
	progress(StageSmoke, "Testing the prebuilt build")
	if _, e := r.smoke(ctx); e != nil {
		return Result{}, r.abort(ctx, e)
	}
	res, err := m.completeBuild(ctx, r, info, curTarget, progress)
	res.Restored = true
	return res, err
}

var buildIDRe = fullSHARe // a prebuilt names a full commit

// verifyPrebuilt reads and validates the BUILD.json of an unpacked prebuilt and checks every
// program against the sha256 it records. Every failure is LAI-KIT-001: the file is not the one that
// was exported.
func (m *Manager) verifyPrebuilt(dir string, programs []string) (BuildInfo, *Error) {
	fsys := m.opt.FS
	bad := func(msg, cause string, kv ...any) (BuildInfo, *Error) {
		return BuildInfo{}, newErr(CodeSourceDamaged, StageSource, "The prebuilt build is damaged: "+msg+". Run `aictl kit verify --full`.", nil,
			append([]any{"cause", cause}, kv...)...)
	}
	jsonPath := filepath.Join(dir, buildJSON)
	if fi, err := fsys.Lstat(jsonPath); err != nil || !fi.Mode().IsRegular() {
		return bad("it has no BUILD.json", "prebuilt_no_build_json")
	}
	raw, err := fsys.ReadFile(jsonPath)
	if err != nil {
		return bad("BUILD.json cannot be read", "prebuilt_no_build_json")
	}
	var info BuildInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return bad("BUILD.json is not valid", "prebuilt_bad_build_json")
	}
	switch info.Backend {
	case BackendCUDA, BackendVulkan, BackendCPU:
	default:
		return bad("BUILD.json names the backend "+fmt.Sprintf("%q", info.Backend), "prebuilt_bad_build_json")
	}
	if !buildIDRe.MatchString(info.Commit) || info.BuildID != BuildIDOf(info.Commit, info.Backend) {
		return bad("BUILD.json has a build id that does not match its commit and backend", "prebuilt_bad_build_json")
	}
	if info.Backend == BackendCUDA && !cudaArchRe.MatchString(info.CudaArch) {
		return bad("BUILD.json has no valid CUDA architecture", "prebuilt_bad_build_json")
	}
	for _, name := range programs {
		want := strings.ToLower(info.SHA256[name])
		if !manifest.IsSHA256(want) {
			return bad("BUILD.json records no sha256 for "+name, "prebuilt_no_sha", "file", name)
		}
		p := filepath.Join(dir, "bin", name)
		if fi, err := fsys.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			return bad(name+" is missing", "prebuilt_missing_file", "file", name)
		}
		f, err := fsys.Open(p)
		if err != nil {
			return bad(name+" cannot be read", "prebuilt_missing_file", "file", name)
		}
		h := sha256.New()
		_, cerr := io.Copy(h, f)
		_ = f.Close()
		if cerr != nil {
			return bad(name+" cannot be read", "prebuilt_missing_file", "file", name)
		}
		if hex.EncodeToString(h.Sum(nil)) != want {
			return bad(name+" does not match the sha256 in BUILD.json", "prebuilt_tampered", "file", name)
		}
	}
	return info, nil
}
