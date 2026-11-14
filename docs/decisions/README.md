# Architecture Decision Records

Each ADR records one decision, its context and the alternatives rejected. To change a decision, write a new ADR with `Status: Supersedes ADR-NNNN`. Never rewrite an accepted ADR, except to mark it superseded.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-agent-in-go.md) | The node agent and CLI are one static Go binary | Accepted |
| [0002](0002-llama-router-mode.md) | Model swapping uses llama.cpp router mode with agent-generated presets | Accepted |
| [0003](0003-outbound-only-cloud-link.md) | Cloud → node control goes through outbound pulls on the EAB worker-node plane | Accepted |
| [0004](0004-desktop-thin-shell.md) | ai-desktop is a thin Electron shell over the agent's local UI | Accepted |
| [0005](0005-ark-storage.md) | Ark content lives on internal disk (per-profile subset) and on an `AI-ARK` drive (full) | Accepted |
| [0006](0006-standalone-offline.md) | Offline nodes are standalone. There is no LAN mesh | Accepted |
| [0007](0007-public-repo-redaction.md) | The repo stays public, and lab inventory is redacted with placeholders | Accepted |
| [0008](0008-cloud-on-eab.md) | The cloud app is built on EnterpriseAppBase packages | Accepted |
| [0009](0009-optimized-for-humans.md) | Everything is optimized for humans: nothing to memorize, menus over flags, state first | Accepted |
| [0010](0010-the-agent-keeps-the-node-complete.md) | The agent keeps the node complete: it installs what the kit holds, safely, and one button does it on demand | Accepted |
| [0011](0011-full-control-from-the-app.md) | Full control from the app (the Dashboard in the Portal, built-in help); no PIN to leave survival mode at the computer, the PIN guards deletes | Accepted |

Template: copy [0000-template.md](0000-template.md).
