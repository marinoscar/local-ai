//go:build !linux

package hardware

import "errors"

// osStatfs is unavailable off Linux (the agent only runs on Linux; this keeps other platforms building).
func osStatfs(string) (int64, int64, error) {
	return 0, 0, errors.New("hardware: statfs is only supported on linux")
}
