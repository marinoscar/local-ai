package whisper

import (
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

// FailedStartsBeforeCPU is how many failed starts on the gpu switch the device to cpu for this boot
// (companion-services.md section 5, LAI-SVC-013).
const FailedStartsBeforeCPU = 2

// StableFor is how long the unit must stay active for earlier failures to be forgotten.
const StableFor = 30 * time.Second

// Fallback counts failed gpu starts of the speech unit in memory. It is per boot: a new process starts
// at zero, so a fixed driver is tried again after a reboot.
type Fallback struct {
	mu          sync.Mutex
	failures    int
	forced      bool
	lastFailing bool
}

// ForceCPU reports whether the device was switched to cpu for this boot.
func (f *Fallback) ForceCPU() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forced
}

// Failures is the current count of failed gpu starts.
func (f *Fallback) Failures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures
}

// Observe feeds one state of the unit. device is the device the running env selects. A failed start is
// the unit entering failed or auto-restart (it exited within its starting window) while device is gpu;
// each entry counts once. A unit active for StableFor clears the count. It returns true exactly once,
// when the count reaches FailedStartsBeforeCPU and the device must be switched to cpu.
func (f *Fallback) Observe(st services.UnitState, device string, now time.Time) (switched bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forced || device != DeviceGPU {
		return false
	}
	failing := st.ActiveState == services.ActiveFailed || st.SubState == "auto-restart"
	if failing && !f.lastFailing {
		f.failures++
		if f.failures >= FailedStartsBeforeCPU {
			f.forced = true
			f.lastFailing = failing
			return true
		}
	}
	f.lastFailing = failing
	if st.Active() && !st.ActiveEnterTimestamp.IsZero() && now.Sub(st.ActiveEnterTimestamp) >= StableFor {
		f.failures = 0
	}
	return false
}
