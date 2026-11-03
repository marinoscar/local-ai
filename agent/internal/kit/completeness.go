package kit

import (
	"errors"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Completeness and consistency (rebuild-kit.md section 4.4 steps 3 to 5): is everything this
// computer needs to rebuild itself in the kit, present and verified; on a drive, is the same true for
// every other computer of pins.json; and do the closures fit this computer (Ubuntu release, CUDA
// against the driver).
//
// "Present and verified" is read from what the integrity pass found: the file is recorded by a
// manifest, is a regular file of the recorded size and, when it was re-hashed in this run, hashes to
// the recorded digest. A file that is there but that no manifest records cannot be trusted and is
// damaged, like a source tarball the build engine would refuse.

// NodeFacts are the facts about a computer the completeness rules need. A field that is empty is
// unknown: the rules that need it are skipped and the report says so. The CLI reads them from
// node.yaml, os-release, the architecture and nvidia-smi; tests build them by hand.
type NodeFacts struct {
	NodeID string `json:"node_id"`
	// Codename is the Ubuntu codename ("resolute") and Arch the kit's architecture word ("amd64",
	// "arm64").
	Codename string `json:"codename,omitempty"`
	Arch     string `json:"arch,omitempty"`
	// Backend is the llama backend: cuda, vulkan or cpu.
	Backend string `json:"backend,omitempty"`
	// CudaArch is the compute capability without the dot ("75" or "75;86"), for the prebuilt name.
	CudaArch string `json:"cuda_arch,omitempty"`
	// NvidiaBranch is the driver branch, "595-open" or just "595"; empty accepts any branch of this
	// computer's codename and architecture.
	NvidiaBranch string `json:"nvidia_branch,omitempty"`
	// DriverCUDA is the CUDA version the installed driver supports, the "CUDA Version: X.Y" of the
	// nvidia-smi header ("" when there is no answer).
	DriverCUDA string `json:"driver_cuda,omitempty"`
	// LlamaCommit and WhisperCommit are the pinned commits of node.yaml (llama.commit,
	// whisper.commit); AgentVersion is the running agent's version ("0.4.0" or "node-v0.4.0").
	LlamaCommit   string `json:"llama_commit,omitempty"`
	WhisperCommit string `json:"whisper_commit,omitempty"`
	AgentVersion  string `json:"agent_version,omitempty"`
}

// The things a computer needs from the kit (Need.Need).
const (
	NeedLlamaSource   = "llama_source"
	NeedWhisperSource = "whisper_source"
	NeedAgentBinary   = "agent_binary"
	NeedAgentSource   = "agent_source"
	NeedGoToolchain   = "go_toolchain"
	NeedDebs          = "debs"
	NeedNvidia        = "nvidia"
	NeedCuda          = "cuda_toolkit"
	NeedKiwix         = "kiwix_tools"
	NeedPmtiles       = "pmtiles"
	NeedPiper         = "piper"
	NeedPrebuilt      = "prebuilt"
)

// Need is one requirement of a computer and how the kit meets it.
type Need struct {
	// Need is one of the Need* keys; Arg says which (a commit, a tag, an architecture, a
	// "<codename>-<arch>" or "<codename>-<arch>-<branch>" folder name).
	Need string `json:"need"`
	Arg  string `json:"arg,omitempty"`
	Part string `json:"part"`
	// Item is the catalog id of the kit item that holds it.
	Item string `json:"item,omitempty"`
	// Optional: a missing prebuilt is a warning, never a failure.
	Optional bool `json:"optional,omitempty"`
	// Status is ok, missing, damaged, inconsistent, warning or skipped; Code the doctor code of a
	// status that is not ok.
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

// NodeResult is the completeness of one computer: this one, or one of the lab (pins.json).
type NodeResult struct {
	Node     string `json:"node"`
	Codename string `json:"codename,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Needs    []Need `json:"needs"`
}

func (r *run) allNeeds() []Need {
	var out []Need
	if r.rep.Node != nil {
		out = append(out, r.rep.Node.Needs...)
	}
	for _, l := range r.rep.Lab {
		out = append(out, l.Needs...)
	}
	return out
}

// release is a (codename, architecture) pair.
type release struct{ codename, arch string }

func (x release) dir() string { return x.codename + "-" + x.arch }

type nvidiaNeed struct {
	release
	branch string
}

// nodeNeeds is what one computer needs, from its facts or from its entries of pins.json.
type nodeNeeds struct {
	id             string
	lab            bool
	backend        string
	cudaArch       string
	driverCUDA     string
	llama, whisper []string
	agentTags      []string
	releases       []release
	archs          []string
	nvidia         []nvidiaNeed
	cuda           bool
	prebuilt       bool
}

var agentVersionRe = regexp.MustCompile(`^(?:node-)?v?([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*)$`)

// agentTag is the release tag of an agent version ("0.4.0" is "node-v0.4.0"); "" for a version that
// is not a release (a development build).
func agentTag(version string) string {
	m := agentVersionRe.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return ""
	}
	return "node-v" + m[1]
}

func needsFromFacts(f NodeFacts) (nodeNeeds, []string) {
	n := nodeNeeds{id: f.NodeID, backend: f.Backend, cudaArch: f.CudaArch, driverCUDA: f.DriverCUDA}
	var unknown []string
	if f.LlamaCommit != "" {
		n.llama = []string{f.LlamaCommit}
	} else {
		unknown = append(unknown, "llama.commit")
	}
	if f.WhisperCommit != "" {
		n.whisper = []string{f.WhisperCommit}
	}
	if t := agentTag(f.AgentVersion); t != "" {
		n.agentTags = []string{t}
	} else {
		unknown = append(unknown, "agent version")
	}
	switch {
	case f.Arch == "":
		unknown = append(unknown, "architecture")
	default:
		n.archs = []string{f.Arch}
		if f.Codename != "" {
			n.releases = []release{{f.Codename, f.Arch}}
		} else {
			unknown = append(unknown, "Ubuntu codename")
		}
	}
	if f.Backend == "cuda" {
		n.cuda = true
		if len(n.releases) > 0 {
			n.nvidia = []nvidiaNeed{{release: n.releases[0], branch: f.NvidiaBranch}}
		}
		if f.DriverCUDA == "" {
			unknown = append(unknown, "driver CUDA version")
		}
	}
	n.prebuilt = f.Backend != "" && f.LlamaCommit != "" && len(n.releases) > 0
	return n, unknown
}

// complete runs steps 3 to 5.
func (r *run) complete() {
	r.progress(StageComplete, "", "")
	if f := r.opts.Node; f != nil {
		n, unknown := needsFromFacts(*f)
		if len(unknown) > 0 {
			r.note(Note{Kind: NoteSkipped, Names: unknown, Detail: "facts about this computer that are not known"})
		}
		needs, findings := r.evalNode(n)
		for _, nd := range needs {
			r.node[nd.Need+"|"+nd.Arg] = true
		}
		for _, fd := range findings {
			r.add(fd)
		}
		r.rep.Node = &NodeResult{Node: f.NodeID, Codename: f.Codename, Arch: f.Arch, Backend: f.Backend, Needs: needs}
	} else {
		r.note(Note{Kind: NoteSkipped, Detail: "the facts about this computer are not known, so what it needs was not checked"})
	}
	if !r.opts.Internal {
		r.lab()
	}
}

// ---- the lab ----------------------------------------------------------------------------------------

// activePins drops the entries no node has reported for longer than PinsMaxAge (the merge would drop
// them too) and says which they were.
func activePins(p Pins, now time.Time) (Pins, []string) {
	var gone []string
	keep := func(own time.Time) bool { return !stale(reportTime(own, p.UpdatedAt), now) }
	out := Pins{UpdatedAt: p.UpdatedAt}
	for _, e := range p.LlamaCpp {
		if keep(e.ReportedAt) {
			out.LlamaCpp = append(out.LlamaCpp, e)
		} else {
			gone = append(gone, "llama.cpp "+shortCommit(e.Commit))
		}
	}
	for _, e := range p.WhisperCpp {
		if keep(e.ReportedAt) {
			out.WhisperCpp = append(out.WhisperCpp, e)
		} else {
			gone = append(gone, "whisper.cpp "+shortCommit(e.Commit))
		}
	}
	for _, e := range p.Agent {
		if keep(e.ReportedAt) {
			out.Agent = append(out.Agent, e)
		} else {
			gone = append(gone, "agent "+e.Tag)
		}
	}
	for _, e := range p.Codenames {
		if keep(e.ReportedAt) {
			out.Codenames = append(out.Codenames, e)
		} else {
			gone = append(gone, e.Codename+"-"+e.Arch)
		}
	}
	for _, e := range p.NvidiaBranches {
		if keep(e.ReportedAt) {
			out.NvidiaBranches = append(out.NvidiaBranches, e)
		} else {
			gone = append(gone, "nvidia "+e.Codename+"-"+e.Arch+"-"+e.Branch)
		}
	}
	return out, gone
}

func shortCommit(c string) string {
	if len(c) > 9 {
		return c[:9]
	}
	return c
}

// lab is step 4: the same completeness for every other node of pins.json, on a drive.
func (r *run) lab() {
	p, err := ReadPins(r.fs, r.abs(rel(PinsDir, PinsFileName)))
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		r.note(Note{Kind: NoteNoPins, Part: PartCatalog, Detail: "the kit has no pins.json, so what the other computers need was not checked"})
		return
	case IsPinsDamaged(err):
		if r.inScope(PartCatalog) {
			r.add(Finding{Code: CodePinsDamaged, Kind: KindPinsDamaged, Part: PartCatalog, Names: []string{rel(PinsDir, PinsFileName)}, Detail: err.Error()})
		}
		return
	default:
		r.note(Note{Kind: NoteNoPins, Part: PartCatalog, Detail: "pins.json cannot be read: " + err.Error()})
		return
	}
	active, gone := activePins(p, r.opts.Now)
	if len(gone) > 0 {
		r.note(Note{Kind: NoteStalePins, Part: PartCatalog, Names: gone, Detail: "no computer has reported these for 180 days; they are not required"})
	}
	own := ""
	if r.opts.Node != nil {
		own = r.opts.Node.NodeID
	}
	byKey := map[string]int{}
	var all []Finding
	for _, id := range active.NodesOf() {
		if id == own {
			continue
		}
		n := nodeNeeds{id: id, lab: true,
			llama: active.LlamaCommits(id), whisper: active.WhisperCommits(id), agentTags: active.AgentTags(id)}
		archSeen := map[string]bool{}
		for _, e := range active.Codenames {
			if hasNode(e.Nodes, id) {
				n.releases = append(n.releases, release{e.Codename, e.Arch})
				if !archSeen[e.Arch] {
					archSeen[e.Arch] = true
					n.archs = append(n.archs, e.Arch)
				}
			}
		}
		for _, e := range active.NvidiaBranches {
			if hasNode(e.Nodes, id) {
				n.cuda = true
				n.nvidia = append(n.nvidia, nvidiaNeed{release: release{e.Codename, e.Arch}, branch: e.Branch})
				if !archSeen[e.Arch] {
					archSeen[e.Arch] = true
					n.archs = append(n.archs, e.Arch)
				}
			}
		}
		if n.cuda {
			n.backend = "cuda"
		}
		needs, findings := r.evalNode(n)
		res := NodeResult{Node: id, Backend: n.backend, Needs: needs}
		if len(n.releases) > 0 {
			res.Codename, res.Arch = n.releases[0].codename, n.releases[0].arch
		}
		r.rep.Lab = append(r.rep.Lab, res)
		for _, f := range findings {
			if f.Kind == KindMissing && r.node[f.Need+"|"+f.Arg] {
				continue // already reported as missing for this computer
			}
			key := f.Code + "|" + f.Kind + "|" + f.Need + "|" + f.Arg + "|" + f.Part
			if i, ok := byKey[key]; ok {
				all[i].Nodes = append(all[i].Nodes, f.Nodes...)
				continue
			}
			byKey[key] = len(all)
			all = append(all, f)
		}
	}
	for _, f := range all {
		r.add(f)
	}
}

// ---- evaluating the needs of one computer -----------------------------------------------------------

// evalNode judges every requirement of one computer against the kit. The needs come in a fixed order
// (sources, agent, toolchain, packages, CUDA, tools, prebuilt); the findings are the ones that carry a
// doctor code.
func (r *run) evalNode(n nodeNeeds) ([]Need, []Finding) {
	var needs []Need
	var findings []Finding
	missingCode := CodeKitMissingNode
	if n.lab {
		missingCode = CodeKitMissingLab
	}
	// emit records a need whose state is st ("ok", "missing", "bad" (recorded, failed its check),
	// "unrecorded" (there, but no manifest records it)).
	emit := func(nd Need, st string, names ...string) {
		if !r.inScope(nd.Part) {
			return
		}
		switch st {
		case "ok":
			nd.Status = StatusOK
		case "missing":
			nd.Status, nd.Code = StatusMissing, missingCode
			findings = append(findings, Finding{Code: missingCode, Kind: KindMissing, Part: nd.Part, Item: nd.Item, Need: nd.Need, Arg: nd.Arg, Nodes: []string{n.id}})
		case "bad":
			nd.Status, nd.Code = StatusDamaged, CodeKitDamagedFile
			if r.signatureFailed() && nd.Need == NeedAgentBinary {
				nd.Code = CodeKitBadSignature
			}
		case "unrecorded":
			nd.Status, nd.Code = StatusDamaged, CodeKitDamagedFile
			findings = append(findings, Finding{Code: CodeKitDamagedFile, Kind: KindDamaged, Part: nd.Part, Item: nd.Item, Names: names, Detail: ReasonNotRecorded})
		}
		needs = append(needs, nd)
	}

	for _, c := range n.llama {
		st, rels := r.tarballState(SourceStemLlama, c)
		emit(Need{Need: NeedLlamaSource, Arg: shortCommit(c), Part: PartSrc, Item: "kit-llama-cpp"}, st, unrecordedNames(st, rels)...)
	}
	for _, c := range n.whisper {
		st, rels := r.tarballState("whisper.cpp", c)
		emit(Need{Need: NeedWhisperSource, Arg: shortCommit(c), Part: PartSrc, Item: "kit-whisper-cpp"}, st, unrecordedNames(st, rels)...)
	}
	for _, a := range n.archs {
		st, rels := r.fileState(rel(PartBin, "aiagent-linux-"+a))
		emit(Need{Need: NeedAgentBinary, Arg: a, Part: PartBin, Item: "kit-agent-binaries"}, st, unrecordedNames(st, rels)...)
	}
	for _, t := range n.agentTags {
		st, rels := r.fileState(rel(PartSrc, "local-ai-"+t+"-src.tar.gz"))
		emit(Need{Need: NeedAgentSource, Arg: t, Part: PartSrc, Item: "kit-agent-source"}, st, unrecordedNames(st, rels)...)
	}
	for _, a := range n.archs {
		st := r.anyFileState(PartToolchains+"/", func(name string) bool {
			return strings.HasPrefix(name, "go") && strings.HasSuffix(name, ".linux-"+a+".tar.gz")
		})
		emit(Need{Need: NeedGoToolchain, Arg: a, Part: PartToolchains, Item: "kit-go-toolchain"}, st)
	}
	for _, rl := range n.releases {
		nd := Need{Need: NeedDebs, Arg: rl.dir(), Part: PartDebs, Item: "kit-debs-" + rl.dir()}
		st, others := r.repoState(PartDebs, rl.dir(), "")
		if st == "missing" && len(others) > 0 && r.opts.Internal && !n.lab {
			r.wrongRelease(&needs, &findings, nd, others)
			continue
		}
		emit(nd, st)
	}
	for _, nv := range n.nvidia {
		nd := Need{Need: NeedNvidia, Arg: nv.dir(), Part: PartNvidia}
		if nv.branch != "" {
			nd.Arg += "-" + nv.branch
		}
		st, others := r.repoState(PartNvidia, nv.dir(), nv.branch)
		if st == "missing" && len(others) > 0 && r.opts.Internal && !n.lab {
			nd.Item = "kit-nvidia-" + nd.Arg
			r.wrongRelease(&needs, &findings, nd, others)
			continue
		}
		if nv.branch != "" {
			nd.Item = "kit-nvidia-" + nd.Arg
		}
		emit(nd, st)
	}
	if n.cuda && len(n.releases) > 0 {
		tk := r.cudaToolkit(n.releases[0])
		nd := Need{Need: NeedCuda, Part: tk.part, Item: tk.item}
		if tk.version != "" {
			nd.Arg = tk.version
		}
		switch {
		case tk.state == "ok" && !n.lab && n.driverCUDA != "" && tk.version != "" && versionLess(n.driverCUDA, tk.version):
			if r.inScope(nd.Part) {
				nd.Status, nd.Code = StatusInconsistent, CodeKitCudaTooNew
				needs = append(needs, nd)
				findings = append(findings, Finding{Code: CodeKitCudaTooNew, Kind: KindCudaTooNew, Part: nd.Part, Item: nd.Item,
					Need: NeedCuda, Arg: tk.version, Have: []string{tk.version}, Want: n.driverCUDA, Nodes: []string{n.id}})
			}
		default:
			emit(nd, tk.state)
		}
	}
	for _, t := range []struct{ need, name string }{{NeedKiwix, "kiwix-tools"}, {NeedPmtiles, "pmtiles"}, {NeedPiper, "piper"}} {
		for _, a := range n.archs {
			words := archWords(a)
			st := r.anyFileState(PartTools+"/"+t.name+"/", func(name string) bool {
				for _, w := range words {
					if strings.Contains(name, w) {
						return true
					}
				}
				return false
			})
			emit(Need{Need: t.need, Arg: a, Part: PartTools, Item: "kit-" + t.name}, st)
		}
	}
	if n.prebuilt && !n.lab {
		nd := Need{Need: NeedPrebuilt, Part: PartPrebuilt, Item: "kit-prebuilt", Optional: true}
		rl := n.releases[0]
		c9 := shortCommit(n.llama[0])
		nd.Arg = c9 + "-" + n.backend
		st := r.prebuiltState(c9, n.backend, rl, n.cudaArch)
		if r.inScope(PartPrebuilt) {
			switch st {
			case "ok":
				nd.Status = StatusOK
			case "bad":
				nd.Status, nd.Code = StatusDamaged, CodeKitDamagedFile
			default:
				nd.Status, nd.Code = StatusWarning, CodeKitMissingNode
				findings = append(findings, Finding{Code: CodeKitMissingNode, Kind: KindPrebuiltMissing, Part: PartPrebuilt, Item: nd.Item,
					Need: NeedPrebuilt, Arg: nd.Arg, Colour: Yellow, Optional: true, Nodes: []string{n.id}})
			}
			needs = append(needs, nd)
		}
	}
	return needs, findings
}

func unrecordedNames(st string, rels []string) []string {
	if st != "unrecorded" {
		return nil
	}
	return rels
}

// wrongRelease records a debs or nvidia need that the kit meets only with another Ubuntu release or
// architecture: LAI-KIT-042 (rebuild-kit.md section 4.4 step 5).
func (r *run) wrongRelease(needs *[]Need, findings *[]Finding, nd Need, others []string) {
	if !r.inScope(nd.Part) {
		return
	}
	nd.Status, nd.Code = StatusInconsistent, CodeKitWrongRelease
	*needs = append(*needs, nd)
	*findings = append(*findings, Finding{Code: CodeKitWrongRelease, Kind: KindWrongRelease, Part: nd.Part, Item: nd.Item,
		Need: nd.Need, Arg: nd.Arg, Have: others, Want: nd.Arg})
}

func archWords(arch string) []string {
	if arch == "arm64" {
		return []string{"aarch64", "arm64"}
	}
	return []string{"x86_64", "amd64"}
}

// ---- looking things up ---------------------------------------------------------------------------------

// fileState says how one file of the kit stands: "ok" (recorded, regular, the recorded size, and
// hashed to the recorded digest when it was re-read), "bad" (recorded and failed its check),
// "unrecorded" (there, but no manifest records it) or "missing".
func (r *run) fileState(relPath string) (string, []string) {
	if f, ok := r.recs[relPath]; ok {
		switch {
		case f.state == recBad && f.reason == ReasonMissing:
			return "missing", nil
		case f.state == recBad:
			return "bad", nil
		}
		return "ok", nil
	}
	if fi, err := r.fs.Lstat(r.abs(relPath)); err == nil && fi.Mode().IsRegular() {
		return "unrecorded", []string{relPath}
	}
	return "missing", nil
}

// tarballState is fileState for the source tarball of a commit: a full commit names the file, a short
// one (7 to 39 hex digits) is looked up by prefix and must name exactly one.
func (r *run) tarballState(stem, commit string) (string, []string) {
	if len(commit) == 40 {
		return r.fileState(rel(PartSrc, SourceTarballName(stem, commit)))
	}
	var found []string
	prefix := rel(PartSrc, stem+"-"+commit)
	for k := range r.recs {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".tar.gz") {
			found = append(found, k)
		}
	}
	if len(found) != 1 {
		return "missing", nil
	}
	return r.fileState(found[0])
}

// anyFileState looks at the recorded files below prefix (a path below kit/ ending in a slash) whose
// base name matches: "ok" when one is fine, else "bad" when one is recorded but failed, else "missing".
func (r *run) anyFileState(prefix string, match func(name string) bool) string {
	state := "missing"
	var keys []string
	for k := range r.recs {
		if strings.HasPrefix(k, prefix) && match(path.Base(k)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		st, _ := r.fileState(k)
		switch st {
		case "ok":
			return "ok"
		case "bad":
			state = "bad"
		}
	}
	return state
}

// repoState says how the apt repository of a computer stands: "ok", "bad" (it fails its check or its
// manifest is damaged) or "missing"; with others naming the repositories of the part that are for
// another release or architecture. For nvidia, branch narrows the repositories of the computer's
// codename and architecture ("595" accepts "595-open"; "" accepts any).
func (r *run) repoState(part, dir, branch string) (string, []string) {
	var mine []*scanItem
	var others []string
	for _, it := range r.items {
		if it.part != part {
			continue
		}
		name := path.Base(it.dir)
		if path.Dir(it.dir) != part {
			continue
		}
		switch {
		case part == PartDebs && name == dir:
			mine = append(mine, it)
		case part == PartNvidia && strings.HasPrefix(name, dir+"-") && branchMatches(strings.TrimPrefix(name, dir+"-"), branch):
			mine = append(mine, it)
		case part == PartNvidia && strings.HasPrefix(name, dir+"-"):
			// the right release but another driver branch: not "another Ubuntu"
		default:
			others = append(others, it.dir)
		}
	}
	if len(mine) == 0 {
		if part == PartNvidia {
			// A repository of this computer's release and another branch is not a different Ubuntu.
			for _, it := range r.items {
				if it.part == PartNvidia && strings.HasPrefix(path.Base(it.dir), dir+"-") {
					return "missing", nil
				}
			}
		}
		return "missing", others
	}
	best := "bad"
	for _, it := range mine {
		if it.valid && it.status == StatusOK {
			best = "ok"
		}
	}
	return best, nil
}

func branchMatches(have, want string) bool {
	return want == "" || have == want || strings.HasPrefix(have, want+"-")
}

// toolkit is where the CUDA toolkit of a computer is in the kit.
type toolkit struct {
	// state is ok, bad or missing; part and item say where it lives; version is its CUDA version.
	state, part, item, version string
}

// cudaToolkit finds the CUDA toolkit for a release: option A, the nvidia-cuda-toolkit package in the
// debs closure of the release (read from its PACKAGES.lock), else option B, a redistributable set in
// kit/cuda/<version>-<arch> (rebuild-kit.md section 4.1.6).
func (r *run) cudaToolkit(rl release) toolkit {
	if it := r.byDir[rel(PartDebs, rl.dir())]; it != nil && it.valid {
		if data, err := r.fs.ReadFile(r.abs(rel(it.dir, LockName))); err == nil {
			if lock, err := ParseLock(data); err == nil {
				for _, e := range lock {
					if e.Name == "nvidia-cuda-toolkit" {
						st := "ok"
						if it.status != StatusOK {
							st = "bad"
						}
						return toolkit{state: st, part: PartDebs, item: it.id, version: cudaVersionOf(e.Version)}
					}
				}
			}
		}
	}
	best := toolkit{state: "missing", part: PartCuda}
	for _, it := range r.items {
		if it.part != PartCuda || path.Dir(it.dir) != PartCuda {
			continue
		}
		name := path.Base(it.dir)
		ver, ok := strings.CutSuffix(name, "-"+rl.arch)
		if !ok || !it.valid {
			continue
		}
		st := "ok"
		if it.status != StatusOK {
			st = "bad"
		}
		cand := toolkit{state: st, part: PartCuda, item: it.id, version: ver}
		if best.state == "missing" || best.state == "bad" && st == "ok" || st == best.state && versionLess(best.version, ver) {
			best = cand
		}
	}
	return best
}

var cudaVersionRe = regexp.MustCompile(`^(?:[0-9]+:)?([0-9]+\.[0-9]+)`)

// cudaVersionOf is the CUDA version at the front of a toolkit package version ("12.4.131~12.4.1-1" is
// "12.4"); "" when there is none.
func cudaVersionOf(debVersion string) string {
	if m := cudaVersionRe.FindStringSubmatch(debVersion); m != nil {
		return m[1]
	}
	return ""
}

// prebuiltState: a prebuilt of the current llama build is in kit/prebuilt: "ok", "bad" (one is there
// and fails its check) or "missing". The name is
// llama.cpp-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz (rebuild-kit.md section 3.1); for CUDA,
// when the compute capabilities are known, the -sm suffix must be one of them.
func (r *run) prebuiltState(c9, backend string, rl release, cudaArch string) string {
	prefix := "llama.cpp-" + c9 + "-" + backend + "-" + rl.dir()
	caps := map[string]bool{}
	for _, c := range strings.Split(cudaArch, ";") {
		if c != "" {
			caps[c] = true
		}
	}
	state := "missing"
	var keys []string
	for k := range r.recs {
		if strings.HasPrefix(k, PartPrebuilt+"/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := path.Base(k)
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok || !strings.HasSuffix(rest, ".tar.gz") {
			continue
		}
		rest = strings.TrimSuffix(rest, ".tar.gz")
		switch {
		case rest == "":
		case strings.HasPrefix(rest, "-sm"):
			if backend == "cuda" && len(caps) > 0 && !caps[strings.TrimPrefix(rest, "-sm")] {
				continue
			}
		default:
			continue
		}
		switch st, _ := r.fileState(k); st {
		case "ok":
			return "ok"
		case "bad":
			state = "bad"
		}
	}
	return state
}
