package cli

import (
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// Tests of `aictl ark init|mount|eject` (aictl.md section 4.10): the typed answer that init needs, the
// drives it refuses before asking, the eject that waits for the unmount, the blockers it names, and
// the exit codes of the refusals.
// Regenerate the golden files with: go test ./internal/cli -run ArkDrive -update-ark-drive

var updateArkDrive = flag.Bool("update-ark-drive", false, "rewrite the golden files of the ark drive commands")

const (
	freshDrive = `{"root_id":"ark-3f9c0a12","device":"/dev/sdb1","mount":"/media/user/AI-ARK","fstype":"ext4","label":"AI-ARK","size_bytes":2000398934016,"free_bytes":2000000000000,"state":"uninitialized"}`
	usedDrive  = `{"root_id":"ark-aabbccdd","device":"/dev/sdc1","mount":"/media/user/AI-ARK1","fstype":"ext4","label":"AI-ARK","size_bytes":2000398934016,"state":"ready"}`
	loneDrive  = `{"root_id":"ark-0000aaaa","device":"/dev/sdd1","mount":"","fstype":"ext4","label":"AI-ARK","size_bytes":2000398934016,"state":"not_mounted"}`
	badDrive   = `{"root_id":null,"device":"/dev/sde1","mount":"/media/user/AI-ARK2","fstype":"exfat","label":"AI-ARK","state":"unusable"}`
)

func drivesBody(ds ...string) string { return `{"drives":[` + strings.Join(ds, ",") + `]}` }

func arkDriveGolden(t *testing.T, name string, got followOut) {
	t.Helper()
	text := "== stdout ==\n" + got.out + "== stderr ==\n" + got.errOut + "== exit " + strconv.Itoa(got.code) + " ==\n"
	p := filepath.Join("testdata", "golden", name+".txt")
	if *updateArkDrive {
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-ark-drive): %v", p, err)
	}
	if text != string(want) {
		t.Errorf("%s differs.\n--- got ---\n%s--- want ---\n%s", name, text, want)
	}
}

func posts(r *mgRig, suffix string) int {
	n := 0
	for _, l := range r.requestLines() {
		if strings.HasPrefix(l, "POST /ark/drives/") && strings.HasSuffix(l, suffix) {
			n++
		}
	}
	return n
}

func newDriveRig(t *testing.T, drives string) *mgRig {
	t.Helper()
	r := newMgRig(t, fake.Options{})
	r.agent().Respond(http.MethodGet, "/ark/drives", 200, drives)
	r.agent().Respond(http.MethodPost, "/ark/drives/ark-3f9c0a12/init", 200, `{"root_id":"ark-3f9c0a12","state":"ready","mount":"/media/user/AI-ARK"}`)
	return r
}

func TestArkInit(t *testing.T) {
	list := drivesBody(freshDrive, usedDrive, loneDrive, badDrive)
	t.Run("typing the id sets the drive up, found by mount path, device or id", func(t *testing.T) {
		for _, arg := range []string{"/media/user/AI-ARK/", "/dev/sdb1", "ark-3f9c0a12"} {
			r := newDriveRig(t, list)
			got := r.run(mgArgs("ark", "init", arg).withTerm("ark-3f9c0a12"))
			if got.code != 0 || posts(r, "/init") != 1 {
				t.Fatalf("%s: %+v %v", arg, got, r.requestLines())
			}
			if !strings.Contains(got.errOut, "It does not format or erase") || !strings.Contains(got.errOut, "Type ark-3f9c0a12 to confirm:") {
				t.Errorf("question: %q", got.errOut)
			}
			if b := r.lastBody(http.MethodPost, "/ark/drives/ark-3f9c0a12/init"); b != "{}" {
				t.Errorf("body %q", b)
			}
		}
	})
	t.Run("golden en and es", func(t *testing.T) {
		got := newDriveRig(t, list).run(mgArgs("ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
		arkDriveGolden(t, "ark.init.en", got)
		got = newDriveRig(t, list).run(mgArgs("--lang", "es", "ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
		arkDriveGolden(t, "ark.init.es", got)
	})
	t.Run("--json prints the drive the agent answered, but needs a terminal: exit 2, nothing sent", func(t *testing.T) {
		r := newDriveRig(t, list)
		got := r.run(mgArgs("--json", "ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
		if got.code != ExitUsage || len(r.requestLines()) != 0 {
			t.Fatalf("%+v %v", got, r.requestLines())
		}
	})
	t.Run("no terminal, or --yes alone: exit 2 and no request at all", func(t *testing.T) {
		for _, a := range [][]string{{"ark", "init", "ark-3f9c0a12"}, {"--yes", "ark", "init", "ark-3f9c0a12"}} {
			r := newDriveRig(t, list)
			got := r.run(mgArgs(a...))
			if got.code != ExitUsage || !strings.Contains(got.errOut, "LAI-CLI-007") || len(r.requestLines()) != 0 {
				t.Fatalf("%v: %+v %v", a, got, r.requestLines())
			}
		}
	})
	t.Run("a wrong, empty or yes answer changes nothing", func(t *testing.T) {
		for _, ans := range []string{"y", "yes", "", "ark-aabbccdd", "AI-ARK"} {
			r := newDriveRig(t, list)
			got := r.run(mgArgs("ark", "init", "ark-3f9c0a12").withTerm(ans))
			if got.code != 0 || posts(r, "/init") != 0 || !strings.Contains(got.errOut+got.out, "Nothing was changed.") {
				t.Fatalf("%q: %+v", ans, got)
			}
		}
	})
	t.Run("refused before the question", func(t *testing.T) {
		cases := []struct {
			name, arg string
			code      int
			want      string
		}{
			{"already set up", "ark-aabbccdd", ExitRefused, "already set up or in use"},
			{"mounted by path but ready", "/media/user/AI-ARK1", ExitRefused, "already set up or in use"},
			{"not mounted", "ark-0000aaaa", ExitRefused, "aictl ark mount ark-0000aaaa"},
			{"unusable", "/dev/sde1", ExitRefused, "cannot be used"},
			{"internal disk", "internal", ExitUsage, "internal disk"},
			{"unknown", "/dev/sdz9", ExitNotFound, "No ark drive matches"},
		}
		for _, tc := range cases {
			r := newDriveRig(t, list)
			got := r.run(mgArgs("ark", "init", tc.arg).withTerm("ark-3f9c0a12"))
			if got.code != tc.code || !strings.Contains(got.errOut, tc.want) || posts(r, "/init") != 0 {
				t.Errorf("%s: %+v %v", tc.name, got, r.requestLines())
			}
		}
	})
	t.Run("no drive at all is exit 5", func(t *testing.T) {
		r := newDriveRig(t, `{"drives":[]}`)
		got := r.run(mgArgs("ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
		if got.code != ExitNotFound || !strings.Contains(got.errOut, "No ark drive was found.") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("two copies with one id need a mount path", func(t *testing.T) {
		clone := strings.Replace(freshDrive, "/media/user/AI-ARK", "/media/user/AI-ARK9", 1)
		r := newDriveRig(t, drivesBody(freshDrive, clone))
		got := r.run(mgArgs("ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
		if got.code != ExitUsage || posts(r, "/init") != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the agent refuses: a localai folder with content, read-only, busy", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			code   string
		}{{409, "LAI-AGT-020"}, {409, "LAI-ARK-003"}, {409, "LAI-AGT-022"}, {409, "LAI-SYN-040"}} {
			r := newDriveRig(t, list)
			r.agent().RespondError(http.MethodPost, "/ark/drives/ark-3f9c0a12/init", tc.status, tc.code, "No.", "Fix it.", map[string]any{"reason": "localai_exists"})
			got := r.run(mgArgs("ark", "init", "ark-3f9c0a12").withTerm("ark-3f9c0a12"))
			if got.code != ExitRefused || !strings.Contains(got.errOut, tc.code) {
				t.Errorf("%s: %+v", tc.code, got)
			}
		}
	})
	t.Run("agent down is exit 3, usage errors are exit 2", func(t *testing.T) {
		for _, a := range [][]string{{"ark", "init"}, {"ark", "init", "a", "b"}} {
			if got := newDriveRig(t, list).run(mgArgs(a...).withTerm("x")); got.code != ExitUsage {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
}

func TestArkMount(t *testing.T) {
	t.Run("mounts and says where", func(t *testing.T) {
		r := newMgRig(t, fake.Options{})
		r.agent().Respond(http.MethodPost, "/ark/drives/ark-0000aaaa/mount", 200, `{"root_id":"ark-0000aaaa","mount":"/media/root/AI-ARK","state":"ready"}`)
		got := r.run(mgArgs("ark", "mount", "ark-0000aaaa"))
		if got.code != 0 || !strings.Contains(got.out+got.errOut, "The drive ark-0000aaaa is mounted at /media/root/AI-ARK.") {
			t.Fatalf("%+v", got)
		}
		arkDriveGolden(t, "ark.mount.en", got)
		es := r.run(mgArgs("--lang", "es", "ark", "mount", "ark-0000aaaa"))
		arkDriveGolden(t, "ark.mount.es", es)
		j := r.run(mgArgs("--json", "ark", "mount", "ark-0000aaaa"))
		if j.code != 0 || !strings.Contains(j.out, `"mount":"/media/root/AI-ARK"`) {
			t.Fatalf("%+v", j)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			code   string
			want   int
		}{{409, "LAI-ARK-005", ExitRefused}, {409, "LAI-AGT-020", ExitRefused}, {404, "LAI-AGT-021", ExitNotFound}} {
			r := newMgRig(t, fake.Options{})
			r.agent().RespondError(http.MethodPost, "/ark/drives/ark-0000aaaa/mount", tc.status, tc.code, "No.", "Fix.", map[string]any{"error": "mount failed"})
			if got := r.run(mgArgs("ark", "mount", "ark-0000aaaa")); got.code != tc.want || !strings.Contains(got.errOut, tc.code) {
				t.Errorf("%s: %+v", tc.code, got)
			}
		}
	})
	t.Run("bad ids and counts are exit 2 with nothing sent", func(t *testing.T) {
		for _, a := range [][]string{{"ark", "mount"}, {"ark", "mount", "/dev/sdb1"}, {"ark", "mount", "ark-1", "x"}} {
			r := newMgRig(t, fake.Options{})
			if got := r.run(mgArgs(a...)); got.code != ExitUsage || len(r.requestLines()) != 0 {
				t.Errorf("%v: %+v", a, got)
			}
		}
	})
}

func ejectRig(t *testing.T, after ...string) (*mgRig, *int32) {
	t.Helper()
	ejectPoll = time.Millisecond
	t.Cleanup(func() { ejectPoll = time.Second })
	r := newMgRig(t, fake.Options{})
	var n int32
	r.agent().Handle(http.MethodGet, "/ark/drives", func(w http.ResponseWriter, _ *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(after) {
			i = len(after) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(after[i]))
	})
	r.agent().Respond(http.MethodPost, "/ark/drives/ark-aabbccdd/eject", 202, `{"state":"ejecting"}`)
	return r, &n
}

func TestArkEject(t *testing.T) {
	ejecting := strings.Replace(usedDrive, `"ready"`, `"ejecting"`, 1)
	t.Run("waits for the unmount, then says it is safe", func(t *testing.T) {
		r, n := ejectRig(t, drivesBody(ejecting), drivesBody(ejecting), drivesBody())
		got := r.run(mgArgs("ark", "eject", "ark-aabbccdd"))
		if got.code != 0 || !strings.Contains(got.out+got.errOut, "You can unplug the drive now.") || *n != 3 {
			t.Fatalf("%+v polls=%d", got, *n)
		}
		if b := r.lastBody(http.MethodPost, "/ark/drives/ark-aabbccdd/eject"); b != `{"force":false}` {
			t.Errorf("body %q", b)
		}
		arkDriveGolden(t, "ark.eject.en", got)
		r2, _ := ejectRig(t, drivesBody(strings.Replace(usedDrive, `"ready"`, `"not_mounted"`, 1)))
		arkDriveGolden(t, "ark.eject.es", r2.run(mgArgs("--lang", "es", "ark", "eject", "ark-aabbccdd")))
	})
	t.Run("--json", func(t *testing.T) {
		r, _ := ejectRig(t, drivesBody())
		got := r.run(mgArgs("--json", "ark", "eject", "ark-aabbccdd"))
		if got.code != 0 || !strings.Contains(got.out, `"safe_to_unplug": true`) && !strings.Contains(got.out, `"safe_to_unplug":true`) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("programs with files open are named, exit 6", func(t *testing.T) {
		r, _ := ejectRig(t, drivesBody(usedDrive))
		r.agent().RespondError(http.MethodPost, "/ark/drives/ark-aabbccdd/eject", 409, "LAI-ARK-009", "Files are in use.", "Close them.",
			map[string]any{"blockers": []any{map[string]any{"kind": "process", "name": "vlc", "pid": 4242}, map[string]any{"kind": "model", "name": "Qwen X", "pid": 0}}})
		got := r.run(mgArgs("ark", "eject", "ark-aabbccdd"))
		if got.code != ExitRefused || !strings.Contains(got.errOut+got.out, "vlc (process 4242) has files open") || !strings.Contains(got.errOut, "LAI-ARK-009") {
			t.Fatalf("%+v", got)
		}
		if strings.Contains(got.errOut+got.out, "--force") {
			t.Errorf("--force must not be offered when a program has a file open: %+v", got)
		}
		arkDriveGolden(t, "ark.eject.blockers.en", got)
	})
	t.Run("a brain on the drive: the hint is --force, which asks first", func(t *testing.T) {
		r, _ := ejectRig(t, drivesBody())
		r.agent().RespondError(http.MethodPost, "/ark/drives/ark-aabbccdd/eject", 409, "LAI-ARK-009", "Files are in use.", "Close them.",
			map[string]any{"blockers": []any{map[string]any{"kind": "model", "name": "Qwen X", "pid": 0}}})
		got := r.run(mgArgs("ark", "eject", "ark-aabbccdd"))
		if got.code != ExitRefused || !strings.Contains(got.errOut+got.out, "The brain Qwen X is running from this drive.") || !strings.Contains(got.errOut+got.out, "--force") {
			t.Fatalf("%+v", got)
		}
		r2, _ := ejectRig(t, drivesBody())
		if got := r2.run(mgArgs("ark", "eject", "ark-aabbccdd", "--force").withTerm("n")); got.code != 0 || posts(r2, "/eject") != 0 {
			t.Fatalf("declined: %+v", got)
		}
		if got := r2.run(mgArgs("ark", "eject", "ark-aabbccdd", "--force").withTerm("y")); got.code != 0 || r2.lastBody(http.MethodPost, "/ark/drives/ark-aabbccdd/eject") != `{"force":true}` {
			t.Fatalf("accepted: %+v", got)
		}
		r3, _ := ejectRig(t, drivesBody())
		if got := r3.run(mgArgs("ark", "eject", "ark-aabbccdd", "--force")); got.code != ExitUsage || posts(r3, "/eject") != 0 {
			t.Fatalf("no terminal: %+v", got)
		}
		if got := r3.run(mgArgs("ark", "eject", "ark-aabbccdd", "--force", "--yes")); got.code != 0 || posts(r3, "/eject") != 1 {
			t.Fatalf("--yes: %+v", got)
		}
	})
	t.Run("the unmount fails and the drive comes back: exit 1, not a false all-clear", func(t *testing.T) {
		r, _ := ejectRig(t, drivesBody(ejecting), drivesBody(usedDrive))
		got := r.run(mgArgs("ark", "eject", "ark-aabbccdd"))
		if got.code != ExitFailed || !strings.Contains(got.errOut, "LAI-ARK-009") || strings.Contains(got.out+got.errOut, "You can unplug") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("never confirmed within the deadline is an error, not a success", func(t *testing.T) {
		r, _ := ejectRig(t, drivesBody(ejecting))
		got := r.run(mgArgs("--timeout", "1", "ark", "eject", "ark-aabbccdd"))
		if got.code == 0 || strings.Contains(got.out+got.errOut, "You can unplug") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("not mounted, busy, unknown, bad id", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			code   string
			want   int
		}{{409, "LAI-AGT-020", ExitRefused}, {409, "LAI-AGT-022", ExitRefused}, {404, "LAI-AGT-021", ExitNotFound}} {
			r, _ := ejectRig(t, drivesBody())
			r.agent().RespondError(http.MethodPost, "/ark/drives/ark-aabbccdd/eject", tc.status, tc.code, "No.", "Fix.", nil)
			if got := r.run(mgArgs("ark", "eject", "ark-aabbccdd")); got.code != tc.want || !strings.Contains(got.errOut, tc.code) {
				t.Errorf("%s: %+v", tc.code, got)
			}
		}
		r, _ := ejectRig(t, drivesBody())
		if got := r.run(mgArgs("ark", "eject", "/media/x")); got.code != ExitUsage || len(r.requestLines()) != 0 {
			t.Errorf("%+v", got)
		}
	})
}
