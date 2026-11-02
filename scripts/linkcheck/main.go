// Command linkcheck verifies that every relative link in the repository's markdown
// files resolves to a file, and that every #anchor matches a heading (GitHub slug rules).
// External https:// links are only fetched with --external.
//
// Usage: linkcheck [--root DIR] [--external]
// Exit:  0 all links ok, 1 broken links found, 2 usage or I/O error.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func listMarkdown(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "--", "*.md")
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				if _, err := os.Stat(filepath.Join(root, l)); err == nil {
					files = append(files, l)
				}
			}
		}
		sort.Strings(files)
		return files, nil
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".md") {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func main() {
	root := flag.String("root", ".", "repository root")
	external := flag.Bool("external", false, "also fetch external http(s) links (slow; for the weekly job)")
	flag.Parse()

	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "linkcheck:", err)
		os.Exit(2)
	}
	files, err := listMarkdown(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "linkcheck:", err)
		os.Exit(2)
	}
	c := NewChecker(abs)
	findings, ext, links := c.CheckFiles(files)
	for _, f := range findings {
		fmt.Println(f)
	}
	broken := len(findings)

	if *external {
		for _, r := range CheckExternal(ext, newClient(), 8) {
			if r.Warning == "" {
				continue
			}
			where := r.Where[0]
			if r.Broken {
				fmt.Printf("BROKEN %s -> %s (%s)\n", where, r.URL, r.Warning)
				broken++
			} else {
				fmt.Printf("WARN %s -> %s (%s)\n", where, r.URL, r.Warning)
			}
		}
	}

	if broken > 0 {
		fmt.Fprintf(os.Stderr, "✗ %d broken link(s) in %d markdown files (%d links checked)\n", broken, len(files), links)
		os.Exit(1)
	}
	fmt.Printf("✓ Link check passed (%d markdown files, %d links)\n", len(files), links)
}
