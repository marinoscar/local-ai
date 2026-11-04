package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/setup/detect"
)

const validCatalog = "../../../../catalog"

// machine is a scripted computer.
type machine struct {
	files map[string]string
	cmds  map[string]string
	exit  map[string]int
	avail map[string]uint64
	euid  int
	env   map[string]string
	year  int
	real  bool // fall back to the real filesystem for paths not scripted (ark tests)
}

func (m machine) detectEnv() detect.Env {
	return detect.Env{
		Exec: func(_ context.Context, name string, args ...string) (string, int, error) {
			k := strings.Join(append([]string{name}, args...), " ")
			out, ok := m.cmds[k]
			if !ok {
				return "", -1, detect.ErrNotFound
			}
			return out, m.exit[k], nil
		},
		ReadFile: func(n string) ([]byte, error) {
			if s, ok := m.files[n]; ok {
				return []byte(s), nil
			}
			if m.real {
				return os.ReadFile(n)
			}
			return nil, fs.ErrNotExist
		},
		Glob: func(p string) ([]string, error) {
			seen := map[string]bool{}
			var out []string
			for n := range m.files {
				for d := path.Dir(n); d != "/" && d != "."; d = path.Dir(d) {
					if ok, _ := path.Match(p, d); ok && !seen[d] {
						seen[d] = true
						out = append(out, d)
					}
				}
			}
			return out, nil
		},
		Getenv: func(k string) string { return m.env[k] },
		EUID:   m.euid,
		Now: func() time.Time {
			y := m.year
			if y == 0 {
				y = 2026
			}
			return time.Date(y, 6, 1, 0, 0, 0, 0, time.UTC)
		},
		AvailBytes: func(p string) (uint64, error) {
			if v, ok := m.avail[p]; ok {
				return v, nil
			}
			return 0, errors.New("no path")
		},
	}
}

const smi = "nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap --format=csv,noheader"

// good is a healthy 24.04 desktop with a working NVIDIA card (the ai-n3 class).
func good() machine {
	return machine{
		euid: 0,
		env:  map[string]string{"SUDO_USER": "alice"},
		files: map[string]string{
			"/etc/os-release": "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n",
			"/proc/meminfo":   "MemTotal:       14680064 kB\n",
		},
		cmds: map[string]string{
			"dpkg --print-architecture":           "amd64\n",
			"systemd-detect-virt --container":     "none\n",
			"lspci -nn":                           "01:00.0 VGA compatible controller [0300]: NVIDIA Corporation [10de:1e84]\n",
			smi:                                   "NVIDIA GeForce RTX 2080, 8192 MiB, 550.54, 7.5\n",
			"timedatectl show -p NTPSynchronized": "NTPSynchronized=yes\n",
			"ss -Hltnp":                           "LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=1,fd=3))\n",
			"mokutil --sb-state":                  "SecureBoot disabled\n",
		},
		avail: map[string]uint64{"/srv": 900_000_000_000},
	}
}

func deps(m machine) Deps {
	return Deps{Env: m.detectEnv(), Internet: func(context.Context) error { return nil }, CatalogDir: "/nonexistent/catalog"}
}

func find(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestPreflightGoodMachine(t *testing.T) {
	r := Preflight(context.Background(), deps(good()), PreflightOptions{})
	if !r.OK || r.Exit != 0 {
		t.Fatalf("%+v", r)
	}
	if r.GPU.Backend != "cuda" || r.GPU.ComputeCap != "7.5" || r.System.Codename != "noble" || r.System.DesktopUser != "alice" {
		t.Fatalf("%+v %+v", r.GPU, r.System)
	}
	for _, c := range r.Checks {
		if c.Status == StatusFail || c.Status == StatusWarn {
			t.Errorf("unexpected %+v", c)
		}
	}
}

func TestPreflightRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*machine)
		check  string
		code   string
	}{
		{"debian", func(m *machine) { m.files["/etc/os-release"] = "ID=debian\nVERSION_ID=\"12\"\n" }, "ubuntu", doctor.INS001},
		{"25.10 interim", func(m *machine) {
			m.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"25.10\"\nVERSION_CODENAME=questing\n"
		}, "ubuntu", doctor.INS001},
		{"no os-release", func(m *machine) { delete(m.files, "/etc/os-release") }, "ubuntu", doctor.INS001},
		{"i386", func(m *machine) { m.cmds["dpkg --print-architecture"] = "i386\n" }, "arch", doctor.INS001},
		{"container", func(m *machine) { m.cmds["systemd-detect-virt --container"] = "docker\n" }, "container", doctor.INS002},
		{"not root", func(m *machine) { m.euid = 1000 }, "root", doctor.INS003},
		{"root without desktop user", func(m *machine) { m.env = nil }, "root", doctor.INS003},
		{"sudo from root", func(m *machine) { m.env = map[string]string{"SUDO_USER": "root"} }, "root", doctor.INS003},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good()
			c.mutate(&m)
			r := Preflight(context.Background(), deps(m), PreflightOptions{})
			ch := find(r, c.check)
			if ch.Status != StatusFail || ch.Code != c.code || ch.Fix == "" {
				t.Fatalf("%+v", ch)
			}
			if r.OK || r.Exit != ExitRefused {
				t.Fatalf("a refusal exits 3, got ok=%v exit=%d", r.OK, r.Exit)
			}
		})
	}
	t.Run("desktop-user flag satisfies root-without-sudo", func(t *testing.T) {
		m := good()
		m.env = nil
		r := Preflight(context.Background(), deps(m), PreflightOptions{DesktopUser: "bob"})
		if !r.OK || r.System.DesktopUser != "bob" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestPreflightReportsEveryFailureAtOnce(t *testing.T) {
	m := good()
	m.euid = 1000
	m.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"22.04\"\n"
	m.cmds["systemd-detect-virt --container"] = "lxc\n"
	m.cmds["ss -Hltnp"] = "LISTEN 0 1 127.0.0.1:7070 0.0.0.0:* users:((\"python3\",pid=1,fd=1))\n"
	m.avail["/srv"] = 1
	d := deps(m)
	d.Internet = func(context.Context) error { return errors.New("offline") }
	r := Preflight(context.Background(), d, PreflightOptions{})
	var failed []string
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			failed = append(failed, c.ID)
		}
	}
	if strings.Join(failed, ",") != "root,ubuntu,container,disk,ports,internet" || r.Exit != ExitRefused {
		t.Fatalf("failed=%v exit=%d", failed, r.Exit)
	}
}

func TestPreflightWarningsDoNotBlock(t *testing.T) {
	m := good()
	m.year = 1970
	m.files["/proc/meminfo"] = "MemTotal: 4000000 kB\n"
	m.files["/sys/class/power_supply/BAT0/type"] = "Battery\n"
	m.files["/sys/class/power_supply/BAT0/status"] = "Discharging\n"
	r := Preflight(context.Background(), deps(m), PreflightOptions{})
	if !r.OK || r.Exit != 0 {
		t.Fatalf("%+v", r)
	}
	if c := find(r, "clock"); c.Status != StatusWarn || c.Code != doctor.INS017 {
		t.Fatalf("%+v", c)
	}
	if c := find(r, "ram"); c.Status != StatusWarn || c.Code != doctor.INS012 {
		t.Fatalf("%+v", c)
	}
	if c := find(r, "power"); c.Status != StatusWarn || !strings.Contains(c.Message, "Plug in the charger") {
		t.Fatalf("%+v", c)
	}
	if !r.System.Laptop || !r.System.OnBattery {
		t.Fatalf("%+v", r.System)
	}
}

func TestPreflightDisk(t *testing.T) {
	const gb = 1_000_000_000
	cases := []struct {
		name   string
		avail  uint64
		min    int64
		yes    bool
		want   string
		wantRC int
	}{
		{"exactly enough", 120 * gb, 100 * gb, false, StatusOK, 0},
		{"one byte short", 120*gb - 1, 100 * gb, false, StatusFail, ExitFailed},
		{"short but --yes", 10 * gb, 100 * gb, true, StatusWarn, 0},
		{"no profile yet needs the 20 GB floor", 19 * gb, 0, false, StatusFail, ExitFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good()
			m.avail["/srv"] = c.avail
			r := Preflight(context.Background(), deps(m), PreflightOptions{MinFreeBytes: c.min, Yes: c.yes})
			if ch := find(r, "disk"); ch.Status != c.want || (c.want != StatusOK && ch.Code != doctor.INS013) || r.Exit != c.wantRC {
				t.Fatalf("%+v exit=%d", ch, r.Exit)
			}
		})
	}
	t.Run("falls back to / when /srv is not measurable", func(t *testing.T) {
		m := good()
		m.avail = map[string]uint64{"/": 500 * gb}
		r := Preflight(context.Background(), deps(m), PreflightOptions{})
		if r.System.DiskPath != "/" || !r.OK {
			t.Fatalf("%+v", r.System)
		}
	})
}

func TestPreflightGPUCases(t *testing.T) {
	t.Run("broken nvidia driver warns and falls to cpu", func(t *testing.T) {
		m := good()
		m.cmds[smi] = ""
		m.exit = map[string]int{smi: 9}
		r := Preflight(context.Background(), deps(m), PreflightOptions{})
		if c := find(r, "gpu"); c.Status != StatusWarn || c.Code != doctor.INS010 {
			t.Fatalf("%+v", c)
		}
		if r.GPU.Backend != "cpu" || !r.GPU.DriverBroken || !r.OK {
			t.Fatalf("%+v", r.GPU)
		}
	})
	t.Run("secure boot with unloaded nvidia module", func(t *testing.T) {
		m := good()
		m.cmds[smi] = ""
		m.exit = map[string]int{smi: 9}
		m.cmds["mokutil --sb-state"] = "SecureBoot enabled\n"
		r := Preflight(context.Background(), deps(m), PreflightOptions{})
		if c := find(r, "secure_boot"); c.Status != StatusWarn || c.Code != doctor.INS011 {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("amd unified memory", func(t *testing.T) {
		m := good()
		m.cmds["lspci -nn"] = ""
		delete(m.cmds, smi)
		m.files["/sys/class/drm/card0/device/vendor"] = "0x1002\n"
		m.files["/sys/class/drm/card0/device/mem_info_gtt_total"] = "68719476736\n"
		r := Preflight(context.Background(), deps(m), PreflightOptions{})
		if r.GPU.Backend != "vulkan" || r.GPU.VRAMMiB != 65536 {
			t.Fatalf("%+v", r.GPU)
		}
	})
	t.Run("no gpu and forced backend", func(t *testing.T) {
		m := good()
		m.cmds["lspci -nn"] = ""
		delete(m.cmds, smi)
		r := Preflight(context.Background(), deps(m), PreflightOptions{})
		if r.GPU.Backend != "cpu" || find(r, "gpu").Status != StatusInfo {
			t.Fatalf("%+v", r.GPU)
		}
		r = Preflight(context.Background(), deps(m), PreflightOptions{Backend: "vulkan"})
		if r.GPU.Backend != "vulkan" {
			t.Fatalf("%+v", r.GPU)
		}
	})
}

func TestPreflightUnknownPortHolderAsNormalUser(t *testing.T) {
	m := good()
	m.euid = 1000
	m.cmds["ss -Hltnp"] = "LISTEN 0 1 0.0.0.0:8080 0.0.0.0:*\n"
	c := find(Preflight(context.Background(), deps(m), PreflightOptions{}), "ports")
	if c.Status != StatusWarn || c.Code != doctor.INS014 {
		t.Fatalf("a holder a normal user cannot see only warns: %+v", c)
	}
	m.euid = 0
	if c := find(Preflight(context.Background(), deps(m), PreflightOptions{}), "ports"); c.Status != StatusFail {
		t.Fatalf("root can see the holder, so an unknown one fails: %+v", c)
	}
}

func TestPreflightLegacy(t *testing.T) {
	m := good()
	m.files["/etc/systemd/system/llama-qwen.service"] = "[Service]\nExecStart=/opt/llama/llama-server -m x.gguf --port 8080\n"
	m.cmds["ss -Hltnp"] = "LISTEN 0 1 0.0.0.0:8080 0.0.0.0:* users:((\"llama-server\",pid=1,fd=1))\n"
	r := Preflight(context.Background(), deps(m), PreflightOptions{})
	if !r.System.LegacyService || !strings.HasPrefix(r.System.LegacyExec, "/opt/llama/llama-server") || !r.OK {
		t.Fatalf("%+v ok=%v", r.System, r.OK)
	}
}

func TestPreflightArk(t *testing.T) {
	root := t.TempDir()
	m := good()
	m.real = true
	d := deps(m)
	d.Internet = func(context.Context) error { t.Fatal("an offline install must not check the internet"); return nil }
	d.KitCheck = func(context.Context, string, kit.NodeFacts) ([]string, error) { return nil, nil }

	r := Preflight(context.Background(), d, PreflightOptions{Ark: root})
	if c := find(r, "ark"); c.Status != StatusFail || c.Code != doctor.INS016 ||
		!strings.Contains(c.Message, "ROOT.json") || !strings.Contains(c.Message, "CATALOG_VERSION") || r.Exit != ExitFailed {
		t.Fatalf("%+v", c)
	}
	kitDir := filepath.Join(root, "localai", "kit", "catalog")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{filepath.Join(root, "localai", "ROOT.json"): "{}", filepath.Join(kitDir, "CATALOG_VERSION"): "2026.11.02-1\n"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var gotFacts kit.NodeFacts
	d.KitCheck = func(_ context.Context, _ string, f kit.NodeFacts) ([]string, error) {
		gotFacts = f
		return []string{"kit-debs-noble-amd64", "kit/bin/aiagent-linux-amd64"}, nil
	}
	r = Preflight(context.Background(), d, PreflightOptions{Ark: root})
	c := find(r, "ark")
	if c.Status != StatusFail || !strings.Contains(c.Message, "kit-debs-noble-amd64") || !strings.Contains(c.Message, "kit/bin/aiagent-linux-amd64") {
		t.Fatalf("%+v", c)
	}
	if gotFacts.Codename != "noble" || gotFacts.Arch != "amd64" || gotFacts.Backend != "cuda" {
		t.Fatalf("%+v", gotFacts)
	}
	d.KitCheck = func(context.Context, string, kit.NodeFacts) ([]string, error) { return nil, nil }
	r = Preflight(context.Background(), d, PreflightOptions{Ark: root})
	if !r.OK || r.Source != "ark" || find(r, "ark").Status != StatusOK {
		t.Fatalf("%+v", r.Checks)
	}
}

func TestVerifyKitReportsMissingKit(t *testing.T) {
	root := t.TempDir()
	got, err := verifyKit(context.Background(), root, kit.NodeFacts{Codename: "noble", Arch: "amd64", Backend: "cpu"})
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "kit") {
		t.Fatalf("%v %v", got, err)
	}
}

func run(t *testing.T, d Deps, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	rc := runInst02(context.Background(), d, args, &o, &e)
	return rc, o.String(), e.String()
}

func TestRunPreflightJSONAndExitCodes(t *testing.T) {
	rc, out, _ := run(t, deps(good()), "preflight", "--json")
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rc != 0 || !rep.OK || len(rep.Checks) < 10 {
		t.Fatalf("rc=%d err=%v out=%s", rc, err, out)
	}
	m := good()
	m.cmds["systemd-detect-virt --container"] = "docker\n"
	if rc, out, _ := run(t, deps(m), "preflight"); rc != ExitRefused || !strings.Contains(out, "LAI-INS-002") || !strings.Contains(out, "fix:") {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if rc, _, e := run(t, deps(good()), "preflight", "--bogus"); rc != ExitUsage || e == "" {
		t.Fatal(rc)
	}
	if rc, _, _ := run(t, deps(good()), "preflight", "--backend", "tpu"); rc != ExitUsage {
		t.Fatal(rc)
	}
	if rc, _, _ := run(t, deps(good())); rc != ExitUsage {
		t.Fatal(rc)
	}
	if rc, _, _ := run(t, deps(good()), "frobnicate"); rc != ExitUsage {
		t.Fatal(rc)
	}
}

func suggestDeps(m machine) Deps {
	d := deps(m)
	d.CatalogDir = validCatalog
	d.Env.ReadFile = func(n string) ([]byte, error) {
		if s, ok := m.files[n]; ok {
			return []byte(s), nil
		}
		return os.ReadFile(n)
	}
	return d
}

func TestProfileSuggest(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*machine)
		profile string
	}{
		{"cuda 8 GB card", func(m *machine) {}, "small-8gb"},
		{"cuda 12 GB card", func(m *machine) { m.cmds[smi] = "NVIDIA RTX 3060, 12288 MiB, 550.54, 8.6\n" }, "cuda-12gb"},
		{"no gpu", func(m *machine) { m.cmds["lspci -nn"] = ""; delete(m.cmds, smi) }, "cpu-only"},
		{"broken driver", func(m *machine) { m.cmds[smi] = ""; m.exit = map[string]int{smi: 1} }, "cpu-only"},
		{"unified memory amd", func(m *machine) {
			m.cmds["lspci -nn"] = ""
			delete(m.cmds, smi)
			m.files["/sys/class/drm/card0/device/vendor"] = "0x1002"
			m.files["/sys/class/drm/card0/device/mem_info_gtt_total"] = "103079215104"
			m.files["/proc/meminfo"] = "MemTotal: 134217728 kB\n"
		}, "max-128gb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good()
			c.mutate(&m)
			rc, out, errs := run(t, suggestDeps(m), "profile-suggest", "--json")
			var s Suggestion
			if rc != 0 {
				t.Fatalf("rc=%d %s", rc, errs)
			}
			if err := json.Unmarshal([]byte(out), &s); err != nil {
				t.Fatal(err, out)
			}
			if s.Profile != c.profile || s.Mismatch || s.Override || len(s.Reasons) == 0 || s.DisplayName == "" || s.CatalogVersion == "" || len(s.Survival) == 0 || s.BudgetBytes <= 0 {
				t.Fatalf("%+v", s)
			}
		})
	}
	t.Run("plain words", func(t *testing.T) {
		rc, out, _ := run(t, suggestDeps(good()), "profile-suggest")
		if rc != 0 || !strings.Contains(out, "content budget") || !strings.Contains(out, "survival models") {
			t.Fatal(out)
		}
	})
	t.Run("override that fits", func(t *testing.T) {
		rc, out, _ := run(t, suggestDeps(good()), "profile-suggest", "--json", "--profile", "small-8gb")
		var s Suggestion
		_ = json.Unmarshal([]byte(out), &s)
		if rc != 0 || s.Mismatch || !s.Override || s.Profile != "small-8gb" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("override that does not fit asks for confirmation", func(t *testing.T) {
		rc, out, _ := run(t, suggestDeps(good()), "profile-suggest", "--json", "--profile", "max-128gb")
		var s Suggestion
		_ = json.Unmarshal([]byte(out), &s)
		if rc != 0 || !s.Mismatch || s.MismatchCode != doctor.CAT005 || s.Profile != "max-128gb" || s.Suggested == "max-128gb" {
			t.Fatalf("%+v", s)
		}
		_, plain, _ := run(t, suggestDeps(good()), "profile-suggest", "--profile", "max-128gb")
		if !strings.Contains(plain, "WARNING "+doctor.CAT005) {
			t.Fatal(plain)
		}
	})
	t.Run("unknown profile", func(t *testing.T) {
		rc, _, e := run(t, suggestDeps(good()), "profile-suggest", "--profile", "nope")
		if rc != ExitFailed || !strings.Contains(e, doctor.CAT003) {
			t.Fatal(rc, e)
		}
	})
	t.Run("backend flag forces cpu", func(t *testing.T) {
		_, out, _ := run(t, suggestDeps(good()), "profile-suggest", "--json", "--backend", "cpu")
		var s Suggestion
		_ = json.Unmarshal([]byte(out), &s)
		if s.Profile != "cpu-only" || s.Backend != "cpu" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("catalog from the ark kit when none is installed", func(t *testing.T) {
		ark := t.TempDir()
		kc := filepath.Join(ark, "localai", "kit")
		if err := os.MkdirAll(kc, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(kc, "catalog"), os.DirFS(validCatalog)); err != nil {
			t.Fatal(err)
		}
		d := suggestDeps(good())
		d.CatalogDir = filepath.Join(t.TempDir(), "none")
		rc, out, e := run(t, d, "profile-suggest", "--json", "--from-ark", ark)
		if rc != 0 || !strings.Contains(out, `"profile"`) {
			t.Fatal(rc, e, out)
		}
	})
	t.Run("missing catalog", func(t *testing.T) {
		d := suggestDeps(good())
		d.CatalogDir = filepath.Join(t.TempDir(), "none")
		if rc, _, e := run(t, d, "profile-suggest"); rc != ExitFailed || e == "" {
			t.Fatal(rc, e)
		}
	})
}

func TestCatalogInstall(t *testing.T) {
	ark := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ark, "localai", "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(ark, "localai", "kit", "catalog"), os.DirFS(validCatalog)); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "etc", "catalog")
	d := Deps{Env: good().detectEnv(), CatalogDir: live, IsRoot: func() bool { return true }}

	rc, out, _ := run(t, d, "catalog", "--from", "ark", "--ark", ark, "--dry-run")
	if rc != 0 || !strings.Contains(out, "[dry-run] would install") {
		t.Fatal(rc, out)
	}
	if _, err := os.Stat(live); err == nil {
		t.Fatal("dry run must not create the catalog")
	}
	rc, out, e := run(t, d, "catalog", "--from", "ark", "--ark", ark)
	if rc != 0 || !strings.Contains(out, "installed catalog") {
		t.Fatal(rc, out, e)
	}
	if v := readVersion(live); v != readVersion(validCatalog) {
		t.Fatalf("installed %q", v)
	}
	rc, out, _ = run(t, d, "catalog", "--from", "ark", "--ark", ark)
	if rc != 0 || !strings.Contains(out, "already installed") {
		t.Fatal("second run must be a no-op:", rc, out)
	}

	// a damaged catalog is rejected and the live one stays
	if err := os.WriteFile(filepath.Join(ark, "localai", "kit", "catalog", "CATALOG_VERSION"), []byte("2099.01.01-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ark, "localai", "kit", "catalog", "models.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, _, e = run(t, d, "catalog", "--from", "ark", "--ark", ark)
	if rc != ExitFailed || e == "" || readVersion(live) != readVersion(validCatalog) {
		t.Fatal("a rejected catalog must leave the live one:", rc, e)
	}

	// no catalog on the drive
	rc, _, e = run(t, d, "catalog", "--from", "ark", "--ark", t.TempDir())
	if rc != ExitFailed || !strings.Contains(e, "sync the drive") {
		t.Fatal(rc, e)
	}

	// release source
	d.FetchRelease = func(_ context.Context, dir string) error { return os.CopyFS(dir, os.DirFS(validCatalog)) }
	live2 := filepath.Join(t.TempDir(), "c")
	d.CatalogDir = live2
	if rc, out, e := run(t, d, "catalog", "--from", "release"); rc != 0 || readVersion(live2) == "" {
		t.Fatal(rc, out, e)
	}
	d.FetchRelease = func(context.Context, string) error { return errors.New("no route to host") }
	d.CatalogDir = filepath.Join(t.TempDir(), "c3")
	if rc, _, e := run(t, d, "catalog", "--from", "release"); rc != ExitFailed || !strings.Contains(e, "could not download") {
		t.Fatal(rc, e)
	}

	// usage and root
	if rc, _, _ := run(t, d, "catalog"); rc != ExitUsage {
		t.Fatal(rc)
	}
	if rc, _, _ := run(t, d, "catalog", "--from", "usb"); rc != ExitFailed {
		t.Fatal(rc)
	}
	d.IsRoot = func() bool { return false }
	if rc, _, _ := run(t, d, "catalog", "--from", "ark", "--ark", ark); rc != ExitNotRoot {
		t.Fatal(rc)
	}
}
