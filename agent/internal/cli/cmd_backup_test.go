package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

var testSecretFiles = []string{"api-key", "agent-token", "cloud-credential", "admin-pin", "hf-token"}

// populateNode fills the node like a working one: config, five secrets, catalog, state, unit.
func (f *localFixture) populateNode() {
	t := f.t
	t.Helper()
	etc := f.env.Paths.EtcDir
	put := func(p, content string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(etc, "node.yaml"), "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n", 0o640)
	put(filepath.Join(etc, "models.ini"), "[*]\n", 0o640)
	for _, n := range testSecretFiles {
		put(filepath.Join(etc, n), "secret-of-"+n+"\n", 0o600)
	}
	writeLocalCatalog(t, filepath.Join(etc, "catalog"), "2026.10.01-1")
	put(filepath.Join(f.env.Paths.StateDir, "state.json"), `{"mode":"offline"}`, 0o640)
	put(filepath.Join(f.env.Paths.SystemdDir, "localai-llama.service"), "[Service]\n", 0o644)
}

// addDrive mounts a fake AI-ARK drive and returns its mount point.
func (f *localFixture) addDrive(name string, readOnly bool) string {
	t := f.t
	mount := filepath.Join(filepath.Dir(f.env.Paths.Mountinfo), "mnt", name)
	if err := os.MkdirAll(filepath.Join(mount, "localai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "localai", "ROOT.json"), []byte(`{"kind":"ark_drive"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := "rw"
	if readOnly {
		opts = "ro"
	}
	line := fmt.Sprintf("40 25 8:1 / %s %s - ext4 /dev/sd%s1 rw\n", mount, opts, name)
	fh, err := os.OpenFile(f.env.Paths.Mountinfo, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(line); err != nil {
		t.Fatal(err)
	}
	return mount
}

func archiveNames(t *testing.T, path string) map[string]string {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func TestBackupConfigDefaultsToTheCurrentDirectory(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	dir := t.TempDir()
	t.Chdir(dir)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	m := regexp.MustCompile(`^✓ Saved \./(localai-config-ai-n3-20261102T150405Z\.tar\.gz) \((\d+) files, includes secrets — keep it private\)\.\n$`).FindStringSubmatch(unwrap(r.out))
	if m == nil {
		t.Fatalf("stdout %q", r.out)
	}
	if fileMode(t, filepath.Join(dir, m[1])) != 0o600 {
		t.Error("backup mode is not 0600")
	}
	names := archiveNames(t, filepath.Join(dir, m[1]))
	if _, ok := names["MANIFEST.json"]; !ok || names["etc/localai/admin-pin"] == "" {
		t.Errorf("archive: %v", len(names))
	}
	if strings.Contains(r.out+r.errOut, "secret-of-") {
		t.Error("a secret was printed")
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "config.backup" || lines[0]["result"] != "ok" || lines[0]["actor"].(map[string]any)["kind"] != "cli-root" {
		t.Errorf("audit %v", lines)
	}
	if strings.Contains(readFile(t, filepath.Join(f.env.Paths.StateDir, "audit.jsonl")), "secret-of-") {
		t.Error("a secret is in the audit log")
	}
}

func TestBackupConfigNoSecretsAndOut(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--no-secrets", "--out", out)
	if r.code != 0 || !regexp.MustCompile(`^✓ Saved \S+x\.tar\.gz \(12 files, no secrets\)\.\n$`).MatchString(unwrap(r.out)) {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	for name, content := range archiveNames(t, out) {
		for _, s := range testSecretFiles {
			if name == "etc/localai/"+s {
				t.Errorf("%s is in a --no-secrets backup", s)
			}
		}
		if strings.Contains(content, "secret-of-") {
			t.Errorf("%s holds a secret", name)
		}
	}
	// the same with --json
	r = runCLI(t, DefaultRegistry, localOpts(nil), "--json", "backup-config", "--no-secrets", "--out", filepath.Join(t.TempDir(), "y.tar.gz"))
	var doc struct {
		Path            string `json:"path"`
		NodeID          string `json:"node_id"`
		Files           int    `json:"files"`
		SecretsIncluded bool   `json:"secrets_included"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || doc.NodeID != "ai-n3" || doc.Files != 12 || doc.SecretsIncluded || r.errOut != "" {
		t.Errorf("json %q (%v) err %q", r.out, err, r.errOut)
	}
}

func TestBackupConfigNeedsRootBeforeTouchingFiles(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	dir := t.TempDir()
	t.Chdir(dir)
	o := localOpts(nil)
	o.euid = 1000
	for _, args := range [][]string{{"backup-config"}, {"backup-config", "--restore", "x.tar.gz", "--yes"}} {
		r := runCLI(t, DefaultRegistry, o, args...)
		if r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.errOut)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files created by a non-root run: %v", entries)
	}
	if len(f.auditLines()) != 0 {
		t.Error("audit line from a non-root run")
	}
}

func TestBackupConfigToArkAsksBeforeWritingSecrets(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	mount := f.addDrive("a", false)
	dir := filepath.Join(mount, "localai", "kit", "config", "ai-n3")

	// declined: nothing is written
	term := fake.Typed(true, "n")
	r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--to-ark")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Cancelled. Nothing was written.") || !strings.Contains(unwrap(r.errOut), "is not encrypted") {
		t.Fatalf("declined: exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the drive was written although the question was declined")
	}

	// accepted
	r = runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "backup-config", "--to-ark")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "✓ Saved "+dir+"/localai-config-ai-n3-20261102T150405Z.tar.gz") || !strings.Contains(r.out, "includes secrets") {
		t.Fatalf("accepted: exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if fileMode(t, dir) != 0o700 {
		t.Errorf("node folder mode %o", fileMode(t, dir))
	}
}

func TestBackupConfigToArkNoSecretsSkipsTheQuestion(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	f.addDrive("a", false)
	term := fake.Typed(false)
	r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--to-ark", "--no-secrets")
	if r.code != 0 || term.Reads() != 0 || strings.Contains(r.errOut, "not encrypted") || !strings.Contains(unwrap(r.out), "no secrets") {
		t.Fatalf("exit %d reads %d\n%s%s", r.code, term.Reads(), r.out, r.errOut)
	}
}

func TestBackupConfigToArkRefusals(t *testing.T) {
	t.Run("no drive is exit 5", func(t *testing.T) {
		f := newLocalFixture(t)
		f.populateNode()
		r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--to-ark")
		if r.code != ExitNotFound || !strings.Contains(r.errOut, "LAI-ARK-001") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("secrets and no terminal need --yes", func(t *testing.T) {
		f := newLocalFixture(t)
		f.populateNode()
		mount := f.addDrive("a", false)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "backup-config", "--to-ark")
		if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-007") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		if _, err := os.Stat(filepath.Join(mount, "localai", "kit")); err == nil {
			t.Error("the drive was written")
		}
		r = runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "backup-config", "--to-ark", "--yes")
		if r.code != 0 {
			t.Fatalf("--yes: exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("a read-only drive", func(t *testing.T) {
		f := newLocalFixture(t)
		f.populateNode()
		mount := f.addDrive("a", true)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "backup-config", "--to-ark")
		if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "mounted read-only") || !strings.Contains(r.errOut, "LAI-CLI-012") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		if _, err := os.Stat(filepath.Join(mount, "localai", "kit")); err == nil {
			t.Error("a read-only drive was written")
		}
	})
	t.Run("to-ark with out", func(t *testing.T) {
		newLocalFixture(t)
		r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--to-ark", "--out", "x")
		if r.code != ExitUsage {
			t.Fatalf("exit %d", r.code)
		}
	})
	t.Run("an existing out file is never overwritten", func(t *testing.T) {
		f := newLocalFixture(t)
		f.populateNode()
		out := filepath.Join(t.TempDir(), "mine.tar.gz")
		if err := os.WriteFile(out, []byte("precious"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--out", out)
		if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "it already exists") || readFile(t, out) != "precious" {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
}

// ---- --restore ----

func (f *localFixture) expectRestart(restartExit int) {
	f.run.Expect(execfake.Key("/usr/bin/systemctl", "daemon-reload"), execx.Result{}, nil)
	f.run.Expect(execfake.Key("/usr/bin/systemctl", "restart", "localai-agent.service"), execx.Result{ExitCode: restartExit}, nil)
}

func (f *localFixture) makeBackup() string {
	f.t.Helper()
	out := filepath.Join(f.t.TempDir(), "b.tar.gz")
	r := runCLI(f.t, DefaultRegistry, localOpts(nil), "backup-config", "--out", out)
	if r.code != 0 {
		f.t.Fatalf("backup: exit %d\n%s", r.code, r.errOut)
	}
	return out
}

func TestRestoreConfigRoundTrip(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	etc := f.env.Paths.EtcDir
	if err := os.WriteFile(filepath.Join(etc, "node.yaml"), []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "admin-pin"), []byte("other-hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.expectRestart(0)

	term := fake.Typed(true, "y")
	r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--restore", backup)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	for _, want := range []string{"Backup of node ai-n3", "/etc/localai/node.yaml", "/etc/localai/admin-pin", "(secret)", "[y/N]"} {
		if !strings.Contains(strings.ReplaceAll(r.out+r.errOut, "\n  ", " "), strings.TrimPrefix(want, "/etc/localai/")) {
			t.Errorf("missing %q in output:\n%s%s", want, r.out, r.errOut)
		}
	}
	if !strings.Contains(unwrap(r.out), "✓ Restored 2 files from "+backup+".") || !strings.Contains(unwrap(r.out), "The agent was restarted.") {
		t.Errorf("success line:\n%s", r.out)
	}
	if readFile(t, filepath.Join(etc, "node.yaml")) != "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n" || fileMode(t, filepath.Join(etc, "node.yaml")) != 0o640 {
		t.Error("node.yaml not restored with its mode")
	}
	if readFile(t, filepath.Join(etc, "admin-pin")) != "secret-of-admin-pin\n" || fileMode(t, filepath.Join(etc, "admin-pin")) != 0o600 {
		t.Error("admin-pin not restored with 0600")
	}
	if readFile(t, filepath.Join(etc, "node.yaml.pre-restore")) != "drifted\n" || readFile(t, filepath.Join(etc, "admin-pin.pre-restore")) != "other-hash\n" {
		t.Error("the originals were not kept as .pre-restore")
	}
	calls := f.run.Calls()
	if len(calls) != 2 || calls[0].Args[0] != "daemon-reload" || strings.Join(calls[1].Args, " ") != "restart localai-agent.service" {
		t.Errorf("systemctl: %+v", calls)
	}
	var restores int
	for _, l := range f.auditLines() {
		if l["action"] == "config.restore" && l["result"] == "ok" && l["actor"].(map[string]any)["kind"] == "cli-root" {
			restores++
		}
	}
	if restores != 1 {
		t.Errorf("audit %v", f.auditLines())
	}
}

func TestRestoreConfigAgainSaysNothingToRestore(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	term := fake.Typed(true)
	r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--restore", backup)
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Nothing to restore") || term.Reads() != 0 || len(f.run.Calls()) != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
}

func TestRestoreConfigDeclinedChangesNothing(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	etc := f.env.Paths.EtcDir
	if err := os.WriteFile(filepath.Join(etc, "node.yaml"), []byte("drifted\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, term := range []Terminal{fake.Typed(true, ""), fake.Typed(true, "no"), fake.Typed(true)} {
		r := runCLI(t, DefaultRegistry, localOpts(term), "backup-config", "--restore", backup)
		if r.code != 0 || !strings.Contains(unwrap(r.out), "Nothing was changed") {
			t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
		}
	}
	if readFile(t, filepath.Join(etc, "node.yaml")) != "drifted\n" || len(f.run.Calls()) != 0 {
		t.Error("a declined restore changed the node")
	}
	if _, err := os.Stat(filepath.Join(etc, "node.yaml.pre-restore")); err == nil {
		t.Error("a pre-restore copy was made")
	}
	// no terminal and no --yes is exit 2, still nothing changed
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "backup-config", "--restore", backup)
	if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-007") {
		t.Errorf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRestoreConfigYesAfterTheCommandAndJSON(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "models.ini"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	f.expectRestart(0)
	r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(false)), "--json", "backup-config", "--restore", backup, "--yes")
	if r.code != 0 || r.out != `{"kept":1,"restarted":true,"restored":1}`+"\n" || r.errOut != "" {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

func TestRestoreConfigAgentRestartFailureIsExit1(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	backup := f.makeBackup()
	if err := os.WriteFile(filepath.Join(f.env.Paths.EtcDir, "models.ini"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	f.expectRestart(5)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", backup, "--yes")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "could not be restarted") || strings.Contains(r.out, "✓") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	if readFile(t, filepath.Join(f.env.Paths.EtcDir, "models.ini")) != "[*]\n" {
		t.Error("files not restored")
	}
}

func TestRestoreConfigRefusesBadFilesAndWarnsOnAnotherNode(t *testing.T) {
	f := newLocalFixture(t)
	f.populateNode()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.tar.gz")
	if err := os.WriteFile(bad, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ file, want string }{
		{bad, "This file cannot be restored"},
		{filepath.Join(dir, "missing.tar.gz"), "it does not exist"},
	} {
		r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", c.file, "--yes")
		if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), c.want) || !strings.Contains(r.errOut, "LAI-CLI-012") {
			t.Errorf("%s: exit %d\n%s", c.file, r.code, r.errOut)
		}
	}

	// a backup of another node onto this one: restored, but flagged first
	backup := f.makeBackup()
	if err := os.WriteFile(f.env.Paths.NodeYAML(), []byte("node:\n  id: ai-n9\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	f.expectRestart(0)
	r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--restore", backup, "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.errOut), "This backup is from node ai-n3, and this node is ai-n9.") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
}

func TestBackupConfigRestoreCannotBeCombined(t *testing.T) {
	newLocalFixture(t)
	for _, extra := range [][]string{{"--out", "x"}, {"--to-ark"}, {"--no-secrets"}} {
		args := append([]string{"backup-config", "--restore", "b.tar.gz"}, extra...)
		if r := runCLI(t, DefaultRegistry, localOpts(nil), args...); r.code != ExitUsage || !strings.Contains(r.errOut, "cannot be combined") {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.errOut)
		}
	}
	if r := runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "extra"); r.code != ExitUsage {
		t.Errorf("a stray argument: exit %d", r.code)
	}
}
