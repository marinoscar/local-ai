package whisper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// CheckBuilt is the id of the doctor check below.
const CheckBuilt = "svc.whisper_built"

// CheckFS is the slice of the filesystem the doctor check reads. The real one is the operating
// system's; tests give it a temporary directory.
type CheckFS interface {
	Lstat(name string) (os.FileInfo, error)
	Stat(name string) (os.FileInfo, error)
	Readlink(name string) (string, error)
}

type osCheckFS struct{}

func (osCheckFS) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (osCheckFS) Stat(name string) (os.FileInfo, error)  { return os.Stat(name) }
func (osCheckFS) Readlink(name string) (string, error)   { return os.Readlink(name) }

// CheckOptions configures DoctorCheck. Every field is optional.
type CheckOptions struct {
	// Root is /opt/localai/whisper.cpp (Root); tests use a temporary directory.
	Root string
	// FS is the filesystem; default the operating system's.
	FS CheckFS
	// Enabled says whether whisper.enabled is true in node.yaml; nil means always. A node with
	// speech off has nothing to report.
	Enabled func() bool
	// Backend returns the backend this node should have built (whisper.backend, with auto resolved
	// to llama.backend); "" or nil means any build will do.
	Backend func() string
}

// DoctorCheck returns the fast doctor check that raises LAI-SVC-012 when
// /opt/localai/whisper.cpp/current/bin/whisper-server is missing, is not a program, or belongs to a
// build for another backend than the configured one (a cpu build on a node set to cuda is "not built
// for this computer": the fix, `sudo aictl rebuild-whisper`, is the same). The caller registers it
// (the package does not touch doctor.DefaultRegistry).
func DoctorCheck(o CheckOptions) doctor.Check {
	root := o.Root
	if root == "" {
		root = Root
	}
	var fsys CheckFS = o.FS
	if fsys == nil {
		fsys = osCheckFS{}
	}
	server := filepath.Join(root, "current", "bin", "whisper-server")
	return doctor.Check{ID: CheckBuilt, Area: "SVC", Fast: true, Run: func(context.Context) []doctor.Result {
		if o.Enabled != nil && !o.Enabled() {
			return nil
		}
		problem := func(reason string, kv ...any) []doctor.Result {
			d := map[string]any{"path": server, "reason": reason}
			for i := 0; i+1 < len(kv); i += 2 {
				d[kv[i].(string)] = kv[i+1]
			}
			return []doctor.Result{{Code: doctor.SVC012, Details: d}}
		}
		info, err := fsys.Stat(server) // through the current link
		switch {
		case err == nil && !info.Mode().IsRegular():
			return problem("not_a_file")
		case errors.Is(err, os.ErrNotExist):
			return problem("missing")
		case err != nil:
			return problem("error", "error", clip(err.Error(), 200))
		}
		want := ""
		if o.Backend != nil {
			want = strings.TrimSpace(o.Backend())
		}
		if want == "" {
			return nil
		}
		target, err := fsys.Readlink(filepath.Join(root, "current"))
		if err != nil {
			return nil // current is not a link we can read: the program exists, which is what matters
		}
		have := buildBackend(target)
		if have != "" && have != want {
			return problem("wrong_backend", "have", have, "want", want)
		}
		return nil
	}}
}

// buildBackend is the backend of a build directory name "<commit9>-<backend>" ("builds/1a2b3c4d5-cuda"),
// "" when the name has no backend suffix.
func buildBackend(target string) string {
	name := filepath.Base(filepath.Clean(target))
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return ""
	}
	switch b := name[i+1:]; b {
	case "cuda", "vulkan", "cpu":
		return b
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
