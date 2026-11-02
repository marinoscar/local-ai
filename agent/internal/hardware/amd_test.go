package hardware

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

func TestAMDDedicatedCard(t *testing.T) {
	e := testEnv(fixture(t, "sysfs/amd-dedicated"), nil)
	p := newAMDProbe(e)
	if !p.present() {
		t.Fatal("card not detected")
	}
	gpus, err := p.sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	g := gpus[0]
	if g.Vendor != VendorAMD || g.Backend != BackendVulkan || g.UnifiedMemory || g.Name != "Radeon RX 6600 Example" || g.Index != 0 {
		t.Errorf("identity = %+v", g)
	}
	if g.VRAMTotalMiB != 8192 || g.VRAMUsedMiB != 1024 {
		t.Errorf("dedicated memory total=%d used=%d (GTT must not count)", g.VRAMTotalMiB, g.VRAMUsedMiB)
	}
	if g.TempC == nil || *g.TempC != 52 || g.UtilPct == nil || *g.UtilPct != 37 {
		t.Errorf("temp=%v util=%v", g.TempC, g.UtilPct)
	}
	if g.ComputeCap != nil || g.Driver != nil || len(g.Throttling) != 0 {
		t.Errorf("nvidia-only fields set: %+v", g)
	}
	// llama is not running (no cgroup.procs): everything in use is "others".
	if g.VRAMUsedByOthersMiB == nil || *g.VRAMUsedByOthersMiB != 1024 {
		t.Errorf("others = %v", g.VRAMUsedByOthersMiB)
	}
}

func TestAMDStrixHaloUnifiedMemory(t *testing.T) {
	r := execfake.New().Expect(execfake.Key("/usr/bin/lspci", "-mm", "-s", "0000:c1:00.0"), ok(string(testdata(t, "lspci/strix-halo.txt"))), nil)
	e := testEnv(fixture(t, "sysfs/amd-strix-halo"), r)
	gpus, err := newAMDProbe(e).sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	g := gpus[0]
	if !g.UnifiedMemory {
		t.Fatal("unified_memory must be true for a 2 GiB UMA carve-out with a 61 GiB GTT")
	}
	if g.VRAMTotalMiB != 62464 {
		t.Errorf("fit memory = %d MiB, want the GTT size 62464", g.VRAMTotalMiB)
	}
	if g.VRAMUsedMiB != 512+4096 {
		t.Errorf("used = %d MiB, want vram_used + gtt_used = 4608", g.VRAMUsedMiB)
	}
	if g.Name != "Strix Halo [Radeon 8060S Graphics]" {
		t.Errorf("name = %q, want the lspci model only", g.Name)
	}
	// llama-server holds 256 MiB VRAM + 3072 MiB GTT; the duplicate fdinfo (same client id) is counted once.
	if g.VRAMUsedByOthersMiB == nil || *g.VRAMUsedByOthersMiB != 4608-3328 {
		t.Errorf("others = %v, want %d", g.VRAMUsedByOthersMiB, 4608-3328)
	}
	if g.TempC == nil || *g.TempC != 47.5 {
		t.Errorf("temp = %v", g.TempC)
	}
}

func TestAMDNameFallbacks(t *testing.T) {
	base := func() fstest.MapFS {
		return fstest.MapFS{
			"sys/class/drm/card0/device/vendor":              file("0x1002\n"),
			"sys/class/drm/card0/device/mem_info_vram_total": file("8589934592\n"),
			"sys/class/drm/card0/device/mem_info_vram_used":  file("0\n"),
			"sys/class/drm/card0/device/uevent":              file("PCI_SLOT_NAME=0000:03:00.0\n"),
		}
	}
	cases := []struct {
		name string
		run  *execfake.Runner
		look func(string) (string, error)
		want string
	}{
		{"lspci fails", execfake.New().Expect(execfake.Key("/usr/bin/lspci", "-mm", "-s", "0000:03:00.0"), exit(1, "x"), nil), nil, "AMD GPU"},
		{"lspci missing", execfake.New(), only(), "AMD GPU"},
		{"lspci garbage", execfake.New().Expect(execfake.Key("/usr/bin/lspci", "-mm", "-s", "0000:03:00.0"), ok("no quotes here"), nil), nil, "AMD GPU"},
		{"lspci model", execfake.New().Expect(execfake.Key("/usr/bin/lspci", "-mm", "-s", "0000:03:00.0"), ok(`03:00.0 "VGA" "Vendor" "Navi Example" -r1`), nil), nil, "Navi Example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(base(), c.run)
			if c.look != nil {
				e.look = c.look
			}
			gpus, err := newAMDProbe(e).sample(context.Background())
			if err != nil || len(gpus) != 1 || gpus[0].Name != c.want {
				t.Fatalf("gpus = %+v, err = %v; want name %q", gpus, err, c.want)
			}
		})
	}
}

func TestAMDPresenceRules(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want bool
	}{
		{"amd card", fstest.MapFS{
			"sys/class/drm/card0/device/vendor": file("0x1002"), "sys/class/drm/card0/device/mem_info_vram_total": file("1"),
		}, true},
		{"vendor in upper case", fstest.MapFS{
			"sys/class/drm/card0/device/vendor": file("0X1002"), "sys/class/drm/card0/device/mem_info_vram_total": file("1"),
		}, true},
		{"nvidia card", fstest.MapFS{
			"sys/class/drm/card0/device/vendor": file("0x10de"), "sys/class/drm/card0/device/mem_info_vram_total": file("1"),
		}, false},
		{"amd card without memory info (not amdgpu bound)", fstest.MapFS{"sys/class/drm/card0/device/vendor": file("0x1002")}, false},
		{"connector entry is not a card", fstest.MapFS{
			"sys/class/drm/card0-DP-1/device/vendor": file("0x1002"), "sys/class/drm/card0-DP-1/device/mem_info_vram_total": file("1"),
		}, false},
		{"no drm class", fstest.MapFS{}, false},
	}
	for _, c := range cases {
		if got := newAMDProbe(testEnv(c.fsys, nil)).present(); got != c.want {
			t.Errorf("%s: present = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAMDSampleFailures(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
	}{
		{"malformed vram_total", fstest.MapFS{
			"sys/class/drm/card0/device/vendor": file("0x1002"), "sys/class/drm/card0/device/mem_info_vram_total": file("lots"),
			"sys/class/drm/card0/device/mem_info_vram_used": file("0"),
		}},
		{"vram_used missing and no rocm-smi", fstest.MapFS{
			"sys/class/drm/card0/device/vendor": file("0x1002"), "sys/class/drm/card0/device/mem_info_vram_total": file("8589934592"),
		}},
	}
	for _, c := range cases {
		e := testEnv(c.fsys, nil)
		e.look = only()
		if gpus, err := newAMDProbe(e).sample(context.Background()); err == nil {
			t.Errorf("%s: expected an error, got %+v", c.name, gpus)
		}
	}
}

func TestAMDOptionalEnrichers(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/drm/card1/device/vendor":              file("0x1002"),
		"sys/class/drm/card1/device/mem_info_vram_total": file("8589934592"),
		// mem_info_vram_used and gpu_busy_percent are unreadable: the enrichers fill them in.
	}
	r := execfake.New().
		Expect(execfake.Key("/usr/bin/rocm-smi", "--showmeminfo", "vram", "--json"), ok(string(testdata(t, "rocm-smi/vram.json"))), nil).
		Expect(execfake.Key("/usr/bin/radeontop", "-d", "-", "-l", "1"), ok(string(testdata(t, "radeontop/sample.txt"))), nil)
	gpus, err := newAMDProbe(testEnv(fsys, r)).sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	if gpus[0].VRAMUsedMiB != 512 {
		t.Errorf("used from rocm-smi = %d, want 512", gpus[0].VRAMUsedMiB)
	}
	if gpus[0].UtilPct == nil || *gpus[0].UtilPct != 24 {
		t.Errorf("util from radeontop = %v, want 24", gpus[0].UtilPct)
	}
}

func TestRocmAndRadeontopParsersRejectGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", `{"card0": {}}`, `{"card1": {"VRAM Total Used Memory (B)": "x"}}`, `{"card1": {"VRAM Total Used Memory (B)": "-5"}}`} {
		if _, err := parseRocmVRAMUsed([]byte(in), 1); err == nil {
			t.Errorf("parseRocmVRAMUsed(%q) accepted garbage", in)
		}
	}
	// radeontop absent, failing or printing no gpu field is simply "no value".
	for _, c := range []struct {
		name string
		run  *execfake.Runner
		look func(string) (string, error)
	}{
		{"missing", execfake.New(), only()},
		{"exit 1", execfake.New().Expect(execfake.Key("/usr/bin/radeontop", "-d", "-", "-l", "1"), exit(1, "x"), nil), nil},
		{"no gpu field", execfake.New().Expect(execfake.Key("/usr/bin/radeontop", "-d", "-", "-l", "1"), ok("nothing useful"), nil), nil},
		{"out of range", execfake.New().Expect(execfake.Key("/usr/bin/radeontop", "-d", "-", "-l", "1"), ok("gpu 250.00%"), nil), nil},
	} {
		e := testEnv(nil, c.run)
		if c.look != nil {
			e.look = c.look
		}
		if v, ok := newAMDProbe(e).radeontopUtil(context.Background()); ok {
			t.Errorf("%s: got %d", c.name, v)
		}
	}
}

func TestAMDOthersIsNullWithoutFdinfoAccounting(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/drm/card0/device/vendor":                             file("0x1002"),
		"sys/class/drm/card0/device/mem_info_vram_total":                file("8589934592"),
		"sys/class/drm/card0/device/mem_info_vram_used":                 file("1073741824"),
		"sys/fs/cgroup/system.slice/localai-llama.service/cgroup.procs": file("100\n"),
		"proc/100/fdinfo/3":                                             file("pos: 0\nflags: 02\n"), // an old kernel: no drm-* keys
	}
	gpus, err := newAMDProbe(testEnv(fsys, nil)).sample(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	if gpus[0].VRAMUsedByOthersMiB != nil {
		t.Errorf("others = %d, want null (llama runs but its memory cannot be told apart)", *gpus[0].VRAMUsedByOthersMiB)
	}
}

func TestAMDOthersWithTwoCardsMatchesPCISlot(t *testing.T) {
	card := func(n, slot string) fstest.MapFS {
		p := "sys/class/drm/card" + n + "/device/"
		return fstest.MapFS{
			p + "vendor": file("0x1002"), p + "mem_info_vram_total": file("8589934592"), p + "mem_info_vram_used": file("2147483648"),
			p + "uevent": file("PCI_SLOT_NAME=" + slot + "\n"),
		}
	}
	fsys := card("0", "0000:03:00.0")
	for k, v := range card("1", "0000:04:00.0") {
		fsys[k] = v
	}
	fsys["sys/fs/cgroup/system.slice/localai-llama.service/cgroup.procs"] = file("100\n")
	fsys["proc/100/fdinfo/3"] = file("drm-driver: amdgpu\ndrm-client-id: 5\ndrm-pdev: 0000:04:00.0\ndrm-memory-vram: 1048576 KiB\n")
	gpus, err := newAMDProbe(testEnv(fsys, nil)).sample(context.Background())
	if err != nil || len(gpus) != 2 {
		t.Fatalf("gpus = %+v, err = %v", gpus, err)
	}
	// llama's 1024 MiB is on the second card only.
	if *gpus[0].VRAMUsedByOthersMiB != 2048 || *gpus[1].VRAMUsedByOthersMiB != 1024 {
		t.Errorf("others = %d and %d, want 2048 and 1024", *gpus[0].VRAMUsedByOthersMiB, *gpus[1].VRAMUsedByOthersMiB)
	}
}

func TestAMDTemperatureLabelAndMissingHwmon(t *testing.T) {
	base := "sys/class/drm/card0/device/"
	fsys := fstest.MapFS{
		base + "hwmon/hwmon1/temp1_label": file("junction\n"), base + "hwmon/hwmon1/temp1_input": file("90000\n"),
		base + "hwmon/hwmon1/temp2_label": file("edge\n"), base + "hwmon/hwmon1/temp2_input": file("55500\n"),
	}
	if v := newAMDProbe(testEnv(fsys, nil)).temp(base); v == nil || *v != 55.5 {
		t.Errorf("edge temperature = %v, want 55.5", v)
	}
	noLabel := fstest.MapFS{base + "hwmon/hwmon1/temp1_input": file("41000\n")}
	if v := newAMDProbe(testEnv(noLabel, nil)).temp(base); v == nil || *v != 41 {
		t.Errorf("fallback temp1_input = %v, want 41", v)
	}
	if v := newAMDProbe(testEnv(fstest.MapFS{}, nil)).temp(base); v != nil {
		t.Errorf("no hwmon: got %v", *v)
	}
}

func TestAMDAccessChecks(t *testing.T) {
	cases := []struct {
		name        string
		fsys        fstest.MapFS
		wantDevices string
		wantGroups  string
	}{
		{"all good", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file(""), "etc/group": file("video:x:44:localai\nrender:x:105:localai\n")}, "", ""},
		{"kfd missing", fstest.MapFS{"dev/dri/renderD128": file("")}, "/dev/kfd", ""},
		{"render node missing", fstest.MapFS{"dev/kfd": file(""), "dev/dri/card0": file("")}, "/dev/dri/renderD*", ""},
		{"no devices at all", fstest.MapFS{}, "/dev/kfd,/dev/dri/renderD*", ""},
		{"not in render", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file(""), "etc/group": file("video:x:44:localai\nrender:x:105:alice\n")}, "", "render"},
		{"in neither", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file(""), "etc/group": file("video:x:44:\nrender:x:105:\n")}, "", "video,render"},
		{"group does not exist on this distro", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file(""), "etc/group": file("video:x:44:localai\n")}, "", ""},
		{"unreadable /etc/group is not reported", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file("")}, "", ""},
		{"garbage group lines", fstest.MapFS{"dev/kfd": file(""), "dev/dri/renderD128": file(""), "etc/group": file("garbage\n:x:1:localai\nvideo:x:44:localai\n")}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newAMDProbe(testEnv(c.fsys, nil)).access()
			if got := strings.Join(a.MissingDevices, ","); got != c.wantDevices {
				t.Errorf("missing devices = %q, want %q", got, c.wantDevices)
			}
			if got := strings.Join(a.MissingGroups, ","); got != c.wantGroups {
				t.Errorf("missing groups = %q, want %q", got, c.wantGroups)
			}
		})
	}
	good := newAMDProbe(testEnv(fixture(t, "sysfs/amd-dedicated"), nil)).access()
	if len(good.MissingDevices) != 0 || len(good.MissingGroups) != 0 {
		t.Errorf("fixture access = %+v", good)
	}
}

func TestLspciModelParsing(t *testing.T) {
	for in, want := range map[string]string{
		`c1:00.0 "VGA compatible controller" "Advanced Micro Devices, Inc. [AMD/ATI]" "Strix Halo [Radeon 8060S Graphics]" -rc1 -p00 "V" "S"`: "Strix Halo [Radeon 8060S Graphics]",
		`03:00.0 "VGA" "Vendor" "  Model X  "`: "Model X",
		`03:00.0 "VGA" "Vendor"`:               "",
		`03:00.0 "VGA" "Vendor" "unterminated`: "",
		"":                                     "",
		"first \"a\" \"b\" \"c\"\nsecond \"x\" \"y\" \"z\"": "c",
	} {
		if got := lspciModel([]byte(in)); got != want {
			t.Errorf("lspciModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBackendDetectionAndMismatch(t *testing.T) {
	for _, c := range []struct {
		nvidia, amd bool
		want        string
	}{
		{true, false, BackendCUDA}, {true, true, BackendCUDA}, {false, true, BackendVulkan}, {false, false, BackendCPU},
	} {
		if got := BackendFor(c.nvidia, c.amd); got != c.want {
			t.Errorf("BackendFor(%v,%v) = %q, want %q", c.nvidia, c.amd, got, c.want)
		}
	}
	for _, c := range []struct {
		configured  string
		nvidia, amd bool
		want        bool
	}{
		{"cuda", false, false, true}, {"cuda", false, true, true}, {"cuda", true, false, false},
		{"vulkan", false, false, true}, {"vulkan", false, true, false}, {"vulkan", true, false, false},
		{"cpu", false, false, false}, {"", false, false, false}, {"rocm", false, false, false},
	} {
		if got := BackendMismatch(c.configured, c.nvidia, c.amd); got != c.want {
			t.Errorf("BackendMismatch(%q,%v,%v) = %v, want %v", c.configured, c.nvidia, c.amd, got, c.want)
		}
	}
}
