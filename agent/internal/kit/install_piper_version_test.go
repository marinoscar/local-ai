package kit

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The manifest version of a url item is the sync's content hash; piper's release number comes from the
// source URL of the file (found on a real node: "version a50cb45f355b is not a release number").
func TestPiperFileReleaseNumber(t *testing.T) {
	const base = "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/"
	file := func(url string) manifest.File {
		return manifest.File{Path: "piper_linux_x86_64.tar.gz", URL: url}
	}
	for _, tc := range []struct {
		name    string
		version string
		url     string
		want    string
		ok      bool
	}{
		{"hash version, release in the URL", "a50cb45f355b", base + "piper_linux_x86_64.tar.gz", "2023.11.14-2", true},
		{"release-number version, no URL", "2023.11.14-2", "", "2023.11.14-2", true},
		{"URL wins over a release-number version", "2023.01.01", base + "piper_linux_x86_64.tar.gz", "2023.11.14-2", true},
		{"hash version and no usable URL", "a50cb45f355b", "https://example.invalid/piper_linux_x86_64.tar.gz", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v, ok := piperFile(manifest.Manifest{Version: tc.version, Files: []manifest.File{file(tc.url)}}, "amd64")
			if ok != tc.ok || v != tc.want {
				t.Errorf("piperFile = %q %v, want %q %v", v, ok, tc.want, tc.ok)
			}
		})
	}
}
