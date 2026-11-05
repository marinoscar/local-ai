package kit

import (
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/update"
)

// ReleaseDirName is the folder of kit/bin that holds the release's signed checksum list:
// kit/bin/release/SHA256SUMS and kit/bin/release/SHA256SUMS.minisig (rebuild-kit.md section 3.2.1).
//
// The sync engine reserves the names SHA256SUMS and MANIFEST.json in every item folder, so the release's own
// SHA256SUMS cannot be a plain file of kit-agent-binaries. The catalog gives the two files a name inside
// a folder (`name: release/SHA256SUMS`) and every reader of the signed list goes through ReleaseListDir.
const ReleaseDirName = "release"

// ReleaseListDir returns the folder that holds the signed list of the agent files in binDir (a kit/bin
// folder): binDir/release when it holds SHA256SUMS or its signature, else binDir itself. The second form is
// the layout of a drive prepared by hand (a release folder copied as it is, `aictl verify-release`
// accepts the same) and of kits made before the folder existed.
func ReleaseListDir(fsys update.FS, binDir string) string {
	dir := filepath.Join(binDir, ReleaseDirName)
	for _, name := range []string{update.SumsName, update.SigName} {
		if fi, err := fsys.Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() {
			return dir
		}
	}
	return binDir
}

// VerifyBin verifies the agent files of binDir (a kit/bin folder) the way `aictl kit install agent`,
// `aictl kit verify` and `aictl verify-release` do: the signature of the release's SHA256SUMS against keys,
// then every listed file that is in binDir. The release's list also names files that belong to other kit
// items (the agent source tarball is in kit/src), so a listed file that is not in binDir is not an error.
func VerifyBin(fsys update.FS, binDir string, keys []update.PublicKey) (update.DirReport, error) {
	return update.VerifyDir(fsys, binDir, keys, update.DirOptions{IgnoreMissing: true, ListDir: ReleaseListDir(fsys, binDir)})
}
