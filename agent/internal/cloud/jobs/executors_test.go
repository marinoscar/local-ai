package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// codeOf is the doctor code a failed command settles with.
func codeOf(err error) string {
	code, _ := cloud.CodeAndMessage(err)
	return code
}

// wantCode fails the test unless err settles with code.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	if got := codeOf(err); got != code {
		t.Errorf("code = %s (%v), want %s", got, err, code)
	}
}

func opError(code router.Code, msg string) error {
	return &router.OpError{Code: code, Status: 409, Message: msg, Details: map[string]any{}}
}

// ---------------------------------------------------------------------------------------------------
// status.refresh

func TestStatusRefresh(t *testing.T) {
	t.Run("sends the key first when the cloud has not got it", func(t *testing.T) {
		f := newFakes()
		var order []string
		f.link.order = &order
		f.link.res = cloud.StateRefresh{Seq: 7, ReportedAt: "2026-11-02T15:00:00Z"}
		out, err := f.run(t, cloud.TypeStatusRefresh, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(order, ",") != "key,state" {
			t.Errorf("order = %v, want key then state", order)
		}
		if got := resultJSON(t, cloud.TypeStatusRefresh, out); got != `{"seq":7,"reported_at":"2026-11-02T15:00:00Z"}` {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("does not resend a key the cloud holds", func(t *testing.T) {
		f := newFakes()
		f.state.st.Key.CloudSynced = true
		f.link.res = cloud.StateRefresh{Seq: 1, ReportedAt: "2026-11-02T15:00:00Z"}
		if _, err := f.run(t, cloud.TypeStatusRefresh, `{}`); err != nil {
			t.Fatal(err)
		}
		if f.link.keys != 0 || f.link.pushes != 1 {
			t.Errorf("keys = %d pushes = %d", f.link.keys, f.link.pushes)
		}
	})
	t.Run("a key push that fails does not fail the refresh", func(t *testing.T) {
		f := newFakes()
		f.link.keyErr = errors.New("no route")
		f.link.res = cloud.StateRefresh{Seq: 2, ReportedAt: "2026-11-02T15:00:00Z"}
		if _, err := f.run(t, cloud.TypeStatusRefresh, `{}`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the clock stands in for a missing reported_at", func(t *testing.T) {
		f := newFakes()
		f.state.st.Key.CloudSynced = true
		f.link.res = cloud.StateRefresh{Seq: 3}
		out, err := f.run(t, cloud.TypeStatusRefresh, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeStatusRefresh, out); !strings.Contains(got, `"reported_at":"2026-11-02T15:00:00Z"`) {
			t.Errorf("result = %s", got)
		}
	})
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"cloud without the endpoint", cloud.ErrNoStateEndpoint, doctor.CLD056},
		{"not linked", cloud.ErrNotLinked, doctor.CLD050},
		{"any other failure", errors.New("boom"), doctor.CLD053},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.state.st.Key.CloudSynced = true
			f.link.err = tc.err
			_, err := f.run(t, cloud.TypeStatusRefresh, `{}`)
			wantCode(t, err, tc.code)
		})
	}
}

// ---------------------------------------------------------------------------------------------------
// model.load, model.unload, model.set_default

func TestModelLoad(t *testing.T) {
	t.Run("loads with wait, the 60 s lock wait and the cloud as principal", func(t *testing.T) {
		f := newFakes()
		f.router.load = router.LoadResult{ID: "model-a", Status: "loaded", LoadMS: 4200, Unloaded: []string{"model-b"}}
		f.status.edit(func(s *statusapi.Status) {
			s.Hardware.VRAMUsedMiB = i64(20480)
			s.Models.Default = str("model-z")
		})
		out, err := f.run(t, cloud.TypeModelLoad, `{"model_id":"model-a"}`)
		if err != nil {
			t.Fatal(err)
		}
		req := f.router.loadReqs[0]
		if req.ID != "model-a" || !req.Wait || !req.UnloadOthers || req.LockWait != router.DefaultLockWait {
			t.Errorf("request = %+v", req)
		}
		if req.Actor != audit.Cloud("job-1") {
			t.Errorf("actor = %+v", req.Actor)
		}
		if len(f.router.defaults) != 0 {
			t.Errorf("the default was set without set_default: %v", f.router.defaults)
		}
		if f.hw.refresh != 1 {
			t.Errorf("hardware refreshes = %d, want one before vram is read", f.hw.refresh)
		}
		got := resultJSON(t, cloud.TypeModelLoad, out)
		for _, want := range []string{`"model_id":"model-a"`, `"status":"loaded"`, `"load_ms":4200`, `"unloaded":["model-b"]`, `"vram_used_mib":20480`, `"is_default":false`} {
			if !strings.Contains(got, want) {
				t.Errorf("result %s lacks %s", got, want)
			}
		}
	})
	t.Run("set_default moves the default after the load", func(t *testing.T) {
		f := newFakes()
		f.router.load = router.LoadResult{ID: "model-a", Status: "loaded"}
		f.status.edit(func(s *statusapi.Status) { s.Models.Default = str("model-a") })
		out, err := f.run(t, cloud.TypeModelLoad, `{"model_id":"model-a","set_default":true,"unload_others":false}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.router.defaults) != 1 || f.router.defaults[0] != "model-a" || f.router.actors[0] != audit.Cloud("job-1") {
			t.Errorf("defaults = %v actors = %v", f.router.defaults, f.router.actors)
		}
		if f.router.loadReqs[0].UnloadOthers {
			t.Error("unload_others false was ignored")
		}
		if got := resultJSON(t, cloud.TypeModelLoad, out); !strings.Contains(got, `"is_default":true`) || !strings.Contains(got, `"vram_used_mib":null`) {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("a load that does not wait is refused locally", func(t *testing.T) {
		f := newFakes()
		f.router.load = router.LoadResult{Accepted: true}
		_, err := f.run(t, cloud.TypeModelLoad, `{"model_id":"model-a"}`)
		wantCode(t, err, doctor.AGT099)
	})
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"does not fit", opError("LAI-CAT-005", "Model does not fit in free VRAM"), "LAI-CAT-005"},
		{"busy", &router.BusyError{OpID: "op-1"}, doctor.AGT022},
		{"interrupted by shutdown", router.ErrInterrupted, doctor.AGT024},
		{"deadline", context.DeadlineExceeded, doctor.AGT099},
	} {
		t.Run("error: "+tc.name, func(t *testing.T) {
			f := newFakes()
			f.router.loadErr = tc.err
			_, err := f.run(t, cloud.TypeModelLoad, `{"model_id":"model-a","set_default":true}`)
			wantCode(t, err, tc.code)
			if len(f.router.defaults) != 0 {
				t.Error("the default moved although the load failed")
			}
		})
	}
	t.Run("a failed set_default fails the command", func(t *testing.T) {
		f := newFakes()
		f.router.load = router.LoadResult{ID: "model-a", Status: "loaded"}
		f.router.setErr = opError("LAI-LLM-002", "not loadable")
		_, err := f.run(t, cloud.TypeModelLoad, `{"model_id":"model-a","set_default":true}`)
		wantCode(t, err, "LAI-LLM-002")
	})
}

func TestModelUnload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		already   bool
		wasLoaded string
	}{
		{"loaded", false, `"was_loaded":true`},
		{"already unloaded", true, `"was_loaded":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.router.unload = router.UnloadResult{ID: "model-a", AlreadyUnloaded: tc.already}
			out, err := f.run(t, cloud.TypeModelUnload, `{"model_id":"model-a"}`)
			if err != nil {
				t.Fatal(err)
			}
			req := f.router.unloadReq[0]
			if req.ID != "model-a" || req.Force || req.LockWait != router.DefaultLockWait || req.Actor != audit.Cloud("job-1") {
				t.Errorf("request = %+v: a cloud unload is never forced and waits for the lock", req)
			}
			if got := resultJSON(t, cloud.TypeModelUnload, out); !strings.Contains(got, tc.wasLoaded) || !strings.Contains(got, `"status":"unloaded"`) {
				t.Errorf("result = %s", got)
			}
		})
	}
	t.Run("error", func(t *testing.T) {
		f := newFakes()
		f.router.unloadErr = opError("LAI-LLM-030", "survival keeps the last model")
		_, err := f.run(t, cloud.TypeModelUnload, `{"model_id":"model-a"}`)
		wantCode(t, err, "LAI-LLM-030")
	})
}

func TestModelSetDefault(t *testing.T) {
	f := newFakes()
	f.router.setRes = router.DefaultResult{Default: "model-a", Previous: "model-p"}
	out, err := f.run(t, cloud.TypeModelSetDefault, `{"model_id":"model-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultJSON(t, cloud.TypeModelSetDefault, out); got != `{"model_id":"model-a","previous":"model-p"}` {
		t.Errorf("result = %s", got)
	}
	f.router.setRes = router.DefaultResult{Default: "model-a"}
	out, err = f.run(t, cloud.TypeModelSetDefault, `{"model_id":"model-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultJSON(t, cloud.TypeModelSetDefault, out); got != `{"model_id":"model-a","previous":null}` {
		t.Errorf("result = %s", got)
	}
	f.router.setErr = opError("LAI-LLM-002", "not loadable")
	_, err = f.run(t, cloud.TypeModelSetDefault, `{"model_id":"model-a"}`)
	wantCode(t, err, "LAI-LLM-002")
}

// ---------------------------------------------------------------------------------------------------
// model.download

func TestModelDownload(t *testing.T) {
	complete := func(roots ...string) []statusapi.ModelLocation {
		var out []statusapi.ModelLocation
		for _, r := range roots {
			out = append(out, statusapi.ModelLocation{Root: r, Complete: true})
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		row      statusapi.ModelRow
		params   string
		present  bool
		wantRoot []string
	}{
		{"already on the internal root", statusapi.ModelRow{Present: true, Verified: true, Locations: complete("internal")}, `{"model_id":"model-a"}`, true, nil},
		{"present but not verified", statusapi.ModelRow{Present: true, Locations: complete("internal"), SizeBytes: 1000}, `{"model_id":"model-a"}`, false, nil},
		{"missing on a named root", statusapi.ModelRow{Present: true, Verified: true, Locations: complete("internal"), SizeBytes: 1000}, `{"model_id":"model-a","roots":["ark-0123abcd"]}`, false, []string{"ark-0123abcd"}},
		{"on every named root", statusapi.ModelRow{Present: true, Verified: true, Locations: complete("internal", "ark-0123abcd")}, `{"model_id":"model-a","roots":["internal","ark-0123abcd"]}`, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.models.detail = modelsapi.ModelDetail{ModelRow: tc.row}
			f.models.accepted = modelsapi.DownloadAccepted{ID: "model-a", SyncRunID: "run-1", Queued: true}
			out, err := f.run(t, cloud.TypeModelDownload, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got := resultJSON(t, cloud.TypeModelDownload, out)
			if tc.present {
				if len(f.models.downloads) != 0 || !strings.Contains(got, `"already_present":true`) || !strings.Contains(got, `"sync_run_id":null`) {
					t.Errorf("downloads = %d result = %s", len(f.models.downloads), got)
				}
				return
			}
			if len(f.models.downloads) != 1 || !strings.Contains(got, `"sync_run_id":"run-1"`) || !strings.Contains(got, `"already_present":false`) {
				t.Errorf("downloads = %d result = %s", len(f.models.downloads), got)
			}
			req := f.models.downloads[0]
			if req.ID != "model-a" || req.Actor != audit.Cloud("job-1") || strings.Join(req.Roots, ",") != strings.Join(tc.wantRoot, ",") {
				t.Errorf("request = %+v", req)
			}
		})
	}
	t.Run("an unknown model", func(t *testing.T) {
		f := newFakes()
		f.models.getErr = api.NewError(doctor.CAT001, 404, nil)
		_, err := f.run(t, cloud.TypeModelDownload, `{"model_id":"model-a"}`)
		wantCode(t, err, doctor.CAT001)
	})
	t.Run("a download the engine refuses", func(t *testing.T) {
		f := newFakes()
		f.models.dlErr = api.NewError(doctor.AGT023, 409, nil)
		_, err := f.run(t, cloud.TypeModelDownload, `{"model_id":"model-a"}`)
		wantCode(t, err, doctor.AGT023)
	})
}

// ---------------------------------------------------------------------------------------------------
// model.delete

func TestModelDeleteAlwaysKeepsTheLastCopy(t *testing.T) {
	f := newFakes()
	f.models.deleteFn = func(req modelsapi.DeleteRequest, call int) (modelsapi.DeleteResult, error) {
		switch req.Root {
		case "internal":
			return modelsapi.DeleteResult{ID: req.ID, Deleted: []modelsapi.DeletedCopy{{Root: "internal", Bytes: 1000}}}, nil
		case "ark-0123abcd":
			return modelsapi.DeleteResult{}, api.NewError(doctor.SYN044, 409, nil)
		case "ark-89abcdef":
			return modelsapi.DeleteResult{}, api.NewError(doctor.AGT022, 409, map[string]any{"reason": "loaded"})
		}
		return modelsapi.DeleteResult{ID: req.ID, Kept: []modelsapi.KeptCopy{{Root: req.Root, Reason: modelsapi.KeptNotPresent}}}, nil
	}
	out, err := f.run(t, cloud.TypeModelDelete, `{"model_id":"model-a","roots":["internal","ark-0123abcd","ark-89abcdef","ark-00000000"],"confirm":"model-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.models.deletes) != 4 {
		t.Fatalf("deletes = %d, one per root", len(f.models.deletes))
	}
	for _, d := range f.models.deletes {
		if !d.KeepLast || d.ID != "model-a" || d.Actor != audit.Cloud("job-1") || d.Root == "" {
			t.Errorf("delete = %+v: keep-last must always be set and the root named", d)
		}
	}
	want := `{"model_id":"model-a","deleted_roots":["internal"],"kept_roots":[` +
		`{"root":"ark-0123abcd","reason":"last_verified_copy"},{"root":"ark-89abcdef","reason":"loaded"},{"root":"ark-00000000","reason":"not_present"}],"freed_bytes":1000}`
	if got := resultJSON(t, cloud.TypeModelDelete, out); got != want {
		t.Errorf("result = %s\nwant     %s", got, want)
	}
}

func TestModelDeleteRefusals(t *testing.T) {
	f := newFakes()
	h := f.handler(t, cloud.TypeModelDelete)
	// The agent never takes the cloud's word for the confirmation.
	if _, err := h.Decode(json.RawMessage(`{"model_id":"model-a","roots":["internal"],"confirm":"model-b"}`)); err == nil {
		t.Fatal("a wrong confirm decoded")
	}
	if len(f.models.deletes) != 0 {
		t.Error("something was deleted")
	}
	t.Run("another error ends the command and later roots are not touched", func(t *testing.T) {
		f := newFakes()
		f.models.deleteFn = func(req modelsapi.DeleteRequest, call int) (modelsapi.DeleteResult, error) {
			return modelsapi.DeleteResult{}, api.NewError(doctor.CAT001, 404, nil)
		}
		_, err := f.run(t, cloud.TypeModelDelete, `{"model_id":"model-a","roots":["internal","ark-0123abcd"],"confirm":"model-a"}`)
		wantCode(t, err, doctor.CAT001)
		if len(f.models.deletes) != 1 {
			t.Errorf("deletes = %d", len(f.models.deletes))
		}
	})
}

// A delete that finds modelOpsMu taken tries again every second for a minute, on the injected clock.
func TestModelDeleteWaitsForTheModelLock(t *testing.T) {
	f := newFakes()
	f.models.deleteFn = func(req modelsapi.DeleteRequest, call int) (modelsapi.DeleteResult, error) {
		if call < 3 {
			return modelsapi.DeleteResult{}, opError(router.CodeBusy, "busy")
		}
		return modelsapi.DeleteResult{ID: req.ID, Deleted: []modelsapi.DeletedCopy{{Root: "internal", Bytes: 5}}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for i := 0; i < 2; i++ {
			if f.clock.WaitPending(ctx, 1) != nil {
				return
			}
			f.clock.Advance(busyPoll)
		}
	}()
	out, err := f.runCtx(t, ctx, cloud.TypeModelDelete, `{"model_id":"model-a","roots":["internal"],"confirm":"model-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.models.deletes) != 3 {
		t.Errorf("deletes = %d, want 3 tries", len(f.models.deletes))
	}
	if got := resultJSON(t, cloud.TypeModelDelete, out); !strings.Contains(got, `"freed_bytes":5`) {
		t.Errorf("result = %s", got)
	}
}

func TestModelDeleteGivesUpAfterAMinuteOfBusy(t *testing.T) {
	f := newFakes()
	f.models.deleteFn = func(modelsapi.DeleteRequest, int) (modelsapi.DeleteResult, error) {
		return modelsapi.DeleteResult{}, opError(router.CodeBusy, "busy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if f.clock.WaitPending(ctx, 1) != nil {
				return
			}
			f.clock.Advance(busyPoll)
		}
	}()
	_, err := f.runCtx(t, ctx, cloud.TypeModelDelete, `{"model_id":"model-a","roots":["internal"],"confirm":"model-a"}`)
	wantCode(t, err, doctor.AGT022)
	if n := len(f.models.deletes); n < 60 || n > 62 {
		t.Errorf("tries = %d, want about one per second for a minute", n)
	}
}

// ---------------------------------------------------------------------------------------------------
// ark.sync, ark.verify

func TestArkSync(t *testing.T) {
	t.Run("starts a run and returns at its start", func(t *testing.T) {
		f := newFakes()
		f.ark.syncRes = ArkSyncStarted{RunID: "run-1", ItemsToDownload: 3, BytesToDownload: 3000, ItemsSkippedBudget: 1}
		out, err := f.run(t, cloud.TypeArkSync, `{"roots":["internal"],"items":["content-a"]}`)
		if err != nil {
			t.Fatal(err)
		}
		req := f.ark.syncs[0]
		if strings.Join(req.Roots, ",") != "internal" || strings.Join(req.Items, ",") != "content-a" || req.DryRun || req.Actor != audit.Cloud("job-1") {
			t.Errorf("request = %+v", req)
		}
		want := `{"sync_run_id":"run-1","started":true,"plan":{"items_to_download":3,"bytes_to_download":3000,"items_skipped_budget":1}}`
		if got := resultJSON(t, cloud.TypeArkSync, out); got != want {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("a run that is already going is not an error", func(t *testing.T) {
		f := newFakes()
		f.ark.syncErr = &modelsapi.RunActiveError{RunID: "run-0"}
		out, err := f.run(t, cloud.TypeArkSync, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeArkSync, out); !strings.Contains(got, `"sync_run_id":"run-0"`) || !strings.Contains(got, `"started":false`) {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("a dry run starts nothing", func(t *testing.T) {
		f := newFakes()
		f.ark.syncRes = ArkSyncStarted{ItemsToDownload: 2, BytesToDownload: 20}
		out, err := f.run(t, cloud.TypeArkSync, `{"dry_run":true}`)
		if err != nil {
			t.Fatal(err)
		}
		if !f.ark.syncs[0].DryRun {
			t.Error("dry_run was not passed on")
		}
		if got := resultJSON(t, cloud.TypeArkSync, out); !strings.Contains(got, `"started":false`) || !strings.Contains(got, `"sync_run_id":"dry-run"`) {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("an engine error keeps its code", func(t *testing.T) {
		f := newFakes()
		f.ark.syncErr = api.NewError(doctor.AGT023, 409, nil)
		_, err := f.run(t, cloud.TypeArkSync, `{}`)
		wantCode(t, err, doctor.AGT023)
	})
}

func TestArkVerify(t *testing.T) {
	f := newFakes()
	f.ark.verRes = ArkVerifyStarted{RunID: "ver-1", ItemsPlanned: 9}
	out, err := f.run(t, cloud.TypeArkVerify, `{"roots":["internal"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultJSON(t, cloud.TypeArkVerify, out); got != `{"verify_run_id":"ver-1","started":true,"items_planned":9}` {
		t.Errorf("result = %s", got)
	}
	if f.ark.verifies[0].Actor != audit.Cloud("job-1") {
		t.Errorf("actor = %+v", f.ark.verifies[0].Actor)
	}
	f.ark.verErr = &modelsapi.RunActiveError{RunID: "run-0"}
	out, err = f.run(t, cloud.TypeArkVerify, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultJSON(t, cloud.TypeArkVerify, out); !strings.Contains(got, `"started":false`) {
		t.Errorf("result = %s", got)
	}
	f.ark.verErr = nil
	f.ark.verRes = ArkVerifyStarted{}
	_, err = f.run(t, cloud.TypeArkVerify, `{}`)
	wantCode(t, err, doctor.AGT099)
}

// ---------------------------------------------------------------------------------------------------
// mode.offline

func TestModeOffline(t *testing.T) {
	t.Run("the result is ready before the transition runs", func(t *testing.T) {
		f := newFakes()
		f.modes.info = modes.Info{Mode: modes.Online, Effective: modes.Online}
		out, err := f.run(t, cloud.TypeModeOffline, `{"reason":"maintenance window"}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.modes.reqs) != 0 {
			t.Fatal("the transition started before the result was settled")
		}
		if out.After == nil {
			t.Fatal("no step after the settle")
		}
		if got := resultJSON(t, cloud.TypeModeOffline, out); got != `{"previous_mode":"online","mode":"offline"}` {
			t.Errorf("result = %s", got)
		}
		out.After(context.Background())
		if len(f.modes.reqs) != 1 || f.modes.reqs[0] != modes.Offline {
			t.Fatalf("requests = %v", f.modes.reqs)
		}
		if a := f.modes.opts[0].Actor; a.Kind != "cloud" {
			t.Errorf("actor = %+v", a)
		}
	})
	t.Run("a transition that fails is logged, not raised", func(t *testing.T) {
		f := newFakes()
		f.modes.err = errors.New("busy")
		out, err := f.run(t, cloud.TypeModeOffline, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		out.After(context.Background())
	})
	t.Run("the cloud cannot leave survival mode", func(t *testing.T) {
		f := newFakes()
		f.modes.info = modes.Info{Mode: modes.Survival, Effective: modes.Survival}
		_, err := f.run(t, cloud.TypeModeOffline, `{}`)
		wantCode(t, err, doctor.AGT023)
		if len(f.modes.reqs) != 0 {
			t.Error("a transition was requested")
		}
	})
}

// ---------------------------------------------------------------------------------------------------
// key.rotate

func TestKeyRotate(t *testing.T) {
	t.Run("the result carries the fingerprint and never the key", func(t *testing.T) {
		f := newFakes()
		f.models.rotated = modelsapi.KeyRotated{Fingerprint: "ab12…cd34", RotatedAt: "2026-11-02T15:00:00Z", CloudSynced: true}
		out, err := f.run(t, cloud.TypeKeyRotate, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeKeyRotate, out); got != `{"fingerprint":"ab12…cd34","rotated_at":"2026-11-02T15:00:00Z","cloud_synced":true}` {
			t.Errorf("result = %s", got)
		}
		if r := f.models.rotates[0]; !r.Restart || r.Actor != audit.Cloud("job-1") {
			t.Errorf("request = %+v", r)
		}
	})
	t.Run("cloud_synced false is reported, not hidden", func(t *testing.T) {
		f := newFakes()
		f.models.rotated = modelsapi.KeyRotated{Fingerprint: "ab12…cd34", RotatedAt: "2026-11-02T15:00:00Z"}
		out, err := f.run(t, cloud.TypeKeyRotate, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeKeyRotate, out); !strings.Contains(got, `"cloud_synced":false`) {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("an error keeps its code", func(t *testing.T) {
		f := newFakes()
		f.models.rotateFn = func(int) (modelsapi.KeyRotated, error) {
			return modelsapi.KeyRotated{}, api.NewError(doctor.AGT023, 409, nil)
		}
		_, err := f.run(t, cloud.TypeKeyRotate, `{}`)
		wantCode(t, err, doctor.AGT023)
	})
}

// ---------------------------------------------------------------------------------------------------
// service.restart

func TestServiceRestart(t *testing.T) {
	for _, tc := range []struct {
		active string
		want   string
	}{
		{"active", "active"}, {"activating", "activating"}, {"failed", "failed"}, {"", "failed"}, {"inactive", "failed"},
	} {
		t.Run("state "+tc.active, func(t *testing.T) {
			f := newFakes()
			f.services.res = api.ServiceActionResult{Active: tc.active}
			out, err := f.run(t, cloud.TypeServiceRestart, `{"service":"kiwix"}`)
			if err != nil {
				t.Fatal(err)
			}
			req := f.services.reqs[0]
			if req.Name != "kiwix" || req.Action != services.ActionRestart || req.Actor != audit.Cloud("job-1") {
				t.Errorf("request = %+v", req)
			}
			want := `{"service":"kiwix","active_state":"` + tc.want + `","restarted_at":"2026-11-02T15:00:00Z"}`
			if got := resultJSON(t, cloud.TypeServiceRestart, out); got != want {
				t.Errorf("result = %s", got)
			}
		})
	}
	t.Run("a refusal keeps its code", func(t *testing.T) {
		f := newFakes()
		f.services.fn = func(int) (api.ServiceActionResult, error) {
			return api.ServiceActionResult{}, api.NewError(doctor.AGT023, 409, nil)
		}
		_, err := f.run(t, cloud.TypeServiceRestart, `{"service":"llama"}`)
		wantCode(t, err, doctor.AGT023)
	})
	t.Run("waits for the model lock", func(t *testing.T) {
		f := newFakes()
		f.services.fn = func(call int) (api.ServiceActionResult, error) {
			if call == 1 {
				return api.ServiceActionResult{}, opError(router.CodeBusy, "busy")
			}
			return api.ServiceActionResult{Name: "llama", Active: "active"}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() {
			if f.clock.WaitPending(ctx, 1) == nil {
				f.clock.Advance(busyPoll)
			}
		}()
		if _, err := f.runCtx(t, ctx, cloud.TypeServiceRestart, `{"service":"llama"}`); err != nil {
			t.Fatal(err)
		}
		if len(f.services.reqs) != 2 {
			t.Errorf("requests = %d", len(f.services.reqs))
		}
	})
}

// ---------------------------------------------------------------------------------------------------
// logs.tail

func TestLogsTail(t *testing.T) {
	at := time.Date(2026, 11, 2, 14, 59, 0, 0, time.UTC)
	t.Run("lines are rfc3339, a space and the message", func(t *testing.T) {
		f := newFakes()
		f.logs.tail = services.JournalTail{Unit: "localai-agent.service", Lines: []services.JournalLine{
			{At: at, Message: "started"}, {At: at.Add(time.Second), Message: "ready"},
		}}
		out, err := f.run(t, cloud.TypeLogsTail, `{"unit":"agent","lines":50}`)
		if err != nil {
			t.Fatal(err)
		}
		if c := f.logs.calls[0]; c.Unit != "localai-agent.service" || c.Lines != 50 {
			t.Errorf("tail call = %+v: the unit must come from the closed table", c)
		}
		want := `{"unit":"localai-agent.service","lines":["2026-11-02T14:59:00Z started","2026-11-02T14:59:01Z ready"],"truncated":false}`
		if got := resultJSON(t, cloud.TypeLogsTail, out); got != want {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("a long line is cut and the reader's truncation is kept", func(t *testing.T) {
		f := newFakes()
		f.logs.tail = services.JournalTail{Unit: "localai-agent.service", Truncated: true, Lines: []services.JournalLine{{At: at, Message: strings.Repeat("x", 5000)}}}
		out, err := f.run(t, cloud.TypeLogsTail, `{"unit":"agent"}`)
		if err != nil {
			t.Fatal(err)
		}
		res := out.Result.(LogsTailResult)
		if len(res.Lines) != 1 || len([]rune(res.Lines[0])) != maxLogLineChars || !res.Truncated {
			t.Errorf("lines = %d, first = %d runes, truncated = %v", len(res.Lines), len([]rune(res.Lines[0])), res.Truncated)
		}
		resultJSON(t, cloud.TypeLogsTail, out)
	})
	t.Run("more lines than asked keeps the newest", func(t *testing.T) {
		f := newFakes()
		var lines []services.JournalLine
		for i := 0; i < 5; i++ {
			lines = append(lines, services.JournalLine{At: at, Message: string(rune('a' + i))})
		}
		f.logs.tail = services.JournalTail{Unit: "localai-agent.service", Lines: lines}
		out, err := f.run(t, cloud.TypeLogsTail, `{"unit":"agent","lines":2}`)
		if err != nil {
			t.Fatal(err)
		}
		res := out.Result.(LogsTailResult)
		if len(res.Lines) != 2 || !strings.HasSuffix(res.Lines[1], " e") || !res.Truncated {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("a result over the byte budget drops the oldest lines", func(t *testing.T) {
		f := newFakes()
		var lines []services.JournalLine
		for i := 0; i < 500; i++ {
			lines = append(lines, services.JournalLine{At: at, Message: strings.Repeat("y", 1900)})
		}
		f.logs.tail = services.JournalTail{Unit: "localai-agent.service", Lines: lines}
		out, err := f.run(t, cloud.TypeLogsTail, `{"unit":"agent","lines":500}`)
		if err != nil {
			t.Fatal(err)
		}
		res := out.Result.(LogsTailResult)
		raw, _ := json.Marshal(res)
		if !res.Truncated || len(res.Lines) >= 500 || len(raw) >= cloud.MaxResultBytes {
			t.Errorf("lines = %d truncated = %v bytes = %d", len(res.Lines), res.Truncated, len(raw))
		}
	})
	t.Run("the journal failing is a plain message, not its text", func(t *testing.T) {
		f := newFakes()
		f.logs.err = errors.New("exec: journalctl: /home/someone/secret")
		_, err := f.run(t, cloud.TypeLogsTail, `{"unit":"agent"}`)
		wantCode(t, err, doctor.AGT099)
		if _, msg := cloud.CodeAndMessage(err); strings.Contains(msg, "someone") {
			t.Errorf("message = %q leaks the error", msg)
		}
	})
}

// ---------------------------------------------------------------------------------------------------
// catalog.refresh

func TestCatalogRefresh(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		f := newFakes()
		f.catalog.body = statusapi.CatalogRefreshBody{Version: "2026.11.02-1", Changed: true}
		out, err := f.run(t, cloud.TypeCatalogRefresh, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeCatalogRefresh, out); got != `{"catalog_version":"2026.11.02-1","changed":true,"accepted":true,"rejected_code":null}` {
			t.Errorf("result = %s", got)
		}
		if f.catalog.actors[0] != audit.Cloud("job-1") {
			t.Errorf("actor = %+v", f.catalog.actors[0])
		}
	})
	t.Run("a catalog that fails its lint is a result, not a failure", func(t *testing.T) {
		f := newFakes()
		f.catalog.err = api.NewError(doctor.CAT002, 422, nil)
		f.status.edit(func(s *statusapi.Status) { s.Node.CatalogVersion = str("2026.10.30-2") })
		out, err := f.run(t, cloud.TypeCatalogRefresh, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := resultJSON(t, cloud.TypeCatalogRefresh, out); got != `{"catalog_version":"2026.10.30-2","changed":false,"accepted":false,"rejected_code":"LAI-CAT-002"}` {
			t.Errorf("result = %s", got)
		}
	})
	t.Run("a rejected catalog with no version to report is a failure", func(t *testing.T) {
		f := newFakes()
		f.catalog.err = api.NewError(doctor.CAT002, 422, nil)
		_, err := f.run(t, cloud.TypeCatalogRefresh, `{}`)
		wantCode(t, err, doctor.CAT002)
	})
	t.Run("a version in the wrong form is not sent", func(t *testing.T) {
		f := newFakes()
		f.catalog.body = statusapi.CatalogRefreshBody{Version: "latest"}
		_, err := f.run(t, cloud.TypeCatalogRefresh, `{}`)
		wantCode(t, err, doctor.AGT099)
	})
	t.Run("another refusal keeps its code", func(t *testing.T) {
		f := newFakes()
		f.catalog.err = api.NewError(doctor.AGT023, 409, nil)
		_, err := f.run(t, cloud.TypeCatalogRefresh, `{}`)
		wantCode(t, err, doctor.AGT023)
	})
}

// ---------------------------------------------------------------------------------------------------
// agent.update (the seam of AGENT-19)

type fakeUpdater struct {
	versions []string
	out      cloud.Outcome
	err      error
}

func (u *fakeUpdater) Update(_ context.Context, _ cloud.Job, version string) (cloud.Outcome, error) {
	u.versions = append(u.versions, version)
	return u.out, u.err
}

func TestAgentUpdateSeam(t *testing.T) {
	t.Run("without an updater the type fails with LAI-AGT-023", func(t *testing.T) {
		f := newFakes()
		_, err := f.run(t, cloud.TypeAgentUpdate, `{"version":"1.2.3"}`)
		wantCode(t, err, doctor.AGT023)
	})
	t.Run("the updater receives the decoded version", func(t *testing.T) {
		f := newFakes()
		u := &fakeUpdater{out: cloud.Outcome{Result: AgentUpdateResult{FromVersion: "1.2.2", ToVersion: "1.2.3", Status: "already_current"}}}
		d := f.deps()
		d.AgentUpdate = u
		h := f.handlerOf(t, d, cloud.TypeAgentUpdate)
		p, err := h.Decode(json.RawMessage(`{"version":"1.2.3"}`))
		if err != nil {
			t.Fatal(err)
		}
		out, err := h.Run(context.Background(), job("job-1"), p)
		if err != nil {
			t.Fatal(err)
		}
		if len(u.versions) != 1 || u.versions[0] != "1.2.3" {
			t.Errorf("versions = %v", u.versions)
		}
		resultJSON(t, cloud.TypeAgentUpdate, out)
	})
	t.Run("the type needs inflight.json", func(t *testing.T) {
		f := newFakes()
		n, ok := f.handler(t, cloud.TypeAgentUpdate).(interface{ NeedsInflight() bool })
		if !ok || !n.NeedsInflight() {
			t.Error("agent.update must be refused when inflight.json cannot be written")
		}
	})
}

// A handler is handed the wrong params type only by a bug; it fails the command, never panics.
func TestHandlerRefusesParamsOfAnotherType(t *testing.T) {
	f := newFakes()
	_, err := f.handler(t, cloud.TypeModelLoad).Run(context.Background(), job("job-1"), struct{}{})
	wantCode(t, err, doctor.AGT099)
}
