package hardware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

// AMD GPU constants (agent.md section 4.4.2).
const (
	amdVendorID     = "0x1002"
	drmClassDir     = "sys/class/drm"
	unifiedVRAMMax  = 4 << 30  // a "dedicated" size below this with a large GTT means an APU
	unifiedGTTMin   = 16 << 30 // minimum GTT for the unified memory rule
	llamaCgroupProc = "sys/fs/cgroup/system.slice/" + llamaCgroupUnit + "/cgroup.procs"
	amdFallbackName = "AMD GPU"
)

var (
	cardDirRE   = regexp.MustCompile(`^card[0-9]+$`)
	pciSlotRE   = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)
	radeontopRE = regexp.MustCompile(`gpu ([0-9]+(?:\.[0-9]+)?)%`)
)

// amdProbe reads AMD GPUs from sysfs.
type amdProbe struct{ e *env }

func newAMDProbe(e *env) *amdProbe { return &amdProbe{e: e} }

// amdCard is one detected AMD card.
type amdCard struct {
	dir  string // "card0"
	num  int
	slot string // PCI slot from the uevent file; only used to match fdinfo and to ask lspci for the model
}

// cards lists the AMD cards: card<N> directories whose vendor is 0x1002 and that expose
// mem_info_vram_total. Connector entries (card0-DP-1) are ignored.
func (p *amdProbe) cards() []amdCard {
	entries, err := fs.ReadDir(p.e.fs, drmClassDir)
	if err != nil {
		return nil
	}
	var out []amdCard
	for _, en := range entries {
		name := en.Name()
		if !cardDirRE.MatchString(name) {
			continue
		}
		base := drmClassDir + "/" + name + "/device/"
		vendor, err := p.e.readTrim(base + "vendor")
		if err != nil || !strings.EqualFold(vendor, amdVendorID) || !p.e.exists(base+"mem_info_vram_total") {
			continue
		}
		num, _ := strconv.Atoi(strings.TrimPrefix(name, "card"))
		out = append(out, amdCard{dir: name, num: num, slot: p.slot(base)})
	}
	return out
}

// present is the AMD presence rule: at least one AMD card.
func (p *amdProbe) present() bool { return len(p.cards()) > 0 }

func (p *amdProbe) slot(base string) string {
	b, err := p.e.read(base + "uevent")
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PCI_SLOT_NAME="); ok && pciSlotRE.MatchString(strings.TrimSpace(v)) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// sample reads every AMD card. A card that cannot be read (no vram_total, malformed number) fails
// the whole sample, so the sampler keeps the previous values instead of reporting a half table.
func (p *amdProbe) sample(ctx context.Context) ([]GPU, error) {
	cards := p.cards()
	gpus := make([]GPU, 0, len(cards))
	if len(cards) == 0 {
		return gpus, nil
	}
	fd := p.llamaFdinfo()
	for i, c := range cards {
		g, err := p.readCard(ctx, c, i)
		if err != nil {
			return nil, err
		}
		g.VRAMUsedByOthersMiB = p.others(g, c, len(cards), fd)
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func (p *amdProbe) readCard(ctx context.Context, c amdCard, index int) (GPU, error) {
	base := drmClassDir + "/" + c.dir + "/device/"
	vramTotal, err := p.e.readInt(base + "mem_info_vram_total")
	if err != nil || vramTotal <= 0 {
		return GPU{}, fmt.Errorf("hardware: %s: cannot read mem_info_vram_total: %v", c.dir, err)
	}
	gttTotal, _ := p.e.readInt(base + "mem_info_gtt_total")
	gttUsed, _ := p.e.readInt(base + "mem_info_gtt_used")
	vramUsed, err := p.e.readInt(base + "mem_info_vram_used")
	if err != nil {
		var rerr error
		if vramUsed, rerr = p.rocmVRAMUsed(ctx, c.num); rerr != nil {
			return GPU{}, fmt.Errorf("hardware: %s: cannot read mem_info_vram_used: %v", c.dir, err)
		}
	}
	g := GPU{
		Index: index, Vendor: VendorAMD, Name: p.name(ctx, base, c), Backend: BackendVulkan,
		Throttling: []string{}, Processes: []GPUProcess{},
	}
	if vramTotal < unifiedVRAMMax && gttTotal >= unifiedGTTMin {
		// Unified memory (an APU): Vulkan RADV allocates from GTT, so that is the memory for fit.
		g.UnifiedMemory = true
		g.VRAMTotalMiB = gttTotal / mib
		g.VRAMUsedMiB = (vramUsed + gttUsed) / mib
	} else {
		g.VRAMTotalMiB = vramTotal / mib
		g.VRAMUsedMiB = vramUsed / mib
	}
	if busy, err := p.e.readInt(base + "gpu_busy_percent"); err == nil && busy >= 0 && busy <= 100 {
		v := int(busy)
		g.UtilPct = &v
	} else if v, ok := p.radeontopUtil(ctx); ok {
		g.UtilPct = &v
	}
	g.TempC = p.temp(base)
	return g, nil
}

// name is device/product_name, else the model of "lspci -mm -s <slot>", else "AMD GPU".
func (p *amdProbe) name(ctx context.Context, base string, c amdCard) string {
	if s, err := p.e.readTrim(base + "product_name"); err == nil && s != "" {
		return s
	}
	if c.slot != "" {
		if out, err := p.e.execOK(ctx, "lspci", "-mm", "-s", c.slot); err == nil {
			if m := lspciModel(out); m != "" {
				return m
			}
		}
	}
	return amdFallbackName
}

// lspciModel returns the device (model) string of the first line of `lspci -mm` output:
// `03:00.0 "VGA compatible controller" "Vendor" "Model" -rXX -pYY "SubVendor" "SubDevice"`.
// Only the model is kept (redaction: nothing else of lspci reaches the snapshot).
func lspciModel(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	var fields []string
	for {
		i := strings.IndexByte(line, '"')
		if i < 0 {
			break
		}
		j := strings.IndexByte(line[i+1:], '"')
		if j < 0 {
			break
		}
		fields = append(fields, line[i+1:i+1+j])
		line = line[i+1+j+1:]
	}
	if len(fields) < 3 {
		return ""
	}
	return strings.TrimSpace(fields[2])
}

// temp returns the edge temperature of the card in degrees: temp<N>_input whose label is "edge",
// else temp1_input (millidegrees in sysfs).
func (p *amdProbe) temp(base string) *float64 {
	hw, err := fs.ReadDir(p.e.fs, base+"hwmon")
	if err != nil {
		return nil
	}
	for _, h := range hw {
		if !strings.HasPrefix(h.Name(), "hwmon") {
			continue
		}
		dir := base + "hwmon/" + h.Name() + "/"
		input := "temp1_input"
		for i := 1; i <= 9; i++ {
			if l, err := p.e.readTrim(dir + "temp" + strconv.Itoa(i) + "_label"); err == nil && l == "edge" {
				input = "temp" + strconv.Itoa(i) + "_input"
				break
			}
		}
		if v, err := p.e.readInt(dir + input); err == nil {
			return ptr(round1(float64(v) / 1000))
		}
	}
	return nil
}

// rocmVRAMUsed asks rocm-smi (optional) for the used VRAM of card<num>, in bytes.
func (p *amdProbe) rocmVRAMUsed(ctx context.Context, num int) (int64, error) {
	out, err := p.e.execOK(ctx, "rocm-smi", "--showmeminfo", "vram", "--json")
	if err != nil {
		return 0, err
	}
	return parseRocmVRAMUsed(out, num)
}

func parseRocmVRAMUsed(out []byte, num int) (int64, error) {
	var doc map[string]map[string]string
	if err := json.Unmarshal(out, &doc); err != nil {
		return 0, fmt.Errorf("hardware: rocm-smi output: %w", err)
	}
	card, ok := doc["card"+strconv.Itoa(num)]
	if !ok {
		return 0, fmt.Errorf("hardware: rocm-smi has no card%d", num)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(card["VRAM Total Used Memory (B)"]), 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("hardware: rocm-smi has no usable used memory")
	}
	return v, nil
}

// radeontopUtil asks radeontop (optional) for one sample of GPU utilization.
func (p *amdProbe) radeontopUtil(ctx context.Context) (int, bool) {
	out, err := p.e.execOK(ctx, "radeontop", "-d", "-", "-l", "1")
	if err != nil {
		return 0, false
	}
	m := radeontopRE.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil || f < 0 || f > 100 {
		return 0, false
	}
	return int(f + 0.5), true
}

// fdinfoUse is the DRM memory the localai-llama processes hold, per PCI device, in KiB.
type fdinfoUse struct {
	known  bool             // at least one amdgpu client was found
	vram   map[string]int64 // by drm-pdev ("" when the kernel does not print it)
	gtt    map[string]int64
	nProcs int
}

// llamaFdinfo sums the DRM memory of the processes in the localai-llama cgroup, reading
// /proc/<pid>/fdinfo/* (drm-memory-vram, drm-memory-gtt, deduplicated by drm-client-id).
func (p *amdProbe) llamaFdinfo() fdinfoUse {
	use := fdinfoUse{vram: map[string]int64{}, gtt: map[string]int64{}}
	procs, err := p.e.read(llamaCgroupProc)
	if err != nil {
		return use // the unit is not running: no llama memory
	}
	seen := map[string]bool{}
	for _, f := range strings.Fields(string(procs)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 0 {
			continue
		}
		use.nProcs++
		dir := "proc/" + f + "/fdinfo"
		entries, err := fs.ReadDir(p.e.fs, dir)
		if err != nil {
			continue
		}
		for _, en := range entries {
			b, err := p.e.read(dir + "/" + en.Name())
			if err != nil {
				continue
			}
			if c, ok := parseDRMFdinfo(b); ok && !seen[c.client] {
				seen[c.client] = true
				use.known = true
				use.vram[c.pdev] += c.vramKiB
				use.gtt[c.pdev] += c.gttKiB
			}
		}
	}
	return use
}

type drmClient struct {
	client          string
	pdev            string
	vramKiB, gttKiB int64
}

// parseDRMFdinfo reads one fdinfo file; ok is false when it is not an amdgpu DRM client.
func parseDRMFdinfo(b []byte) (c drmClient, ok bool) {
	amd := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "drm-driver":
			amd = v == "amdgpu"
		case "drm-client-id":
			c.client = v
		case "drm-pdev":
			c.pdev = v
		case "drm-memory-vram":
			c.vramKiB = kibValue(v)
		case "drm-memory-gtt":
			c.gttKiB = kibValue(v)
		}
	}
	return c, amd && c.client != ""
}

// kibValue parses "123 KiB" (the kernel only prints KiB; a bare number is read as KiB too).
func kibValue(v string) int64 {
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	if len(f) > 1 && strings.EqualFold(f[1], "MiB") {
		return n * 1024
	}
	return n
}

// others returns vram_used_by_others_mib: the used memory minus what the localai-llama processes
// hold. It is null when llama-server runs but the kernel exposes no DRM memory accounting.
func (p *amdProbe) others(g GPU, c amdCard, nCards int, fd fdinfoUse) *int64 {
	if fd.nProcs > 0 && !fd.known {
		return nil
	}
	var kib int64
	for pdev, v := range fd.vram {
		if nCards > 1 && c.slot != "" && pdev != "" && !strings.EqualFold(pdev, c.slot) {
			continue
		}
		kib += v
		if g.UnifiedMemory {
			kib += fd.gtt[pdev]
		}
	}
	others := g.VRAMUsedMiB - kib/1024
	if others < 0 {
		others = 0
	}
	return &others
}

// AMDAccess is the result of the device-node and group checks (LAI-HW-003 and LAI-HW-004).
type AMDAccess struct {
	// MissingDevices lists the device nodes that do not exist ("/dev/kfd", "/dev/dri/renderD*").
	MissingDevices []string
	// MissingGroups lists the groups (video, render) the localai user is not a member of.
	MissingGroups []string
}

// access checks /dev/kfd, /dev/dri/renderD* and the membership of the localai user in the video
// and render groups, read from /etc/group. A group that does not exist, or an unreadable
// /etc/group, is not reported: the check only flags what it can prove.
func (p *amdProbe) access() AMDAccess {
	var a AMDAccess
	if !p.e.exists("dev/kfd") {
		a.MissingDevices = append(a.MissingDevices, "/dev/kfd")
	}
	hasRender := false
	if entries, err := fs.ReadDir(p.e.fs, "dev/dri"); err == nil {
		for _, en := range entries {
			if strings.HasPrefix(en.Name(), "renderD") {
				hasRender = true
				break
			}
		}
	}
	if !hasRender {
		a.MissingDevices = append(a.MissingDevices, "/dev/dri/renderD*")
	}
	if b, err := p.e.read("etc/group"); err == nil {
		groups := parseGroupMembers(b)
		for _, g := range []string{"video", "render"} {
			members, exists := groups[g]
			if !exists {
				continue
			}
			if !members["localai"] {
				a.MissingGroups = append(a.MissingGroups, g)
			}
		}
	}
	return a
}

// parseGroupMembers maps group name to its member set (the fourth field of /etc/group).
func parseGroupMembers(b []byte) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		m := map[string]bool{}
		for _, u := range strings.Split(f[3], ",") {
			if u = strings.TrimSpace(u); u != "" {
				m[u] = true
			}
		}
		out[f[0]] = m
	}
	return out
}
