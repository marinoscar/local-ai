package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Limits of a download (agent.md section 4.10 step 2).
const (
	// MaxBinaryBytes is the cap on the agent binary.
	MaxBinaryBytes int64 = 200 << 20
	// DefaultDownloadTimeout bounds each asset download.
	DefaultDownloadTimeout = 10 * time.Minute

	partSuffix = ".part"
)

// BinaryName is the release asset of the agent for an architecture ("aiagent-linux-amd64").
func BinaryName(arch string) string { return "aiagent-linux-" + arch }

// SupportedArch reports whether releases carry a binary for arch.
func SupportedArch(arch string) bool { return arch == "amd64" || arch == "arm64" }

// Staged is a release downloaded into its staging directory. Nothing in it is trusted until Verify has run.
type Staged struct {
	Dir      string
	Sums     string
	Sig      string
	Binary   string
	BinName  string
	Version  Version
	downSize int64
}

// stageDir is /var/lib/localai/update/<version>/.
func (p Paths) stageDir(v Version) string { return filepath.Join(p.UpdateDir, v.String()) }

// download stages SHA256SUMS, its signature and the binary for arch (step 2). A file that was only partly
// downloaded by an earlier run is resumed with a Range request; a complete binary from an earlier run is
// reused (it is verified like a fresh one). The three files are fetched from addresses built by the Source,
// never from an answer of the API. Every size is capped.
func (r *Runner) download(ctx context.Context, rel ReleaseInfo, arch string) (Staged, error) {
	st := Staged{Version: rel.Version, BinName: BinaryName(arch)}
	st.Dir = r.Paths.stageDir(rel.Version)
	st.Sums = filepath.Join(st.Dir, SumsName)
	st.Sig = filepath.Join(st.Dir, SigName)
	st.Binary = filepath.Join(st.Dir, st.BinName)

	for _, name := range []string{SumsName, SigName, st.BinName} {
		if !rel.Has(name) {
			return st, fail(doctor.AGT010, "download", "The release is incomplete: "+name+" is missing, so it was not installed.", nil)
		}
	}
	if err := r.FS.MkdirAll(r.Paths.UpdateDir, 0o750); err != nil {
		return st, fail(doctor.AGT009, "download", "The update folder could not be created.", err)
	}
	if err := r.FS.MkdirAll(st.Dir, 0o700); err != nil {
		return st, fail(doctor.AGT009, "download", "The update folder could not be created.", err)
	}
	if err := r.FS.Chmod(st.Dir, 0o700); err != nil {
		return st, fail(doctor.AGT009, "download", "The update folder could not be locked down.", err)
	}
	src := r.Source
	// The two small files are always fetched again: a stale copy is never worth keeping.
	for _, f := range []struct {
		name, dst string
		limit     int64
	}{{SumsName, st.Sums, maxSumsBytes}, {SigName, st.Sig, maxSignatureFile}} {
		_ = r.FS.Remove(f.dst)
		_ = r.FS.Remove(f.dst + partSuffix)
		if err := r.fetch(ctx, src.assetURL(rel.Tag, f.name), f.dst, f.limit); err != nil {
			return st, err
		}
	}
	if fi, err := r.FS.Stat(st.Binary); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 && fi.Size() <= MaxBinaryBytes {
		st.downSize = fi.Size()
		return st, nil
	}
	if err := r.fetch(ctx, src.assetURL(rel.Tag, st.BinName), st.Binary, MaxBinaryBytes); err != nil {
		return st, err
	}
	if fi, err := r.FS.Stat(st.Binary); err == nil {
		st.downSize = fi.Size()
	}
	return st, nil
}

// fetch downloads address into dst through dst+".part", resuming a partial file, within limit bytes.
func (r *Runner) fetch(ctx context.Context, address, dst string, limit int64) error {
	part := dst + partSuffix
	timeout := r.DownloadTimeout
	if timeout <= 0 {
		timeout = DefaultDownloadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var have int64
	if fi, err := r.FS.Stat(part); err == nil && fi.Mode().IsRegular() {
		have = fi.Size()
		if have >= limit {
			_ = r.FS.Remove(part)
			have = 0
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return fail(doctor.AGT099, "download", "The download request could not be made.", err)
		}
		req.Header.Set("User-Agent", r.Source.userAgent())
		req.Header.Set("Accept", "application/octet-stream")
		// An identity encoding keeps Range offsets and the size cap about the bytes that are stored.
		req.Header.Set("Accept-Encoding", "identity")
		if have > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		}
		resp, err := r.Source.client().Do(req)
		if err != nil {
			return netError("download", err)
		}
		retry, err := r.store(resp, part, have, limit)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if retry && attempt == 0 {
			_ = r.FS.Remove(part)
			have = 0
			continue
		}
		if retry {
			return fail(doctor.AGT009, "download", "GitHub did not give a usable file.", nil)
		}
		break
	}
	if err := r.FS.Rename(part, dst); err != nil {
		return fail(doctor.AGT009, "download", "The downloaded file could not be put in place.", err)
	}
	return nil
}

// store writes one response into part. retry is true when the partial file must be thrown away and the
// request repeated from the start (the server ignored or refused the Range).
func (r *Runner) store(resp *http.Response, part string, have, limit int64) (retry bool, err error) {
	appendMode := false
	switch resp.StatusCode {
	case http.StatusOK:
		have = 0
	case http.StatusPartialContent:
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != have || have == 0 {
			return true, nil
		}
		appendMode = true
	case http.StatusRequestedRangeNotSatisfiable:
		return true, nil
	case http.StatusNotFound:
		return false, fail(doctor.AGT010, "download", "The release is incomplete: a file is missing on GitHub, so it was not installed.", nil)
	default:
		return false, fail(doctor.AGT009, "download", fmt.Sprintf("GitHub answered %d for a download.", resp.StatusCode), nil)
	}
	if resp.ContentLength >= 0 && have+resp.ContentLength > limit {
		return false, fail(doctor.AGT009, "download", "The file is larger than the agent allows, so it was not downloaded.", nil)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := r.FS.OpenFile(part, flags, 0o600)
	if err != nil {
		return false, fail(doctor.AGT009, "download", "The download could not be written to disk.", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit-have+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		// The partial file is kept: a later run resumes it.
		return false, fail(doctor.AGT009, "download", "The download was interrupted; run the update again to resume it.", copyErr)
	case have+n > limit:
		_ = r.FS.Remove(part)
		return false, fail(doctor.AGT009, "download", "The file is larger than the agent allows, so it was not downloaded.", nil)
	case resp.ContentLength >= 0 && n != resp.ContentLength:
		return false, fail(doctor.AGT009, "download", "The download ended early; run the update again to resume it.", io.ErrUnexpectedEOF)
	case syncErr != nil || closeErr != nil:
		return false, fail(doctor.AGT009, "download", "The download could not be written to disk.", errors.Join(syncErr, closeErr))
	}
	return false, nil
}

// contentRangeStart parses "bytes 100-199/200" and returns 100.
func contentRangeStart(h string) (int64, bool) {
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(first, 10, 64)
	return n, err == nil && n >= 0
}
