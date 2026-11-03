package ark_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

func driveAPI(a *actRig) ark.DriveAPI {
	return ark.DriveAPI{Actions: a.acts, View: ark.DrivesView{Detector: a.m}}
}

func req(root string) api.ArkDriveRequest {
	return api.ArkDriveRequest{RootID: root, Lang: "en", OpID: "op_7k2m9q4r8t1c"}
}

// wantAPIError asserts err is an *api.Error with the status and code and returns it.
func wantAPIError(t *testing.T, err error, status int, code string) *api.Error {
	t.Helper()
	var ae *api.Error
	if !errors.As(err, &ae) {
		t.Fatalf("err = %#v, want *api.Error", err)
	}
	if ae.Status != status || ae.Code != code {
		t.Fatalf("answer %d %s (%v), want %d %s", ae.Status, ae.Code, ae.Details, status, code)
	}
	return ae
}

// Init answers the drive object of GET /ark/drives, state ready; it validates against the ArkDrive schema.
func TestDriveAPIInitReturnsTheDriveObject(t *testing.T) {
	a := newActRig(t)
	a.uninitialized()
	got, err := driveAPI(a).Init(context.Background(), req(rid))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["root_id"] != rid || doc["state"] != "ready" || doc["initialized"] != true || doc["profile"] != "ark-drive-test" || doc["mount"] != a.mnt {
		t.Errorf("drive = %s", b)
	}
	spec, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	sch, err := spec.Schema("ArkDrive")
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.V.Validate(sch, doc); err != nil {
		t.Errorf("the drive object does not match the ArkDrive schema: %v\n%s", err, b)
	}
}

// Mount answers the same drive object.
func TestDriveAPIMount(t *testing.T) {
	a := newActRig(t)
	a.notMounted(true)
	got, err := driveAPI(a).Mount(context.Background(), req(rid))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.(ark.DriveView); !ok || v.State != "ready" || v.Mount == nil || *v.Mount != a.mnt {
		t.Errorf("drive = %#v", got)
	}
}

func TestDriveAPISetProfile(t *testing.T) {
	a := newActRig(t)
	a.ready()
	got, err := driveAPI(a).SetProfile(context.Background(), api.ArkProfileRequest{ArkDriveRequest: req(rid), Profile: "ark-drive-lite"})
	if err != nil || got != (api.ArkProfileResult{RootID: rid, Profile: "ark-drive-lite"}) {
		t.Fatalf("SetProfile = %+v, %v", got, err)
	}
	_, err = driveAPI(a).SetProfile(context.Background(), api.ArkProfileRequest{ArkDriveRequest: req(rid), Profile: "nope"})
	ae := wantAPIError(t, err, http.StatusUnprocessableEntity, doctor.CAT003)
	if ae.Details["profile"] != "nope" {
		t.Errorf("details = %v", ae.Details)
	}
}

// Eject accepts (nil) and refuses with LAI-ARK-009 and the blockers in the shape of the spec.
func TestDriveAPIEject(t *testing.T) {
	a := newActRig(t)
	a.ready()
	a.proc.add(4242, "bash", a.mnt+"/localai")
	err := driveAPI(a).Eject(context.Background(), api.ArkEjectRequest{ArkDriveRequest: req(rid)})
	ae := wantAPIError(t, err, http.StatusConflict, doctor.ARK009)
	b, _ := json.Marshal(ae.Details)
	if string(b) != `{"blockers":[{"kind":"process","name":"bash","pid":4242}],"root_id":"ark-3f9c0a12"}` {
		t.Errorf("details = %s", b)
	}

	a.proc.drop(4242)
	if err := driveAPI(a).Eject(context.Background(), api.ArkEjectRequest{ArkDriveRequest: req(rid)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	if err := a.acts.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

// Every error of the actions maps to its documented answer.
func TestDriveAPIErrorMapping(t *testing.T) {
	t.Run("an unknown drive is 404 for every action", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		d := driveAPI(a)
		ctx := context.Background()
		r := req("ark-deadbeef")
		_, e1 := d.Init(ctx, r)
		_, e2 := d.Mount(ctx, r)
		e3 := d.Eject(ctx, api.ArkEjectRequest{ArkDriveRequest: r})
		_, e4 := d.SetProfile(ctx, api.ArkProfileRequest{ArkDriveRequest: r, Profile: "ark-drive-full"})
		for _, err := range []error{e1, e2, e3, e4} {
			ae := wantAPIError(t, err, http.StatusNotFound, api.CodeNotFound)
			if ae.Details["root_id"] != "ark-deadbeef" {
				t.Errorf("details = %v", ae.Details)
			}
		}
	})
	t.Run("a wrong state is 409 LAI-AGT-020 with details.state", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		_, err := driveAPI(a).Init(context.Background(), req(rid))
		ae := wantAPIError(t, err, http.StatusConflict, api.CodeInvalidRequest)
		if ae.Details["state"] != "ready" || ae.Details["root_id"] != rid {
			t.Errorf("details = %v", ae.Details)
		}
		_, err = driveAPI(a).Mount(context.Background(), req(rid))
		if ae := wantAPIError(t, err, http.StatusConflict, api.CodeInvalidRequest); ae.Details["state"] != "ready" {
			t.Errorf("details = %v", ae.Details)
		}
	})
	t.Run("init when localai already has content", func(t *testing.T) {
		a := newActRig(t)
		a.uninitialized()
		writeFile(t, filepath.Join(a.mnt, "localai", "x"), "x")
		_, err := driveAPI(a).Init(context.Background(), req(rid))
		ae := wantAPIError(t, err, http.StatusConflict, api.CodeInvalidRequest)
		if ae.Details["state"] != "uninitialized" || ae.Details["reason"] != "localai_exists" {
			t.Errorf("details = %v", ae.Details)
		}
	})
	t.Run("another action is running: 409 LAI-AGT-022", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		a.mops.hold = make(chan struct{})
		a.mops.entered = make(chan struct{})
		if err := a.acts.Eject(context.Background(), rid, false); err != nil {
			t.Fatal(err)
		}
		<-a.mops.entered
		_, err := driveAPI(a).SetProfile(context.Background(), api.ArkProfileRequest{ArkDriveRequest: req(rid), Profile: "ark-drive-lite"})
		ae := wantAPIError(t, err, http.StatusConflict, api.CodeBusy)
		if ae.Details["operation"] != "eject" {
			t.Errorf("details = %v", ae.Details)
		}
		close(a.mops.hold)
		ctx, cancel := context.WithTimeout(context.Background(), 5e9)
		defer cancel()
		if err := a.acts.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a mount that fails is 409 LAI-ARK-005 with the reason", func(t *testing.T) {
		a := newActRig(t)
		a.notMounted(true)
		a.run.handle = func(execx.Cmd) (execx.Result, error) {
			return execx.Result{ExitCode: 1, Stderr: []byte("Error mounting /dev/sdb1: wrong fs type\n")}, nil
		}
		_, err := driveAPI(a).Mount(context.Background(), req(rid))
		ae := wantAPIError(t, err, http.StatusConflict, doctor.ARK005)
		want := map[string]any{"root_id": rid, "device": "/dev/sdb1", "reason": "mount_failed", "error": "Error mounting /dev/sdb1: wrong fs type"}
		if !reflect.DeepEqual(ae.Details, want) {
			t.Errorf("details = %v", ae.Details)
		}
	})
	t.Run("a missing udisksctl is told apart", func(t *testing.T) {
		a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.Udisksctl = "" })
		a.notMounted(true)
		_, err := driveAPI(a).Mount(context.Background(), req(rid))
		ae := wantAPIError(t, err, http.StatusConflict, doctor.ARK005)
		if ae.Details["reason"] != "udisksctl_unavailable" {
			t.Errorf("details = %v", ae.Details)
		}
	})
	t.Run("a read-only drive cannot change profile: 409 LAI-ARK-003", func(t *testing.T) {
		a := newActRig(t)
		a.initDrive(a.mnt, uuidA, "")
		a.mounts.set(a.rootDisk(), a.arkDisk(120, "8:17", a.mnt, "/dev/sdb1", uuidA, "ext4", "ro,relatime"))
		a.refresh()
		_, err := driveAPI(a).SetProfile(context.Background(), api.ArkProfileRequest{ArkDriveRequest: req(rid), Profile: "ark-drive-lite"})
		wantAPIError(t, err, http.StatusConflict, doctor.ARK003)
	})
	t.Run("no catalog is 409 LAI-CAT-001", func(t *testing.T) {
		a := newActRig(t, func(_ *actRig, o *ark.DriveActionsOptions) { o.Profiles = nil })
		a.ready()
		_, err := driveAPI(a).SetProfile(context.Background(), api.ArkProfileRequest{ArkDriveRequest: req(rid), Profile: "ark-drive-lite"})
		ae := wantAPIError(t, err, http.StatusConflict, doctor.CAT001)
		if ae.Details["reason"] != "no_catalog" {
			t.Errorf("details = %v", ae.Details)
		}
	})
	t.Run("a drive that cannot be written when it is set up", func(t *testing.T) {
		for _, tc := range []struct {
			errno  error
			code   string
			reason string
		}{
			{syscall.EROFS, doctor.ARK003, "read_only"},
			{syscall.ENOSPC, doctor.SYN040, "disk_full"},
			{syscall.EDQUOT, doctor.SYN040, "disk_full"},
			{syscall.EIO, doctor.ARK014, "io_error"},
		} {
			fs := newFS()
			a := newActRig(t, func(a *actRig, o *ark.DriveActionsOptions) {
				fs.mkdirErr[filepath.Join(a.mnt, "localai")] = errnoErr("mkdir", "localai", tc.errno)
				o.FS = fs
			})
			a.uninitialized()
			_, err := driveAPI(a).Init(context.Background(), req(rid))
			ae := wantAPIError(t, err, http.StatusConflict, tc.code)
			if ae.Details["reason"] != tc.reason {
				t.Errorf("%v: details = %v", tc.errno, ae.Details)
			}
		}
	})
	t.Run("an error it does not know is left as it is (a bug, 500)", func(t *testing.T) {
		a := newActRig(t)
		a.ready()
		a.user("router").set(nil, errors.New("the router crashed"))
		err := driveAPI(a).Eject(context.Background(), api.ArkEjectRequest{ArkDriveRequest: req(rid)})
		var ae *api.Error
		if err == nil || errors.As(err, &ae) {
			t.Errorf("err = %#v", err)
		}
	})
}
