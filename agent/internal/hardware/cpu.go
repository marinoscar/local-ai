package hardware

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CPU is the cpu block of GET /hardware.
type CPU struct {
	Model string  `json:"model"`
	Cores int     `json:"cores"`
	Load1 float64 `json:"load1"`
	Stale bool    `json:"stale,omitempty"`
	// PhysicalCores counts distinct (physical id, core id) pairs of /proc/cpuinfo, so hyper-threads
	// are not counted twice; it equals Cores where cpuinfo has no such ids (ARM). Not on the wire.
	PhysicalCores int `json:"-"`
}

// Self is the agent's own footprint (agent.md section 4.4.4), used by the heartbeat vitals. It is
// not part of GET /hardware.
type Self struct {
	// CPUPercent is utime+stime since the previous sample, as a percentage of one core. Zero on the
	// first sample.
	CPUPercent float64
	RSSMiB     int64
}

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat times. It is 100 on every Linux the agent
// targets (amd64 and arm64).
const clockTicks = 100

// parseCPUInfo returns the first "model name" (x86) or "Model"/"Hardware" (ARM) and the number of
// "processor" lines.
func parseCPUInfo(b []byte) (model string, cores int, err error) {
	var alt string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "processor":
			cores++
		case "model name":
			if model == "" {
				model = val
			}
		case "Model", "Hardware":
			if alt == "" {
				alt = val
			}
		}
	}
	if cores == 0 {
		return "", 0, fmt.Errorf("hardware: /proc/cpuinfo lists no processor")
	}
	if model == "" {
		model = alt
	}
	return model, cores, nil
}

func parseLoadavg(b []byte) (float64, error) {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("hardware: /proc/loadavg is empty")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("hardware: /proc/loadavg: bad value %q", f[0])
	}
	return v, nil
}

func (e *env) readCPU() (CPU, error) {
	info, err := e.read("proc/cpuinfo")
	if err != nil {
		return CPU{}, err
	}
	model, cores, err := parseCPUInfo(info)
	if err != nil {
		return CPU{}, err
	}
	la, err := e.read("proc/loadavg")
	if err != nil {
		return CPU{}, err
	}
	load, err := parseLoadavg(la)
	if err != nil {
		return CPU{}, err
	}
	return CPU{Model: model, Cores: cores, Load1: load, PhysicalCores: parsePhysicalCores(info, cores)}, nil
}

// parsePhysicalCores counts the distinct (physical id, core id) pairs of /proc/cpuinfo; when the file
// has none (ARM) it returns logical.
func parsePhysicalCores(b []byte, logical int) int {
	seen := map[string]bool{}
	phys, core := "", ""
	flush := func() {
		if core != "" {
			seen[phys+"/"+core] = true
		}
		phys, core = "", ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "physical id":
			phys = strings.TrimSpace(val)
		case "core id":
			core = strings.TrimSpace(val)
		}
	}
	flush()
	if len(seen) == 0 {
		return logical
	}
	return len(seen)
}

// parseSelfStat returns utime+stime in clock ticks. The command name (field 2) may contain spaces
// and parentheses, so fields are counted after the last ")".
func parseSelfStat(b []byte) (uint64, error) {
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("hardware: /proc/self/stat: no command field")
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); utime is field 14 and stime field 15.
	if len(f) < 13 {
		return 0, fmt.Errorf("hardware: /proc/self/stat: %d fields after the command, need 13", len(f))
	}
	u, err1 := strconv.ParseUint(f[11], 10, 64)
	st, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("hardware: /proc/self/stat: bad utime or stime")
	}
	return u + st, nil
}

// parseVmRSS returns VmRSS of /proc/self/status in MiB.
func parseVmRSS(b []byte) (int64, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				break
			}
			v, err := strconv.ParseInt(f[0], 10, 64)
			if err != nil || v < 0 {
				return 0, fmt.Errorf("hardware: /proc/self/status: bad VmRSS %q", f[0])
			}
			return v / 1024, nil
		}
	}
	return 0, fmt.Errorf("hardware: /proc/self/status has no VmRSS")
}

// selfTracker turns two readings of /proc/self/stat into a CPU percentage.
type selfTracker struct {
	ticks uint64
	at    time.Time
	ok    bool
}

func (t *selfTracker) read(e *env, now time.Time) (Self, error) {
	st, err := e.read("proc/self/stat")
	if err != nil {
		return Self{}, err
	}
	ticks, err := parseSelfStat(st)
	if err != nil {
		return Self{}, err
	}
	status, err := e.read("proc/self/status")
	if err != nil {
		return Self{}, err
	}
	rss, err := parseVmRSS(status)
	if err != nil {
		return Self{}, err
	}
	out := Self{RSSMiB: rss}
	if t.ok && ticks >= t.ticks {
		if dt := now.Sub(t.at).Seconds(); dt > 0 {
			out.CPUPercent = round1(float64(ticks-t.ticks) / clockTicks / dt * 100)
		}
	}
	t.ticks, t.at, t.ok = ticks, now, true
	return out, nil
}
