package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Tests of `aictl backup-config --restore latest` and of `--restore` with no file (issue #433,
// aictl.md section 4.25): the backups are found in the kit config folders of this computer and of the
// mounted AI-ARK drives.

// arkBackups makes a real backup of this node and copies it under the given names into
// <root>/kit/config/<node>/ (node is read from the name), so the restore has something to check.
func (f *localFixture) arkBackups(root string, names ...string) map[string]string {
	t := f.t
	t.Helper()
	src := f.makeBackup()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, n := range names {
		node := strings.TrimSuffix(strings.TrimPrefix(n, "localai-config-"), n[strings.LastIndex(n, "-"):])
		p := filepath.Join(root, "kit", "config", node, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths[n] = p
	}
	return paths
}

// drift changes node.yaml so a restore has something to write.
func (f *localFixture) drift() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "node.yaml"), []byte("schema_version: 1\nnode:\n  id: ai-n3\nprofile: drifted\n"), 0o640); err != nil {
		f.t.Fatal(err)
	}
}

const (
	bOld    = "localai-config-ai-n3-20261001T000000Z.tar.gz"
	bNew    = "localai-config-ai-n3-20261102T150405Z.tar.gz"
	bOther  = "localai-config-ai-n9-20261201T000000Z.tar.gz"
	bOther2 = "localai-config-ai-n5-20261103T000000Z.tar.gz"
)

func TestRestoreLatestTakesTheNewestBackupOfThisNode(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	drive := filepath.Join(f.addDrive("b", false), "localai")
	onDrive := f.arkBackups(drive, bNew, bOther)
	f.arkBackups(f.env.Paths.InternalRoot, bOld)
	f.drift()
	f.expectRestart(0)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", "latest", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	out := unwrap(r.out)
	if !strings.Contains(out, "i Using the newest backup of ai-n3, made 2026-11-02 15:04 UTC: "+onDrive[bNew]) ||
		!strings.Contains(out, "Restored 1 files from "+onDrive[bNew]) {
		t.Errorf("not the newest backup of this node (another node's newer one must not be taken):\n%s", out)
	}
}

func TestRestoreLatestWithNoBackupIsKIT050(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", "latest", "--yes")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "["+doctor.KIT050+"]") ||
		!strings.Contains(unwrap(r.errOut), "No backup of node ai-n3 was found in localai/kit/config/ai-n3/") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	// Other nodes' backups are named, never taken.
	drive := filepath.Join(f.addDrive("b", false), "localai")
	f.arkBackups(drive, bOther, bOther2)
	r = runCLI(t, DefaultRegistry, localOpts(nil), "--lang", "es", "backup-config", "--restore", "latest", "--yes")
	if r.code != ExitNotFound || !strings.Contains(unwrap(r.errOut), "Hay copias de ai-n9, ai-n5: ejecute `sudo aictl backup-config --restore` para elegir una.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRestoreWithNoFileListsTheBackupsAtATerminal(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	drive := filepath.Join(f.addDrive("b", false), "localai")
	paths := f.arkBackups(drive, bNew, bOther)
	for k, v := range f.arkBackups(f.env.Paths.InternalRoot, bOld) {
		paths[k] = v
	}
	f.drift()
	f.expectRestart(0)
	// a wrong answer is asked again; 2 is the older backup of this node; then the confirmation
	term := fake.Typed(true, "9", "2", "y")
	r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--restore")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	errOut := r.errOut
	mount := filepath.Dir(drive)
	for _, want := range []string{
		"Backups found (newest first):",
		"   1) ai-n3  2026-11-02 15:04 UTC  " + mount + "\n",
		"   2) ai-n3  2026-10-01 00:00 UTC  this computer\n",
		"   3) ai-n9  2026-12-01 00:00 UTC  " + mount + "  (another node)\n",
		"Which one? Type its number (1 to 3, Enter is 1): ",
		"Type a number from 1 to 3.",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(unwrap(r.out), "Restored 1 files from "+paths[bOld]) {
		t.Errorf("the chosen backup was not restored:\n%s", r.out)
	}
}

func TestRestoreWithNoFileEnterIsTheNewestAndEOFChoosesNothing(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	paths := f.arkBackups(f.env.Paths.InternalRoot, bOld, bNew)
	f.drift()
	f.expectRestart(0)
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "", "y")), "backup-config", "--restore")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Restored 1 files from "+paths[bNew]) {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	f.drift()
	r = runCLI(t, DefaultRegistry, localOpts(fake.Typed(true)), "backup-config", "--restore")
	if r.code != 0 || !strings.Contains(r.out, "Cancelled. Nothing was changed.") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
}

func TestRestoreWithNoFileNeedsATerminal(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	f.arkBackups(f.env.Paths.InternalRoot, bNew)
	for _, args := range [][]string{{"backup-config", "--restore", "--yes"}, {"--json", "backup-config", "--restore"}} {
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), args...)
		if r.code != ExitUsage || !strings.Contains(r.out+r.errOut, "--restore latest") {
			t.Errorf("%v: exit %d\n%s%s", args, r.code, r.out, r.errOut)
		}
	}
	// With nothing found the list is not shown either.
	g := newLocalFixture(t)
	g.populateNode()
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true)), "backup-config", "--restore")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "["+doctor.KIT050+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRestoreFileFormsStillWork(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	f.drift()
	f.expectRestart(0)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore="+backup, "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Restored 1 files from "+backup) {
		t.Fatalf("--restore=<file>: exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", backup, "extra"); r.code != ExitUsage {
		t.Errorf("two arguments: exit %d", r.code)
	}
}
