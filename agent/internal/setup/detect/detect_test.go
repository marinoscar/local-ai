package detect

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
	"time"
)

// fake is a scripted machine: files and command output.
type fake struct {
	files map[string]string
	cmds  map[string]string // "name arg arg" -> stdout
	exit  map[string]int
	avail map[string]uint64
	now   time.Time
}

func (f fake) env() Env {
	return Env{
		Exec: func(_ context.Context, name string, args ...string) (string, int, error) {
			k := strings.Join(append([]string{name}, args...), " ")
			out, ok := f.cmds[k]
			if !ok {
				return "", -1, ErrNotFound
			}
			return out, f.exit[k], nil
		},
		ReadFile: func(n string) ([]byte, error) {
			if s, ok := f.files[n]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		Glob: func(p string) ([]string, error) {
			var out []string
			for n := range f.files {
				if ok, _ := path.Match(p, n); ok {
					out = append(out, n)
				}
			}
			// directory-style globs: match the parent of any file below
			seen := map[string]bool{}
			for n := range f.files {
				for d := path.Dir(n); d != "/" && d != "."; d = path.Dir(d) {
					if ok, _ := path.Match(p, d); ok && !seen[d] {
						seen[d] = true
						out = append(out, d)
					}
				}
			}
			sort.Strings(out)
			return dedupe(out), nil
		},
		Getenv: func(string) string { return "" },
		EUID:   0,
		Now:    func() time.Time { return f.now },
		AvailBytes: func(p string) (uint64, error) {
			if v, ok := f.avail[p]; ok {
				return v, nil
			}
			return 0, errors.New("no such path")
		},
	}
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func TestOSRelease(t *testing.T) {
	cases := []struct {
		name, text string
		ok         bool
		codename   string
	}{
		{"24.04", "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n", true, "noble"},
		{"26.04", "ID=ubuntu\nVERSION_ID=\"26.04\"\nVERSION_CODENAME=resolute\n", true, "resolute"},
		{"25.10 interim refused", "ID=ubuntu\nVERSION_ID=\"25.10\"\nVERSION_CODENAME=questing\n", false, "questing"},
		{"22.04 refused", "ID=ubuntu\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\n", false, "jammy"},
		{"debian refused", "ID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\n", false, "bookworm"},
		{"empty", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ParseOSRelease(c.text)
			if r.SupportedUbuntu() != c.ok || r.Codename != c.codename {
				t.Fatalf("got %+v supported=%v", r, r.SupportedUbuntu())
			}
		})
	}
}

func TestArchAndContainer(t *testing.T) {
	f := fake{cmds: map[string]string{"dpkg --print-architecture": "arm64\n", "systemd-detect-virt --container": "none\n"}}
	e := f.env()
	if a := e.Arch(context.Background()); a != "arm64" || !SupportedArch(a) {
		t.Fatalf("arch %q", a)
	}
	if c := e.Container(context.Background()); c != "" {
		t.Fatalf("container %q", c)
	}
	f.cmds["systemd-detect-virt --container"] = "lxc\n"
	if c := f.env().Container(context.Background()); c != "lxc" {
		t.Fatalf("container %q", c)
	}
	if SupportedArch("i386") || SupportedArch("") {
		t.Fatal("i386 must be refused")
	}
	if (fake{}).env().Arch(context.Background()) != "" || (fake{}).env().Container(context.Background()) != "" {
		t.Fatal("missing tools must give empty answers")
	}
}

func TestLaptop(t *testing.T) {
	for _, n := range []string{"8", "9", "10", "14", "31", "32"} {
		l := fake{files: map[string]string{"/sys/class/dmi/id/chassis_type": n + "\n"}}.env().DetectLaptop()
		if !l.IsLaptop {
			t.Errorf("chassis %s should be a laptop", n)
		}
	}
	for _, n := range []string{"3", "7", "23", "x"} {
		l := fake{files: map[string]string{"/sys/class/dmi/id/chassis_type": n}}.env().DetectLaptop()
		if l.IsLaptop {
			t.Errorf("chassis %s should not be a laptop", n)
		}
	}
	bat := fake{files: map[string]string{
		"/sys/class/dmi/id/chassis_type":      "3",
		"/sys/class/power_supply/BAT0/type":   "Battery\n",
		"/sys/class/power_supply/BAT0/status": "Discharging\n",
	}}.env().DetectLaptop()
	if !bat.IsLaptop || !bat.OnBattery {
		t.Fatalf("battery: %+v", bat)
	}
	plugged := fake{files: map[string]string{
		"/sys/class/power_supply/BAT0/type":   "Battery\n",
		"/sys/class/power_supply/BAT0/status": "Charging\n",
		"/sys/class/power_supply/AC/type":     "Mains\n",
		"/sys/class/power_supply/AC/online":   "1\n",
	}}.env().DetectLaptop()
	if !plugged.IsLaptop || plugged.OnBattery {
		t.Fatalf("plugged in: %+v", plugged)
	}
	unplugged := fake{files: map[string]string{
		"/sys/class/power_supply/BAT0/type":   "Battery\n",
		"/sys/class/power_supply/BAT0/status": "Not charging\n",
		"/sys/class/power_supply/AC/type":     "Mains\n",
		"/sys/class/power_supply/AC/online":   "0\n",
	}}.env().DetectLaptop()
	if !unplugged.OnBattery {
		t.Fatalf("mains offline should be on battery: %+v", unplugged)
	}
	desktop := fake{files: map[string]string{"/sys/class/dmi/id/chassis_type": "3"}}.env().DetectLaptop()
	if desktop.IsLaptop || desktop.OnBattery {
		t.Fatalf("desktop: %+v", desktop)
	}
}

func TestMemTotal(t *testing.T) {
	if got := ParseMemTotalMiB("MemTotal:       16384000 kB\nMemFree: 1 kB\n"); got != 16000 {
		t.Fatalf("got %d", got)
	}
	for _, bad := range []string{"", "MemTotal:\n", "MemTotal: abc kB\n", "MemFree: 1 kB\n"} {
		if ParseMemTotalMiB(bad) != 0 {
			t.Fatalf("%q should be unknown", bad)
		}
	}
}

func TestClock(t *testing.T) {
	f := fake{cmds: map[string]string{"timedatectl show -p NTPSynchronized": "NTPSynchronized=no\n"}, now: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	c := f.env().Clock(context.Background())
	if !c.OK() || !c.NTPKnown || c.NTPSync {
		t.Fatalf("%+v", c)
	}
	f.now = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	f.cmds["timedatectl show -p NTPSynchronized"] = "NTPSynchronized=yes\n"
	c = f.env().Clock(context.Background())
	if c.OK() || !c.NTPSync {
		t.Fatalf("%+v", c)
	}
	if (fake{now: f.now}).env().Clock(context.Background()).NTPKnown {
		t.Fatal("no timedatectl means unknown")
	}
}

func TestDiskAvail(t *testing.T) {
	e := fake{avail: map[string]uint64{"/srv": 5, "/": 9}}.env()
	if n, p, ok := e.DiskAvail("/srv"); !ok || n != 5 || p != "/srv" {
		t.Fatal(n, p, ok)
	}
	e = fake{avail: map[string]uint64{"/": 9}}.env()
	if n, p, ok := e.DiskAvail("/srv"); !ok || n != 9 || p != "/" {
		t.Fatal(n, p, ok)
	}
	if _, _, ok := (fake{}).env().DiskAvail("/srv"); ok {
		t.Fatal("nothing measurable must be not ok")
	}
}

const ssSample = `LISTEN 0 4096 127.0.0.1:7070 0.0.0.0:* users:(("aiagent",pid=10,fd=7))
LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
LISTEN 0 4096 127.0.0.1:8888 0.0.0.0:* users:(("python3",pid=11,fd=4))
LISTEN 0 4096 [::]:8080 [::]:* users:(("llama-server",pid=12,fd=3))
LISTEN 0 4096 203.0.113.5:8081 0.0.0.0:*
`

func TestPorts(t *testing.T) {
	ls := ParseSS(ssSample)
	if len(ls) != 5 || ls[0].Process != "aiagent" || ls[3].Addr != "::" || ls[3].Port != 8080 {
		t.Fatalf("%+v", ls)
	}
	got := PortConflicts(ls)
	if len(got) != 1 || got[0].Port != 8888 || got[0].Process != "python3" {
		t.Fatalf("only the foreign holder of 8888 conflicts, got %+v", got)
	}
	free := PortConflicts(ParseSS(""))
	if len(free) != 0 {
		t.Fatal("free ports never conflict")
	}
	unknown := PortConflicts(ParseSS("LISTEN 0 1 127.0.0.1:7070 0.0.0.0:*\n"))
	if len(unknown) != 1 || unknown[0].Process != "an unknown program" {
		t.Fatalf("%+v", unknown)
	}
	legacy := PortConflicts(ParseSS(`LISTEN 0 1 0.0.0.0:8080 0.0.0.0:* users:(("llama-qwen",pid=1,fd=1))`))
	if len(legacy) != 0 {
		t.Fatal("legacy llama-qwen may hold 8080")
	}
	foreign8080 := PortConflicts(ParseSS(`LISTEN 0 1 0.0.0.0:8080 0.0.0.0:* users:(("nginx",pid=1,fd=1))`))
	if len(foreign8080) != 1 {
		t.Fatal("a foreign 8080 holder conflicts")
	}
}

func TestGPU(t *testing.T) {
	ctx := context.Background()
	smi := "nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap --format=csv,noheader"
	nvidiaPCI := "01:00.0 VGA compatible controller [0300]: NVIDIA Corporation TU104 [10de:1e84] (rev a1)\n"
	t.Run("nvidia ok", func(t *testing.T) {
		g := fake{cmds: map[string]string{"lspci -nn": nvidiaPCI, smi: "NVIDIA GeForce RTX 2080, 8192 MiB, 550.54, 7.5\n"}}.env().DetectGPU(ctx)
		if g.Backend != "cuda" || g.VRAMMiB != 8192 || g.ComputeCap != "7.5" || g.DriverBroken || g.Driver != "550.54" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("nvidia 3D controller class 0302", func(t *testing.T) {
		if !HasNvidiaPCI("00:02.0 3D controller [0302]: NVIDIA Corporation [10DE:1234]") {
			t.Fatal("0302 must count")
		}
		if HasNvidiaPCI("00:03.0 Audio device [0403]: NVIDIA Corporation [10de:10f0]") {
			t.Fatal("an audio function is not a GPU")
		}
	})
	t.Run("nvidia broken", func(t *testing.T) {
		f := fake{cmds: map[string]string{"lspci -nn": nvidiaPCI, smi: ""}, exit: map[string]int{smi: 9}}
		g := f.env().DetectGPU(ctx)
		if g.Backend != "cpu" || !g.DriverBroken || !g.NvidiaFound {
			t.Fatalf("%+v", g)
		}
		f2 := fake{cmds: map[string]string{"lspci -nn": nvidiaPCI}} // nvidia-smi missing
		if g := f2.env().DetectGPU(ctx); !g.DriverBroken {
			t.Fatalf("missing nvidia-smi is a broken driver: %+v", g)
		}
	})
	t.Run("amd with vram and gtt", func(t *testing.T) {
		g := fake{files: map[string]string{
			"/sys/class/drm/card0/device/vendor":              "0x1002\n",
			"/sys/class/drm/card0/device/mem_info_vram_total": "536870912\n",   // 512 MiB carve-out
			"/sys/class/drm/card0/device/mem_info_gtt_total":  "34359738368\n", // 32 GiB GTT
		}, cmds: map[string]string{"lspci -nn": "", "vulkaninfo --summary": "GPU0:\n\tdeviceName = AMD Radeon 890M\n"}}.env().DetectGPU(ctx)
		if g.Backend != "vulkan" || g.Vendor != "amd" || g.VRAMMiB != 32768 || g.Name != "AMD Radeon 890M" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("amd discrete uses vram", func(t *testing.T) {
		g := fake{files: map[string]string{
			"/sys/class/drm/card0/device/vendor":              "0x1002",
			"/sys/class/drm/card0/device/mem_info_vram_total": "17163091968",
			"/sys/class/drm/card0/device/mem_info_gtt_total":  "4294967296",
		}}.env().DetectGPU(ctx)
		if g.Backend != "vulkan" || g.VRAMMiB != 16368 {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("intel", func(t *testing.T) {
		g := fake{files: map[string]string{"/sys/class/drm/card0/device/vendor": "0x8086"}}.env().DetectGPU(ctx)
		if g.Backend != "vulkan" || g.Vendor != "intel" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("llvmpipe only is cpu", func(t *testing.T) {
		g := fake{files: map[string]string{"/sys/class/drm/card0/device/vendor": "0x8086"},
			cmds: map[string]string{"vulkaninfo --summary": "deviceName = llvmpipe (LLVM 17)\n"}}.env().DetectGPU(ctx)
		if g.Backend != "cpu" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("none", func(t *testing.T) {
		g := (fake{}).env().DetectGPU(ctx)
		if g.Backend != "cpu" || g.Vendor != "" || g.NvidiaFound {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("other vendor ignored", func(t *testing.T) {
		g := fake{files: map[string]string{"/sys/class/drm/card0/device/vendor": "0x1234"}}.env().DetectGPU(ctx)
		if g.Backend != "cpu" {
			t.Fatalf("%+v", g)
		}
	})
}
