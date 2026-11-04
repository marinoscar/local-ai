// Package maps holds the agent's side of the offline map: the "Where am I" service and its routes
// (companion-services.md section 4.4.5). The tile and search handlers belong to other issues.
package maps

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ErrCoordinates is returned for text that is not a coordinate pair.
var ErrCoordinates = errors.New("not a coordinate pair: use decimal \"9.93, -84.08\" or degrees-minutes-seconds \"9°55'48\\\"N 84°4'48\\\"W\"")

var (
	decimalRe = regexp.MustCompile(`^\s*([+-]?\d+(?:\.\d+)?)\s*[,;\s]\s*([+-]?\d+(?:\.\d+)?)\s*$`)
	// dmsPartRe is one DMS value: degrees, optional minutes and seconds, and a hemisphere letter.
	dmsPartRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*°\s*(?:(\d+(?:\.\d+)?)\s*['′]\s*)?(?:(\d+(?:\.\d+)?)\s*(?:"|″|'')\s*)?([NSEWnsew])`)
)

// ParseCoordinates reads a position typed by a person: decimal degrees ("9.93, -84.08", comma,
// semicolon or space separated) or degrees-minutes-seconds with hemisphere letters
// ("9°55'48\"N 84°4'48\"W"; the minutes and seconds are optional). The result is validated to
// -90..90 and -180..180.
func ParseCoordinates(text string) (lat, lon float64, err error) {
	text = strings.TrimSpace(text)
	if m := decimalRe.FindStringSubmatch(text); m != nil {
		lat, _ = strconv.ParseFloat(m[1], 64)
		lon, _ = strconv.ParseFloat(m[2], 64)
		return lat, lon, validate(lat, lon)
	}
	parts := dmsPartRe.FindAllStringSubmatch(text, -1)
	if len(parts) != 2 {
		return 0, 0, ErrCoordinates
	}
	// Nothing but separators may stand between and around the two values.
	if rest := strings.TrimSpace(dmsPartRe.ReplaceAllString(text, "")); strings.Trim(rest, ",; ") != "" {
		return 0, 0, ErrCoordinates
	}
	var got [2]float64
	var hemi [2]byte
	for i, p := range parts {
		deg, _ := strconv.ParseFloat(p[1], 64)
		var min, sec float64
		if p[2] != "" {
			min, _ = strconv.ParseFloat(p[2], 64)
		}
		if p[3] != "" {
			sec, _ = strconv.ParseFloat(p[3], 64)
		}
		if min >= 60 || sec >= 60 {
			return 0, 0, ErrCoordinates
		}
		v := deg + min/60 + sec/3600
		h := strings.ToUpper(p[4])[0]
		if h == 'S' || h == 'W' {
			v = -v
		}
		got[i], hemi[i] = v, h
	}
	isLat := func(h byte) bool { return h == 'N' || h == 'S' }
	switch {
	case isLat(hemi[0]) && !isLat(hemi[1]):
		lat, lon = got[0], got[1]
	case !isLat(hemi[0]) && isLat(hemi[1]):
		lat, lon = got[1], got[0]
	default:
		return 0, 0, ErrCoordinates
	}
	return lat, lon, validate(lat, lon)
}

func validate(lat, lon float64) error {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return fmt.Errorf("coordinates out of range: latitude -90..90, longitude -180..180")
	}
	return nil
}
