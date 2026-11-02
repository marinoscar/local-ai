package local

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

var secretNames = []string{"api-key", "agent-token", "cloud-credential", "admin-pin", "hf-token"}

// populate fills the node's files: a config, the five secrets, the catalog, the state, the unit.
func populate(t *testing.T, e Env) {
	t.Helper()
	etc := e.Paths.EtcDir
	write(t, filepath.Join(etc, "node.yaml"), "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n", 0o640)
	write(t, filepath.Join(etc, "models.ini"), "[*]\nmodels-max = 1\n", 0o640)
	for _, n := range secretNames {
		write(t, filepath.Join(etc, n), "secret-of-"+n+"\n", 0o600)
	}
	writeCatalog(t, e.Paths.CatalogDir(), verOld)
	write(t, filepath.Join(e.Paths.StateDir, "state.json"), `{"mode":"offline"}`, 0o640)
	write(t, filepath.Join(e.Paths.SystemdDir, "localai-llama.service"), "[Service]\nExecStart=/bin/true\n", 0o644)
	write(t, filepath.Join(e.Paths.SystemdDir, "localai-llama.service.d", "50-env.conf"), "[Service]\nEnvironment=X=1\n", 0o644)
}

// readBackup returns the manifest and the contents of a backup file.
func readBackup(t *testing.T, path string) (Manifest, map[string]string, []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	var order []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
		order = append(order, h.Name)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(files[manifestName]), &m); err != nil {
		t.Fatal(err)
	}
	return m, files, order
}

func TestBackupContentsManifestAndMode(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	out := t.TempDir()
	res, err := Backup(e, BackupOptions{Out: out})
	if err != nil {
		t.Fatal(err)
	}
	wantName := "localai-config-ai-n3-20261102T150405Z.tar.gz"
	if res.Path != filepath.Join(out, wantName) || res.NodeID != "ai-n3" {
		t.Fatalf("path %q node %q", res.Path, res.NodeID)
	}
	if m := modeOf(t, res.Path); m != 0o600 {
		t.Errorf("mode %o, want 0600", m)
	}
	if uid, _ := ownerOf(mustStat(t, res.Path)); uid != e.RootUID {
		t.Errorf("owner %d", uid)
	}
	m, files, order := readBackup(t, res.Path)
	if order[0] != manifestName {
		t.Errorf("the manifest must come first, got %v", order[:2])
	}
	if !m.SecretsIncluded || !res.SecretsIncluded || m.NodeID != "ai-n3" || m.AgentVersion != "0.0.0-test" || m.CreatedAt != "2026-11-02T15:04:05Z" {
		t.Errorf("manifest header %+v (result secrets=%v)", m, res.SecretsIncluded)
	}
	if len(m.Files) != res.Files || len(files) != res.Files+1 {
		t.Fatalf("manifest %d, result %d, archive %d", len(m.Files), res.Files, len(files)-1)
	}
	want := []string{
		"etc/localai/node.yaml", "etc/localai/models.ini", "etc/localai/api-key", "etc/localai/agent-token",
		"etc/localai/cloud-credential", "etc/localai/admin-pin", "etc/localai/hf-token",
		"etc/localai/catalog/CATALOG_VERSION", "etc/localai/catalog/models.yaml", "etc/localai/catalog/profiles/cpu-only.yaml",
		"var/lib/localai/state.json", "etc/systemd/system/localai-llama.service",
		"etc/systemd/system/localai-llama.service.d/50-env.conf",
	}
	for _, w := range want {
		if _, ok := files[w]; !ok {
			t.Errorf("%s is not in the backup", w)
		}
	}
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") {
			t.Errorf("path %q is not absolute-stripped", f.Path)
		}
		sum := sha256.Sum256([]byte(files[f.Path]))
		if f.SHA256 != hex.EncodeToString(sum[:]) || f.Size != int64(len(files[f.Path])) {
			t.Errorf("%s: manifest hash or size does not match the content", f.Path)
		}
		if f.Mode == "" || f.Owner != "testowner" || f.Group != "testgroup" {
			t.Errorf("%s: mode/owner/group %+v", f.Path, f)
		}
	}
	byPath := map[string]ManifestFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	if byPath["etc/localai/admin-pin"].Mode != "0600" || byPath["etc/localai/node.yaml"].Mode != "0640" {
		t.Errorf("modes not recorded: %+v %+v", byPath["etc/localai/admin-pin"], byPath["etc/localai/node.yaml"])
	}
}

func TestBackupResultReportsSecrets(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	res, err := Backup(e, BackupOptions{Out: t.TempDir()})
	if err != nil || !res.SecretsIncluded {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = Backup(e, BackupOptions{Out: t.TempDir(), NoSecrets: true})
	if err != nil || res.SecretsIncluded {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestBackupNoSecretsHoldsNoneOfTheFiveSecretFiles(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	res, err := Backup(e, BackupOptions{Out: filepath.Join(t.TempDir(), "x.tar.gz"), NoSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	m, files, _ := readBackup(t, res.Path)
	if m.SecretsIncluded {
		t.Error("secrets_included is true")
	}
	for _, n := range secretNames {
		if _, ok := files["etc/localai/"+n]; ok {
			t.Errorf("%s is in a --no-secrets backup", n)
		}
	}
	for name, content := range files {
		if strings.Contains(content, "secret-of-") {
			t.Errorf("%s holds a secret", name)
		}
	}
	if len(m.Files) != 13 { // 18 files of populate minus the five secrets
		t.Errorf("%d files, want 13", len(m.Files))
	}
}

func TestBackupOptionalFilesMayBeAbsent(t *testing.T) {
	e, _ := testEnv(t)
	write(t, filepath.Join(e.Paths.EtcDir, "node.yaml"), "node:\n  id: ai-n7\n", 0o640)
	res, err := Backup(e, BackupOptions{Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || res.NodeID != "ai-n7" || res.SecretsIncluded {
		t.Errorf("%+v", res)
	}
}

func TestBackupOfAnEmptyNodeIsAnError(t *testing.T) {
	e, _ := testEnv(t)
	_, err := Backup(e, BackupOptions{Out: t.TempDir()})
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("got %v", err)
	}
}

func TestBackupNodeIDIsNeverAPath(t *testing.T) {
	for _, yaml := range []string{"", "node: [", "node:\n  id: ../../etc\n", "node:\n  id: ai-n0\n", "node:\n  id: AI-N3\n"} {
		e, _ := testEnv(t)
		write(t, e.Paths.NodeYAML(), yaml, 0o640)
		if got := NodeID(e); got != "unconfigured" {
			t.Errorf("%q: node id %q", yaml, got)
		}
	}
}

func TestBackupRefusesToOverwriteAndToFollowLinks(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	target := filepath.Join(t.TempDir(), "mine.tar.gz")
	write(t, target, "precious", 0o644)
	_, err := Backup(e, BackupOptions{Out: target})
	var fe *FileError
	if !errors.As(err, &fe) || fe.Reason() != "it already exists" {
		t.Fatalf("overwrite: %v", err)
	}
	if read(t, target) != "precious" {
		t.Error("the existing file was changed")
	}
	// a symlinked secret is not followed
	if err := os.Remove(filepath.Join(e.Paths.EtcDir, "api-key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(e.Paths.EtcDir, "api-key")); err != nil {
		t.Fatal(err)
	}
	_, err = Backup(e, BackupOptions{Out: filepath.Join(t.TempDir(), "b.tar.gz")})
	if !errors.As(err, &fe) || fe.Reason() != "it is not a regular file" {
		t.Fatalf("symlink: %v", err)
	}
}

func TestBackupToDriveKeepsTheLastFivePerNode(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	drive := addDrive(t, e, "a", false)
	dir := filepath.Join(drive.Path, "kit", "config", "ai-n3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("localai-config-ai-n3-2026100%dT000000Z.tar.gz", i)), "old", 0o600)
	}
	write(t, filepath.Join(dir, "localai-config-ai-n9-20260101T000000Z.tar.gz"), "other node", 0o600)
	write(t, filepath.Join(dir, "notes.txt"), "mine", 0o600)

	res, err := Backup(e, BackupOptions{ToDrive: &drive})
	if err != nil {
		t.Fatal(err)
	}
	if res.PruneErr != nil || filepath.Dir(res.Path) != dir {
		t.Fatalf("%+v", res)
	}
	var left []string
	for _, f := range listFiles(t, dir) {
		left = append(left, f)
	}
	sort.Strings(left)
	if len(left) != 5+2 { // five backups of ai-n3, the other node's backup and the note
		t.Fatalf("left %v", left)
	}
	if !contains(left, "localai-config-ai-n3-20261102T150405Z.tar.gz") || contains(left, "localai-config-ai-n3-20261001T000000Z.tar.gz") ||
		contains(left, "localai-config-ai-n3-20261002T000000Z.tar.gz") || !contains(left, "localai-config-ai-n9-20260101T000000Z.tar.gz") || !contains(left, "notes.txt") {
		t.Errorf("wrong files kept: %v", left)
	}
	if len(res.Pruned) != 2 {
		t.Errorf("pruned %v", res.Pruned)
	}
	// secrets live here: the two folders are private, the kit folder stays readable
	if modeOf(t, dir) != 0o700 || modeOf(t, filepath.Join(drive.Path, "kit", "config")) != 0o700 {
		t.Error("config folders are not 0700")
	}
}

func TestBackupToFreshDriveCreatesTheKitFolders(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	drive := addDrive(t, e, "a", false)
	if _, err := Backup(e, BackupOptions{ToDrive: &drive}); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, filepath.Join(drive.Path, "kit")); m != 0o755 {
		t.Errorf("kit mode %o, want 0755", m)
	}
}

func TestBackupToReadOnlyDriveIsRefused(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	drive := addDrive(t, e, "a", true)
	_, err := Backup(e, BackupOptions{ToDrive: &drive})
	var fe *FileError
	if !errors.As(err, &fe) || fe.Reason() != "the drive is mounted read-only" {
		t.Fatalf("got %v", err)
	}
	if exists(filepath.Join(drive.Path, "kit")) {
		t.Error("something was created on a read-only drive")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- restore ----

func TestRestoreRoundTripRestoresModesOwnersAndKeepsOriginals(t *testing.T) {
	e, run := testEnv(t)
	populate(t, e)
	res, err := Backup(e, BackupOptions{Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// the node drifts: edited files, a different mode, a stray catalog file, a missing secret
	etc := e.Paths.EtcDir
	write(t, filepath.Join(etc, "node.yaml"), "node:\n  id: ai-n3\nprofile: other\n", 0o600)
	write(t, filepath.Join(etc, "admin-pin"), "a-newer-pin-hash\n", 0o644)
	if err := os.Remove(filepath.Join(etc, "cloud-credential")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.Paths.CatalogDir(), "stray.yaml"), "x", 0o640)
	write(t, filepath.Join(e.Paths.CatalogDir(), "models.yaml"), "changed", 0o640)
	run.Expect(fake.Key("/usr/bin/systemctl", "daemon-reload"), execx.Result{}, nil)
	run.Expect(fake.Key("/usr/bin/systemctl", "restart", "localai-agent.service"), execx.Result{}, nil)

	plan, err := PrepareRestore(e, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Foreign || plan.ThisNode != "ai-n3" || !plan.CatalogChanges {
		t.Fatalf("plan %+v", plan)
	}
	// unchanged files are listed but marked Same
	same := map[string]bool{}
	for _, it := range plan.Items {
		same[filepath.Base(it.Path)] = it.Same
	}
	if !same["models.ini"] || same["node.yaml"] || same["admin-pin"] || same["cloud-credential"] {
		t.Errorf("same flags: %v", same)
	}
	// nothing has changed yet
	if read(t, filepath.Join(etc, "node.yaml")) == "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n" {
		t.Fatal("PrepareRestore changed a file")
	}

	rr, err := plan.Apply(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if rr.ServiceErr != nil {
		t.Errorf("service error %v", rr.ServiceErr)
	}
	// content, mode and owner are the backup's
	if read(t, filepath.Join(etc, "node.yaml")) != "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\n" || modeOf(t, filepath.Join(etc, "node.yaml")) != 0o640 {
		t.Error("node.yaml not restored")
	}
	if read(t, filepath.Join(etc, "admin-pin")) != "secret-of-admin-pin\n" || modeOf(t, filepath.Join(etc, "admin-pin")) != 0o600 {
		t.Error("admin-pin not restored with 0600")
	}
	if read(t, filepath.Join(etc, "cloud-credential")) != "secret-of-cloud-credential\n" {
		t.Error("the missing file was not recreated")
	}
	if exists(filepath.Join(e.Paths.CatalogDir(), "stray.yaml")) || read(t, filepath.Join(e.Paths.CatalogDir(), "models.yaml")) == "changed" {
		t.Error("the catalog folder was not replaced as a whole")
	}
	if uid, gid := ownerOf(mustStat(t, filepath.Join(etc, "admin-pin"))); uid != e.RootUID || gid != e.RootGID {
		t.Errorf("owner %d:%d", uid, gid)
	}
	if m := modeOf(t, e.Paths.CatalogDir()); m != 0o750 {
		t.Errorf("catalog folder mode %o", m)
	}
	// the originals were kept as <file>.pre-restore, with their old content and mode
	if read(t, filepath.Join(etc, "node.yaml.pre-restore")) != "node:\n  id: ai-n3\nprofile: other\n" || modeOf(t, filepath.Join(etc, "node.yaml.pre-restore")) != 0o600 {
		t.Error("node.yaml.pre-restore wrong")
	}
	if read(t, filepath.Join(etc, "admin-pin.pre-restore")) != "a-newer-pin-hash\n" || modeOf(t, filepath.Join(etc, "admin-pin.pre-restore")) != 0o644 {
		t.Error("admin-pin.pre-restore wrong")
	}
	if exists(filepath.Join(etc, "models.ini.pre-restore")) || exists(filepath.Join(etc, "cloud-credential.pre-restore")) {
		t.Error("a pre-restore copy was made for an unchanged or missing file")
	}
	if read(t, filepath.Join(etc, "catalog.pre-restore", "stray.yaml")) != "x" {
		t.Error("catalog.pre-restore does not hold the old catalog")
	}
	if len(rr.Kept) != 3 {
		t.Errorf("kept %v", rr.Kept)
	}
	// daemon-reload, then the agent restart, with the fixed argv
	calls := run.Calls()
	if len(calls) != 2 || fake.Key(calls[0].Path, calls[0].Args...) != "/usr/bin/systemctl daemon-reload" ||
		fake.Key(calls[1].Path, calls[1].Args...) != "/usr/bin/systemctl restart localai-agent.service" {
		t.Errorf("systemctl calls: %+v", calls)
	}
	if exists(e.Paths.CatalogDir() + ".restoring") {
		t.Error("staging folder left behind")
	}
	for _, f := range listFiles(t, etc) {
		if strings.Contains(f, ".tmp-") {
			t.Errorf("temp file %s left behind", f)
		}
	}
}

func TestRestoreTwiceKeepsTheTrueOriginals(t *testing.T) {
	e, run := testEnv(t)
	populate(t, e)
	res, err := Backup(e, BackupOptions{Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	etc := e.Paths.EtcDir
	write(t, filepath.Join(etc, "node.yaml"), "the-original-before-restore\n", 0o640)
	run.Expect(fake.Key("/usr/bin/systemctl", "daemon-reload"), execx.Result{}, nil)
	run.Expect(fake.Key("/usr/bin/systemctl", "restart", "localai-agent.service"), execx.Result{}, nil)
	for i := 0; i < 2; i++ {
		plan, err := PrepareRestore(e, res.Path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (plan.Changes() != 0 || plan.CatalogChanges) {
			t.Fatalf("after a restore everything must be the same: %d changes", plan.Changes())
		}
		if _, err := plan.Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if read(t, filepath.Join(etc, "node.yaml.pre-restore")) != "the-original-before-restore\n" {
		t.Error("a second restore overwrote the true original")
	}
}

func TestRestoreOntoARebuiltNodeUsesNamesNotNumbers(t *testing.T) {
	e, run := testEnv(t)
	populate(t, e)
	res, err := Backup(e, BackupOptions{Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// a fresh machine: nothing there, other node id, the groups have other numbers
	e2, run2 := testEnv(t)
	_ = run
	var asked []string
	e2.LookupGroup = func(n string) (int, error) { asked = append(asked, n); return os.Getgid(), nil }
	run2.Expect(fake.Key("/usr/bin/systemctl", "daemon-reload"), execx.Result{}, nil)
	run2.Expect(fake.Key("/usr/bin/systemctl", "restart", "localai-agent.service"), execx.Result{}, nil)
	plan, err := PrepareRestore(e2, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Foreign || plan.ThisNode != "unconfigured" || plan.Manifest.NodeID != "ai-n3" {
		t.Errorf("foreign %v this %q backup %q", plan.Foreign, plan.ThisNode, plan.Manifest.NodeID)
	}
	if len(asked) == 0 || asked[0] != "testgroup" {
		t.Errorf("groups were not resolved by name: %v", asked)
	}
	if _, err := plan.Apply(context.Background(), e2); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(e2.Paths.EtcDir, "admin-pin")) != "secret-of-admin-pin\n" || !exists(filepath.Join(e2.Paths.SystemdDir, "localai-llama.service.d", "50-env.conf")) {
		t.Error("files missing on the rebuilt node")
	}
	if exists(filepath.Join(e2.Paths.EtcDir, "admin-pin.pre-restore")) {
		t.Error("a pre-restore copy of a file that did not exist")
	}
}

func TestRestoreServiceFailureIsReportedButFilesAreInPlace(t *testing.T) {
	e, run := testEnv(t)
	populate(t, e)
	res, _ := Backup(e, BackupOptions{Out: t.TempDir()})
	write(t, filepath.Join(e.Paths.EtcDir, "models.ini"), "x", 0o640)
	run.Expect(fake.Key("/usr/bin/systemctl", "daemon-reload"), execx.Result{}, nil)
	run.Expect(fake.Key("/usr/bin/systemctl", "restart", "localai-agent.service"), execx.Result{ExitCode: 5}, nil)
	plan, err := PrepareRestore(e, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := plan.Apply(context.Background(), e)
	if err != nil || rr.ServiceErr == nil {
		t.Fatalf("err %v, service %v", err, rr)
	}
	if read(t, filepath.Join(e.Paths.EtcDir, "models.ini")) != "[*]\nmodels-max = 1\n" {
		t.Error("files not restored")
	}
}

func TestRestoreWriteFailureStopsAndLeavesTheRest(t *testing.T) {
	e, _ := testEnv(t)
	populate(t, e)
	res, _ := Backup(e, BackupOptions{Out: t.TempDir()})
	write(t, filepath.Join(e.Paths.EtcDir, "node.yaml"), "edited", 0o640)
	plan, err := PrepareRestore(e, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	e.FS = cfake.New(config.OSFS{}).FailOn("rename", 1)
	if _, err := plan.Apply(context.Background(), e); err == nil {
		t.Fatal("no error")
	}
	if read(t, filepath.Join(e.Paths.EtcDir, "node.yaml")) != "edited" {
		t.Error("node.yaml changed although its write failed")
	}
}

// ---- hostile and damaged archives ----

type craftEntry struct {
	name  string
	typ   byte
	data  string
	link  string
	mode  int64
	extra bool // in the archive but not in the manifest
}

// craft writes a .tar.gz with the given entries. manifest files default to one per regular entry
// (unless extra), with a correct hash; mutate may change the manifest before it is written.
func craft(t *testing.T, entries []craftEntry, mutate func(*Manifest)) string {
	t.Helper()
	m := Manifest{NodeID: "ai-n3", CreatedAt: "2026-11-02T15:04:05Z", AgentVersion: "x", SecretsIncluded: true}
	for _, en := range entries {
		if en.typ != tar.TypeReg && en.typ != 0 || en.extra {
			continue
		}
		sum := sha256.Sum256([]byte(en.data))
		m.Files = append(m.Files, ManifestFile{Path: en.name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(en.data)), Mode: "0640", UID: 0, GID: 0})
	}
	if mutate != nil {
		mutate(&m)
	}
	mj, _ := json.Marshal(m)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, data string) {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if data != "" {
			_, _ = tw.Write([]byte(data))
		}
	}
	add(&tar.Header{Name: manifestName, Typeflag: tar.TypeReg, Size: int64(len(mj)), Mode: 0o600}, string(mj))
	for _, en := range entries {
		typ := en.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := en.mode
		if mode == 0 {
			mode = 0o640
		}
		h := &tar.Header{Name: en.name, Typeflag: typ, Mode: mode, Linkname: en.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(en.data))
		}
		add(h, en.data)
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "crafted.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRestoreRefusesHostileOrDamagedBackups(t *testing.T) {
	good := craftEntry{name: "etc/localai/models.ini", data: "ok"}
	cases := []struct {
		name    string
		entries []craftEntry
		mutate  func(*Manifest)
		reason  string
	}{
		{"parent traversal", []craftEntry{{name: "../etc/passwd", data: "x"}}, nil, "unsafe path"},
		{"traversal inside", []craftEntry{{name: "etc/localai/../../passwd", data: "x"}}, nil, "unsafe path"},
		{"absolute path", []craftEntry{{name: "/etc/localai/node.yaml", data: "x"}}, nil, "unsafe path"},
		{"backslash", []craftEntry{{name: `etc\localai\node.yaml`, data: "x"}}, nil, "unsafe path"},
		{"symlink entry", []craftEntry{good, {name: "etc/localai/node.yaml", typ: tar.TypeSymlink, link: "/etc/shadow"}}, nil, "link or special file"},
		{"hardlink entry", []craftEntry{good, {name: "etc/localai/node.yaml", typ: tar.TypeLink, link: "etc/localai/models.ini"}}, nil, "link or special file"},
		{"device entry", []craftEntry{good, {name: "etc/localai/node.yaml", typ: tar.TypeChar}}, nil, "link or special file"},
		{"duplicate entry", []craftEntry{good, good}, nil, "twice"},
		{"outside the allowlist", []craftEntry{{name: "etc/passwd", data: "x"}}, nil, "may not write"},
		{"unknown file in etc/localai", []craftEntry{{name: "etc/localai/evil.sh", data: "x"}}, nil, "may not write"},
		{"other systemd unit", []craftEntry{{name: "etc/systemd/system/evil.service", data: "x"}}, nil, "may not write"},
		{"catalog folder itself", []craftEntry{{name: "etc/localai/catalog", data: "x"}}, nil, "may not write"},
		{"not in the manifest", []craftEntry{good, {name: "etc/localai/node.yaml", data: "x", extra: true}}, nil, "manifest does not list"},
		{"in the manifest only", []craftEntry{good}, func(m *Manifest) {
			m.Files = append(m.Files, ManifestFile{Path: "etc/localai/node.yaml", SHA256: strings.Repeat("0", 64), Mode: "0640"})
		}, "not in the archive"},
		{"hash mismatch", []craftEntry{good}, func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("a", 64) }, "checksum"},
		{"size mismatch", []craftEntry{good}, func(m *Manifest) { m.Files[0].Size = 99 }, "checksum"},
		{"setuid mode", []craftEntry{good}, func(m *Manifest) { m.Files[0].Mode = "4755" }, "unsafe mode"},
		{"world-writable mode", []craftEntry{good}, func(m *Manifest) { m.Files[0].Mode = "0666" }, "unsafe mode"},
		{"bad mode text", []craftEntry{good}, func(m *Manifest) { m.Files[0].Mode = "rw-r--r--" }, "unsafe mode"},
		{"repeated manifest path", []craftEntry{good}, func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) }, "unsafe or repeated"},
		{"manifest traversal", []craftEntry{good}, func(m *Manifest) { m.Files[0].Path = "etc/localai/../../x" }, "unsafe or repeated"},
		{"no files", nil, nil, "no files"},
		{"secrets claimed absent", []craftEntry{{name: "etc/localai/admin-pin", data: "h"}}, func(m *Manifest) { m.SecretsIncluded = false }, "no secrets"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := testEnv(t)
			populate(t, e)
			before := snapshot(t, e.Paths.EtcDir)
			_, err := PrepareRestore(e, craft(t, c.entries, c.mutate))
			var be *BackupError
			if !errors.As(err, &be) || !strings.Contains(be.Reason, c.reason) {
				t.Fatalf("got %v, want a BackupError containing %q", err, c.reason)
			}
			after := snapshot(t, e.Paths.EtcDir)
			if fmt.Sprint(before) != fmt.Sprint(after) {
				t.Error("a refused backup changed the node")
			}
		})
	}
}

func TestRestoreRefusesFilesThatAreNotBackups(t *testing.T) {
	e, _ := testEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plain.txt"), "hello", 0o644)
	var truncated []byte
	{
		p := craft(t, []craftEntry{{name: "etc/localai/models.ini", data: strings.Repeat("x", 5000)}}, nil)
		b, _ := os.ReadFile(p)
		truncated = b[:len(b)/2]
	}
	write(t, filepath.Join(dir, "cut.tar.gz"), string(truncated), 0o644)
	// gzip of something that is not a tar
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("not a tar archive at all"))
	_ = gz.Close()
	write(t, filepath.Join(dir, "nottar.tar.gz"), buf.String(), 0o644)
	for _, name := range []string{"plain.txt", "cut.tar.gz", "nottar.tar.gz"} {
		_, err := PrepareRestore(e, filepath.Join(dir, name))
		var be *BackupError
		if !errors.As(err, &be) {
			t.Errorf("%s: got %v, want a BackupError", name, err)
		}
	}
	if _, err := PrepareRestore(e, filepath.Join(dir, "missing.tar.gz")); !isFileErr(err, "it does not exist") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := PrepareRestore(e, dir); !isFileErr(err, "it is not a regular file") {
		t.Errorf("a folder: %v", err)
	}
}

func TestRestoreRefusesAnArchiveWithoutAManifest(t *testing.T) {
	e, _ := testEnv(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "etc/localai/models.ini", Typeflag: tar.TypeReg, Size: 1, Mode: 0o640})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "n.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var be *BackupError
	if _, err := PrepareRestore(e, p); !errors.As(err, &be) || !strings.Contains(be.Reason, "MANIFEST.json") {
		t.Fatalf("got %v", err)
	}
}

func TestRestoreRefusesATooLargeEntry(t *testing.T) {
	e, _ := testEnv(t)
	big := craftEntry{name: "etc/localai/models.ini", data: strings.Repeat("x", maxBackupFile+1)}
	var be *BackupError
	if _, err := PrepareRestore(e, craft(t, []craftEntry{big}, nil)); !errors.As(err, &be) || !strings.Contains(be.Reason, "too large") {
		t.Fatalf("got %v", err)
	}
}

func isFileErr(err error, reason string) bool {
	var fe *FileError
	return errors.As(err, &fe) && fe.Reason() == reason
}
