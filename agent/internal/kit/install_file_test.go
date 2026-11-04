package kit_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

func TestLocateChecksTheManifestAndInstallsNothing(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	body := []byte("kiwix tarball for x86_64")
	dir := putTool(t, kitDir, "tools/kiwix-tools", "3.8.2", map[string][]byte{"kiwix-tools_linux-x86_64-3.8.2.tar.gz": body})
	rec := &recorder{}
	ti, _ := newTools(t, nil)
	ti.Kiwix = rec.install
	loc, err := ti.Locate(context.Background(), kitDir, kit.ToolKiwix)
	if err != nil || loc.Version != "3.8.2" || loc.SHA256 != hexSHA(body) || loc.Path != filepath.Join(dir, "kiwix-tools_linux-x86_64-3.8.2.tar.gz") {
		t.Fatalf("loc %+v err %v", loc, err)
	}
	if len(rec.calls) != 0 {
		t.Fatal("Locate must not install")
	}
	writeFile(t, loc.Path, "tampered")
	if _, err := ti.Locate(context.Background(), kitDir, kit.ToolKiwix); !errors.Is(err, kit.ErrToolDamaged) {
		t.Fatalf("err %v", err)
	}
	if _, err := ti.Locate(context.Background(), kitDir, kit.ToolAgent); !errors.Is(err, kit.ErrToolNotInKit) {
		t.Fatalf("agent is not a tarball tool: %v", err)
	}
}

func TestInstallFileRefusesAHashMismatchBeforeInstalling(t *testing.T) {
	f := filepath.Join(t.TempDir(), "kiwix-tools_linux-x86_64-3.8.2.tar.gz")
	writeFile(t, f, "downloaded")
	rec := &recorder{}
	ti, _ := newTools(t, nil)
	ti.Kiwix = rec.install
	_, err := ti.InstallFile(context.Background(), kit.ToolKiwix, f, strings.Repeat("0", 64), "3.8.2")
	var te *kit.ToolError
	if !errors.As(err, &te) || te.Code() != doctor.KIT001 || len(rec.calls) != 0 {
		t.Fatalf("err %v calls %v", err, rec.calls)
	}
	res, err := ti.InstallFile(context.Background(), kit.ToolKiwix, f, hexSHA([]byte("downloaded")), "3.8.2")
	if err != nil || res.Version != "3.8.2" || len(rec.calls) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestReleaseFromURL(t *testing.T) {
	cases := []struct {
		tool, arch, url, want string
	}{
		{"kiwix-tools", "amd64", "https://download.kiwix.org/release/kiwix-tools/kiwix-tools_linux-x86_64-3.8.2.tar.gz", "3.8.2"},
		{"kiwix-tools", "arm64", "https://download.kiwix.org/release/kiwix-tools/kiwix-tools_linux-aarch64-3.8.2.tar.gz", "3.8.2"},
		{"kiwix-tools", "arm64", "https://x/kiwix-tools_linux-x86_64-3.8.2.tar.gz", ""},
		{"pmtiles", "amd64", "https://github.com/protomaps/go-pmtiles/releases/download/v1.31.2/go-pmtiles_1.31.2_Linux_x86_64.tar.gz", "1.31.2"},
		{"pmtiles", "arm64", "https://github.com/protomaps/go-pmtiles/releases/download/v1.31.2/go-pmtiles_1.31.2_Linux_arm64.tar.gz", "1.31.2"},
		{"piper", "amd64", "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz", "2023.11.14-2"},
		{"piper", "arm64", "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_aarch64.tar.gz", "2023.11.14-2"},
		{"piper", "amd64", "https://example.test/piper_linux_x86_64.tar.gz", ""},
		{"agent", "amd64", "https://x/aiagent-linux-amd64", ""},
	}
	for _, c := range cases {
		got, ok := kit.ReleaseFromURL(c.tool, c.arch, c.url)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s %s %s = %q,%v want %q", c.tool, c.arch, c.url, got, ok, c.want)
		}
	}
}
