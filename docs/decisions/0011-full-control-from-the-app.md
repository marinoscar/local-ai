# ADR-0011: Full control from the app; no PIN to leave survival mode at the computer

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** owner

## Context
Local AI is the family's emergency system. The person most likely to operate it in a disaster is not the engineer who built it: it is a family member who has never used a terminal and **will not know the admin PIN**. [ADR-0009](0009-optimized-for-humans.md) already says every common task must be doable by reading the screen and choosing an option.

An audit of the app on 2026-11-14 (epic #492) found that this was not true:
- Outside survival mode the ai-desktop app shows `agent/ui/index.html`, a placeholder. The "Dashboard" that [survival-portal.md](../specs/survival-portal.md), [drill.md](../specs/drill.md) and [desktop.md](../specs/desktop.md) send people to was never specified or built.
- Leaving survival mode works only from the tray icon (missing on some desktops, `LAI-DSK-013`), and it asks for the PIN ([modes.md](../architecture/modes.md) §2.4). A family member could switch to survival mode and then be unable to switch back.
- Most operations of `aictl` (sync, verify, import, drives and safe eject, models, health checks, logs, drill, updates, kit, backup) have no button; help is hard to find and tells people to type commands.

The PIN exists to stop a destructive mistake and to keep a stranger with a browser from changing the node. Leaving survival mode destroys nothing: models, content and settings stay; it only turns the normal services back on. And the CLI already lets any member of `localai-operators` leave survival without a PIN (modes.md §2.4): the desktop app runs as that same person, with the same agent token.

## Decision
1. **Everything from the app.** Every operator capability of `aictl` is reachable from the ai-desktop app by a non-expert, offline, in English and Spanish. The terminal stays the expert path and never the only path. The screens are specified in [dashboard.md](../specs/dashboard.md).
2. **One app.** The Dashboard is built by extending the Survival Portal SPA (`agent/ui`): Home shows the state first, then the big buttons, then **Manage this computer** (Mode, Offline content and drives, AI models, Health, System) and **Help**. The same app is shown in every mode; `/` is no longer a placeholder.
3. **No PIN to leave survival mode at the computer.** `POST /mode` out of survival accepts an **operator** principal: the agent token (the desktop app), a session made from it, or a socket peer in `localai-operators`. A browser that has only the Portal session (`lai_portal`) and the cloud still need the PIN, so a stranger on the machine's browser cannot do it. The tray stops asking for the desktop PIN to leave survival. This supersedes the sentence "Switching *out of* survival requires the PIN in the desktop/UI" of modes.md §2.4.
4. **The PIN guards deletes.** Deleting models or content, formatting or pruning a drive keep needing the PIN (AGENTS.md rule 3), and so do the lab-only settings a family member never needs (API key rotation, cloud link, profile). The app says why before it asks.
5. **Help is built in.** A guide ("What this app can do") is one tap from Home, the desktop menu and the tray, works offline, and names the exact buttons, never a command.
6. **English by default**, Spanish one tap away; the choice is remembered.

## Consequences
- **Good:** a family member can do every task, including leaving survival mode, without the engineer, a terminal or the PIN; the app explains itself.
- **Cost:** anyone who can use the logged-in desktop session can leave survival mode. That person could already do it with `aictl`, and leaving survival loses nothing. Deletes stay behind the PIN.
- **Work:** the epic #492 issues #493 to #501. Until they land, the survival runbook keeps describing the tray and the terminal paths.
- **Specs changed with this ADR:** modes.md §2.4, agent.md (auth of `POST /mode`), survival-portal.md §1 (admin tasks live in the Manage area), desktop.md §4.7 (leaving survival), new dashboard.md.

## Alternatives considered
- **A separate dashboard app next to the Portal:** rejected by the owner. Two apps to learn, and in an emergency the person would meet a screen they had never seen.
- **Keep the PIN for leaving survival and teach the family the PIN:** rejected by the owner. A forgotten PIN must not trap a family in survival mode, and leaving survival is not destructive.
- **No PIN at all:** rejected. Deleting the only copy of a library or a model must stay hard to do by accident (rule 3).
