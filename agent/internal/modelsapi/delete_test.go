package modelsapi_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

const alphaBytes = 5870000000

func deleted(roots ...string) modelsapi.SyncDeleteResult {
	var res modelsapi.SyncDeleteResult
	for _, r := range roots {
		res.Deleted = append(res.Deleted, modelsapi.DeletedCopy{Root: r, Bytes: alphaBytes})
	}
	return res
}

func TestDeleteRemovesTheCopiesAndAudits(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.sync.delRes = deleted(rootMain)
	rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[modelsapi.DeleteResult](t, rec.Body.Bytes())
	if got.ID != idAlpha || len(got.Deleted) != 1 || got.Deleted[0].Root != rootMain || got.Deleted[0].Bytes != alphaBytes || got.Kept != nil || got.OpID == "" {
		t.Errorf("body = %+v", got)
	}
	if rec.Header().Get("X-LocalAI-Op-Id") != got.OpID {
		t.Errorf("op id header %q, body %q", rec.Header().Get("X-LocalAI-Op-Id"), got.OpID)
	}

	calls := r.sync.deleteCalls()
	if len(calls) != 1 || calls[0].Model.ID != idAlpha || len(calls[0].Roots) != 0 || !calls[0].KeepLast || calls[0].OpID != got.OpID {
		t.Fatalf("engine calls = %+v", calls)
	}
	if calls[0].Actor.Kind != audit.KindAdmin {
		t.Errorf("actor = %+v", calls[0].Actor)
	}
	ev := r.audit.only(t, audit.ActionModelDelete)
	if ev.Result != audit.ResultOK || ev.Code != "" || ev.OpID != got.OpID || ev.Actor.Kind != audit.KindAdmin ||
		ev.Target["model"] != idAlpha || ev.Target["roots"] != rootMain || ev.Target["keep_last"] != "true" {
		t.Errorf("audit = %+v", ev)
	}
	// models.ini is regenerated for a llama model that lost its files, with the lock already free.
	if r.regen.count() != 1 || len(r.regen.lockFree) != 1 || !r.regen.lockFree[0] {
		t.Errorf("regenerations %d, lock free during them %v", r.regen.count(), r.regen.lockFree)
	}
	if _, held := r.lock.Holder(); held {
		t.Error("the lock is still held")
	}
}

func TestDeleteQueryParameters(t *testing.T) {
	cases := []struct {
		query    string
		root     string
		keepLast bool
	}{
		{"", "", true},
		{"?keep_last=true", "", true},
		{"?keep_last=false", "", false},
		{"?keep_last=0", "", false},
		{"?root=" + rootDrive, rootDrive, true},
		{"?root=ark-3f9c0a12&keep_last=false", "ark-3f9c0a12", false},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			r := newRig(t, nil)
			r.sync.delRes = deleted(rootMain)
			if rec := r.send("DELETE", "/models/"+idAlpha+c.query, "admin", ``); rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			calls := r.sync.deleteCalls()
			if len(calls) != 1 || calls[0].KeepLast != c.keepLast {
				t.Fatalf("engine calls = %+v", calls)
			}
			if c.root == "" && len(calls[0].Roots) != 0 || c.root != "" && (len(calls[0].Roots) != 1 || calls[0].Roots[0] != c.root) {
				t.Errorf("roots = %v, want %q", calls[0].Roots, c.root)
			}
			if ev := r.audit.only(t, audit.ActionModelDelete); ev.Target["root"] != c.root {
				t.Errorf("audit target = %v", ev.Target)
			}
		})
	}
}

func TestDeleteNeedsTheAdmin(t *testing.T) {
	for _, c := range []struct {
		principal string
		status    int
		code      string
	}{
		{"", 401, doctor.SEC001},
		{"op", 403, doctor.SEC002},
		{"portal", 401, doctor.SVC110},
	} {
		r := newRig(t, nil)
		r.sync.delRes = deleted(rootMain)
		rec := r.send("DELETE", "/models/"+idAlpha, c.principal, ``)
		if rec.Code != c.status || envelope(t, rec.Body.Bytes()).Error.Code != c.code {
			t.Errorf("%q: %d %s", c.principal, rec.Code, rec.Body)
		}
		if len(r.sync.deleteCalls()) != 0 || len(r.audit.events()) != 0 {
			t.Errorf("%q: something ran", c.principal)
		}
	}
}

func TestDeleteRefusesAModelTheRouterHolds(t *testing.T) {
	for _, status := range []router.Status{router.StatusLoaded, router.StatusLoading, router.StatusSleeping} {
		t.Run(string(status), func(t *testing.T) {
			r := newRig(t, nil)
			r.has(rootMain, idAlpha)
			r.routerHas(router.ModelStatus{ID: idAlpha, Status: status})
			rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != http.StatusConflict || e.Error.Code != doctor.AGT022 || e.Error.Details["reason"] != "loaded" || e.Error.Details["model"] != idAlpha {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if e.Error.Fix != "Unload it first." {
				t.Errorf("fix = %q", e.Error.Fix)
			}
			if len(r.sync.deleteCalls()) != 0 {
				t.Error("the engine was asked to delete a loaded model")
			}
			if ev := r.audit.only(t, audit.ActionModelDelete); ev.Result != audit.ResultFail || ev.Code != doctor.AGT022 {
				t.Errorf("audit = %+v", ev)
			}
			if r.regen.count() != 0 {
				t.Error("regenerated after a refusal")
			}
			if _, held := r.lock.Holder(); held {
				t.Error("the lock is still held after the refusal")
			}
		})
	}
}

func TestDeleteAllowsAModelTheRouterListsButHasNotLoaded(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.routerHas(router.ModelStatus{ID: idAlpha, Status: router.StatusUnloaded})
	r.sync.delRes = deleted(rootMain)
	if rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``); rec.Code != http.StatusOK {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestDeleteRefusalInSpanish(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.routerHas(router.ModelStatus{ID: idAlpha, Status: router.StatusLoaded})
	rec := do(t, r.h, call{method: "DELETE", target: "/models/" + idAlpha, principal: "admin", header: map[string]string{"Accept-Language": "es"}})
	e := envelope(t, rec.Body.Bytes())
	if e.Error.Code != doctor.AGT022 || e.Error.Fix != "Descárguelo primero." || !strings.Contains(e.Error.Message, "está cargado") {
		t.Errorf("error = %+v", e.Error)
	}
}

func TestDeleteWhileAnotherModelOperationRuns(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	hold, err := r.lock.TryAcquire(router.HolderLoad)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusConflict || e.Error.Code != doctor.AGT022 || e.Error.Details["op_id"] != hold.OpID || e.Error.Details["running"] != router.HolderLoad {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.sync.deleteCalls()) != 0 {
		t.Error("the engine ran while the lock was held by a load")
	}
	if ev := r.audit.only(t, audit.ActionModelDelete); ev.Result != audit.ResultFail || ev.Code != doctor.AGT022 {
		t.Errorf("audit = %+v", ev)
	}
}

func TestDeleteHoldsTheLockWhileTheEngineWorks(t *testing.T) {
	r := newRig(t, nil)
	r.sync.delRes = deleted(rootMain)
	var kind string
	r.sync.onDelete = func() {
		if h, ok := r.lock.Holder(); ok {
			kind = h.Kind
		}
	}
	if rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if kind != router.HolderDelete {
		t.Errorf("holder during the delete = %q", kind)
	}
}

func TestDeleteKeepLast(t *testing.T) {
	last := modelsapi.SyncDeleteResult{Kept: []modelsapi.KeptCopy{{Root: rootMain, Reason: modelsapi.KeptLastVerifiedCopy}}}
	t.Run("the only copy is refused", func(t *testing.T) {
		r := newRig(t, nil)
		r.has(rootMain, idAlpha)
		r.sync.delRes = last
		rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
		e := envelope(t, rec.Body.Bytes())
		if rec.Code != http.StatusConflict || e.Error.Code != doctor.SYN044 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if kept, _ := e.Error.Details["kept"].([]any); len(kept) != 1 {
			t.Errorf("details = %v", e.Error.Details)
		}
		if ev := r.audit.only(t, audit.ActionModelDelete); ev.Result != audit.ResultDenied || ev.Code != doctor.SYN044 {
			t.Errorf("audit = %+v", ev)
		}
		if r.regen.count() != 0 {
			t.Error("regenerated although nothing was deleted")
		}
	})
	t.Run("one of two copies goes, the other is kept", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = modelsapi.SyncDeleteResult{
			Deleted: []modelsapi.DeletedCopy{{Root: rootMain, Bytes: alphaBytes}},
			Kept:    []modelsapi.KeptCopy{{Root: rootDrive, Reason: modelsapi.KeptLastVerifiedCopy}},
		}
		rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		got := decode[modelsapi.DeleteResult](t, rec.Body.Bytes())
		if len(got.Deleted) != 1 || len(got.Kept) != 1 || got.Kept[0].Root != rootDrive || got.Kept[0].Reason != "last_verified_copy" {
			t.Errorf("body = %+v", got)
		}
	})
	t.Run("keep_last=false is passed on, and an engine that deletes the last copy is believed", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = deleted(rootMain)
		if rec := r.send("DELETE", "/models/"+idAlpha+"?keep_last=false", "admin", ``); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if calls := r.sync.deleteCalls(); len(calls) != 1 || calls[0].KeepLast {
			t.Errorf("engine calls = %+v", calls)
		}
		if ev := r.audit.only(t, audit.ActionModelDelete); ev.Target["keep_last"] != "false" {
			t.Errorf("audit = %+v", ev)
		}
	})
	t.Run("a refusal without keep_last does not exist: kept copies are only reported", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = last
		rec := r.send("DELETE", "/models/"+idAlpha+"?keep_last=false", "admin", ``)
		if rec.Code != http.StatusOK {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestDeleteOfAModelThatIsNowhereIsAnIdempotentSuccess(t *testing.T) {
	r := newRig(t, nil)
	r.sync.delRes = modelsapi.SyncDeleteResult{Kept: []modelsapi.KeptCopy{{Root: rootMain, Reason: modelsapi.KeptNotPresent}}}
	rec := r.send("DELETE", "/models/"+idBeta, "admin", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[modelsapi.DeleteResult](t, rec.Body.Bytes())
	if got.Deleted == nil || len(got.Deleted) != 0 || len(got.Kept) != 1 || got.Kept[0].Reason != "not_present" {
		t.Errorf("body = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"deleted":[]`) {
		t.Errorf("deleted must be an empty array: %s", rec.Body)
	}
	if r.regen.count() != 0 {
		t.Error("regenerated although nothing was deleted")
	}
}

func TestDeleteRefusals(t *testing.T) {
	cases := []struct {
		name, target string
		status       int
		code         string
		field        string
	}{
		{"unknown model", "/models/no-such-model", 404, doctor.AGT021, ""},
		{"bad keep_last", "/models/" + idAlpha + "?keep_last=maybe", 400, doctor.AGT020, "keep_last"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			rec := r.send("DELETE", c.target, "admin", ``)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != c.status || e.Error.Code != c.code || (c.field != "" && e.Error.Details["field"] != c.field) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(r.sync.deleteCalls()) != 0 {
				t.Error("the engine was called")
			}
		})
	}
}

func TestDeleteEngineErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unknown root", &modelsapi.UnknownRootError{Root: "/mnt/nowhere"}, 400, doctor.AGT020},
		{"a bug", errors.New("engine exploded"), 500, doctor.AGT099},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.sync.delErr = c.err
			rec := r.send("DELETE", "/models/"+idAlpha+"?root=/mnt/nowhere", "admin", ``)
			e := envelope(t, rec.Body.Bytes())
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if c.status == 400 && e.Error.Details["field"] != "root" {
				t.Errorf("details = %v", e.Error.Details)
			}
			if ev := r.audit.only(t, audit.ActionModelDelete); ev.Result != audit.ResultFail || ev.Code != c.code {
				t.Errorf("audit = %+v", ev)
			}
			if _, held := r.lock.Holder(); held {
				t.Error("the lock is still held after a failure")
			}
			if r.regen.count() != 0 {
				t.Error("regenerated after a failure")
			}
		})
	}
}

func TestDeleteRegeneratesOnlyForLlamaModelsAndReportsItsWarnings(t *testing.T) {
	t.Run("not a llama model", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = deleted(rootMain)
		if rec := r.send("DELETE", "/models/"+idWhisper, "admin", ``); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if r.regen.count() != 0 {
			t.Error("models.ini regenerated for a whisper model")
		}
	})
	t.Run("warnings", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = deleted(rootMain)
		r.regen.res = statusapi.RegenResult{Warnings: []statusapi.WarningBlock{{Code: doctor.AGT022, Message: "later"}}}
		rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
		got := decode[modelsapi.DeleteResult](t, rec.Body.Bytes())
		if rec.Code != http.StatusOK || len(got.Warnings) != 1 || got.Warnings[0].Code != doctor.AGT022 {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("a regeneration that fails does not fail the delete", func(t *testing.T) {
		r := newRig(t, nil)
		r.sync.delRes = deleted(rootMain)
		r.regen.err = errors.New("boom")
		rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``)
		got := decode[modelsapi.DeleteResult](t, rec.Body.Bytes())
		if rec.Code != http.StatusOK || len(got.Deleted) != 1 || len(got.Warnings) != 1 || got.Warnings[0].Code != doctor.AGT099 {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("no regenerator", func(t *testing.T) {
		r := newRig(t, func(_ *rig, d *modelsapi.Deps) { d.Regenerator = nil })
		r.sync.delRes = deleted(rootMain)
		if rec := r.send("DELETE", "/models/"+idAlpha, "admin", ``); rec.Code != http.StatusOK {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestDeleteServiceMethodForTheCloudExecutor(t *testing.T) {
	r := newRig(t, nil)
	r.sync.delRes = deleted(rootMain)
	res, err := r.svc.Delete(t.Context(), modelsapi.DeleteRequest{ID: idAlpha, KeepLast: true, Actor: audit.Cloud("job-7")})
	if err != nil || len(res.Deleted) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if ev := r.audit.only(t, audit.ActionModelDelete); ev.Actor.Kind != audit.KindCloud || ev.Actor.JobID != "job-7" {
		t.Errorf("audit = %+v", ev)
	}
}
