package update

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// bigBinary is a fake agent with enough bytes to cut in the middle: the first line is what the fake exec
// reads, the rest is padding that makes every byte count in the sha256.
func bigBinary(version string) []byte {
	return append(fakeBinary(version, testArch), []byte(strings.Repeat("0123456789abcdef", 256))...)
}

// cutDownload makes the first request for the binary stop after n bytes of the promised length, the way a
// dropped connection does, and serves every later request normally.
func cutDownload(r *rig, n int, data []byte) {
	cut := false
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if cut || !strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
			return false
		}
		cut = true
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data[:n])
		w.(http.Flusher).Flush()
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
		return true
	}
}

func TestDownloadResumesAPartialFile(t *testing.T) {
	r := newRig(t, "0.1.0")
	bin := bigBinary("0.2.0")
	rel := r.release("0.2.0")
	rel.Assets[BinaryName(testArch)] = bin
	sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): fakeBinary("0.2.0", "arm64")})
	rel.Assets[SumsName] = sums
	rel.Assets[SigName] = r.signer.Sign(sums, updatetest.SignOptions{})
	cutDownload(r, 1500, bin)

	run := r.runner("0.1.0")
	_, err := run.Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT009)

	// The client applied the first part: the partial file is on disk, the final name is not.
	stage := filepath.Join(r.paths.UpdateDir, "0.2.0")
	part, perr := os.Stat(filepath.Join(stage, BinaryName(testArch)+".part"))
	if perr != nil || part.Size() == 0 || part.Size() > 1500 {
		t.Fatalf("partial file: %v %v", part, perr)
	}
	have := part.Size() // what the client applied, which is what the next request must ask to resume from
	if r.exists(filepath.Join(stage, BinaryName(testArch))) {
		t.Fatal("an incomplete download must never have the final name")
	}
	if r.exists(r.paths.Pending()) || r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
		t.Fatal("nothing may be installed from a partial download")
	}

	// The next run resumes where the file stopped and installs.
	r.exec.reset()
	if _, err := run.Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	var ranges []string
	for _, q := range r.srv.Requests() {
		if strings.HasSuffix(q.Path, "/"+BinaryName(testArch)) {
			ranges = append(ranges, q.Range)
		}
	}
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != fmt.Sprintf("bytes=%d-", have) {
		t.Fatalf("binary requests carried ranges %q", ranges)
	}
	if got := r.agentContent(); got != string(bin) {
		t.Fatal("the resumed binary is not the one that was signed")
	}
}

func TestDownloadRestartsWhenTheServerIgnoresRange(t *testing.T) {
	r := newRig(t, "0.1.0")
	bin := bigBinary("0.2.0")
	rel := r.release("0.2.0")
	rel.Assets[BinaryName(testArch)] = bin
	sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): fakeBinary("0.2.0", "arm64")})
	rel.Assets[SumsName], rel.Assets[SigName] = sums, r.signer.Sign(sums, updatetest.SignOptions{})

	// A leftover partial file of an earlier run, and a server that answers every request with the whole file.
	stage := filepath.Join(r.paths.UpdateDir, "0.2.0")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, BinaryName(testArch)+".part"), bin[:700], 0o600); err != nil {
		t.Fatal(err)
	}
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if !strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
			return false
		}
		_, _ = w.Write(bin)
		return true
	}
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if got := r.agentContent(); got != string(bin) {
		t.Fatal("a server that ignores Range must give a clean whole file, not a spliced one")
	}
}

func TestDownloadRangeNotSatisfiableStartsOver(t *testing.T) {
	r := newRig(t, "0.1.0")
	bin := bigBinary("0.2.0")
	rel := r.release("0.2.0")
	rel.Assets[BinaryName(testArch)] = bin
	sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): fakeBinary("0.2.0", "arm64")})
	rel.Assets[SumsName], rel.Assets[SigName] = sums, r.signer.Sign(sums, updatetest.SignOptions{})
	stage := filepath.Join(r.paths.UpdateDir, "0.2.0")
	_ = os.MkdirAll(stage, 0o700)
	// A partial file longer than the real one (a different, bigger build earlier).
	_ = os.WriteFile(filepath.Join(stage, BinaryName(testArch)+".part"), append(bin, bin...), 0o600)
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if got := r.agentContent(); got != string(bin) {
		t.Fatal("the stale partial file must be replaced by the whole download")
	}
}

func TestDownloadRefusesAWrongContentRange(t *testing.T) {
	r := newRig(t, "0.1.0")
	bin := bigBinary("0.2.0")
	rel := r.release("0.2.0")
	rel.Assets[BinaryName(testArch)] = bin
	sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): fakeBinary("0.2.0", "arm64")})
	rel.Assets[SumsName], rel.Assets[SigName] = sums, r.signer.Sign(sums, updatetest.SignOptions{})
	stage := filepath.Join(r.paths.UpdateDir, "0.2.0")
	_ = os.MkdirAll(stage, 0o700)
	_ = os.WriteFile(filepath.Join(stage, BinaryName(testArch)+".part"), bin[:500], 0o600)
	// 206 answers that start somewhere else than asked: appending them would splice the file.
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if req.Header.Get("Range") == "" || !strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
			return false
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(bin)-1, len(bin)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(bin)
		return true
	}
	// The wrong answer is thrown away, the request repeats from the start and the whole file arrives; the
	// hook only answers the ranged request.
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if got := r.agentContent(); got != string(bin) {
		t.Fatal("the installed binary is not the signed one")
	}
}

func TestDownloadSizeCaps(t *testing.T) {
	cases := map[string]string{
		"SHA256SUMS too large":          SumsName,
		"signature too large":           SigName,
		"binary without a content size": BinaryName(testArch),
	}
	for name, asset := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "0.1.0")
			r.release("0.2.0")
			limit := map[string]int64{SumsName: maxSumsBytes, SigName: maxSignatureFile, BinaryName(testArch): MaxBinaryBytes}[asset]
			// A chunked answer has no Content-Length: the cap is enforced while reading.
			r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
				if !strings.HasSuffix(req.URL.Path, "/"+asset) {
					return false
				}
				w.Header().Set("Transfer-Encoding", "chunked")
				chunk := []byte(strings.Repeat("x", 1<<20))
				for sent := int64(0); sent <= limit; sent += int64(len(chunk)) {
					if _, err := w.Write(chunk); err != nil {
						return true
					}
					w.(http.Flusher).Flush()
				}
				return true
			}
			_, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"})
			if err == nil {
				t.Fatal("an oversized file must be refused")
			}
			if c := CodeOf(err); c != doctor.AGT009 {
				t.Fatalf("code %s: %v", c, err)
			}
			if r.exists(r.paths.Pending()) || r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
				t.Fatal("nothing may be installed")
			}
			if entries, _ := os.ReadDir(filepath.Join(r.paths.UpdateDir, "0.2.0")); entries != nil {
				for _, e := range entries {
					if info, _ := e.Info(); info != nil && info.Size() > limit+1 {
						t.Fatalf("%s holds %d bytes, more than the cap", e.Name(), info.Size())
					}
				}
			}
		})
	}
}

// The addresses of the downloads come from the validated repository and the parsed tag, never from the API
// answer, so a hostile answer cannot send the agent to another host.
func TestDownloadAddressesAreBuiltLocally(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
		if strings.HasSuffix(req.URL.Path, "/releases") {
			_, _ = w.Write([]byte(`[{"tag_name":"node-v0.2.0","assets":[` +
				`{"name":"SHA256SUMS","browser_download_url":"https://evil.example.test/SHA256SUMS"},` +
				`{"name":"SHA256SUMS.minisig","browser_download_url":"https://evil.example.test/x"},` +
				`{"name":"aiagent-linux-amd64","browser_download_url":"https://evil.example.test/y"}]}]`))
			return true
		}
		return false
	}
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range r.srv.Requests() {
		if strings.Contains(q.Path, "evil") {
			t.Fatalf("a request went to an address from the API answer: %s", q.Path)
		}
	}
	if r.downloads() != 3 {
		t.Fatalf("want 3 downloads, saw %d", r.downloads())
	}
}
