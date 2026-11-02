package local

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// readArchive reads a backup completely into memory, refusing anything a restore must never
// follow: a link or device entry, an absolute or ".." path, a duplicate name, an oversize entry.
func readArchive(file string) (Manifest, map[string][]byte, error) {
	var m Manifest
	fi, err := os.Stat(file)
	if err != nil {
		return m, nil, fileErr("read", file, err)
	}
	if !fi.Mode().IsRegular() {
		return m, nil, fileErr("read", file, errNotRegular)
	}
	if fi.Size() > maxBackupGzip {
		return m, nil, &BackupError{Reason: "the file is too large to be a config backup"}
	}
	f, err := os.Open(file)
	if err != nil {
		return m, nil, fileErr("read", file, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return m, nil, &BackupError{Reason: "the file is not a config backup (it is not a .tar.gz archive)"}
	}
	tr := tar.NewReader(io.LimitReader(gz, maxBackupTotal+1))
	files := map[string][]byte{}
	total := int64(0)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, &BackupError{Reason: "the archive is damaged or incomplete"}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return m, nil, &BackupError{Reason: fmt.Sprintf("the archive holds %q, which is a link or special file", printable(hdr.Name))}
		}
		if !safeName(hdr.Name) {
			return m, nil, &BackupError{Reason: fmt.Sprintf("the archive holds an unsafe path %q", printable(hdr.Name))}
		}
		if _, dup := files[hdr.Name]; dup {
			return m, nil, &BackupError{Reason: fmt.Sprintf("the archive holds %q twice", hdr.Name)}
		}
		if hdr.Size > maxBackupFile {
			return m, nil, &BackupError{Reason: fmt.Sprintf("%q is too large for a config backup", hdr.Name)}
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxBackupFile+1))
		if err != nil || int64(len(data)) != hdr.Size {
			return m, nil, &BackupError{Reason: "the archive is damaged or incomplete"}
		}
		if total += int64(len(data)); total > maxBackupTotal {
			return m, nil, &BackupError{Reason: "the archive holds too much data to be a config backup"}
		}
		files[hdr.Name] = data
	}
	raw, ok := files[manifestName]
	if !ok {
		return m, nil, &BackupError{Reason: "the archive has no " + manifestName}
	}
	delete(files, manifestName)
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, nil, &BackupError{Reason: manifestName + " cannot be read"}
	}
	return m, files, nil
}

// printable keeps an attacker-controlled name from putting control characters on a terminal.
func printable(s string) string {
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// safeName accepts only clean relative paths: no leading slash, no "." or ".." segment, no empty
// segment, no backslash, no NUL.
func safeName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\\x00") || path.Clean(n) != n {
		return false
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}

type targetKind int

const (
	kindFile targetKind = iota + 1
	kindCatalog
)

// restoreTarget maps an archive path to its place on this machine. It is an allowlist: only the
// files a backup is made of can be restored, so a manifest cannot name any other path.
func restoreTarget(e Env, name string) (host string, kind targetKind, ok bool) {
	switch {
	case name == archState:
		return e.Paths.StateFile(), kindFile, true
	case name == archUnit:
		return filepath.Join(e.Paths.SystemdDir, unitName), kindFile, true
	case name == archDropIn:
		return filepath.Join(e.Paths.SystemdDir, dropInDir, dropInName), kindFile, true
	case strings.HasPrefix(name, archCatalog) && len(name) > len(archCatalog):
		return filepath.Join(e.Paths.EtcDir, filepath.FromSlash(strings.TrimPrefix(name, archEtc))), kindCatalog, true
	case strings.HasPrefix(name, archEtc):
		rel := strings.TrimPrefix(name, archEtc)
		for _, f := range etcFiles {
			if rel == f {
				return filepath.Join(e.Paths.EtcDir, f), kindFile, true
			}
		}
	}
	return "", 0, false
}

// PlanItem is one file a restore will write.
type PlanItem struct {
	// Path is where it goes on this machine.
	Path string
	// Arch is the archive path.
	Arch string
	// Secret marks one of the five secret files.
	Secret bool
	// Exists says a file is there now; Same says it is already identical (content and mode), so the
	// restore leaves it alone and keeps no .pre-restore copy.
	Exists, Same bool

	kind     targetKind
	data     []byte
	mode     os.FileMode
	uid, gid int
}

// RestorePlan is a verified backup and what restoring it would do. Nothing on the node has changed
// when it is returned.
type RestorePlan struct {
	Manifest Manifest
	// Items are sorted by Path.
	Items []PlanItem
	// ThisNode is the id in this node's node.yaml; Foreign says the backup is from another node.
	ThisNode string
	Foreign  bool
	// CatalogChanges is true when the catalog folder will be replaced (the backup has one and it
	// differs from the live one).
	CatalogChanges bool
}

// Changes is the number of files the restore will write.
func (p *RestorePlan) Changes() int {
	n := 0
	for _, it := range p.Items {
		if !it.Same {
			n++
		}
	}
	return n
}

// PrepareRestore opens a backup, verifies it and returns the plan. It checks that every path is a
// place a backup is allowed to restore, that the manifest and the archive list exactly the same
// files, that every size and sha256 matches, and that no mode is unsafe. A failure is *BackupError
// (or *FileError for an unreadable file).
func PrepareRestore(e Env, file string) (*RestorePlan, error) {
	m, files, err := readArchive(file)
	if err != nil {
		return nil, err
	}
	if len(m.Files) == 0 {
		return nil, &BackupError{Reason: "the backup lists no files"}
	}
	plan := &RestorePlan{Manifest: m, ThisNode: NodeID(e)}
	if nodeIDRe.MatchString(m.NodeID) {
		plan.Foreign = m.NodeID != plan.ThisNode
	} else {
		plan.Manifest.NodeID = "unknown"
		plan.Foreign = true
	}
	seen := map[string]bool{}
	catalogFiles := map[string][]byte{}
	for _, mf := range m.Files {
		if !safeName(mf.Path) || seen[mf.Path] {
			return nil, &BackupError{Reason: fmt.Sprintf("the manifest lists an unsafe or repeated path %q", printable(mf.Path))}
		}
		seen[mf.Path] = true
		host, kind, ok := restoreTarget(e, mf.Path)
		if !ok {
			return nil, &BackupError{Reason: fmt.Sprintf("the manifest lists %q, which a restore may not write", printable(mf.Path))}
		}
		data, ok := files[mf.Path]
		if !ok {
			return nil, &BackupError{Reason: fmt.Sprintf("%q is in the manifest but not in the archive", mf.Path)}
		}
		sum := sha256.Sum256(data)
		if mf.Size != int64(len(data)) || !strings.EqualFold(mf.SHA256, hex.EncodeToString(sum[:])) {
			return nil, &BackupError{Reason: fmt.Sprintf("%q does not match its checksum: the backup is damaged", mf.Path)}
		}
		mode, merr := strconv.ParseUint(mf.Mode, 8, 32)
		if merr != nil || mode > 0o777 || mode&0o002 != 0 {
			return nil, &BackupError{Reason: fmt.Sprintf("%q has an unsafe mode %q", mf.Path, printable(mf.Mode))}
		}
		secret := kind == kindFile && strings.HasPrefix(mf.Path, archEtc) && secretFiles[strings.TrimPrefix(mf.Path, archEtc)]
		if secret && !m.SecretsIncluded {
			return nil, &BackupError{Reason: "the manifest says there are no secrets but lists " + path.Base(mf.Path)}
		}
		uid, gid := e.resolveOwner(mf)
		it := PlanItem{Path: host, Arch: mf.Path, Secret: secret, kind: kind, data: data, mode: os.FileMode(mode), uid: uid, gid: gid}
		if fi, serr := os.Lstat(host); serr == nil && kind == kindFile {
			it.Exists = true
			cur, rerr := os.ReadFile(host)
			it.Same = fi.Mode().IsRegular() && rerr == nil && bytes.Equal(cur, data) && fi.Mode().Perm() == it.mode
		}
		if kind == kindCatalog {
			catalogFiles[mf.Path] = data
		}
		plan.Items = append(plan.Items, it)
	}
	for name := range files {
		if !seen[name] {
			return nil, &BackupError{Reason: fmt.Sprintf("the archive holds %q, which the manifest does not list", printable(name))}
		}
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Path < plan.Items[j].Path })
	if len(catalogFiles) > 0 {
		same := liveCatalogMatches(e, catalogFiles)
		plan.CatalogChanges = !same
		for i := range plan.Items {
			if plan.Items[i].kind == kindCatalog {
				plan.Items[i].Same = same
				plan.Items[i].Exists = same || isDir(e.Paths.CatalogDir())
			}
		}
	}
	return plan, nil
}

// liveCatalogMatches says whether /etc/localai/catalog holds exactly the files in want (by archive
// path), with the same content.
func liveCatalogMatches(e Env, want map[string][]byte) bool {
	got := map[string][]byte{}
	dir := e.Paths.CatalogDir()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errNotRegular
		}
		rel, _ := filepath.Rel(dir, p)
		data, rerr := os.ReadFile(p)
		got[archCatalog+filepath.ToSlash(rel)] = data
		return rerr
	})
	if err != nil || len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			return false
		}
	}
	return true
}

// resolveOwner finds the account a restored file belongs to: the names in the manifest first,
// because numeric ids can differ on a rebuilt machine, then the numeric ids, then root.
func (e Env) resolveOwner(mf ManifestFile) (uid, gid int) {
	uid, gid = mf.UID, mf.GID
	if mf.Owner != "" && e.LookupUser != nil {
		if id, err := e.LookupUser(mf.Owner); err == nil {
			uid = id
		}
	}
	if mf.Group != "" && e.LookupGroup != nil {
		if id, err := e.LookupGroup(mf.Group); err == nil {
			gid = id
		}
	}
	if uid < 0 {
		uid = e.RootUID
	}
	if gid < 0 {
		gid = e.RootGID
	}
	return uid, gid
}

// RestoreResult says what a restore did.
type RestoreResult struct {
	// Written is the number of files written; Kept are the .pre-restore copies made (a folder
	// counts once).
	Written int
	Kept    []string
	// ServiceErr is why `systemctl daemon-reload` or the agent restart failed; the files are in
	// place either way.
	ServiceErr error
}

// Apply restores the plan. Steps: copy each file it will replace to <file>.pre-restore (a copy of a
// file that is already identical is skipped, so a restore that was interrupted and is run again
// keeps the true originals), write each file atomically with the manifest's mode and owner, replace
// the catalog folder last (the old one becomes catalog.pre-restore), then `systemctl daemon-reload`
// and `systemctl restart localai-agent.service` through execx with fixed arguments. The caller holds
// Lock and has confirmed.
func (p *RestorePlan) Apply(ctx context.Context, e Env) (*RestoreResult, error) {
	res := &RestoreResult{}
	var catalog []PlanItem
	// 1. originals first, so a failure while copying changes nothing that matters.
	for _, it := range p.Items {
		if it.Same || it.kind != kindFile || !it.Exists {
			continue
		}
		fi, err := os.Lstat(it.Path)
		if err != nil || !fi.Mode().IsRegular() {
			return res, fileErr("read", it.Path, firstErr(err, errNotRegular))
		}
		cur, err := os.ReadFile(it.Path)
		if err != nil {
			return res, fileErr("read", it.Path, err)
		}
		uid, gid := ownerOf(fi)
		keep := it.Path + preRestoreSf
		if err := config.WriteFileAtomicFS(e.fsys(), keep, cur, fi.Mode().Perm(), uid, gid); err != nil {
			return res, fileErr("write", keep, err)
		}
		res.Kept = append(res.Kept, keep)
	}
	// 2. the files.
	for _, it := range p.Items {
		if it.kind == kindCatalog {
			catalog = append(catalog, it)
			continue
		}
		if it.Same {
			continue
		}
		if err := e.ensureParent(filepath.Dir(it.Path)); err != nil {
			return res, err
		}
		if err := config.WriteFileAtomicFS(e.fsys(), it.Path, it.data, it.mode, it.uid, it.gid); err != nil {
			return res, fileErr("write", it.Path, err)
		}
		res.Written++
	}
	// 3. the catalog folder, last, so its CATALOG_VERSION is the newest write the agent sees.
	if p.CatalogChanges && len(catalog) > 0 {
		kept, err := e.swapCatalog(catalog)
		if err != nil {
			return res, err
		}
		if kept != "" {
			res.Kept = append(res.Kept, kept)
		}
		res.Written += len(catalog)
	}
	res.ServiceErr = e.restartAgent(ctx)
	return res, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// ensureParent creates a missing directory (the systemd drop-in folder, or /etc/localai on a bare
// machine) with the usual 0755, or 0750 for the configuration directory.
func (e Env) ensureParent(dir string) error {
	if isDir(dir) {
		return nil
	}
	mode := os.FileMode(0o755)
	if dir == e.Paths.EtcDir {
		mode = 0o750
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return fileErr("write", dir, err)
	}
	return fileErr("write", dir, os.Chmod(dir, mode))
}

// swapCatalog writes the catalog files into catalog.restoring, then renames the live folder to
// catalog.pre-restore (when there is one) and the new one into place. It returns the pre-restore
// folder, or "" when there was no live folder.
func (e Env) swapCatalog(items []PlanItem) (kept string, err error) {
	live := e.Paths.CatalogDir()
	stage := live + ".restoring"
	pre := live + preRestoreSf
	if err := os.RemoveAll(stage); err != nil {
		return "", fileErr("remove", stage, err)
	}
	// Folders: root, the mode catalog folders have, and the group of the files inside.
	dirGID := items[0].gid
	mkdir := func(dir string) error {
		if err := os.MkdirAll(dir, ModeCatDir); err != nil {
			return fileErr("write", dir, err)
		}
		if err := os.Chmod(dir, ModeCatDir); err != nil {
			return fileErr("write", dir, err)
		}
		return fileErr("write", dir, os.Chown(dir, e.RootUID, dirGID))
	}
	if err := mkdir(stage); err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(stage)
		return "", err
	}
	for _, it := range items {
		rel := strings.TrimPrefix(it.Arch, archCatalog)
		dst := filepath.Join(stage, filepath.FromSlash(rel))
		if d := filepath.Dir(dst); d != stage && !isDir(d) {
			if err := mkdirTree(d, stage, mkdir); err != nil {
				return fail(err)
			}
		}
		if err := config.WriteFileAtomicFS(e.fsys(), dst, it.data, it.mode, it.uid, it.gid); err != nil {
			return fail(fileErr("write", dst, err))
		}
	}
	if isDir(live) {
		if err := os.RemoveAll(pre); err != nil {
			return fail(fileErr("remove", pre, err))
		}
		if err := os.Rename(live, pre); err != nil {
			return fail(fileErr("write", live, err))
		}
		kept = pre
	}
	if err := os.Rename(stage, live); err != nil {
		if kept != "" {
			_ = os.Rename(pre, live) // put the old catalog back
		}
		return fail(fileErr("write", live, err))
	}
	if err := syncDir(e.Paths.EtcDir); err != nil {
		return kept, fileErr("write", e.Paths.EtcDir, err)
	}
	return kept, nil
}

// mkdirTree creates dir and any missing parents between it and stage.
func mkdirTree(dir, stage string, mkdir func(string) error) error {
	rel, err := filepath.Rel(stage, dir)
	if err != nil {
		return fileErr("write", dir, err)
	}
	cur := stage
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		if !isDir(cur) {
			if err := mkdir(cur); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// restartAgent runs `systemctl daemon-reload` and `systemctl restart localai-agent.service`. Both
// are tried; the first failure is returned.
func (e Env) restartAgent(ctx context.Context) error {
	if e.Runner == nil || e.LookPath == nil {
		return nil
	}
	bin, err := e.LookPath("systemctl")
	if err != nil {
		return err
	}
	var first error
	for _, args := range [][]string{{"daemon-reload"}, {"restart", "localai-agent.service"}} {
		r, err := e.Runner.Run(ctx, execx.Cmd{Path: bin, Args: args, Timeout: 60 * time.Second, MaxOut: 16 << 10})
		if err == nil && r.ExitCode != 0 {
			err = fmt.Errorf("systemctl %s exited with status %d", args[0], r.ExitCode)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
