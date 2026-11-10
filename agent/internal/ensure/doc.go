// Package ensure keeps a node complete (ADR-0010, rebuild-kit.md section 4.5, agent.md section 3.8.33).
//
// Ark sync downloads and verifies content; nothing turned that content into a working node, so a person had
// to know that kiwix-tools, pmtiles, piper, the offline apt packages and the whisper build exist and type
// `sudo aictl kit install ...`. This package closes that gap:
//
//   - the Planner reads what the profile and node.yaml want and what this computer has, and lists the steps
//     that would make it complete (Plan);
//   - the Executor runs those steps, one at a time, from the kit items that verified, through an allowlist
//     of named actions only (Allowlist), journaled, audited, idempotent and cancellable;
//   - the Service owns the one pass that runs at a time, the triggers (the end of a sync run, the start of
//     the agent, mode_online), the setting ensure.auto, and the state the REST route, `aictl ensure` and the
//     doctor show.
//
// What is automatic: installing a tool or a package set from the kit, and restoring a ready-made whisper
// build when the node has none. What needs a confirmation passed by the caller, never automatic: compiling
// from source, restarting a service that is serving, replacing the agent binary (never done here at all, it
// stays `aictl update`), and anything destructive (nothing here deletes).
//
// Everything that touches the machine (the disk, dpkg, systemd, the kit, the audit log) is behind an
// interface in this package, so the tests run on fakes.
package ensure
