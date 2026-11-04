// Package ui embeds the built Portal (agent/ui/dist, written by `npm run build`) into the agent
// binary (survival-portal.md section 4.11: everything the Portal needs is on the agent's own disk).
//
// The dist folder always exists in the tree (a .gitkeep keeps go:embed happy). When the UI was not
// built, portal.html is missing and the agent answers its built-in emergency page (LAI-SVC-100).
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the embedded build, rooted at dist: portal.html, index.html and assets/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil { // cannot happen: dist is embedded
		return dist
	}
	return sub
}
