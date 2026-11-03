package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// verify checks a staged release (steps 3 and 4): the minisign signature over SHA256SUMS against the
// embedded keys first, and only then the sha256 of the binary against its line of the signed file. Nothing
// from SHA256SUMS is looked at before the signature has verified. Any failure is LAI-AGT-010.
func (r *Runner) verify(st Staged) error {
	const msg = "The downloaded update failed its signature or checksum check and was discarded."
	sums, err := readLimited(r.FS, st.Sums, maxSumsBytes)
	if err != nil {
		return fail(doctor.AGT010, "signature", msg, err)
	}
	sig, err := readLimited(r.FS, st.Sig, maxSignatureFile)
	if err != nil {
		return fail(doctor.AGT010, "signature", msg, err)
	}
	if _, err := Verify(r.Keys, sig, sums); err != nil {
		return fail(doctor.AGT010, "signature", msg, err)
	}
	entries, err := ParseSums(sums)
	if err != nil {
		return fail(doctor.AGT010, "checksum", msg, err)
	}
	want, ok := entries.Lookup(st.BinName)
	if !ok {
		return fail(doctor.AGT010, "checksum", msg, fmt.Errorf("%s is not listed in SHA256SUMS", st.BinName))
	}
	got, _, err := sha256File(r.FS, st.Binary, MaxBinaryBytes)
	if err != nil {
		return fail(doctor.AGT010, "checksum", msg, err)
	}
	if !hashEqual(got, want.Hash) {
		return fail(doctor.AGT010, "checksum", msg, errors.New("the binary does not match its sha256"))
	}
	return nil
}

// smokeInfo is `aiagent version --json`.
type smokeInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// smoke runs the verified staged binary (`<staged> version --json`, 5 s) and requires the expected version
// and architecture (step 5). The binary is made executable first; it lives in a root-only folder. A binary
// that does not run, or reports something else, is never installed: the update fails with LAI-AGT-009 and
// the installed agent is untouched.
func (r *Runner) smoke(ctx context.Context, st Staged, want Version) error {
	const msg = "The new agent did not pass its start-up test, so it was not installed."
	if err := r.FS.Chmod(st.Binary, 0o755); err != nil {
		return fail(doctor.AGT009, "smoke", msg, err)
	}
	res, err := r.Exec.Run(ctx, execx.Cmd{Path: st.Binary, Args: []string{"version", "--json"}, Timeout: SmokeTimeout, MaxOut: 64 << 10})
	if err != nil {
		return fail(doctor.AGT009, "smoke", msg, err)
	}
	if res.ExitCode != 0 {
		return fail(doctor.AGT009, "smoke", msg, fmt.Errorf("exit status %d", res.ExitCode))
	}
	var info smokeInfo
	if err := json.Unmarshal(res.Stdout, &info); err != nil {
		return fail(doctor.AGT009, "smoke", msg, fmt.Errorf("the output is not JSON: %w", err))
	}
	switch {
	case info.Version != want.String():
		return fail(doctor.AGT009, "smoke", msg, fmt.Errorf("it reports version %q, not %q", truncate(info.Version, 32), want))
	case info.Arch != r.Arch:
		return fail(doctor.AGT009, "smoke", msg, fmt.Errorf("it reports architecture %q, not %q", truncate(info.Arch, 16), r.Arch))
	case info.OS != "linux":
		return fail(doctor.AGT009, "smoke", msg, fmt.Errorf("it reports os %q", truncate(info.OS, 16)))
	}
	return nil
}

// install is step 6: the running binary is copied to aiagent.prev (overwritten, atomically), the staged one
// is written to aiagent.new with mode 0755, fsynced and renamed over aiagent. A reader (or a crash) sees the
// old binary or the new one, never half of one. aiagent.prev is kept for good (ADR-0001).
func (r *Runner) install(st Staged) error {
	const msg = "The new agent could not be installed; the current one was left as it is."
	if err := r.FS.MkdirAll(r.Paths.BinDir, 0o755); err != nil {
		return fail(doctor.AGT009, "install", msg, err)
	}
	if fi, err := r.FS.Stat(r.Paths.Agent()); err != nil || !fi.Mode().IsRegular() {
		return fail(doctor.AGT009, "install", msg, fmt.Errorf("the installed agent %s is not a file: %v", r.Paths.Agent(), err))
	}
	if err := r.copyBinary(r.Paths.Agent(), r.Paths.Prev()+".tmp", r.Paths.Prev()); err != nil {
		return fail(doctor.AGT009, "install", msg, err)
	}
	if err := r.copyBinary(st.Binary, r.Paths.New(), r.Paths.Agent()); err != nil {
		return fail(doctor.AGT009, "install", msg, err)
	}
	return nil
}

// restorePrev puts aiagent.prev back as aiagent: copy to aiagent.new, then rename (agent.md section 4.10
// step 10). It never touches aiagent.prev.
func (r *Runner) restorePrev() error {
	fi, err := r.FS.Stat(r.Paths.Prev())
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return fmt.Errorf("the previous agent %s is not available: %v", r.Paths.Prev(), err)
	}
	return r.copyBinary(r.Paths.Prev(), r.Paths.New(), r.Paths.Agent())
}

// copyBinary copies src to tmp (0755, fsynced) and renames tmp to dst, then syncs the folder. On any failure
// tmp is removed and dst is untouched.
func (r *Runner) copyBinary(src, tmp, dst string) (err error) {
	in, err := r.FS.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := r.FS.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if err != nil {
			if !closed {
				_ = out.Close()
			}
			_ = r.FS.Remove(tmp)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(in, MaxBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	if n > MaxBinaryBytes {
		return fmt.Errorf("%s is larger than %d bytes", filepath.Base(src), MaxBinaryBytes)
	}
	if err = out.Chmod(0o755); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	closed = true
	if err = out.Close(); err != nil {
		return err
	}
	if err = r.FS.Rename(tmp, dst); err != nil {
		return err
	}
	if serr := r.FS.SyncDir(filepath.Dir(dst)); serr != nil && !isUnsupportedSync(serr) {
		r.Log.Warn("the folder of the agent binary could not be synced", "error", serr.Error())
	}
	return nil
}

// isUnsupportedSync reports a directory fsync that the filesystem does not support.
func isUnsupportedSync(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}
