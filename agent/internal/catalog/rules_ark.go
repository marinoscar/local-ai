package catalog

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Rules of catalog.md section 3 (ark.yaml): LAI-CAT-010, 011, 012, 015, 016, 017.

var (
	codenameRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	debPkgRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]*$`)
	repoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-]*$`)
)

// kindTypes says which source types each kind may use.
var kindTypes = map[string][]string{
	"kiwix": {"kiwix"},
	"maps":  {"pmtiles_extract", "url", "github_release"},
	"kit":   {"git_mirror", "apt_closure", "url", "github_release", "repo_docs"},
	"docs":  {"repo_docs"},
}

// sourceFields says which ContentSource keys each source type uses ("type" is always allowed).
var sourceFields = map[string][]string{
	"kiwix":           {"book", "base_url", "version_policy", "pinned", "checksum"},
	"url":             {"url", "sha256", "files", "version_policy", "checksum", "sidecar_url", "trust", "extract"},
	"pmtiles_extract": {"planet_url_pattern", "bbox", "maxzoom", "checksum"},
	"github_release":  {"repo", "tag", "asset", "sha256", "extract"},
	"git_mirror":      {"url", "also_tarball_of"},
	"apt_closure":     {"codename", "arch", "packages", "extra_repos"},
	"repo_docs":       {},
}

// optionalFieldKeys are the keys whose misuse is LAI-CAT-016 rather than a plain schema problem.
var optionalFieldKeys = []string{"extract", "extra_repos", "checksum", "trust", "sidecar_url", "url", "files"}

// KiwixMinRefreshDays is the minimum refresh_days of a kiwix item that follows the latest file.
const KiwixMinRefreshDays = 30

func (s *lintState) lintArk(pf *parsedFile[ArkFile], ids map[string]string) {
	items := pf.Value.Content
	kinds := map[string]string{}
	for i := range items {
		s.checkID(FileArk, indexPath("content", i), items[i].ID, ids)
		kinds[items[i].ID] = items[i].Kind
	}
	portalKeys := map[string]string{}
	for i := range items {
		p := indexPath("content", i)
		if pf.Dirty[p] {
			continue
		}
		s.lintItem(p, &items[i])
		s.lintPortalLinks(p, &items[i], kinds, len(pf.Dirty) == 0, portalKeys)
	}
}

func (s *lintState) lintItem(p string, it *ContentItem) {
	const f = FileArk
	if !oneOf(it.Kind, contentKinds) {
		s.add(f, CodeSchema, joinPath(p, "kind"), "%s", enumMsg("kind", it.Kind, contentKinds))
	}
	if it.SurvivalTier != 0 && (it.SurvivalTier < 1 || it.SurvivalTier > 3) {
		s.add(f, CodeSchema, joinPath(p, "survival_tier"), "survival_tier must be 1, 2 or 3, got %d", it.SurvivalTier)
	} else if (it.Kind == "kit" || it.Kind == "docs") && it.SurvivalTier > 1 {
		s.add(f, CodeArkItem, joinPath(p, "survival_tier"), "a %s item is always survival tier 1, got %d", it.Kind, it.SurvivalTier)
	}
	if it.ExpectedSizeBytes < 0 {
		s.add(f, CodeSchema, joinPath(p, "expected_size_bytes"), "expected_size_bytes must not be negative")
	}
	if it.RefreshDays < 0 {
		s.add(f, CodeSchema, joinPath(p, "refresh_days"), "refresh_days must not be negative")
	}
	s.lintKitID(p, it)
	s.lintSource(p, it)
}

// lintKitID checks that kit-debs-<codename>-<arch> and kit-nvidia-<codename>-<arch>-<branch> ids
// agree with source.codename and source.arch (catalog.md section 3).
func (s *lintState) lintKitID(p string, it *ContentItem) {
	idp := joinPath(p, "id")
	for _, prefix := range []string{"kit-debs-", "kit-nvidia-"} {
		if !strings.HasPrefix(it.ID, prefix) {
			continue
		}
		if it.Kind != "kit" || it.Source.Type != "apt_closure" {
			s.add(FileArk, CodeArkItem, idp, "ids starting with %s are for kind: kit items with an apt_closure source", prefix)
			return
		}
		cn, arch := it.Source.Codename, it.Source.Arch
		if cn == "" || arch == "" {
			return // reported as missing required fields
		}
		want := prefix + cn + "-" + arch
		if prefix == "kit-debs-" && it.ID != want {
			s.add(FileArk, CodeArkItem, idp, "id %q must be kit-debs-<codename>-<arch> and agree with source.codename %q and source.arch %q (expected %q)", it.ID, cn, arch, want)
		}
		if prefix == "kit-nvidia-" && (!strings.HasPrefix(it.ID, want+"-") || len(it.ID) == len(want)+1) {
			s.add(FileArk, CodeArkItem, idp, "id %q must be kit-nvidia-<codename>-<arch>-<branch> and agree with source.codename %q and source.arch %q (expected %q-<branch>)", it.ID, cn, arch, want)
		}
		return
	}
}

func (s *lintState) lintSource(p string, it *ContentItem) {
	const f = FileArk
	src := &it.Source
	sp := joinPath(p, "source")
	if !oneOf(src.Type, arkSrcTypes) {
		s.add(f, CodeSchema, joinPath(sp, "type"), "%s", enumMsg("source.type", src.Type, arkSrcTypes))
		return
	}
	if allowed, ok := kindTypes[it.Kind]; ok && !contains(allowed, src.Type) {
		s.add(f, CodeArkItem, joinPath(sp, "type"), "source.type %s is not used by kind %s (use %s)", src.Type, it.Kind, quoted(allowed))
	}
	s.checkAllowedFields(sp, src)
	switch src.Type {
	case "kiwix":
		s.lintKiwix(p, it)
	case "url":
		s.lintURLSource(p, it)
	case "pmtiles_extract":
		s.lintPMTiles(sp, src)
	case "github_release":
		s.needRepo(sp, src.Repo)
		s.need(sp, "tag", src.Tag)
		s.need(sp, "asset", src.Asset)
		s.pinnedSHA(f, joinPath(sp, "sha256"), src.SHA256)
	case "git_mirror":
		s.needURL(sp, "url", src.URL)
		if src.AlsoTarballOf != "" && src.AlsoTarballOf != "pinned" {
			s.add(f, CodeSchema, joinPath(sp, "also_tarball_of"), `also_tarball_of must be "pinned", got %q`, truncate(src.AlsoTarballOf, 40))
		}
	case "apt_closure":
		s.lintAPT(sp, src)
	}
}

// checkAllowedFields rejects keys the source type does not use. Misusing one of the optional
// fields (extract, extra_repos, checksum, trust, sidecar_url, url, files) is LAI-CAT-016.
func (s *lintState) checkAllowedFields(sp string, src *ContentSource) {
	allowed := sourceFields[src.Type]
	v := reflect.ValueOf(src).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "type" || contains(allowed, name) {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Map:
			if fv.IsNil() {
				continue
			}
		default:
			if fv.IsZero() {
				continue
			}
		}
		code, msg := CodeSchema, fmt.Sprintf("%s is not used by source.type %s", name, src.Type)
		if contains(optionalFieldKeys, name) {
			code = CodeOptionalField
			switch name {
			case "extract":
				msg = "extract is only allowed on github_release and url items"
			case "extra_repos":
				msg = "extra_repos is only allowed on apt_closure items"
			default:
				msg = fmt.Sprintf("%s is not allowed on a %s item", name, src.Type)
			}
		}
		s.add(FileArk, code, joinPath(sp, name), "%s", msg)
	}
}

func (s *lintState) need(sp, key, val string) {
	if val == "" {
		s.add(FileArk, CodeSchema, joinPath(sp, key), "missing required field %q", key)
	}
}

func (s *lintState) needURL(sp, key, val string) {
	switch {
	case val == "":
		s.need(sp, key, val)
	case !validHTTPURL(val):
		s.add(FileArk, CodeSchema, joinPath(sp, key), "%s must be an http(s) URL, got %q", key, truncate(val, 60))
	}
}

func (s *lintState) needRepo(sp, repo string) {
	switch {
	case repo == "":
		s.need(sp, "repo", repo)
	case !repoRe.MatchString(repo):
		s.add(FileArk, CodeSchema, joinPath(sp, "repo"), "repo must look like owner/name, got %q", truncate(repo, 60))
	}
}

func (s *lintState) lintKiwix(p string, it *ContentItem) {
	const f = FileArk
	sp := joinPath(p, "source")
	src := &it.Source
	s.need(sp, "book", src.Book)
	s.needURL(sp, "base_url", src.BaseURL)
	switch {
	case src.VersionPolicy == "":
		s.need(sp, "version_policy", "")
	case !oneOf(src.VersionPolicy, versionPolicy):
		s.add(f, CodeSchema, joinPath(sp, "version_policy"), "%s", enumMsg("version_policy", src.VersionPolicy, versionPolicy))
	}
	switch {
	case src.Checksum == "":
		s.need(sp, "checksum", "")
	case !oneOf(src.Checksum, kiwixChecksums):
		s.add(f, CodeSchema, joinPath(sp, "checksum"), "%s", enumMsg("checksum", src.Checksum, kiwixChecksums))
	}
	if src.VersionPolicy == "latest" && it.RefreshDays < KiwixMinRefreshDays {
		s.add(f, CodeArkItem, joinPath(p, "refresh_days"), "a kiwix item with version_policy: latest needs refresh_days >= %d, got %d", KiwixMinRefreshDays, it.RefreshDays)
	}
	if (src.VersionPolicy == "pinned" || src.Checksum == "pinned") && src.Pinned == nil {
		s.add(f, CodeArkItem, joinPath(sp, "pinned"), "version_policy: pinned and checksum: pinned need a pinned block {file, sha256}")
	}
	if src.Pinned != nil {
		s.pinnedSHA(f, joinPath(joinPath(sp, "pinned"), "sha256"), src.Pinned.SHA256)
	}
}

func (s *lintState) lintURLSource(p string, it *ContentItem) {
	const f = FileArk
	sp := joinPath(p, "source")
	src := &it.Source
	hasURL, hasFiles := src.URL != "", src.Files != nil
	switch {
	case hasURL && hasFiles:
		s.add(f, CodeOptionalField, joinPath(sp, "files"), "use either url + sha256 or a files list, not both")
	case !hasURL && !hasFiles:
		s.add(f, CodeOptionalField, sp, "a url item needs url + sha256, or a files list")
	case hasFiles && len(src.Files) == 0:
		s.add(f, CodeOptionalField, joinPath(sp, "files"), "files must list at least one entry")
	}
	if hasFiles && src.SHA256 != "" {
		s.add(f, CodeOptionalField, joinPath(sp, "sha256"), "sha256 goes with url; with a files list each entry carries its own")
	}
	if hasURL && !validHTTPURL(src.URL) {
		s.add(f, CodeSchema, joinPath(sp, "url"), "url must be an http(s) URL, got %q", truncate(src.URL, 60))
	}
	names := map[string]bool{}
	for i, file := range src.Files {
		fp := indexPath(joinPath(sp, "files"), i)
		if !validHTTPURL(file.URL) {
			s.add(f, CodeSchema, joinPath(fp, "url"), "url must be an http(s) URL, got %q", truncate(file.URL, 60))
		}
		if file.Name != "" && !manifest.SafePath(file.Name) {
			s.add(f, CodeSchema, joinPath(fp, "name"), "name must be a clean relative path inside the item folder (not SHA256SUMS or MANIFEST.json), got %q", truncate(file.Name, 60))
		}
		// Two files of one item cannot share a name, written or taken from the URL.
		if n := file.ItemPath(); n != "" {
			if names[n] {
				s.add(f, CodeSchema, joinPath(fp, "url"), "the file name %q is used twice in this item (give one of them a name)", truncate(n, 60))
			}
			names[n] = true
		}
	}
	if src.VersionPolicy != "" && !oneOf(src.VersionPolicy, versionPolicy) {
		s.add(f, CodeSchema, joinPath(sp, "version_policy"), "%s", enumMsg("version_policy", src.VersionPolicy, versionPolicy))
	}

	checksum := src.Checksum
	if checksum == "" {
		checksum = "pinned"
	}
	if !oneOf(checksum, urlChecksums) {
		s.add(f, CodeSchema, joinPath(sp, "checksum"), "%s", enumMsg("checksum", checksum, urlChecksums))
		return
	}
	check := s.optionalSHA
	if checksum == "pinned" {
		check = s.pinnedSHA
	}
	if hasURL {
		check(f, joinPath(sp, "sha256"), src.SHA256)
	}
	for i, file := range src.Files {
		check(f, joinPath(indexPath(joinPath(sp, "files"), i), "sha256"), file.SHA256)
	}

	// sidecar
	switch {
	case checksum == "sidecar" && src.SidecarURL == "":
		s.add(f, CodeOptionalField, joinPath(sp, "sidecar_url"), "checksum: sidecar requires sidecar_url")
	case checksum == "sidecar" && !validHTTPURL(src.SidecarURL):
		s.add(f, CodeSchema, joinPath(sp, "sidecar_url"), "sidecar_url must be an http(s) URL, got %q", truncate(src.SidecarURL, 60))
	case checksum != "sidecar" && src.SidecarURL != "":
		s.add(f, CodeOptionalField, joinPath(sp, "sidecar_url"), "sidecar_url is only used with checksum: sidecar")
	}

	// trust on first use
	tp := joinPath(sp, "trust")
	switch {
	case src.Trust != "" && src.Trust != "tofu":
		s.add(f, CodeOptionalField, tp, `trust must be "tofu", got %q`, truncate(src.Trust, 40))
	case checksum == "computed" && src.Trust != "tofu":
		s.add(f, CodeOptionalField, tp, "checksum: computed requires trust: tofu")
	case checksum != "computed" && src.Trust != "":
		s.add(f, CodeOptionalField, tp, "trust is only allowed with checksum: computed")
	case src.Trust == "tofu" && it.Kind != "maps":
		s.add(f, CodeOptionalField, tp, "trust: tofu is allowed only on kind: maps items")
	}
}

func (s *lintState) lintPMTiles(sp string, src *ContentSource) {
	const f = FileArk
	s.needURL(sp, "planet_url_pattern", src.PlanetURLPattern)
	bp := joinPath(sp, "bbox")
	switch {
	case len(src.BBox) == 0:
		s.add(f, CodeSchema, bp, `missing required field "bbox"`)
	case len(src.BBox) != 4:
		s.add(f, CodeArkItem, bp, "bbox must be [minLon, minLat, maxLon, maxLat], got %d numbers", len(src.BBox))
	default:
		b := src.BBox
		if b[0] >= b[2] || b[1] >= b[3] {
			s.add(f, CodeArkItem, bp, "bbox needs min < max on both axes: minLon %v < maxLon %v and minLat %v < maxLat %v", b[0], b[2], b[1], b[3])
		}
		if b[0] < -180 || b[2] > 180 || b[1] < -90 || b[3] > 90 {
			s.add(f, CodeArkItem, bp, "bbox is outside longitude -180..180 and latitude -90..90")
		}
	}
	switch {
	case src.MaxZoom == nil:
		s.add(f, CodeSchema, joinPath(sp, "maxzoom"), `missing required field "maxzoom"`)
	case *src.MaxZoom < 0 || *src.MaxZoom > 24:
		s.add(f, CodeSchema, joinPath(sp, "maxzoom"), "maxzoom must be between 0 and 24, got %d", *src.MaxZoom)
	}
	switch {
	case src.Checksum == "":
		s.need(sp, "checksum", "")
	case src.Checksum != "computed":
		s.add(f, CodeOptionalField, joinPath(sp, "checksum"), "a pmtiles_extract item uses checksum: computed, got %q", truncate(src.Checksum, 40))
	}
}

func (s *lintState) lintAPT(sp string, src *ContentSource) {
	const f = FileArk
	switch {
	case src.Codename == "":
		s.need(sp, "codename", "")
	case !codenameRe.MatchString(src.Codename):
		s.add(f, CodeSchema, joinPath(sp, "codename"), "codename must be a lowercase Ubuntu codename, got %q", truncate(src.Codename, 40))
	}
	switch {
	case src.Arch == "":
		s.need(sp, "arch", "")
	case !oneOf(src.Arch, archs):
		s.add(f, CodeSchema, joinPath(sp, "arch"), "%s", enumMsg("arch", src.Arch, archs))
	}
	pp := joinPath(sp, "packages")
	if len(src.Packages) == 0 {
		s.add(f, CodeSchema, pp, "packages must list at least one package")
	}
	for i, pkg := range src.Packages {
		if !debPkgRe.MatchString(pkg) {
			s.add(f, CodeSchema, indexPath(pp, i), "%q is not a Debian package name", truncate(pkg, 40))
		}
	}
	seen := map[string]bool{}
	for i, r := range src.ExtraRepos {
		rp := indexPath(joinPath(sp, "extra_repos"), i)
		switch {
		case !repoNameRe.MatchString(r.Name):
			s.add(f, CodeOptionalField, joinPath(rp, "name"), "extra_repos name %q must be a lowercase slug", truncate(r.Name, 40))
		case seen[r.Name]:
			s.add(f, CodeOptionalField, joinPath(rp, "name"), "duplicate extra_repos name %q", r.Name)
		}
		seen[r.Name] = true
		if !validHTTPURL(r.URI) {
			s.add(f, CodeSchema, joinPath(rp, "uri"), "uri must be an http(s) URL, got %q", truncate(r.URI, 60))
		}
		if !contains(EmbeddedKeyrings, r.Key) {
			s.add(f, CodeOptionalField, joinPath(rp, "key"), "key %q is not a keyring embedded in the agent (known: %s)", truncate(r.Key, 50), quoted(EmbeddedKeyrings))
		}
	}
}

func (s *lintState) lintPortalLinks(p string, it *ContentItem, kinds map[string]string, kindsComplete bool, keys map[string]string) {
	const f = FileArk
	if it.PortalLinks == nil {
		return
	}
	pl := joinPath(p, "portal_links")
	if it.Kind != "kiwix" {
		s.add(f, CodePortalLinks, pl, "portal_links is only allowed on kiwix items")
		return
	}
	for i, l := range it.PortalLinks {
		lp := indexPath(pl, i)
		switch {
		case l.Key == "":
			s.add(f, CodePortalLinks, joinPath(lp, "key"), "key is required")
		default:
			if first, dup := keys[l.Key]; dup {
				s.add(f, CodePortalLinks, joinPath(lp, "key"), "duplicate portal link key %q (first at %s)", l.Key, first)
			} else {
				keys[l.Key] = f + ":" + joinPath(lp, "key")
			}
		}
		if strings.TrimSpace(l.Article) == "" {
			s.add(f, CodePortalLinks, joinPath(lp, "article"), "article must not be empty")
		}
		switch {
		case l.ESItem != "":
			kind, ok := kinds[l.ESItem]
			switch {
			case !ok && !kindsComplete:
				// the file did not decode completely: do not guess
			case !ok:
				s.add(f, CodePortalLinks, joinPath(lp, "es_item"), "es_item %q is not an item of ark.yaml", truncate(l.ESItem, 70))
			case kind != "kiwix":
				s.add(f, CodePortalLinks, joinPath(lp, "es_item"), "es_item %q is not a kiwix item", l.ESItem)
			}
			if strings.TrimSpace(l.ESArticle) == "" {
				s.add(f, CodePortalLinks, joinPath(lp, "es_article"), "es_article is required when es_item is set")
			}
		case l.ESArticle != "":
			s.add(f, CodePortalLinks, joinPath(lp, "es_article"), "es_article needs es_item")
		}
	}
}
