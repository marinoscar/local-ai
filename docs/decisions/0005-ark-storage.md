# ADR-0005: Ark content lives on internal disk (per-profile subset) and on an AI-ARK drive (full)

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
The full ark is about 300–600 GB: models for every node class, Kiwix libraries, maps and the rebuild kit. Internal disks vary (ai-n3 has about 290 GB free). The owner wants each laptop self-sufficient **and** a portable copy that can seed or rebuild any node.

## Decision
- **Internal:** each node keeps the subset in its profile (`catalog/profiles/<id>.yaml`) under `/srv/localai`, within `budget_bytes` and while keeping `min_free_bytes` free.
- **Ark drive:** any ext4 filesystem labelled **`AI-ARK`**, mounted anywhere. The agent detects it from `/proc/self/mountinfo` and blkid (ark-sync.md §4.1.2). It gets the `ark-drive-full` profile under `<mount>/localai/`, with the same layout.
- When the drive is mounted, the agent can **serve directly from it**: models load from the drive and Kiwix includes its ZIMs. It can also **import** from it to internal disk without internet (`aictl ark import`).
- Sync writes to every mounted content root whenever it runs.

## Consequences
- **Good:** a brand-new machine plus the drive plus an Ubuntu USB stick gives a working node with no internet ([runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md)).
- **Cost:** the drive only syncs when it is plugged in. The readiness report shows its age, and the cloud warns when it is older than 30 days.

## Alternatives considered
- **Internal only:** a dead laptop takes its ark with it.
- **Drive only:** a laptop without the drive is useless offline.
- **exFAT drive:** no Unix permissions or symlinks. ext4 is required and documented, with a formatting runbook.
