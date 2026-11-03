package keys

import (
	"embed"
	"io/fs"
	"path"
	"sort"
)

// The pattern is a plain "*" on purpose: an embed pattern that matches no file is a build error, and the
// production key is added by the owner after this package exists (release-signing.md section 3). With "*"
// the package always builds, and Files keeps only the key files; the embedded key list is simply empty until
// then (a self-update then fails closed, see internal/update).
//
//go:embed *
var embedded embed.FS

// File is one embedded key file.
type File struct {
	Name string
	Data []byte
}

// Files returns the contents of every embedded public key file in name order. A key file is minisign.pub, or
// a file named minisign*.pub (the bridge key of a planned rotation, release-signing.md section 5). It
// returns nothing while the owner has not committed a key.
func Files() []File {
	entries, err := fs.ReadDir(embedded, ".")
	if err != nil {
		return nil
	}
	var out []File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isKeyFile(name) {
			continue
		}
		data, err := fs.ReadFile(embedded, name)
		if err != nil {
			continue
		}
		out = append(out, File{Name: name, Data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func isKeyFile(name string) bool {
	ok, _ := path.Match("minisign*.pub", name)
	return ok
}
