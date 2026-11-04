// Package gps reads a position from gpsd or from a USB NMEA receiver (companion-services.md section
// 4.4.5, issue SVC-12). It never logs a coordinate and never sends one anywhere: the position stays
// in memory and is served on loopback by GET /maps/whereami.
//
// Everything that touches the world sits behind an interface (a net dialer, a serial opener, a
// sysfs root), so the unit tests use fixtures and fakes: no real serial port or gpsd runs in them.
package gps

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Fix is one position report.
type Fix struct {
	Lat, Lon float64
	// AccuracyM is the estimated horizontal error in metres; 0 when the receiver gave none.
	AccuracyM float64
	// Mode is 2 for a 2D fix and 3 for a 3D fix.
	Mode int
	// At is when the agent received the fix (the agent's clock, not the receiver's).
	At time.Time
}

// uere is the user equivalent range error, in metres, that turns an HDOP into a rough accuracy.
const uere = 5.0

// nmeaChecksumOK validates "$....*HH": the XOR of the characters between $ and * equals HH.
func nmeaChecksumOK(line string) (body string, ok bool) {
	if len(line) < 5 || line[0] != '$' {
		return "", false
	}
	star := strings.LastIndexByte(line, '*')
	if star < 0 || len(line)-star-1 < 2 {
		return "", false
	}
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	if err != nil {
		return "", false
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= line[i]
	}
	if sum != byte(want) {
		return "", false
	}
	return line[1:star], true
}

// parseCoord turns "ddmm.mmmm" / "dddmm.mmmm" and a hemisphere letter into signed degrees.
func parseCoord(v, hemi string, degDigits int) (float64, bool) {
	if len(v) < degDigits+2 || len(hemi) != 1 {
		return 0, false
	}
	deg, err := strconv.ParseFloat(v[:degDigits], 64)
	if err != nil {
		return 0, false
	}
	min, err := strconv.ParseFloat(v[degDigits:], 64)
	if err != nil || min < 0 || min >= 60 {
		return 0, false
	}
	d := deg + min/60
	switch hemi {
	case "N", "E":
	case "S", "W":
		d = -d
	default:
		return 0, false
	}
	return d, true
}

// NMEA turns sentences into fixes. A GGA sentence carries the quality, the HDOP and the altitude; an
// RMC sentence carries the validity flag. The two arrive together once a second, so an RMC keeps the
// mode and accuracy of a GGA that arrived less than gga ago.
type NMEA struct {
	lastGGA Fix
	hasGGA  bool
}

const ggaMemory = 5 * time.Second

// Feed parses one line (with or without the line ending). It returns the fix of a valid RMC or GGA
// sentence; a bad checksum, another sentence, or a sentence without a fix returns ok=false.
func (n *NMEA) Feed(line string, now time.Time) (Fix, bool) {
	body, ok := nmeaChecksumOK(strings.TrimSpace(line))
	if !ok {
		return Fix{}, false
	}
	f := strings.Split(body, ",")
	id := f[0]
	if len(id) != 5 || id[0] != 'G' {
		return Fix{}, false
	}
	switch id[2:] {
	case "RMC":
		return n.rmc(f, now)
	case "GGA":
		return n.gga(f, now)
	}
	return Fix{}, false
}

// rmc: $GPRMC,time,A,lat,N,lon,E,speed,course,date,...
func (n *NMEA) rmc(f []string, now time.Time) (Fix, bool) {
	if len(f) < 7 || f[2] != "A" {
		return Fix{}, false
	}
	lat, ok1 := parseCoord(f[3], f[4], 2)
	lon, ok2 := parseCoord(f[5], f[6], 3)
	if !ok1 || !ok2 || !inRange(lat, lon) {
		return Fix{}, false
	}
	fix := Fix{Lat: lat, Lon: lon, Mode: 2, At: now}
	if n.hasGGA && now.Sub(n.lastGGA.At) >= 0 && now.Sub(n.lastGGA.At) < ggaMemory {
		fix.Mode, fix.AccuracyM = n.lastGGA.Mode, n.lastGGA.AccuracyM
	}
	return fix, true
}

// gga: $GPGGA,time,lat,N,lon,E,quality,sats,hdop,alt,M,...
func (n *NMEA) gga(f []string, now time.Time) (Fix, bool) {
	if len(f) < 10 {
		return Fix{}, false
	}
	if q, err := strconv.Atoi(f[6]); err != nil || q < 1 {
		return Fix{}, false
	}
	lat, ok1 := parseCoord(f[2], f[3], 2)
	lon, ok2 := parseCoord(f[4], f[5], 3)
	if !ok1 || !ok2 || !inRange(lat, lon) {
		return Fix{}, false
	}
	fix := Fix{Lat: lat, Lon: lon, Mode: 2, At: now}
	if sats, err := strconv.Atoi(f[7]); err == nil && sats >= 4 && f[9] != "" {
		fix.Mode = 3
	}
	if h, err := strconv.ParseFloat(f[8], 64); err == nil && h > 0 {
		fix.AccuracyM = math.Round(h * uere)
	}
	n.lastGGA, n.hasGGA = fix, true
	return fix, true
}

func inRange(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
