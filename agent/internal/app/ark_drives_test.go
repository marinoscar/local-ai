package app

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// The AI-ARK drive detector (SYNC-02) wired into the daemon: a drive that is already plugged in when
// the agent starts is read, its root joins ArkRoots before drive.mounted is handled, and GET
// /ark/drives shows it. The mount table and blkid are fakes; the drive is a real temp directory.

const driveTestUUID = "3f9c0a12-7b4e-4c1d-9a55-0e1f2a3b4c5d"

type oneMountTable struct {
	mu      sync.Mutex
	entries []ark.MountEntry
}

func (m *oneMountTable) Read() ([]ark.MountEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ark.MountEntry(nil), m.entries...), nil
}

func (m *oneMountTable) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{})
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}

type tagsByDevice map[string]ark.BlockInfo

func (t tagsByDevice) Probe(_ context.Context, dev string) (ark.BlockInfo, error) {
	if b, ok := t[dev]; ok {
		return b, nil
	}
	return ark.BlockInfo{Device: dev}, nil
}

func (t tagsByDevice) FindByLabel(context.Context, string) ([]string, error) { return nil, nil }

// pluggedDrive makes an initialized drive in a temp directory and returns a hook for Env.ArkDrives that
// serves it as already mounted.
func pluggedDrive(t *testing.T) (mount string, hook func(*ark.Options)) {
	t.Helper()
	mount = filepath.Join(t.TempDir(), "AI-ARK")
	root, err := ark.NewDriveRoot(ark.DriveInfo{Mount: mount, FSUUID: driveTestUUID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ark.CreateLayout(ark.OSFS{}, root.Path, ark.LayoutOptions{}); err != nil {
		t.Fatal(err)
	}
	rj := ark.NewRootJSON(root, ark.CreatedBy{NodeID: "node-a", AgentVersion: "0.0.0"}, "AI-ARK", time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC))
	if err := ark.WriteRootJSON(filepath.Join(root.Path, ark.RootJSONName), rj); err != nil {
		t.Fatal(err)
	}
	line := "120 29 8:17 / " + mount + " rw,relatime shared:80 - ext4 /dev/sdb1 rw,errors=remount-ro\n"
	entries, err := ark.ParseMountInfo(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	return mount, func(o *ark.Options) {
		o.Mounts = &oneMountTable{entries: entries}
		o.Block = tagsByDevice{"/dev/sdb1": {Device: "/dev/sdb1", Label: ark.ArkLabel, UUID: driveTestUUID, Type: "ext4"}}
		o.Errors = nil
		o.Statfs = func(string) (int64, int64, error) { return 1000, 400, nil }
	}
}

func TestDriveDetectorIsWiredIntoTheDaemon(t *testing.T) {
	mount, hook := pluggedDrive(t)
	handled := make(chan string, 4)
	r := newRig(t, withEnv(func(e *Env) {
		e.ArkDrives = hook
		e.OnDriveEvent = func(typ, root string) { handled <- typ + " " + root }
	}))
	r.start()
	r.waitStarted()

	select {
	case got := <-handled:
		if got != "drive.mounted ark-3f9c0a12" {
			t.Fatalf("handled %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the drive that was plugged in at start was never announced\n%s", r.logText())
	}

	// The root is in ArkRoots: the engine, READY and the kit verifier see it.
	var found bool
	for _, root := range r.d.ark.roots.List() {
		if root.ID == "ark-3f9c0a12" {
			found = root.Path == filepath.Join(mount, "localai") && root.Kind == ark.KindArkDrive && root.State == ark.StateReady
		}
	}
	if !found {
		t.Errorf("roots = %+v", r.d.ark.roots.List())
	}

	got := r.do("GET", "/ark/drives", nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET /ark/drives = %d %s", got.status, got.body)
	}
	drives, _ := got.json()["drives"].([]any)
	if len(drives) != 1 {
		t.Fatalf("body = %s", got.body)
	}
	d := drives[0].(map[string]any)
	if d["root_id"] != "ark-3f9c0a12" || d["state"] != "ready" || d["mount"] != mount || d["device"] != "/dev/sdb1" || d["fstype"] != "ext4" ||
		d["size_bytes"] != float64(1000) || d["initialized"] != true || d["profile"] != "ark-drive-full" {
		t.Errorf("drive = %v", d)
	}
	if rd, ok := d["ready"].(map[string]any); ok && rd["overall"] == "" {
		t.Errorf("ready = %v", rd)
	}
	// A route that needs the operator level refuses a request with no credentials.
	if un := r.doAuth("GET", "/ark/drives", nil, false); un.status != http.StatusUnauthorized {
		t.Errorf("GET /ark/drives with no credentials = %d", un.status)
	}
}

// An unprivileged daemon with no fakes (every other test of this package) must not read the real mount
// table of the machine: the detector is built but not started, and the route answers an empty list.
func TestDriveDetectorStaysOffWithoutFakesInATestDaemon(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	if r.d.ark.watchDrives {
		t.Error("the detector watches the real mount table in an unprivileged daemon")
	}
	got := r.do("GET", "/ark/drives", nil)
	if got.status != http.StatusOK || strings.TrimSpace(string(got.body)) != `{"drives":[]}` {
		t.Errorf("GET /ark/drives = %d %s", got.status, got.body)
	}
}
