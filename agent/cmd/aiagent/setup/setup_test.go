package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

type fakeSystemctl struct{ calls []string }

func (f *fakeSystemctl) DaemonReload(context.Context) error {
	f.calls = append(f.calls, "daemon-reload")
	return nil
}
func (f *fakeSystemctl) Enable(_ context.Context, u ...string) error {
	f.calls = append(f.calls, "enable "+strings.Join(u, " "))
	return nil
}
func (f *fakeSystemctl) EnableNow(_ context.Context, u ...string) error {
	f.calls = append(f.calls, "enable --now "+strings.Join(u, " "))
	return nil
}

type rig struct {
	e    *Env
	out  *bytes.Buffer
	errb *bytes.Buffer
	sc   *fakeSystemctl
	root string
}

func newRig(t *testing.T, root bool) *rig {
	t.Helper()
	r := &rig{out: &bytes.Buffer{}, errb: &bytes.Buffer{}, sc: &fakeSystemctl{}, root: t.TempDir()}
	r.e = &Env{
		Root: r.root, Stdout: r.out, Stderr: r.errb, Systemctl: r.sc,
		IsRoot:  func() bool { return root },
		GroupID: func(string) (int, bool) { return 0, false },
		Random:  bytes.NewReader(bytes.Repeat([]byte{0xab, 0xcd}, 16)),
	}
	return r
}

func (r *rig) run(args ...string) int {
	return RunEnv(context.Background(), r.e, args)
}

func (r *rig) path(p string) string { return filepath.Join(r.root, p) }

func (r *rig) write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	full := r.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(r.path(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDispatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no step", nil, ExitUsage},
		{"help", []string{"--help"}, ExitOK},
		{"unknown", []string{"nope"}, ExitUsage},
	}
	for _, c := range cases {
		r := newRig(t, true)
		if got := r.run(c.args...); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}

func TestNotRootIs77ButDryRunIsFine(t *testing.T) {
	for _, step := range []string{"node-yaml", "units", "api-key"} {
		r := newRig(t, false)
		args := []string{step}
		if step == "node-yaml" {
			args = append(args, "--node-id", "ai-n4")
		}
		if got := r.run(args...); got != ExitNotRoot {
			t.Errorf("%s as non-root: exit %d, want 77", step, got)
		}
		r = newRig(t, false)
		if got := r.run(append(args, "--dry-run")...); got != ExitOK {
			t.Errorf("%s --dry-run as non-root: exit %d, want 0 (%s)", step, got, r.errb)
		}
	}
}

func TestNodeYAML(t *testing.T) {
	t.Run("creates the file", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("node-yaml", "--node-id", "ai-n4", "--profile", "cuda-12gb", "--backend", "cuda", "--serve-pattern", "B"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		got := r.read(t, "etc/localai/node.yaml")
		for _, want := range []string{"schema_version: 1", "id: ai-n4", "profile: cuda-12gb", "backend: cuda", "serve_pattern: B"} {
			if !strings.Contains(got, want) {
				t.Errorf("node.yaml lacks %q:\n%s", want, got)
			}
		}
		info, _ := os.Stat(r.path("etc/localai/node.yaml"))
		if info.Mode().Perm() != 0o640 {
			t.Errorf("mode %v, want 0640", info.Mode().Perm())
		}
	})
	t.Run("re-run keeps comments and existing values and fills only missing keys", func(t *testing.T) {
		r := newRig(t, true)
		r.write(t, "etc/localai/node.yaml", "# my notes\nschema_version: 1\nnode:\n  id: ai-n4 # keep\nprofile: desk-small\n", 0o640)
		if rc := r.run("node-yaml", "--node-id", "ai-n4", "--profile", "OTHER", "--backend", "cpu"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		got := r.read(t, "etc/localai/node.yaml")
		if !strings.Contains(got, "# my notes") || !strings.Contains(got, "# keep") {
			t.Errorf("comments lost:\n%s", got)
		}
		if strings.Contains(got, "OTHER") || !strings.Contains(got, "profile: desk-small") {
			t.Errorf("an existing value was changed:\n%s", got)
		}
		if !strings.Contains(got, "backend: cpu") {
			t.Errorf("missing key not filled:\n%s", got)
		}
	})
	t.Run("a different node id is LAI-INS-020 and changes nothing", func(t *testing.T) {
		r := newRig(t, true)
		orig := "schema_version: 1\nnode:\n  id: ai-n4\nprofile: p\n"
		r.write(t, "etc/localai/node.yaml", orig, 0o640)
		if rc := r.run("node-yaml", "--node-id", "ai-n5"); rc != ExitFailed {
			t.Fatalf("exit %d, want 1", rc)
		}
		if !strings.Contains(r.errb.String(), "LAI-INS-020") {
			t.Errorf("no doctor code: %s", r.errb)
		}
		if r.read(t, "etc/localai/node.yaml") != orig {
			t.Error("node.yaml changed")
		}
	})
	t.Run("no node id anywhere is a usage error", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("node-yaml"); rc != ExitUsage {
			t.Errorf("exit %d, want 2", rc)
		}
	})
	t.Run("node id omitted on a re-run reuses the existing one", func(t *testing.T) {
		r := newRig(t, true)
		r.write(t, "etc/localai/node.yaml", "schema_version: 1\nnode:\n  id: ai-n4\nprofile: p\n", 0o640)
		if rc := r.run("node-yaml", "--backend", "vulkan"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
	})
	for _, bad := range [][]string{
		{"--node-id", "ai-n0"}, {"--node-id", "ai-n4", "--backend", "tpu"}, {"--node-id", "ai-n4", "--serve-pattern", "C"}, {"--node-id", "ai-n4", "extra"},
	} {
		r := newRig(t, true)
		if rc := r.run(append([]string{"node-yaml"}, bad...)...); rc != ExitUsage {
			t.Errorf("%v: exit %d, want 2", bad, rc)
		}
	}
	t.Run("inventory fills the profile, backend and pattern", func(t *testing.T) {
		r := newRig(t, true)
		inv := filepath.Join(t.TempDir(), "inv.yaml")
		_ = os.WriteFile(inv, []byte("nodes:\n  - id: ai-n4\n    public_host: ai-n4.example.test\n    backend: vulkan\n    profile: max-128gb\n    serve_pattern: A\n"), 0o600)
		if rc := r.run("node-yaml", "--node-id", "ai-n4", "--inventory", inv, "--backend", "cpu"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		got := r.read(t, "etc/localai/node.yaml")
		for _, want := range []string{"profile: max-128gb", "backend: cpu", "serve_pattern: A", "public_host: ai-n4.example.test"} {
			if !strings.Contains(got, want) {
				t.Errorf("lacks %q:\n%s", want, got)
			}
		}
	})
	t.Run("dry run writes nothing", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("node-yaml", "--node-id", "ai-n4", "--dry-run"); rc != ExitOK {
			t.Fatalf("exit %d", rc)
		}
		if _, err := os.Stat(r.path("etc/localai/node.yaml")); err == nil {
			t.Error("dry run wrote node.yaml")
		}
		if !strings.Contains(r.out.String(), "[dry-run] would create") || !strings.Contains(r.out.String(), "+ node.id") {
			t.Errorf("dry run output: %s", r.out)
		}
	})
}

func TestUnits(t *testing.T) {
	wantFiles := []string{
		"localai-agent.service", "localai-llama.service", "localai-kiwix.service", "localai-whisper.service",
		"localai-sync.service", "localai-sync.timer", "localai-drill.service", "localai-drill.timer",
	}
	t.Run("writes every unit, reloads and enables", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("units", "--backup-dir", "/bk"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		for _, f := range wantFiles {
			if r.read(t, "etc/systemd/system/"+f) == "" {
				t.Errorf("%s empty", f)
			}
		}
		want := []string{
			"daemon-reload",
			"enable localai-agent.service localai-llama.service localai-kiwix.service localai-whisper.service",
			"enable --now localai-sync.timer localai-drill.timer",
		}
		if strings.Join(r.sc.calls, "|") != strings.Join(want, "|") {
			t.Errorf("systemctl calls %v, want %v", r.sc.calls, want)
		}
		for _, f := range []string{"localai-sync.service", "localai-drill.service"} {
			if !strings.Contains(r.read(t, "etc/systemd/system/"+f), "SuccessExitStatus=8") {
				t.Errorf("%s lost SuccessExitStatus=8", f)
			}
		}
	})
	t.Run("second run changes nothing and does not reload", func(t *testing.T) {
		r := newRig(t, true)
		r.run("units", "--backup-dir", "/bk")
		r.sc.calls = nil
		r.out.Reset()
		if rc := r.run("units", "--backup-dir", "/bk"); rc != ExitOK {
			t.Fatalf("exit %d", rc)
		}
		if strings.Contains(r.out.String(), "wrote ") || len(r.sc.calls) != 2 {
			t.Errorf("not idempotent: %s / %v", r.out, r.sc.calls)
		}
	})
	t.Run("a changed file is backed up", func(t *testing.T) {
		r := newRig(t, true)
		r.write(t, "etc/systemd/system/localai-agent.service", "hand edited\n", 0o644)
		if rc := r.run("units", "--backup-dir", "/bk"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		var found string
		_ = filepath.Walk(r.path("bk"), func(p string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() && strings.HasSuffix(p, "localai-agent.service") {
				b, _ := os.ReadFile(p)
				found = string(b)
			}
			return nil
		})
		if found != "hand edited\n" {
			t.Errorf("backup content %q", found)
		}
	})
	t.Run("dry run writes and calls nothing", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("units", "--dry-run"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		if _, err := os.Stat(r.path("etc/systemd/system")); err == nil {
			t.Error("dry run created the unit folder")
		}
		if len(r.sc.calls) != 0 || !strings.Contains(r.out.String(), "[dry-run] would run: systemctl daemon-reload") {
			t.Errorf("calls %v out %s", r.sc.calls, r.out)
		}
	})
	t.Run("extra_env makes the drop-in and kiwix threads are used", func(t *testing.T) {
		r := newRig(t, true)
		r.write(t, "etc/localai/node.yaml", "schema_version: 1\nnode:\n  id: ai-n4\nprofile: p\nllama:\n  extra_env:\n    HSA_X: \"1\"\nkiwix:\n  threads: 6\n", 0o640)
		if rc := r.run("units", "--backup-dir", "/bk"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		if !strings.Contains(r.read(t, "etc/systemd/system/localai-llama.service.d/50-env.conf"), "Environment=HSA_X=1") {
			t.Error("drop-in missing")
		}
		if !strings.Contains(r.read(t, "etc/systemd/system/localai-kiwix.service"), "--threads=6") {
			t.Error("kiwix threads ignored")
		}
	})
	t.Run("rendering is deterministic", func(t *testing.T) {
		a, _, _ := renderUnits(t.TempDir())
		b, _, _ := renderUnits(t.TempDir())
		if len(a) != len(b) {
			t.Fatal("different counts")
		}
		for i := range a {
			if a[i].rel != b[i].rel || !bytes.Equal(a[i].data, b[i].data) {
				t.Errorf("%s differs between renders", a[i].rel)
			}
		}
	})
}

// TestUnitsVerify runs systemd-analyze verify on the written set where the tool exists. Only a diagnostic
// that is not about a program that is not installed on this machine counts as failure.
func TestUnitsVerify(t *testing.T) {
	sa, err := execx.Lookup("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	r := newRig(t, true)
	if rc := r.run("units", "--backup-dir", "/bk"); rc != ExitOK {
		t.Fatalf("exit %d: %s", rc, r.errb)
	}
	dir := r.path("etc/systemd/system")
	for _, f := range []string{"localai-agent.service", "localai-llama.service", "localai-kiwix.service", "localai-whisper.service", "localai-sync.service", "localai-sync.timer", "localai-drill.service", "localai-drill.timer"} {
		res, _ := execx.Exec{}.Run(context.Background(), execx.Cmd{Path: sa, Args: []string{"verify", filepath.Join(dir, f)}, Timeout: time.Minute, MaxOut: 1 << 20})
		for _, line := range strings.Split(string(res.Stdout)+"\n"+string(res.Stderr), "\n") {
			l := strings.TrimSpace(line)
			if l == "" || strings.Contains(l, "Command ") || strings.Contains(l, "is not executable") ||
				strings.Contains(l, "No such file") || strings.Contains(l, "not found") || strings.Contains(l, "Failed to") ||
				strings.Contains(l, "ConditionPath") || strings.Contains(l, "failed to load") || strings.Contains(l, "Unit ") && strings.Contains(l, "not found") {
				continue
			}
			t.Errorf("systemd-analyze verify %s: %s", f, l)
		}
	}
}

func TestAPIKey(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}\n$`)
	t.Run("generates 64 hex, root-only, prints only the mask", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("api-key"); rc != ExitOK {
			t.Fatalf("exit %d: %s", rc, r.errb)
		}
		key := r.read(t, "etc/localai/api-key")
		if !hex64.MatchString(key) {
			t.Fatalf("key %q is not 64 hex", key)
		}
		info, _ := os.Stat(r.path("etc/localai/api-key"))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", info.Mode().Perm())
		}
		full := strings.TrimSpace(key)
		if strings.Contains(r.out.String(), full) || strings.Contains(r.errb.String(), full) {
			t.Error("the full key was printed")
		}
		if !strings.Contains(r.out.String(), full[:4]+"…"+full[60:]) || !strings.Contains(r.out.String(), "aictl key show") {
			t.Errorf("output %q", r.out)
		}
	})
	t.Run("keeps an existing key", func(t *testing.T) {
		r := newRig(t, true)
		existing := strings.Repeat("0f", 32) + "\n"
		r.write(t, "etc/localai/api-key", existing, 0o644)
		if rc := r.run("api-key"); rc != ExitOK {
			t.Fatalf("exit %d", rc)
		}
		if r.read(t, "etc/localai/api-key") != existing {
			t.Error("key changed")
		}
		info, _ := os.Stat(r.path("etc/localai/api-key"))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode not tightened: %v", info.Mode().Perm())
		}
	})
	t.Run("refuses to overwrite a malformed key", func(t *testing.T) {
		r := newRig(t, true)
		r.write(t, "etc/localai/api-key", "not a key\n", 0o600)
		if rc := r.run("api-key"); rc != ExitFailed {
			t.Fatalf("exit %d, want 1", rc)
		}
		if r.read(t, "etc/localai/api-key") != "not a key\n" {
			t.Error("malformed key overwritten")
		}
	})
	t.Run("reuses the legacy llama-qwen key", func(t *testing.T) {
		r := newRig(t, true)
		legacy := strings.Repeat("1e", 32)
		r.write(t, "etc/llama-qwen/api-key", legacy+"\n", 0o600)
		if rc := r.run("api-key"); rc != ExitOK {
			t.Fatalf("exit %d", rc)
		}
		if strings.TrimSpace(r.read(t, "etc/localai/api-key")) != legacy {
			t.Error("legacy key not reused")
		}
	})
	t.Run("dry run writes nothing", func(t *testing.T) {
		r := newRig(t, true)
		if rc := r.run("api-key", "--dry-run"); rc != ExitOK {
			t.Fatalf("exit %d", rc)
		}
		if _, err := os.Stat(r.path("etc/localai/api-key")); err == nil {
			t.Error("dry run wrote the key")
		}
	})
	t.Run("no entropy fails without writing", func(t *testing.T) {
		r := newRig(t, true)
		r.e.Random = bytes.NewReader([]byte{1, 2})
		if rc := r.run("api-key"); rc != ExitFailed {
			t.Fatalf("exit %d, want 1", rc)
		}
		if _, err := os.Stat(r.path("etc/localai/api-key")); err == nil {
			t.Error("partial key written")
		}
	})
}

func TestMaskKey(t *testing.T) {
	k := strings.Repeat("ab12", 8) + strings.Repeat("cd34", 8)
	if got := MaskKey(k); got != "ab12…cd34" {
		t.Errorf("MaskKey = %q", got)
	}
	if got := MaskKey("short"); got != "…" {
		t.Errorf("short key mask %q", got)
	}
}
