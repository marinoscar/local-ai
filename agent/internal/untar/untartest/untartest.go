// Package untartest makes the small archives the music runtime's tests unpack.
package untartest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
)

// PythonTarball is a .tar.gz shaped like python-build-standalone's install_only tarball:
// python/bin/python3.12 (an executable script) and python/bin/python3 and python/bin/python, links to it.
func PythonTarball() []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\n")
	_ = tw.WriteHeader(&tar.Header{Name: "python/bin/python3.12", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	_, _ = tw.Write(body)
	for _, l := range []string{"python/bin/python3", "python/bin/python"} {
		_ = tw.WriteHeader(&tar.Header{Name: l, Typeflag: tar.TypeSymlink, Linkname: "python3.12", Mode: 0o777})
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
