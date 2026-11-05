package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// Tests of `aictl verify-release` (AICTL-10, aictl.md section 4.27a). The release is built at run time
// and signed with a throwaway key; the trusted list is replaced for the test.

type releaseFix struct {
	t      *testing.T
	signer *updatetest.Signer
	dir    string
	files  map[string][]byte
}

// newReleaseFix writes four files, their SHA256SUMS and its signature into a temporary folder and makes
// the signer's key the trusted one.
func newReleaseFix(t *testing.T) *releaseFix {
	t.Helper()
	f := &releaseFix{t: t, signer: updatetest.NewSigner(), dir: t.TempDir(), files: map[string][]byte{
		"aiagent-linux-amd64": []byte("program one"),
		"aiagent-linux-arm64": []byte("program two"),
		"ai-desktop.deb":      []byte("package"),
		"README.txt":          []byte("notes"),
	}}
	for n, d := range f.files {
		f.write(n, d)
	}
	sums := updatetest.Sums(f.files)
	f.write(update.SumsName, sums)
	f.write(update.SigName, f.signer.Sign(sums, updatetest.SignOptions{TrustedComment: "timestamp:1 file:SHA256SUMS"}))
	old := verifyReleaseTrust
	pub, err := update.ParsePublicKey(f.signer.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	verifyReleaseTrust = func() []update.PublicKey { return []update.PublicKey{pub} }
	t.Cleanup(func() { verifyReleaseTrust = old })
	return f
}

func (f *releaseFix) write(name string, data []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *releaseFix) run(args ...string) followOut {
	f.t.Helper()
	return newMgRig(f.t, fake.Options{}).run(mgArgs(args...))
}

func TestVerifyReleaseGoodRelease(t *testing.T) {
	f := newReleaseFix(t)
	got := f.run("verify-release", f.dir)
	checkRun(t, got, 0, "✓ Signature OK (Local AI release key). 4 files checked: all match.\n", "")
	es := f.run("--lang", "es", "verify-release", f.dir)
	if es.code != 0 || !strings.Contains(flat(es.out), "Firma correcta (clave de publicación de Local AI). 4 archivos comprobados: todos coinciden.") {
		t.Fatalf("%+v", es)
	}
	v := f.run("-v", "verify-release", f.dir)
	if v.code != 0 || !strings.Contains(v.out, "Signed with key "+update.KeyIDString(f.signer.ID)) || !strings.Contains(v.out, "timestamp:1") {
		t.Fatalf("%+v", v)
	}
}

func TestVerifyReleaseJSON(t *testing.T) {
	f := newReleaseFix(t)
	got := f.run("--json", "verify-release", f.dir)
	if got.code != 0 || got.errOut != "" {
		t.Fatalf("%+v", got)
	}
	var doc struct {
		Signature string `json:"signature"`
		KeyID     string `json:"key_id"`
		Files     []struct{ Name, Status string }
		OK        bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(got.out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, got.out)
	}
	if doc.Signature != "ok" || !doc.OK || doc.KeyID != update.KeyIDString(f.signer.ID) || len(doc.Files) != 4 {
		t.Fatalf("%+v", doc)
	}
	for _, fl := range doc.Files {
		if fl.Status != "ok" {
			t.Errorf("%+v", fl)
		}
	}
}

func TestVerifyReleaseFailures(t *testing.T) {
	tamper := func(f *releaseFix) { f.write("ai-desktop.deb", []byte("changed")) }
	remove := func(f *releaseFix) {
		if err := os.Remove(filepath.Join(f.dir, "README.txt")); err != nil {
			f.t.Fatal(err)
		}
	}
	badSig := func(f *releaseFix) {
		// the list is edited after it was signed
		f.write(update.SumsName, append([]byte(nil), updatetest.Sums(map[string][]byte{"aiagent-linux-amd64": []byte("other")})...))
	}
	unknownKey := func(f *releaseFix) {
		other := updatetest.NewSigner()
		f.write(update.SigName, other.Sign(updatetest.Sums(f.files), updatetest.SignOptions{}))
	}
	garbage := func(f *releaseFix) { f.write(update.SigName, []byte("not a signature\n")) }
	cases := []struct {
		name   string
		mutate func(*releaseFix)
		kit    bool
		exit   int
		code   string
		says   string
		sig    string
		file   string // file status in --json
		status string
	}{
		{"a tampered file", tamper, false, ExitFailed, "LAI-AGT-010", "ai-desktop.deb", "ok", "ai-desktop.deb", "mismatch"},
		{"a missing file", remove, false, ExitFailed, "LAI-AGT-010", "README.txt", "ok", "README.txt", "missing"},
		{"a bad signature", badSig, false, ExitFailed, "LAI-AGT-010", "signature is not valid", "bad", "", ""},
		{"an unknown key id", unknownKey, false, ExitFailed, "LAI-AGT-010", "key this program does not know", "bad", "", ""},
		{"an unreadable signature", garbage, false, ExitFailed, "LAI-AGT-010", "not in the expected format", "bad", "", ""},
		{"a tampered file in a kit folder", func(f *releaseFix) { tamper(f); f.makeKit() }, true, ExitFailed, "LAI-KIT-044", "ai-desktop.deb", "ok", "ai-desktop.deb", "mismatch"},
		{"a bad signature in a kit folder", func(f *releaseFix) { badSig(f); f.makeKit() }, true, ExitFailed, "LAI-KIT-044", "signature is not valid", "bad", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFix(t)
			tc.mutate(f)
			got := f.run("verify-release", f.dir)
			if got.code != tc.exit || got.out != "" || !strings.Contains(got.errOut, "["+tc.code+"]") || !strings.Contains(flat(got.errOut), tc.says) {
				t.Fatalf("%+v", got)
			}
			if !strings.Contains(got.errOut, "Fix:") {
				t.Errorf("no fix line: %s", got.errOut)
			}
			j := f.run("--json", "verify-release", f.dir)
			if j.code != tc.exit || j.errOut != "" {
				t.Fatalf("json: %+v", j)
			}
			var doc releaseReport
			if err := json.Unmarshal([]byte(j.out), &doc); err != nil {
				t.Fatalf("%v\n%s", err, j.out)
			}
			if doc.Signature != tc.sig || doc.OK || (doc.KeyID == "") != (tc.name == "an unreadable signature") {
				t.Errorf("%+v", doc)
			}
			if tc.file != "" {
				found := false
				for _, fl := range doc.Files {
					if fl.Name == tc.file && fl.Status == tc.status {
						found = true
					}
				}
				if !found {
					t.Errorf("%s is not %s in %+v", tc.file, tc.status, doc.Files)
				}
			}
		})
	}
}

// makeKit turns the folder into a kit folder: it gets a kit/MANIFEST.json.
func (f *releaseFix) makeKit() {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.dir, kit.DirName), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, kit.DirName, kit.IndexFileName), []byte("{}"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func TestVerifyReleaseKitDirectoryRule(t *testing.T) {
	t.Run("a folder called kit with a MANIFEST.json is a kit folder", func(t *testing.T) {
		root := t.TempDir()
		kitDir := filepath.Join(root, kit.DirName)
		if err := os.MkdirAll(kitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if isKitDir(kitDir) {
			t.Error("a kit folder without a MANIFEST.json is not a kit folder")
		}
		if err := os.WriteFile(filepath.Join(kitDir, kit.IndexFileName), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !isKitDir(kitDir) || !isKitDir(root) {
			t.Error("kit/ with MANIFEST.json, and its parent, are kit folders")
		}
	})
	t.Run("another name or no MANIFEST.json is a release folder", func(t *testing.T) {
		root := t.TempDir()
		other := filepath.Join(root, "kit-bin")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, kit.IndexFileName), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if isKitDir(other) || isKitDir(root) {
			t.Error("not a kit folder")
		}
	})
	t.Run("the kit/bin folder of a kit is a release folder", func(t *testing.T) {
		// aictl.md section 4.27a: only the kit folder and its parent are kit folders.
		f := newReleaseFix(t)
		f.makeKit()
		sub := filepath.Join(f.dir, kit.DirName, "bin")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if isKitDir(sub) {
			t.Error("kit/bin is not a kit folder")
		}
	})
}

func TestVerifyReleaseMissingSums(t *testing.T) {
	for _, name := range []string{update.SumsName, update.SigName} {
		t.Run("without "+name, func(t *testing.T) {
			f := newReleaseFix(t)
			if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
				t.Fatal(err)
			}
			got := f.run("verify-release", f.dir)
			if got.code != ExitUsage || got.out != "" || !strings.Contains(flat(got.errOut), "SHA256SUMS or SHA256SUMS.minisig is missing") {
				t.Fatalf("%+v", got)
			}
			j := f.run("--json", "verify-release", f.dir)
			if j.code != ExitUsage || !strings.Contains(j.out, `"error"`) {
				t.Fatalf("%+v", j)
			}
		})
	}
	t.Run("an empty folder", func(t *testing.T) {
		got := newMgRig(t, fake.Options{}).run(mgArgs("verify-release", t.TempDir()))
		if got.code != ExitUsage {
			t.Fatalf("%+v", got)
		}
	})
}

func TestVerifyReleaseUsage(t *testing.T) {
	f := newReleaseFix(t)
	for _, args := range [][]string{
		{"verify-release"},
		{"verify-release", f.dir, f.dir},
		{"verify-release", filepath.Join(f.dir, "nope")},
		{"verify-release", filepath.Join(f.dir, update.SumsName)},
	} {
		got := f.run(args...)
		if got.code != ExitUsage || !strings.Contains(got.errOut, "[LAI-CLI-005]") {
			t.Errorf("%v: %+v", args, got)
		}
	}
}

func TestVerifyReleaseWithNoKeyBuiltIn(t *testing.T) {
	f := newReleaseFix(t)
	verifyReleaseTrust = func() []update.PublicKey { return nil }
	got := f.run("verify-release", f.dir)
	if got.code != ExitFailed || !strings.Contains(flat(got.errOut), "no release key built in") || !strings.Contains(got.errOut, "[LAI-AGT-010]") {
		t.Fatalf("%+v", got)
	}
}

func TestVerifyReleaseNeedsNoAgentAndNoRoot(t *testing.T) {
	f := newReleaseFix(t)
	got := newMgRig(t, fake.Options{NoSocket: true, NoTCP: true}).run(mgRun{args: []string{"verify-release", f.dir}, euid: 1000})
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestVerifyReleaseGolden(t *testing.T) {
	f := newReleaseFix(t)
	checkGolden(t, "verify-release.ok.en", runResult(f.run("verify-release", f.dir)))
	checkGolden(t, "verify-release.ok.es", runResult(f.run("--lang", "es", "verify-release", f.dir)))
	f.write("ai-desktop.deb", []byte("changed"))
	checkGolden(t, "verify-release.mismatch.en", runResult(f.run("verify-release", f.dir)))
	checkGolden(t, "verify-release.mismatch.es", runResult(f.run("--lang", "es", "verify-release", f.dir)))
}

// A kit's bin folder keeps the release's signed list in release/ and does not hold every file it names
// (the agent source is in kit/src): the check reads release/ and does not ask for the others.
func TestVerifyReleaseKitBinFolder(t *testing.T) {
	f := newReleaseFix(t)
	// Move the pair into release/ and add a line for a file that is not in this folder.
	whole := map[string][]byte{}
	for n, d := range f.files {
		whole[n] = d
	}
	whole["local-ai-node-v0.1.0-src.tar.gz"] = []byte("source")
	sums := updatetest.Sums(whole)
	for _, n := range []string{update.SumsName, update.SigName} {
		if err := os.Remove(filepath.Join(f.dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(f.dir, kit.ReleaseDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(kit.ReleaseDirName, update.SumsName), sums)
	f.write(filepath.Join(kit.ReleaseDirName, update.SigName), f.signer.Sign(sums, updatetest.SignOptions{}))

	got := f.run("verify-release", f.dir)
	checkRun(t, got, 0, "✓ Signature OK (Local AI release key). 4 files checked: all match.\n", "")
	j := f.run("--json", "verify-release", f.dir)
	var doc releaseReport
	if err := json.Unmarshal([]byte(j.out), &doc); err != nil || !doc.OK || doc.Signature != "ok" || doc.KeyID == "" || len(doc.Files) != 5 {
		t.Fatalf("%v %+v\n%s", err, doc, j.out)
	}

	// A changed binary is still caught.
	f.write("aiagent-linux-amd64", []byte("evil"))
	bad := f.run("verify-release", f.dir)
	if bad.code != ExitFailed || !strings.Contains(flat(bad.errOut), "aiagent-linux-amd64") {
		t.Fatalf("%+v", bad)
	}

	// A folder that still has the classic pair at its top is checked the classic way, including the
	// files that are missing.
	g := newReleaseFix(t)
	if err := os.Remove(filepath.Join(g.dir, "README.txt")); err != nil {
		t.Fatal(err)
	}
	if miss := g.run("verify-release", g.dir); miss.code != ExitFailed {
		t.Fatalf("a missing file of a classic release must fail: %+v", miss)
	}
}
