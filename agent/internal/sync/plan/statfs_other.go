//go:build !linux

package plan

import "errors"

// OSStatfs is unavailable off Linux (the agent only runs on Linux; this keeps other platforms building).
func OSStatfs(string) (int64, int64, error) {
	return 0, 0, errors.New("plan: statfs is only supported on linux")
}
