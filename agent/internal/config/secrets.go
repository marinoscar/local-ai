package config

import (
	"errors"
	"fmt"
	"os"
)

// maxSecretFile bounds what ReadSecretFile will read; a secret file is a token or a hash.
const maxSecretFile = 1 << 20

// ErrNotRegularFile is returned for a secret path that is a symlink, a directory or a device.
// The agent never follows a link to a secret: chmod would change the link's target.
var ErrNotRegularFile = errors.New("config: secret path is not a regular file")

// SecretPolicy is the permission a secret file must have. UID or GID of -1 means "not checked".
// Mode is the most permissive mode allowed: a file with fewer permission bits is fine.
type SecretPolicy struct {
	Mode os.FileMode
	UID  int
	GID  int
}

// The policies of the secret files in /etc/localai (agent.md 3.3 and 4.1 step 6). agent-token is
// root:localai-operators; the group id is looked up by the caller, so GID is not checked here.
var (
	PolicyAPIKey          = SecretPolicy{Mode: 0o600, UID: 0, GID: 0}
	PolicyCloudCredential = SecretPolicy{Mode: 0o600, UID: 0, GID: 0}
	PolicyAdminPIN        = SecretPolicy{Mode: 0o600, UID: 0, GID: 0}
	PolicyAgentToken      = SecretPolicy{Mode: 0o640, UID: 0, GID: -1}
)

// PermIssue is one way a secret file differs from its policy. Raise CodeSecretPermissions for it.
type PermIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// SecretFile is a secret file's content plus the permission issues found when it was read.
type SecretFile struct {
	Data   []byte
	Issues []PermIssue
}

// CheckSecret compares a file's mode and owner with the policy. It does not read the file and
// never changes it. A missing file is an error that satisfies errors.Is(err, os.ErrNotExist).
func CheckSecret(fsys FS, path string, p SecretPolicy) ([]PermIssue, error) {
	if fsys == nil {
		fsys = OSFS{}
	}
	info, err := fsys.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}
	var issues []PermIssue
	mode := info.Mode()
	if extra := mode.Perm() &^ p.Mode; extra != 0 {
		issues = append(issues, PermIssue{path, fmt.Sprintf("mode %04o is more open than %04o", mode.Perm(), p.Mode)})
	}
	if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		issues = append(issues, PermIssue{path, "has a setuid, setgid or sticky bit"})
	}
	if uid, gid, ok := ownerOf(info); ok {
		if p.UID >= 0 && uid != p.UID {
			issues = append(issues, PermIssue{path, fmt.Sprintf("owner is uid %d, expected %d", uid, p.UID)})
		}
		if p.GID >= 0 && gid != p.GID {
			issues = append(issues, PermIssue{path, fmt.Sprintf("group is gid %d, expected %d", gid, p.GID)})
		}
	}
	return issues, nil
}

// ReadSecretFile checks the file against the policy, then reads it. A file with wrong permissions
// is still read (the caller decides, usually by calling EnforceSecret and raising
// CodeSecretPermissions); a symlink or non-regular file is refused. The content is never logged here.
func ReadSecretFile(fsys FS, path string, p SecretPolicy) (SecretFile, error) {
	if fsys == nil {
		fsys = OSFS{}
	}
	issues, err := CheckSecret(fsys, path, p)
	if err != nil {
		return SecretFile{}, err
	}
	info, err := fsys.Stat(path)
	if err != nil {
		return SecretFile{}, err
	}
	if info.Size() > maxSecretFile {
		return SecretFile{}, fmt.Errorf("config: secret file %s is larger than %d bytes", path, maxSecretFile)
	}
	data, err := fsys.ReadFile(path)
	if err != nil {
		return SecretFile{}, err
	}
	return SecretFile{Data: data, Issues: issues}, nil
}

// EnforceSecret fixes what CheckSecret reports: it sets the mode to the policy's mode and the owner
// to the policy's uid and gid (where they are not -1). It returns the issues it found and fixed;
// a non-empty result is the cue to raise CodeSecretPermissions.
func EnforceSecret(fsys FS, path string, p SecretPolicy) ([]PermIssue, error) {
	if fsys == nil {
		fsys = OSFS{}
	}
	issues, err := CheckSecret(fsys, path, p)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	if info, serr := fsys.Lstat(path); serr == nil {
		if uid, gid, ok := ownerOf(info); ok && ((p.UID >= 0 && uid != p.UID) || (p.GID >= 0 && gid != p.GID)) {
			if err := fsys.Chown(path, p.UID, p.GID); err != nil {
				return issues, fmt.Errorf("fix owner of %s: %w", path, err)
			}
		}
	}
	if err := fsys.Chmod(path, p.Mode); err != nil {
		return issues, fmt.Errorf("fix mode of %s: %w", path, err)
	}
	return issues, nil
}
