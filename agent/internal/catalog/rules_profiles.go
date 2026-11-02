package catalog

import (
	"math"
	"sort"
)

// Rules of catalog.md section 4 (profiles): LAI-CAT-004, 010, 012, 018, 019.

func (s *lintState) lintProfiles(docs []profileDoc, models *parsedFile[ModelsFile], ark *parsedFile[ArkFile]) {
	var modelIDs, contentIDs map[string]bool
	// A file with schema problems may have decoded only partly (yaml.v3 stops at some errors), so
	// its ids are not trusted: cross references to it are skipped until its schema is clean.
	if models != nil && len(models.Dirty) == 0 {
		modelIDs = map[string]bool{}
		for _, m := range models.Value.Models {
			modelIDs[m.ID] = true
		}
	}
	if ark != nil && len(ark.Dirty) == 0 {
		contentIDs = map[string]bool{}
		for _, c := range ark.Value.Content {
			contentIDs[c.ID] = true
		}
	}
	seen := map[string]string{}
	var node []profileDoc // profiles with a match, for the overlap rule
	for _, d := range docs {
		if d.pf == nil {
			continue
		}
		p := d.pf.Value
		// id: format, uniqueness among profiles, and agreement with the file name (layout)
		if p.ID != "" {
			if !idRe.MatchString(p.ID) {
				s.add(d.rel, CodeID, "id", "id %q must match %s", truncate(p.ID, 70), idFormat)
			}
			if first, dup := seen[p.ID]; dup {
				s.add(d.rel, CodeID, "id", "duplicate profile id %q (first defined in %s)", p.ID, first)
			}
			seen[p.ID] = d.rel
			if p.ID != d.stem {
				s.add(d.rel, CodeLayout, "id", "profile id %q must match the file name (profiles/%s.yaml)", truncate(p.ID, 70), truncate(p.ID, 70))
			}
		}
		if d.pf.Dirty[""] {
			continue // a schema problem was reported; do not pile rules on top
		}
		s.lintProfile(d, modelIDs, contentIDs)
		if p.Match != nil && p.ID != CPUOnlyProfileID && p.ID != ArkDriveProfileID {
			node = append(node, d)
		}
	}
	s.lintOverlaps(node)
}

func (s *lintState) lintProfile(d profileDoc, modelIDs, contentIDs map[string]bool) {
	f := d.rel
	p := d.pf.Value
	add := func(code, path, format string, args ...any) { s.add(f, code, path, format, args...) }

	// router
	if p.Router.ModelsMax < 1 {
		add(CodeProfile, "router.models_max", "router.models_max must be at least 1, got %d", p.Router.ModelsMax)
	}
	if p.Router.MemoryMarginMiB < 0 {
		add(CodeSchema, "router.memory_margin_mib", "memory_margin_mib must not be negative")
	}
	if p.Router.Autoload != nil && *p.Router.Autoload {
		add(CodeProfile, "router.autoload", "router.autoload must be false: the unit always passes --no-models-autoload and the agent loads models explicitly")
	}

	// storage
	if !oneOf(p.Storage.Target, storageTargets) {
		add(CodeSchema, "storage.target", "%s", enumMsg("storage.target", p.Storage.Target, storageTargets))
	}
	if p.Storage.BudgetBytes <= 0 {
		add(CodeSchema, "storage.budget_bytes", "budget_bytes must be greater than 0")
	}
	if p.Storage.MinFreeBytes < 0 {
		add(CodeSchema, "storage.min_free_bytes", "min_free_bytes must not be negative")
	}

	// match: ark-drive-full has none, every other profile needs one
	switch {
	case p.ID == ArkDriveProfileID && p.Match != nil:
		add(CodeProfile, "match", "%s has no match: it is never selected automatically", ArkDriveProfileID)
	case p.ID != ArkDriveProfileID && p.Match == nil:
		add(CodeProfile, "match", "a node profile needs a match block (only %s has none)", ArkDriveProfileID)
	}
	if m := p.Match; m != nil {
		for i, b := range m.Backends {
			if !oneOf(b, backends) {
				add(CodeSchema, indexPath("match.backends", i), "%s", enumMsg("backend", b, backends))
			}
		}
		s.lintRange(f, "match.vram_mib", m.VRAMMiB)
		s.lintRange(f, "match.ram_mib", m.RAMMiB)
	}

	// cross references (LAI-CAT-004) and "inside include" (LAI-CAT-018)
	inInclude := map[string]bool{}
	for _, id := range p.Include.Models {
		inInclude[id] = true
	}
	if modelIDs != nil {
		for i, id := range p.Include.Models {
			if !modelIDs[id] {
				add(CodeUnknownRef, indexPath("include.models", i), "unknown model id %q%s", truncate(id, 70), hint(id, contentIDs, "a content id"))
			}
		}
	}
	if contentIDs != nil {
		for i, id := range p.Include.Content {
			if !contentIDs[id] {
				add(CodeUnknownRef, indexPath("include.content", i), "unknown content id %q%s", truncate(id, 70), hint(id, modelIDs, "a model id"))
			}
		}
	}
	s.modelRef(f, "default_model", p.DefaultModel, modelIDs, inInclude)
	for i, id := range p.Survival.Models {
		s.modelRef(f, indexPath("survival.models", i), id, modelIDs, inInclude)
	}
	s.modelRef(f, "survival.default_model", p.Survival.DefaultModel, modelIDs, inInclude)
}

// hint says when an unknown id exists in the other namespace.
func hint(id string, other map[string]bool, what string) string {
	if other[id] {
		return " (it is " + what + ")"
	}
	return ""
}

// modelRef checks that a model reference exists (LAI-CAT-004) and is inside include.models (LAI-CAT-018).
func (s *lintState) modelRef(file, path, id string, modelIDs, inInclude map[string]bool) {
	switch {
	case id == "":
		return
	case modelIDs != nil && !modelIDs[id]:
		s.add(file, CodeUnknownRef, path, "unknown model id %q", truncate(id, 70))
	case !inInclude[id]:
		s.add(file, CodeProfile, path, "%q must also be listed in include.models", truncate(id, 70))
	}
}

func (s *lintState) lintRange(file, path string, r *Range) {
	if r == nil {
		return
	}
	if r.Min != nil && *r.Min < 0 {
		s.add(file, CodeProfile, path+".min", "min must not be negative")
	}
	if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
		s.add(file, CodeProfile, path, "min %d is greater than max %d", *r.Min, *r.Max)
	}
}

// lintOverlaps rejects two node profiles whose match ranges overlap (same backend and intersecting
// vram_mib and ram_mib ranges). cpu-only is exempt: it is the catch-all, always evaluated last.
func (s *lintState) lintOverlaps(docs []profileDoc) {
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].pf.Value.ID < docs[j].pf.Value.ID })
	for j := 1; j < len(docs); j++ {
		for i := 0; i < j; i++ {
			a, b := docs[i].pf.Value, docs[j].pf.Value
			if matchOverlap(a.Match, b.Match) {
				s.add(docs[j].rel, CodeProfile, "match",
					"match overlaps profile %q (same backend and intersecting vram_mib and ram_mib ranges)", a.ID)
			}
		}
	}
}

func matchOverlap(a, b *Match) bool {
	if !backendsIntersect(a.Backends, b.Backends) {
		return false
	}
	return rangesIntersect(a.VRAMMiB, b.VRAMMiB) && rangesIntersect(a.RAMMiB, b.RAMMiB)
}

// backendsIntersect: an empty list means any backend.
func backendsIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

func bounds(r *Range) (lo, hi float64) {
	lo, hi = math.Inf(-1), math.Inf(1)
	if r == nil {
		return
	}
	if r.Min != nil {
		lo = float64(*r.Min)
	}
	if r.Max != nil {
		hi = float64(*r.Max)
	}
	return
}

func rangesIntersect(a, b *Range) bool {
	alo, ahi := bounds(a)
	blo, bhi := bounds(b)
	return math.Max(alo, blo) <= math.Min(ahi, bhi)
}
