//go:build !linux

package execx

import "errors"

// setRlimit is only implemented on Linux, the one platform the agent runs on; elsewhere a limit
// cannot be enforced, so the program is refused rather than run unlimited.
func setRlimit(int, Rlimit) error { return errors.New("resource limits need Linux") }
