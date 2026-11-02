package hardware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// The two nvidia-smi queries (agent.md section 4.4.1). The argv is fixed; the field list never
// carries uuid, serial or pci.bus_id (a test asserts it).
const (
	nvidiaDriverProc = "proc/driver/nvidia/version"
	nvidiaFieldsHead = "index,name,memory.total,memory.used,memory.free,temperature.gpu,utilization.gpu,power.draw,compute_cap,driver_version,"
	nvidiaReasons    = "clocks_event_reasons.active"
	nvidiaReasonsOld = "clocks_throttle_reasons.active"
	nvidiaAppsQuery  = "--query-compute-apps=pid,process_name,used_memory"
	nvidiaFormat     = "--format=csv,noheader,nounits"
	nvidiaGPUColumns = 11
)

// Throttle bits of the clocks event reasons bitmask.
const (
	reasonSWThermal  = 0x20
	reasonHWThermal  = 0x40
	reasonPowerBrake = 0x80
)

// nvidiaProbe samples NVIDIA GPUs through nvidia-smi.
type nvidiaProbe struct {
	e *env

	mu    sync.Mutex
	field string // the working clocks reasons field, remembered for the process lifetime
}

func newNvidiaProbe(e *env) *nvidiaProbe { return &nvidiaProbe{e: e, field: nvidiaReasons} }

// driverLoaded reports whether the kernel driver is loaded: /proc/driver/nvidia/version exists.
func (p *nvidiaProbe) driverLoaded() bool { return p.e.exists(nvidiaDriverProc) }

// hasTool reports whether nvidia-smi is installed in the fixed PATH.
func (p *nvidiaProbe) hasTool() bool {
	_, err := p.e.look("nvidia-smi")
	return err == nil
}

// present is the presence rule of agent.md section 4.4.1: the driver proc file, or the program.
func (p *nvidiaProbe) present() bool { return p.driverLoaded() || p.hasTool() }

func (p *nvidiaProbe) reasonsField() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.field
}

func (p *nvidiaProbe) setReasonsField(f string) {
	p.mu.Lock()
	p.field = f
	p.mu.Unlock()
}

func (p *nvidiaProbe) gpuArgs(reasons string) []string {
	return []string{"--query-gpu=" + nvidiaFieldsHead + reasons, nvidiaFormat}
}

// sample runs the GPU query, then the compute-apps query, and joins them with the cgroup match.
// A failing GPU query is an error (the sampler keeps the last value and counts the failure); a
// failing apps query only loses the per-process detail.
func (p *nvidiaProbe) sample(ctx context.Context) ([]GPU, error) {
	out, err := p.queryGPUs(ctx)
	if err != nil {
		return nil, err
	}
	gpus, err := parseNvidiaGPUs(out)
	if err != nil {
		return nil, err
	}
	if len(gpus) == 0 {
		return gpus, nil
	}
	var apps []GPUProcess
	if raw, aerr := p.e.execOK(ctx, "nvidia-smi", nvidiaAppsQuery, nvidiaFormat); aerr == nil {
		apps, _ = parseNvidiaApps(raw) // a malformed apps list only loses the detail
	}
	p.attribute(gpus, apps)
	return gpus, nil
}

// queryGPUs runs the GPU query, retrying once with the old field name when the driver rejects the
// new one, and remembers the name that worked.
func (p *nvidiaProbe) queryGPUs(ctx context.Context) ([]byte, error) {
	field := p.reasonsField()
	res, err := p.e.exec(ctx, "nvidia-smi", p.gpuArgs(field)...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && field == nvidiaReasons && invalidField(res) {
		field = nvidiaReasonsOld
		if res, err = p.e.exec(ctx, "nvidia-smi", p.gpuArgs(field)...); err != nil {
			return nil, err
		}
		if res.ExitCode == 0 {
			p.setReasonsField(field)
		}
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("hardware: nvidia-smi exited %d: %s", res.ExitCode, firstLine(append(append([]byte{}, res.Stderr...), res.Stdout...)))
	}
	return res.Stdout, nil
}

// invalidField reports whether nvidia-smi rejected a queried field name ("Field \"x\" is not a valid
// field to query."), which is what an older driver says about clocks_event_reasons.active.
func invalidField(res execx.Result) bool {
	return bytes.Contains(res.Stdout, []byte("is not a valid field")) || bytes.Contains(res.Stderr, []byte("is not a valid field"))
}

// attribute fills Processes and VRAMUsedByOthersMiB. On a node with one GPU every compute process
// belongs to it. With several GPUs a process cannot be attributed (no uuid or bus id is queried),
// so the llama-server memory is spread in proportion to each GPU's use.
func (p *nvidiaProbe) attribute(gpus []GPU, apps []GPUProcess) {
	var llama int64
	for _, a := range apps {
		if p.inLlamaCgroup(a.PID) {
			llama += a.VRAMMiB
		}
	}
	var usedSum int64
	for _, g := range gpus {
		usedSum += g.VRAMUsedMiB
	}
	for i := range gpus {
		share := llama
		if len(gpus) > 1 {
			share = 0
			if usedSum > 0 {
				share = llama * gpus[i].VRAMUsedMiB / usedSum
			}
		}
		others := gpus[i].VRAMUsedMiB - share
		if others < 0 {
			others = 0
		}
		gpus[i].VRAMUsedByOthersMiB = &others
		if len(gpus) == 1 {
			gpus[i].Processes = append([]GPUProcess{}, apps...)
		}
	}
}

// inLlamaCgroup reads /proc/<pid>/cgroup and reports whether the process runs in the localai-llama
// unit. An unreadable file (the process ended) counts as "not llama".
func (p *nvidiaProbe) inLlamaCgroup(pid int) bool {
	b, err := p.e.read("proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return false
	}
	return cgroupHasUnit(b, llamaCgroupUnit)
}

// cgroupHasUnit reports whether any line of a /proc/<pid>/cgroup file has the unit as a path
// element ("0::/system.slice/localai-llama.service" or "1:name=systemd:/system.slice/...").
func cgroupHasUnit(b []byte, unit string) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, el := range strings.Split(parts[2], "/") {
			if el == unit {
				return true
			}
		}
	}
	return false
}

// nullable turns the "no value" spellings of nvidia-smi ("[N/A]", "[Not Supported]", "") into false.
func nullable(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || (strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return "", false
	}
	return s, true
}

func csvReader(b []byte, fields int) *csv.Reader {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = fields
	r.TrimLeadingSpace = true
	r.ReuseRecord = false
	return r
}

// parseNvidiaGPUs parses the output of the GPU query. Any malformed row fails the whole parse, so
// a half-read table is never reported as truth. index, name and the memory totals are required.
func parseNvidiaGPUs(b []byte) ([]GPU, error) {
	r := csvReader(b, nvidiaGPUColumns)
	var gpus []GPU
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hardware: nvidia-smi output: %w", err)
		}
		g, err := nvidiaRow(rec)
		if err != nil {
			return nil, err
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func nvidiaRow(rec []string) (GPU, error) {
	for i := range rec {
		rec[i] = strings.TrimSpace(rec[i])
	}
	idx, err := strconv.Atoi(rec[0])
	if err != nil || idx < 0 {
		return GPU{}, fmt.Errorf("hardware: nvidia-smi: bad GPU index %q", rec[0])
	}
	if rec[1] == "" {
		return GPU{}, fmt.Errorf("hardware: nvidia-smi: GPU %d has no name", idx)
	}
	total, err1 := strconv.ParseInt(rec[2], 10, 64)
	used, err2 := strconv.ParseInt(rec[3], 10, 64)
	if err1 != nil || err2 != nil || total <= 0 || used < 0 {
		return GPU{}, fmt.Errorf("hardware: nvidia-smi: GPU %d has bad memory values %q, %q", idx, rec[2], rec[3])
	}
	g := GPU{
		Index: idx, Vendor: VendorNVIDIA, Name: rec[1], Backend: BackendCUDA,
		VRAMTotalMiB: total, VRAMUsedMiB: used,
		Throttling: []string{}, Processes: []GPUProcess{},
	}
	if s, ok := nullable(rec[5]); ok {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			g.TempC = ptr(v)
		}
	}
	if s, ok := nullable(rec[6]); ok {
		if v, err := strconv.Atoi(s); err == nil {
			g.UtilPct = &v
		}
	}
	if s, ok := nullable(rec[7]); ok {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			g.PowerW = ptr(v)
		}
	}
	if s, ok := nullable(rec[8]); ok {
		g.ComputeCap = &s
	}
	if s, ok := nullable(rec[9]); ok {
		g.Driver = &s
	}
	if s, ok := nullable(rec[10]); ok {
		g.Throttling = throttleReasons(s)
	}
	return g, nil
}

// throttleReasons maps the hex bitmask of clocks_event_reasons.active to names. An unparsable
// value is treated as "no throttling" rather than failing the sample.
func throttleReasons(s string) []string {
	out := []string{}
	mask, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return out
	}
	if mask&(reasonSWThermal|reasonHWThermal) != 0 {
		out = append(out, ThrottleThermal)
	}
	if mask&reasonPowerBrake != 0 {
		out = append(out, ThrottlePowerBrake)
	}
	return out
}

// parseNvidiaApps parses the compute-apps query: "pid, process_name, used_memory". Names are
// reduced to their base name (nvidia-smi prints the executable path). A process whose memory is
// [N/A] (some drivers hide it) is listed with 0.
func parseNvidiaApps(b []byte) ([]GPUProcess, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	r := csvReader(b, 3)
	var out []GPUProcess
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("hardware: nvidia-smi compute apps: %w", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil || pid <= 0 {
			return out, fmt.Errorf("hardware: nvidia-smi compute apps: bad pid %q", rec[0])
		}
		var mem int64
		if s, ok := nullable(rec[2]); ok {
			if mem, err = strconv.ParseInt(s, 10, 64); err != nil || mem < 0 {
				return out, fmt.Errorf("hardware: nvidia-smi compute apps: bad memory %q", rec[2])
			}
		}
		out = append(out, GPUProcess{PID: pid, Name: path.Base(strings.TrimSpace(rec[1])), VRAMMiB: mem})
	}
	return out, nil
}
