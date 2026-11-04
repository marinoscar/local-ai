//go:build !linux

package gps

import (
	"errors"
	"io"
)

// OpenSerial is only implemented on Linux, the only platform the agent runs on.
func OpenSerial(Device) (io.ReadCloser, error) { return nil, errors.New("gps: serial is Linux only") }
