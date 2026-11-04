package dirs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/setup/templates"
)

type node struct {
	dir      bool
	uid, gid int
	mode     os.FileMode
	data     []byte
}

// fakeSys is an in-memory machine that records the commands it was asked to run.
type fakeSys struct {
	users    map[string][2]int
	groups   map[string]int
	members  map[string]map[string]bool // user -> group set
	files    map[string]*node
	cmds     []string
	failOn   string // an operation name that fails: "mkdir", "addgroup", ...
	nextID   int
	writeErr error
}

func newFake() *fakeSys {
	f := &fakeSys{
		users:   map[string][2]int{"root": {0, 0}, "alice": {1000, 1000}},
		groups:  map[string]int{"root": 0, "video": 44, "render": 105, "alice": 1000},
		members: map[string]map[string]bool{},
		files:   map[string]*node{},
		nextID:  900,
	}
	for _, d := range []string{"/", "/opt", "/etc", "/var", "/var/lib", "/srv"} {
		f.files[d] = &node{dir: true, mode: 0o755}
	}
	return f
}

func (f *fakeSys) LookupUser(n string) (int, int, bool, error) {
	u, ok := f.users[n]
	return u[0], u[1], ok, nil
}
func (f *fakeSys) LookupGroup(n string) (int, bool, error) { g, ok := f.groups[n]; return g, ok, nil }
func (f *fakeSys) InGroup(u, g string) (bool, error)       { return f.members[u][g], nil }
func (f *fakeSys) AddGroup(_ context.Context, n string) error {
	f.cmds = append(f.cmds, "groupadd --system "+n)
	if f.failOn == "addgroup" {
		return errors.New("boom")
	}
	f.nextID++
	f.groups[n] = f.nextID
	return nil
}
func (f *fakeSys) AddUser(_ context.Context, n, home string) error {
	f.cmds = append(f.cmds, "useradd "+n+" "+home)
	f.nextID++
	f.users[n] = [2]int{f.nextID, f.nextID}
	f.groups[n] = f.nextID
	return nil
}
func (f *fakeSys) AddToGroups(_ context.Context, u string, gs []string) error {
	f.cmds = append(f.cmds, "usermod -aG "+strings.Join(gs, ",")+" "+u)
	if f.members[u] == nil {
		f.members[u] = map[string]bool{}
	}
	for _, g := range gs {
		f.members[u][g] = true
	}
	return nil
}
func (f *fakeSys) Stat(p string) (Info, error) {
	n, ok := f.files[p]
	if !ok {
		return Info{}, nil
	}
	return Info{Exists: true, IsDir: n.dir, UID: n.uid, GID: n.gid, Mode: n.mode}, nil
}
func (f *fakeSys) Mkdir(p string, m os.FileMode) error {
	if f.failOn == "mkdir" {
		return errors.New("no space left on device")
	}
	if _, ok := f.files[p]; ok {
		return os.ErrExist
	}
	f.files[p] = &node{dir: true, mode: m}
	return nil
}
func (f *fakeSys) Chown(p string, uid, gid int) error {
	f.files[p].uid, f.files[p].gid = uid, gid
	return nil
}
func (f *fakeSys) Chmod(p string, m os.FileMode) error { f.files[p].mode = m; return nil }
func (f *fakeSys) Names(uid, gid int) string {
	return fmt.Sprintf("uid%d:gid%d", uid, gid)
}
func (f *fakeSys) ReadFile(p string) ([]byte, error) { return f.files[p].data, nil }
func (f *fakeSys) WriteFile(p string, d []byte, m os.FileMode, uid, gid int) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.files[p] = &node{data: d, mode: m, uid: uid, gid: gid}
	return nil
}
func (f *fakeSys) Rename(a, b string) error { f.files[b] = f.files[a]; delete(f.files, a); return nil }

var fixedNow = func() time.Time { return time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC) }

func run(t *testing.T, f *fakeSys, o Options) *Report {
	t.Helper()
	o.Now = fixedNow
	rep, err := Run(context.Background(), f, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func TestFreshInstallCreatesEverythingExactly(t *testing.T) {
	f := newFake()
	rep := run(t, f, Options{DesktopUser: "alice", NodeID: "ai-n4", AgentVersion: "0.1.0"})
	wantCmds := []string{
		"groupadd --system localai-operators",
		"useradd localai /var/lib/localai",
		"usermod -aG video,render localai",
		"usermod -aG localai-operators alice",
	}
	if strings.Join(f.cmds, "|") != strings.Join(wantCmds, "|") {
		t.Fatalf("commands = %v, want %v", f.cmds, wantCmds)
	}
	lu := f.users["localai"][0]
	lg := f.users["localai"][1]
	for _, s := range Table() {
		n := f.files[s.Path]
		if n == nil || !n.dir {
			t.Fatalf("%s was not created", s.Path)
		}
		wantUID, wantGID := 0, 0
		if s.Owner == "localai" {
			wantUID = lu
		}
		if s.Group == "localai" {
			wantGID = lg
		}
		if n.uid != wantUID || n.gid != wantGID || n.mode != s.Mode {
			t.Errorf("%s = %d:%d %04o, want %d:%d %04o", s.Path, n.uid, n.gid, n.mode, wantUID, wantGID, s.Mode)
		}
	}
	for _, rel := range ark.LayoutDirs {
		if n := f.files[ContentRoot+"/"+rel]; n == nil || !n.dir {
			t.Errorf("layout dir %s missing", rel)
		}
	}
	if f.files[ContentRoot+"/.staging"].mode != 0o700 || f.files[ContentRoot+"/models"].mode != 0o755 {
		t.Error("layout modes are wrong")
	}
	rj, err := ark.ParseRootJSON(f.files[ContentRoot+"/ROOT.json"].data)
	if err != nil || rj.Kind != ark.KindInternal || rj.RootID != "internal" || rj.CreatedBy.NodeID != "ai-n4" {
		t.Fatalf("ROOT.json = %+v, %v", rj, err)
	}
	pol := f.files[PolkitDir+"/"+templates.PolkitRuleName]
	if pol == nil || string(pol.data) != string(templates.PolkitRule()) || pol.mode != 0o644 || pol.uid != 0 {
		t.Fatalf("polkit rule not written exactly: %+v", pol)
	}
	if !rep.LogoutNeeded || len(rep.Notes) == 0 || !strings.Contains(rep.Notes[len(rep.Notes)-1], "Log out") {
		t.Errorf("notes = %v, logout = %v", rep.Notes, rep.LogoutNeeded)
	}
}

// The table of the issue, spelled out once more so a typo in Table() is caught.
func TestTableMatchesOverview(t *testing.T) {
	want := map[string]string{
		"/opt/localai/bin": "root:root 0755", "/opt/localai/installers": "root:root 0755",
		"/opt/localai/llama.cpp/builds": "root:root 0755", "/opt/localai/llama.cpp/src": "root:root 0755",
		"/opt/localai/whisper.cpp/builds": "root:root 0755", "/opt/localai/piper": "root:root 0755",
		"/opt/localai/kiwix-tools": "root:root 0755", "/opt/localai/pmtiles": "root:root 0755",
		"/etc/localai": "root:localai 0750", "/etc/localai/catalog": "root:localai 0750",
		"/var/lib/localai":      "localai:localai 0750",
		"/var/lib/localai/sync": "localai:localai 0750", "/var/lib/localai/drill": "localai:localai 0750",
		"/var/lib/localai/cloud": "localai:localai 0750", "/var/lib/localai/kiwix": "localai:localai 0750",
		"/var/lib/localai/llama-cache": "localai:localai 0750",
		"/var/lib/localai/install":     "root:root 0700", "/var/lib/localai/build": "root:root 0700",
		"/var/lib/localai/update": "root:root 0700", "/var/lib/localai/migration": "root:root 0700",
		"/var/lib/localai/bench": "root:root 0755",
		"/srv/localai":           "localai:localai 0755",
	}
	got := map[string]string{}
	for _, s := range Table() {
		got[s.Path] = s.want()
	}
	var bad []string
	for p, w := range want {
		if got[p] != w {
			bad = append(bad, fmt.Sprintf("%s: got %q want %q", p, got[p], w))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatal(strings.Join(bad, "\n"))
	}
}

func TestSecondRunChangesNothing(t *testing.T) {
	f := newFake()
	run(t, f, Options{DesktopUser: "alice"})
	f.cmds = nil
	rep := run(t, f, Options{DesktopUser: "alice"})
	if rep.Pending() || len(f.cmds) != 0 {
		t.Fatalf("second run changed things: %+v, cmds %v", rep.Changes, f.cmds)
	}
	if rep.LogoutNeeded {
		t.Error("no logout needed when already a member")
	}
}

func TestWrongOwnerAndModeAreCorrectedWithOldValues(t *testing.T) {
	f := newFake()
	run(t, f, Options{DesktopUser: "alice"})
	f.files["/etc/localai"].mode = 0o755
	f.files["/etc/localai"].gid = 0
	f.files["/var/lib/localai/update"].mode = 0o755
	f.files[ContentRoot+"/.trash"].mode = 0o755
	f.files[ContentRoot+"/models"].mode = 0o700 // not private: left alone
	rep := run(t, f, Options{DesktopUser: "alice"})
	var lines []string
	for _, c := range rep.Changes {
		lines = append(lines, c.String())
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"fix owner of /etc/localai: uid0:gid0 -> root:localai",
		"fix mode of /etc/localai: 0755 -> 0750",
		"fix mode of /var/lib/localai/update: 0755 -> 0700",
		"fix mode of " + ContentRoot + "/.trash: 0755 -> 0700",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/models") {
		t.Errorf("a non-private layout dir must be left alone:\n%s", got)
	}
	if f.files["/etc/localai"].mode != 0o750 || f.files["/etc/localai"].gid != f.users["localai"][1] {
		t.Error("not corrected")
	}
}

func TestExistingUserAndGroupAreNotRecreated(t *testing.T) {
	f := newFake()
	f.users["localai"] = [2]int{990, 990}
	f.groups["localai"] = 990
	f.groups["localai-operators"] = 991
	f.members["localai"] = map[string]bool{"video": true, "render": true}
	f.members["alice"] = map[string]bool{"localai-operators": true}
	rep := run(t, f, Options{DesktopUser: "alice"})
	if len(f.cmds) != 0 {
		t.Fatalf("unexpected commands %v", f.cmds)
	}
	if rep.LogoutNeeded {
		t.Error("alice was already a member")
	}
}

func TestMissingRenderGroupIsSkippedWithANote(t *testing.T) {
	f := newFake()
	delete(f.groups, "render")
	rep := run(t, f, Options{DesktopUser: "alice"})
	if !contains(f.cmds, "usermod -aG video localai") {
		t.Fatalf("cmds = %v", f.cmds)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "Group render does not exist") {
		t.Errorf("notes = %v", rep.Notes)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestNoPolkitWritesNoRule(t *testing.T) {
	f := newFake()
	rep := run(t, f, Options{DesktopUser: "alice", NoPolkit: true})
	if _, ok := f.files[PolkitDir+"/"+templates.PolkitRuleName]; ok {
		t.Fatal("the rule was written with --no-polkit")
	}
	if _, ok := f.files["/etc/polkit-1"]; ok {
		t.Fatal("the polkit folder was created with --no-polkit")
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "--no-polkit") {
		t.Errorf("notes = %v", rep.Notes)
	}
}

func TestPolkitRuleIsReplacedWhenDifferent(t *testing.T) {
	f := newFake()
	run(t, f, Options{DesktopUser: "alice"})
	p := PolkitDir + "/" + templates.PolkitRuleName
	f.files[p].data = []byte("// edited")
	rep := run(t, f, Options{DesktopUser: "alice"})
	if !rep.Pending() || string(f.files[p].data) != string(templates.PolkitRule()) {
		t.Fatalf("not restored: %+v", rep.Changes)
	}
}

func TestDryRunChangesNothingAndReportsEverything(t *testing.T) {
	f := newFake()
	rep := run(t, f, Options{DesktopUser: "alice", DryRun: true})
	if len(f.cmds) != 0 {
		t.Fatalf("dry run ran %v", f.cmds)
	}
	if _, ok := f.files["/etc/localai"]; ok {
		t.Fatal("dry run created a folder")
	}
	if len(f.users) != 2 {
		t.Fatal("dry run created a user")
	}
	kinds := map[string]int{}
	for _, c := range rep.Changes {
		kinds[c.Kind]++
	}
	if kinds["group-add"] != 1 || kinds["user-add"] != 1 || kinds["mkdir"] < 30 || kinds["write"] != 2 {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestDryRunWithPlannedUserFlagsExistingDirsForChown(t *testing.T) {
	f := newFake()
	f.files["/var/lib/localai"] = &node{dir: true, mode: 0o700}
	rep := run(t, f, Options{DesktopUser: "alice", DryRun: true})
	found := false
	for _, c := range rep.Changes {
		if c.Kind == "chown" && c.Target == "/var/lib/localai" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a planned chown of /var/lib/localai, got %+v", rep.Changes[:5])
	}
}

func TestUnknownDesktopUserIsRefused(t *testing.T) {
	f := newFake()
	_, err := Run(context.Background(), f, Options{DesktopUser: "ghost", Now: fixedNow})
	if !errors.Is(err, ErrDesktopUser) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoDesktopUserOnlyNotes(t *testing.T) {
	f := newFake()
	rep := run(t, f, Options{})
	if f.members["alice"] != nil || rep.LogoutNeeded {
		t.Error("nobody should be added")
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "No desktop user") {
		t.Errorf("notes = %v", rep.Notes)
	}
}

func TestFileInTheWayIsAnErrorNotDeleted(t *testing.T) {
	f := newFake()
	f.files["/etc/localai"] = &node{dir: false, mode: 0o644, data: []byte("x")}
	_, err := Run(context.Background(), f, Options{DesktopUser: "alice", Now: fixedNow})
	if !errors.Is(err, ErrNotADirectory) {
		t.Fatalf("err = %v", err)
	}
	if string(f.files["/etc/localai"].data) != "x" {
		t.Fatal("the file was touched")
	}
}

func TestDiskFullStopsWithAReportOfWhatWasDone(t *testing.T) {
	f := newFake()
	f.failOn = "mkdir"
	rep, err := Run(context.Background(), f, Options{DesktopUser: "alice", Now: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("err = %v", err)
	}
	if len(rep.Changes) == 0 {
		t.Fatal("the report should list what was planned up to the failure")
	}
}

func TestDamagedRootJSONIsMovedAsideAndRewritten(t *testing.T) {
	f := newFake()
	run(t, f, Options{DesktopUser: "alice"})
	p := ContentRoot + "/ROOT.json"
	f.files[p].data = []byte("{not json")
	run(t, f, Options{DesktopUser: "alice"})
	if _, err := ark.ParseRootJSON(f.files[p].data); err != nil {
		t.Fatalf("not rewritten: %v", err)
	}
	if string(f.files[p+".damaged-20261102T180411Z"].data) != "{not json" {
		t.Fatal("the damaged file was not kept")
	}
}

func TestValidRootJSONIsKept(t *testing.T) {
	f := newFake()
	run(t, f, Options{DesktopUser: "alice", NodeID: "ai-n1"})
	before := string(f.files[ContentRoot+"/ROOT.json"].data)
	run(t, f, Options{DesktopUser: "alice", NodeID: "ai-n9"})
	if string(f.files[ContentRoot+"/ROOT.json"].data) != before {
		t.Fatal("ROOT.json was rewritten")
	}
}

func TestPolkitRuleIsTheSpecText(t *testing.T) {
	r := string(templates.PolkitRule())
	for _, must := range []string{
		`action.id == "org.freedesktop.systemd1.manage-units"`,
		`action.lookup("unit") == "localai-agent.service"`,
		`action.lookup("verb") == "start" || action.lookup("verb") == "restart"`,
		`subject.isInGroup("localai-operators")`,
		`return polkit.Result.YES;`,
	} {
		if !strings.Contains(r, must) {
			t.Errorf("rule lacks %q", must)
		}
	}
}
