package acestep

import (
	"context"
	"strconv"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Doctor check ids of the music runtime.
const (
	CheckRuntime = "svc.music_runtime"
	CheckDriver  = "svc.music_driver"
)

// CheckOptions configures the doctor checks. Wanted is required.
type CheckOptions struct {
	// Wanted says whether this node wants music: the profile or node.yaml includes an acestep model
	// (in include.models, add_models or a workload). A node without music has nothing to report.
	Wanted func() bool
	// Runtime answers whether the runtime is installed for this backend.
	Runtime Runtime
	// Driver returns the NVIDIA driver version of the GPU ("" when there is none or it is unknown).
	Driver func() string
}

// DoctorChecks returns the fast checks of the music runtime (music.md section 3.1):
//
//   - LAI-SVC-150: music is wanted and the runtime is missing, damaged, or built for another backend;
//   - LAI-SVC-159: music is wanted and the NVIDIA driver is older than MinDriverMajor.
//
// The caller registers them.
func DoctorChecks(o CheckOptions) []doctor.Check {
	wanted := func() bool { return o.Wanted != nil && o.Wanted() }
	return []doctor.Check{
		{ID: CheckRuntime, Area: "SVC", Fast: true, Run: func(context.Context) []doctor.Result {
			if !wanted() {
				return nil
			}
			if _, err := o.Runtime.Current(); err != nil {
				return []doctor.Result{{Code: doctor.SVC150, Details: map[string]any{"root": o.Runtime.root(), "reason": clip(err.Error(), 200)}}}
			}
			return nil
		}},
		{ID: CheckDriver, Area: "SVC", Fast: true, Run: func(context.Context) []doctor.Result {
			if !wanted() || o.Driver == nil {
				return nil
			}
			if v := o.Driver(); DriverTooOld(v) {
				return []doctor.Result{{Code: doctor.SVC159, Details: map[string]any{"driver": v, "min_major": strconv.Itoa(MinDriverMajor)}}}
			}
			return nil
		}},
	}
}
