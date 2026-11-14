package acestep

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// BuildFile is the name of the runtime's record next to venv: /opt/localai/acestep/<version>/BUILD.json
// (music.md section 3.1).
const BuildFile = "BUILD.json"

// Build is BUILD.json: what the runtime in a version directory was built from.
type Build struct {
	Version    string `json:"version"`
	Backend    string `json:"backend"`
	Python     string `json:"python"`
	LockSHA256 string `json:"lock_sha256"`
	BuiltAt    string `json:"built_at"`
}

// VersionRe is the form of a runtime version ("1.5.0", "1.5.0-2"): a plain directory name.
var VersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)

// ReadBuild reads BUILD.json of the version dir (a path such as /opt/localai/acestep/current).
func ReadBuild(dir string) (Build, error) {
	b, err := os.ReadFile(filepath.Join(dir, BuildFile))
	if err != nil {
		return Build{}, err
	}
	var out Build
	if err := json.Unmarshal(b, &out); err != nil {
		return Build{}, fmt.Errorf("acestep: %s: %w", filepath.Join(dir, BuildFile), err)
	}
	if !VersionRe.MatchString(out.Version) || out.Backend == "" {
		return Build{}, fmt.Errorf("acestep: %s names no usable version or backend", filepath.Join(dir, BuildFile))
	}
	return out, nil
}

// Runtime answers "is the music runtime installed for this backend" (music.md section 3.1).
type Runtime struct {
	// Root is /opt/localai/acestep; tests use a temporary directory.
	Root string
	// Backend returns the node's backend (llama.backend); nil or "" means any backend will do.
	Backend func() string
}

func (r Runtime) root() string {
	if r.Root == "" {
		return RuntimeRoot
	}
	return r.Root
}

// Current is the build in use, and why there is none: the interpreter missing or not a program, or
// BUILD.json missing, damaged or for another backend.
func (r Runtime) Current() (Build, error) {
	cur := filepath.Join(r.root(), "current")
	if err := RuntimeInstalled(nil, filepath.Join(cur, "venv", "bin", "python")); err != nil {
		return Build{}, err
	}
	b, err := ReadBuild(cur)
	if err != nil {
		return Build{}, err
	}
	if r.Backend != nil {
		if want := strings.TrimSpace(r.Backend()); want != "" && b.Backend != want {
			return b, fmt.Errorf("acestep: the music runtime is built for %s, this computer uses %s", b.Backend, want)
		}
	}
	return b, nil
}

// Installed is nil when Current has a build; it is the adapter's Installed.
func (r Runtime) Installed() error {
	_, err := r.Current()
	return err
}

// MinDriverMajor is the oldest NVIDIA driver branch the music runtime's CUDA wheels run on.
// TODO(#505): the spike measures the bundle's CUDA version; until then this is the CUDA 12.4 floor.
const MinDriverMajor = 550

// DriverTooOld reports whether an NVIDIA driver version ("595.45.04") is older than MinDriverMajor. A
// version that cannot be read is not judged (false).
func DriverTooOld(version string) bool {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return false
	}
	return n < MinDriverMajor
}
