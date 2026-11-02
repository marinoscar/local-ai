# Local AI: lab control and offline ark

This repository is the **runbook and source tree** for two things:

1. **The Local AI lab.** GPU nodes running local models (coding, chat, vision, agents), all controlled from **https://ai.marin.cr**, with one OpenAI-compatible API at `https://ai.marin.cr/v1`.
2. **The ark.** Every node, especially the laptops, keeps everything it needs to give you useful intelligence **with no internet at all**: models, an offline library (Wikipedia, medical, how-to, repair, survival), maps, voice and a kit to rebuild itself. A non-expert can operate it with the **ai-desktop** app or the built-in **Survival Portal**.

## In an emergency, read this first
➡️ **[docs/runbooks/01-survival-mode.md](docs/runbooks/01-survival-mode.md)**. A printed copy should be kept next to each laptop.

Quick version: turn the laptop on, double-click **AI Desktop** and press **Survival mode**. If the app doesn't open, use any web browser and go to **http://127.0.0.1:7070**.

## Where things are
| You want to… | Go to |
|---|---|
| Understand the whole system | [docs/architecture/overview.md](docs/architecture/overview.md) |
| Operate a node (runbooks) | [docs/runbooks/](docs/runbooks/) — start at [00-START-HERE](docs/runbooks/00-START-HERE.md) |
| See the build plan, epics and issues | [docs/ROADMAP.md](docs/ROADMAP.md) and [GitHub Issues](https://github.com/marinoscar/local-ai/issues) |
| Implement something (coding agents) | [AGENTS.md](AGENTS.md), then the issue, then its spec in [docs/specs/](docs/specs/) |
| Know why something is the way it is | [docs/decisions/](docs/decisions/) |
| See the original setup notes | [docs/source/](docs/source/) (redacted archive) |

## Status
Design approved 2026-11-02. Implementation is tracked as GitHub Epics E0–E9. See [ROADMAP](docs/ROADMAP.md).

> This repo is **public**. Lab-specific values (IPs, tailnet, zone IDs) are placeholders. Real values live in a local, git-ignored `inventory.local.yaml`. See [ADR-0007](docs/decisions/0007-public-repo-redaction.md).
