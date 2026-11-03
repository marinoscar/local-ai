package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/jsonord"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The generated files of the kit (rebuild-kit.md section 3.1).
const (
	// IndexFileName is kit/MANIFEST.json, the index of the kit items present.
	IndexFileName = "MANIFEST.json"
	// ReadmeFileName is kit/README.txt, the plain-text guide.
	ReadmeFileName = "README.txt"
	// IndexSchema is the value of the index's "schema" key. An item MANIFEST has no such key and
	// carries schema_version instead, so a tool that walks every MANIFEST.json (kit verify, the
	// scanner) tells the index from an item manifest by this key and never mistakes one for the other.
	IndexSchema = "localai.kit-index"
	// IndexVersion is the only index_version this code writes.
	IndexVersion = 1
)

// CodeIndexFailed is the doctor code of an index or guide that could not be written (LAI-KIT-046).
const CodeIndexFailed = doctor.KIT046

// ErrIndex: the kit index or guide could not be scanned or written. The kit itself is untouched
// (LAI-KIT-046); the next sync rewrites both.
var ErrIndex = errors.New("kit: could not refresh the kit index")

// IndexItem is one kit item present on the root, taken from its MANIFEST.json.
type IndexItem struct {
	ID string
	// Dir is the item directory below kit/, slash separated (git/llama.cpp.git).
	Dir string
	// Part is the top-level part of Dir (git).
	Part           string
	Version        string
	CatalogVersion string
	// TotalBytes and Files are the size and the number of files the manifest records.
	TotalBytes int64
	Files      int
	// TreeSHA256 is set for a directory item (a git mirror, an apt closure).
	TreeSHA256 string
	Trust      string
	VerifiedAt time.Time
}

// IndexProblem is a directory of kit/ whose manifest is damaged, incomplete, not verified, from a
// newer schema or in the wrong place (ark.ScanRoot's LAI-SYN-052). The item counts as absent.
type IndexProblem struct {
	Dir  string
	Code string
}

// PartSummary is the share of one part in the index.
type PartSummary struct {
	Part       string
	Items      int
	TotalBytes int64
}

// Index is kit/MANIFEST.json before encoding.
type Index struct {
	GeneratedAt    time.Time
	RootID         string
	CatalogVersion string
	// Items are the kit items present, sorted by Dir.
	Items []IndexItem
	// Parts summarises Items per part, in the order of Parts; a part with no item is left out.
	Parts []PartSummary
	// Invalid lists the kit directories whose manifest could not be used, sorted by Dir.
	Invalid []IndexProblem
	// Missing lists the ids of the kit items the caller expected that are not present, sorted.
	Missing    []string
	TotalBytes int64
}

// IndexOptions are the inputs of BuildIndex besides the root.
type IndexOptions struct {
	// Now is the generation time; the caller's clock, so tests never read the real one.
	Now time.Time
	// Expected are the catalog items the root should hold (for example the kit items of
	// plan.DesiredItems). Only kit items count. nil lists no missing item.
	Expected []catalog.ContentItem
	// CatalogVersion is the version of the catalog the root was planned with (informational).
	CatalogVersion string
}

// BuildIndex scans <root>/kit for item MANIFESTs (the same walk as ark.ScanRoot, so there is one
// definition of "an item directory") and returns the index. It reads manifests and stats files, never
// hashes and never writes. A directory without a manifest is silently not an item (kit/config/, a
// half-swapped directory); one with a bad manifest is listed in Invalid. The only error is the
// context's or a failed scan of the root.
func BuildIndex(ctx context.Context, fsys config.FS, root ark.Root, opts IndexOptions) (Index, error) {
	scanned, problems, err := ark.ScanRoot(ctx, fsys, root)
	if err != nil {
		return Index{}, fmt.Errorf("%w: %v", ErrIndex, err)
	}
	ix := Index{GeneratedAt: stamp(opts.Now), RootID: root.ID, CatalogVersion: opts.CatalogVersion}
	present := map[string]bool{}
	for _, s := range scanned {
		dir, ok := kitRel(s.RelDir)
		if !ok || s.Manifest.ItemKind != manifest.KindKit {
			continue
		}
		m := s.Manifest
		files := len(m.Files)
		if m.IsDir() {
			files = m.FileCount
		}
		ix.Items = append(ix.Items, IndexItem{
			ID: m.ItemID, Dir: dir, Part: partOf(dir), Version: m.Version, CatalogVersion: m.CatalogVersion,
			TotalBytes: m.TotalBytes, Files: files, TreeSHA256: m.TreeSHA256, Trust: string(m.Trust),
			VerifiedAt: m.VerifiedAt,
		})
		present[m.ItemID] = true
		ix.TotalBytes += m.TotalBytes
	}
	for _, p := range problems {
		dirAny, _ := p.Details["dir"].(string)
		dir, ok := kitRel(dirAny)
		if !ok && dirAny != DirName {
			continue
		}
		if dirAny == DirName {
			dir = "."
		}
		ix.Invalid = append(ix.Invalid, IndexProblem{Dir: dir, Code: p.Code})
	}
	sort.SliceStable(ix.Items, func(i, j int) bool { return ix.Items[i].Dir < ix.Items[j].Dir })
	sort.SliceStable(ix.Invalid, func(i, j int) bool { return ix.Invalid[i].Dir < ix.Invalid[j].Dir })
	for _, it := range opts.Expected {
		if IsKitItem(it) && !present[it.ID] {
			ix.Missing = append(ix.Missing, it.ID)
		}
	}
	ix.Missing = dedupSorted(ix.Missing)
	byPart := map[string]*PartSummary{}
	for _, it := range ix.Items {
		ps := byPart[it.Part]
		if ps == nil {
			ps = &PartSummary{Part: it.Part}
			byPart[it.Part] = ps
		}
		ps.Items++
		ps.TotalBytes += it.TotalBytes
	}
	for _, name := range Parts {
		if ps := byPart[name]; ps != nil {
			ix.Parts = append(ix.Parts, *ps)
			delete(byPart, name)
		}
	}
	// A directory outside the known parts (a kit written by a newer agent) is still listed, after.
	var rest []string
	for name := range byPart {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		ix.Parts = append(ix.Parts, *byPart[name])
	}
	return ix, nil
}

// kitRel turns "kit/git/llama.cpp.git" into "git/llama.cpp.git".
func kitRel(rel string) (string, bool) {
	rest, ok := strings.CutPrefix(rel, DirName+"/")
	return rest, ok && rest != ""
}

func partOf(dir string) string {
	first, _, _ := strings.Cut(dir, "/")
	return first
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Encoding

type wireItem struct {
	ID             string        `json:"id"`
	Dir            string        `json:"dir"`
	Part           string        `json:"part"`
	Version        string        `json:"version"`
	CatalogVersion string        `json:"catalog_version,omitempty"`
	TotalBytes     int64         `json:"total_bytes"`
	Files          int           `json:"files"`
	TreeSHA256     string        `json:"tree_sha256,omitempty"`
	Trust          string        `json:"trust,omitempty"`
	VerifiedAt     jsonord.Stamp `json:"verified_at"`
}

type wirePart struct {
	Part       string `json:"part"`
	Items      int    `json:"items"`
	TotalBytes int64  `json:"total_bytes"`
}

type wireInvalid struct {
	Dir  string `json:"dir"`
	Code string `json:"code"`
}

// Encode returns the bytes of kit/MANIFEST.json: the keys in a fixed order, items sorted by
// directory, indented by two spaces, trailing newline. The same Index always gives the same bytes.
// "schema" comes first and "schema_version" is deliberately absent (see IndexSchema).
func (ix Index) Encode() ([]byte, error) {
	items := make([]wireItem, 0, len(ix.Items))
	for _, it := range ix.Items {
		items = append(items, wireItem{
			ID: it.ID, Dir: it.Dir, Part: it.Part, Version: it.Version, CatalogVersion: it.CatalogVersion,
			TotalBytes: it.TotalBytes, Files: it.Files, TreeSHA256: it.TreeSHA256, Trust: it.Trust,
			VerifiedAt: jsonord.Stamp(it.VerifiedAt),
		})
	}
	parts := make([]wirePart, 0, len(ix.Parts))
	for _, p := range ix.Parts {
		parts = append(parts, wirePart{p.Part, p.Items, p.TotalBytes})
	}
	invalid := make([]wireInvalid, 0, len(ix.Invalid))
	for _, p := range ix.Invalid {
		invalid = append(invalid, wireInvalid{p.Dir, p.Code})
	}
	missing := ix.Missing
	if missing == nil {
		missing = []string{}
	}
	known := []jsonord.KV{
		{Key: "schema", Val: IndexSchema},
		{Key: "index_version", Val: IndexVersion},
		{Key: "generated_at", Val: jsonord.Stamp(ix.GeneratedAt)},
		{Key: "root_id", Val: ix.RootID},
	}
	if ix.CatalogVersion != "" {
		known = append(known, jsonord.KV{Key: "catalog_version", Val: ix.CatalogVersion})
	}
	known = append(known,
		jsonord.KV{Key: "total_bytes", Val: ix.TotalBytes},
		jsonord.KV{Key: "parts", Val: parts},
		jsonord.KV{Key: "items", Val: items},
		jsonord.KV{Key: "missing", Val: missing},
		jsonord.KV{Key: "invalid", Val: invalid},
	)
	raw, err := jsonord.Object(known, nil)
	if err != nil {
		return nil, err
	}
	return jsonord.Indent(raw)
}

// IsIndex reports whether the bytes of a MANIFEST.json are a kit index rather than an item
// manifest: a JSON object whose "schema" is IndexSchema. Anything else (including damaged JSON) is
// false, so a caller that then parses it as an item manifest reports the damage itself.
func IsIndex(data []byte) bool {
	var probe struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Schema == IndexSchema
}

// ---------------------------------------------------------------------------------------------
// README.txt

// Readme returns the plain-text guide kit/README.txt: what is in the kit, where to start, where the
// manuals are. It is built from the index only, so it is as deterministic as the index; it never
// prints a host path or a number that is not in the index. Lines are at most 78 characters.
func (ix Index) Readme() []byte {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("LOCAL AI REBUILD KIT")
	line("====================")
	line("")
	line("This folder lets you rebuild a Local AI computer with NO internet. It holds the")
	line("programs, the source code, the Ubuntu packages, the graphics driver packages and")
	line("the catalog that a rebuild needs. An Ubuntu install USB stick is the only other")
	line("thing you need.")
	line("")
	line("Do not edit, rename or delete anything in this folder by hand. The Local AI agent")
	line("keeps it up to date and checks it. This file is rewritten after every change.")
	line("")
	line("Written (UTC):  %s", ix.GeneratedAt.UTC().Format("2006-01-02 15:04"))
	if ix.CatalogVersion != "" {
		line("Catalog:        %s", ix.CatalogVersion)
	}
	line("Items here:     %d (%s)", len(ix.Items), HumanBytes(ix.TotalBytes))
	line("")
	line("WHERE TO START")
	line("--------------")
	line("1. Read the guide \"Rebuild a node offline\". Open this file in any web browser:")
	line("       ../docs/06-rebuild-a-node-offline.html")
	line("   All the manuals are listed in ../docs/index.html.")
	line("2. To see what is here:             aictl kit status")
	line("3. To check nothing is damaged:     aictl kit verify")
	line("4. To rebuild a computer: install Ubuntu from the USB stick, then run")
	line("       installers/install-node.sh")
	line("   from this folder, as the guide says, step by step.")
	line("")
	line("WHAT IS IN EACH FOLDER")
	line("----------------------")
	byPart := map[string]PartSummary{}
	for _, p := range ix.Parts {
		byPart[p.Part] = p
	}
	for _, name := range Parts {
		desc := partText[name]
		if name == PartInstallers {
			// installers/ is written together with bin/ and has no manifest of its own.
			if p, ok := byPart[PartBin]; ok && p.Items > 0 {
				line("%-12s %s", name+"/", desc.what)
				line("%-12s here, with the agent program", "")
			} else {
				line("%-12s %s", name+"/", desc.what)
				line("%-12s NOT HERE YET", "")
			}
			continue
		}
		if name == PartPrebuilt {
			// prebuilt/ is written by the computers themselves and is not a sync item.
			line("%-12s %s", name+"/", desc.what)
			if p, ok := byPart[name]; ok {
				line("%-12s here (%d items, %s)", "", p.Items, HumanBytes(p.TotalBytes))
			} else {
				line("%-12s none yet (a computer adds its own after it builds)", "")
			}
			continue
		}
		line("%-12s %s", name+"/", desc.what)
		if p, ok := byPart[name]; ok {
			line("%-12s here (%d %s, %s)", "", p.Items, plural(p.Items, "item", "items"), HumanBytes(p.TotalBytes))
		} else if desc.driveOnly {
			line("%-12s not here (only the AI-ARK drive keeps this)", "")
		} else {
			line("%-12s NOT HERE YET", "")
		}
	}
	line("%-12s %s", "config/", "Backups of each computer's settings. Never deleted by the agent.")
	if len(ix.Missing) > 0 || len(ix.Invalid) > 0 {
		line("")
		line("NEEDS ATTENTION")
		line("---------------")
		if len(ix.Missing) > 0 {
			line("Missing: %s", strings.Join(ix.Missing, ", "))
			line("   Connect to the internet and run: aictl ark sync")
		}
		for _, p := range ix.Invalid {
			line("Damaged: %s (%s)", p.Dir, p.Code)
		}
		if len(ix.Invalid) > 0 {
			line("   Run: aictl kit verify --full     (then aictl ark sync, with internet)")
		}
	}
	line("")
	line("The same index in machine-readable form is MANIFEST.json, next to this file.")
	return []byte(b.String())
}

type partDesc struct {
	what      string
	driveOnly bool
}

var partText = map[string]partDesc{
	PartCatalog:    {what: "The catalog this kit was planned with, and pins.json (versions in use)."},
	PartInstallers: {what: "install-node.sh, the installer that starts an offline rebuild."},
	PartBin:        {what: "The Local AI agent program for both CPU types, with signatures."},
	PartSrc:        {what: "Source code packages (llama.cpp, whisper.cpp, the agent), ready to build."},
	PartGit:        {what: "Full copies of the source repositories, to move to another version."},
	PartToolchains: {what: "The Go compiler, to rebuild the agent with no internet."},
	PartTools:      {what: "kiwix-tools (Wikipedia reader), pmtiles (maps), piper (voice)."},
	PartDesktop:    {what: "The ai-desktop app installers."},
	PartDebs:       {what: "Ubuntu packages (compilers, Vulkan, tools), one folder per Ubuntu release."},
	PartNvidia:     {what: "NVIDIA graphics driver packages, one folder per driver version."},
	PartCuda:       {what: "Optional smaller CUDA compiler files."},
	PartPrebuilt:   {what: "Ready-made llama.cpp and whisper.cpp builds exported by the computers."},
	PartISO:        {what: "The Ubuntu install image, to make a new install USB stick.", driveOnly: true},
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// HumanBytes writes a byte count with one decimal in decimal units (1 GB = 10^9 bytes), the way the
// catalog states sizes: "0 B", "512 B", "1.5 kB", "612.4 MB", "3.2 GB", "1.1 TB".
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGT"[exp])
}

// ---------------------------------------------------------------------------------------------
// Writing and the swap hook

// WriteIndex writes kit/MANIFEST.json and then kit/README.txt atomically (temp file, fsync, rename,
// fsync of the directory), mode 0644. It does not create kit/: a root without one has nothing to
// index. The index goes first: a crash between the two leaves a fresh index and the previous guide,
// both valid. Failures wrap ErrIndex.
func WriteIndex(fsys config.FS, root ark.Root, ix Index) error {
	dir, err := root.Join(DirName)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	info, err := fsys.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s has no kit directory", ErrIndex, root.ID)
	}
	data, err := ix.Encode()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, IndexFileName), data, 0o644, -1, -1); err != nil {
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	if err := config.WriteFileAtomicFS(fsys, filepath.Join(dir, ReadmeFileName), ix.Readme(), 0o644, -1, -1); err != nil {
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	return nil
}

// Regenerate scans the root and rewrites both generated files, returning the index it wrote.
func Regenerate(ctx context.Context, fsys config.FS, root ark.Root, opts IndexOptions) (Index, error) {
	ix, err := BuildIndex(ctx, fsys, root, opts)
	if err != nil {
		return Index{}, err
	}
	if err := WriteIndex(fsys, root, ix); err != nil {
		return Index{}, err
	}
	return ix, nil
}

// Refresher is the hook the sync engine calls after it swaps a kit item into a root: it regenerates
// kit/MANIFEST.json and kit/README.txt so they always describe what is on the disk. It is plain
// data plus a clock, so the engine (and tests) build one and pass it around; nothing registers it.
type Refresher struct {
	FS config.FS
	// Now is the clock; time.Now in production.
	Now func() time.Time
	// Expected gives the catalog items the root should hold, so missing ones are listed. It may be
	// nil, and it may return nil.
	Expected func(root ark.Root) []catalog.ContentItem
	// CatalogVersion is the catalog the root was planned with.
	CatalogVersion func() string
}

// AfterSwap is the hook body. It does nothing (and returns nil) for an item that is not a kit item
// and for a read-only root, which sync never writes. Otherwise it regenerates the two files; a failure
// wraps ErrIndex (LAI-KIT-046, a warning: the swap itself stands).
func (r Refresher) AfterSwap(ctx context.Context, root ark.Root, item catalog.ContentItem) error {
	if !IsKitItem(item) || root.ReadOnly {
		return nil
	}
	return r.Refresh(ctx, root)
}

// Refresh regenerates the two files of a root unconditionally (after a swap, at the end of a sync
// run, after an import).
func (r Refresher) Refresh(ctx context.Context, root ark.Root) error {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	opts := IndexOptions{Now: now()}
	if r.Expected != nil {
		opts.Expected = r.Expected(root)
	}
	if r.CatalogVersion != nil {
		opts.CatalogVersion = r.CatalogVersion()
	}
	_, err := Regenerate(ctx, r.FS, root, opts)
	return err
}

// Rel returns the path of a generated file relative to a content root (kit/MANIFEST.json).
func Rel(name string) string { return path.Join(DirName, name) }
