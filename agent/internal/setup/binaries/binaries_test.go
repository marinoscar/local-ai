package binaries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

type fakeInst struct {
	calls []string
	// failTool fails that tool with err.
	failTool string
	err      error
}

func (f *fakeInst) Locate(_ context.Context, kitDir, tool string) (*kit.Located, error) {
	f.calls = append(f.calls, "locate "+tool)
	if tool == f.failTool {
		return nil, f.err
	}
	return &kit.Located{Path: kitDir + "/" + tool + ".tar.gz", SHA256: "aa", Version: "1.0"}, nil
}
func (f *fakeInst) Install(_ context.Context, kitDir, tool string) (*kit.ToolResult, error) {
	f.calls = append(f.calls, "install "+tool)
	if tool == f.failTool {
		return nil, f.err
	}
	return &kit.ToolResult{Tool: tool, Version: "1.0", Source: kitDir}, nil
}
func (f *fakeInst) InstallFile(_ context.Context, tool, tarball, sha, version string) (*kit.ToolResult, error) {
	f.calls = append(f.calls, "installfile "+tool+" "+filepath.Base(tarball)+" "+sha+" "+version)
	if tool == f.failTool {
		return nil, f.err
	}
	return &kit.ToolResult{Tool: tool, Version: version, Unchanged: tool == "piper"}, nil
}

type fakeFetch struct {
	urls []string
	err  error
	body string
}

func (f *fakeFetch) Fetch(_ context.Context, url, dest string) error {
	f.urls = append(f.urls, url)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(dest, []byte(f.body), 0o600)
}

func TestOfflineInstallsAllThreeFromTheKit(t *testing.T) {
	fi := &fakeInst{}
	rep, err := Run(context.Background(), fi, Options{KitDir: "/kit"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fi.calls, ",") != "install kiwix-tools,install pmtiles,install piper" {
		t.Fatalf("calls %v", fi.calls)
	}
	if rep.Online || len(rep.Tools) != 3 || rep.Tools[0].Status != "installed" {
		t.Fatalf("report %+v", rep)
	}
}

func TestOfflineDryRunOnlyVerifies(t *testing.T) {
	fi := &fakeInst{}
	rep, err := Run(context.Background(), fi, Options{KitDir: "/kit", DryRun: true, Tools: []string{"pmtiles"}})
	if err != nil || strings.Join(fi.calls, ",") != "locate pmtiles" || rep.Tools[0].Status != "would-install" {
		t.Fatalf("calls %v rep %+v err %v", fi.calls, rep, err)
	}
}

func TestOfflineDamagedTarballStopsWithItsKitCode(t *testing.T) {
	fi := &fakeInst{failTool: "pmtiles", err: &kit.ToolError{Tool: "pmtiles", Kind: kit.ErrToolDamaged}}
	rep, err := Run(context.Background(), fi, Options{KitDir: "/kit"})
	if err == nil || CodeOf(err) != doctor.KIT001 {
		t.Fatalf("err %v code %q", err, CodeOf(err))
	}
	if len(rep.Tools) != 2 || rep.Tools[1].Status != "failed" || rep.Tools[1].Code != doctor.KIT001 {
		t.Fatalf("report %+v", rep.Tools)
	}
	if strings.Contains(strings.Join(fi.calls, ","), "piper") {
		t.Fatal("must stop at the first failure")
	}
}

func TestOnlineDownloadsPinnedUrlThenInstallsWithItsHash(t *testing.T) {
	fi := &fakeInst{}
	ff := &fakeFetch{body: "x"}
	pins := map[string]Pin{
		"kiwix-tools": {URL: "https://h/kiwix-tools_linux-x86_64-3.8.2.tar.gz", SHA256: "11", Version: "3.8.2"},
		"pmtiles":     {URL: "https://h/go-pmtiles_1.31.2_Linux_x86_64.tar.gz", SHA256: "22", Version: "1.31.2"},
		"piper":       {URL: "https://h/piper_linux_x86_64.tar.gz", SHA256: "33", Version: "2023.11.14-2"},
	}
	tmp := t.TempDir()
	rep, err := Run(context.Background(), fi, Options{Pins: pins, Fetch: ff, TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Online || len(ff.urls) != 3 {
		t.Fatalf("rep %+v urls %v", rep, ff.urls)
	}
	if fi.calls[0] != "installfile kiwix-tools kiwix-tools_linux-x86_64-3.8.2.tar.gz 11 3.8.2" {
		t.Fatalf("calls %v", fi.calls)
	}
	if rep.Tools[2].Status != "unchanged" {
		t.Errorf("piper %+v", rep.Tools[2])
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("downloads left behind: %v", left)
	}
}

func TestOnlineDryRunDownloadsNothing(t *testing.T) {
	fi := &fakeInst{}
	ff := &fakeFetch{}
	rep, err := Run(context.Background(), fi, Options{Pins: map[string]Pin{"pmtiles": {URL: "https://h/p.tar.gz", Version: "1"}}, Fetch: ff, DryRun: true, Tools: []string{"pmtiles"}})
	if err != nil || len(ff.urls) != 0 || len(fi.calls) != 0 || rep.Tools[0].Status != "would-install" {
		t.Fatalf("rep %+v err %v", rep, err)
	}
}

func TestOnlineHashMismatchIsRefusedByTheInstaller(t *testing.T) {
	fi := &fakeInst{failTool: "pmtiles", err: &kit.ToolError{Tool: "pmtiles", Kind: kit.ErrToolDamaged}}
	_, err := Run(context.Background(), fi, Options{Tools: []string{"pmtiles"}, Fetch: &fakeFetch{body: "x"},
		Pins: map[string]Pin{"pmtiles": {URL: "https://h/p.tar.gz", SHA256: "00", Version: "1"}}})
	if CodeOf(err) != doctor.KIT001 {
		t.Fatalf("err %v", err)
	}
}

func TestOnlineDownloadFailureIsNoInternet(t *testing.T) {
	_, err := Run(context.Background(), &fakeInst{}, Options{Tools: []string{"pmtiles"}, Fetch: &fakeFetch{err: errors.New("dial tcp: no route")},
		Pins: map[string]Pin{"pmtiles": {URL: "https://h/p.tar.gz", SHA256: "00", Version: "1"}}})
	if CodeOf(err) != doctor.INS015 {
		t.Fatalf("code %q err %v", CodeOf(err), err)
	}
}

func TestMissingPinIsRefused(t *testing.T) {
	_, err := Run(context.Background(), &fakeInst{}, Options{Tools: []string{"piper"}, Fetch: &fakeFetch{}})
	if err == nil || CodeOf(err) != doctor.INS035 {
		t.Fatalf("err %v", err)
	}
}

func TestUnknownToolIsRefused(t *testing.T) {
	_, err := Run(context.Background(), &fakeInst{}, Options{Tools: []string{"agent"}, KitDir: "/kit"})
	if err == nil || CodeOf(err) != doctor.INS035 {
		t.Fatalf("err %v", err)
	}
}

func TestPinsFromTheRealCatalog(t *testing.T) {
	c, err := catalog.Load("../../../../catalog")
	if err != nil {
		t.Skipf("catalog not loadable from here: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		pins := PinsFromCatalog(c, nil, arch)
		for _, tool := range DefaultTools {
			p, ok := pins[tool]
			if !ok || len(p.SHA256) != 64 || p.Version == "" || !strings.HasPrefix(p.URL, "https://") {
				t.Errorf("%s/%s pin = %+v", arch, tool, p)
			}
		}
	}
}

func TestHTTPSFetcher(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("tarball"))
	}))
	defer srv.Close()
	f := HTTPSFetcher{Client: srv.Client()}
	dest := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := f.Fetch(context.Background(), srv.URL+"/ok", dest); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dest)
	sum := sha256.Sum256(b)
	if string(b) != "tarball" || hex.EncodeToString(sum[:]) == "" {
		t.Fatalf("body %q", b)
	}
	if err := f.Fetch(context.Background(), srv.URL+"/missing", filepath.Join(t.TempDir(), "y")); err == nil {
		t.Fatal("a 404 must fail")
	}
	if err := f.Fetch(context.Background(), strings.Replace(srv.URL, "https", "http", 1)+"/ok", filepath.Join(t.TempDir(), "z")); err == nil {
		t.Fatal("plain http must be refused")
	}
}
