package item

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Products: items made of several parts (ark-sync.md section 4.9.9).
//
// A resolver that is not a plain download (git, docs, apt) produces its item in one call
// (Resolution.Produce). The machine installs what it made with the one swap it already has:
//
//   - the primary content is the item directory: a tree staged under tree/ and swapped in (a docs
//     tree, a kit catalog, an apt closure, the first clone of a git mirror), or, for a git mirror that
//     is already there, the directory itself, updated in place (Produced.InPlace);
//   - companions are files that belong to the item but live outside its directory (the source tarballs
//     of a mirror, which sit in kit/src/ beside another item's directory). They are verified with the
//     item, installed by the same journaled plan, and recorded in the item's own manifest under
//     source.<key>, because a manifest lists only files inside its own directory.
//
// Because the plan is one plan and MANIFEST.json is its last rename, a mirror and its tarballs are
// verified and swapped as a unit: until the manifest is committed the item counts as not present, and a
// crash anywhere is finished (or restored) by the same replay as any other swap.
//
// A product that has two items (the docs tree and the kit catalog) is two jobs, one per item, each with
// its own journal record, verification, swap and manifest; the engine picks the part of each
// (internal/sync/run, ProductResolver).

// ProducedName is the file in the staging directory that keeps what Produce answered. It is written
// (atomically) before the tree is put at tree/, and read back when a run finds the item staged.
const ProducedName = "PRODUCED.json"

// NoticesKey is the manifest.source field that carries the notices of a produced item.
const NoticesKey = "notices"

// ProduceEnv is what the machine hands Resolution.Produce.
type ProduceEnv struct {
	// StagingDir is <root>/.staging/<item>/<version>: Produce writes only below it (an in-place update
	// of the item directory is the one exception).
	StagingDir string
	// Checkpoints persists the resume points of the downloads Produce makes (the journal's).
	Checkpoints download.Checkpointer
}

// CompanionFile is one file of a Companion, as the producer made it.
type CompanionFile struct {
	// Path is relative to the companion's directory: a clean slash path.
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size_bytes"`
	// SHA256 is the producer's digest; the machine reads the file back and compares.
	SHA256       string `json:"sha256"`
	SHA256Source string `json:"sha256_source,omitempty"`
}

// Companion is a set of files installed beside the item (outside its directory).
type Companion struct {
	// Key is the manifest.source key the files are recorded under ("tarballs").
	Key string `json:"key"`
	// From is the staged directory below the staging directory ("src"); To the directory below the
	// root they are installed into ("kit/src").
	From  string          `json:"from"`
	To    string          `json:"to"`
	Files []CompanionFile `json:"files"`
}

// Produced is what Resolution.Produce made.
type Produced struct {
	// SortKey replaces Resolution.SortKey when set (the producer's own, after refinement).
	SortKey string `json:"sort_key,omitempty"`
	// Trust replaces Resolution.Trust when set.
	Trust manifest.Trust `json:"trust,omitempty"`
	// Source holds extra manifest.source fields (the url, the commit, the codename...). Never a secret.
	Source map[string]any `json:"source,omitempty"`
	// TreeFrom is the staged directory (below the staging directory) that holds the tree when it is not
	// already at tree/; the machine renames it to tree/. Empty or "tree" for a tree already there.
	TreeFrom string `json:"tree_from,omitempty"`
	// TreeSHA256 is the tree hash the producer computed over what it sealed; the verification compares
	// it with what it reads back from disk. Empty when the tree is not hashed whole (Record).
	TreeSHA256 string `json:"tree_sha256,omitempty"`
	// InPlace: the item directory was updated where it lives (a git mirror fetch). No tree is staged;
	// the commit is the new manifest.
	InPlace bool `json:"in_place,omitempty"`
	// Record lists the files SHA256SUMS records instead of every file of the directory (a git
	// mirror records its HEAD only: git changes the rest of the repository itself, so it is never
	// hashed whole).
	Record []string `json:"record,omitempty"`
	// Companions are the files installed beside the item.
	Companions []Companion `json:"companions,omitempty"`
	// Notices are the doctor codes the item succeeded with (LAI-SYN-062, LAI-CLI-014). They are
	// recorded in the manifest as source.notices.
	Notices []string `json:"notices,omitempty"`
	// Keep: the present version is already the target, nothing was staged and nothing is swapped.
	Keep bool `json:"keep,omitempty"`
}

func (p Produced) treeAt() string {
	if p.TreeFrom == "" || p.TreeFrom == TreeDir {
		return ""
	}
	return p.TreeFrom
}

// reservedSourceKeys cannot be the key of a companion or an extra field: they are the manifest's own.
var reservedSourceKeys = map[string]bool{"type": true, NoticesKey: true}

// validateProduced refuses an answer that would write outside the item or the staging directory.
// relDir is the item directory below the root (slash separated).
func validateProduced(p Produced, relDir string) error {
	if p.InPlace && (p.TreeFrom != "" || p.Keep) {
		return errors.New("item: an in-place product has no staged tree")
	}
	if p.TreeFrom != "" && !cleanRel(p.TreeFrom) {
		return fmt.Errorf("item: the producer named the unusable tree directory %q", p.TreeFrom)
	}
	for _, f := range p.Record {
		if !manifest.ValidFilePath(f) {
			return fmt.Errorf("item: the producer recorded the unusable file path %q", f)
		}
	}
	if p.TreeSHA256 != "" && !manifest.IsSHA256(p.TreeSHA256) {
		return errors.New("item: the producer's tree hash is not 64 lowercase hex digits")
	}
	if p.Trust != "" && !p.Trust.Valid() {
		return fmt.Errorf("item: the producer's trust %q is not valid", p.Trust)
	}
	for k := range p.Source {
		if reservedSourceKeys[k] || !manifest.SafeName(k) {
			return fmt.Errorf("item: the producer's source field %q is not allowed", k)
		}
	}
	seenKey := map[string]bool{}
	for _, c := range p.Companions {
		switch {
		case !manifest.SafeName(c.Key) || reservedSourceKeys[c.Key] || seenKey[c.Key]:
			return fmt.Errorf("item: the companion key %q is not usable", c.Key)
		case !cleanRel(c.From) || !cleanRel(c.To):
			return fmt.Errorf("item: companion %s has an unusable directory", c.Key)
		case overlaps(c.To, relDir) || strings.HasPrefix(c.To, ".staging") || strings.HasPrefix(c.To, ".trash"):
			return fmt.Errorf("item: companion %s would be installed over the item or its bookkeeping", c.Key)
		case c.From == TreeDir || strings.HasPrefix(c.From, TreeDir+"/"):
			return fmt.Errorf("item: companion %s is staged inside the tree", c.Key)
		case len(c.Files) == 0:
			return fmt.Errorf("item: companion %s lists no files", c.Key)
		}
		seenKey[c.Key] = true
		seen := map[string]bool{}
		for _, f := range c.Files {
			switch {
			case !manifest.ValidFilePath(f.Path) || seen[f.Path]:
				return fmt.Errorf("item: companion %s lists the unusable file path %q", c.Key, f.Path)
			case !manifest.IsSHA256(f.SHA256) || f.Size < 0 || f.Kind == "":
				return fmt.Errorf("item: companion %s: file %q has no usable digest, size or kind", c.Key, f.Path)
			}
			seen[f.Path] = true
		}
	}
	return nil
}

func cleanRel(p string) bool {
	return p != "" && p != "." && fs.ValidPath(p) && path.Clean(p) == p && !strings.ContainsRune(p, '\\')
}

// overlaps reports whether either slash path is the other or inside it.
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (r *runner) producedPath() string { return filepath.Join(r.stagingDir(), ProducedName) }

func (r *runner) loadProduced() (Produced, bool) {
	data, err := r.m.d.FS.ReadFile(r.producedPath())
	if err != nil {
		return Produced{}, false
	}
	var p Produced
	if json.Unmarshal(data, &p) != nil || validateProduced(p, r.job.RelDir) != nil {
		return Produced{}, false
	}
	return p, true
}

func (r *runner) saveProduced(p Produced) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	err = config.WriteFileAtomicFS(r.m.d.FS, r.producedPath(), append(data, '\n'), 0o644, -1, -1)
	if isNotDurable(err) {
		return nil
	}
	return err
}

// stagedPieces reports whether everything a produced item needs is in staging: the tree (or the tree
// directory still to be renamed to tree/), and every companion file.
func (r *runner) stagedPieces(p Produced) bool {
	fsys := r.m.d.FS
	s := r.stagingDir()
	if !p.Keep && !p.InPlace {
		if !exists(fsys, filepath.Join(s, TreeDir)) && !(p.treeAt() != "" && exists(fsys, filepath.Join(s, filepath.FromSlash(p.treeAt())))) {
			return false
		}
	}
	for _, c := range p.Companions {
		for _, f := range c.Files {
			info, err := fsys.Stat(filepath.Join(s, filepath.FromSlash(c.From), filepath.FromSlash(f.Path)))
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
		}
	}
	return true
}

// producedReady loads what an earlier attempt produced and reports whether it can be used as it is:
// Produce then does not run again.
func (r *runner) producedReady() bool {
	p, ok := r.loadProduced()
	if !ok || !r.stagedPieces(p) {
		return false
	}
	if err := r.placeTree(p); err != nil {
		return false
	}
	r.produced = &p
	r.keep = p.Keep
	return true
}

// placeTree puts the produced tree at tree/. It is idempotent: a tree already there is left, and a
// leftover tree/ of an earlier attempt that is in the way is moved aside, never deleted.
func (r *runner) placeTree(p Produced) error {
	from := p.treeAt()
	if from == "" || p.InPlace || p.Keep {
		return nil
	}
	fsys := r.m.d.FS
	s := r.stagingDir()
	src, dst := filepath.Join(s, filepath.FromSlash(from)), filepath.Join(s, TreeDir)
	if !exists(fsys, src) {
		return nil // already renamed
	}
	if exists(fsys, dst) {
		stamp := r.m.now().UTC().Format(quarantineStamp)
		aside := dst + ".unusable-" + stamp
		for n := 1; exists(fsys, aside); n++ {
			aside = fmt.Sprintf("%s.unusable-%s-%d", dst, stamp, n)
		}
		if err := fsys.Rename(dst, aside); err != nil {
			return err
		}
	}
	if err := fsys.Rename(src, dst); err != nil {
		return err
	}
	return fsys.SyncDir(s)
}

// produce is the Produce half of the downloading state. It returns false when r.res is final.
func (r *runner) produce() bool {
	if r.producedReady() {
		r.log(slog.LevelInfo, "content produced by an earlier attempt is used", "in_place", r.produced.InPlace)
		return true
	}
	fsys := r.m.d.FS
	s := r.stagingDir()
	if err := fsys.MkdirAll(s, 0o755); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if g := r.job.Gate; g != nil && r.rsl.Exclusive != "" {
		release, err := g.Exclusive(r.ctx, r.rsl.Exclusive)
		if err != nil {
			r.cancelled(journal.StateDownloading)
			return false
		}
		defer release()
	}
	p, err := r.rsl.Produce(r.ctx, ProduceEnv{StagingDir: s, Checkpoints: r.cp})
	if err != nil {
		return r.produceError(err)
	}
	if err := validateProduced(p, r.job.RelDir); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, false)
		return false
	}
	// Write-ahead for the rename that follows: what was made is on disk before the tree moves.
	if err := r.saveProduced(p); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if err := r.placeTree(p); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	r.produced, r.keep = &p, p.Keep
	return true
}

// produceError maps what Produce returned to the item's outcome. It always returns false.
func (r *runner) produceError(err error) bool {
	if r.ctx.Err() != nil && isCtxErr(err) {
		r.cancelled(journal.StateDownloading)
		return false
	}
	var de *download.Error
	if errors.As(err, &de) {
		switch de.Kind {
		case download.KindPermanent:
			_ = r.finish(journal.StateFailed, de.Code, "permanent", err, false)
			r.cleanStaging()
		case download.KindDisk:
			_ = r.finish(journal.StateSkipped, de.Code, ReasonDisk, err, true)
		case download.KindRootFault:
			r.res.RootFault = de
			_ = r.finish(journal.StateFailed, de.Code, "root_fault", err, true)
		case download.KindMismatch:
			// What came over the wire is not what the index promised: the bytes stay in staging
			// (retention owns them) and the next run starts again.
			_ = r.finish(journal.StateFailed, de.Code, "mismatch", err, true)
		default:
			_ = r.finish(journal.StateFailed, de.Code, "exhausted", err, true)
		}
		return false
	}
	var re *ResolveError
	code, permanent := CodeUnresolved, false
	if errors.As(err, &re) {
		if re.Code != "" {
			code = re.Code
		}
		permanent = re.Permanent
	}
	_ = r.finish(journal.StateFailed, code, "build", err, !permanent)
	return false
}

// finishKeep ends an item whose producer said the present version is the target: nothing is swapped.
func (r *runner) finishKeep() Result {
	d, err := r.job.Root.Join(filepath.FromSlash(r.job.RelDir))
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, false)
		return r.res
	}
	cur, err := manifest.Load(r.m.d.FS, d)
	if err != nil || cur.Version != r.version {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", fmt.Errorf("item: the producer kept a version that is not on disk: %v", err), true)
		return r.res
	}
	r.cleanStaging()
	if err := r.transition(journal.StateVerified, r.version, func(i *journal.Item) {
		i.Attempts, i.LastError, i.Code, i.PartialBytes, i.Checkpoints, i.ConsecutiveFailedRuns = 0, "", "", 0, nil, 0
		i.Extras = clearQuarantine(*i)
	}); err != nil {
		r.res.State, r.res.Code, r.res.Reason, r.res.Err, r.res.Retryable = journal.StateFailed, CodeInternal, "internal", err, true
		return r.res
	}
	r.res.State, r.res.Code, r.res.Reason, r.res.Err, r.res.Retryable, r.res.Bytes = journal.StateVerified, "", "", nil, false, cur.TotalBytes
	return r.res
}

// ---- verifying -------------------------------------------------------------------------------------

// hashRecord hashes the files a produced directory records (a mirror's HEAD), from disk.
func (r *runner) hashRecord(dir string) ([]TreeFile, error) {
	fsys := r.m.d.FS
	var out []TreeFile
	paths := append([]string(nil), r.produced.Record...)
	sort.Strings(paths)
	for _, rel := range paths {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := fsys.Lstat(full)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s", ErrNotRegular, rel)
		}
		if err := syncFile(fsys, full); err != nil {
			return nil, fmt.Errorf("fsync %s: %w", rel, err)
		}
		_ = fsys.DropCache(full)
		sum, size, err := hashFile(r.ctx, fsys, full, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, TreeFile{Path: rel, SHA256: sum, Size: size})
	}
	return out, nil
}

// setAsideTemps moves the temporary file of an interrupted SHA256SUMS write (the debris of a process
// killed while it sealed the tree) out of the tree, into the staging directory: the tree is swapped into
// the item directory as a whole, and a stray file must not go with it. Never deleted: staging goes
// away as a whole after the swap.
func (r *runner) setAsideTemps(tree string) {
	fsys := r.m.d.FS
	ents, err := fsys.ReadDir(tree)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), manifest.SumsName+".tmp-") {
			continue
		}
		if err := fsys.Rename(filepath.Join(tree, e.Name()), filepath.Join(r.stagingDir(), e.Name()+".unusable")); err != nil {
			r.log(slog.LevelWarn, "a leftover temporary file could not be moved out of the tree", "err", err.Error())
		}
	}
}

// recorded reports whether the produced tree is hashed only in part (Produced.Record).
func (r *runner) recorded() bool { return r.produced != nil && len(r.produced.Record) > 0 }

// inPlace reports whether the item directory was updated where it lives.
func (r *runner) inPlace() bool { return r.produced != nil && r.produced.InPlace }

// sumsDir is the directory whose SHA256SUMS the manifest's tree hash is computed from: the staged tree,
// or the staging directory itself for an in-place item (its sums are swapped into the item directory).
func (r *runner) sumsDir() string {
	if r.inPlace() {
		return r.stagingDir()
	}
	return filepath.Join(r.stagingDir(), TreeDir)
}

// verifyInPlace is verifying for an item updated where it lives: the recorded files are hashed from
// disk and SHA256SUMS is written into staging (it moves in with the manifest), then the companions are
// checked. It returns false when r.res is final.
func (r *runner) verifyInPlace() bool {
	fsys := r.m.d.FS
	d, err := r.job.Root.Join(filepath.FromSlash(r.job.RelDir))
	if err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, false)
		return false
	}
	if info, err := fsys.Lstat(d); err != nil || !info.IsDir() {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", fmt.Errorf("item: the directory updated in place is not there: %v", err), true)
		return false
	}
	if len(r.produced.Record) == 0 {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", errors.New("item: an in-place item records no file"), false)
		return false
	}
	files, err := r.hashRecord(d)
	if err != nil {
		if errors.Is(err, ErrNotRegular) || isNotExist(err) {
			_ = r.finish(journal.StateFailed, download.CodeChecksum, "verify", fmt.Errorf("the directory updated in place lacks a file it must hold: %w", err), true)
			return false
		}
		return r.verifyIOError(err)
	}
	sums := make([]manifest.Sum, len(files))
	for i, tf := range files {
		sums[i] = manifest.Sum{SHA256: tf.SHA256, Path: tf.Path}
	}
	if err := manifest.WriteSums(fsys, r.stagingDir(), sums); err != nil && !isNotDurable(err) {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	if !r.verifyCompanions() {
		return false
	}
	if err := r.transition(journal.StateStaged, r.version, nil); err != nil {
		_ = r.finish(journal.StateFailed, CodeInternal, "internal", err, true)
		return false
	}
	return true
}

// verifyCompanions reads every companion file back from disk and compares it with what the producer
// said; a mismatch quarantines the whole set (the next run rebuilds the files). It returns false when
// r.res is final.
func (r *runner) verifyCompanions() bool {
	if r.produced == nil {
		return true
	}
	fsys := r.m.d.FS
	s := r.stagingDir()
	for _, c := range r.produced.Companions {
		for _, f := range c.Files {
			src := filepath.Join(s, filepath.FromSlash(c.From), filepath.FromSlash(f.Path))
			sum, size, err := verifyFile(r.ctx, fsys, src, r.m.reread(), nil)
			if err != nil {
				return r.verifyIOError(err)
			}
			if (sum != "" && sum != f.SHA256) || (f.Size > 0 && size != f.Size) {
				rel := c.Key + "/" + f.Path
				entry := ReasonFileEntry{Path: rel, ExpectedSHA256: f.SHA256, ActualSHA256: sum, ExpectedBytes: f.Size, Bytes: size}
				var files []qFile
				for _, c2 := range r.produced.Companions {
					for _, f2 := range c2.Files {
						files = append(files, qFile{Src: filepath.Join(s, filepath.FromSlash(c2.From), filepath.FromSlash(f2.Path)), Rel: c2.Key + "/" + f2.Path})
					}
				}
				r.quarantineFiles(download.CodeChecksum, "mismatch", fmt.Errorf("the companion file %s read back from disk does not match", rel), []ReasonFileEntry{entry}, files, "", "")
				return false
			}
			if err := fsys.Chmod(src, 0o644); err != nil {
				r.log(slog.LevelWarn, "chmod failed", "file", f.Path, "err", err.Error())
			}
		}
		_ = fsys.SyncDir(filepath.Join(s, filepath.FromSlash(c.From)))
	}
	return true
}

// ---- the manifest ----------------------------------------------------------------------------------

// applyProduced adds what the producer reported to the manifest: the sort key, the trust, the extra
// source fields, the companions (source.<key>, each {kind, path, size_bytes, sha256, sha256_source}) and
// the notices.
func (r *runner) applyProduced(m *manifest.Manifest) error {
	p := r.produced
	if p == nil {
		return nil
	}
	if p.SortKey != "" {
		m.SortKey = p.SortKey
	}
	if p.Trust != "" {
		m.Trust = p.Trust
	}
	var err error
	keys := make([]string, 0, len(p.Source))
	for k := range p.Source {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m.Source, err = m.Source.With(k, p.Source[k]); err != nil {
			return err
		}
	}
	for _, c := range p.Companions {
		list := make([]manifest.File, 0, len(c.Files))
		for _, f := range c.Files {
			src := f.SHA256Source
			if src == "" {
				src = "computed"
			}
			list = append(list, manifest.File{Kind: f.Kind, Path: f.Path, SizeBytes: f.Size, SHA256: f.SHA256, SHA256Source: src})
			m.TotalBytes += f.Size
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
		if m.Source, err = m.Source.With(c.Key, list); err != nil {
			return err
		}
	}
	if len(p.Notices) > 0 {
		notices := append([]string(nil), p.Notices...)
		sort.Strings(notices)
		if m.Source, err = m.Source.With(NoticesKey, notices); err != nil {
			return err
		}
	}
	return nil
}

// dirBytes is the size of the regular files below dir, whatever else is in it (a repository may hold
// anything git put there); an unreadable entry counts as zero.
func dirBytes(fsys FS, dir string) int64 {
	var n int64
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		info, err := fsys.Lstat(p)
		switch {
		case err != nil:
		case info.IsDir():
			n += dirBytes(fsys, p)
		case info.Mode().IsRegular() && e.Name() != manifest.SumsName:
			n += info.Size()
		}
	}
	return n
}

// ---- planning the companions -----------------------------------------------------------------------

// companionMove is one companion file to install.
type companionMove struct {
	src, dst string
	// rel is where the file it replaces goes in the trash.
	rel string
	sha string
}

// companionMoves lists the moves of the produced companions into the root.
func (r *runner) companionMoves() []companionMove {
	if r.produced == nil {
		return nil
	}
	var out []companionMove
	for _, c := range r.produced.Companions {
		for _, f := range c.Files {
			out = append(out, companionMove{
				src: filepath.Join(r.stagingDir(), filepath.FromSlash(c.From), filepath.FromSlash(f.Path)),
				dst: filepath.Join(r.job.Root.Path, filepath.FromSlash(c.To), filepath.FromSlash(f.Path)),
				rel: path.Join("companions", c.Key, f.Path),
				sha: f.SHA256,
			})
		}
	}
	return out
}

// companionDests lists the destinations of the plan that are outside the item directory and are not
// its manifest: the companions, for the check after the swap.
func companionDests(p swapPaths) []string {
	var out []string
	n := len(p.Plan.Renames)
	for i, pair := range p.Plan.Renames {
		dst := pair[1]
		if i == n-1 || filepath.Base(pair[0]) == TreeDir || dst == p.D || strings.HasPrefix(dst, p.D+string(filepath.Separator)) {
			continue
		}
		out = append(out, dst)
	}
	return out
}
