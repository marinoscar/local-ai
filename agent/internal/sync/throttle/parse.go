package throttle

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ErrBadLimit is wrapped by every ParseLimit error. node.yaml validation reports it as a
// configuration error (ark-sync.md section 3.3).
var ErrBadLimit = errors.New("throttle: not a bandwidth limit")

// maxLimit is the largest limit accepted (1 TiB/s): above that the number is a typo.
const maxLimit = 1 << 40

var limitRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]+)/s$`)

// units maps the unit as written to bytes per second per unit. A unit ending in a capital "B" is
// bytes (so "MB/s" is megabytes and "mb/s" is refused as ambiguous); one ending in "bit" is bits.
// KB, MB, GB are decimal, KiB, MiB, GiB binary, kbit, Mbit, Gbit decimal bits.
var units = map[string]float64{
	"B": 1, "KB": 1e3, "kB": 1e3, "MB": 1e6, "GB": 1e9,
	"KiB": 1 << 10, "kiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30,
	"bit": 1.0 / 8, "kbit": 1e3 / 8, "Kbit": 1e3 / 8, "Mbit": 1e6 / 8, "Gbit": 1e9 / 8,
}

// ParseLimit parses a sync.bandwidth_limit value into bytes per second: "0" is unlimited (0),
// otherwise a number and a unit per second: "500KB/s", "20MiB/s", "100Mbit/s", "1.5MB/s". It
// returns a value of at least one byte per second or an error wrapping ErrBadLimit.
func ParseLimit(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "0" {
		return 0, nil
	}
	m := limitRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%w: %q (use 0, or a number and a unit per second such as 20MiB/s, 500KB/s or 100Mbit/s)", ErrBadLimit, s)
	}
	mult, ok := units[m[2]]
	if !ok {
		return 0, fmt.Errorf("%w: %q has the unit %q (use B, KB, MB, GB, KiB, MiB, GiB, kbit, Mbit or Gbit)", ErrBadLimit, s, m[2])
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsInf(num, 0) {
		return 0, fmt.Errorf("%w: %q", ErrBadLimit, s)
	}
	bps := num * mult
	if bps < 1 {
		return 0, fmt.Errorf("%w: %q is less than one byte per second (use 0 for no limit)", ErrBadLimit, s)
	}
	if bps > maxLimit {
		return 0, fmt.Errorf("%w: %q is more than 1 TiB/s", ErrBadLimit, s)
	}
	return int64(bps), nil
}
