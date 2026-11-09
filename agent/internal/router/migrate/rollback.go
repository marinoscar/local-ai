package migrate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// rollback is R1 to R6 (llama-router.md section 4.9.3). It is safe to run any time after the migration
// started, and every step is idempotent and journaled, so a rollback that is killed continues when the
// migration is run again. auto is true when a failed verification started it.
func (e *Engine) rollback(ctx context.Context, p *Plan, cause string, restoreFiles, auto bool) (*Result, error) {
	pr := e.prog
	if pr.Rollback == "" || pr.Rollback == "R6" {
		pr.Rollback, pr.Cause, pr.RestoreFiles = "R0", cause, restoreFiles
		if err := e.saveProgress(pr); err != nil {
			return nil, err
		}
		e.auditLine("migration.llama_qwen.rollback", audit.ResultOK, "", map[string]string{"cause": clip(cause, 120)})
	} else {
		restoreFiles = pr.RestoreFiles
	}
	steps := map[string]func(context.Context, *Plan, bool) error{
		"R1": e.r1StopNew, "R2": e.r2RestoreUnit, "R3": e.r3RestoreKey, "R4": e.r4Files, "R5": e.r5StartOld, "R6": e.r6Record,
	}
	for _, id := range rollbackSteps[stepIndex(rollbackSteps, e.prog.Rollback)+1:] {
		e.say("%s %s ...", id, rollbackTitle[id])
		if err := steps[id](ctx, p, restoreFiles); err != nil {
			var me *Error
			if !errors.As(err, &me) {
				me = wrap(CodeInterrupted, id, err, "rollback step %s failed", id)
			}
			me.Step = id
			if me.Code == "" {
				me.Code = CodeInterrupted
			}
			e.auditLine("migration.llama_qwen.rollback", audit.ResultFail, me.Code, map[string]string{"step": id})
			return nil, me
		}
		next := e.prog
		next.Rollback = id
		if err := e.saveProgress(next); err != nil {
			return nil, err
		}
	}
	e.auditLine("migration.llama_qwen.rolled_back", audit.ResultOK, "", map[string]string{"cause": clip(cause, 120)})
	res := &Result{State: StateRolledBack, ModelID: p.ModelID, KeyFingerprint: p.KeyFingerprint, ClientsToUpdate: []string{}, ServeUnchanged: true}
	e.say("Rolled back: %s serves again on port %d with the same key. Tailscale Serve was never changed.", e.legacy(), LegacyPort)
	return res, nil
}

// finishRollback continues a rollback that was interrupted.
func (e *Engine) finishRollback(ctx context.Context, p *Plan) (*Result, error) {
	return e.rollback(ctx, p, orStr(e.prog.Cause, "interrupted"), e.prog.RestoreFiles, false)
}

var rollbackTitle = map[string]string{
	"R1": "stop and disable the new service", "R2": "put the old unit back", "R3": "restore the key file",
	"R4": "model files", "R5": "start the old service", "R6": "record",
}

// ignorable is true for the error of a unit that systemd does not know ("not loaded", "does not exist"):
// there is nothing to stop or disable then.
func ignorable(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not loaded") || strings.Contains(s, "does not exist") || strings.Contains(s, "not found") ||
		strings.Contains(s, "no such file")
}

// R1: systemctl stop localai-llama; systemctl disable localai-llama.
func (e *Engine) r1StopNew(ctx context.Context, _ *Plan, _ bool) error {
	if err := e.sd.Stop(ctx, NewUnitName); err != nil && !ignorable(err) {
		return wrap(CodeInterrupted, "R1", err, "cannot stop %s", NewUnitName)
	}
	if err := e.sd.Disable(ctx, NewUnitName); err != nil && !ignorable(err) {
		return wrap(CodeInterrupted, "R1", err, "cannot disable %s", NewUnitName)
	}
	return nil
}

// R2: unmask the old unit (and the other legacy units M9 masked), put their files (and drop-ins) back
// from the backup when they are missing, daemon-reload.
func (e *Engine) r2RestoreUnit(ctx context.Context, p *Plan, _ bool) error {
	for _, u := range p.retiredUnits() {
		if err := e.restoreUnit(ctx, u); err != nil {
			return err
		}
	}
	if err := e.sd.DaemonReload(ctx); err != nil {
		return wrap(CodeInterrupted, "R2", err, "daemon-reload failed")
	}
	return nil
}

// restoreUnit unmasks one legacy unit and puts its file back from the backup when it is missing.
func (e *Engine) restoreUnit(ctx context.Context, name string) error {
	st, err := e.sd.State(ctx, name)
	if err != nil {
		return wrap(CodeInterrupted, "R2", err, "cannot read the state of %s", name)
	}
	if strings.HasPrefix(st.UnitFileState, "masked") {
		if err := e.sd.Unmask(ctx, name); err != nil {
			return wrap(CodeInterrupted, "R2", err, "cannot unmask %s", name)
		}
	}
	unit, backup := e.paths.UnitFile(name), e.paths.UnitBackupOf(name)
	if !exists(e.fs, unit) {
		data, err := e.fs.ReadFile(backup)
		if err != nil {
			return wrap(CodeInterrupted, "R2", err, "%s is missing and so is its backup %s: restore it by hand", unit, backup)
		}
		if err := config.WriteFileAtomicFS(e.fs, unit, data, 0o644, e.rootUID, e.rootGID); err != nil {
			return wrap(CodeInterrupted, "R2", err, "cannot restore %s", unit)
		}
	}
	return e.restoreDropIns(name)
}

// restoreDropIns copies the saved drop-in files back when the directory is gone.
func (e *Engine) restoreDropIns(name string) error {
	entries, err := e.fs.ReadDir(e.paths.DropInBackupOf(name))
	if err != nil {
		if isNotExist(err) {
			return nil // there were none
		}
		return wrap(CodeInterrupted, "R2", err, "cannot read %s", e.paths.DropInBackupOf(name))
	}
	dir := e.paths.DropInDir(name)
	if err := e.fs.MkdirAll(dir, 0o755); err != nil {
		return wrap(CodeInterrupted, "R2", err, "cannot create %s", dir)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		dst := filepath.Join(dir, ent.Name())
		if exists(e.fs, dst) {
			continue
		}
		data, err := e.fs.ReadFile(filepath.Join(e.paths.DropInBackupOf(name), ent.Name()))
		if err != nil {
			return wrap(CodeInterrupted, "R2", err, "cannot read the saved drop-in %s", ent.Name())
		}
		if err := config.WriteFileAtomicFS(e.fs, dst, data, 0o644, e.rootUID, e.rootGID); err != nil {
			return wrap(CodeInterrupted, "R2", err, "cannot restore the drop-in %s", dst)
		}
	}
	return nil
}

// R3: when the old key path is a link (or gone), make it a regular file again holding the CURRENT key:
// if the key was rotated after the migration, the old service must use the current one.
func (e *Engine) r3RestoreKey(_ context.Context, _ *Plan, _ bool) error {
	fi, err := e.fs.Lstat(e.paths.LegacyKey)
	if err == nil && !isLink(fi) {
		return nil // a regular file: the key the old service always used
	}
	if err != nil && !isNotExist(err) {
		return wrap(CodeInterrupted, "R3", err, "cannot look at %s", e.paths.LegacyKey)
	}
	key, kerr := e.readKey(e.paths.NewKey)
	if kerr != nil {
		return fail(CodeCredShape, "R3", "the current key %s cannot be read or is not 64 hex digits: the old key path cannot be restored", e.paths.NewKey)
	}
	if err := e.fs.MkdirAll(e.paths.LegacyKeyDir(), 0o700); err != nil {
		return wrap(CodeInterrupted, "R3", err, "cannot create %s", e.paths.LegacyKeyDir())
	}
	if err := config.WriteFileAtomicFS(e.fs, e.paths.LegacyKey, key, 0o600, e.rootUID, e.rootGID); err != nil {
		return wrap(CodeInterrupted, "R3", err, "cannot write %s", e.paths.LegacyKey)
	}
	return nil
}

// R4: the symbolic links keep the old -m paths valid, so nothing has to move. With --restore-files the
// files go back (same filesystem only), owned by the old unit's user.
func (e *Engine) r4Files(ctx context.Context, p *Plan, restore bool) error {
	if !restore && e.oldServiceCannotRead(ctx, p) {
		// The links keep the old -m valid only when the old user can follow them. When it cannot, starting the
		// old service would fail (the 5 minute outage of the first run on ai-n2, #158): put the files back.
		e.warnf("%s cannot read the models at their new place, so they go back to their old paths; otherwise %s would not start", p.User, e.legacy())
		restore = true
	}
	if !restore {
		return nil
	}
	restored, total := 0, 0
	for _, f := range p.Files {
		total++
		ok, err := e.restoreFile(p, f)
		if err != nil {
			return err
		}
		if ok {
			restored++
		}
	}
	if total > 0 && restored == total {
		// The files are not under /srv/localai any more: a manifest that lists them would lie.
		mf := manifest.Path(e.paths.ModelDir(p.ModelID))
		if m, err := manifest.Read(e.fs, mf); err == nil && m.Source.Type == adoptedSource {
			if err := e.removeFile(mf); err != nil {
				return wrap(CodeInterrupted, "R4", err, "cannot remove %s", mf)
			}
		}
	}
	return nil
}

// oldServiceCannotRead is true when a model that was moved (its old path is a link to the new one) cannot
// be read by the old unit's user.
func (e *Engine) oldServiceCannotRead(ctx context.Context, p *Plan) bool {
	user := e.legacyUserToCheck(p)
	if user == "" {
		return false
	}
	for _, f := range p.Files {
		fi, err := e.fs.Lstat(f.Old)
		if err != nil || !isLink(fi) {
			continue
		}
		if t, err := e.fs.Readlink(f.Old); err != nil || t != f.New {
			continue
		}
		if !e.canRead(ctx, user, f.Old) {
			return true
		}
	}
	return false
}

// restoreFile moves one file back to its old path; ok is false when it is left where it is.
func (e *Engine) restoreFile(p *Plan, f PlanFile) (bool, error) {
	fi, err := e.fs.Lstat(f.Old)
	if err != nil || !isLink(fi) {
		return err == nil, nil // already a real file there (or gone: nothing to restore)
	}
	if t, err := e.fs.Readlink(f.Old); err != nil || t != f.New {
		return false, nil
	}
	a, err1 := e.fs.Dev(f.New)
	b, err2 := e.fs.Dev(path.Dir(f.Old))
	if err1 != nil || err2 != nil || a != b {
		e.warnf("%s is on another filesystem than %s: it stays where it is, and %s keeps pointing at it", f.Name, path.Dir(f.Old), f.Old)
		return false, nil
	}
	if err := e.fs.Rename(f.New, f.Old); err != nil {
		return false, wrap(CodeInterrupted, "R4", err, "cannot move %s back to %s", f.New, f.Old)
	}
	if uid, gid, ok := e.unitOwner(p); ok {
		if err := e.fs.Chown(f.Old, uid, gid); err != nil {
			e.warnf("cannot give %s back to %s: %v", f.Old, p.User, err)
		}
	}
	return true, nil
}

// unitOwner resolves the old unit's User and Group to ids.
func (e *Engine) unitOwner(p *Plan) (uid, gid int, ok bool) {
	if p.User == "" {
		return 0, 0, false
	}
	uid, gid, err := e.users.LookupUser(p.User)
	if err != nil {
		return 0, 0, false
	}
	if p.Group != "" {
		if g, err := e.users.LookupGroup(p.Group); err == nil {
			gid = g
		}
	}
	return uid, gid, true
}

// R5: enable and start the old service, and see it answer: /health 200, /v1/models 200 with the key and
// 401 without. The other legacy units are enabled again only when they were before (never started: the
// old service holds the port).
func (e *Engine) r5StartOld(ctx context.Context, p *Plan, _ bool) error {
	for _, o := range p.OtherUnits {
		if !o.Enabled {
			continue
		}
		if err := e.sd.Enable(ctx, o.Unit); err != nil {
			return wrap(CodeInterrupted, "R5", err, "cannot enable %s", o.Unit)
		}
	}
	if err := e.sd.Enable(ctx, e.legacy()); err != nil {
		return wrap(CodeInterrupted, "R5", err, "cannot enable %s", e.legacy())
	}
	if err := e.sd.Start(ctx, e.legacy()); err != nil {
		return wrap(CodeInterrupted, "R5", err, "cannot start %s", e.legacy())
	}
	return e.waitOldAnswers(ctx, e.timing.RollbackWait)
}

// waitOldAnswers waits for the old service to serve its contract.
func (e *Engine) waitOldAnswers(ctx context.Context, max time.Duration) error {
	var last error
	timedOut, err := e.until(ctx, max, func(c context.Context) (bool, error) {
		if err := e.probe.Health(c); err != nil {
			last = fmt.Errorf("/health: %w", err)
			return false, last
		}
		st, _, err := e.probe.V1Models(c, true)
		if err != nil || st != 200 {
			last = fmt.Errorf("GET /v1/models with the key answered %d (%v)", st, err)
			return false, last
		}
		st, _, err = e.probe.V1Models(c, false)
		if err != nil || st != 401 {
			last = fmt.Errorf("GET /v1/models without the key answered %d, not 401 (%v)", st, err)
			return false, last
		}
		return true, nil
	})
	if err != nil && !timedOut {
		return wrap(CodeInterrupted, "R5", err, "waiting for %s was interrupted", e.legacy())
	}
	if timedOut {
		return wrap(CodeInterrupted, "R5", last, "%s did not answer within %s", e.legacy(), max)
	}
	return nil
}

// R6: record the result. Tailscale Serve was never touched, so there is nothing to undo there.
func (e *Engine) r6Record(_ context.Context, _ *Plan, _ bool) error {
	_ = e.removeFile(e.paths.Result())
	return nil
}
