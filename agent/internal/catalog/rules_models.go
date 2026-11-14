package catalog

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Rules of catalog.md section 2 (models.yaml): LAI-CAT-010 to 014.

var (
	repoRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]*/[A-Za-z0-9][A-Za-z0-9._\-]*$`)
	licenseRe = regexp.MustCompile(`^(custom:https?://\S+|[A-Za-z0-9][A-Za-z0-9.+\- ()]*)$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// SummaryMaxRunes is the length limit of a model summary.
const SummaryMaxRunes = 120

func (s *lintState) lintModels(pf *parsedFile[ModelsFile], ids map[string]string) {
	for i := range pf.Value.Models {
		m := &pf.Value.Models[i]
		p := indexPath("models", i)
		s.checkID(FileModels, p, m.ID, ids)
		if pf.Dirty[p] {
			continue // a schema problem inside this entry was reported; do not pile rules on top
		}
		s.lintModel(p, m)
	}
}

func (s *lintState) lintModel(p string, m *Model) {
	const f = FileModels
	sch := func(sub, format string, args ...any) { s.add(f, CodeSchema, joinPath(p, sub), format, args...) }

	if n := utf8.RuneCountInString(m.Summary); n > SummaryMaxRunes {
		sch("summary", "summary is %d characters, the limit is %d", n, SummaryMaxRunes)
	}
	for _, key := range sortedKeys(m.Summaries) {
		if n := utf8.RuneCountInString(m.Summaries[key]); n > SummaryMaxRunes {
			sch(key, "%s is %d characters, the limit is %d", key, n, SummaryMaxRunes)
		}
	}
	if !oneOf(m.Engine, engines) {
		sch("engine", "%s", enumMsg("engine", m.Engine, engines))
	}
	if len(m.Roles) == 0 {
		sch("roles", "roles must list at least one role")
	}
	for i, r := range m.Roles {
		if !oneOf(r, roles) {
			s.add(f, CodeSchema, indexPath(joinPath(p, "roles"), i), "%s", enumMsg("role", r, roles))
		}
	}
	if m.SurvivalTier < 1 || m.SurvivalTier > 3 {
		sch("survival_tier", "survival_tier must be 1, 2 or 3, got %d", m.SurvivalTier)
	}
	if !licenseRe.MatchString(m.License) {
		sch("license", `license must be an SPDX id or "custom:<url>", got %q`, truncate(m.License, 40))
	}

	s.lintRequirements(p, &m.Requirements)
	s.lintModelSource(p, m)
	count := s.lintModelFiles(p, m)
	s.lintModelFileRules(p, m, count)
	if m.Llama != nil {
		s.lintLlama(p, m, count)
	}
	if m.ClientHints != nil && m.ClientHints.MaxOutputTokens < 0 {
		sch("client_hints.max_output_tokens", "max_output_tokens must not be negative")
	}
}

func (s *lintState) lintRequirements(p string, r *Requirements) {
	rp := joinPath(p, "requirements")
	if len(r.Backends) == 0 {
		s.add(FileModels, CodeSchema, joinPath(rp, "backends"), "backends must list at least one of %s", quoted(backends))
	}
	for i, b := range r.Backends {
		if !oneOf(b, backends) {
			s.add(FileModels, CodeSchema, indexPath(joinPath(rp, "backends"), i), "%s", enumMsg("backend", b, backends))
		}
	}
	for _, c := range []struct {
		name string
		v    int64
	}{{"min_vram_mib", r.MinVRAMMiB}, {"min_ram_mib", r.MinRAMMiB}, {"min_disk_bytes", r.MinDiskBytes}} {
		if c.v < 0 {
			s.add(FileModels, CodeSchema, joinPath(rp, c.name), "%s must not be negative", c.name)
		}
	}
}

func (s *lintState) lintModelSource(p string, m *Model) {
	const f = FileModels
	sp := joinPath(p, "source")
	src := m.Source
	if !oneOf(src.Type, modelSrcTypes) {
		s.add(f, CodeSchema, joinPath(sp, "type"), "%s", enumMsg("source.type", src.Type, modelSrcTypes))
		return
	}
	if src.Type == "huggingface" {
		switch {
		case src.Repo == "":
			s.add(f, CodeSchema, joinPath(sp, "repo"), `missing required field "repo" (source.type is huggingface)`)
		case !repoRe.MatchString(src.Repo):
			s.add(f, CodeSchema, joinPath(sp, "repo"), "repo must look like owner/name, got %q", truncate(src.Repo, 60))
		}
		if !isHex(src.Revision, 40) {
			if src.Revision == "" {
				s.add(f, CodePinning, joinPath(sp, "revision"), "revision is required: a pinned 40-hex commit")
			} else {
				s.add(f, CodePinning, joinPath(sp, "revision"), "revision must be a 40-hex commit, got %q", truncate(src.Revision, 60))
			}
		}
		return
	}
	// type url: there is no commit to pin; a revision, if given, must still not float.
	if src.Revision != "" && !isHex(src.Revision, 40) {
		s.add(f, CodePinning, joinPath(sp, "revision"), "revision must be a 40-hex commit, got %q", truncate(src.Revision, 60))
	}
}

// lintModelFiles checks every files[*] entry and returns the number of files per kind.
func (s *lintState) lintModelFiles(p string, m *Model) map[string]int {
	const f = FileModels
	fp := joinPath(p, "files")
	count := map[string]int{}
	if len(m.Files) == 0 {
		s.add(f, CodeSchema, fp, "files must list at least one file")
	}
	for i, file := range m.Files {
		ep := indexPath(fp, i)
		if !oneOf(file.Kind, fileKinds) {
			s.add(f, CodeSchema, joinPath(ep, "kind"), "%s", enumMsg("kind", file.Kind, fileKinds))
		} else {
			count[file.Kind]++
		}
		if unsafeName(file.Name) {
			s.add(f, CodeSchema, joinPath(ep, "name"), "name %q must be a relative path without .. segments", truncate(file.Name, 60))
		}
		if m.Source.Type == "url" && !validHTTPURL(file.URL) {
			s.add(f, CodeSchema, joinPath(ep, "url"), "url must be an http(s) URL when source.type is url, got %q", truncate(file.URL, 60))
		}
		if file.SizeBytes <= 0 {
			s.add(f, CodePinning, joinPath(ep, "size_bytes"), "size_bytes is required and must be greater than 0")
		}
		s.pinnedSHA(f, joinPath(ep, "sha256"), file.SHA256)
	}
	return count
}

// lintModelFileRules are the per-engine file rules (LAI-CAT-013).
func (s *lintState) lintModelFileRules(p string, m *Model, count map[string]int) {
	const f = FileModels
	fp := joinPath(p, "files")
	exactlyOne := func(kind, engine string) {
		switch n := count[kind]; {
		case n == 0:
			s.add(f, CodeModelFiles, fp, "engine %s requires a files entry of kind %s", engine, kind)
		case n > 1:
			s.add(f, CodeModelFiles, fp, "engine %s allows exactly one files entry of kind %s, found %d", engine, kind, n)
		}
	}
	switch m.Engine {
	case "llama":
		if m.Llama == nil {
			s.add(f, CodeModelFiles, joinPath(p, "llama"), "engine llama requires a llama block")
		}
		if count["weights"] == 0 {
			s.add(f, CodeModelFiles, fp, "engine llama requires a files entry of kind weights")
		}
	case "whisper":
		exactlyOne("whisper", "whisper")
	case "piper":
		exactlyOne("piper_onnx", "piper")
		exactlyOne("piper_config", "piper")
	case EngineAcestep:
		s.lintAcestep(p, m, count)
	}
	if m.Engine != EngineAcestep && count[FileKindAcestep] > 0 {
		s.add(f, CodeAcestepLayout, fp, "files of kind %s belong to engine %s only", FileKindAcestep, EngineAcestep)
	}
}

// lintAcestep is the file layout rule of an engine acestep model (LAI-CAT-021, catalog.md section 2):
// one or more files, every one of kind acestep, each name a file or one sub-directory and a file
// ("acestep-v15-turbo/model.safetensors"); the music role; a graphics-memory requirement greater
// than 0 (it is counted in the GPU fit next to the llama models); the cuda backend only; no llama
// block.
func (s *lintState) lintAcestep(p string, m *Model, count map[string]int) {
	const f = FileModels
	fp := joinPath(p, "files")
	add := func(path, format string, args ...any) { s.add(f, CodeAcestepLayout, path, format, args...) }
	if count[FileKindAcestep] == 0 {
		add(fp, "engine acestep requires at least one files entry of kind acestep")
	}
	for i, file := range m.Files {
		ep := indexPath(fp, i)
		if file.Kind != FileKindAcestep {
			add(joinPath(ep, "kind"), "engine acestep allows only files of kind acestep, got %q", truncate(file.Kind, 40))
		}
		if !unsafeName(file.Name) && strings.Count(file.Name, "/") > 1 {
			add(joinPath(ep, "name"), "name %q may have at most one sub-directory level", truncate(file.Name, 60))
		}
	}
	if !contains(m.Roles, "music") {
		add(joinPath(p, "roles"), "engine acestep requires the role music")
	}
	if m.Requirements.MinVRAMMiB <= 0 {
		add(joinPath(p, "requirements.min_vram_mib"), "engine acestep requires min_vram_mib greater than 0")
	}
	for i, b := range m.Requirements.Backends {
		if !oneOf(b, AcestepBackends) {
			add(indexPath(joinPath(p, "requirements.backends"), i), "engine acestep runs on %s only, got %q", quoted(AcestepBackends), truncate(b, 40))
		}
	}
	if m.Llama != nil {
		add(joinPath(p, "llama"), "engine acestep takes no llama block")
	}
}

func (s *lintState) lintLlama(p string, m *Model, count map[string]int) {
	const f = FileModels
	lp := joinPath(p, "llama")
	l := m.Llama
	if l.Ctx <= 0 {
		s.add(f, CodeSchema, joinPath(lp, "ctx"), "ctx must be greater than 0")
	}
	if l.Parallel < 0 {
		s.add(f, CodeSchema, joinPath(lp, "parallel"), "parallel must not be negative")
	}
	if l.FlashAttn != "" && !oneOf(l.FlashAttn, flashAttn) {
		s.add(f, CodeSchema, joinPath(lp, "flash_attn"), "%s", enumMsg("flash_attn", l.FlashAttn, flashAttn))
	}

	// vision needs an mmproj file
	vision := contains(m.Roles, "vision")
	switch {
	case vision && l.MmprojFileKind == "":
		s.add(f, CodeModelFiles, joinPath(lp, "mmproj_file_kind"), "roles include vision, so llama.mmproj_file_kind is required (mmproj)")
	case l.MmprojFileKind != "" && count[l.MmprojFileKind] == 0:
		s.add(f, CodeModelFiles, joinPath(lp, "mmproj_file_kind"), "mmproj_file_kind %q has no matching files entry", l.MmprojFileKind)
	}

	// speculative decoding
	if sp := l.Speculative; sp != nil {
		spp := joinPath(lp, "speculative")
		if !oneOf(sp.Type, specTypes) {
			s.add(f, CodeSchema, joinPath(spp, "type"), "%s", enumMsg("speculative.type", sp.Type, specTypes))
		}
		if sp.NMax < 0 {
			s.add(f, CodeSchema, joinPath(spp, "n_max"), "n_max must not be negative")
		}
		if sp.Type == "draft" {
			if count["draft"] == 0 {
				s.add(f, CodeModelFiles, spp, "speculative.type draft requires a files entry of kind draft")
			}
			if sp.DraftFileKind != "draft" {
				s.add(f, CodeModelFiles, joinPath(spp, "draft_file_kind"), "speculative.type draft requires draft_file_kind: draft")
			}
		} else if sp.DraftFileKind != "" && count[sp.DraftFileKind] == 0 {
			s.add(f, CodeModelFiles, joinPath(spp, "draft_file_kind"), "draft_file_kind %q has no matching files entry", sp.DraftFileKind)
		}
	}

	// chat_template: either a builtin template name or the file kind chat_template
	if l.ChatTemplate == "chat_template" && count["chat_template"] == 0 {
		s.add(f, CodeModelFiles, joinPath(lp, "chat_template"), "chat_template: chat_template requires a files entry of kind chat_template")
	}

	// extra_args and env (LAI-CAT-014)
	for i, a := range l.ExtraArgs {
		if flag, bad := forbiddenArg(a); bad {
			s.add(f, CodeExtraArgs, indexPath(joinPath(lp, "extra_args"), i), "extra_args must not contain %s: the router controls it", flag)
		}
	}
	for _, k := range sortedKeys(l.Env) {
		ep := joinPath(joinPath(lp, "env"), k)
		if !envKeyRe.MatchString(k) {
			s.add(f, CodeExtraArgs, ep, "env key %q is not a valid environment variable name", truncate(k, 40))
		}
		if strings.ContainsAny(l.Env[k], "\n\r\x00") {
			s.add(f, CodeExtraArgs, ep, "env value must be a single line")
		}
	}
}

// forbiddenArg reports a llama-server argument the router owns (catalog.md section 2): --host,
// --port, --api-key*, -m, --model, --models-*, --alias and -hf, also in the --flag=value form.
func forbiddenArg(a string) (string, bool) {
	tok := strings.TrimSpace(a)
	if i := strings.IndexAny(tok, " \t="); i >= 0 {
		tok = tok[:i]
	}
	switch {
	case tok == "--host", tok == "--port", tok == "-m", tok == "--model", tok == "--alias", tok == "-hf":
		return tok, true
	case strings.HasPrefix(tok, "--api-key"):
		return "--api-key*", true
	case strings.HasPrefix(tok, "--models-"):
		return "--models-*", true
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
