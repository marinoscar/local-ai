package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func gpu(used int64) hardware.GPU {
	return hardware.GPU{Vendor: hardware.VendorNVIDIA, VRAMUsedMiB: used}
}

func TestSnapshotRefreshAndSet(t *testing.T) {
	f := New(hardware.Snapshot{GPUs: []hardware.GPU{gpu(1)}})
	f.QueueRefresh(hardware.Snapshot{GPUs: []hardware.GPU{gpu(2)}}, hardware.Snapshot{GPUs: []hardware.GPU{gpu(3)}})
	if f.Snapshot().GPUs[0].VRAMUsedMiB != 1 {
		t.Fatal("initial snapshot")
	}
	for i, want := range []int64{2, 3, 3} {
		if got := f.Refresh(context.Background()).GPUs[0].VRAMUsedMiB; got != want {
			t.Errorf("refresh %d = %d, want %d", i, got, want)
		}
	}
	if f.Refreshes() != 3 {
		t.Errorf("refreshes = %d", f.Refreshes())
	}
	f.Set(hardware.Snapshot{GPUs: []hardware.GPU{gpu(9)}})
	if f.Snapshot().GPUs[0].VRAMUsedMiB != 9 {
		t.Error("Set")
	}
}

func TestSampleGPUsScriptAndError(t *testing.T) {
	f := New(hardware.Snapshot{GPUs: []hardware.GPU{gpu(5)}})
	if g, err := f.SampleGPUs(context.Background()); err != nil || g[0].VRAMUsedMiB != 5 {
		t.Fatalf("default = %+v, %v", g, err)
	}
	f = New(hardware.Snapshot{})
	f.QueueGPUReads([]hardware.GPU{gpu(10)}, []hardware.GPU{gpu(20)})
	for i, want := range []int64{10, 20, 20} {
		g, err := f.SampleGPUs(context.Background())
		if err != nil || g[0].VRAMUsedMiB != want {
			t.Errorf("read %d = %+v, %v; want %d", i, g, err, want)
		}
	}
	f.FailGPUReads(errors.New("driver gone"))
	if _, err := f.SampleGPUs(context.Background()); err == nil {
		t.Error("expected the scripted error")
	}
}
