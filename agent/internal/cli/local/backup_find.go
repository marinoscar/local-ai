package local

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// BackupFile is a config backup found in the kit config folder of a content root (issue #433,
// aictl.md section 4.25): <root>/kit/config/<node-id>/localai-config-<node-id>-<UTC time>.tar.gz.
type BackupFile struct {
	Path string
	// NodeID is the node the backup was made on; Time its UTC time as written in the name
	// (YYYYMMDDTHHMMSSZ), which sorts in time order.
	NodeID, Time string
	// Root is the content root it was found on.
	Root Root
}

var backupNameRe = regexp.MustCompile(`^localai-config-(ai-n[1-9][0-9]{0,3}|unconfigured)-(\d{8}T\d{6}Z)\.tar\.gz$`)

// FindBackups lists the config backups in the kit config folders of roots, newest first (by the time
// in the name; on a tie, in the order of roots). Only names of the exact generated form, in the folder
// of the node they name, and regular files count: anything else is never offered for a restore.
// Folders that cannot be read are skipped.
func FindBackups(roots []Root) []BackupFile {
	var out []BackupFile
	for _, r := range roots {
		nodes, err := os.ReadDir(r.KitConfig())
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if !n.IsDir() {
				continue
			}
			dir := filepath.Join(r.KitConfig(), n.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				g := backupNameRe.FindStringSubmatch(f.Name())
				if g == nil || g[1] != n.Name() || !f.Type().IsRegular() {
					continue
				}
				out = append(out, BackupFile{Path: filepath.Join(dir, f.Name()), NodeID: g[1], Time: g[2], Root: r})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// LatestBackup is the newest backup of node among backups (FindBackups' order); ok is false when
// there is none.
func LatestBackup(backups []BackupFile, node string) (BackupFile, bool) {
	for _, b := range backups {
		if b.NodeID == node {
			return b, true
		}
	}
	return BackupFile{}, false
}
