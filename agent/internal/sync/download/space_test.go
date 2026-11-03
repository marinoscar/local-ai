package download

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// ckptOf returns the saved checkpoint and checks its hash state covers exactly the .part prefix.
func ckptOf(t *testing.T, e *env, data []byte) Checkpoint {
	t.Helper()
	cp, ok := e.cp.Get(testName)
	if !ok {
		t.Fatal("no checkpoint was saved")
	}
	h, err := decodeSHA(cp.ShaState, cp.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if got := sumOf(h.Sum(nil)); got != sum(data[:cp.Offset]) {
		t.Errorf("the checkpoint's hash state does not cover the first %d bytes", cp.Offset)
	}
	return cp
}

func sumOf(b []byte) string { return sumHex(b) }

// ENOSPC at byte N: the .part is cut back to the last good offset, checkpointed there, the file is
// "skipped: disk", and the next run resumes from exactly that offset (acceptance criterion).
func TestFetchENOSPCAtByteN(t *testing.T) {
	data := genData(testSize, 20)
	for _, n := range []int64{1000, testBuf, 3*testBuf + 17, 50000, 100000, 199999} {
		t.Run(itoaN(n), func(t *testing.T) {
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			e.fs.setLimit(n, nil)
			res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
			var fe *Error
			if !errors.As(err, &fe) || fe.Kind != KindDisk || fe.Code != CodeDisk {
				t.Fatalf("err = %v, want a disk skip", err)
			}
			if res.Skipped != "disk" || res.Code != CodeDisk || res.Permanent {
				t.Errorf("Result = %+v", res)
			}
			lastGood := n / testBuf * testBuf
			if got := int64(len(e.readPart())); got != lastGood || res.Bytes != lastGood {
				t.Errorf(".part has %d bytes (Result.Bytes %d), want the last good offset %d", got, res.Bytes, lastGood)
			}
			if lastGood > 0 {
				if cp := ckptOf(t, e, data); cp.Offset != lastGood {
					t.Errorf("checkpoint offset = %d, want %d", cp.Offset, lastGood)
				}
			} else if _, ok := e.cp.Get(testName); ok {
				t.Error("a checkpoint at offset 0 must not exist")
			}
			if srv.count() != 1 {
				t.Errorf("requests = %d: a full disk is not retried", srv.count())
			}
			// The disk has room again: the next run resumes where this one stopped.
			e.fs.heal()
			res2 := mustFetch(t, e, e.job(srv.URL+"/f", data))
			if res2.ResumedFrom != lastGood || string(e.readPart()) != string(data) {
				t.Errorf("second run: %+v", res2)
			}
			if lastGood > 0 {
				if r := srv.requests()[1]; r.Range != "bytes="+itoaN(lastGood)+"-" {
					t.Errorf("resume Range = %q", r.Range)
				}
			}
		})
	}
}

func TestFetchPurgeMakesRoomMidDownload(t *testing.T) {
	data := genData(testSize, 21)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	p := &purger{free: 1 << 30, probe: e.probe, onRun: e.fs.heal}
	e.opts.Space = p
	e.rebuild()
	e.bucket.hook = func(total int64) {
		if total == 8*testBuf { // the other process fills the disk
			e.fs.setLimit(e.fs.total(), nil)
			e.probe.set(10)
		}
	}
	j := e.job(srv.URL+"/f", data)
	j.MinFreeBytes = 1000
	res := mustFetch(t, e, j)
	if p.count() != 1 || res.PurgedBytes != 1<<30 {
		t.Errorf("purges = %d, purged %d bytes", p.count(), res.PurgedBytes)
	}
	if srv.count() != 1 || string(e.readPart()) != string(data) {
		t.Errorf("requests = %d, or the file differs: purging must not restart the download", srv.count())
	}
}

func TestFetchStillNoRoomAfterPurgeSkips(t *testing.T) {
	data := genData(testSize, 22)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	p := &purger{} // nothing eligible
	e.opts.Space = p
	e.rebuild()
	e.bucket.hook = func(total int64) {
		if total == 6*testBuf {
			e.fs.setLimit(e.fs.total(), nil)
			e.probe.set(10)
		}
	}
	j := e.job(srv.URL+"/f", data)
	j.MinFreeBytes = 1000
	res, err := e.dl.Fetch(context.Background(), j, e.cp)
	if !errors.Is(err, ErrDisk) || res.Skipped != "disk" {
		t.Fatalf("err = %v, Result %+v", err, res)
	}
	if p.count() != 1 || p.calls[0] != 1000+testBuf-10 {
		t.Errorf("purge calls = %v, want one asking for the %d bytes short", p.calls, 1000+testBuf-10)
	}
	if got := len(e.readPart()); got != 6*testBuf {
		t.Errorf(".part has %d bytes, want 6 blocks kept", got)
	}
	if cp := ckptOf(t, e, data); cp.Offset != 6*testBuf {
		t.Errorf("checkpoint = %d", cp.Offset)
	}
}

func TestFetchPreStartSpaceCheck(t *testing.T) {
	data := genData(testSize, 23)
	for _, tc := range []struct {
		name      string
		free      int64
		minFree   int64
		purge     *purger
		wantSkip  bool
		wantCalls int
		wantNeed  int64
	}{
		{name: "room", free: 1 << 20, minFree: 100 << 10},
		{name: "exactly enough", free: testSize + 100<<10, minFree: 100 << 10},
		{name: "one byte short, nothing to purge", free: testSize + 100<<10 - 1, minFree: 100 << 10, purge: &purger{}, wantSkip: true, wantCalls: 1, wantNeed: 1},
		{name: "no room and no SpaceMaker", free: 1000, minFree: 0, wantSkip: true},
		{name: "purging the trash makes room", free: 1000, minFree: 0, purge: &purger{free: 1 << 30}, wantCalls: 1, wantNeed: testSize - 1000},
		{name: "purging frees too little", free: 1000, minFree: 0, purge: &purger{free: 5000}, wantSkip: true, wantCalls: 1, wantNeed: testSize - 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			srv := newTestServer(t, data)
			e.probe.set(tc.free)
			if tc.purge != nil {
				tc.purge.probe = e.probe
				e.opts.Space = tc.purge
				e.rebuild()
			}
			j := e.job(srv.URL+"/f", data)
			j.MinFreeBytes = tc.minFree
			res, err := e.dl.Fetch(context.Background(), j, e.cp)
			if tc.wantSkip {
				var fe *Error
				if !errors.As(err, &fe) || fe.Kind != KindDisk || fe.Code != CodeDisk || res.Skipped != "disk" {
					t.Fatalf("err = %v, Result %+v", err, res)
				}
				if srv.count() != 0 {
					t.Errorf("made %d requests for a download that cannot fit", srv.count())
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.purge != nil {
				if tc.purge.count() != tc.wantCalls {
					t.Fatalf("purge calls = %d, want %d", tc.purge.count(), tc.wantCalls)
				}
				if tc.wantCalls > 0 && tc.purge.calls[0] != tc.wantNeed {
					t.Errorf("purge asked for %d bytes, want %d", tc.purge.calls[0], tc.wantNeed)
				}
			}
		})
	}
}

func TestEnsureSpaceIsTheEngineHook(t *testing.T) {
	e := newEnv(t, nil)
	e.probe.set(10 << 20)
	if purged, err := e.dl.EnsureSpace(context.Background(), testRoot, e.root, 5<<20, 1<<20); err != nil || purged != 0 {
		t.Fatalf("EnsureSpace = %d, %v", purged, err)
	}
	if _, err := e.dl.EnsureSpace(context.Background(), testRoot, e.root, 10<<20, 1<<20); !errors.Is(err, ErrDisk) {
		t.Errorf("err = %v, want a disk skip", err)
	}
	e.probe.err = syscall.EIO
	if _, err := e.dl.EnsureSpace(context.Background(), testRoot, e.root, 1, 0); !errors.Is(err, syscall.EIO) {
		t.Errorf("an I/O error of the probe must surface, got %v", err)
	}
	e.probe.err = errors.New("statfs unavailable")
	if _, err := e.dl.EnsureSpace(context.Background(), testRoot, e.root, 1, 0); err != nil {
		t.Errorf("an unknown free space must not block the download: %v", err)
	}
}

func TestFetchInFlightLowSpaceSkipsAndKeepsPart(t *testing.T) {
	data := genData(testSize, 24)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	e.bucket.hook = func(total int64) {
		if total == 10*testBuf { // another process takes the space
			e.probe.set(1000)
		}
	}
	j := e.job(srv.URL+"/f", data)
	j.MinFreeBytes = 5000
	res, err := e.dl.Fetch(context.Background(), j, e.cp)
	if !errors.Is(err, ErrDisk) || res.Skipped != "disk" {
		t.Fatalf("err = %v, Result %+v", err, res)
	}
	// The re-check runs every SpaceRecheckEvery (64 KiB in the test environment): the first one
	// after the drop is at 64 KiB.
	if res.Bytes != 64<<10 {
		t.Errorf("stopped at %d, want the first re-check at %d", res.Bytes, 64<<10)
	}
	if got := len(e.readPart()); int64(got) != res.Bytes {
		t.Errorf(".part has %d bytes", got)
	}
	if cp := ckptOf(t, e, data); cp.Offset != res.Bytes {
		t.Errorf("checkpoint = %d, want %d", cp.Offset, res.Bytes)
	}
}

func TestFetchRootFaults(t *testing.T) {
	data := genData(testSize, 25)
	for _, tc := range []struct {
		name         string
		err          error
		wantCode     string
		wantReadOnly bool
		wantStopped  bool // all further writes to the root are refused
	}{
		{"EIO on a drive root", syscall.EIO, CodeIO, false, true},
		{"EROFS: the drive was remounted read-only", syscall.EROFS, CodeReadOnly, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var faults []string
			var readOnly bool
			e := newEnv(t, func(o *Options) {
				o.OnRootFault = func(rootID string, err *Error, ro bool) { faults = append(faults, rootID+" "+err.Code); readOnly = ro }
			})
			srv := newTestServer(t, data)
			e.fs.setLimit(3*testBuf+100, tc.err)
			res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
			var fe *Error
			if !errors.As(err, &fe) || fe.Kind != KindRootFault || fe.Code != tc.wantCode || res.Code != tc.wantCode {
				t.Fatalf("err = %v, Result %+v; want a root fault %s", err, res, tc.wantCode)
			}
			if len(faults) != 1 || faults[0] != testRoot+" "+tc.wantCode || readOnly != tc.wantReadOnly {
				t.Errorf("OnRootFault calls = %v (readOnly %v)", faults, readOnly)
			}
			// All writes to the root stop: a second file of the same root is refused at once.
			before := srv.count()
			j2 := e.job(srv.URL+"/other", data)
			j2.Name = "other.bin"
			if _, err := e.dl.Fetch(context.Background(), j2, e.cp); !errors.Is(err, ErrRootFault) {
				t.Errorf("a fetch on the faulted root: %v", err)
			}
			if srv.count() != before {
				t.Error("a request was made for a root that cannot be written")
			}
			if _, err := os.Stat(j2.PartPath()); err == nil {
				t.Error("a file was created on the faulted root")
			}
			// Other roots are not affected.
			j3 := e.job(srv.URL+"/f", data)
			j3.RootID, j3.Root = "ark-0a1b2c3d", t.TempDir()
			e.fs.heal()
			if _, err := e.dl.Fetch(context.Background(), j3, &MemoryCheckpointer{}); err != nil {
				t.Errorf("another root must be unaffected: %v", err)
			}
			// Once the caller dealt with the fault, the root is writable again and the file resumes.
			e.dl.ClearRootFault(testRoot)
			res2 := mustFetch(t, e, e.job(srv.URL+"/f", data))
			if string(e.readPart()) != string(data) || res2.Code != "" {
				t.Errorf("after the fault was cleared: %+v", res2)
			}
		})
	}
}

func TestFetchUnwritableRoot(t *testing.T) {
	data := genData(testSize, 26)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	e.fs.mkdirErr = syscall.EACCES
	res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindRootFault || fe.Code != CodeNoWritableRoot || res.Code != CodeNoWritableRoot {
		t.Fatalf("err = %v, Result %+v", err, res)
	}
	if srv.count() != 0 {
		t.Error("a request was made for an unwritable root")
	}
	e.fs.mkdirErr = syscall.ENOSPC
	if _, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp); !errors.Is(err, ErrDisk) {
		t.Errorf("a full disk while creating the directory is a disk skip, got %v", err)
	}
	e.fs.mkdirErr = nil
	mustFetch(t, e, e.job(srv.URL+"/f", data))
}

// The same condition with a real permission bit (skipped as root, which ignores it; CI runs the
// package as an unprivileged user too).
func TestFetchRealUnwritableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	data := genData(1000, 27)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	e := newEnv(t, func(o *Options) { o.FS = OSFS{} })
	e.root = dir
	srv := newTestServer(t, data)
	_, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeNoWritableRoot {
		t.Fatalf("err = %v, want %s", err, CodeNoWritableRoot)
	}
}

func TestDriveMinFree(t *testing.T) {
	for _, tc := range []struct{ total, want int64 }{
		{0, 5_000_000_000},
		{100_000_000_000, 5_000_000_000},
		{250_000_000_000, 5_000_000_000},
		{251_000_000_000, 5_020_000_000},
		{4_000_000_000_000, 80_000_000_000},
	} {
		if got := DriveMinFree(tc.total); got != tc.want {
			t.Errorf("DriveMinFree(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

// The downloader never deletes. Its filesystem interface has no way to, and its sources do not
// call os.Remove, os.RemoveAll or an unlink (the static check of ark-sync.md section 9.1).
func TestTheDownloaderCannotDelete(t *testing.T) {
	ft := reflect.TypeOf((*FS)(nil)).Elem()
	for i := 0; i < ft.NumMethod(); i++ {
		if n := ft.Method(i).Name; strings.HasPrefix(n, "Remove") || strings.HasPrefix(n, "Unlink") || strings.HasPrefix(n, "Delete") {
			t.Errorf("FS has a deleting method %s", n)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseFile(fset, f, src, parser.ParseComments); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"os.Remove", "os.RemoveAll", "unix.Unlink", "syscall.Unlink", "os.Truncate("} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s uses %s", f, banned)
			}
		}
	}
}
