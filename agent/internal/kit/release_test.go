package kit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// The release's signed SHA256SUMS lives in kit/bin/release/ (the sync engine reserves that file name in an
// item folder, issue #360). The readers find it there and still accept the list beside the binaries.

func TestReleaseListDir(t *testing.T) {
	bin := t.TempDir()
	fsys := update.OSFS{}
	if got := kit.ReleaseListDir(fsys, bin); got != bin {
		t.Errorf("an empty folder: %q, want the folder itself", got)
	}
	writeFile(t, filepath.Join(bin, "SHA256SUMS"), "x")
	if got := kit.ReleaseListDir(fsys, bin); got != bin {
		t.Errorf("a list beside the binaries: %q", got)
	}
	// Either file of the release folder selects it; the list beside the binaries is then not used.
	writeFile(t, filepath.Join(bin, kit.ReleaseDirName, "SHA256SUMS.minisig"), "x")
	if got, want := kit.ReleaseListDir(fsys, bin), filepath.Join(bin, kit.ReleaseDirName); got != want {
		t.Errorf("a release folder: %q, want %q", got, want)
	}
	// A folder called release that holds neither file is not a list.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, kit.ReleaseDirName, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := kit.ReleaseListDir(fsys, other); got != other {
		t.Errorf("an unrelated release folder: %q", got)
	}
}

func TestVerifyBinLayouts(t *testing.T) {
	s := updatetest.NewSigner()
	pub, err := update.ParsePublicKey(s.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	keys := []update.PublicKey{pub}
	agent := map[string][]byte{"aiagent-linux-amd64": []byte("amd64"), "aiagent-linux-arm64": []byte("arm64")}
	whole := map[string][]byte{"install-node.sh": []byte("#!/bin/sh\n"), "local-ai-node-v0.1.0-src.tar.gz": []byte("source")}
	for n, b := range agent {
		whole[n] = b
	}
	sums := updatetest.Sums(whole)
	sig := s.Sign(sums, updatetest.SignOptions{})
	put := func(t *testing.T, listDir string) string {
		bin := t.TempDir()
		for n, b := range agent {
			writeFile(t, filepath.Join(bin, n), string(b))
		}
		writeFile(t, filepath.Join(bin, listDir, "SHA256SUMS"), string(sums))
		writeFile(t, filepath.Join(bin, listDir, "SHA256SUMS.minisig"), string(sig))
		return bin
	}
	check := func(t *testing.T, bin string) (update.DirReport, error) {
		t.Helper()
		return kit.VerifyBin(update.OSFS{}, bin, keys)
	}

	t.Run("the list in bin/release, with lines for files of other items", func(t *testing.T) {
		rep, err := check(t, put(t, kit.ReleaseDirName))
		if err != nil || rep.Checked() != 2 || len(rep.Files) != 4 {
			t.Fatalf("report %+v err %v", rep, err)
		}
	})
	t.Run("the list beside the binaries (a drive prepared by hand)", func(t *testing.T) {
		rep, err := check(t, put(t, "."))
		if err != nil || rep.Checked() != 2 {
			t.Fatalf("report %+v err %v", rep, err)
		}
	})
	t.Run("the release folder wins over a list beside the binaries", func(t *testing.T) {
		bin := put(t, kit.ReleaseDirName)
		// A stale list that does not verify, beside the binaries: it is not read.
		writeFile(t, filepath.Join(bin, "SHA256SUMS"), "garbage")
		writeFile(t, filepath.Join(bin, "SHA256SUMS.minisig"), "garbage")
		if _, err := check(t, bin); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a binary that changed after it was signed", func(t *testing.T) {
		bin := put(t, kit.ReleaseDirName)
		writeFile(t, filepath.Join(bin, "aiagent-linux-amd64"), "evil")
		var me *update.MismatchError
		if _, err := check(t, bin); !errors.As(err, &me) || len(me.Files) != 1 || me.Files[0] != "aiagent-linux-amd64" {
			t.Fatalf("want a mismatch of the amd64 binary, got %v", err)
		}
	})
	t.Run("a tampered signature", func(t *testing.T) {
		bin := put(t, kit.ReleaseDirName)
		writeFile(t, filepath.Join(bin, kit.ReleaseDirName, "SHA256SUMS.minisig"), string(updatetest.NewSigner().Sign(sums, updatetest.SignOptions{})))
		if _, err := check(t, bin); !errors.Is(err, update.ErrUnknownKey) {
			t.Fatalf("want ErrUnknownKey, got %v", err)
		}
	})
	t.Run("a release folder with the list but without its signature fails closed", func(t *testing.T) {
		bin := put(t, kit.ReleaseDirName)
		if err := os.Remove(filepath.Join(bin, kit.ReleaseDirName, "SHA256SUMS.minisig")); err != nil {
			t.Fatal(err)
		}
		// A good pair beside the binaries must not stand in for the release folder's missing signature.
		writeFile(t, filepath.Join(bin, "SHA256SUMS"), string(sums))
		writeFile(t, filepath.Join(bin, "SHA256SUMS.minisig"), string(sig))
		if _, err := check(t, bin); !errors.Is(err, update.ErrNoSums) {
			t.Fatalf("want ErrNoSums, got %v", err)
		}
	})
	t.Run("no list at all", func(t *testing.T) {
		if _, err := check(t, t.TempDir()); !errors.Is(err, update.ErrNoSums) {
			t.Fatalf("want ErrNoSums, got %v", err)
		}
	})
}

// A kit made before the release folder existed (the list beside the binaries) still installs and verifies.
func TestInstallAgentFromTheListBesideTheBinaries(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	// Move the pair out of bin/release, as the first kits and a hand-made drive have it.
	for _, n := range []string{"SHA256SUMS", "SHA256SUMS.minisig"} {
		data, err := os.ReadFile(k.rel(n))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, k.bin(n), string(data))
	}
	if err := os.RemoveAll(filepath.Join(k.kitDir, "bin", kit.ReleaseDirName)); err != nil {
		t.Fatal(err)
	}
	ti, opt := newTools(t, agentRunner(0))
	ti.Keys = k.keys
	installedAgent(t, opt, "0.4.0")
	if _, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.5.0", "amd64")) {
		t.Errorf("the agent was not installed: %s", got)
	}
}

// With the list only in bin/release (the layout the catalog produces) the install works, and a signature that
// does not verify is LAI-KIT-044 and replaces nothing.
func TestInstallAgentFromTheReleaseFolder(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	ti, opt := newTools(t, agentRunner(0))
	ti.Keys = k.keys
	installedAgent(t, opt, "0.4.0")
	res, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if err != nil || res.Unchanged {
		t.Fatalf("%+v %v", res, err)
	}
	if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.5.0", "amd64")) {
		t.Errorf("the agent was not installed: %s", got)
	}

	// The same kit with the signature of the release folder replaced by one of another key.
	k2 := newAgentKit(t, "0.5.0")
	writeFile(t, k2.rel("SHA256SUMS.minisig"), string(updatetest.NewSigner().Sign([]byte(readFile(t, k2.rel("SHA256SUMS"))), updatetest.SignOptions{})))
	ti2, opt2 := newTools(t, agentRunner(0))
	ti2.Keys = k2.keys
	installedAgent(t, opt2, "0.4.0")
	_, err = ti2.Install(context.Background(), k2.kitDir, kit.ToolAgent)
	var te *kit.ToolError
	if !errors.As(err, &te) || te.Code() != doctor.KIT044 {
		t.Fatalf("want LAI-KIT-044, got %v", err)
	}
}
