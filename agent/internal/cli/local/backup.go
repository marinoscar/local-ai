package local

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Limits that keep a damaged or hostile archive from filling memory: a real backup is a few
// hundred KiB (the catalog is at most 2 MB).
const (
	maxBackupFile  = 8 << 20  // one file inside a backup
	maxBackupTotal = 64 << 20 // everything inside a backup, uncompressed
	maxBackupGzip  = 64 << 20 // the archive on disk
	// KeepPerNode is how many backups per node `--to-ark` keeps on the drive.
	KeepPerNode  = 5
	manifestName = "MANIFEST.json"
	timeLayout   = "20060102T150405Z"
)

// secretFiles are the five files `--no-secrets` leaves out (aictl.md section 4.25).
var secretFiles = map[string]bool{
	"api-key": true, "agent-token": true, "cloud-credential": true, "admin-pin": true, "hf-token": true,
}

// etcFiles are the plain files of /etc/localai a backup carries when they exist.
var etcFiles = []string{"node.yaml", "models.ini", "api-key", "agent-token", "cloud-credential", "admin-pin", "hf-token"}

const (
	archEtc      = "etc/localai/"
	archState    = "var/lib/localai/state.json"
	archUnit     = "etc/systemd/system/localai-llama.service"
	archDropIn   = "etc/systemd/system/localai-llama.service.d/50-env.conf"
	archCatalog  = "etc/localai/catalog/"
	unitName     = "localai-llama.service"
	dropInDir    = "localai-llama.service.d"
	dropInName   = "50-env.conf"
	preRestoreSf = ".pre-restore"
)

// Manifest is MANIFEST.json, the first entry of a backup.
type Manifest struct {
	NodeID          string         `json:"node_id"`
	CreatedAt       string         `json:"created_at"`
	AgentVersion    string         `json:"agent_version"`
	Files           []ManifestFile `json:"files"`
	SecretsIncluded bool           `json:"secrets_included"`
}

// ManifestFile describes one file of a backup. Path is the archive path with no leading slash
// (etc/localai/node.yaml). Owner and Group are the names behind UID and GID, so a restore on a
// rebuilt machine, where the numeric ids may differ, gives the file to the same account.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"` // octal, "0600"
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Owner  string `json:"owner,omitempty"`
	Group  string `json:"group,omitempty"`
}

// BackupOptions configure Backup.
type BackupOptions struct {
	// Out is the file to write, or an existing directory to put the generated name in; empty means
	// the current directory. Ignored when ToDrive is set.
	Out string
	// ToDrive writes into <drive>/kit/config/<node-id>/ and keeps the last KeepPerNode there.
	ToDrive *Root
	// NoSecrets leaves out the five secret files.
	NoSecrets bool
}

// BackupResult says what was written.
type BackupResult struct {
	Path            string
	NodeID          string
	Files           int
	SecretsIncluded bool
	// Pruned are the older backups removed from the drive; PruneErr is why one could not be.
	Pruned   []string
	PruneErr error
}

type bentry struct {
	arch   string // archive path
	host   string // path on this machine
	secret bool
	data   []byte
	fi     os.FileInfo
}

var nodeIDRe = regexp.MustCompile(`^ai-n[1-9][0-9]{0,3}$`)

// NodeID reads node.id from /etc/localai/node.yaml without validating the rest of the file, so a
// damaged node.yaml does not stop a backup. An id that is missing, unreadable or not of the form
// ai-n<N> becomes "unconfigured": the id goes into file names and folder names.
func NodeID(e Env) string {
	data, err := os.ReadFile(e.Paths.NodeYAML())
	if err != nil || len(data) > 1<<20 {
		return config.UnconfiguredNodeID
	}
	cfg, _ := config.Parse(data, nil)
	if nodeIDRe.MatchString(cfg.Node.ID) {
		return cfg.Node.ID
	}
	return config.UnconfiguredNodeID
}

// errNotRegular is a source that is a symlink, a device or a directory where a file is expected. A
// backup never follows a link: it could leak a file that is not part of the node's configuration.
var errNotRegular = errors.New("it is not a regular file")

// collect reads every file a backup carries that exists on this machine. A file that is absent is
// skipped (hf-token and the drop-in are optional); one that cannot be read, or is not a regular
// file, is an error naming it.
func collect(e Env, noSecrets bool) ([]bentry, error) {
	var out []bentry
	add := func(arch, host string, secret bool) error {
		if secret && noSecrets {
			return nil
		}
		fi, err := os.Lstat(host)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fileErr("read", host, err)
		}
		if !fi.Mode().IsRegular() {
			return fileErr("read", host, errNotRegular)
		}
		if fi.Size() > maxBackupFile {
			return fileErr("read", host, fmt.Errorf("it is larger than %d MiB", maxBackupFile>>20))
		}
		data, err := os.ReadFile(host)
		if err != nil {
			return fileErr("read", host, err)
		}
		out = append(out, bentry{arch: arch, host: host, secret: secret, data: data, fi: fi})
		return nil
	}
	for _, name := range etcFiles {
		if err := add(archEtc+name, filepath.Join(e.Paths.EtcDir, name), secretFiles[name]); err != nil {
			return nil, err
		}
	}
	if err := collectTree(e.Paths.CatalogDir(), archCatalog, add); err != nil {
		return nil, err
	}
	if err := add(archState, e.Paths.StateFile(), false); err != nil {
		return nil, err
	}
	if err := add(archUnit, filepath.Join(e.Paths.SystemdDir, unitName), false); err != nil {
		return nil, err
	}
	if err := add(archDropIn, filepath.Join(e.Paths.SystemdDir, dropInDir, dropInName), false); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].arch < out[j].arch })
	return out, nil
}

// collectTree adds every file under dir (a missing dir is fine) with archive prefix prefix.
func collectTree(dir, prefix string, add func(arch, host string, secret bool) error) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fileErr("read", dir, err)
	}
	if !fi.IsDir() {
		return fileErr("read", dir, errors.New("it is not a folder"))
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fileErr("read", p, err)
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return fileErr("read", p, rerr)
		}
		return add(prefix+filepath.ToSlash(rel), p, false)
	})
}

// buildArchive renders entries as a gzip tar with MANIFEST.json first.
func buildArchive(e Env, node string, entries []bentry, secrets bool) ([]byte, error) {
	m := Manifest{NodeID: node, CreatedAt: e.now().UTC().Format(time.RFC3339), AgentVersion: e.AgentVersion, SecretsIncluded: secrets,
		Files: make([]ManifestFile, 0, len(entries))}
	for _, en := range entries {
		sum := sha256.Sum256(en.data)
		uid, gid := ownerOf(en.fi)
		mf := ManifestFile{Path: en.arch, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(en.data)),
			Mode: fmt.Sprintf("%04o", en.fi.Mode().Perm()), UID: uid, GID: gid}
		if uid >= 0 && e.UserName != nil {
			mf.Owner = e.UserName(uid)
		}
		if gid >= 0 && e.GroupName != nil {
			mf.Group = e.GroupName(gid)
		}
		m.Files = append(m.Files, mf)
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte, mode int64, uid, gid int, uname, gname string, mod time.Time) error {
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: mode, ModTime: mod}
		if uid >= 0 {
			hdr.Uid, hdr.Uname = uid, uname
		}
		if gid >= 0 {
			hdr.Gid, hdr.Gname = gid, gname
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := put(manifestName, mj, 0o600, e.RootUID, e.RootGID, "", "", e.now()); err != nil {
		return nil, err
	}
	for i, en := range entries {
		mf := m.Files[i]
		if err := put(en.arch, en.data, int64(en.fi.Mode().Perm()), mf.UID, mf.GID, mf.Owner, mf.Group, en.fi.ModTime()); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Backup is `aictl backup-config`: it writes localai-config-<node-id>-<UTC time>.tar.gz (root 0600,
// atomically, never over an existing file) holding the configuration files of this node and a
// MANIFEST.json with the sha256 of each. It prints nothing and keeps no secret in its result.
func Backup(e Env, o BackupOptions) (*BackupResult, error) {
	node := NodeID(e)
	entries, err := collect(e, o.NoSecrets)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fileErr("read", e.Paths.EtcDir, errors.New("it holds no node configuration to save"))
	}
	secrets := false
	for _, en := range entries {
		secrets = secrets || en.secret
	}
	data, err := buildArchive(e, node, entries, secrets)
	if err != nil {
		return nil, fileErr("write", "the backup", err)
	}
	name := fmt.Sprintf("localai-config-%s-%s.tar.gz", node, e.now().UTC().Format(timeLayout))
	var dest string
	switch {
	case o.ToDrive != nil:
		dir, derr := ensureConfigDir(o.ToDrive, node)
		if derr != nil {
			return nil, derr
		}
		dest = filepath.Join(dir, name)
	case o.Out == "":
		dest = "./" + name
	default:
		dest = o.Out
		if fi, serr := os.Stat(dest); serr == nil && fi.IsDir() {
			dest = filepath.Join(dest, name)
		}
	}
	if _, err := os.Lstat(dest); err == nil {
		return nil, fileErr("write", dest, fs.ErrExist)
	}
	if err := config.WriteFileAtomicFS(e.fsys(), dest, data, ModeSecret, e.RootUID, e.RootGID); err != nil {
		return nil, fileErr("write", dest, err)
	}
	res := &BackupResult{Path: dest, NodeID: node, Files: len(entries), SecretsIncluded: secrets}
	if o.ToDrive != nil {
		res.Pruned, res.PruneErr = pruneBackups(filepath.Dir(dest), node)
	}
	return res, nil
}

// ensureConfigDir creates <root>/kit/config/<node> (kit 0755, the two below it 0700: they hold
// secrets) and returns it. A read-only drive is refused with a plain reason.
func ensureConfigDir(r *Root, node string) (string, error) {
	if r.ReadOnly {
		return "", fileErr("write", r.Path, errors.New("the drive is mounted read-only"))
	}
	kit := filepath.Join(r.Path, "kit")
	for _, d := range []struct {
		p    string
		mode os.FileMode
	}{{kit, 0o755}, {r.KitConfig(), 0o700}, {filepath.Join(r.KitConfig(), node), 0o700}} {
		err := os.Mkdir(d.p, d.mode)
		switch {
		case err == nil:
			if err := os.Chmod(d.p, d.mode); err != nil { // the umask must not widen or narrow it
				return "", fileErr("write", d.p, err)
			}
		case errors.Is(err, fs.ErrExist):
		default:
			return "", fileErr("write", d.p, err)
		}
	}
	return filepath.Join(r.KitConfig(), node), nil
}

// pruneBackups keeps the newest KeepPerNode backups of node in dir and removes the rest. Only names
// of the exact generated form are touched.
func pruneBackups(dir, node string) (removed []string, err error) {
	re := regexp.MustCompile(`^localai-config-` + regexp.QuoteMeta(node) + `-\d{8}T\d{6}Z\.tar\.gz$`)
	list, rerr := os.ReadDir(dir)
	if rerr != nil {
		return nil, fileErr("read", dir, rerr)
	}
	var names []string
	for _, d := range list {
		if d.Type().IsRegular() && re.MatchString(d.Name()) {
			names = append(names, d.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[min(KeepPerNode, len(names)):] {
		p := filepath.Join(dir, n)
		if rerr := os.Remove(p); rerr != nil {
			err = errors.Join(err, fileErr("remove", p, rerr))
			continue
		}
		removed = append(removed, p)
	}
	return removed, err
}
