package kiwix

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Library regeneration (companion-services.md section 4.1.2). The merged library
// /var/lib/localai/kiwix/library.xml lists the best verified copy of every kiwix item across the
// mounted content roots; each writable root also gets <root>/kiwix/library.xml with relative paths, so
// a drive can be served on any machine that has only kiwix-tools.
//
// Decisions (recorded in the spec):
//   - The ZIM "name" used to find two copies of one book is MANIFEST source.book (no ZIM parsing); the
//     newest sort_key wins.
//   - kiwix-manage always writes into the state directory, as the localai user. The agent validates the
//     result and writes the final file itself (atomic write), so a read-only or vanishing drive never
//     leaves a half-written file and kiwix-manage never needs to write on a drive. A per-root library is
//     the same output with the root's own prefix turned into a relative path.
//   - Library itself never starts, stops or restarts localai-kiwix: kiwix-serve runs with
//     --monitorLibrary and reloads the file. The empty and non-empty transitions and the open-file check
//     after an eject are Service's (service.go, units.go).

const (
	// LibraryFile is the name of a library file.
	LibraryFile = "library.xml"
	// DefaultStateDir is /var/lib/localai/kiwix.
	DefaultStateDir = "/var/lib/localai/kiwix"

	// ManageTimeout bounds one kiwix-manage run (spec: 120 s).
	ManageTimeout = 120 * time.Second
	manageMaxOut  = 64 << 10

	libraryFileMode os.FileMode = 0o644
	libraryDirMode  os.FileMode = 0o755
	// emptyLibrary is what kiwix-manage writes for a library without books.
	emptyLibrary = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<library version=\"20110515\">\n</library>\n"
)

// Errors of the regeneration. Every one keeps the previous library file.
var (
	// ErrToolMissing: kiwix-manage is not installed.
	ErrToolMissing = errors.New("kiwix-manage is not installed")
	// ErrManage: kiwix-manage failed or its output is not the library asked for.
	ErrManage = errors.New("kiwix-manage failed")
)

// Finder is the part of the best-copy index regeneration reads; *ark.Index implements it.
type Finder interface {
	ItemIDs() []string
	Copies(itemID string) []ark.Copy
}

// AlertSink raises and clears doctor alerts; *doctor.Manager implements it.
type AlertSink interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
	Clear(code string)
}

// Library regenerates the library files.
type Library struct {
	// Finder is the best-copy index; required.
	Finder Finder
	// Roots lists the mounted content roots; required.
	Roots func() []ark.Root
	// Runner runs kiwix-manage; required.
	Runner execx.Runner
	// Tool is the kiwix-manage path; default DefaultRoot/current/kiwix-manage.
	Tool string
	// StateDir holds the merged library; default DefaultStateDir.
	StateDir string
	// Cred, when set, runs kiwix-manage as that user and owns the files and the state directory.
	Cred *execx.Credential
	// Alerts, when set, gets LAI-SVC-005 (rebuild failed) and LAI-SVC-001 (no books).
	Alerts AlertSink
	// Log is the logger; default slog.Default.
	Log *slog.Logger
	// Status, when set, receives the books of every good merged run (the health probe reads it).
	Status *Status

	mu sync.Mutex
	// last remembers the input of the last successful build per target, so an unchanged set of ZIMs
	// does not run kiwix-manage again.
	last map[string]string
}

// Result is what one Regenerate did.
type Result struct {
	// Books is the number of books in the merged library.
	Books int
	// Written is false when the input was unchanged and kiwix-manage did not run.
	Written bool
	// Skipped lists ZIMs kiwix-manage could not read (a damaged file); the others are listed.
	Skipped []string
	// Empty: the merged library has no books.
	Empty bool
	// List is the books of the merged library (without the skipped ones), for GET /services.
	List []BookRef
}

// RootResult is what RegenerateRoot did.
type RootResult struct {
	Root    string
	Books   int
	Written bool
	Skipped []string
	// NoWrite: the root is read-only, unmounted or has no kiwix books; nothing was written.
	NoWrite bool
}

func (l *Library) log() *slog.Logger {
	if l.Log != nil {
		return l.Log.With("component", "kiwix")
	}
	return slog.Default().With("component", "kiwix")
}

func (l *Library) tool() string {
	if l.Tool != "" {
		return l.Tool
	}
	return filepath.Join(DefaultRoot, CurrentName, BinManage)
}

func (l *Library) stateDir() string {
	if l.StateDir != "" {
		return l.StateDir
	}
	return DefaultStateDir
}

func (l *Library) ids() (int, int) {
	if l.Cred == nil {
		return -1, -1
	}
	return int(l.Cred.UID), int(l.Cred.GID)
}

// zim is one book file chosen for a library.
type zim struct {
	path    string
	name    string // source.book, or the item id
	sortKey string
	size    int64
	itemID  string // the catalog content id
	rootID  string
}

func bookRefs(zs []zim, skipped []string) []BookRef {
	skip := map[string]bool{}
	for _, p := range skipped {
		skip[p] = true
	}
	out := make([]BookRef, 0, len(zs))
	for _, z := range zs {
		if !skip[z.path] {
			out = append(out, BookRef{ContentID: z.itemID, Name: z.name, Root: z.rootID})
		}
	}
	return out
}

// collect returns the ZIMs of every kiwix item, one per book name (newest wins), sorted by path.
// onRoot "" means the best copy of each item on a mounted root; otherwise every copy on that root.
func (l *Library) collect(roots []ark.Root, onRoot string) []zim {
	mounted := map[string]bool{}
	for _, r := range roots {
		mounted[r.ID] = true
	}
	byName := map[string]zim{}
	for _, id := range l.Finder.ItemIDs() {
		copies := l.Finder.Copies(id)
		if len(copies) == 0 || copies[0].Manifest.ItemKind != manifest.KindKiwix {
			continue
		}
		for _, c := range copies {
			if !mounted[c.RootID] || (onRoot != "" && c.RootID != onRoot) {
				continue
			}
			for i, f := range c.Manifest.Files {
				if f.Kind != "zim" || i >= len(c.Files) {
					continue
				}
				fi, err := os.Stat(c.Files[i])
				if err != nil || !fi.Mode().IsRegular() {
					continue // a root that went away mid-run: leave the book out
				}
				name := c.Manifest.Source.String("book")
				if name == "" {
					name = id
				}
				z := zim{path: c.Files[i], name: name, sortKey: c.Manifest.SortKey, size: fi.Size(), itemID: id, rootID: c.RootID}
				if cur, ok := byName[name]; !ok || z.sortKey > cur.sortKey {
					byName[name] = z
				}
			}
			if onRoot == "" {
				break // the best copy only
			}
		}
	}
	out := make([]zim, 0, len(byName))
	for _, z := range byName {
		out = append(out, z)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func fingerprint(zs []zim) string {
	var b strings.Builder
	for _, z := range zs {
		b.WriteString(z.path + "\x00" + strconv.FormatInt(z.size, 10) + "\n")
	}
	return b.String()
}

// Regenerate rebuilds the merged library. On a kiwix-manage failure the old library stays, LAI-SVC-005
// is raised and the error is returned.
func (l *Library) Regenerate(ctx context.Context) (Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	zs := l.collect(l.Roots(), "")
	dst := filepath.Join(l.stateDir(), LibraryFile)
	res := Result{Empty: len(zs) == 0}
	fp := fingerprint(zs)
	if cur, ok := l.last[dst]; ok && cur == fp {
		if _, err := os.Stat(dst); err == nil {
			res.Books, res.List = len(zs), bookRefs(zs, nil)
			l.Status.Set(res.List)
			l.alertState(res)
			return res, nil
		}
	}
	if err := l.prepareDir(); err != nil {
		return res, l.failed(err)
	}
	data, skipped, err := l.build(ctx, filepath.Join(l.stateDir(), "library.xml.tmp"), zs)
	if err != nil {
		return res, l.failed(err)
	}
	uid, gid := l.ids()
	if err := config.WriteFileAtomic(dst, data, libraryFileMode, uid, gid); err != nil {
		return res, l.failed(fmt.Errorf("write %s: %w", dst, err))
	}
	l.remember(dst, fp)
	res.Written, res.Skipped = true, skipped
	res.Books = len(zs) - len(skipped)
	res.Empty = res.Books == 0
	res.List = bookRefs(zs, skipped)
	l.Status.Set(res.List)
	l.log().Info("the merged library was rebuilt", "books", res.Books, "skipped", len(skipped))
	l.alertState(res)
	return res, nil
}

func (l *Library) remember(key, fp string) {
	if l.last == nil {
		l.last = map[string]string{}
	}
	l.last[key] = fp
}

// failed raises LAI-SVC-005 and returns err.
func (l *Library) failed(err error) error {
	l.log().Warn("the library list could not be rebuilt; the old list is kept", "code", doctor.SVC005, "error", err.Error())
	if l.Alerts != nil {
		_ = l.Alerts.Raise(doctor.SVC005, map[string]any{"error": err.Error()})
	}
	return err
}

// alertState clears LAI-SVC-005 after a good run and raises or clears LAI-SVC-001 by the book count.
func (l *Library) alertState(r Result) {
	if l.Alerts == nil {
		return
	}
	l.Alerts.Clear(doctor.SVC005)
	if r.Empty {
		_ = l.Alerts.Raise(doctor.SVC001, map[string]any{})
	} else {
		l.Alerts.Clear(doctor.SVC001)
	}
}

func (l *Library) prepareDir() error {
	if err := fsperm.MkdirAll(l.stateDir(), libraryDirMode); err != nil {
		return err
	}
	if uid, gid := l.ids(); uid >= 0 {
		return os.Chown(l.stateDir(), uid, gid)
	}
	return nil
}

// RegenerateRoot rebuilds <root>/kiwix/library.xml for one root: only that root's ZIMs, relative
// paths. A read-only root, an unmounted one, or one without kiwix books is not written.
func (l *Library) RegenerateRoot(ctx context.Context, rootID string) (RootResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	res := RootResult{Root: rootID, NoWrite: true}
	var root *ark.Root
	roots := l.Roots()
	for i := range roots {
		if roots[i].ID == rootID {
			root = &roots[i]
		}
	}
	if root == nil || root.ReadOnly || root.Path == "" {
		return res, nil
	}
	zs := l.collect(roots, rootID)
	if len(zs) == 0 {
		return res, nil
	}
	prefix := filepath.Join(root.Path, "kiwix") + string(filepath.Separator)
	dst := filepath.Join(root.Path, "kiwix", LibraryFile)
	fp := fingerprint(zs)
	if cur, ok := l.last[dst]; ok && cur == fp {
		if _, err := os.Stat(dst); err == nil {
			res.NoWrite, res.Books = false, len(zs)
			return res, nil
		}
	}
	if err := l.prepareDir(); err != nil {
		return res, l.rootFailed(rootID, err)
	}
	data, skipped, err := l.build(ctx, filepath.Join(l.stateDir(), "library."+rootID+".xml.tmp"), zs)
	if err != nil {
		return res, l.rootFailed(rootID, err)
	}
	data = relativize(data, prefix)
	res.Skipped = skipped
	res.Books = len(zs) - len(skipped)
	res.NoWrite = false
	if old, rerr := os.ReadFile(dst); rerr == nil && bytes.Equal(old, data) {
		l.remember(dst, fp)
		return res, nil
	}
	uid, gid := l.ids()
	if err := fsperm.MkdirAll(filepath.Dir(dst), libraryDirMode); err != nil {
		return res, l.rootFailed(rootID, err)
	}
	if err := config.WriteFileAtomic(dst, data, libraryFileMode, uid, gid); err != nil {
		// The drive was pulled or went read-only while we worked.
		return res, l.rootFailed(rootID, fmt.Errorf("write %s: %w", dst, err))
	}
	l.remember(dst, fp)
	res.Written = true
	l.log().Info("a per-root library was rebuilt", "root", rootID, "books", res.Books)
	return res, nil
}

// rootFailed logs a per-root failure; it does not raise LAI-SVC-005, because the merged library (the one
// kiwix-serve reads) is unaffected.
func (l *Library) rootFailed(rootID string, err error) error {
	l.log().Warn("a per-root library could not be rebuilt", "root", rootID, "error", err.Error())
	return err
}

var pathAttr = regexp.MustCompile(`(\spath=")([^"]*)(")`)

// relativize turns every path under prefix into the part after it.
func relativize(data []byte, prefix string) []byte {
	return pathAttr.ReplaceAllFunc(data, func(m []byte) []byte {
		sub := pathAttr.FindSubmatch(m)
		p := strings.TrimPrefix(string(sub[2]), prefix)
		return []byte(string(sub[1]) + p + string(sub[3]))
	})
}

// build makes the library text for zs with kiwix-manage into tmp (state directory, as the service
// user), validates it, removes tmp and returns the text. When the batch fails and there are several
// files, each is tried alone so one damaged ZIM does not hide the others; the unreadable ones are
// returned as skipped. No ZIM at all gives the empty library without running anything.
func (l *Library) build(ctx context.Context, tmp string, zs []zim) (data []byte, skipped []string, err error) {
	if len(zs) == 0 {
		return []byte(emptyLibrary), nil, nil
	}
	if _, serr := os.Stat(l.tool()); serr != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrToolMissing, serr)
	}
	defer func() { _ = os.Remove(tmp) }()
	paths := make([]string, len(zs))
	for i, z := range zs {
		paths[i] = z.path
	}
	data, err = l.manage(ctx, tmp, paths)
	if err == nil || len(paths) < 2 || ctx.Err() != nil {
		return data, nil, err
	}
	batchErr := err
	var good []string
	for _, p := range paths {
		if _, perr := l.manage(ctx, tmp, []string{p}); perr != nil {
			skipped = append(skipped, p)
			l.log().Warn("a ZIM could not be added to the library", "code", doctor.SVC004, "file", p)
			continue
		}
		good = append(good, p)
	}
	if len(skipped) == 0 || len(good) == 0 {
		return nil, nil, batchErr // nothing to isolate, or nothing usable
	}
	data, err = l.manage(ctx, tmp, good)
	if err != nil {
		return nil, nil, err
	}
	return data, skipped, nil
}

// manage runs `kiwix-manage <tmp> add <paths...>` and checks the file has one book per path.
func (l *Library) manage(ctx context.Context, tmp string, paths []string) ([]byte, error) {
	_ = os.Remove(tmp) // kiwix-manage appends to an existing library
	res, err := l.Runner.Run(ctx, execx.Cmd{
		Path:       l.tool(),
		Args:       append([]string{tmp, "add"}, paths...),
		Timeout:    ManageTimeout,
		MaxOut:     manageMaxOut,
		Credential: l.Cred,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManage, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%w: exit %d: %s", ErrManage, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("%w: no output: %v", ErrManage, err)
	}
	n, err := countBooks(data)
	if err != nil {
		return nil, fmt.Errorf("%w: output is not XML: %v", ErrManage, err)
	}
	if n != len(paths) {
		return nil, fmt.Errorf("%w: %d books for %d files", ErrManage, n, len(paths))
	}
	return data, nil
}

// countBooks parses a library and counts its <book> elements.
func countBooks(data []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	n := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "book" {
			n++
		}
	}
}
