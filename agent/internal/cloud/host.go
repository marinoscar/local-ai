package cloud

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// HostVitals is the heartbeat's vitals.host (EAB's node_host_vitals feature): the machine, not the
// agent process.
type HostVitals struct {
	CPUPercent        float64 `json:"cpuPercent"`
	CPUCount          int     `json:"cpuCount"`
	LoadAvg1          float64 `json:"loadAvg1"`
	LoadAvg5          float64 `json:"loadAvg5"`
	LoadAvg15         float64 `json:"loadAvg15"`
	MemTotalBytes     int64   `json:"memTotalBytes"`
	MemFreeBytes      int64   `json:"memFreeBytes"`
	MemAvailableBytes int64   `json:"memAvailableBytes"`
	OSRelease         string  `json:"osRelease"`
	Arch              string  `json:"arch"`
	UptimeSeconds     int64   `json:"uptimeSeconds"`
}

// HostSource reports the host's vitals. ProcHost reads /proc; tests use a fake.
type HostSource interface {
	Host() HostVitals
}

// ProcHost is the HostSource of a Linux machine. Root is the filesystem root ("" or "/" for the real
// one); the whole-machine CPU percentage is the change of /proc/stat since the previous call, so the
// first call reports 0.
type ProcHost struct {
	Root string

	mu        sync.Mutex
	prevTotal uint64
	prevIdle  uint64
	havePrev  bool
}

func (p *ProcHost) path(rel string) string {
	root := p.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, rel)
}

// Host implements HostSource. Every value that cannot be read stays zero (or empty); the clamp in
// Vitals turns those into values the cloud accepts.
func (p *ProcHost) Host() HostVitals {
	h := HostVitals{CPUCount: runtime.NumCPU(), Arch: archName(runtime.GOARCH)}
	if b, err := os.ReadFile(p.path("proc/loadavg")); err == nil {
		h.LoadAvg1, h.LoadAvg5, h.LoadAvg15 = parseLoadAvg(string(b))
	}
	if b, err := os.ReadFile(p.path("proc/meminfo")); err == nil {
		h.MemTotalBytes, h.MemFreeBytes, h.MemAvailableBytes = parseMemInfo(string(b))
	}
	if b, err := os.ReadFile(p.path("proc/uptime")); err == nil {
		h.UptimeSeconds = parseUptime(string(b))
	}
	if b, err := os.ReadFile(p.path("etc/os-release")); err == nil {
		h.OSRelease = parseOSRelease(string(b))
	}
	if b, err := os.ReadFile(p.path("proc/stat")); err == nil {
		if total, idle, ok := parseCPUStat(string(b)); ok {
			p.mu.Lock()
			if p.havePrev && total > p.prevTotal {
				dt, di := total-p.prevTotal, idle-p.prevIdle
				if idle < p.prevIdle {
					di = 0
				}
				h.CPUPercent = 100 * float64(dt-di) / float64(dt)
			}
			p.prevTotal, p.prevIdle, p.havePrev = total, idle, true
			p.mu.Unlock()
		}
	}
	return h
}

func archName(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

func parseLoadAvg(s string) (l1, l5, l15 float64) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return
}

func parseMemInfo(s string) (total, free, avail int64) {
	for _, line := range strings.Split(s, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = kb * 1024
		case "MemFree":
			free = kb * 1024
		case "MemAvailable":
			avail = kb * 1024
		}
	}
	return
}

func parseUptime(s string) int64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(v)
}

func parseOSRelease(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// parseCPUStat reads the aggregate "cpu" line: total jiffies and idle (idle + iowait).
func parseCPUStat(s string) (total, idle uint64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var vals []uint64
		for _, x := range f[1:] {
			v, err := strconv.ParseUint(x, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			vals = append(vals, v)
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		return total, idle, true
	}
	return 0, 0, false
}
