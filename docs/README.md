# Documentation map

| Folder | What's in it | Audience |
|---|---|---|
| [runbooks/](runbooks/) | Step-by-step operator procedures in plain language | Anyone operating a node or the lab |
| [architecture/](architecture/) | [overview](architecture/overview.md) (**canonical names**), [modes](architecture/modes.md), [security model](architecture/security-model.md), [lab inventory](architecture/lab-inventory.md) | Maintainers, coding agents |
| [specs/](specs/) | Component contracts that code is built against | Coding agents |
| [decisions/](decisions/) | ADRs | Maintainers |
| [source/](source/) | Redacted archive of the original gists and infra doc. History only; **don't follow these for new work** | Reference |
| [ROADMAP.md](ROADMAP.md) | Epics, issue map, dependency graph, spec→issue coverage | Everyone |

## Specs
| Spec | Component | Epic |
|---|---|---|
| [agent.md](specs/agent.md) | `aiagent` daemon and REST API | E1 |
| [aictl.md](specs/aictl.md) | CLI | E1 |
| [catalog.md](specs/catalog.md) | Models, content, profiles (YAML) | E2/E3 |
| [llama-router.md](specs/llama-router.md) | llama-server router, presets, builds, migration | E2 |
| [ark-sync.md](specs/ark-sync.md) | Scheduled sync, ark drive, readiness | E3 |
| [companion-services.md](specs/companion-services.md) | Kiwix, whisper, piper, maps, vision | E4 |
| [survival-portal.md](specs/survival-portal.md) | Offline big-button UI | E4 |
| [installer.md](specs/installer.md) | `install-node.sh` (online/offline), laptop hardening | E5 |
| [rebuild-kit.md](specs/rebuild-kit.md) | Offline rebuild materials | E5 |
| [cloud-app.md](specs/cloud-app.md) | ai.marin.cr (EAB) | E6 |
| [gateway.md](specs/gateway.md) | ai.marin.cr/v1 | E7 |
| [desktop.md](specs/desktop.md) | ai-desktop (Electron) | E8 |
| [drill.md](specs/drill.md) | Monthly self-test and manual drills | E9 |
| [doctor-codes.md](specs/doctor-codes.md) | Registry of all `LAI-*` codes | all |

## Writing rules
- Runbooks: numbered steps, exact commands or buttons, a "What you should see" after each step, and "If it doesn't work" with a doctor code. Write for someone who has never used a terminal. Spanish summaries are welcome for the survival runbook.
- Specs: follow the section format used in existing specs (purpose, interfaces, behaviour, failures, security, observability, doctor codes, test plan, implementation breakdown).
- Never restate canonical names. Link to [overview.md §3](architecture/overview.md#3-canonical-names-single-source-of-truth).
- Every relative link and `#anchor` must resolve: run `scripts/check-links.sh` (CI runs it on every PR; a weekly job also checks external links).
- Placeholders only for lab-specific values ([security-model.md §5](architecture/security-model.md#5-redaction-public-repo)).
