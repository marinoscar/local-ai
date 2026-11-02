package hardware

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// RAM is the memory block of GET /hardware (agent.md section 3.8.6).
type RAM struct {
	TotalMiB     int64 `json:"total_mib"`
	AvailableMiB int64 `json:"available_mib"`
	SwapTotalMiB int64 `json:"swap_total_mib"`
	SwapFreeMiB  int64 `json:"swap_free_mib"`
	// Stale is true when the last probe failed and these are the previous values.
	Stale bool `json:"stale,omitempty"`
}

// SwapUsedPct is the share of swap in use, 0 when there is no swap.
func (r RAM) SwapUsedPct() float64 {
	if r.SwapTotalMiB <= 0 {
		return 0
	}
	return float64(r.SwapTotalMiB-r.SwapFreeMiB) * 100 / float64(r.SwapTotalMiB)
}

// AvailablePct is MemAvailable as a share of MemTotal; 100 when the total is unknown.
func (r RAM) AvailablePct() float64 {
	if r.TotalMiB <= 0 {
		return 100
	}
	return float64(r.AvailableMiB) * 100 / float64(r.TotalMiB)
}

// parseMeminfo reads MemTotal, MemAvailable, SwapTotal and SwapFree (kB, converted to MiB).
// MemTotal and MemAvailable are required; a machine without swap reports SwapTotal 0.
func parseMeminfo(b []byte) (RAM, error) {
	kb := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal", "MemAvailable", "SwapTotal", "SwapFree":
			f := strings.Fields(rest)
			if len(f) == 0 {
				continue
			}
			v, err := strconv.ParseInt(f[0], 10, 64)
			if err != nil || v < 0 {
				return RAM{}, fmt.Errorf("hardware: /proc/meminfo: bad %s value %q", key, f[0])
			}
			kb[key] = v
		}
	}
	for _, need := range []string{"MemTotal", "MemAvailable"} {
		if _, ok := kb[need]; !ok {
			return RAM{}, fmt.Errorf("hardware: /proc/meminfo has no %s", need)
		}
	}
	return RAM{
		TotalMiB:     kb["MemTotal"] / 1024,
		AvailableMiB: kb["MemAvailable"] / 1024,
		SwapTotalMiB: kb["SwapTotal"] / 1024,
		SwapFreeMiB:  kb["SwapFree"] / 1024,
	}, nil
}

func (e *env) readRAM() (RAM, error) {
	b, err := e.read("proc/meminfo")
	if err != nil {
		return RAM{}, err
	}
	return parseMeminfo(b)
}
