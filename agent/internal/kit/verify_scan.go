package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// The scan and integrity passes of the verifier (rebuild-kit.md section 4.4 steps 1 and 2).

// run is the state of one verification.
type run struct {
	fs       VerifyFS
	opts     Options
	rootPath string
	kitDir   string
	rep      VerifyReport

	// partDirs says which top-level parts exist as folders.
	partDirs map[string]bool
	items    []*scanItem
	byDir    map[string]*scanItem
	// recs is every file some manifest records, by its path below kit/ (slash separated): the item's
	// own files, the companions a mirror records for kit/src, the files of a directory item. The
	// completeness rules ask it whether a file is there and verified.
	recs map[string]*fileRec

	hashedFiles int
	hashedBytes int64
	// node remembers the needs already reported for this computer, so the lab pass does not report
	// them twice.
	node map[string]bool
}

// scanItem is one MANIFEST.json found below kit/.
type scanItem struct {
	// dir is the item directory below kit/, slash separated; part its top-level part.
	dir, part string
	id        string
	// valid: the manifest was read and sits where its item belongs.
	valid bool
	// tree: a directory item (its files are the lines of SHA256SUMS).
	tree  bool
	m     manifest.Manifest
	files []*fileRec
	// total is the size the manifest records; lastRe its last re-verification by sync.
	total  int64
	lastRe time.Time
	// status is ok or damaged once the integrity pass has run.
	status string
}

// fileRec is a file some manifest records.
type fileRec struct {
	rel, part, item string
	sha             string
	// size is the recorded size, -1 when the manifest does not record one.
	size   int64
	state  string // "" not checked, "ok", "bad"
	reason string
}

const (
	recOK  = "ok"
	recBad = "bad"
)

// kitScanLevels is how deep below kit/ an item folder may be (debs/<codename>-<arch>/ is two).
const kitScanLevels = 3

// maxFindingNames bounds the file names one finding carries.
const maxFindingNames = 20

// expectedID is the item id a well-known kit folder holds ("" when the folder has no fixed id).
func expectedID(dir string) string {
	p := strings.Split(dir, "/")
	switch {
	case len(p) == 1 && p[0] == PartCatalog:
		return "kit-catalog"
	case len(p) == 1 && p[0] == PartBin:
		return "kit-agent-binaries"
	case len(p) == 1 && p[0] == PartSrc:
		return "kit-agent-source"
	case len(p) == 1 && p[0] == PartToolchains:
		return "kit-go-toolchain"
	case len(p) == 1 && p[0] == PartDesktop:
		return "kit-desktop"
	case len(p) == 2 && p[0] == PartTools:
		return "kit-" + p[1]
	case len(p) == 2 && p[0] == PartDebs:
		return "kit-debs-" + p[1]
	case len(p) == 2 && p[0] == PartNvidia:
		return "kit-nvidia-" + p[1]
	case len(p) == 2 && p[0] == PartCuda:
		return "kit-cuda-redist-" + p[1]
	}
	return ""
}

func partOf2(dir string) string {
	first, _, _ := strings.Cut(dir, "/")
	return first
}

// scan finds every MANIFEST.json below kit/ (the same folders as ark.ScanRoot: kit/config and hidden
// folders are not looked into) and reads it. It hashes nothing.
func (r *run) scan(ctx context.Context) error {
	r.progress(StageScan, "", "")
	if ents, err := r.fs.ReadDir(r.kitDir); err == nil {
		for _, e := range ents {
			if e.IsDir() && ValidPart(e.Name()) {
				r.partDirs[e.Name()] = true
			}
		}
	}
	var walk func(parent string, level int) error
	walk = func(parent string, level int) error {
		ents, err := r.fs.ReadDir(r.abs(parent))
		if err != nil {
			return nil
		}
		for _, e := range ents {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || parent == "" && name == ConfigDirName {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			dir := rel(parent, name)
			r.tryItem(dir)
			if level < kitScanLevels {
				if err := walk(dir, level+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("", 1); err != nil {
		return err
	}
	sort.SliceStable(r.items, func(i, j int) bool { return r.items[i].dir < r.items[j].dir })
	return nil
}

// tryItem reads the manifest of one folder, when it has one.
func (r *run) tryItem(dir string) {
	mp := filepath.Join(r.abs(dir), manifest.FileName)
	if _, err := r.fs.Lstat(mp); err != nil && errors.Is(err, fs.ErrNotExist) {
		return
	}
	it := &scanItem{dir: dir, part: partOf2(dir), id: expectedID(dir), status: StatusOK}
	r.items = append(r.items, it)
	r.byDir[dir] = it
	var reason string
	if dir == PartPrebuilt {
		reason = r.readPrebuilt(it, mp)
	} else {
		reason = r.readItem(it, mp)
	}
	if reason == "" {
		it.valid = true
		return
	}
	it.status = StatusDamaged
	if !r.inScope(it.part) {
		return
	}
	res := ItemResult{ID: it.id, Dir: dir, Part: it.part, Status: StatusDamaged,
		Problems: []Problem{{Part: it.part, Path: rel(dir, manifest.FileName), Reason: reason}}}
	r.rep.Items = append(r.rep.Items, res)
	r.add(Finding{Code: CodeKitDamagedFile, Kind: KindBadManifest, Part: it.part, Item: it.id,
		Names: []string{rel(dir, manifest.FileName)}, Detail: reason})
}

// readItem parses a strict item manifest into it and checks that it is where its item belongs. It
// returns "" when all is well, else the reason.
func (r *run) readItem(it *scanItem, mp string) string {
	m, err := manifest.Read(r.fs, mp)
	if err != nil {
		return ReasonBadManifest
	}
	if m.ItemKind != manifest.KindKit {
		return ReasonMisplaced
	}
	if want := expectedID(it.dir); want != "" && want != m.ItemID {
		return ReasonMisplaced
	}
	it.m, it.id, it.total, it.lastRe, it.tree = m, m.ItemID, m.TotalBytes, m.LastReverifiedAt, m.IsDir()
	for _, f := range m.Files {
		it.files = append(it.files, &fileRec{rel: rel(it.dir, f.Path), part: it.part, item: it.dir, sha: strings.ToLower(f.SHA256), size: f.SizeBytes})
	}
	for _, c := range companionFiles(m) {
		it.files = append(it.files, &fileRec{rel: rel(c.dir, c.name), part: c.dir, item: it.dir, sha: c.sha, size: c.size})
	}
	return ""
}

// companion is a file an item records that lives outside its own folder.
type companion struct {
	dir, name, sha string
	size           int64
}

// listedFile is one entry of a companion list or of the prebuilt manifest, read leniently.
type listedFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	SizeBytes *int64 `json:"size_bytes"`
	Size      *int64 `json:"size"`
}

func (f listedFile) fileName() string {
	switch {
	case f.Path != "":
		return f.Path
	case f.Name != "":
		return f.Name
	}
	return f.File
}

func (f listedFile) bytes() int64 {
	switch {
	case f.SizeBytes != nil:
		return *f.SizeBytes
	case f.Size != nil:
		return *f.Size
	}
	return -1
}

// companionFiles are the files an item records outside its folder (rebuild-kit.md section 3.2.1): the
// source tarballs of a git mirror (source.tarballs, into src/) and the installer of the agent
// binaries (source.installers, into installers/). An entry with a name that is not one plain file
// name or a digest that is not 64 hex digits is left out.
func companionFiles(m manifest.Manifest) []companion {
	var out []companion
	for _, c := range []struct{ key, dir string }{{"tarballs", PartSrc}, {"installers", PartInstallers}} {
		raw, ok := m.Source.Fields[c.key]
		if !ok {
			continue
		}
		var list []listedFile
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		for _, e := range list {
			name, sum := e.fileName(), strings.ToLower(e.SHA256)
			if name == "" || path.Base(name) != name || !manifest.IsSHA256(sum) {
				continue
			}
			out = append(out, companion{dir: c.dir, name: name, sha: sum, size: e.bytes()})
		}
	}
	return out
}

// readPrebuilt reads kit/prebuilt/MANIFEST.json, which the nodes write when they export a build
// (rebuild-kit.md section 4.2): a list of files with their sha256, read leniently because the strict
// reader of the sync items does not apply to it.
func (r *run) readPrebuilt(it *scanItem, mp string) string {
	data, err := r.fs.ReadFile(mp)
	if err != nil {
		return ReasonUnreadable
	}
	var v struct {
		Files            []listedFile `json:"files"`
		LastReverifiedAt time.Time      `json:"last_reverified_at"`
		VerifiedAt       time.Time      `json:"verified_at"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ReasonBadManifest
	}
	it.id = "kit-prebuilt"
	it.lastRe = v.LastReverifiedAt
	if it.lastRe.IsZero() {
		it.lastRe = v.VerifiedAt
	}
	for _, f := range v.Files {
		name, sum := f.fileName(), strings.ToLower(f.SHA256)
		if name == "" || path.Base(name) != name || !manifest.IsSHA256(sum) {
			return ReasonBadManifest
		}
		it.files = append(it.files, &fileRec{rel: rel(it.dir, name), part: it.part, item: it.dir, sha: sum, size: f.bytes()})
		if s := f.bytes(); s > 0 {
			it.total += s
		}
	}
	return ""
}

// due says whether an item is re-hashed in this run.
func (r *run) due(last time.Time) bool {
	switch r.opts.Mode {
	case ModeFull:
		return true
	case ModeQuick:
		return false
	}
	if last.IsZero() {
		return true
	}
	return r.opts.Now.Sub(last) >= time.Duration(r.opts.ReverifyDays)*24*time.Hour
}

// ---- step 1: integrity --------------------------------------------------------------------------

// integrity checks every item in scope: its files are there with the recorded size and, when the
// item is due, hash to the recorded sha256 when read again from the disk.
func (r *run) integrity(ctx context.Context) error {
	for _, it := range r.items {
		if !it.valid {
			continue
		}
		ownScope := r.inScope(it.part)
		hash := r.due(it.lastRe)
		res := ItemResult{ID: it.id, Dir: it.dir, Part: it.part, Status: StatusOK, Bytes: it.total}
		if !it.lastRe.IsZero() {
			t := it.lastRe.UTC()
			res.LastReverifiedAt = &t
		}
		// Register what the item records, whether or not it is checked now: the completeness rules
		// look files up by name.
		for _, f := range it.files {
			r.recs[f.rel] = f
		}
		if ownScope {
			if err := r.checkOwn(ctx, it, &res, hash); err != nil {
				return err
			}
		}
		// A companion file lives in a part of its own (a source tarball of a mirror, in src/): it is
		// checked when that part is in scope, whoever records it.
		for _, f := range it.files {
			if f.part == it.part || !r.inScope(f.part) {
				continue
			}
			if err := r.checkFile(ctx, &res, it, f, hash); err != nil {
				return err
			}
		}
		if !ownScope && !r.anyScoped(it) {
			continue
		}
		res.Files = len(it.files)
		if it.tree {
			res.Files = it.m.FileCount
		}
		if len(res.Problems) > 0 {
			res.Status, it.status = StatusDamaged, StatusDamaged
			r.reportDamage(it, res.Problems)
		}
		r.rep.Items = append(r.rep.Items, res)
	}
	return nil
}

// anyScoped: the item has a companion file in the part the run is limited to.
func (r *run) anyScoped(it *scanItem) bool {
	for _, f := range it.files {
		if f.part != it.part && r.inScope(f.part) {
			return true
		}
	}
	return false
}

// checkOwn checks the files of the item's own folder.
func (r *run) checkOwn(ctx context.Context, it *scanItem, res *ItemResult, hash bool) error {
	switch {
	case it.part == PartDebs || it.part == PartNvidia:
		return r.checkApt(ctx, it, res, hash)
	case it.part == PartGit:
		return r.checkMirror(ctx, it, res, hash)
	case it.tree:
		return r.checkTree(ctx, it, res, hash)
	}
	for _, f := range it.files {
		if f.part != it.part {
			continue
		}
		if err := r.checkFile(ctx, res, it, f, hash); err != nil {
			return err
		}
	}
	return nil
}

// checkFile checks one recorded file: it is a regular file with the recorded size, and, when hash is
// set, its content hashes to the recorded sha256. A failure is recorded on the file and the item.
func (r *run) checkFile(ctx context.Context, res *ItemResult, it *scanItem, f *fileRec, hash bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := r.abs(f.rel)
	fi, err := r.fs.Lstat(p)
	switch {
	case err != nil:
		r.bad(res, f, reasonFor(err))
		return nil
	case !fi.Mode().IsRegular():
		r.bad(res, f, ReasonNotRegular)
		return nil
	case f.size >= 0 && fi.Size() != f.size:
		if fi.Size() < f.size {
			r.bad(res, f, ReasonTruncated)
		} else {
			r.bad(res, f, ReasonWrongSize)
		}
		return nil
	}
	if hash {
		r.progress(StageHash, it.dir, f.rel)
		sum, n, err := r.hash(ctx, p)
		switch {
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			r.bad(res, f, reasonFor(err))
			return nil
		}
		res.Hashed++
		r.hashedFiles++
		r.hashedBytes += n
		if sum != f.sha {
			r.bad(res, f, ReasonChanged)
			return nil
		}
	}
	f.state = recOK
	return nil
}

func (r *run) bad(res *ItemResult, f *fileRec, reason string) {
	f.state, f.reason = recBad, reason
	res.Problems = append(res.Problems, Problem{Part: f.part, Path: f.rel, Reason: reason})
}

// hash reads the whole file from the disk: the kernel's cached pages of it are dropped first (a
// re-read of the cache would prove nothing about the disk) and again afterwards (so checking gigabytes
// does not push a loaded model out of memory).
func (r *run) hash(ctx context.Context, p string) (string, int64, error) {
	dropCache(r.fs, p)
	defer dropCache(r.fs, p)
	rc, err := openForHash(r.fs, p)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	return hashReader(ctx, rc)
}

// hashReader returns the sha256 and the length of what rd yields, stopping at the next block when ctx
// ends.
func hashReader(ctx context.Context, rd io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(h, ctxReader{ctx, rd}, make([]byte, 1<<20))
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// checkTree checks a directory item that is not an apt repository: SHA256SUMS is there and is the one
// the manifest's tree hash and file count describe, and every file it lists is a regular file (and
// hashes, when the item is due).
func (r *run) checkTree(ctx context.Context, it *scanItem, res *ItemResult, hash bool) error {
	dir := r.abs(it.dir)
	sums, err := manifest.ReadSums(r.fs, dir)
	if err != nil {
		reason := ReasonNoSums
		if !errors.Is(err, fs.ErrNotExist) {
			reason = ReasonBadManifest
		}
		res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, manifest.SumsName), Reason: reason})
		return nil
	}
	if tree, terr := manifest.TreeSHA256(sums); terr != nil || tree != it.m.TreeSHA256 || len(sums) != it.m.FileCount {
		res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, manifest.SumsName), Reason: ReasonSumsMismatch})
		return nil
	}
	for _, s := range sums {
		f := &fileRec{rel: rel(it.dir, s.Path), part: it.part, item: it.dir, sha: s.SHA256, size: -1}
		r.recs[f.rel] = f
		if err := r.checkFile(ctx, res, it, f, hash); err != nil {
			return err
		}
	}
	return nil
}

// checkApt checks an apt repository (kit/debs, kit/nvidia). When the item is due, VerifyRepo does it
// (every file against SHA256SUMS and PACKAGES.lock, the Packages index against the lock, no stray file
// apt could prefer); otherwise SHA256SUMS against the manifest and the presence of every listed file.
func (r *run) checkApt(ctx context.Context, it *scanItem, res *ItemResult, hash bool) error {
	if !hash {
		return r.checkAptCheap(ctx, it, res)
	}
	repo := Repo{Part: it.part, Name: path.Base(it.dir), Dir: r.abs(it.dir)}
	_, err := VerifyRepo(ctx, r.fs, repo, func(file string) { r.progress(StageHash, it.dir, rel(it.dir, file)) })
	res.Hashed = it.m.FileCount
	r.hashedFiles += it.m.FileCount
	r.hashedBytes += it.m.TotalBytes
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var e *Error
	if errors.As(err, &e) {
		for _, n := range e.Names {
			res.Problems = append(res.Problems, aptProblem(it, n))
		}
		if len(res.Problems) > 0 {
			return nil
		}
	}
	res.Problems = append(res.Problems, Problem{Part: it.part, Path: it.dir, Reason: ReasonUnreadable})
	return nil
}

// aptProblem turns a name VerifyRepo reports ("pool/x.deb (changed)") into a Problem.
func aptProblem(it *scanItem, name string) Problem {
	reason := ReasonChanged
	if i := strings.LastIndex(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		reason = name[i+2 : len(name)-1]
		name = name[:i]
	}
	return Problem{Part: it.part, Path: rel(it.dir, name), Reason: reason}
}

func (r *run) checkAptCheap(ctx context.Context, it *scanItem, res *ItemResult) error {
	sums, err := manifest.ReadSums(r.fs, r.abs(it.dir))
	if err != nil {
		reason := ReasonNoSums
		if !errors.Is(err, fs.ErrNotExist) {
			reason = ReasonBadManifest
		}
		res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, manifest.SumsName), Reason: reason})
		return nil
	}
	if tree, terr := manifest.TreeSHA256(sums); terr != nil || it.tree && tree != it.m.TreeSHA256 {
		res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, manifest.SumsName), Reason: ReasonSumsMismatch})
		return nil
	}
	for _, s := range sums {
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := r.fs.Lstat(r.abs(rel(it.dir, s.Path)))
		switch {
		case err != nil:
			res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, s.Path), Reason: reasonFor(err)})
		case !fi.Mode().IsRegular():
			res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, s.Path), Reason: ReasonNotRegular})
		}
	}
	return nil
}

// gitFsckTimeout bounds one `git fsck` of a mirror.
const gitFsckTimeout = 30 * time.Minute

// checkMirror checks a git mirror: the folder looks like a bare repository (HEAD and objects/) and, in
// ModeFull, `git fsck --connectivity-only` passes. Its file list, when it has one, is checked like any
// other; its tree hash, when it has one, is not (ark-sync.md section 4.9.6: the mirror is not hashed).
func (r *run) checkMirror(ctx context.Context, it *scanItem, res *ItemResult, hash bool) error {
	for _, name := range []string{"HEAD", "objects"} {
		fi, err := r.fs.Lstat(r.abs(rel(it.dir, name)))
		switch {
		case err != nil:
			res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, name), Reason: reasonFor(err)})
		case name == "HEAD" && !fi.Mode().IsRegular(), name == "objects" && !fi.IsDir():
			res.Problems = append(res.Problems, Problem{Part: it.part, Path: rel(it.dir, name), Reason: ReasonNotRegular})
		}
	}
	for _, f := range it.files {
		if f.part != it.part {
			continue
		}
		if err := r.checkFile(ctx, res, it, f, hash); err != nil {
			return err
		}
	}
	if len(res.Problems) > 0 || r.opts.Mode != ModeFull {
		return nil
	}
	if r.opts.Runner == nil || r.opts.LookPath == nil {
		r.note(Note{Kind: NoteNoGit, Part: it.part, Names: []string{it.dir}, Detail: "git was not given to the verifier"})
		return nil
	}
	bin, err := r.opts.LookPath("git")
	if err != nil {
		r.note(Note{Kind: NoteNoGit, Part: it.part, Names: []string{it.dir}, Detail: "git is not installed"})
		return nil
	}
	r.progress(StageHash, it.dir, "git fsck")
	dir := r.abs(it.dir)
	out, err := r.opts.Runner.Run(ctx, execx.Cmd{
		Path: bin, Timeout: gitFsckTimeout, MaxOut: 64 << 10,
		Args: []string{"-c", "safe.directory=" + dir, "-c", "core.hooksPath=/dev/null", "-C", dir, "fsck", "--connectivity-only", "--no-dangling"},
		Env: []string{"PATH=" + execx.FixedPath, "HOME=/nonexistent", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"},
	})
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		r.note(Note{Kind: NoteNoGit, Part: it.part, Names: []string{it.dir}, Detail: err.Error()})
	case out.ExitCode != 0:
		res.Problems = append(res.Problems, Problem{Part: it.part, Path: it.dir, Reason: ReasonFsck})
	}
	return nil
}

// reportDamage turns the problems of an item into findings: one LAI-KIT-001 per part the damage is in.
func (r *run) reportDamage(it *scanItem, problems []Problem) {
	byPart := map[string][]Problem{}
	var order []string
	for _, p := range problems {
		if _, ok := byPart[p.Part]; !ok {
			order = append(order, p.Part)
		}
		byPart[p.Part] = append(byPart[p.Part], p)
	}
	for _, part := range order {
		list := byPart[part]
		names := make([]string, 0, len(list))
		for _, p := range list {
			names = append(names, p.Path+" ("+p.Reason+")")
		}
		if len(names) > maxFindingNames {
			more := len(names) - maxFindingNames
			names = append(names[:maxFindingNames], fmt.Sprintf("and %d more", more))
		}
		r.add(Finding{Code: CodeKitDamagedFile, Kind: KindDamaged, Part: part, Item: it.id, Names: names})
	}
}

// ---- step 2: signatures ---------------------------------------------------------------------------

// signature checks kit/bin/SHA256SUMS.minisig against the embedded keys and every file of SHA256SUMS
// against the binaries (the same update.VerifyDir as `aictl kit install agent` and `aictl
// verify-release`). A bad signature, an unknown key, a missing signature file or a binary that does not
// match is LAI-KIT-044. A program with no release key built in cannot say either way: the report says
// so and the part is a warning.
func (r *run) signature(ctx context.Context) error {
	if !r.inScope(PartBin) {
		return nil
	}
	r.progress(StageSignature, PartBin, "")
	binDir := r.abs(PartBin)
	if fi, err := r.fs.Stat(binDir); err != nil || !fi.IsDir() {
		r.rep.Signature = &SignatureResult{Status: SigAbsent}
		return nil
	}
	rep, err := update.VerifyDir(r.fs, binDir, r.opts.Keys, update.DirOptions{IgnoreMissing: true})
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	sig := &SignatureResult{KeyID: rep.KeyID, Files: rep.Checked()}
	r.rep.Signature = sig
	if err == nil {
		sig.Status = SigOK
		return nil
	}
	var mm *update.MismatchError
	switch {
	case errors.Is(err, update.ErrNoKeys):
		sig.Status, sig.Detail = SigUnchecked, "this program has no release key built in"
		r.note(Note{Kind: NoteUnchecked, Part: PartBin, Names: []string{rel(PartBin, "SHA256SUMS.minisig")}, Detail: sig.Detail})
		return nil
	case errors.As(err, &mm):
		sig.Status, sig.Bad = SigFailed, mm.Files
		sig.Detail = "files that do not match the signed checksums"
	case errors.Is(err, update.ErrNoSums):
		sig.Status, sig.Detail = SigFailed, "SHA256SUMS or SHA256SUMS.minisig is missing"
	default:
		sig.Status, sig.Detail = SigFailed, "the signature of SHA256SUMS did not verify"
	}
	names := append([]string(nil), sig.Bad...)
	for i := range names {
		names[i] = rel(PartBin, names[i])
	}
	r.add(Finding{Code: CodeKitBadSignature, Kind: KindBadSignature, Part: PartBin, Item: "kit-agent-binaries", Names: names, Detail: sig.Detail})
	return nil
}

// signatureFailed reports whether the check of the agent binaries failed.
func (r *run) signatureFailed() bool {
	return r.rep.Signature != nil && r.rep.Signature.Status == SigFailed
}

// ---- the index ------------------------------------------------------------------------------------

// index compares kit/MANIFEST.json (the index sync writes, rebuild-kit.md section 3.6) with the items
// the scan found. A different list is only a note (LAI-KIT-046): the kit itself is fine and the next
// sync rewrites the index.
func (r *run) index() {
	if r.opts.Part != "" {
		return
	}
	var have []string
	for _, it := range r.items {
		if it.valid && it.dir != PartPrebuilt {
			have = append(have, it.id+"@"+it.dir)
		}
	}
	data, err := r.fs.ReadFile(r.abs(IndexFileName))
	if err != nil {
		if len(have) > 0 {
			r.note(Note{Kind: NoteIndexStale, Code: CodeKitStaleIndex, Names: []string{IndexFileName}, Detail: "missing"})
		}
		return
	}
	var ix struct {
		Schema string `json:"schema"`
		Items  []struct {
			ID  string `json:"id"`
			Dir string `json:"dir"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &ix) != nil || ix.Schema != IndexSchema {
		r.note(Note{Kind: NoteIndexStale, Code: CodeKitStaleIndex, Names: []string{IndexFileName}, Detail: "unreadable"})
		return
	}
	var listed []string
	for _, e := range ix.Items {
		listed = append(listed, e.ID+"@"+e.Dir)
	}
	sort.Strings(have)
	sort.Strings(listed)
	if strings.Join(have, "\n") != strings.Join(listed, "\n") {
		r.note(Note{Kind: NoteIndexStale, Code: CodeKitStaleIndex, Names: []string{IndexFileName}, Detail: "out of date"})
	}
}

// ---- step 6: buildability ---------------------------------------------------------------------------

// buildability runs the caller's dry run of rebuild-llama's preflight (the build engine imports this
// package, so it is handed in) and turns each failed check into a finding with its own code.
func (r *run) buildability(ctx context.Context) error {
	if r.opts.Build == nil || r.opts.Part != "" {
		return nil
	}
	r.progress(StageBuild, "", "")
	res := r.opts.Build(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	r.rep.Build = &res
	for _, c := range res.Checks {
		if c.OK || c.Code == "" {
			continue
		}
		r.add(Finding{Code: c.Code, Kind: KindBuildCheck, Need: c.ID, Detail: c.Title + ": " + c.Detail, Colour: Red})
	}
	return nil
}

