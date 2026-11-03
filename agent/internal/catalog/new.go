package catalog

// New builds a Catalog from values already in memory: a host that composes a catalog itself and the
// tests of the packages that read one (the sync planner builds tiny synthetic catalogs with exact
// sizes). It does not lint, so the caller owns the invariants Load guarantees (unique ids, profiles
// that reference known ids). The agent itself only ever reads the catalog through Load.
func New(version string, models []Model, content []ContentItem, profiles []Profile) *Catalog {
	return newCatalog(version, models, content, profiles)
}
