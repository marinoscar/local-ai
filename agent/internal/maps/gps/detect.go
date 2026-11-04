package gps

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SysClassTTY is the sysfs directory the allowlist scan reads.
const SysClassTTY = "/sys/class/tty"

// uBloxVendor is the USB vendor id of u-blox receivers.
const uBloxVendor = "1546"

var (
	ttyNameRe = regexp.MustCompile(`^tty(ACM|USB)[0-9]+$`)
	// devicePathRe is what an explicit nmea:<device> may name: a serial tty under /dev.
	devicePathRe = regexp.MustCompile(`^/dev/tty(ACM|USB|S|AMA)[0-9]+$`)
)

// Device is a serial device that may be a GPS receiver.
type Device struct {
	// Path is /dev/<name>.
	Path string
	// USB is true for a ttyUSB device (9600 8N1 is set); a ttyACM device ignores the baud rate.
	USB bool
}

// ValidDevicePath reports whether p is acceptable in node.yaml as nmea:<p>.
func ValidDevicePath(p string) bool { return devicePathRe.MatchString(p) }

// DeviceForPath builds the Device of an explicit nmea:<device> choice.
func DeviceForPath(p string) Device {
	return Device{Path: p, USB: strings.HasPrefix(filepath.Base(p), "ttyUSB")}
}

// Detect lists the ttyACM and ttyUSB devices under sysClassTTY that belong to a USB device with vendor
// id 1546 (u-blox) or a product string containing GPS or GNSS. Any other serial device is never
// returned, so `auto` never opens it (a modem, a printer or a microcontroller would be disturbed).
// Entries are sorted by name (os.ReadDir order).
func Detect(sysClassTTY string) []Device {
	entries, err := os.ReadDir(sysClassTTY)
	if err != nil {
		return nil
	}
	var out []Device
	for _, e := range entries {
		name := e.Name()
		if !ttyNameRe.MatchString(name) {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(sysClassTTY, name))
		if err != nil {
			continue
		}
		vendor, product, ok := usbIdentity(real)
		if !ok || !(vendor == uBloxVendor || isGNSSProduct(product)) {
			continue
		}
		out = append(out, Device{Path: "/dev/" + name, USB: strings.HasPrefix(name, "ttyUSB")})
	}
	return out
}

func isGNSSProduct(product string) bool {
	p := strings.ToUpper(product)
	return strings.Contains(p, "GPS") || strings.Contains(p, "GNSS")
}

// usbIdentity walks up from the resolved tty directory to the USB device directory (the first
// ancestor with an idVendor file) and reads idVendor and product. ok is false for a tty that is not
// behind USB (a built-in UART, a virtual terminal).
func usbIdentity(dir string) (vendor, product string, ok bool) {
	for i := 0; i < 8; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "idVendor")); err == nil {
			vendor = strings.ToLower(strings.TrimSpace(string(b)))
			if p, err := os.ReadFile(filepath.Join(dir, "product")); err == nil {
				product = strings.TrimSpace(string(p))
			}
			return vendor, product, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}
