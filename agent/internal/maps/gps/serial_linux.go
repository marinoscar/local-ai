//go:build linux

package gps

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// OpenSerial opens a serial device read-only without making it the controlling terminal, puts it in
// raw mode (no echo, no CR/LF translation) and, for a ttyUSB device, sets 9600 8N1. A ttyACM device
// ignores the baud rate. The descriptor is non-blocking so closing the file wakes a pending read.
func OpenSerial(d Device) (io.ReadCloser, error) {
	fd, err := unix.Open(d.Path, unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: d.Path, Err: err}
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "tcgetattr", Path: d.Path, Err: err}
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	if d.USB {
		t.Cflag &^= unix.CBAUD
		t.Cflag |= unix.B9600
		t.Ispeed, t.Ospeed = unix.B9600, unix.B9600
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "tcsetattr", Path: d.Path, Err: err}
	}
	return os.NewFile(uintptr(fd), d.Path), nil
}
