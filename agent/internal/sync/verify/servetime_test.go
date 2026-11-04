package verify

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

func (r *rig) serve() *ServeVerifier {
	v, err := NewServeVerifier(ServeDeps{FS: r.fs, Roots: r.rootList, Index: r.idx, Journal: r.jr, Logger: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func uncleanRig(t *testing.T) (*rig, string) {
	r := newRig(t)
	dir := r.install(driveID, "qwen-x", "v1", epoch.Add(-days(60)))
	r.install(driveID, "other", "v1", epoch.Add(-days(60)))
	r.setRoot(driveID, func(ro *ark.Root) { ro.State = ark.StateUnclean })
	return r, dir
}

func TestServeTimeAGoodCopyOnAnUncleanRootIsHashedOnceThenServed(t *testing.T) {
	r, dir := uncleanRig(t)
	v := r.serve()
	if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("an unverified copy on an unclean root must be invisible")
	}
	before := snapshot(t, r.root(driveID).Path)

	if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); err != nil {
		t.Fatal(err)
	}
	if c, err := r.idx.BestCopy("qwen-x"); err != nil || c.RootID != driveID {
		t.Fatalf("after the check the copy must be served: %v", err)
	}
	if _, err := r.idx.BestCopy("other"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("verifying one item must not verify its neighbour")
	}
	// The second ask is answered from the mount's memory: the file is not read again.
	f := filepath.Join(dir, "w.gguf")
	opens := r.fs.opened(f)
	for i := 0; i < 3; i++ {
		if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); err != nil {
			t.Fatal(err)
		}
	}
	if r.fs.opened(f) != opens {
		t.Fatalf("the file was read %d more times", r.fs.opened(f)-opens)
	}
	if after := snapshot(t, r.root(driveID).Path); !equal(before, after) {
		t.Fatal("serve-time verification wrote to the drive")
	}
}

func TestServeTimeADamagedCopyIsExcludedInMemoryAndNeverServed(t *testing.T) {
	r, dir := uncleanRig(t)
	v := r.serve()
	rot(t, filepath.Join(dir, "w.gguf"))
	before := snapshot(t, r.root(driveID).Path)

	err := v.VerifyBeforeServe(bg, driveID, "qwen-x")
	var damaged *DamagedError
	if !errors.As(err, &damaged) || damaged.Code() != doctor.SYN025 || damaged.Version != "v1" || damaged.Bad.Reason != BadChecksum {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.idx.BestCopy("qwen-x"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatal("a damaged copy is served")
	}
	if !r.idx.Excluded(driveID, "qwen-x", "v1") {
		t.Fatal("not excluded in memory")
	}
	if rec, ok := r.jr.Get(driveID, "qwen-x"); !ok || rec.State != journal.StateQuarantined || rec.Code != doctor.SYN025 {
		t.Fatalf("journal = %+v", rec)
	}
	if after := snapshot(t, r.root(driveID).Path); !equal(before, after) {
		t.Fatal("a drive that may be dying was written to")
	}
	// The verdict is remembered for the life of the mount; unmounting forgets it.
	f := filepath.Join(dir, "w.gguf")
	opens := r.fs.opened(f)
	if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); !errors.As(err, &damaged) {
		t.Fatalf("second ask: %v", err)
	}
	if r.fs.opened(f) != opens {
		t.Fatal("the damaged copy was hashed again")
	}
	v.Unmounted(driveID)
	if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); !errors.As(err, &damaged) || r.fs.opened(f) == opens {
		t.Fatalf("after an unmount the copy must be judged again: %v", err)
	}
}

func TestServeTimeOnlyUncleanRootsAreHashed(t *testing.T) {
	r := newRig(t)
	dir := r.install(driveID, "qwen-x", "v1", epoch.Add(-days(60)))
	v := r.serve()
	f := filepath.Join(dir, "w.gguf")
	if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); err != nil || r.fs.opened(f) != 0 {
		t.Fatalf("a clean root costs nothing: %v, %d reads", err, r.fs.opened(f))
	}
	if err := v.VerifyBeforeServe(bg, "ark-ffffffff", "qwen-x"); !errors.Is(err, ErrNotOnRoot) {
		t.Fatalf("an unknown root: %v", err)
	}
	r.setRoot(driveID, func(ro *ark.Root) { ro.State = ark.StateUnclean })
	if err := v.VerifyBeforeServe(bg, driveID, "nothing-here"); !errors.Is(err, ErrNotOnRoot) {
		t.Fatalf("an item the root does not hold: %v", err)
	}
}

func TestServeTimeACancelledCheckIsNotRemembered(t *testing.T) {
	r, dir := uncleanRig(t)
	v := r.serve()
	ctx, cancel := context.WithCancel(bg)
	r.fs.cancel = cancel
	r.fs.midFile = filepath.Join(dir, "w.gguf")
	if err := v.VerifyBeforeServe(ctx, driveID, "qwen-x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	r.fs.midFile = ""
	if err := v.VerifyBeforeServe(bg, driveID, "qwen-x"); err != nil {
		t.Fatalf("a cancelled check must not leave a verdict: %v", err)
	}
}

func TestServeTimeAskersOfTheSameCopyShareOneRead(t *testing.T) {
	r, dir := uncleanRig(t)
	v := r.serve()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- v.VerifyBeforeServe(bg, driveID, "qwen-x")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := r.fs.opened(filepath.Join(dir, "w.gguf")); n != 1 {
		t.Fatalf("the file was read %d times", n)
	}
}
