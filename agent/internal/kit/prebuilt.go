package kit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Prebuilt export and selection (rebuild-kit.md section 4.2, KIT-05). After a build is activated, the
// node that built it packs its programs into <root>/kit/prebuilt/ on every writable root, so that the
// fastest recovery (same Ubuntu release, same driver) is a copy and a smoke test instead of a compile
// (`aictl rebuild-llama --prebuilt <file>`, `aictl rebuild-whisper --prebuilt <file>`).
//
// This file is the kit side: the file naming, MANIFEST.json of kit/prebuilt/, the exporter that the
// build manager calls after a switch (build.PrebuiltExporter, satisfied structurally: package build
// imports kit, never the other way round), the pruning to 2 per node and backend, and the rule that
// says whether a file fits this computer. The restore itself (verification of BUILD.json and of
// every program, placement, smoke test, activation) is internal/router/build.
//
// kit/prebuilt/ is not a sync item: it is written by the computers themselves, so it has no item
// manifest, the sync engine never touches it (rebuild-kit.md section 3.2.1) and neither does
// retention.CanDelete (R9: not an item). Its MANIFEST.json is therefore this file's own schema, and
// the pruning here is its own: it keeps the newest two, and never deletes a file unless two others
// that exist remain.

// Names of the prebuilt part of the kit.
const (
	// PrebuiltDirName is kit/prebuilt.
	PrebuiltDirName = "prebuilt"
	// PrebuiltStagingName is the scratch folder inside it: a file is written there and renamed.
	PrebuiltStagingName = ".staging"
	// PrebuiltSchema and PrebuiltSchemaVersion identify kit/prebuilt/MANIFEST.json. The schema is not
	// the item manifest of ark sync, and not the index of kit.IsIndex.
	PrebuiltSchema        = "localai.prebuilt-manifest"
	PrebuiltSchemaVersion = 1
	// MaxPrebuiltPerBackend is how many exports of one program for one node and backend are kept.
	MaxPrebuiltPerBackend = 2

	prebuiltManifestLimit = 4 << 20
)

// Programs that can be exported (the Name of their build recipe).
const (
	ProgramLlama   = SourceStemLlama
	ProgramWhisper = "whisper.cpp"
)

var (
	prebuiltNameRe = regexp.MustCompile(`^(llama\.cpp|whisper\.cpp)-([0-9a-f]{9})-(cuda|vulkan|cpu)-([a-z]+)-(amd64|arm64)(?:-sm([0-9]+(?:_[0-9]+)*))?\.tar\.gz$`)
	fullCommitRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	cudaArchListRe = regexp.MustCompile(`^[0-9]{2,3}(;[0-9]{2,3})*$`)
)

// Errors of the prebuilt part. Test with errors.Is; the typed ones with errors.As.
var (
	// ErrPrebuiltUnrecorded: a file of kit/prebuilt is not recorded in its MANIFEST.json (or the
	// manifest is missing or damaged), so there is no digest to trust it by (LAI-KIT-001).
	ErrPrebuiltUnrecorded = errors.New("kit: the prebuilt file is not recorded in the kit's prebuilt manifest")
	// ErrPrebuiltMismatch: the build is for another Ubuntu release, architecture, backend or CUDA
	// architecture than this computer, or is of another commit than asked for (LAI-KIT-048).
	ErrPrebuiltMismatch = errors.New("kit: the ready-made build does not fit this computer")
	// ErrNoPrebuilt: no root has a ready-made build of the program that fits (LAI-KIT-048 when some
	// other build exists, none at all otherwise).
	ErrNoPrebuilt = errors.New("kit: there is no ready-made build for this computer")
	// ErrNoWritableRoot: the build could not be written to any root (LAI-KIT-049).
	ErrNoWritableRoot = errors.New("kit: no root could take the exported build")
	// ErrExportRefused: the build to export is not what its BUILD.json says, so it would not restore
	// (LAI-KIT-049).
	ErrExportRefused = errors.New("kit: the build does not match its BUILD.json, so it was not exported")
)

// PrebuiltName is the file name of an export (rebuild-kit.md section 3.1):
// <program>-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz. The suffix is only for CUDA, with
// the compute capability without the dot ("-sm75"; several are joined with "_": "-sm75_86").
func PrebuiltName(program, commit, backend, codename, arch, cudaArch string) string {
	short := commit
	if len(short) > 9 {
		short = short[:9]
	}
	name := program + "-" + short + "-" + backend + "-" + codename + "-" + arch
	if backend == "cuda" && cudaArch != "" {
		name += "-sm" + strings.ReplaceAll(cudaArch, ";", "_")
	}
	return name + ".tar.gz"
}

// ParsePrebuiltName reads the fields back out of a file name. The commit is the 9 characters of the
// name, CudaArch the semicolon list ("75;86"); ok is false for a name that does not follow the layout.
func ParsePrebuiltName(name string) (rec PrebuiltRecord, ok bool) {
	m := prebuiltNameRe.FindStringSubmatch(name)
	if m == nil {
		return PrebuiltRecord{}, false
	}
	rec = PrebuiltRecord{File: name, Program: m[1], Commit: m[2], Backend: m[3], Codename: m[4], Arch: m[5]}
	if m[6] != "" {
		rec.CudaArch = strings.ReplaceAll(m[6], "_", ";")
	}
	return rec, true
}

// PrebuiltDir is <root>/kit/prebuilt.
func PrebuiltDir(root string) string { return filepath.Join(root, DirName, PrebuiltDirName) }

// PrebuiltRecord is one export, as kit/prebuilt/MANIFEST.json records it (rebuild-kit.md section 4.2
// step 2): the digest and size of the archive, how it was built, what `--version` said, the CUDA and
// driver versions, the kernel, the node and the time it was built.
type PrebuiltRecord struct {
	// File is the file name in kit/prebuilt.
	File string `json:"file"`
	// Program is "llama.cpp" or "whisper.cpp"; Commit the full 40-hex commit (9 characters when read
	// from a name alone).
	Program string `json:"program"`
	Commit  string `json:"commit"`
	Backend string `json:"backend"`
	// Codename and Arch are this node's Ubuntu release and CPU architecture in kit naming.
	Codename string `json:"codename"`
	Arch     string `json:"arch"`
	// CudaArch is the compute capability list ("75" or "75;86") of a CUDA build.
	CudaArch string `json:"cuda_arch,omitempty"`

	SHA256        string   `json:"sha256"`
	Size          int64    `json:"size_bytes"`
	CMakeFlags    []string `json:"cmake_flags"`
	VersionOutput string   `json:"version_output"`
	CudaVersion   string   `json:"cuda_version,omitempty"`
	DriverVersion string   `json:"driver_version,omitempty"`
	Kernel        string   `json:"kernel"`
	NodeID        string   `json:"node_id"`
	BuiltAt       string   `json:"built_at"`
}

// PrebuiltManifest is kit/prebuilt/MANIFEST.json.
type PrebuiltManifest struct {
	Schema        string           `json:"schema"`
	SchemaVersion int              `json:"schema_version"`
	Files         []PrebuiltRecord `json:"files"`
}

// ReadPrebuiltManifest reads <dir>/MANIFEST.json. A missing file is (empty manifest, nil); one that is
// damaged, of another schema or newer is an error wrapping ErrPrebuiltUnrecorded.
func ReadPrebuiltManifest(fsys FS, dir string) (PrebuiltManifest, error) {
	empty := PrebuiltManifest{Schema: PrebuiltSchema, SchemaVersion: PrebuiltSchemaVersion}
	p := filepath.Join(dir, manifest.FileName)
	fi, err := fsys.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > prebuiltManifestLimit {
		return empty, fmt.Errorf("%w: %s cannot be read", ErrPrebuiltUnrecorded, p)
	}
	data, err := fsys.ReadFile(p)
	if err != nil {
		return empty, fmt.Errorf("%w: %s: %v", ErrPrebuiltUnrecorded, p, err)
	}
	var m PrebuiltManifest
	if err := json.Unmarshal(data, &m); err != nil || m.Schema != PrebuiltSchema || m.SchemaVersion != PrebuiltSchemaVersion {
		return empty, fmt.Errorf("%w: %s is not a prebuilt manifest this program understands", ErrPrebuiltUnrecorded, p)
	}
	return m, nil
}

// writePrebuiltManifest writes the manifest atomically (temp file, fsync, rename), mode 0644.
func writePrebuiltManifest(fsys FS, dir string, m PrebuiltManifest) error {
	m.Schema, m.SchemaVersion = PrebuiltSchema, PrebuiltSchemaVersion
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].File < m.Files[j].File })
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, manifest.FileName), data, 0o644, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return err
	}
	return nil
}

func (m PrebuiltManifest) find(file string) (PrebuiltRecord, bool) {
	for _, r := range m.Files {
		if r.File == file {
			return r, true
		}
	}
	return PrebuiltRecord{}, false
}

// ---- what a computer needs ---------------------------------------------------------------------

// PrebuiltWant is what a build must be to fit this computer.
type PrebuiltWant struct {
	// Program is "llama.cpp" or "whisper.cpp".
	Program string
	// Commit is the commit asked for (a prefix of 7 to 40 characters, or a full one); empty accepts
	// any commit.
	Commit string
	// Backend, Codename and Arch are this computer's; CudaArch the compute capability list of its GPU
	// (only compared for cuda).
	Backend, Codename, Arch, CudaArch string
}

// PrebuiltDiff is one way a build differs from what is wanted.
type PrebuiltDiff struct {
	// Field is "ubuntu", "arch", "backend", "cuda_arch" or "commit".
	Field string
	// Has is what the build is for, Wants what this computer needs.
	Has, Wants string
}

// PrebuiltMismatchError says why a ready-made build does not fit and what the kit has instead.
type PrebuiltMismatchError struct {
	// File is the file that was asked for ("" when the kit was searched).
	File string
	Want PrebuiltWant
	// Diffs are the differences of File from Want (empty when the kit was searched).
	Diffs []PrebuiltDiff
	// Have are the builds of the program the kit does have, newest first (what to tell the operator).
	Have []PrebuiltRecord
}

// Error implements error.
func (e *PrebuiltMismatchError) Error() string {
	var parts []string
	for _, d := range e.Diffs {
		parts = append(parts, fmt.Sprintf("%s: the build is for %s, this computer needs %s", d.Field, d.Has, d.Wants))
	}
	s := ErrPrebuiltMismatch.Error()
	if e.File != "" {
		s += ": " + e.File
	}
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, "; ")
	}
	if len(e.Have) > 0 {
		s += "; the kit has: " + strings.Join(PrebuiltFiles(e.Have), ", ")
	}
	return s
}

// Unwrap lets errors.Is match ErrPrebuiltMismatch (and ErrNoPrebuilt when the kit was searched).
func (e *PrebuiltMismatchError) Unwrap() error {
	if e.File == "" {
		return ErrNoPrebuilt
	}
	return ErrPrebuiltMismatch
}

// PrebuiltFiles lists the file names of records.
func PrebuiltFiles(recs []PrebuiltRecord) []string {
	names := make([]string, len(recs))
	for i, r := range recs {
		names[i] = r.File
	}
	return names
}

// DiffPrebuilt compares a build with what is wanted: the Ubuntu release, the CPU architecture and the
// backend always, the CUDA architecture for cuda when the computer's is known, and the commit when one
// was asked for. A nil result is a fit. Nothing is ever approximated: a build for noble does not fit
// resolute.
func DiffPrebuilt(rec PrebuiltRecord, want PrebuiltWant) []PrebuiltDiff {
	var diffs []PrebuiltDiff
	add := func(field, has, wants string) {
		diffs = append(diffs, PrebuiltDiff{Field: field, Has: has, Wants: wants})
	}
	if rec.Codename != want.Codename {
		add("ubuntu", rec.Codename, want.Codename)
	}
	if rec.Arch != want.Arch {
		add("arch", rec.Arch, want.Arch)
	}
	if rec.Backend != want.Backend {
		add("backend", rec.Backend, want.Backend)
	}
	if rec.Backend == "cuda" && want.Backend == "cuda" && want.CudaArch != "" && rec.CudaArch != want.CudaArch {
		add("cuda_arch", rec.CudaArch, want.CudaArch)
	}
	if c := strings.ToLower(strings.TrimSpace(want.Commit)); c != "" && !commitMatches(rec.Commit, c) {
		add("commit", shortOf(rec.Commit), shortOf(c))
	}
	return diffsOrNil(diffs)
}

func diffsOrNil(d []PrebuiltDiff) []PrebuiltDiff {
	if len(d) == 0 {
		return nil
	}
	return d
}

func shortOf(c string) string {
	if len(c) > 9 {
		return c[:9]
	}
	return c
}

// commitMatches says whether the recorded commit (full, or 9 characters from a name) and the wanted
// one (full or a prefix) name the same commit.
func commitMatches(have, want string) bool {
	if have == "" || want == "" {
		return false
	}
	return strings.HasPrefix(have, want) || strings.HasPrefix(want, have)
}

// ---- finding a file ----------------------------------------------------------------------------

// PrebuiltFile is a recorded export on a root.
type PrebuiltFile struct {
	// Root is the content root, Path the absolute path of the archive.
	Root, Path string
	Record     PrebuiltRecord
}

// ListPrebuilt returns every recorded export of the program on the roots whose file exists with the
// recorded size, newest first (by built_at, then name, then root order). A root whose manifest is
// unreadable contributes nothing: nothing is offered that cannot be verified. program "" lists all.
func ListPrebuilt(fsys FS, roots []string, program string) []PrebuiltFile {
	var out []PrebuiltFile
	for _, root := range roots {
		dir := PrebuiltDir(root)
		m, err := ReadPrebuiltManifest(fsys, dir)
		if err != nil {
			continue
		}
		for _, r := range m.Files {
			if program != "" && r.Program != program {
				continue
			}
			p := filepath.Join(dir, r.File)
			if fi, err := fsys.Lstat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() != r.Size {
				continue
			}
			out = append(out, PrebuiltFile{Root: root, Path: p, Record: r})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Record, out[j].Record
		if a.BuiltAt != b.BuiltAt {
			return a.BuiltAt > b.BuiltAt
		}
		return a.File < b.File
	})
	return out
}

// SelectPrebuilt picks the export that fits this computer (rebuild-kit.md section 4.2: offered first
// when the commit, backend, Ubuntu release and CPU architecture match, and the compute capability for
// CUDA). When several fit, the newest built wins. When none does the error is a
// *PrebuiltMismatchError (errors.Is ErrNoPrebuilt) naming the builds the kit does have.
func SelectPrebuilt(fsys FS, roots []string, want PrebuiltWant) (PrebuiltFile, error) {
	all := ListPrebuilt(fsys, roots, want.Program)
	for _, f := range all {
		if DiffPrebuilt(f.Record, want) == nil {
			return f, nil
		}
	}
	return PrebuiltFile{}, &PrebuiltMismatchError{Want: want, Have: distinctRecords(all)}
}

// distinctRecords lists the records of files once each: the same export on two roots is one build.
func distinctRecords(all []PrebuiltFile) []PrebuiltRecord {
	var out []PrebuiltRecord
	seen := map[string]bool{}
	for _, f := range all {
		key := f.Record.File + "\x00" + f.Record.SHA256
		if !seen[key] {
			seen[key] = true
			out = append(out, f.Record)
		}
	}
	return out
}

// LoadPrebuilt reads the record of an export a caller names by path (`--prebuilt <file>`). A file in a
// kit/prebuilt folder must be recorded in the MANIFEST.json beside it (a missing record, a missing or
// damaged manifest, or a size that differs is ErrPrebuiltUnrecorded: there is no digest to trust it
// by). A file anywhere else, a copy made by hand, is read from its name alone and reported with
// recorded false: the caller warns that only its own BUILD.json vouches for it.
func LoadPrebuilt(fsys FS, file string) (rec PrebuiltRecord, recorded bool, err error) {
	dir, name := filepath.Dir(file), filepath.Base(file)
	fi, serr := fsys.Stat(file)
	if serr != nil || !fi.Mode().IsRegular() {
		return PrebuiltRecord{}, false, fmt.Errorf("%w: %s is not a readable file", ErrPrebuiltUnrecorded, file)
	}
	inKit := filepath.Base(dir) == PrebuiltDirName
	if _, merr := fsys.Lstat(filepath.Join(dir, manifest.FileName)); merr != nil && !inKit {
		parsed, ok := ParsePrebuiltName(name)
		if !ok {
			return PrebuiltRecord{}, false, fmt.Errorf("%w: %q does not follow the naming of the kit's ready-made builds", ErrPrebuiltUnrecorded, name)
		}
		parsed.Size = fi.Size()
		return parsed, false, nil
	}
	m, merr := ReadPrebuiltManifest(fsys, dir)
	if merr != nil {
		return PrebuiltRecord{}, false, merr
	}
	r, ok := m.find(name)
	if !ok {
		return PrebuiltRecord{}, false, fmt.Errorf("%w: %s has no record in %s", ErrPrebuiltUnrecorded, name, filepath.Join(dir, manifest.FileName))
	}
	if r.Size != fi.Size() {
		return PrebuiltRecord{}, false, fmt.Errorf("%w: %s is %d bytes, the manifest says %d", ErrPrebuiltUnrecorded, name, fi.Size(), r.Size)
	}
	if !manifest.IsSHA256(strings.ToLower(r.SHA256)) {
		return PrebuiltRecord{}, false, fmt.Errorf("%w: the record of %s has no valid sha256", ErrPrebuiltUnrecorded, name)
	}
	r.SHA256 = strings.ToLower(r.SHA256)
	return r, true, nil
}

// CheckPrebuilt refuses a build that does not fit: the error is a *PrebuiltMismatchError that lists
// what differs and, from roots, the builds of the program the kit has (so the operator is told what
// would match, never left with "wrong build").
func CheckPrebuilt(fsys FS, roots []string, rec PrebuiltRecord, want PrebuiltWant) error {
	var diffs []PrebuiltDiff
	if rec.Program != want.Program {
		diffs = []PrebuiltDiff{{Field: "program", Has: rec.Program, Wants: want.Program}}
	} else if diffs = DiffPrebuilt(rec, want); diffs == nil {
		return nil
	}
	all := ListPrebuilt(fsys, roots, want.Program)
	return &PrebuiltMismatchError{File: rec.File, Want: want, Diffs: diffs, Have: distinctRecords(all)}
}

// ---- the exporter ------------------------------------------------------------------------------

// buildDoc is the part of a build's BUILD.json the exporter reads.
type buildDoc struct {
	BuildID    string            `json:"build_id"`
	Commit     string            `json:"commit"`
	Backend    string            `json:"backend"`
	CudaArch   string            `json:"cuda_arch"`
	CMakeFlags []string          `json:"cmake_flags"`
	NVCC       string            `json:"nvcc"`
	BuiltAt    string            `json:"built_at"`
	SHA256     map[string]string `json:"sha256"`
	Smoke      struct {
		Version string `json:"version"`
	} `json:"smoke"`
}

// PrebuiltExporter writes an activated build into kit/prebuilt of every writable root. Its method
// ExportBuild is the hook of the build manager (build.PrebuiltExporter); Export returns the details
// `aictl kit export-build` prints.
type PrebuiltExporter struct {
	FS FS
	// Runner and LookPath run nvidia-smi for the driver version of a CUDA build (best effort).
	Runner   execx.Runner
	LookPath func(name string) (string, error)
	// Roots returns the content roots to write, in order (the internal one first).
	Roots func() []string
	// Program is "llama.cpp" or "whisper.cpp".
	Program string
	// Codename and Arch are this computer's Ubuntu release and CPU architecture.
	Codename, Arch string
	// NodeID returns node.yaml's node.id; Kernel `uname -r`. Either may be nil ("" is recorded).
	NodeID func() string
	Kernel func() string
	// Progress, when set, is told each root as it is written.
	Progress func(root string)
}

// PrebuiltRootResult is what happened on one root.
type PrebuiltRootResult struct {
	Root string `json:"root"`
	// Path is the archive on this root when it was written or already there.
	Path string `json:"path,omitempty"`
	// Written is true when the archive and its record were put in place; Unchanged when the same bytes
	// were already there and recorded.
	Written   bool `json:"written"`
	Unchanged bool `json:"unchanged"`
	// ReadOnly is true when the root cannot be written (a drive mounted read-only): expected, skipped.
	ReadOnly bool `json:"read_only,omitempty"`
	// Pruned lists the older exports removed to keep MaxPrebuiltPerBackend.
	Pruned []string `json:"pruned,omitempty"`
	// Error is a failure that is not "read-only".
	Error string `json:"error,omitempty"`
}

// PrebuiltExport is the result of Export.
type PrebuiltExport struct {
	BuildID string `json:"build_id"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size_bytes"`
	// Record is what the manifests now say.
	Record PrebuiltRecord       `json:"record"`
	Roots  []PrebuiltRootResult `json:"roots"`
}

// ExportBuild implements build.PrebuiltExporter: a build that was just activated is exported. It fails
// only when nothing could be written (or a root failed for a reason other than being read-only); the
// build manager shows that as a warning, because the build is already in use.
func (e *PrebuiltExporter) ExportBuild(ctx context.Context, buildID, buildDir string) error {
	res, err := e.Export(ctx, buildDir)
	if err != nil {
		return err
	}
	if res.BuildID != "" && buildID != "" && res.BuildID != buildID {
		return fmt.Errorf("%w: the folder is build %s, not %s", ErrExportRefused, res.BuildID, buildID)
	}
	return nil
}

// Export packs the build in buildDir (a folder with BUILD.json, bin/ and, for a CUDA build with
// redistributable libraries, lib/) and writes it to every root. The archive is made once, with a
// fixed header and no names or times, so the same build always gives the same bytes; it is hashed
// while it is written, read back and renamed into place. Every program in it must match the sha256 its
// BUILD.json records, or nothing is written (a build that would not restore is not exported).
func (e *PrebuiltExporter) Export(ctx context.Context, buildDir string) (*PrebuiltExport, error) {
	raw, err := e.FS.ReadFile(filepath.Join(buildDir, "BUILD.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: BUILD.json cannot be read: %v", ErrExportRefused, err)
	}
	var doc buildDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: BUILD.json is not valid: %v", ErrExportRefused, err)
	}
	if !fullCommitRe.MatchString(doc.Commit) {
		return nil, fmt.Errorf("%w: BUILD.json has no full commit", ErrExportRefused)
	}
	switch doc.Backend {
	case "cuda", "vulkan", "cpu":
	default:
		return nil, fmt.Errorf("%w: BUILD.json names the backend %q", ErrExportRefused, doc.Backend)
	}
	if doc.Backend == "cuda" && !cudaArchListRe.MatchString(doc.CudaArch) {
		return nil, fmt.Errorf("%w: BUILD.json has no CUDA architecture", ErrExportRefused)
	}
	if len(doc.SHA256) == 0 {
		return nil, fmt.Errorf("%w: BUILD.json records no programs", ErrExportRefused)
	}
	if !safeToken(e.Codename) || !safeToken(e.Arch) || (e.Program != ProgramLlama && e.Program != ProgramWhisper) {
		return nil, fmt.Errorf("%w: the computer's Ubuntu release or architecture is unknown", ErrExportRefused)
	}
	name := PrebuiltName(e.Program, doc.Commit, doc.Backend, e.Codename, e.Arch, doc.CudaArch)
	rec := PrebuiltRecord{
		File: name, Program: e.Program, Commit: doc.Commit, Backend: doc.Backend, Codename: e.Codename, Arch: e.Arch,
		CudaArch: doc.CudaArch, CMakeFlags: doc.CMakeFlags, VersionOutput: strings.TrimSpace(doc.Smoke.Version),
		CudaVersion: doc.NVCC, BuiltAt: doc.BuiltAt,
	}
	if rec.CMakeFlags == nil {
		rec.CMakeFlags = []string{}
	}
	if e.NodeID != nil {
		rec.NodeID = e.NodeID()
	}
	if e.Kernel != nil {
		rec.Kernel = e.Kernel()
	}
	if doc.Backend == "cuda" {
		rec.DriverVersion = e.driverVersion(ctx)
	}

	res := &PrebuiltExport{BuildID: doc.BuildID, File: name}
	var primary string // the archive on the first root that took it: the source for the others
	var failures []error
	wrote := 0
	for _, root := range e.Roots() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if e.Progress != nil {
			e.Progress(root)
		}
		rr := PrebuiltRootResult{Root: root}
		dir := PrebuiltDir(root)
		staging := filepath.Join(dir, PrebuiltStagingName)
		if err := e.FS.MkdirAll(staging, 0o755); err != nil {
			if readOnlyErr(err) {
				rr.ReadOnly = true
			} else {
				rr.Error = err.Error()
				failures = append(failures, fmt.Errorf("%s: %w", root, err))
			}
			res.Roots = append(res.Roots, rr)
			continue
		}
		part := filepath.Join(staging, name+".part")
		_ = e.FS.Remove(part)
		var sum string
		var size int64
		var werr error
		if primary == "" {
			sum, size, werr = e.writeArchive(ctx, part, buildDir, raw, doc)
		} else {
			sum, size, werr = e.copyArchive(ctx, part, primary, res.SHA256)
		}
		if werr != nil {
			_ = e.FS.Remove(part)
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if errors.Is(werr, ErrExportRefused) && primary == "" {
				return res, werr // the build itself is wrong: no root can take it
			}
			rr.Error = werr.Error()
			failures = append(failures, fmt.Errorf("%s: %w", root, werr))
			res.Roots = append(res.Roots, rr)
			continue
		}
		rec.SHA256, rec.Size = sum, size
		final := filepath.Join(dir, name)
		rr.Path = final
		mf, merr := ReadPrebuiltManifest(e.FS, dir)
		if merr != nil {
			// A damaged manifest is replaced: what it recorded could not be trusted anyway.
			mf = PrebuiltManifest{Schema: PrebuiltSchema, SchemaVersion: PrebuiltSchemaVersion}
		}
		if old, ok := mf.find(name); ok && old.SHA256 == sum && sameRecord(old, rec) {
			if fi, err := e.FS.Lstat(final); err == nil && fi.Mode().IsRegular() && fi.Size() == size {
				_ = e.FS.Remove(part)
				rr.Unchanged = true
				rr.Pruned, _ = e.prune(dir, mf, rec)
				if primary == "" {
					primary = final
					res.SHA256, res.Size, res.Record = sum, size, rec
				}
				wrote++
				res.Roots = append(res.Roots, rr)
				continue
			}
		}
		if err := e.FS.Rename(part, final); err != nil {
			_ = e.FS.Remove(part)
			rr.Error = err.Error()
			failures = append(failures, fmt.Errorf("%s: %w", root, err))
			res.Roots = append(res.Roots, rr)
			continue
		}
		_ = e.FS.SyncDir(dir)
		mf = upsertRecord(mf, rec)
		pruned, perr := e.prune(dir, mf, rec)
		rr.Pruned = pruned
		if perr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", root, perr))
			rr.Error = perr.Error()
		}
		if primary == "" {
			primary = final
			res.SHA256, res.Size, res.Record = sum, size, rec
		}
		rr.Written = true
		wrote++
		res.Roots = append(res.Roots, rr)
	}
	switch {
	case wrote == 0 && len(failures) == 0:
		return res, fmt.Errorf("%w: every root is read-only or there is none", ErrNoWritableRoot)
	case wrote == 0:
		return res, fmt.Errorf("%w: %w", ErrNoWritableRoot, errors.Join(failures...))
	case len(failures) > 0:
		return res, errors.Join(failures...)
	}
	return res, nil
}

func sameRecord(a, b PrebuiltRecord) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func upsertRecord(m PrebuiltManifest, rec PrebuiltRecord) PrebuiltManifest {
	out := m.Files[:0:0]
	for _, r := range m.Files {
		if r.File != rec.File {
			out = append(out, r)
		}
	}
	m.Files = append(out, rec)
	return m
}

// prune enforces MaxPrebuiltPerBackend for the program, node and backend of keep, then writes the
// manifest. It keeps the newest exports whose files exist (the one just written first of all) and
// deletes an older one only when that many others that exist remain, so the last copy is never
// deleted. A record whose file is gone is dropped with it. It returns the names it removed.
func (e *PrebuiltExporter) prune(dir string, m PrebuiltManifest, keep PrebuiltRecord) ([]string, error) {
	var group []PrebuiltRecord
	var rest []PrebuiltRecord
	for _, r := range m.Files {
		if r.Program == keep.Program && r.NodeID == keep.NodeID && r.Backend == keep.Backend {
			group = append(group, r)
		} else {
			rest = append(rest, r)
		}
	}
	sort.SliceStable(group, func(i, j int) bool {
		if group[i].File == keep.File || group[j].File == keep.File {
			return group[i].File == keep.File
		}
		if group[i].BuiltAt != group[j].BuiltAt {
			return group[i].BuiltAt > group[j].BuiltAt
		}
		return group[i].File < group[j].File
	})
	var kept []PrebuiltRecord
	var drop []PrebuiltRecord
	for _, r := range group {
		fi, err := e.FS.Lstat(filepath.Join(dir, r.File))
		exists := err == nil && fi.Mode().IsRegular()
		switch {
		case !exists:
			// recorded, but the file is gone: forget the record
		case len(kept) < MaxPrebuiltPerBackend:
			kept = append(kept, r)
		default:
			drop = append(drop, r)
		}
	}
	var removed []string
	var firstErr error
	for _, r := range drop {
		if err := e.FS.Remove(filepath.Join(dir, r.File)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if firstErr == nil {
				firstErr = fmt.Errorf("the older export %s could not be removed: %w", r.File, err)
			}
			kept = append(kept, r) // still there: keep it recorded
			continue
		}
		removed = append(removed, r.File)
	}
	m.Files = append(rest, kept...)
	if err := writePrebuiltManifest(e.FS, dir, m); err != nil {
		return removed, fmt.Errorf("the prebuilt manifest could not be written: %w", err)
	}
	sort.Strings(removed)
	return removed, firstErr
}

// writeArchive writes the tar.gz of the build to dst and returns its sha256 and size.
func (e *PrebuiltExporter) writeArchive(ctx context.Context, dst, buildDir string, buildJSON []byte, doc buildDoc) (sum string, size int64, err error) {
	out, err := e.FS.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(out, h)}
	gz, err := gzip.NewWriterLevel(cw, gzip.BestCompression)
	if err != nil {
		return "", 0, err
	}
	gz.Header = gzip.Header{} // gzip -n: no name, no time
	tw := tar.NewWriter(gz)
	hdr := func(name string, mode int64, typ byte, n int64, link string) *tar.Header {
		return &tar.Header{Name: name, Mode: mode, Typeflag: typ, Size: n, Linkname: link, Format: tar.FormatPAX, ModTime: time.Unix(0, 0)}
	}
	if err = tw.WriteHeader(hdr("BUILD.json", 0o644, tar.TypeReg, int64(len(buildJSON)), "")); err != nil {
		return "", 0, err
	}
	if _, err = tw.Write(buildJSON); err != nil {
		return "", 0, err
	}
	if err = tw.WriteHeader(hdr("bin/", 0o755, tar.TypeDir, 0, "")); err != nil {
		return "", 0, err
	}
	var progs []string
	for n := range doc.SHA256 {
		progs = append(progs, n)
	}
	sort.Strings(progs)
	for _, n := range progs {
		if !manifest.SafeName(n) {
			return "", 0, fmt.Errorf("%w: BUILD.json names the program %q", ErrExportRefused, n)
		}
		p := filepath.Join(buildDir, "bin", n)
		fi, lerr := e.FS.Lstat(p)
		if lerr != nil || !fi.Mode().IsRegular() {
			return "", 0, fmt.Errorf("%w: bin/%s is missing or not a file", ErrExportRefused, n)
		}
		f, oerr := e.FS.Open(p)
		if oerr != nil {
			return "", 0, fmt.Errorf("%w: bin/%s cannot be read: %v", ErrExportRefused, n, oerr)
		}
		if err = tw.WriteHeader(hdr("bin/"+n, 0o755, tar.TypeReg, fi.Size(), "")); err != nil {
			_ = f.Close()
			return "", 0, err
		}
		ph := sha256.New()
		copied, cerr := io.Copy(io.MultiWriter(tw, ph), io.LimitReader(ctxReader{ctx, f}, fi.Size()+1))
		_ = f.Close()
		switch {
		case cerr != nil:
			return "", 0, cerr
		case copied != fi.Size():
			return "", 0, fmt.Errorf("%w: bin/%s changed while it was read", ErrExportRefused, n)
		case hex.EncodeToString(ph.Sum(nil)) != strings.ToLower(doc.SHA256[n]):
			return "", 0, fmt.Errorf("%w: bin/%s is not the program BUILD.json describes", ErrExportRefused, n)
		}
	}
	if err = e.writeLib(ctx, tw, buildDir, hdr); err != nil {
		return "", 0, err
	}
	if err = tw.Close(); err != nil {
		return "", 0, err
	}
	if err = gz.Close(); err != nil {
		return "", 0, err
	}
	if sy, ok := out.(interface{ Sync() error }); ok {
		if err = sy.Sync(); err != nil {
			return "", 0, err
		}
	}
	closed = true
	if err = out.Close(); err != nil {
		return "", 0, err
	}
	sum = hex.EncodeToString(h.Sum(nil))
	// Read it back: what is renamed into place is what was hashed.
	back, err := hashFile(ctx, e.FS, dst, 1<<40)
	if err != nil {
		return "", 0, fmt.Errorf("the exported file cannot be read back: %w", err)
	}
	if back != sum {
		return "", 0, errors.New("the exported file does not read back as written")
	}
	return sum, cw.n, nil
}

// writeLib adds lib/ (the CUDA redistributable libraries of option B) when the build has it: regular
// files and links to a file of the same folder, in name order. Anything else is refused.
func (e *PrebuiltExporter) writeLib(ctx context.Context, tw *tar.Writer, buildDir string, hdr func(string, int64, byte, int64, string) *tar.Header) error {
	lib := filepath.Join(buildDir, "lib")
	fi, err := e.FS.Lstat(lib)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: lib is not a folder", ErrExportRefused)
	}
	entries, err := e.FS.ReadDir(lib)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(hdr("lib/", 0o755, tar.TypeDir, 0, "")); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, de := range entries {
		n := de.Name()
		if !manifest.SafeName(n) {
			return fmt.Errorf("%w: lib/%s has an unsafe name", ErrExportRefused, n)
		}
		p := filepath.Join(lib, n)
		st, err := e.FS.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			target, rerr := readlinkFS(e.FS, p)
			if rerr != nil || target == "" || path.Base(target) != target || target == "." || target == ".." {
				return fmt.Errorf("%w: lib/%s is a link to somewhere else", ErrExportRefused, n)
			}
			if err := tw.WriteHeader(hdr("lib/"+n, 0o777, tar.TypeSymlink, 0, target)); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			f, oerr := e.FS.Open(p)
			if oerr != nil {
				return oerr
			}
			if err := tw.WriteHeader(hdr("lib/"+n, int64(st.Mode().Perm()), tar.TypeReg, st.Size(), "")); err != nil {
				_ = f.Close()
				return err
			}
			copied, cerr := io.Copy(tw, io.LimitReader(ctxReader{ctx, f}, st.Size()+1))
			_ = f.Close()
			if cerr != nil {
				return cerr
			}
			if copied != st.Size() {
				return fmt.Errorf("%w: lib/%s changed while it was read", ErrExportRefused, n)
			}
		default:
			return fmt.Errorf("%w: lib/%s is not a file", ErrExportRefused, n)
		}
	}
	return nil
}

// readlinkFS reads a link through the FS when it can (kit.FS has Readlink).
func readlinkFS(fsys FS, p string) (string, error) { return fsys.Readlink(p) }

// copyArchive copies the archive already written on another root into dst and checks its digest.
func (e *PrebuiltExporter) copyArchive(ctx context.Context, dst, src, wantSum string) (string, int64, error) {
	in, err := e.FS.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := e.FS.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), ctxReader{ctx, in})
	if err == nil {
		if sy, ok := out.(interface{ Sync() error }); ok {
			err = sy.Sync()
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != wantSum {
		return "", 0, errors.New("the copy on this root does not match the exported file")
	}
	return sum, n, nil
}

// driverVersion asks nvidia-smi for the driver version ("" when it cannot say).
func (e *PrebuiltExporter) driverVersion(ctx context.Context) string {
	if e.Runner == nil || e.LookPath == nil {
		return ""
	}
	bin, err := e.LookPath("nvidia-smi")
	if err != nil {
		return ""
	}
	res, err := e.Runner.Run(ctx, execx.Cmd{Path: bin, Args: []string{"--query-gpu=driver_version", "--format=csv,noheader"}, Timeout: 30 * time.Second, MaxOut: 16 << 10})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if v := strings.TrimSpace(l); v != "" {
			return clip(v, 32)
		}
	}
	return ""
}

func safeToken(s string) bool { return s != "" && regexp.MustCompile(`^[a-z0-9]+$`).MatchString(s) }

func readOnlyErr(err error) bool {
	return errors.Is(err, syscall.EROFS) || errors.Is(err, fs.ErrPermission)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
