//go:build linux

package execx

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// setRlimit applies one limit to a running child with prlimit(2). The limit lands a few
// microseconds after the program started, which matters for none of the three resources: address
// space and open files bound what the program asks for from then on, and CPU time counts from its
// start. Go's os/exec has no way to set a limit between fork and exec.
func setRlimit(pid int, rl Rlimit) error {
	var res int
	switch rl.Resource {
	case RlimitAS:
		res = unix.RLIMIT_AS
	case RlimitCPU:
		res = unix.RLIMIT_CPU
	case RlimitNOFILE:
		res = unix.RLIMIT_NOFILE
	default:
		return fmt.Errorf("unknown resource %d", rl.Resource)
	}
	return unix.Prlimit(pid, res, &unix.Rlimit{Cur: rl.Limit, Max: rl.Limit}, nil)
}
