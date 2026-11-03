// Package units holds the systemd unit templates the agent installs with `aiagent setup units`
// (INST-06): the files are embedded in the binary so a node with no internet can still write them.
//
// This file embeds the drill pair (drill.md section 3.3, DRILL-01); the installer's other units join
// the package in their own files.
package units

import "embed"

// Drill is the localai-drill.timer and localai-drill.service templates.
//
//go:embed localai-drill.timer localai-drill.service
var Drill embed.FS
