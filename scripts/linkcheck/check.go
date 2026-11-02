package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding is one broken link.
type Finding struct {
	File   string // repo-relative, slash separated
	Line   int
	Target string
	Reason string
}

func (f Finding) String() string {
	return fmt.Sprintf("BROKEN %s:%d -> %s (%s)", f.File, f.Line, f.Target, f.Reason)
}

// Checker resolves relative links against a repository root.
type Checker struct {
	Root  string
	cache map[string]*Doc // repo-relative path -> parsed doc
}

func NewChecker(root string) *Checker {
	return &Checker{Root: root, cache: map[string]*Doc{}}
}

var reScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

func isExternal(t string) bool {
	l := strings.ToLower(t)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "//")
}

func (c *Checker) doc(rel string) (*Doc, error) {
	if d, ok := c.cache[rel]; ok {
		return d, nil
	}
	b, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	d := parseMarkdown(string(b))
	c.cache[rel] = &d
	return &d, nil
}

// resolve joins a link path to the directory of the file that contains it.
// It returns the repo-relative slash path, or ok=false if it escapes the repository.
func resolve(fromFile, linkPath string) (string, bool) {
	var p string
	if strings.HasPrefix(linkPath, "/") {
		p = strings.TrimPrefix(linkPath, "/") // GitHub: root-relative
	} else {
		p = filepath.ToSlash(filepath.Join(filepath.Dir(filepath.FromSlash(fromFile)), filepath.FromSlash(linkPath)))
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// CheckLink validates one relative target found in `file`. External and non-file
// schemes return ("", true). A non-empty reason means the link is broken.
func (c *Checker) CheckLink(file, target string) (reason string) {
	t := strings.TrimSpace(target)
	if t == "" || isExternal(t) || reScheme.MatchString(t) {
		return ""
	}
	p, frag := t, ""
	if i := strings.Index(t, "#"); i >= 0 {
		p, frag = t[:i], t[i+1:]
	}
	if i := strings.Index(p, "?"); i >= 0 {
		p = p[:i]
	}
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	if d, err := url.PathUnescape(frag); err == nil {
		frag = d
	}

	targetRel := file
	if p != "" {
		var ok bool
		if targetRel, ok = resolve(file, p); !ok {
			return "path escapes the repository"
		}
	}
	info, err := os.Stat(filepath.Join(c.Root, filepath.FromSlash(targetRel)))
	if err != nil {
		return "file not found"
	}
	if frag == "" || info.IsDir() || !strings.EqualFold(filepath.Ext(targetRel), ".md") {
		return ""
	}
	if strings.HasPrefix(frag, "L") && regexp.MustCompile(`^L\d+(-L\d+)?$`).MatchString(frag) {
		return "" // GitHub line anchors
	}
	d, err := c.doc(targetRel)
	if err != nil {
		return "file not readable"
	}
	if !d.Anchors[strings.ToLower(frag)] && !d.Anchors[frag] {
		return "anchor not found in " + targetRel
	}
	return ""
}

// CheckFiles checks every markdown file (repo-relative paths) and returns findings
// plus the external URLs seen (url -> first locations), and the link count.
func (c *Checker) CheckFiles(files []string) (findings []Finding, external map[string][]string, links int) {
	external = map[string][]string{}
	for _, f := range files {
		d, err := c.doc(f)
		if err != nil {
			findings = append(findings, Finding{f, 0, f, "file not readable"})
			continue
		}
		for _, l := range d.Links {
			links++
			t := strings.TrimSpace(l.Target)
			if isExternal(t) {
				u := t
				if strings.HasPrefix(u, "//") {
					u = "https:" + u
				}
				if i := strings.Index(u, "#"); i >= 0 {
					u = u[:i]
				}
				external[u] = append(external[u], fmt.Sprintf("%s:%d", f, l.Line))
				continue
			}
			if reason := c.CheckLink(f, t); reason != "" {
				findings = append(findings, Finding{f, l.Line, l.Target, reason})
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, external, links
}
