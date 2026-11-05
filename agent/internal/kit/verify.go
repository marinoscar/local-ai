package kit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Kit verification (rebuild-kit.md section 4.4, KIT-03). One pass over <root>/kit/ that
//
//  1. re-hashes the kit files against their MANIFESTs (every file with Full, the items not
//     re-verified within ReverifyDays by default, nothing with ModeQuick),
//  2. checks kit/bin/release/SHA256SUMS.minisig with the embedded key and SHA256SUMS against the binaries,
//  3. checks that everything this node needs is present and verified (completeness.go),
//  4. on a drive, the same for every node of pins.json,
//  5. checks the consistency of the closures with this node (Ubuntu codename, CUDA against the driver),
//  6. when the caller supplies the dry run, that rebuild-llama's preflight passes,
//
// and returns ONE VerifyReport: what `aictl kit verify` prints, what READY.json reads (package kitready)
// and what the drill reads (drl.kit.present, steps 1 to 5).
//
// A verification never writes. Every access goes through ReadOnly, so a damaged file stays where
// it is (sync's quarantine path is the only code that moves one) and the last verified copy of
// anything is never touched. It needs no network, no agent and no root.

// Mode says how much of the kit is re-hashed.
type Mode string

// The modes.
const (
	// ModeQuick hashes nothing: manifests, file presence and sizes, the signature of the agent
	// checksums, completeness and consistency. What READY and the drill use (the drill hashes
	// through its own check).
	ModeQuick Mode = "quick"
	// ModeBudgeted is the default of `aictl kit verify`: it re-hashes the items whose
	// last_reverified_at is older than ReverifyDays (or unknown).
	ModeBudgeted Mode = "budgeted"
	// ModeFull is `--full`: it re-hashes everything now.
	ModeFull Mode = "full"
)

// DefaultReverifyDays is sync.reverify_days (ark-sync.md section 4.13).
const DefaultReverifyDays = 30

// Overall colours of a VerifyReport, and the colour of one finding.
const (
	Green  = "green"
	Yellow = "yellow"
	Red    = "red"
)

// Statuses of a part (and of one requirement).
const (
	StatusOK           = "ok"
	StatusMissing      = "missing"
	StatusDamaged      = "damaged"
	StatusInconsistent = "inconsistent"
	StatusWarning      = "warning"
	// StatusSkipped: a requirement that could not be judged because a fact about this computer is
	// unknown (no llama.commit in node.yaml, no NVIDIA driver answer).
	StatusSkipped = "skipped"
)

// Doctor codes of the report. They alias internal/doctor, the one file that spells a code.
const (
	CodeKitDamagedFile  = doctor.KIT001
	CodeKitMissingNode  = doctor.KIT040
	CodeKitMissingLab   = doctor.KIT041
	CodeKitWrongRelease = doctor.KIT042
	CodeKitCudaTooNew   = doctor.KIT043
	CodeKitBadSignature = doctor.KIT044
	CodeKitStaleIndex   = doctor.KIT046
)

// Reasons a file of the kit failed its check (Problem.Reason).
const (
	ReasonChanged      = "changed"
	ReasonMissing      = "missing"
	ReasonTruncated    = "truncated"
	ReasonWrongSize    = "wrong size"
	ReasonUnreadable   = "unreadable"
	ReasonNotRegular   = "not a regular file"
	ReasonNotRecorded  = "not recorded"
	ReasonBadManifest  = "bad manifest"
	ReasonMisplaced    = "misplaced"
	ReasonFsck         = "git fsck failed"
	ReasonNoSums       = "SHA256SUMS missing"
	ReasonSumsMismatch = "SHA256SUMS does not match the manifest"
)

// Kinds of Finding.
const (
	KindDamaged         = "damaged"
	KindBadManifest     = "bad_manifest"
	KindBadSignature    = "bad_signature"
	KindMissing         = "missing"
	KindWrongRelease    = "wrong_release"
	KindCudaTooNew      = "cuda_too_new"
	KindBuildCheck      = "build_check"
	KindPinsDamaged     = "pins_damaged"
	KindPrebuiltMissing = "prebuilt_missing"
	// KindUnverifiedRepos: an apt item was built from vendor repositories whose signature was not
	// checked (LAI-SYN-063; sync.apt_unverified_repos: allow). Names are the repositories.
	KindUnverifiedRepos = "unverified_repos"
)

// Kinds of Note (information with no doctor code).
const (
	NoteUnchecked  = "unchecked"
	NoteSkipped    = "skipped"
	NoteIndexStale = "index_stale"
	NoteStalePins  = "stale_pins"
	NoteNoPins     = "no_pins"
	NoteNoGit      = "no_git"
)

// ErrNoKit: <root>/kit does not exist (the CLI answers LAI-KIT-040, exit 5).
var ErrNoKit = errors.New("kit: the root has no kit folder")

// ErrUnknownPart: Options.Part is not one of Parts (the CLI answers exit 2).
var ErrUnknownPart = errors.New("kit: unknown kit part")

// Problem is one file or folder of the kit that failed its check.
type Problem struct {
	// Part is the top-level part the file belongs to (a source tarball of a git mirror is in "src").
	Part string `json:"part"`
	// Path is relative to kit/, slash separated.
	Path string `json:"path"`
	// Reason is one of the Reason constants.
	Reason string `json:"reason"`
}

// ItemResult is the verdict on one kit item (one MANIFEST.json).
type ItemResult struct {
	ID   string `json:"id"`
	Dir  string `json:"dir"`
	Part string `json:"part"`
	// Status is ok or damaged.
	Status string `json:"status"`
	// Files is the number of files the manifest lists; Hashed how many of them this run re-read.
	Files  int `json:"files"`
	Hashed int `json:"hashed"`
	// Bytes is the size the manifest records.
	Bytes int64 `json:"bytes"`
	// LastReverifiedAt is the manifest's own record of the last re-hash by sync.
	LastReverifiedAt *time.Time `json:"last_reverified_at,omitempty"`
	Problems         []Problem  `json:"problems,omitempty"`
}

// Finding is one thing wrong that has a doctor code. The CLI words it in English and Spanish from
// Kind and the fields; READY reads Code and Item.
type Finding struct {
	Code string `json:"code"`
	// Severity is the code's doctor severity (warning, error, critical).
	Severity string `json:"severity"`
	// Colour is what the finding makes the report on this root: red or yellow (the colour rules of
	// ark-sync.md section 4.12.2: LAI-KIT-040 is red on the internal root and yellow on a drive).
	Colour string `json:"colour"`
	Kind   string `json:"kind"`
	Part   string `json:"part,omitempty"`
	// Item is the catalog id of the kit item the finding is about (kit-debs-resolute-amd64).
	Item string `json:"item,omitempty"`
	// Nodes names the computers it is about: this one, or the lab nodes of pins.json.
	Nodes []string `json:"nodes,omitempty"`
	// Need and Arg say what is missing or inconsistent (Need* constants and their argument).
	Need string `json:"need,omitempty"`
	Arg  string `json:"arg,omitempty"`
	// Names are the files or folders it is about; Have and Want the two sides of a mismatch.
	Names  []string `json:"names,omitempty"`
	Have   []string `json:"have,omitempty"`
	Want   string   `json:"want,omitempty"`
	Detail string   `json:"detail,omitempty"`
	// Optional marks a finding that is a warning only (a missing prebuilt): READY does not read it.
	Optional bool `json:"optional,omitempty"`
}

// Note is something worth saying that has no doctor code.
type Note struct {
	Kind string `json:"kind"`
	// Code is the doctor code when the note has one (the index of LAI-KIT-046).
	Code   string   `json:"code,omitempty"`
	Part   string   `json:"part,omitempty"`
	Names  []string `json:"names,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// PartResult is one top-level part of kit/ in the report ({part, status, code} of rebuild-kit.md
// section 4.4).
type PartResult struct {
	Part string `json:"part"`
	// Status is ok, missing, damaged, inconsistent or warning.
	Status string `json:"status"`
	// Code is the doctor code of the most serious finding about the part ("" when ok).
	Code string `json:"code,omitempty"`
	// Present is whether kit/<part> exists; Items how many manifests it holds.
	Present bool `json:"present"`
	Items   int  `json:"items"`
	// OldestReverifiedAt is the oldest last_reverified_at among the items of the part, so a status
	// line can say how long ago the part was last re-hashed.
	OldestReverifiedAt *time.Time `json:"oldest_reverified_at,omitempty"`
}

// SignatureResult is step 2: kit/bin/SHA256SUMS.minisig against the embedded key.
type SignatureResult struct {
	// Status is ok, failed, unchecked (this program has no release key built in) or absent (the kit
	// has no bin/ folder).
	Status string `json:"status"`
	KeyID  string `json:"key_id,omitempty"`
	// Files lists the files of SHA256SUMS that matched; Bad those that did not.
	Files  int      `json:"files,omitempty"`
	Bad    []string `json:"bad,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// Signature statuses.
const (
	SigOK        = "ok"
	SigFailed    = "failed"
	SigUnchecked = "unchecked"
	SigAbsent    = "absent"
)

// BuildCheck is one check of rebuild-llama's dry run (router/build.Check, copied so that package kit
// does not import the build engine).
type BuildCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// BuildResult is step 6.
type BuildResult struct {
	// Skipped says why the dry run was not made ("" when it was).
	Skipped string       `json:"skipped,omitempty"`
	OK      bool         `json:"ok"`
	Checks  []BuildCheck `json:"checks,omitempty"`
}

// Totals counts the work of a run.
type Totals struct {
	Items       int   `json:"items"`
	Files       int   `json:"files"`
	FilesHashed int   `json:"files_hashed"`
	BytesHashed int64 `json:"bytes_hashed"`
}

// VerifyReport is the one report of a verification (rebuild-kit.md section 4.4, "Result").
type VerifyReport struct {
	// Overall is green, yellow or red.
	Overall string `json:"overall"`
	// Root is the id of the root ("internal", "ark-3f9c0a12" or a path) and Path its folder.
	Root     string `json:"root"`
	Path     string `json:"path"`
	Internal bool   `json:"internal"`
	Mode     Mode   `json:"mode"`
	// Part is the --part filter ("" for the whole kit).
	Part      string    `json:"part,omitempty"`
	CheckedAt time.Time `json:"checked_at"`

	Parts []PartResult `json:"parts"`
	Items []ItemResult `json:"items"`
	// Node is this computer's completeness (step 3 and 5); nil when its facts are unknown.
	Node *NodeResult `json:"node,omitempty"`
	// Lab is the completeness for every other node of pins.json (step 4, drive roots only).
	Lab       []NodeResult     `json:"lab,omitempty"`
	Signature *SignatureResult `json:"signature,omitempty"`
	Build     *BuildResult     `json:"build,omitempty"`
	Findings  []Finding        `json:"findings"`
	Notes     []Note           `json:"notes,omitempty"`
	Totals    Totals           `json:"totals"`
}

// Damaged reports whether anything failed an integrity check or the signature: what `aictl kit
// verify` answers with exit 1 and LAI-KIT-001 (or LAI-KIT-044).
func (r VerifyReport) Damaged() bool {
	for _, f := range r.Findings {
		if f.Code == CodeKitDamagedFile || f.Code == CodeKitBadSignature {
			return true
		}
	}
	return false
}

// NodeProblems are the findings that say this computer's own parts are missing or inconsistent
// (LAI-KIT-040 and 042, not the optional ones): what the drill's drl.kit.present check fails on.
func (r VerifyReport) NodeProblems() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if (f.Code == CodeKitMissingNode || f.Code == CodeKitWrongRelease) && !f.Optional {
			out = append(out, f)
		}
	}
	return out
}

// LabProblems are the findings about the other computers of the lab (LAI-KIT-041): the warning of
// the drill's drl.kit.present check.
func (r VerifyReport) LabProblems() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Code == CodeKitMissingLab {
			out = append(out, f)
		}
	}
	return out
}

// HasCode reports whether a finding carries code.
func (r VerifyReport) HasCode(code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// PartOf returns the result of one part.
func (r VerifyReport) PartOf(name string) (PartResult, bool) {
	for _, p := range r.Parts {
		if p.Part == name {
			return p, true
		}
	}
	return PartResult{}, false
}

// Progress is told what a run is doing, for the progress line of a command.
type Progress struct {
	// Stage is scan, hash, signature, complete or build.
	Stage string
	// Item is the item directory (below kit/) and File the file being hashed, when there is one.
	Item, File string
}

// Progress stages.
const (
	StageScan      = "scan"
	StageHash      = "hash"
	StageSignature = "signature"
	StageComplete  = "complete"
	StageBuild     = "build"
)

// Options are the inputs of a verification.
type Options struct {
	Mode Mode
	// Part limits the run to one top-level part of rebuild-kit.md section 3.1 ("" is all).
	Part string
	// Now is the clock (the caller's; tests never read the real one). It decides which items are due
	// for a re-hash and which pins are stale.
	Now time.Time
	// ReverifyDays is sync.reverify_days; DefaultReverifyDays when 0.
	ReverifyDays int
	// RootID names the root in the report; Internal says it is the internal root (not a drive).
	RootID   string
	Internal bool
	// Node holds the facts about this computer; nil when unknown (the completeness for this node is
	// then skipped and the report says so).
	Node *NodeFacts
	// Keys are the trusted minisign keys (update.EmbeddedKeys()).
	Keys []update.PublicKey
	// Runner and LookPath run `git fsck` over the mirrors in ModeFull; without them the mirrors get
	// the structural check only and a note says so.
	Runner   execx.Runner
	LookPath func(name string) (string, error)
	// Build is step 6: rebuild-llama's dry run. nil skips it. It runs only for a whole-kit
	// verification (no Part).
	Build func(ctx context.Context) BuildResult
	// Progress, when set, is told each stage.
	Progress func(Progress)
}

// Verifier verifies kits through FS. Every access is read-only.
type Verifier struct {
	FS VerifyFS
}

// Verify verifies the kit of the content root rootPath (the folder that holds kit/). The error is
// ErrNoKit when kit/ does not exist, ErrUnknownPart for a Part that is not one of Parts, or the
// context's; every other problem is in the VerifyReport.
func (v Verifier) Verify(ctx context.Context, rootPath string, opts Options) (VerifyReport, error) {
	if opts.Part != "" && !ValidPart(opts.Part) {
		return VerifyReport{}, fmt.Errorf("%w: %q", ErrUnknownPart, opts.Part)
	}
	if opts.Mode == "" {
		opts.Mode = ModeBudgeted
	}
	if opts.ReverifyDays <= 0 {
		opts.ReverifyDays = DefaultReverifyDays
	}
	ro := ReadOnly(v.FS)
	kitDir := filepath.Join(rootPath, DirName)
	fi, err := ro.Stat(kitDir)
	if err != nil || !fi.IsDir() {
		return VerifyReport{}, fmt.Errorf("%w: %s", ErrNoKit, kitDir)
	}
	r := &run{
		fs: ro, opts: opts, rootPath: rootPath, kitDir: kitDir,
		recs: map[string]*fileRec{}, byDir: map[string]*scanItem{}, partDirs: map[string]bool{},
		node: map[string]bool{},
	}
	r.rep = VerifyReport{
		Root: opts.RootID, Path: rootPath, Internal: opts.Internal, Mode: opts.Mode, Part: opts.Part,
		CheckedAt: opts.Now.UTC().Truncate(time.Second), Findings: []Finding{},
	}
	if err := r.scan(ctx); err != nil {
		return VerifyReport{}, err
	}
	if err := r.integrity(ctx); err != nil {
		return VerifyReport{}, err
	}
	if err := r.signature(ctx); err != nil {
		return VerifyReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return VerifyReport{}, err
	}
	r.complete()
	r.index()
	if err := r.buildability(ctx); err != nil {
		return VerifyReport{}, err
	}
	r.finish()
	return r.rep, nil
}

// ---- finishing the report -----------------------------------------------------------------------

// finish derives the part results, the overall colour and the totals, and sorts everything.
func (r *run) finish() {
	rep := &r.rep
	sort.SliceStable(rep.Items, func(i, j int) bool { return rep.Items[i].Dir < rep.Items[j].Dir })
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Severity == "" {
			f.Severity = severityOf(f.Code)
		}
	}
	rep.Findings = orderFindings(rep.Findings)
	rep.Totals.Items = len(rep.Items)
	for _, it := range rep.Items {
		rep.Totals.Files += it.Files
	}
	rep.Totals.FilesHashed, rep.Totals.BytesHashed = r.hashedFiles, r.hashedBytes

	// Parts: every part that is present, needed or asked for, in the order of the layout.
	needed := map[string]bool{}
	for _, n := range r.allNeeds() {
		needed[n.Part] = true
	}
	for _, name := range Parts {
		if r.opts.Part != "" && r.opts.Part != name {
			continue
		}
		present := r.partDirs[name]
		if !present && !needed[name] && r.opts.Part != name {
			continue
		}
		pr := PartResult{Part: name, Status: StatusOK, Present: present}
		for _, it := range rep.Items {
			if it.Part != name {
				continue
			}
			pr.Items++
			if it.LastReverifiedAt != nil && (pr.OldestReverifiedAt == nil || it.LastReverifiedAt.Before(*pr.OldestReverifiedAt)) {
				t := *it.LastReverifiedAt
				pr.OldestReverifiedAt = &t
			}
		}
		worst := 0
		for _, f := range rep.Findings {
			if f.Part != name {
				continue
			}
			st := statusOfFinding(f)
			if rank := statusRank(st); rank > worst || rank == worst && f.Code == CodeKitBadSignature {
				worst, pr.Status, pr.Code = rank, st, f.Code
			}
		}
		for _, n := range rep.Notes {
			if n.Part == name && n.Kind == NoteUnchecked && statusRank(StatusWarning) > worst {
				worst, pr.Status = statusRank(StatusWarning), StatusWarning
			}
		}
		rep.Parts = append(rep.Parts, pr)
	}

	rep.Overall = Green
	for _, f := range rep.Findings {
		switch f.Colour {
		case Red:
			rep.Overall = Red
		case Yellow:
			if rep.Overall != Red {
				rep.Overall = Yellow
			}
		}
	}
	if rep.Overall == Green {
		for _, p := range rep.Parts {
			if p.Status != StatusOK {
				rep.Overall = Yellow
			}
		}
	}
}

func statusRank(s string) int {
	switch s {
	case StatusDamaged:
		return 5
	case StatusMissing:
		return 4
	case StatusInconsistent:
		return 3
	case StatusWarning:
		return 2
	}
	return 1
}

func statusOfFinding(f Finding) string {
	switch f.Kind {
	case KindDamaged, KindBadManifest, KindBadSignature:
		return StatusDamaged
	case KindMissing:
		return StatusMissing
	case KindWrongRelease, KindCudaTooNew:
		return StatusInconsistent
	}
	return StatusWarning
}

// severityOf is the doctor severity of a code ("warning" when the registry does not know it).
func severityOf(code string) string {
	if c, ok := doctor.Lookup(code); ok {
		return string(c.Severity)
	}
	return string(doctor.Warning)
}

// colourOf is the colour a finding with this code gives a root (ark-sync.md section 4.12.2): the
// same table as ready.KitColour, kept here because package ready imports the planner and so this
// package. kitready's tests prove the two agree.
func colourOf(code string, internal bool) string {
	switch code {
	case CodeKitMissingNode, CodeKitWrongRelease:
		if internal {
			return Red
		}
		return Yellow
	case CodeKitMissingLab, CodeKitCudaTooNew, CodeKitStaleIndex, doctor.KIT045:
		return Yellow
	}
	return Red // 001, 044 and the failed checks of the build dry run
}

// orderFindings sorts findings most serious first, then by part, item and need, so the report is
// deterministic.
func orderFindings(in []Finding) []Finding {
	out := append([]Finding(nil), in...)
	rank := func(f Finding) int {
		switch {
		case f.Code == CodeKitBadSignature:
			return 0
		case f.Colour == Red:
			return 1
		}
		return 2
	}
	order := map[string]int{}
	for i, p := range Parts {
		order[p] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if order[a.Part] != order[b.Part] {
			return order[a.Part] < order[b.Part]
		}
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		if a.Need != b.Need {
			return a.Need < b.Need
		}
		return a.Arg < b.Arg
	})
	return out
}

// ---- small helpers shared by the passes ----------------------------------------------------------

// rel joins slash path elements below kit/.
func rel(elem ...string) string { return path.Join(elem...) }

func (r *run) abs(relPath string) string {
	return filepath.Join(r.kitDir, filepath.FromSlash(relPath))
}

func (r *run) progress(stage, item, file string) {
	if r.opts.Progress != nil {
		r.opts.Progress(Progress{Stage: stage, Item: item, File: file})
	}
}

// inScope reports whether a part is part of this run.
func (r *run) inScope(part string) bool { return r.opts.Part == "" || r.opts.Part == part }

func (r *run) add(f Finding) {
	if f.Code == "" {
		return
	}
	if f.Colour == "" {
		f.Colour = colourOf(f.Code, r.opts.Internal)
	}
	r.rep.Findings = append(r.rep.Findings, f)
}

func (r *run) note(n Note) { r.rep.Notes = append(r.rep.Notes, n) }

func reasonFor(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ReasonMissing
	}
	return ReasonUnreadable
}
