// Package resolve holds the resolvers and producers of the kit and docs source types of ark sync
// (ark-sync.md sections 4.9.6 to 4.9.8): git_mirror, apt_closure and repo_docs. They fill the rebuild
// kit (kit/git, kit/src, kit/debs/<codename>-<arch>, kit/catalog) and the manuals (docs/).
//
// Each source type has two halves that share one type:
//
//   - the planner half, Resolve(ctx, plan.CatalogItem) (plan.Resolved, error), registered per source
//     type with plan.Resolvers.Register (see Register). It reads metadata only and reduces the answer
//     to the version, sort key and size the planner needs. A failure leaves the item unresolved, which
//     is the offline behaviour of the planner: the plan keeps what is present and skips the rest.
//   - the fetch half, Produce(ctx, Request) (Product, error), which the sync engine calls while the
//     item is "downloading". It writes only below <root>/.staging/<item>/<version>/, except for the
//     one in-place operation the spec allows: a git mirror update fetches into the present mirror
//     ("append-mostly, copies nothing"). It never swaps, never writes a MANIFEST and never deletes:
//     the Product says what to swap where (Part) and what the manifests should record.
//
// Rules this package keeps (AGENTS.md rules 2 and 3, ark-sync.md sections 5 and 6):
//
//   - every subprocess goes through execx with a fixed argv, an absolute binary from the fixed PATH, a
//     minimal environment, a bounded runtime and a bounded output; nothing from the network or from a
//     catalog field is ever interpreted by a shell, and a value that becomes part of an argv is
//     validated first (a URL scheme and host, a 40-hex commit, a Debian package name);
//   - the network is touched only when Env.Online says so (the planner and the engine gate survival and
//     offline mode; this is the second lock), and only the hosts the catalog item names, over https
//     for git and with apt's signed indexes for packages;
//   - pins are enforced: an expected commit that is absent, or a deb whose size or sha256 differs from
//     the signed index, is an error and the bytes are not installed (the downloader quarantines);
//   - git operations and the docs renderer never execute fetched content, and a tarball is unpacked
//     only by the safe extractor of this package (no traversal, no links, no devices, bounded size);
//   - everything produced is usable with no network: the mirror, the tarballs, the closure with its
//     Packages index and the rendered docs.
package resolve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// The doctor codes of the resolvers, aliased from the single table (doctor/codes.go).
const (
	// CodeMirror: the mirror is damaged or lacks the pinned commit (LAI-SYN-024).
	CodeMirror = doctor.SYN024
	// CodeApt: the package set could not be computed or is not trustworthy (LAI-SYN-060).
	CodeApt = doctor.SYN060
	// CodeTool: git, apt-get or another needed tool is missing (LAI-SYN-061).
	CodeTool = doctor.SYN061
	// CodeNoInventory: manuals were made without the real lab values (LAI-SYN-062, yellow, not a failure).
	CodeNoInventory = doctor.SYN062
	// CodeNoPDF: the survival card PDF could not be made (LAI-CLI-014, yellow, not a failure).
	CodeNoPDF = doctor.CLI014
	// CodeCatalogLint: the catalog of the tarball failed lint, the old kit/catalog is kept (LAI-CAT-002).
	CodeCatalogLint = doctor.CAT002
	// CodeOffline: a network operation was asked for while the node must not use the network
	// (LAI-SYN-002).
	CodeOffline = doctor.SYN002
	// CodeRefused: the source refused or does not have what the catalog names (LAI-SYN-030).
	CodeRefused = doctor.SYN030
	// CodeVerify: a verification of what was produced failed (LAI-SYN-020 family, used for the docs
	// tree check that has no code of its own: the item is not installed).
	CodeVerify = doctor.SYN020
)

// Error is the error of this package: a doctor code and one plain sentence. Cause details that came
// from a subprocess are bounded and stripped of control characters (see Clip).
type Error struct {
	// Code is the doctor code of the condition.
	Code string
	// Msg says what happened.
	Msg string
	// Err is the cause, may be nil.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code + ": " + e.Msg
	}
	return e.Code + ": " + e.Msg + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

func fail(code string, err error, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the doctor code carried by err ("" when it carries none): this package's Error, or
// the downloader's.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var d *download.Error
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

// ErrOffline is wrapped by every refusal to use the network (CodeOffline).
var ErrOffline = errors.New("resolve: the node must not use the network now")

// Clip makes a subprocess message safe to put in an error or a log line: control characters become
// spaces, runs of space collapse and the result is cut to max runes.
func Clip(b []byte, max int) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, string(bytes.ToValidUTF8(b, []byte("?"))))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "..."
	}
	return s
}

// Env is everything the resolvers touch outside their own memory. Set Online (a nil Online means
// "never online", the safe default), and the rest as needed by the types in use. Every field has a
// seam so tests run hermetically.
type Env struct {
	// Runner runs subprocesses; default execx.Exec{}. Tests use execx/fake or the real runner over a
	// local fixture.
	Runner execx.Runner
	// Lookup finds a binary inside the fixed PATH; default execx.Lookup.
	Lookup func(name string) (string, error)
	// Downloader fetches plain files (debs, the docs tarball); required by apt_closure and by the
	// network path of repo_docs.
	Downloader *download.Downloader
	// Bucket is the shared bandwidth bucket; with a limit, subprocesses are throttled through the
	// proxy (throttle.StartProxy). nil is unlimited.
	Bucket throttle.Bucket
	// StartProxy starts the throttling proxy; default throttle.StartProxy.
	StartProxy func(ctx context.Context, b throttle.Bucket, allowHosts []string) (addr string, stop func(), err error)
	// Online reports whether the node may use the network now (online mode and the internet
	// reachable). The planner and the engine gate survival and offline mode already; this is the second
	// lock. nil means false.
	Online func() bool
	// Now is the clock; default time.Now. A test reads it once and shares it.
	Now func() time.Time
	// Logger receives notices; default slog.Default. Nothing secret is ever logged.
	Logger *slog.Logger
	// SyncHome is /var/lib/localai/sync: the HOME of git, and the parent of the private apt trees.
	SyncHome string
}

func (e *Env) runner() execx.Runner {
	if e.Runner != nil {
		return e.Runner
	}
	return execx.Exec{}
}

func (e *Env) lookup(name string) (string, error) {
	if e.Lookup != nil {
		return e.Lookup(name)
	}
	return execx.Lookup(name)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) log() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// requireOnline is the defence-in-depth gate in front of every network operation.
func (e *Env) requireOnline() error {
	if e.Online != nil && e.Online() {
		return nil
	}
	return fail(CodeOffline, ErrOffline, "not fetching, the node is offline or in survival mode")
}

// tool finds a program, or fails with LAI-SYN-061 naming it.
func (e *Env) tool(name string) (string, error) {
	p, err := e.lookup(name)
	if err != nil {
		return "", fail(CodeTool, nil, "%s is not installed", name)
	}
	return p, nil
}

// startProxy starts the throttling proxy for hosts when a bandwidth limit applies. addr "" means no
// proxy (unlimited): the caller runs the subprocess direct.
func (e *Env) startProxy(ctx context.Context, hosts []string) (addr string, stop func(), err error) {
	start := e.StartProxy
	if start == nil {
		start = throttle.StartProxy
	}
	addr, stop, err = start(ctx, e.Bucket, hosts)
	if stop == nil {
		stop = func() {}
	}
	return addr, stop, err
}

// cmd is one subprocess call.
type cmd struct {
	bin     string
	args    []string
	env     []string
	timeout time.Duration
	maxOut  int64
	cred    *execx.Credential
}

// Output caps of the subprocesses (ark-sync.md section 6: every subprocess result is size-bounded).
const (
	smallOut = 1 << 20  // 1 MiB: rev-parse, ls-remote, clone and fsck chatter
	aptOut   = 16 << 20 // 16 MiB: apt-get update and --print-uris
	showOut  = 96 << 20 // 96 MiB: apt-cache show of a whole closure
)

// run runs one command. A command that could not start, timed out, or was cancelled is an error; a
// non-zero exit and a truncated output are returned in the Result for the caller to judge.
func (e *Env) run(ctx context.Context, c cmd) (execx.Result, error) {
	return e.runner().Run(ctx, execx.Cmd{
		Path: c.bin, Args: c.args, Env: c.env, Timeout: c.timeout, MaxOut: c.maxOut, Credential: c.cred,
	})
}

// Register registers the planner half of each resolver under its catalog source type. A second
// registration of a type is an error (plan.Resolvers.Register).
func Register(reg *plan.Resolvers, rs ...NamedResolver) error {
	for _, r := range rs {
		if err := reg.Register(r.SourceType(), r); err != nil {
			return err
		}
	}
	return nil
}

// NamedResolver is a planner resolver that knows its catalog source type.
type NamedResolver interface {
	plan.Resolver
	SourceType() string
}
