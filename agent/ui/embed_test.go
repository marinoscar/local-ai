package ui

import (
	"io/fs"
	"testing"
)

// The dist folder always exists in the tree (go:embed needs it); portal.html is there only after
// `npm run build`. Both cases must be a usable FS, because the agent serves its LAI-SVC-100 page
// when the Portal is missing.
func TestDistIsAnFS(t *testing.T) {
	fsys := Dist()
	if fsys == nil {
		t.Fatal("Dist() is nil")
	}
	if _, err := fs.Stat(fsys, "."); err != nil {
		t.Fatalf("stat root: %v", err)
	}
}
