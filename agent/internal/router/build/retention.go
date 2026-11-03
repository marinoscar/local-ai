package build

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// retain applies the retention of llama-router.md section 4.10.3 step 7 after a good build: current,
// the previous build and the newest other build stay; older builds go (keep is the number of builds
// kept besides current, so the default 2 keeps exactly those three). Leftovers of failed or
// interrupted builds (<id>.tmp, <id>.tmp.failed) go too, because a later good build supersedes
// them. Of the source trees, those of the last 2 commits (by last use) and the one just built stay.
// protect names builds that must stay whatever their age; the build that current points at is always
// added. A deletion that fails is a warning, never an error: the build itself succeeded.
func (m *Manager) retain(keep int, plan Plan, protect []string) (removed, warnings []string) {
	fsys := m.opt.FS
	keep = max(keep, 1)
	warn := func(what string, err error) {
		m.log.Warn("retention could not remove "+what, "error", err)
		warnings = append(warnings, "Could not remove "+what+": "+err.Error())
	}
	remove := func(dir string) {
		if err := fsys.RemoveAll(dir); err != nil {
			warn(dir, err)
			return
		}
		removed = append(removed, dir)
	}

	protected := map[string]bool{}
	for _, p := range protect {
		if p != "" {
			protected[p] = true
		}
	}
	if t, err := m.currentTarget(); err == nil && t != "" {
		protected[buildName(t)] = true
	}

	builds := filepath.Join(m.root, buildsDir)
	type candidate struct {
		name string
		at   time.Time
	}
	var kept, others []candidate
	entries, err := fsys.ReadDir(builds)
	if err != nil {
		warn(builds, err)
	}
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(builds, name)
		switch {
		case !e.IsDir():
			continue
		case strings.HasSuffix(name, suffixTmp) || strings.HasSuffix(name, suffixFailed):
			remove(dir)
			continue
		}
		raw, err := fsys.ReadFile(filepath.Join(dir, buildJSON))
		if err != nil {
			continue // not a build of ours: leave it alone
		}
		c := candidate{name: name}
		var b struct {
			BuiltAt string `json:"built_at"`
		}
		if json.Unmarshal(raw, &b) == nil {
			c.at, _ = time.Parse(time.RFC3339, b.BuiltAt)
		}
		if c.at.IsZero() {
			if fi, err := e.Info(); err == nil {
				c.at = fi.ModTime()
			}
		}
		if protected[name] {
			kept = append(kept, c)
		} else {
			others = append(others, c)
		}
	}
	sort.Slice(others, func(i, j int) bool {
		if !others[i].at.Equal(others[j].at) {
			return others[i].at.After(others[j].at)
		}
		return others[i].name > others[j].name
	})
	slots := max(0, keep+1-len(kept))
	for i, c := range others {
		if i >= slots {
			remove(filepath.Join(builds, c.name))
		}
	}

	m.retainSources(plan.Commit, remove, warn)
	return removed, warnings
}

// retainSources keeps the source trees of the last KeepSources commits by last use (the tree of a
// build is touched when the build starts) and the tree of the commit just built.
func (m *Manager) retainSources(built string, remove func(string), warn func(string, error)) {
	srcRoot := filepath.Join(m.root, srcDir)
	entries, err := m.opt.FS.ReadDir(srcRoot)
	if err != nil {
		return
	}
	type tree struct {
		name string
		at   time.Time
	}
	var trees []tree
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, suffixTmp) || strings.HasSuffix(name, suffixTmp+".tar"):
			remove(filepath.Join(srcRoot, name))
		case e.IsDir() && fullSHARe.MatchString(name):
			fi, err := e.Info()
			if err != nil {
				warn(filepath.Join(srcRoot, name), err)
				continue
			}
			trees = append(trees, tree{name, fi.ModTime()})
		}
	}
	sort.Slice(trees, func(i, j int) bool {
		if !trees[i].at.Equal(trees[j].at) {
			return trees[i].at.After(trees[j].at)
		}
		return trees[i].name > trees[j].name
	})
	for i, t := range trees {
		if i >= KeepSources && t.name != built {
			remove(filepath.Join(srcRoot, t.name))
		}
	}
}
